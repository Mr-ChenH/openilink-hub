import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";
import { sanitizeEventData } from "./event-policy.js";
import type { CreateRunRequest, RunEvent, RunRecord } from "./types.js";

export class ConflictError extends Error {}

function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (value !== null && typeof value === "object") {
    return `{${Object.entries(value as Record<string, unknown>)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([key, item]) => `${JSON.stringify(key)}:${canonical(item)}`)
      .join(",")}}`;
  }
  return JSON.stringify(value);
}

export function requestHash(request: CreateRunRequest): string {
  return createHash("sha256").update(canonical(request)).digest("hex");
}

export class RunStore {
  readonly #runs = new Map<string, RunRecord>();
  readonly #listeners = new Map<string, Set<(event: RunEvent) => void>>();

  constructor(private readonly eventLimit: number, private readonly filePath?: string) {
    if (!filePath) return;
    mkdirSync(dirname(filePath), { recursive: true });
    try {
      const records = JSON.parse(readFileSync(filePath, "utf8")) as RunRecord[];
      for (const run of records) this.#runs.set(run.id, run);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    }
    let changed = false;
    for (const run of this.#runs.values()) {
      if (run.status === "queued" || run.status === "running") {
        run.status = "interrupted";
        run.finishedAt = new Date().toISOString();
        run.error = { code: "sidecar_restarted", message: "agent sidecar restarted during execution" };
        const data = sanitizeEventData("run.interrupted", run.error) ?? {};
        const event: RunEvent = {
          seq: (run.events.at(-1)?.seq ?? 0) + 1,
          run_id: run.id,
          type: "run.interrupted",
          timestamp: run.finishedAt,
          data,
        };
        run.events.push(event);
        if (run.events.length > this.eventLimit) run.events.splice(0, run.events.length - this.eventLimit);
        changed = true;
      }
      changed = this.#scrubTerminal(run) || changed;
    }
    if (changed) this.#persist();
  }

  create(request: CreateRunRequest): { run: RunRecord; created: boolean } {
    const hash = requestHash(request);
    const existing = this.#runs.get(request.run_id);
    if (existing) {
      if (existing.requestHash !== hash) throw new ConflictError("run_id already exists with a different payload");
      return { run: existing, created: false };
    }
    const run: RunRecord = {
      id: request.run_id,
      request,
      requestHash: hash,
      status: "queued",
      createdAt: new Date().toISOString(),
      events: [],
    };
    this.#runs.set(run.id, run);
    this.#persist();
    return { run, created: true };
  }

  get(id: string): RunRecord | undefined {
    return this.#runs.get(id);
  }

  append(run: RunRecord, type: RunEvent["type"], data: Record<string, unknown> = {}): RunEvent | undefined {
    const sanitized = sanitizeEventData(type, data);
    if (!sanitized) return undefined;
    const event: RunEvent = {
      seq: (run.events.at(-1)?.seq ?? 0) + 1,
      run_id: run.id,
      type,
      timestamp: new Date().toISOString(),
      data: sanitized,
    };
    run.events.push(event);
    if (run.events.length > this.eventLimit) run.events.splice(0, run.events.length - this.eventLimit);
    this.#persist();
    for (const listener of this.#listeners.get(run.id) ?? []) listener(event);
    return event;
  }

  finish(
    run: RunRecord,
    status: "completed" | "failed" | "cancelled",
    type: "run.completed" | "run.failed" | "run.cancelled",
    data: Record<string, unknown> = {},
    outcome: Pick<RunRecord, "result" | "error"> = {},
  ): RunEvent | undefined {
    const sanitized = sanitizeEventData(type, data);
    if (!sanitized) return undefined;
    run.status = status;
    run.finishedAt = new Date().toISOString();
    if (outcome.result) run.result = outcome.result;
    else delete run.result;
    if (outcome.error) run.error = outcome.error;
    else delete run.error;
    this.#scrubTerminal(run);
    const event: RunEvent = {
      seq: (run.events.at(-1)?.seq ?? 0) + 1,
      run_id: run.id,
      type,
      timestamp: run.finishedAt,
      data: sanitized,
    };
    run.events.push(event);
    if (run.events.length > this.eventLimit) run.events.splice(0, run.events.length - this.eventLimit);
    this.#persist();
    for (const listener of this.#listeners.get(run.id) ?? []) listener(event);
    return event;
  }

  #scrubTerminal(run: RunRecord): boolean {
    if (run.status !== "completed" && run.status !== "failed" && run.status !== "cancelled" && run.status !== "interrupted") return false;
    const alreadyScrubbed = run.request.tool_capability === undefined
      && run.request.input.message_id === ""
      && run.request.input.text === ""
      && run.request.tools.length === 0;
    if (alreadyScrubbed) return false;
    const { tool_capability: _discardedCapability, ...retained } = run.request;
    run.request = {
      ...retained,
      input: { message_id: "", text: "" },
      tools: [],
    };
    return true;
  }

  #persist(): void {
    if (!this.filePath) return;
    const temporary = `${this.filePath}.tmp`;
    writeFileSync(temporary, JSON.stringify([...this.#runs.values()]), { encoding: "utf8", mode: 0o600 });
    renameSync(temporary, this.filePath);
  }

  observe(id: string, afterSeq: number, listener: (event: RunEvent) => void): { replay: RunEvent[]; close(): void } {
    const run = this.#runs.get(id);
    if (!run) throw new Error("run not found");
    let listeners = this.#listeners.get(id);
    if (!listeners) {
      listeners = new Set();
      this.#listeners.set(id, listeners);
    }
    listeners.add(listener);
    return {
      replay: run.events.filter((event) => event.seq > afterSeq),
      close: () => {
        listeners?.delete(listener);
        if (listeners?.size === 0) this.#listeners.delete(id);
      },
    };
  }
}

export function publicRun(run: RunRecord): Record<string, unknown> {
  return {
    run_id: run.id,
    status: run.status,
    created_at: run.createdAt,
    ...(run.startedAt ? { started_at: run.startedAt } : {}),
    ...(run.finishedAt ? { finished_at: run.finishedAt } : {}),
    ...(run.result ? { result: run.result, text: run.result.text } : {}),
    ...(run.error ? { error: run.error.message, error_code: run.error.code } : {}),
  };
}
