import assert from "node:assert/strict";
import { once } from "node:events";
import type { AddressInfo } from "node:net";
import { afterEach, test } from "node:test";
import type { ServiceConfig } from "../src/config.js";
import { createApp } from "../src/server.js";
import type { AgentRuntime, CreateRunRequest, RuntimeCallbacks, RuntimeHandle } from "../src/types.js";

class MockRuntime implements AgentRuntime {
  readonly started: Array<{ request: CreateRunRequest; callbacks: RuntimeCallbacks }> = [];
  readonly pending = new Map<string, { resolve(text: string): void; reject(error: Error): void; aborted: boolean }>();

  async ready(): Promise<void> {}

  async start(request: CreateRunRequest, callbacks: RuntimeCallbacks): Promise<RuntimeHandle> {
    this.started.push({ request, callbacks });
    let resolve!: (text: string) => void;
    let reject!: (error: Error) => void;
    const result = new Promise<string>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    const entry = { resolve, reject, aborted: false };
    this.pending.set(request.run_id, entry);
    return {
      result,
      abort: async () => {
        if (!entry.aborted) {
          entry.aborted = true;
          reject(new Error("aborted"));
        }
      },
    };
  }

  finish(id: string, text = "done"): void {
    this.pending.get(id)?.resolve(text);
  }
}

const servers: Array<ReturnType<typeof createApp>["server"]> = [];

function config(overrides: Partial<ServiceConfig> = {}): ServiceConfig {
  return {
    host: "127.0.0.1",
    port: 0,
    serviceToken: "test-token",
    hubBaseUrl: "http://hub.invalid/internal/agent/v1",
    agentDir: "/tmp/pi-agent-test/config",
    sessionDir: "/tmp/pi-agent-test/sessions",
    workspaceDir: "/tmp/pi-agent-test/workspace",
    modelProfiles: { default: { provider: "test", model: "test" } },
    maxConcurrency: 2,
    maxQueue: 20,
    defaultTimeoutMs: 10_000,
    maxTimeoutMs: 10_000,
    defaultMaxToolCalls: 8,
    toolTimeoutMs: 1_000,
    eventLimit: 100,
    ...overrides,
  };
}

async function app(runtime = new MockRuntime(), overrides: Partial<ServiceConfig> = {}) {
  const created = createApp(config(overrides), runtime);
  created.server.listen(0, "127.0.0.1");
  await once(created.server, "listening");
  servers.push(created.server);
  const address = created.server.address() as AddressInfo;
  return { ...created, runtime, base: `http://127.0.0.1:${address.port}` };
}

function run(id: string, conversation = "conv-a", text = "hello"): CreateRunRequest {
  return {
    protocol_version: 1,
    run_id: id,
    conversation_id: conversation,
    session_epoch: 1,
    input: { message_id: `msg-${id}`, text },
    model_profile: "default",
    catalog_version: "cat-1",
    tools: [],
  };
}

function headers(): HeadersInit {
  return { authorization: "Bearer test-token", "content-type": "application/json" };
}

async function create(base: string, value: CreateRunRequest): Promise<Response> {
  return fetch(`${base}/v1/runs`, { method: "POST", headers: headers(), body: JSON.stringify(value) });
}

async function waitFor(check: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.fail("condition was not reached");
}

afterEach(async () => {
  await Promise.all(servers.splice(0).map((server) => new Promise<void>((resolve) => {
    server.closeAllConnections();
    server.close(() => resolve());
  })));
});

test("health is public while run APIs require service authentication", async () => {
  const { base } = await app();
  assert.equal((await fetch(`${base}/healthz`)).status, 200);
  assert.equal((await fetch(`${base}/readyz`)).status, 200);
  assert.equal((await fetch(`${base}/v1/runs/missing`)).status, 401);
});

test("run creation is idempotent and rejects payload changes", async () => {
  const { base, runtime } = await app();
  const request = run("run-one");
  assert.equal((await create(base, request)).status, 202);
  assert.equal((await create(base, request)).status, 202);
  assert.equal(runtime.started.length, 1);
  const changed = structuredClone(request);
  changed.input.text = "different";
  const conflict = await create(base, changed);
  assert.equal(conflict.status, 409);
  assert.equal((await conflict.json() as { error: { code: string } }).error.code, "idempotency_conflict");
  runtime.finish("run-one");
});

test("scheduler serializes a conversation while running other conversations", async () => {
  const { base, runtime } = await app(undefined, { maxConcurrency: 2 });
  await create(base, run("run-a1", "conversation-a"));
  await create(base, run("run-a2", "conversation-a"));
  await create(base, run("run-b1", "conversation-b"));
  await waitFor(() => runtime.started.length === 2);
  assert.deepEqual(runtime.started.map(({ request }) => request.run_id), ["run-a1", "run-b1"]);
  runtime.finish("run-a1");
  await waitFor(() => runtime.started.length === 3);
  assert.equal(runtime.started[2]?.request.run_id, "run-a2");
  runtime.finish("run-a2");
  runtime.finish("run-b1");
});

test("cancel is idempotent for queued and active runs", async () => {
  const { base, runtime } = await app(undefined, { maxConcurrency: 1 });
  await create(base, run("active", "conversation-a"));
  await create(base, run("queued", "conversation-b"));
  const queuedCancel = await fetch(`${base}/v1/runs/queued/cancel`, { method: "POST", headers: headers() });
  assert.equal((await queuedCancel.json() as { status: string }).status, "cancelled");
  const activeCancel = await fetch(`${base}/v1/runs/active/cancel`, { method: "POST", headers: headers() });
  assert.equal((await activeCancel.json() as { status: string }).status, "cancelled");
  const repeated = await fetch(`${base}/v1/runs/active/cancel`, { method: "POST", headers: headers() });
  assert.equal((await repeated.json() as { status: string }).status, "cancelled");
  assert.equal(runtime.pending.get("active")?.aborted, true);
  assert.equal(runtime.started.some(({ request }) => request.run_id === "queued"), false);
});

test("SSE replays events after Last-Event-ID and closes at terminal state", async () => {
  const { base, runtime } = await app();
  await create(base, run("stream"));
  await waitFor(() => runtime.started.length === 1);
  runtime.started[0]?.callbacks.event("text.delta", { text: "hello" });
  runtime.finish("stream", "hello");
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const response = await fetch(`${base}/v1/runs/stream`, { headers: headers() });
    if ((await response.json() as { status: string }).status === "completed") break;
    await new Promise((resolve) => setTimeout(resolve, 5));
    if (attempt === 99) assert.fail("run did not complete");
  }
  const response = await fetch(`${base}/v1/runs/stream/events`, {
    headers: { authorization: "Bearer test-token", "last-event-id": "1" },
  });
  const text = await response.text();
  assert.doesNotMatch(text, /event: run.started/);
  assert.match(text, /event: text.delta/);
  assert.match(text, /event: run.completed/);
  assert.match(text, /"text":"hello"/);
});

test("validation requires capability for dynamic tools and preserves raw slash text", async () => {
  const { base, runtime } = await app();
  const request = run("tool-run", "conversation-a", "/not-an-extension command");
  request.tools = [{ name: "app_1_lookup", description: "Lookup a record", parameters: { type: "object", properties: {} } }];
  assert.equal((await create(base, request)).status, 400);
  request.tool_capability = "short-lived-capability";
  assert.equal((await create(base, request)).status, 202);
  await waitFor(() => runtime.started.length === 1);
  assert.equal(runtime.started[0]?.request.input.text, "/not-an-extension command");
  runtime.finish("tool-run");
});
