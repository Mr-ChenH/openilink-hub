import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { test } from "node:test";
import { createHubTools } from "../src/hub-tools.js";
import { DEFAULT_PROMPT_VERSION, isolatedResourceLoader } from "../src/resource-loader.js";
import type { CreateRunRequest } from "../src/types.js";

test("isolated resource loader exposes no host-discovered resources", async () => {
  const loader = isolatedResourceLoader();
  await loader.reload();
  assert.deepEqual(loader.getExtensions().extensions, []);
  assert.deepEqual(loader.getSkills().skills, []);
  assert.deepEqual(loader.getPrompts().prompts, []);
  assert.deepEqual(loader.getThemes().themes, []);
  assert.deepEqual(loader.getAgentsFiles().agentsFiles, []);
  assert.deepEqual(loader.getAppendSystemPrompt(), []);
  assert.deepEqual(loader.getSystemPromptSource(), { path: `builtin:${DEFAULT_PROMPT_VERSION}` });
  assert.throws(() => isolatedResourceLoader("../../host-file"), /unknown system prompt version/);
});

test("dynamic tool calls only the fixed Hub broker with service and run credentials", async () => {
  let received: { url: string | undefined; authorization: string | undefined; capability: string | undefined; body: unknown } = {
    url: undefined,
    authorization: undefined,
    capability: undefined,
    body: undefined,
  };
  const hub = createServer(async (request, response) => {
    const chunks: Buffer[] = [];
    for await (const chunk of request) chunks.push(Buffer.from(chunk as Uint8Array));
    received = {
      url: request.url,
      authorization: request.headers.authorization,
      capability: request.headers["x-agent-run-capability"] as string,
      body: JSON.parse(Buffer.concat(chunks).toString("utf8")),
    };
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({ status: "succeeded", output: { count: 2 }, text: "Found two records" }));
  });
  hub.listen(0, "127.0.0.1");
  await once(hub, "listening");
  const address = hub.address() as AddressInfo;
  const request: CreateRunRequest = {
    protocol_version: 1,
    run_id: "run-tool",
    conversation_id: "conversation",
    session_epoch: 1,
    input: { message_id: "message", text: "lookup" },
    model_profile: "default",
    system_prompt_version: "messaging-v1",
    catalog_version: "catalog-v1",
    tool_capability: "run-secret",
    tools: [{ name: "app_1_lookup", description: "Lookup", parameters: { type: "object", properties: {} } }],
  };
  const lifecycle: string[] = [];
  const [tool] = createHubTools({
    request,
    hubBaseUrl: `http://127.0.0.1:${address.port}/internal/agent/v1`,
    serviceToken: "service-secret",
    timeoutMs: 1_000,
    maxToolCalls: 1,
    onStarted: () => lifecycle.push("started"),
    onCompleted: (_id, _name, error) => lifecycle.push(error ? "error" : "completed"),
  });
  assert.ok(tool);
  try {
    const result = await tool.execute("call-1", { query: "value" }, undefined, undefined, undefined as never);
    assert.deepEqual(result.content, [{ type: "text", text: "Found two records" }]);
    assert.deepEqual(lifecycle, ["started", "completed"]);
    assert.equal(received.url, "/internal/agent/v1/runs/run-tool/tool-calls");
    assert.equal(received.authorization, "Bearer service-secret");
    assert.equal(received.capability, "run-secret");
    assert.deepEqual(received.body, {
      call_id: "call-1",
      tool_name: "app_1_lookup",
      catalog_version: "catalog-v1",
      arguments: { query: "value" },
    });
    await assert.rejects(() => tool.execute("call-2", {}, undefined, undefined, undefined as never), /maximum tool call count/);
  } finally {
    hub.closeAllConnections();
    await new Promise<void>((resolve) => hub.close(() => resolve()));
  }
});
