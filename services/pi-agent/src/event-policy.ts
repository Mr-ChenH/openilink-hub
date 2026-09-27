const MAX_TEXT_DELTA_BYTES = 8 * 1024;
const MAX_FINAL_TEXT_BYTES = 32 * 1024;
const MAX_IDENTIFIER_LENGTH = 256;

function boundedString(value: unknown, maxBytes: number): string | undefined {
  if (typeof value !== "string") return undefined;
  if (Buffer.byteLength(value, "utf8") <= maxBytes) return value;
  let end = Math.min(value.length, maxBytes);
  while (end > 0 && Buffer.byteLength(value.slice(0, end), "utf8") > maxBytes) end -= 1;
  return value.slice(0, end);
}

function identifier(value: unknown): string | undefined {
  return typeof value === "string" && value.length <= MAX_IDENTIFIER_LENGTH ? value : undefined;
}

function count(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

export function boundedFinalText(value: string): string {
  return boundedString(value, MAX_FINAL_TEXT_BYTES) ?? "";
}

// Runtime event payloads cross two persistence boundaries. Rebuild every
// allowed shape so provider reasoning, credentials, and tool content cannot
// enter replay storage through unexpected fields.
export function sanitizeEventData(type: string, data: Record<string, unknown>): Record<string, unknown> | undefined {
  switch (type) {
    case "run.started":
    case "run.cancelled":
      return {};
    case "text.delta": {
      const text = boundedString(data.text, MAX_TEXT_DELTA_BYTES);
      return text === undefined ? {} : { text };
    }
    case "tool.started": {
      const callId = identifier(data.call_id);
      const toolName = identifier(data.tool_name);
      return { ...(callId ? { call_id: callId } : {}), ...(toolName ? { tool_name: toolName } : {}) };
    }
    case "tool.completed": {
      const callId = identifier(data.call_id);
      const toolName = identifier(data.tool_name);
      return {
        ...(callId ? { call_id: callId } : {}),
        ...(toolName ? { tool_name: toolName } : {}),
        ...(typeof data.is_error === "boolean" ? { is_error: data.is_error } : {}),
      };
    }
    case "usage.updated": {
      const result: Record<string, number> = {};
      for (const key of ["input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"] as const) {
        const value = count(data[key]);
        if (value !== undefined) result[key] = value;
      }
      return result;
    }
    case "run.completed": {
      const text = boundedFinalText(typeof data.text === "string" ? data.text : "");
      return { text };
    }
    case "run.failed":
    case "run.interrupted": {
      const code = identifier(data.code);
      return { ...(code ? { code } : {}), message: "agent runtime failed" };
    }
    default:
      return undefined;
  }
}
