import type { ToolDefinition } from "@earendil-works/pi-coding-agent";
import type { TSchema } from "typebox";
import type { CreateRunRequest } from "./types.js";

interface HubToolResponse {
  status?: string;
  output?: unknown;
  text?: string;
  error?: { code?: string; message?: string };
}

async function readLimited(response: Response, limit: number): Promise<string> {
  const reader = response.body?.getReader();
  if (!reader) return "";
  const decoder = new TextDecoder();
  let result = "";
  let size = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done) return result + decoder.decode();
    size += value.byteLength;
    if (size > limit) {
      await reader.cancel();
      throw new Error("Hub tool response exceeds 32 KiB");
    }
    result += decoder.decode(value, { stream: true });
  }
}

export function createHubTools(options: {
  request: CreateRunRequest;
  hubBaseUrl: string;
  serviceToken: string;
  timeoutMs: number;
  maxToolCalls: number;
  onStarted(callId: string, name: string): void;
  onCompleted(callId: string, name: string, isError: boolean): void;
}): ToolDefinition[] {
  let calls = 0;
  return options.request.tools.map((tool) => ({
    name: tool.name,
    label: tool.name,
    description: tool.description,
    parameters: tool.parameters as TSchema,
    executionMode: "sequential",
    execute: async (toolCallId, params, signal) => {
      calls += 1;
      if (calls > options.maxToolCalls) throw new Error("maximum tool call count exceeded");
      options.onStarted(toolCallId, tool.name);
      const timeout = AbortSignal.timeout(options.timeoutMs);
      const combined = signal ? AbortSignal.any([signal, timeout]) : timeout;
      let isError = true;
      try {
        const response = await fetch(
          `${options.hubBaseUrl}/runs/${encodeURIComponent(options.request.run_id)}/tool-calls`,
          {
            method: "POST",
            headers: {
              authorization: `Bearer ${options.serviceToken}`,
              "content-type": "application/json",
              "x-agent-run-capability": options.request.tool_capability ?? "",
            },
            body: JSON.stringify({
              call_id: toolCallId,
              tool_name: tool.name,
              catalog_version: options.request.catalog_version,
              arguments: params,
            }),
            signal: combined,
          },
        );
        const raw = await readLimited(response, 32 * 1024);
        let body: HubToolResponse;
        try {
          body = raw ? (JSON.parse(raw) as HubToolResponse) : {};
        } catch {
          throw new Error(`Hub returned invalid JSON (${response.status})`);
        }
        if (!response.ok || body.status === "failed") {
          throw new Error(body.error?.message ?? `Hub tool request failed (${response.status})`);
        }
        isError = false;
        const text = body.text ?? (body.output === undefined ? "Tool completed successfully" : JSON.stringify(body.output));
        return { content: [{ type: "text", text }], details: { status: body.status ?? "succeeded" } };
      } finally {
        options.onCompleted(toolCallId, tool.name, isError);
      }
    },
  }));
}
