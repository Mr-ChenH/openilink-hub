package agent

import (
	"context"
	"testing"
	"time"

	"github.com/openilink/openilink-hub/internal/store"
	"github.com/openilink/openilink-hub/internal/store/memstore"
)

func confirmationBrokerFixture(t *testing.T) (*Broker, *memstore.Store, *dispatcherStub, ToolCallRequest) {
	t.Helper()
	s := memstore.New()
	s.AddBot(&store.Bot{ID: "bot", UserID: "owner"})
	s.AddInstallation(&store.AppInstallation{ID: "inst", AppID: "app", BotID: "bot", Enabled: true})
	conversation, _, err := s.GetOrCreateAgentConversation(&store.AgentConversation{ID: "conversation", TenantID: "owner", BotID: "bot", Provider: "test", SenderID: "sender"})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := s.CreateAgentRun(&store.AgentRun{ID: "run", ConversationID: conversation.ID, BotID: "bot", InboundMessageID: "message", Runtime: "pi", CatalogVersion: "cat_current"})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := s.TransitionAgentRun(run.ID, store.AgentRunQueued, store.AgentRunRunning, "", ""); err != nil || !changed {
		t.Fatalf("start run: changed=%v err=%v", changed, err)
	}
	dispatcher := &dispatcherStub{}
	broker := &Broker{
		Catalog: fixedResolver{testCatalog(PolicyConfirm, EffectWrite)}, Installations: s,
		Authorizer: allowAuthorizer{}, Dispatcher: dispatcher, AgentStore: s,
	}
	return broker, s, dispatcher, testCall()
}

func TestConfirmationBindsOwnerRunCallArgsAndConsumesOnce(t *testing.T) {
	broker, s, dispatcher, request := confirmationBrokerFixture(t)
	_, err := broker.Execute(context.Background(), request)
	assertBrokerCode(t, err, CodeConfirmationNeeded)
	call, err := s.GetAgentToolCall(request.RunID, request.CallID)
	if err != nil || call.Status != store.AgentToolAwaitingConfirmation || call.ConfirmationID == "" {
		t.Fatalf("awaiting call=%+v err=%v", call, err)
	}
	run, _ := s.GetAgentRun(request.RunID)
	if run.Status != store.AgentRunWaitingConfirmation || dispatcher.calls != 0 {
		t.Fatalf("run=%s dispatches=%d", run.Status, dispatcher.calls)
	}
	confirmation, err := s.GetAgentConfirmation(call.ConfirmationID)
	if err != nil || confirmation.RunID != request.RunID || confirmation.CallID != request.CallID || confirmation.OwnerID != "owner" || confirmation.ArgsHash != call.ArgsHash {
		t.Fatalf("confirmation binding=%+v err=%v", confirmation, err)
	}
	for _, attempt := range []struct{ run, call, owner, args string }{
		{"other", request.CallID, "owner", call.ArgsHash},
		{request.RunID, "other", "owner", call.ArgsHash},
		{request.RunID, request.CallID, "attacker", call.ArgsHash},
		{request.RunID, request.CallID, "owner", "different"},
	} {
		if ok, err := s.ResolveAgentToolConfirmation(confirmation.ID, attempt.run, attempt.call, attempt.owner, attempt.args, "approve", time.Now().Unix()); err != nil || ok {
			t.Fatalf("mismatched approval accepted: %+v ok=%v err=%v", attempt, ok, err)
		}
	}
	if ok, err := s.ResolveAgentToolConfirmation(confirmation.ID, request.RunID, request.CallID, "owner", call.ArgsHash, "approve", time.Now().Unix()); err != nil || !ok {
		t.Fatalf("approval: ok=%v err=%v", ok, err)
	}
	if ok, _ := s.ResolveAgentToolConfirmation(confirmation.ID, request.RunID, request.CallID, "owner", call.ArgsHash, "approve", time.Now().Unix()); ok {
		t.Fatal("confirmation replay was accepted")
	}
	result, err := broker.Execute(context.Background(), request)
	if err != nil || result.Text != "done" || dispatcher.calls != 1 {
		t.Fatalf("resume result=%+v calls=%d err=%v", result, dispatcher.calls, err)
	}
}

func TestExpiredConfirmationNeverDispatches(t *testing.T) {
	broker, s, dispatcher, request := confirmationBrokerFixture(t)
	_, _ = broker.Execute(context.Background(), request)
	call, _ := s.GetAgentToolCall(request.RunID, request.CallID)
	confirmation, _ := s.GetAgentConfirmation(call.ConfirmationID)
	confirmation.ExpiresAt = 1
	// Replace the generated record in a separate fixture-compatible path by expiring at a future clock.
	if ok, err := s.ExpireAgentToolConfirmation(confirmation.ID, request.RunID, request.CallID, time.Now().Add(20*time.Minute).Unix()); err != nil || !ok {
		t.Fatalf("expire: ok=%v err=%v", ok, err)
	}
	if _, err := broker.Execute(context.Background(), request); err == nil {
		t.Fatal("expired call became executable")
	}
	call, _ = s.GetAgentToolCall(request.RunID, request.CallID)
	if call.Status != store.AgentToolTimedOut || dispatcher.calls != 0 {
		t.Fatalf("expired call=%+v dispatches=%d", call, dispatcher.calls)
	}
}
