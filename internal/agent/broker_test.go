package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openilink/openilink-hub/internal/store"
)

type fixedResolver struct{ catalog *ToolCatalog }

func (r fixedResolver) ResolveEffectiveTools(context.Context, string) (*ToolCatalog, error) {
	return r.catalog, nil
}

type installationReaderStub struct{ installation *store.AppInstallation }

func (s *installationReaderStub) GetInstallation(string) (*store.AppInstallation, error) {
	return s.installation, nil
}

type dispatcherStub struct {
	calls   int
	request DispatchRequest
	err     error
}

func (d *dispatcherStub) DispatchTool(_ context.Context, request DispatchRequest) (ToolResult, error) {
	d.calls++
	d.request = request
	return ToolResult{Status: "succeeded", Text: "done"}, d.err
}

func testCatalog(policy PolicyAction, effect Effect) *ToolCatalog {
	tool := EffectiveTool{
		ModelName: "app_123_search_456", Name: "search", AppID: "app",
		InstallationID: "inst", Policy: policy, Execution: ExecutionMetadata{Effect: effect},
		Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","enum":["open","closed"]}},"required":["query"],"additionalProperties":false}`),
	}
	return &ToolCatalog{
		BotID: "bot", Version: "cat_current", Tools: []EffectiveTool{tool},
		Lookup: map[string]EffectiveTool{tool.ModelName: tool},
	}
}

func testCall() ToolCallRequest {
	return ToolCallRequest{
		RunID: "run", CallID: "call", BotID: "bot", CatalogVersion: "cat_current",
		ToolName: "app_123_search_456", Arguments: json.RawMessage(`{"query":"open"}`),
	}
}

func TestBrokerDispatchesResolvedRouteExactlyOnce(t *testing.T) {
	dispatcher := &dispatcherStub{}
	broker := &Broker{
		Catalog: fixedResolver{testCatalog(PolicyAllow, EffectRead)},
		Installations: &installationReaderStub{&store.AppInstallation{
			ID: "inst", AppID: "app", BotID: "bot", Enabled: true,
		}},
		Dispatcher: dispatcher,
	}
	result, err := broker.Execute(context.Background(), testCall())
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "done" || dispatcher.calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, dispatcher.calls)
	}
	if dispatcher.request.ToolName != "search" || dispatcher.request.InstallationID != "inst" {
		t.Fatalf("dispatcher received model-controlled route: %+v", dispatcher.request)
	}
}

func TestBrokerRejectsStaleCatalogBeforeDispatch(t *testing.T) {
	dispatcher := &dispatcherStub{}
	broker := &Broker{
		Catalog:       fixedResolver{testCatalog(PolicyAllow, EffectRead)},
		Installations: &installationReaderStub{&store.AppInstallation{ID: "inst", AppID: "app", BotID: "bot", Enabled: true}},
		Dispatcher:    dispatcher,
	}
	request := testCall()
	request.CatalogVersion = "cat_stale"
	_, err := broker.Execute(context.Background(), request)
	assertBrokerCode(t, err, CodeCatalogChanged)
	if dispatcher.calls != 0 {
		t.Fatal("stale catalog was dispatched")
	}
}

func TestBrokerRechecksInstallationOwnership(t *testing.T) {
	dispatcher := &dispatcherStub{}
	broker := &Broker{
		Catalog:       fixedResolver{testCatalog(PolicyAllow, EffectRead)},
		Installations: &installationReaderStub{&store.AppInstallation{ID: "inst", AppID: "app", BotID: "other", Enabled: true}},
		Dispatcher:    dispatcher,
	}
	_, err := broker.Execute(context.Background(), testCall())
	assertBrokerCode(t, err, CodePermissionDenied)
	if dispatcher.calls != 0 {
		t.Fatal("cross-bot installation was dispatched")
	}
}

func TestBrokerEnforcesPolicyBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		policy PolicyAction
		code   string
	}{{PolicyDeny, CodePermissionDenied}, {PolicyConfirm, CodeConfirmationNeeded}} {
		t.Run(string(test.policy), func(t *testing.T) {
			dispatcher := &dispatcherStub{}
			broker := &Broker{
				Catalog:       fixedResolver{testCatalog(test.policy, EffectUnknown)},
				Installations: &installationReaderStub{&store.AppInstallation{ID: "inst", AppID: "app", BotID: "bot", Enabled: true}},
				Dispatcher:    dispatcher,
			}
			_, err := broker.Execute(context.Background(), testCall())
			assertBrokerCode(t, err, test.code)
			if dispatcher.calls != 0 {
				t.Fatal("policy-blocked tool was dispatched")
			}
		})
	}
}

func TestBrokerDoesNotRetryUnknownWriteFailure(t *testing.T) {
	dispatcher := &dispatcherStub{err: errors.New("timeout after request write")}
	broker := &Broker{
		Catalog:       fixedResolver{testCatalog(PolicyAllow, EffectWrite)},
		Installations: &installationReaderStub{&store.AppInstallation{ID: "inst", AppID: "app", BotID: "bot", Enabled: true}},
		Dispatcher:    dispatcher,
	}
	_, err := broker.Execute(context.Background(), testCall())
	if err == nil {
		t.Fatal("expected dispatch error")
	}
	if dispatcher.calls != 1 {
		t.Fatalf("write dispatch called %d times, want exactly one", dispatcher.calls)
	}
}

func TestBrokerValidatesPreservedJSONSchema(t *testing.T) {
	dispatcher := &dispatcherStub{}
	broker := &Broker{
		Catalog:       fixedResolver{testCatalog(PolicyAllow, EffectRead)},
		Installations: &installationReaderStub{&store.AppInstallation{ID: "inst", AppID: "app", BotID: "bot", Enabled: true}},
		Dispatcher:    dispatcher,
	}
	for _, arguments := range []string{`{}`, `{"query":"merged"}`, `{"query":"open","extra":true}`} {
		request := testCall()
		request.Arguments = json.RawMessage(arguments)
		_, err := broker.Execute(context.Background(), request)
		assertBrokerCode(t, err, CodeInvalidArguments)
	}
	if dispatcher.calls != 0 {
		t.Fatal("schema-invalid arguments were dispatched")
	}
}

func TestBrokerRequiresObjectArguments(t *testing.T) {
	dispatcher := &dispatcherStub{}
	broker := &Broker{
		Catalog:       fixedResolver{testCatalog(PolicyAllow, EffectRead)},
		Installations: &installationReaderStub{&store.AppInstallation{ID: "inst", AppID: "app", BotID: "bot", Enabled: true}},
		Dispatcher:    dispatcher,
	}
	request := testCall()
	request.Arguments = json.RawMessage(`[]`)
	_, err := broker.Execute(context.Background(), request)
	assertBrokerCode(t, err, CodeInvalidRequest)
}

func assertBrokerCode(t *testing.T, err error, want string) {
	t.Helper()
	var brokerErr *BrokerError
	if !errors.As(err, &brokerErr) {
		t.Fatalf("got %v, want BrokerError %q", err, want)
	}
	if brokerErr.Code != want {
		t.Fatalf("got code %q, want %q", brokerErr.Code, want)
	}
}
