import { createHash, timingSafeEqual } from "node:crypto";
import { createServer as nodeCreateServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { ServiceConfig } from "./config.js";
import { ConflictError, publicRun, RunStore } from "./run-store.js";
import { RunScheduler } from "./scheduler.js";
import type { AgentRuntime, RunEvent, RunRecord } from "./types.js";
import { parseRunRequest, ValidationError } from "./validation.js";

const terminal = new Set(["completed", "failed", "cancelled"]);

function json(response: ServerResponse, status: number, body: unknown): void {
  response.writeHead(status, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" });
  response.end(JSON.stringify(body));
}

async function body(request: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of request) {
    const value = Buffer.from(chunk as Uint8Array);
    size += value.length;
    if (size > 1024 * 1024) throw new ValidationError("request body exceeds 1 MiB");
    chunks.push(value);
  }
  try {
    return JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } catch {
    throw new ValidationError("request body must be valid JSON");
  }
}

function authenticated(request: IncomingMessage, token: string): boolean {
  const supplied = request.headers.authorization?.match(/^Bearer (.+)$/)?.[1] ?? "";
  const expectedHash = createHash("sha256").update(token).digest();
  const suppliedHash = createHash("sha256").update(supplied).digest();
  return timingSafeEqual(expectedHash, suppliedHash);
}

function sendEvent(response: ServerResponse, event: RunEvent): void {
  response.write(`id: ${event.seq}\nevent: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`);
}

function routeId(pathname: string, suffix = ""): string | undefined {
  const match = pathname.match(new RegExp(`^/v1/runs/([^/]+)${suffix}$`));
  if (!match?.[1]) return undefined;
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return undefined;
  }
}

export function createApp(config: ServiceConfig, runtime: AgentRuntime) {
  const store = new RunStore(config.eventLimit);
  const scheduler = new RunScheduler(store, runtime, config);

  const server = nodeCreateServer(async (request, response) => {
    try {
      const url = new URL(request.url ?? "/", "http://sidecar.local");
      if (request.method === "GET" && url.pathname === "/healthz") return json(response, 200, { status: "ok" });
      if (request.method === "GET" && url.pathname === "/readyz") {
        try {
          await runtime.ready();
          return json(response, 200, { status: "ready" });
        } catch {
          return json(response, 503, { status: "not_ready" });
        }
      }
      if (!authenticated(request, config.serviceToken)) return json(response, 401, { error: { code: "unauthorized", message: "service authentication required" } });

      if (request.method === "POST" && url.pathname === "/v1/runs") {
        const parsed = parseRunRequest(await body(request));
        if (!store.get(parsed.run_id) && !scheduler.canEnqueue()) {
          return json(response, 503, { error: { code: "queue_full", message: "run queue is full" } });
        }
        const { run, created } = store.create(parsed);
        if (created) scheduler.enqueue(run);
        return json(response, 202, publicRun(run));
      }

      const cancelId = routeId(url.pathname, "/cancel");
      if (request.method === "POST" && cancelId) {
        const run = store.get(cancelId);
        if (!run) return json(response, 404, { error: { code: "not_found", message: "run not found" } });
        await scheduler.cancel(run);
        return json(response, 200, publicRun(run));
      }

      const eventsId = routeId(url.pathname, "/events");
      if (request.method === "GET" && eventsId) {
        const run = store.get(eventsId);
        if (!run) return json(response, 404, { error: { code: "not_found", message: "run not found" } });
        const header = request.headers["last-event-id"];
        const afterSeq = typeof header === "string" && /^\d+$/.test(header) ? Number(header) : 0;
        response.writeHead(200, {
          "content-type": "text/event-stream; charset=utf-8",
          "cache-control": "no-cache, no-transform",
          connection: "keep-alive",
          "x-accel-buffering": "no",
        });
        response.write("retry: 2000\n\n");
        let closed = false;
        const close = () => {
          if (closed) return;
          closed = true;
          observation.close();
          clearInterval(keepAlive);
          if (!response.writableEnded) response.end();
        };
        const observation = store.observe(eventsId, afterSeq, (event) => {
          sendEvent(response, event);
          if (terminal.has(run.status)) close();
        });
        const keepAlive = setInterval(() => response.write(": keep-alive\n\n"), 15_000);
        request.on("close", close);
        for (const event of observation.replay) sendEvent(response, event);
        if (terminal.has(run.status)) close();
        return;
      }

      const runId = routeId(url.pathname);
      if (request.method === "GET" && runId) {
        const run = store.get(runId);
        if (!run) return json(response, 404, { error: { code: "not_found", message: "run not found" } });
        return json(response, 200, publicRun(run));
      }
      return json(response, 404, { error: { code: "not_found", message: "route not found" } });
    } catch (error) {
      if (error instanceof ValidationError) return json(response, 400, { error: { code: "invalid_request", message: error.message } });
      if (error instanceof ConflictError) return json(response, 409, { error: { code: "idempotency_conflict", message: error.message } });
      return json(response, 500, { error: { code: "internal_error", message: "internal server error" } });
    }
  });

  return { server, store, scheduler };
}

export function isTerminal(run: RunRecord): boolean {
  return terminal.has(run.status);
}
