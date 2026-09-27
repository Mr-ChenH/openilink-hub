import { boundedFinalText } from "./event-policy.js";
import type { ServiceConfig } from "./config.js";
import { sessionKey } from "./config.js";
import { RunStore } from "./run-store.js";
import type { AgentRuntime, RunRecord, RuntimeHandle } from "./types.js";

class TimeoutError extends Error {}

export class RunScheduler {
  readonly #queue: RunRecord[] = [];
  readonly #active = new Map<string, { run: RunRecord; handle?: RuntimeHandle }>();
  readonly #activeConversations = new Set<string>();

  constructor(
    private readonly store: RunStore,
    private readonly runtime: AgentRuntime,
    private readonly config: Pick<ServiceConfig, "maxConcurrency" | "maxQueue" | "defaultTimeoutMs" | "maxTimeoutMs">,
  ) {}

  canEnqueue(): boolean {
    return this.#queue.length < this.config.maxQueue;
  }

  enqueue(run: RunRecord): void {
    if (!this.canEnqueue()) throw new Error("run queue is full");
    this.#queue.push(run);
    this.#pump();
  }

  async cancel(run: RunRecord): Promise<void> {
    if (run.status === "queued") {
      const index = this.#queue.indexOf(run);
      if (index >= 0) this.#queue.splice(index, 1);
      this.#finishCancelled(run);
      return;
    }
    if (run.status === "running") {
      const active = this.#active.get(run.id);
      this.#finishCancelled(run);
      await active?.handle?.abort();
    }
  }

  #finishCancelled(run: RunRecord): void {
    if (run.status === "completed" || run.status === "failed" || run.status === "cancelled" || run.status === "interrupted") return;
    this.store.finish(run, "cancelled", "run.cancelled");
  }

  #pump(): void {
    while (this.#active.size < this.config.maxConcurrency) {
      const index = this.#queue.findIndex((run) => !this.#activeConversations.has(sessionKey(run.request.conversation_id, run.request.session_epoch)));
      if (index < 0) return;
      const [run] = this.#queue.splice(index, 1);
      if (!run) return;
      const conversation = sessionKey(run.request.conversation_id, run.request.session_epoch);
      this.#active.set(run.id, { run });
      this.#activeConversations.add(conversation);
      void this.#execute(run, conversation);
    }
  }

  async #execute(run: RunRecord, conversation: string): Promise<void> {
    run.status = "running";
    run.startedAt = new Date().toISOString();
    this.store.append(run, "run.started");
    const requestedTimeout = run.request.limits?.timeout_ms ?? this.config.defaultTimeoutMs;
    const timeoutMs = Math.min(requestedTimeout, this.config.maxTimeoutMs);
    let timer: NodeJS.Timeout | undefined;
    try {
      const handle = await this.runtime.start(run.request, {
        event: (type, data = {}) => {
          if (run.status === "running") this.store.append(run, type, data);
        },
      });
      const active = this.#active.get(run.id);
      if (active) active.handle = handle;
      if (this.store.get(run.id)?.status === "cancelled") await handle.abort();
      const timeout = new Promise<never>((_, reject) => {
        timer = setTimeout(() => {
          void handle.abort();
          reject(new TimeoutError("run timed out"));
        }, timeoutMs);
      });
      const text = boundedFinalText(await Promise.race([handle.result, timeout]));
      if (run.status === "running") {
        this.store.finish(run, "completed", "run.completed", { text }, { result: { text } });
      }
    } catch (error) {
      if (run.status === "running") {
        const timeout = error instanceof TimeoutError;
        const runError = { code: timeout ? "timeout" : "runtime_error", message: timeout ? "run timed out" : "agent runtime failed" };
        this.store.finish(run, "failed", "run.failed", runError, { error: runError });
      }
    } finally {
      if (timer) clearTimeout(timer);
      this.#active.delete(run.id);
      this.#activeConversations.delete(conversation);
      this.#pump();
    }
  }
}
