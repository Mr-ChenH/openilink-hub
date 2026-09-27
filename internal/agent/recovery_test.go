package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openilink/openilink-hub/internal/store"
	"github.com/openilink/openilink-hub/internal/store/memstore"
)

type recoveryRuntime struct {
	mu          sync.Mutex
	streamCalls int
	state       RunState
	stateErr    error
	events      []RuntimeEvent
}

func (r *recoveryRuntime) Start(context.Context, RunRequest) (RunHandle, error) {
	return RunHandle{}, nil
}
func (r *recoveryRuntime) Cancel(context.Context, string) error { return nil }
func (r *recoveryRuntime) Health(context.Context) error         { return nil }
func (r *recoveryRuntime) GetRun(context.Context, string) (RunState, error) {
	return r.state, r.stateErr
}
func (r *recoveryRuntime) StreamEvents(_ context.Context, _ string, last string, yield func(RuntimeEvent) error) error {
	r.mu.Lock()
	r.streamCalls++
	r.mu.Unlock()
	for _, event := range r.events {
		if last == "" || last == "0" || event.Seq > 1 {
			if err := yield(event); err != nil {
				return err
			}
		}
	}
	return nil
}

type retrySender struct {
	mu        sync.Mutex
	calls     int
	ambiguous bool
}

func (s *retrySender) SendAgentReply(context.Context, string, string, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls == 1 {
		if s.ambiguous {
			return "", errors.New("connection dropped after write")
		}
		return "", &KnownUnsentError{Err: errors.New("bot disconnected")}
	}
	return "provider-message", nil
}

