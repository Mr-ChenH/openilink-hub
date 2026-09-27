package storetest

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openilink/openilink-hub/internal/store"
)

// TestAgentStore exercises the agent state machines and idempotency contract.
func TestAgentStore(t *testing.T, s store.AgentStore) {
	profile := &store.AgentProfile{ID: "profile-1", OwnerID: "owner-1", Runtime: "pi", ModelProfile: "default", PromptVersion: "v1", Limits: json.RawMessage(`{"max_tool_calls":8}`), Enabled: true}
	if err := s.CreateAgentProfile(profile); err != nil {
		t.Fatalf("CreateAgentProfile: %v", err)
	}
	profile.ModelProfile = "fast"
	if err := s.UpdateAgentProfile(profile); err != nil {
		t.Fatalf("UpdateAgentProfile: %v", err)
	}
	gotProfile, err := s.GetAgentProfile(profile.ID)
	if err != nil || gotProfile.ModelProfile != "fast" {
		t.Fatalf("GetAgentProfile: got=%+v err=%v", gotProfile, err)
	}
	otherProfile := &store.AgentProfile{ID: "profile-other", OwnerID: "owner-2", Runtime: "pi", ModelProfile: "other", Enabled: true}
	if err := s.CreateAgentProfile(otherProfile); err != nil {
		t.Fatalf("CreateAgentProfile other: %v", err)
	}
	profiles, err := s.ListAgentProfilesByOwner(profile.OwnerID)
	if err != nil || len(profiles) != 1 || profiles[0].ID != profile.ID {
		t.Fatalf("ListAgentProfilesByOwner: got=%+v err=%v", profiles, err)
	}

	settings, err := s.GetBotAgentSettings("missing-bot")
	if !errors.Is(err, sql.ErrNoRows) || settings != nil {
		t.Fatalf("missing GetBotAgentSettings: got=%+v err=%v", settings, err)
	}

	settings = &store.BotAgentSettings{BotID: "bot-agent-1", ProfileID: profile.ID, RoutingMode: "pi", TriggerPolicy: json.RawMessage(`{"private":true}`), ToolPolicy: json.RawMessage(`{"default":"confirm"}`)}
	if err := s.PutBotAgentSettings(settings); err != nil {
		t.Fatalf("PutBotAgentSettings: %v", err)
	}
	settings.RoutingMode = "native"
	if err := s.PutBotAgentSettings(settings); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	gotSettings, err := s.GetBotAgentSettings(settings.BotID)
	if err != nil || gotSettings.RoutingMode != "native" {
		t.Fatalf("GetBotAgentSettings: got=%+v err=%v", gotSettings, err)
	}

	conv := &store.AgentConversation{ID: "conv-1", TenantID: "tenant-1", BotID: settings.BotID, Provider: "ilink", SenderID: "sender-1", SessionRef: "session-1"}
	gotConv, inserted, err := s.GetOrCreateAgentConversation(conv)
	if err != nil || !inserted || gotConv.Epoch != 1 {
		t.Fatalf("create conversation: got=%+v inserted=%v err=%v", gotConv, inserted, err)
	}
	replay := *conv
	replay.ID = "different-id"
	gotConv, inserted, err = s.GetOrCreateAgentConversation(&replay)
	if err != nil || inserted || gotConv.ID != conv.ID {
		t.Fatalf("replay conversation: got=%+v inserted=%v err=%v", gotConv, inserted, err)
	}
	conversations, err := s.ListAgentConversationsByBot(conv.BotID, 10)
	if err != nil || len(conversations) != 1 || conversations[0].ID != conv.ID {
		t.Fatalf("ListAgentConversationsByBot: got=%+v err=%v", conversations, err)
	}

	run := &store.AgentRun{ID: "run-1", ConversationID: conv.ID, BotID: conv.BotID, InboundMessageID: "msg-1", RunKind: "message", Runtime: "pi", CatalogVersion: "cat-1"}
	gotRun, inserted, err := s.CreateAgentRun(run)
	if err != nil || !inserted || gotRun.Status != store.AgentRunQueued {
		t.Fatalf("create run: got=%+v inserted=%v err=%v", gotRun, inserted, err)
	}
	_, inserted, err = s.CreateAgentRun(run)
	if err != nil || inserted {
		t.Fatalf("idempotent run replay: inserted=%v err=%v", inserted, err)
	}
	runs, err := s.ListAgentRunsByBot(conv.BotID, 0, "", 10)
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("ListAgentRunsByBot: got=%+v err=%v", runs, err)
	}
	cursorRuns, err := s.ListAgentRunsByBot(conv.BotID, runs[0].CreatedAt, runs[0].ID, 10)
	if err != nil || len(cursorRuns) != 0 {
		t.Fatalf("ListAgentRunsByBot cursor: got=%+v err=%v", cursorRuns, err)
	}
	conflict := *run
	conflict.Runtime = "native"
	if _, _, err := s.CreateAgentRun(&conflict); err == nil {
		t.Fatal("expected conflicting run replay error")
	}

	fence, acquired, err := s.AcquireAgentRunLease(run.ID, "worker-a", 100, 200)
	if err != nil || !acquired || fence != 1 {
		t.Fatalf("first lease: fence=%d acquired=%v err=%v", fence, acquired, err)
	}
	if _, acquired, err = s.AcquireAgentRunLease(run.ID, "worker-b", 150, 250); err != nil || acquired {
		t.Fatalf("unexpired competing lease: acquired=%v err=%v", acquired, err)
	}
	fence, acquired, err = s.AcquireAgentRunLease(run.ID, "worker-b", 201, 300)
	if err != nil || !acquired || fence != 2 {
		t.Fatalf("expired lease: fence=%d acquired=%v err=%v", fence, acquired, err)
	}
	if ok, err := s.TransitionAgentRun(run.ID, store.AgentRunQueued, store.AgentRunRunning, "", ""); err != nil || !ok {
		t.Fatalf("run transition: ok=%v err=%v", ok, err)
	}
	if ok, err := s.TransitionAgentRun(run.ID, store.AgentRunQueued, store.AgentRunFailed, "bad", "bad"); err != nil || ok {
		t.Fatalf("stale run transition: ok=%v err=%v", ok, err)
	}

	call := &store.AgentToolCall{ID: "call-1", RunID: run.ID, InstallationID: "installation-1", ToolName: "list_prs", Arguments: json.RawMessage(`{"repo":"x/y"}`), ArgsHash: "args-1", SchemaHash: "schema-1", Effect: "read"}
	gotCall, inserted, err := s.CreateAgentToolCall(call)
	if err != nil || !inserted || gotCall.Status != store.AgentToolCreated {
		t.Fatalf("create call: got=%+v inserted=%v err=%v", gotCall, inserted, err)
	}
	_, inserted, err = s.CreateAgentToolCall(call)
	if err != nil || inserted {
		t.Fatalf("idempotent call replay: inserted=%v err=%v", inserted, err)
	}
	callConflict := *call
	callConflict.ArgsHash = "different"
	if _, _, err := s.CreateAgentToolCall(&callConflict); err == nil {
		t.Fatal("expected conflicting tool call replay error")
	}
	if ok, err := s.TransitionAgentToolCall(run.ID, call.ID, store.AgentToolCreated, store.AgentToolAuthorized, nil, "", "", ""); err != nil || !ok {
		t.Fatalf("authorize call: ok=%v err=%v", ok, err)
	}
	if ok, err := s.TransitionAgentToolCall(run.ID, call.ID, store.AgentToolAuthorized, store.AgentToolDispatched, nil, "", "", ""); err != nil || !ok {
		t.Fatalf("dispatch call: ok=%v err=%v", ok, err)
	}
	result := json.RawMessage(`{"items":[]}`)
	if ok, err := s.TransitionAgentToolCall(run.ID, call.ID, store.AgentToolDispatched, store.AgentToolSucceeded, result, "", "", ""); err != nil || !ok {
		t.Fatalf("complete call: ok=%v err=%v", ok, err)
	}
	gotCall, err = s.GetAgentToolCall(run.ID, call.ID)
	if err != nil || gotCall.Attempt != 1 || gotCall.Status != store.AgentToolSucceeded {
		t.Fatalf("completed call: got=%+v err=%v", gotCall, err)
	}
	calls, err := s.ListAgentToolCalls(run.ID)
	if err != nil || len(calls) != 1 || calls[0].ID != call.ID {
		t.Fatalf("ListAgentToolCalls: got=%+v err=%v", calls, err)
	}

	confirmation := &store.AgentConfirmation{ID: "confirmation-1", CallID: call.ID, SenderID: conv.SenderID, CodeHash: "code", ArgsHash: call.ArgsHash, ExpiresAt: 500}
	if err := s.CreateAgentConfirmation(confirmation); err != nil {
		t.Fatalf("CreateAgentConfirmation: %v", err)
	}
	if ok, err := s.ConsumeAgentConfirmation(confirmation.ID, "wrong", "code", call.ArgsHash, 400); err != nil || ok {
		t.Fatalf("wrong sender confirmation: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ConsumeAgentConfirmation(confirmation.ID, conv.SenderID, "code", call.ArgsHash, 400); err != nil || !ok {
		t.Fatalf("consume confirmation: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ConsumeAgentConfirmation(confirmation.ID, conv.SenderID, "code", call.ArgsHash, 401); err != nil || ok {
		t.Fatalf("reused confirmation: ok=%v err=%v", ok, err)
	}

	for _, e := range []store.AgentRunEvent{{RunID: run.ID, Seq: 2, EventType: "tool.completed", SanitizedPayload: result}, {RunID: run.ID, Seq: 1, EventType: "run.started", SanitizedPayload: json.RawMessage(`{}`)}} {
		if inserted, err := s.AppendAgentRunEvent(&e); err != nil || !inserted {
			t.Fatalf("append event: inserted=%v err=%v", inserted, err)
		}
	}
	replayEvent := &store.AgentRunEvent{RunID: run.ID, Seq: 1, EventType: "run.started", SanitizedPayload: json.RawMessage(`{}`)}
	if inserted, err := s.AppendAgentRunEvent(replayEvent); err != nil || inserted {
		t.Fatalf("event replay: inserted=%v err=%v", inserted, err)
	}
	events, err := s.ListAgentRunEvents(run.ID, 0, 10)
	if err != nil || len(events) != 2 || events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("events ordering: events=%+v err=%v", events, err)
	}

	item := &store.AgentOutboxItem{ID: "outbox-1", RunID: run.ID, Kind: "final", Recipient: conv.SenderID, Content: json.RawMessage(`{"text":"done"}`)}
	gotItem, inserted, err := s.CreateAgentOutboxItem(item)
	if err != nil || !inserted || gotItem.Status != store.AgentOutboxPending {
		t.Fatalf("create outbox: got=%+v inserted=%v err=%v", gotItem, inserted, err)
	}
	_, inserted, err = s.CreateAgentOutboxItem(item)
	if err != nil || inserted {
		t.Fatalf("outbox replay: inserted=%v err=%v", inserted, err)
	}
	pending, err := s.ListPendingAgentOutbox(10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending outbox: count=%d err=%v", len(pending), err)
	}
	if ok, err := s.TransitionAgentOutboxItem(item.ID, store.AgentOutboxPending, store.AgentOutboxSending, "", ""); err != nil || !ok {
		t.Fatalf("claim outbox: ok=%v err=%v", ok, err)
	}
	if ok, err := s.TransitionAgentOutboxItem(item.ID, store.AgentOutboxSending, store.AgentOutboxSent, "provider-1", ""); err != nil || !ok {
		t.Fatalf("send outbox: ok=%v err=%v", ok, err)
	}

	if ok, err := s.TransitionAgentRun(run.ID, store.AgentRunRunning, store.AgentRunCompleted, "", ""); err != nil || !ok {
		t.Fatalf("complete run: ok=%v err=%v", ok, err)
	}
	gotConv, err = s.GetAgentConversation(conv.ID)
	if err != nil || gotConv.LastCompletedRunID != run.ID {
		t.Fatalf("conversation completion: got=%+v err=%v", gotConv, err)
	}
	gotConv, err = s.ResetAgentConversation(conv.ID, "session-2")
	if err != nil || gotConv.Epoch != 2 || gotConv.LastCompletedRunID != "" {
		t.Fatalf("reset conversation: got=%+v err=%v", gotConv, err)
	}
	if _, acquired, err := s.AcquireAgentRunLease(run.ID, "worker-c", 400, 500); err != nil || acquired {
		t.Fatalf("terminal run lease: acquired=%v err=%v", acquired, err)
	}
}
