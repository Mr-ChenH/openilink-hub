package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeRuntimeEventUsesAllowlistedShapes(t *testing.T) {
	payload, allowed := sanitizeRuntimeEvent(RuntimeEvent{Type: "tool.completed", Data: json.RawMessage(`{"call_id":"call-1","tool_name":"lookup","is_error":false,"reasoning":"secret","capability":"token","output":{"private":true}}`)})
	if !allowed {
		t.Fatal("allowed event was rejected")
	}
	if got := string(payload); got != `{"call_id":"call-1","is_error":false,"tool_name":"lookup"}` {
		t.Fatalf("sanitized payload = %s", got)
	}
	if _, allowed := sanitizeRuntimeEvent(RuntimeEvent{Type: "provider.reasoning", Data: json.RawMessage(`{"text":"secret"}`)}); allowed {
		t.Fatal("unknown event type was allowed")
	}
	if _, allowed := sanitizeRuntimeEvent(RuntimeEvent{Type: "run.completed", Data: json.RawMessage(`{"text":"` + strings.Repeat("x", maxRuntimeEventBytes) + `"}`)}); allowed {
		t.Fatal("oversized event was allowed")
	}
}

func TestCompletedEventPersistsOnlyLength(t *testing.T) {
	payload, allowed := sanitizeRuntimeEvent(RuntimeEvent{Type: "run.completed", Data: json.RawMessage(`{"text":"private answer","reasoning":"secret"}`)})
	if !allowed || string(payload) != `{"text_length":14}` {
		t.Fatalf("payload=%s allowed=%v", payload, allowed)
	}
}
