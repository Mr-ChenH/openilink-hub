package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	appdelivery "github.com/openilink/openilink-hub/internal/app"
	"github.com/openilink/openilink-hub/internal/store"
	"github.com/openilink/openilink-hub/internal/store/memstore"
)

type endToEndRuntime struct {
	mu      sync.Mutex
	request RunRequest
	broker  *Broker
}

func (r *endToEndRuntime) Start(_ context.Context, request RunRequest) (RunHandle, error) {
	r.mu.Lock()
	r.request = request
	r.mu.Unlock()
	return RunHandle{RunID: request.RunID, Status: RunQueued}, nil
}
func (r *endToEndRuntime) Cancel(context.Context, string) error { return nil }
func (r *endToEndRuntime) Health(context.Context) error         { return nil }
func (r *endToEndRuntime) StreamEvents(ctx context.Context, runID, _ string, yield func(RuntimeEvent) error) error {
	r.mu.Lock()
	request := r.request
	r.mu.Unlock()
	if err := yield(RuntimeEvent{Seq: 1, RunID: runID, Type: "run.started", Timestamp: time.Now(), Data: json.RawMessage(`{}`)}); err != nil {
		return err
	}
	result, err := r.broker.Execute(ctx, ToolCallRequest{
		RunID: runID, CallID: "call-1", BotID: "bot-1", CatalogVersion: request.CatalogVersion,
		ToolName: request.Tools[0].Name, Arguments: json.RawMessage(`{"city":"Paris"}`),
	})
	if err != nil {
		return err
	}
	data, _ := json.Marshal(map[string]string{"text": "Pi says: " + result.Text})
	return yield(RuntimeEvent{Seq: 2, RunID: runID, Type: "run.completed", Timestamp: time.Now(), Data: data})
}

type captureSender struct {
	done chan string
}

func (s *captureSender) SendAgentReply(_ context.Context, botID, recipient, text string) (string, error) {
	s.done <- botID + ":" + recipient + ":" + text
	return "provider-message-1", nil
}

func TestAppTransportResultIsInstallationBoundAndOnceOnly(t *testing.T) {
	transport := &AppTransport{pending: map[string]pendingTool{
		"call-1": {installationID: "inst-1", result: make(chan ToolResult, 1)},
	}}
	if err := transport.ResolveToolResult("inst-2", "call-1", ToolResult{Text: "wrong"}); err == nil {
		t.Fatal("accepted result from wrong installation")
	}
	if err := transport.ResolveToolResult("inst-1", "call-1", ToolResult{Text: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := transport.ResolveToolResult("inst-1", "call-1", ToolResult{Text: "duplicate"}); err == nil {
		t.Fatal("accepted duplicate result")
	}
}

func TestMessageToPiToolToOutboxEndToEnd(t *testing.T) {
	appServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope map[string]any
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Errorf("decode app event: %v", err)
		}
		if envelope["installation_id"] != "inst-1" {
			t.Errorf("installation = %v", envelope["installation_id"])
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"reply": "sunny", "reply_type": "text"})
	}))
	defer appServer.Close()

	s := memstore.New()
	s.AddBot(&store.Bot{ID: "bot-1", UserID: "tenant-1", Provider: "mock"})
	s.AddApp(&store.App{ID: "app-1", Name: "Weather", Slug: "weather", WebhookURL: appServer.URL, WebhookSecret: "secret", Tools: json.RawMessage(`[
		{"name":"weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]},"execution":{"effect":"read","idempotent":true}}
	]`)})
	s.AddInstallation(&store.AppInstallation{
		ID: "inst-1", AppID: "app-1", BotID: "bot-1", Enabled: true,
		AppName: "Weather", AppSlug: "weather", AppWebhookURL: appServer.URL, AppWebhookSecret: "secret",
	})
	profile := &store.AgentProfile{ID: "profile-1", OwnerID: "tenant-1", Runtime: "pi", ModelProfile: "default", Enabled: true}
	if err := s.CreateAgentProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := s.PutBotAgentSettings(&store.BotAgentSettings{BotID: "bot-1", ProfileID: profile.ID, RoutingMode: "agent"}); err != nil {
		t.Fatal(err)
	}

	catalog := &StoreCatalogResolver{Store: s}
	transport := NewAppTransport(s, appdelivery.NewDispatcher(s), appdelivery.NewWSHub())
	broker := &Broker{Catalog: catalog, Installations: s, Dispatcher: transport, AgentStore: s}
	runtime := &endToEndRuntime{broker: broker}
	sender := &captureSender{done: make(chan string, 1)}
	coordinator := &Coordinator{Store: s, Runtime: runtime, Catalog: catalog, Sender: sender, ServiceToken: "service-secret", Timeout: time.Second, MaxToolCalls: 2}

	accepted, err := coordinator.StartMessage(context.Background(), Inbound{
		BotID: "bot-1", TenantID: "tenant-1", Provider: "mock", SenderID: "user-1", MessageID: "message-1", Text: "weather?",
	})
	if err != nil || !accepted {
		t.Fatalf("start message: accepted=%v err=%v", accepted, err)
	}
	select {
	case got := <-sender.done:
		if got != "bot-1:user-1:Pi says: sunny" {
			t.Fatalf("reply = %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for final outbox reply")
	}
	items, err := s.ListPendingAgentOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("pending outbox = %d, want 0", len(items))
	}
}
