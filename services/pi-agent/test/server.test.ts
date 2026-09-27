import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { once } from "node:events";
import type { AddressInfo } from "node:net";
import { afterEach, test } from "node:test";
import type { ServiceConfig } from "../src/config.js";
import { createApp } from "../src/server.js";
import { RunStore } from "../src/run-store.js";
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
const temporaryDirectories: string[] = [];

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
  const sessionDir = overrides.sessionDir ?? mkdtempSync(join(tmpdir(), "pi-agent-test-"));
  if (!overrides.sessionDir) temporaryDirectories.push(sessionDir);
  const created = createApp(config({ ...overrides, sessionDir }), runtime);
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
    system_prompt_version: "messaging-v1",
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
  for (const directory of temporaryDirectories.splice(0)) rmSync(directory, { recursive: true, force: true });
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
  const { base, runtime, store } = await app();
  await create(base, run("stream"));
  await waitFor(() => runtime.started.length === 1);
  runtime.started[0]?.callbacks.event("text.delta", { text: "hello", reasoning: "must-not-persist", service_token: "secret" });
  const emitUntrusted = runtime.started[0]?.callbacks.event as ((type: string, data: Record<string, unknown>) => void) | undefined;
  emitUntrusted?.("provider.reasoning", { text: "hidden chain of thought" });
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
  assert.doesNotMatch(text, /must-not-persist|service_token|secret|provider\.reasoning|hidden chain of thought/);
  const stored = store.get("stream");
  assert.equal(stored?.request.system_prompt_version, "messaging-v1");
  assert.equal(stored?.request.input.text, "");
  assert.equal(stored?.request.tool_capability, undefined);
  assert.deepEqual(stored?.request.tools, []);
});

test("restart persists identity and exposes in-flight work as interrupted without re-execution", async () => {
  const sessionDir = mkdtempSync(join(tmpdir(), "pi-agent-restart-"));
  temporaryDirectories.push(sessionDir);
  const first = await app(new MockRuntime(), { sessionDir });
  const request = run("restart-run");
  assert.equal((await create(first.base, request)).status, 202);
  await waitFor(() => first.runtime.started.length === 1);

  first.server.closeAllConnections();
  await new Promise<void>((resolve) => first.server.close(() => resolve()));
  servers.splice(servers.indexOf(first.server), 1);

  const secondRuntime = new MockRuntime();
  const second = await app(secondRuntime, { sessionDir });
  const recovered = await fetch(`${second.base}/v1/runs/restart-run`, { headers: headers() });
  const recoveredBody = await recovered.json() as { status: string; error_code: string };
  assert.equal(recoveredBody.status, "interrupted");
  assert.equal(recoveredBody.error_code, "sidecar_restarted");
  const persisted = second.store.get("restart-run");
  assert.equal(persisted?.request.input.text, "");
  assert.equal(persisted?.request.tool_capability, undefined);
  assert.deepEqual(persisted?.request.tools, []);
  assert.equal((await create(second.base, request)).status, 202);
  assert.equal(secondRuntime.started.length, 0);

  const events = await fetch(`${second.base}/v1/runs/restart-run/events`, { headers: headers() });
  assert.match(await events.text(), /event: run.interrupted/);

  // Release the first process's mock promise so the in-process crash simulation
  // leaves no scheduler work behind after the test.
  first.runtime.finish("restart-run", "ignored");
  await new Promise((resolve) => setTimeout(resolve, 20));
});

test("terminal persistence scrubs secrets atomically and repairs legacy terminal rows", () => {
  const sessionDir = mkdtempSync(join(tmpdir(), "pi-agent-persist-"));
  temporaryDirectories.push(sessionDir);
  const filePath = join(sessionDir, "runs.json");
  const request = run("persisted-terminal", "conversation-a", "sensitive input");
  request.input.message_id = "sensitive-message-id";
  request.tool_capability = "sensitive-capability";
  request.tools = [{ name: "secret_tool", description: "sensitive tool catalog", parameters: {} }];

  const store = new RunStore(100, filePath);
  const { run: record } = store.create(request);
  store.finish(record, "completed", "run.completed", { text: "done" }, { result: { text: "done" } });
  const persisted = readFileSync(filePath, "utf8");
  assert.doesNotMatch(persisted, /sensitive input|sensitive-message-id|sensitive-capability|secret_tool|sensitive tool catalog/);

  const legacy = JSON.parse(persisted) as Array<Record<string, unknown>>;
  const legacyRequest = legacy[0]?.request as CreateRunRequest;
  legacyRequest.input = { message_id: "legacy-message", text: "legacy-input" };
  legacyRequest.tool_capability = "legacy-capability";
  legacyRequest.tools = [{ name: "legacy_tool", description: "legacy catalog", parameters: {} }];
  writeFileSync(filePath, JSON.stringify(legacy), "utf8");

  new RunStore(100, filePath);
  const repaired = readFileSync(filePath, "utf8");
  assert.doesNotMatch(repaired, /legacy-message|legacy-input|legacy-capability|legacy_tool|legacy catalog/);
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
  assert.equal(runtime.started[0]?.request.system_prompt_version, "messaging-v1");
  runtime.finish("tool-run");
});
