import { createHash } from "node:crypto";
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

  constructor(private readonly eventLimit: number) {}

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
    return { run, created: true };
  }

  get(id: string): RunRecord | undefined {
    return this.#runs.get(id);
  }

  append(run: RunRecord, type: RunEvent["type"], data: Record<string, unknown> = {}): RunEvent {
    const event: RunEvent = {
      seq: (run.events.at(-1)?.seq ?? 0) + 1,
      run_id: run.id,
      type,
      timestamp: new Date().toISOString(),
      data,
    };
    run.events.push(event);
    if (run.events.length > this.eventLimit) run.events.splice(0, run.events.length - this.eventLimit);
    for (const listener of this.#listeners.get(run.id) ?? []) listener(event);
    return event;
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
    ...(run.result ? { result: run.result } : {}),
    ...(run.error ? { error: run.error } : {}),
  };
}
