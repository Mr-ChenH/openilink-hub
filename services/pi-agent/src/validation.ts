import type { CreateRunRequest, ToolSpec } from "./types.js";

export class ValidationError extends Error {}

function object(value: unknown, name: string): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ValidationError(`${name} must be an object`);
  return value as Record<string, unknown>;
}

function string(value: unknown, name: string, max: number): string {
  if (typeof value !== "string" || value.length === 0 || value.length > max) {
    throw new ValidationError(`${name} must be a non-empty string of at most ${max} characters`);
  }
  return value;
}

function integer(value: unknown, name: string, minimum: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum) throw new ValidationError(`${name} must be an integer >= ${minimum}`);
  return value as number;
}

export function parseRunRequest(value: unknown): CreateRunRequest {
  const root = object(value, "request");
  if (root.protocol_version !== 1) throw new ValidationError("protocol_version must be 1");
  const runId = string(root.run_id, "run_id", 128);
  const conversationId = string(root.conversation_id, "conversation_id", 256);
  if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]*$/.test(runId)) throw new ValidationError("run_id contains invalid characters");
  const input = object(root.input, "input");
  const rawTools = root.tools;
  if (!Array.isArray(rawTools) || rawTools.length > 40) throw new ValidationError("tools must be an array with at most 40 entries");
  const names = new Set<string>();
  const tools: ToolSpec[] = rawTools.map((raw, index) => {
    const tool = object(raw, `tools[${index}]`);
    const name = string(tool.name, `tools[${index}].name`, 64);
    if (!/^[A-Za-z][A-Za-z0-9_]*$/.test(name)) throw new ValidationError(`invalid tool name: ${name}`);
    if (names.has(name)) throw new ValidationError(`duplicate tool name: ${name}`);
    names.add(name);
    const parameters = object(tool.parameters, `tools[${index}].parameters`);
    if (parameters.type !== "object") throw new ValidationError(`tools[${index}].parameters.type must be object`);
    return { name, description: string(tool.description, `tools[${index}].description`, 2_000), parameters };
  });
  const limits = root.limits === undefined ? undefined : object(root.limits, "limits");
  const capability = root.tool_capability;
  if (tools.length > 0 && (typeof capability !== "string" || capability.length === 0)) {
    throw new ValidationError("tool_capability is required when tools are present");
  }
  return {
    protocol_version: 1,
    run_id: runId,
    conversation_id: conversationId,
    session_epoch: integer(root.session_epoch, "session_epoch", 0),
    input: {
      message_id: string(input.message_id, "input.message_id", 256),
      text: string(input.text, "input.text", 100_000),
    },
    model_profile: string(root.model_profile, "model_profile", 128),
    catalog_version: string(root.catalog_version, "catalog_version", 256),
    tools,
    ...(typeof capability === "string" ? { tool_capability: capability } : {}),
    ...(limits
      ? {
          limits: {
            ...(limits.max_tool_calls === undefined ? {} : { max_tool_calls: integer(limits.max_tool_calls, "limits.max_tool_calls", 0) }),
            ...(limits.timeout_ms === undefined ? {} : { timeout_ms: integer(limits.timeout_ms, "limits.timeout_ms", 1) }),
          },
        }
      : {}),
  };
}