func completedRun(t *testing.T) (*memstore.Store, *store.AgentRun) {
	t.Helper()
	s := memstore.New()
	s.AddBot(&store.Bot{ID: "bot-recovery", UserID: "tenant", Provider: "mock"})
	conversation, _, err := s.GetOrCreateAgentConversation(&store.AgentConversation{
		ID: "conversation-recovery", TenantID: "tenant", BotID: "bot-recovery", Provider: "mock", SenderID: "user-recovery", SessionRef: "session", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := s.CreateAgentRun(&store.AgentRun{
		ID: "run-recovery", ConversationID: conversation.ID, BotID: "bot-recovery", InboundMessageID: "message-recovery", RunKind: "message", Runtime: "pi", CatalogVersion: "catalog", Deadline: time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TransitionAgentRun(run.ID, store.AgentRunQueued, store.AgentRunRunning, "", ""); err != nil || !ok {
		t.Fatalf("transition running: ok=%v err=%v", ok, err)
	}
	if ok, err := s.TransitionAgentRun(run.ID, store.AgentRunRunning, store.AgentRunCompleted, "", ""); err != nil || !ok {
		t.Fatalf("transition completed: ok=%v err=%v", ok, err)
	}
	return s, run
}

func TestOversizedRecoveredEventFailsRunWithoutPersistence(t *testing.T) {
	s := memstore.New()
	s.AddBot(&store.Bot{ID: "bot-event-limit", UserID: "tenant", Provider: "mock"})
	conversation, _, err := s.GetOrCreateAgentConversation(&store.AgentConversation{
		ID: "conversation-event-limit", TenantID: "tenant", BotID: "bot-event-limit", Provider: "mock", SenderID: "user", SessionRef: "session", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := s.CreateAgentRun(&store.AgentRun{
		ID: "run-event-limit", ConversationID: conversation.ID, BotID: "bot-event-limit", InboundMessageID: "message-event-limit", RunKind: "message", Runtime: "pi", CatalogVersion: "catalog", Deadline: time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	fence, acquired, err := s.AcquireAgentRunLease(run.ID, "test-owner", time.Now().Unix(), time.Now().Add(time.Minute).Unix())
	if err != nil || !acquired {
		t.Fatalf("acquire lease: fence=%d acquired=%v err=%v", fence, acquired, err)
	}
	coordinator := &Coordinator{Store: s}
	err = coordinator.handleEvent(run.ID, fence, RuntimeEvent{
		RunID: run.ID, Seq: 1, Type: "text.delta", Data: json.RawMessage(`{"text":"` + strings.Repeat("x", maxRuntimeEventBytes) + `"}`),
	})
	if err == nil {
		t.Fatal("oversized event was accepted")
	}
	got, err := s.GetAgentRun(run.ID)
	if err != nil || got.Status != store.AgentRunFailed || got.ErrorCode != "runtime_event_too_large" {
		t.Fatalf("run=%+v err=%v", got, err)
	}
	events, err := s.ListAgentRunEvents(run.ID, 0, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestOutboxRetriesOnlyDefinitelyUnsentDelivery(t *testing.T) {
	s, run := completedRun(t)
	content, _ := json.Marshal(map[string]string{"text": "done"})
	item, _, err := s.CreateAgentOutboxItem(&store.AgentOutboxItem{
		ID: "stable-delivery", RunID: run.ID, Kind: "final", Recipient: "user-recovery", Content: content, ContentRef: "agent-outbox:stable-delivery",
	})
	if err != nil {
		t.Fatal(err)
	}
	sender := &retrySender{}
	coordinator := &Coordinator{Store: s, Sender: sender, OutboxBaseDelay: time.Millisecond}
	coordinator.DrainOutbox(context.Background())
	time.Sleep(1100 * time.Millisecond)
	coordinator.DrainOutbox(context.Background())
	got, err := s.GetAgentOutboxItem(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.AgentOutboxSent || got.Attempt != 2 || got.ContentRef != item.ContentRef || sender.calls != 2 {
		t.Fatalf("outbox=%+v calls=%d", got, sender.calls)
	}
}

func TestOutboxBlocksAmbiguousDelivery(t *testing.T) {
	s, run := completedRun(t)
	content, _ := json.Marshal(map[string]string{"text": "done"})
	item, _, err := s.CreateAgentOutboxItem(&store.AgentOutboxItem{ID: "ambiguous-delivery", RunID: run.ID, Kind: "final", Recipient: "user-recovery", Content: content})
	if err != nil {
		t.Fatal(err)
	}
	sender := &retrySender{ambiguous: true}
	coordinator := &Coordinator{Store: s, Sender: sender, OutboxBaseDelay: time.Millisecond}
	coordinator.DrainOutbox(context.Background())
	coordinator.DrainOutbox(context.Background())
	got, err := s.GetAgentOutboxItem(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.AgentOutboxDeliveryBlocked || sender.calls != 1 {
		t.Fatalf("outbox=%+v calls=%d", got, sender.calls)
	}
}

type failOnceOutboxStore struct {
	store.Store
	fail bool
}

func (s *failOnceOutboxStore) CreateAgentOutboxItem(item *store.AgentOutboxItem) (*store.AgentOutboxItem, bool, error) {
	if s.fail {
		s.fail = false
		return nil, false, errors.New("temporary outbox persistence failure")
	}
	return s.Store.CreateAgentOutboxItem(item)
}

func TestRecoveredCompletionRetriesAfterOutboxPersistenceFailure(t *testing.T) {
	s, completed := completedRun(t)
	run, _, err := s.CreateAgentRun(&store.AgentRun{
		ID: "run-persistence-retry", ConversationID: completed.ConversationID, BotID: completed.BotID,
		InboundMessageID: "message-persistence-retry", RunKind: "message", Runtime: "pi",
	})
	if err != nil {
		t.Fatal(err)
	}
	fence, acquired, err := s.AcquireAgentRunLease(run.ID, "owner", time.Now().Unix(), time.Now().Add(time.Minute).Unix())
	if err != nil || !acquired {
		t.Fatalf("acquire lease: fence=%d acquired=%v err=%v", fence, acquired, err)
	}
	wrapped := &failOnceOutboxStore{Store: s, fail: true}
	coordinator := &Coordinator{Store: wrapped}
	state := RunState{RunHandle: RunHandle{RunID: run.ID, Status: RunCompleted}, Text: "recovered"}
	if coordinator.reconcileState(run.ID, fence, state) {
		t.Fatal("transient persistence error was treated as terminal")
	}
	got, _ := s.GetAgentRun(run.ID)
	if got.Status != store.AgentRunQueued {
		t.Fatalf("run status after transient error = %s", got.Status)
	}
	if !coordinator.reconcileState(run.ID, fence, state) {
		t.Fatal("recovered completion did not settle on retry")
	}
	got, _ = s.GetAgentRun(run.ID)
	if got.Status != store.AgentRunCompleted {
		t.Fatalf("run status after retry = %s", got.Status)
	}
}

func TestRecoveredCompletionReusesOutboxAcrossCrashWindow(t *testing.T) {
	s, completed := completedRun(t)
	run, _, err := s.CreateAgentRun(&store.AgentRun{
		ID: "run-crash-window", ConversationID: completed.ConversationID, BotID: completed.BotID,
		InboundMessageID: "message-crash-window", RunKind: "message", Runtime: "pi",
	})
	if err != nil {
		t.Fatal(err)
	}
	fence, acquired, err := s.AcquireAgentRunLease(run.ID, "owner", time.Now().Unix(), time.Now().Add(time.Minute).Unix())
	if err != nil || !acquired {
		t.Fatalf("acquire lease: fence=%d acquired=%v err=%v", fence, acquired, err)
	}
	coordinator := &Coordinator{Store: s}
	first, err := coordinator.enqueueReply(run, "final", "persisted before crash")
	if err != nil {
		t.Fatal(err)
	}
	state := RunState{RunHandle: RunHandle{RunID: run.ID, Status: RunCompleted}, Text: "recomputed after restart"}
	if !coordinator.reconcileState(run.ID, fence, state) {
		t.Fatal("crash-window reconciliation did not settle")
	}
	got, _ := s.GetAgentRun(run.ID)
	if got.Status != store.AgentRunCompleted {
		t.Fatalf("run status = %s", got.Status)
	}
	pending, err := s.ListPendingAgentOutbox(10)
	if err != nil || len(pending) != 1 || pending[0].ID != first.ID || string(pending[0].Content) != string(first.Content) {
		t.Fatalf("outbox after recovery=%+v err=%v", pending, err)
	}
}

func TestMalformedRecoveredCompletionFailsRun(t *testing.T) {
	s, completed := completedRun(t)
	run, _, err := s.CreateAgentRun(&store.AgentRun{
		ID: "run-malformed-result", ConversationID: completed.ConversationID, BotID: completed.BotID,
		InboundMessageID: "message-malformed-result", RunKind: "message", Runtime: "pi",
	})
	if err != nil {
		t.Fatal(err)
	}
	fence, acquired, err := s.AcquireAgentRunLease(run.ID, "owner", time.Now().Unix(), time.Now().Add(time.Minute).Unix())
	if err != nil || !acquired {
		t.Fatalf("acquire lease: fence=%d acquired=%v err=%v", fence, acquired, err)
	}
	coordinator := &Coordinator{Store: s}
	state := RunState{RunHandle: RunHandle{RunID: run.ID, Status: RunCompleted}}
	if !coordinator.reconcileState(run.ID, fence, state) {
		t.Fatal("malformed result did not settle")
	}
	got, _ := s.GetAgentRun(run.ID)
	if got.Status != store.AgentRunFailed || got.ErrorCode != "invalid_runtime_result" {
		t.Fatalf("run=%+v", got)
	}
}

func TestDuplicateTerminalRecoveryCreatesOneReply(t *testing.T) {
	s, completed := completedRun(t)
	run, _, err := s.CreateAgentRun(&store.AgentRun{
		ID: "run-terminal-recovery", ConversationID: completed.ConversationID, BotID: completed.BotID, InboundMessageID: "message-terminal-recovery", RunKind: "message", Runtime: "pi", CatalogVersion: "catalog", Deadline: time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &recoveryRuntime{state: RunState{RunHandle: RunHandle{RunID: run.ID, Status: RunCompleted}, Text: "recovered"}}
	sender := &captureSender{done: make(chan string, 2)}
	coordinator := &Coordinator{Store: s, Runtime: runtime, Sender: sender}
	coordinator.reconcile()
	select {
	case <-sender.done:
	case <-time.After(2 * time.Second):
		got, _ := s.GetAgentRun(run.ID)
		pending, _ := s.ListPendingAgentOutbox(10)
		t.Fatalf("terminal recovery did not deliver reply: run=%+v pending=%+v", got, pending)
	}
	coordinator.reconcile()
	time.Sleep(20 * time.Millisecond)
	select {
	case duplicate := <-sender.done:
		t.Fatalf("duplicate terminal recovery reply: %s", duplicate)
	default:
	}
	got, err := s.GetAgentRun(run.ID)
	if err != nil || got.Status != store.AgentRunCompleted {
		t.Fatalf("run=%+v err=%v", got, err)
	}
}

func TestReconcileMissingRuntimeMarksInterruptedAndRepliesOnce(t *testing.T) {
	s, completed := completedRun(t)
	// Create a distinct nonterminal run in the same durable conversation.
	run, _, err := s.CreateAgentRun(&store.AgentRun{
		ID: "run-missing", ConversationID: completed.ConversationID, BotID: completed.BotID, InboundMessageID: "message-missing", RunKind: "message", Runtime: "pi", CatalogVersion: "catalog", Deadline: time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &recoveryRuntime{stateErr: &RuntimeHTTPError{StatusCode: 404, Message: "not found"}}
	sender := &captureSender{done: make(chan string, 2)}
	coordinator := &Coordinator{Store: s, Runtime: runtime, Sender: sender}
	coordinator.reconcile()
	select {
	case <-sender.done:
	case <-time.After(2 * time.Second):
		t.Fatal("missing runtime did not produce a user-visible failure")
	}
	coordinator.reconcile()
	time.Sleep(20 * time.Millisecond)
	got, err := s.GetAgentRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.AgentRunInterrupted || got.ErrorCode != "runtime_state_lost" {
		t.Fatalf("run=%+v", got)
	}
	select {
	case duplicate := <-sender.done:
		t.Fatalf("duplicate recovery reply: %s", duplicate)
	default:
	}
}
