package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/openilink/openilink-hub/internal/auth"
	"github.com/openilink/openilink-hub/internal/store"
)

func TestAgentControlPlaneOwnershipAndRunData(t *testing.T) {
	env := setupTestEnv(t)
	bot := createTestBot(t, env.store, env.user.ID, "agent-bot")

	member, err := env.store.CreateUserFull("agent-member", "", "Agent Member", "hashed", store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpdateUserStatus(member.ID, store.StatusActive); err != nil {
		t.Fatal(err)
	}
	memberToken, err := auth.CreateSession(env.store, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	memberCookie := &http.Cookie{Name: "session", Value: memberToken}

	resp := doJSON(t, env.ts, http.MethodPost, "/api/agent/profiles", map[string]any{
		"model_profile": "default", "enabled": true,
	}, withCookie(env.cookie))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create profile status=%d body=%v", resp.StatusCode, decodeJSON(t, resp))
	}
	var ownProfile store.AgentProfile
	if err := json.NewDecoder(resp.Body).Decode(&ownProfile); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp = doJSON(t, env.ts, http.MethodPost, "/api/agent/profiles", map[string]any{
		"model_profile": "other", "enabled": true,
	}, withCookie(memberCookie))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create member profile status=%d", resp.StatusCode)
	}
	var otherProfile store.AgentProfile
	if err := json.NewDecoder(resp.Body).Decode(&otherProfile); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	t.Run("profile list is owner scoped", func(t *testing.T) {
		resp := doJSON(t, env.ts, http.MethodGet, "/api/agent/profiles", nil, withCookie(env.cookie))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", resp.StatusCode)
		}
		var profiles []store.AgentProfile
		if err := json.NewDecoder(resp.Body).Decode(&profiles); err != nil {
			t.Fatal(err)
		}
		if len(profiles) != 1 || profiles[0].ID != ownProfile.ID {
			t.Fatalf("unexpected profiles: %+v", profiles)
		}
	})

	t.Run("profile update hides another owner profile", func(t *testing.T) {
		resp := doJSON(t, env.ts, http.MethodPut, "/api/agent/profiles/"+otherProfile.ID, map[string]any{
			"model_profile": "stolen", "enabled": true,
		}, withCookie(env.cookie))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status=%d", resp.StatusCode)
		}
	})

	t.Run("settings reject another owner profile", func(t *testing.T) {
		resp := doJSON(t, env.ts, http.MethodPut, "/api/bots/"+bot.ID+"/agent/settings", map[string]any{
			"profile_id": otherProfile.ID, "routing_mode": "agent",
		}, withCookie(env.cookie))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d", resp.StatusCode)
		}
	})

	t.Run("settings expose unavailable runtime without secrets", func(t *testing.T) {
		resp := doJSON(t, env.ts, http.MethodGet, "/api/bots/"+bot.ID+"/agent/settings", nil, withCookie(env.cookie))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", resp.StatusCode)
		}
		var body struct {
			Settings         store.BotAgentSettings `json:"settings"`
			RuntimeAvailable bool                   `json:"runtime_available"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.RuntimeAvailable || body.Settings.RoutingMode != "off" {
			t.Fatalf("unexpected settings: %+v", body)
		}
	})

	conversation, _, err := env.store.GetOrCreateAgentConversation(&store.AgentConversation{
		ID: "conversation-api", TenantID: env.user.ID, BotID: bot.ID, Provider: "test", SenderID: "sender",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := env.store.CreateAgentRun(&store.AgentRun{
		ID: "run-api", ConversationID: conversation.ID, BotID: bot.ID, InboundMessageID: "message-api", Runtime: "pi",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.store.AppendAgentRunEvent(&store.AgentRunEvent{RunID: run.ID, Seq: 1, EventType: "run.started", SanitizedPayload: json.RawMessage(`{"reasoning":"must-not-leak"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = env.store.CreateAgentToolCall(&store.AgentToolCall{
		ID: "call-api", RunID: run.ID, InstallationID: "installation-api", ToolName: "lookup",
		Arguments: json.RawMessage(`{"secret":"must-not-leak"}`), ArgsHash: "args", SchemaHash: "schema",
		Result: json.RawMessage(`{"secret":"must-not-leak"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("run list and detail include conversation and sanitized events", func(t *testing.T) {
		resp := doJSON(t, env.ts, http.MethodGet, "/api/bots/"+bot.ID+"/agent/runs?limit=1", nil, withCookie(env.cookie))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list status=%d", resp.StatusCode)
		}
		var list struct {
			Runs       []store.AgentRun `json:"runs"`
			NextCursor string           `json:"next_cursor"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if len(list.Runs) != 1 || list.Runs[0].ID != run.ID {
			t.Fatalf("unexpected runs: %+v", list.Runs)
		}

		resp = doJSON(t, env.ts, http.MethodGet, "/api/bots/"+bot.ID+"/agent/runs/"+run.ID, nil, withCookie(env.cookie))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("detail status=%d", resp.StatusCode)
		}
		var detail struct {
			Conversation store.AgentConversation `json:"conversation"`
			Events       []struct {
				SanitizedPayload map[string]string `json:"sanitized_payload"`
			} `json:"events"`
			ToolCalls []map[string]any `json:"tool_calls"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
			t.Fatal(err)
		}
		if detail.Conversation.ID != conversation.ID || len(detail.Events) != 1 || len(detail.ToolCalls) != 1 {
			t.Fatalf("unexpected detail: %+v", detail)
		}
		if _, exposed := detail.Events[0].SanitizedPayload["reasoning"]; exposed {
			t.Fatal("run detail exposed internal reasoning")
		}
		for _, field := range []string{"arguments", "result", "args_hash", "schema_hash"} {
			if _, exposed := detail.ToolCalls[0][field]; exposed {
				t.Fatalf("run detail exposed tool-call field %q", field)
			}
		}
	})

	t.Run("another user cannot read run data", func(t *testing.T) {
		resp := doJSON(t, env.ts, http.MethodGet, "/api/bots/"+bot.ID+"/agent/runs/"+run.ID, nil, withCookie(memberCookie))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status=%d", resp.StatusCode)
		}
	})
}
