package agent

import (
	"encoding/json"
	"unicode/utf8"
)

const maxRuntimeEventBytes = 64 * 1024

func sanitizeRuntimeEvent(event RuntimeEvent) (json.RawMessage, bool) {
	if len(event.Data) > maxRuntimeEventBytes {
		return nil, false
	}
	var data map[string]any
	if len(event.Data) > 0 && json.Unmarshal(event.Data, &data) != nil {
		return nil, false
	}
	if data == nil {
		data = map[string]any{}
	}
	clean := map[string]any{}
	switch event.Type {
	case "run.started", "run.cancelled", "text.delta":
		// Deltas are streamed to the coordinator but do not need durable content.
	case "tool.started", "tool.completed":
		copyBoundedString(clean, data, "call_id", 256)
		copyBoundedString(clean, data, "tool_name", 256)
		if value, ok := data["is_error"].(bool); ok && event.Type == "tool.completed" {
			clean["is_error"] = value
		}
	case "usage.updated":
		for _, key := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"} {
			if value, ok := data[key].(float64); ok && value >= 0 && value == float64(int64(value)) {
				clean[key] = int64(value)
			}
		}
	case "run.completed":
		if text, ok := data["text"].(string); ok {
			clean["text_length"] = utf8.RuneCountInString(text)
		}
	case "run.failed":
		copyBoundedString(clean, data, "code", 128)
	default:
		return nil, false
	}
	payload, err := json.Marshal(clean)
	return payload, err == nil
}

func copyBoundedString(dst, src map[string]any, key string, max int) {
	if value, ok := src[key].(string); ok && len(value) <= max {
		dst[key] = value
	}
}
