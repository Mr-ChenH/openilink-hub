package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/openilink/openilink-hub/internal/bot"
	"github.com/openilink/openilink-hub/internal/provider"
	"github.com/openilink/openilink-hub/internal/relay"
	"github.com/openilink/openilink-hub/internal/store"
	"github.com/openilink/openilink-hub/internal/store/sqlite"
)

type appriseTestProvider struct {
	mu   sync.Mutex
	last provider.OutboundMessage
}

func (p *appriseTestProvider) Name() string                                       { return "apprise-test" }
func (p *appriseTestProvider) Start(context.Context, provider.StartOptions) error { return nil }
func (p *appriseTestProvider) Stop()                                              {}
func (p *appriseTestProvider) Send(_ context.Context, msg provider.OutboundMessage) (string, error) {
	p.mu.Lock()
	p.last = msg
	p.mu.Unlock()
	return "apprise-client-id", nil
}
func (p *appriseTestProvider) SendTyping(context.Context, string, string, bool) error { return nil }
func (p *appriseTestProvider) GetConfig(context.Context, string, string) (*provider.BotConfig, error) {
	return nil, nil
}
func (p *appriseTestProvider) DownloadMedia(context.Context, *provider.Media) ([]byte, error) {
	return nil, nil
}
func (p *appriseTestProvider) DownloadVoice(context.Context, *provider.Media, int) ([]byte, error) {
	return nil, nil
}
func (p *appriseTestProvider) Status() string { return "connected" }

func TestAppriseJSON(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "apprise.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, err := db.CreateUserFull("apprise-user", "", "Apprise User", "hash", store.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	botRow, err := db.CreateBot(user.ID, "Apprise Bot", "apprise-test", "", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	var testProvider *appriseTestProvider
	provider.Register("apprise-test", func() provider.Provider {
		testProvider = &appriseTestProvider{}
		return testProvider
	})
	manager := bot.NewManager(db, relay.NewHub(), nil, nil, "")
	if err := manager.StartBot(context.Background(), botRow); err != nil {
		t.Fatal(err)
	}
	defer manager.StopAll()

	msgID := int64(9001)
	if _, err := db.SaveMessage(&store.Message{
		BotID: botRow.ID, Direction: "inbound", MessageID: &msgID,
		FromUserID: "user@im.wechat", ContextToken: "context-token",
	}); err != nil {
		t.Fatal(err)
	}

	app, err := db.CreateApp(&store.App{
		OwnerID: user.ID, Name: "Apprise", Slug: "apprise-json",
		Scopes: json.RawMessage(`["message:write"]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := db.InstallApp(app.ID, botRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateInstallation(inst.ID, "", json.RawMessage(`{"recipient":"user@im.wechat"}`), json.RawMessage(`["message:write"]`), true); err != nil {
		t.Fatal(err)
	}
	inst, err = db.GetInstallation(inst.ID)
	if err != nil {
		t.Fatal(err)
	}

	server := &Server{Store: db, BotManager: manager}
	body := []byte(`{"version":"1.0","title":"Disk alert","message":"Usage is 95%","type":"warning"}`)
	req := httptest.NewRequest(http.MethodPost, "/bot/v1/apprise", bytes.NewReader(body))
	req.SetBasicAuth(inst.AppToken, "")
	rec := httptest.NewRecorder()
	server.handleAppriseJSON(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	testProvider.mu.Lock()
	got := testProvider.last
	testProvider.mu.Unlock()
	if got.Recipient != "user@im.wechat" {
		t.Errorf("recipient = %q", got.Recipient)
	}
	if got.ContextToken != "context-token" {
		t.Errorf("context token = %q", got.ContextToken)
	}
	if got.Text != "[WARNING] Disk alert\nUsage is 95%" {
		t.Errorf("text = %q", got.Text)
	}
}

func TestAppriseJSONRejectsInvalidToken(t *testing.T) {
	server := &Server{Store: &invalidTokenStore{}}
	req := httptest.NewRequest(http.MethodPost, "/bot/v1/apprise", bytes.NewBufferString(`{"message":"test"}`))
	req.SetBasicAuth("invalid", "")
	rec := httptest.NewRecorder()
	server.handleAppriseJSON(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

type invalidTokenStore struct{ store.Store }

func (s *invalidTokenStore) GetInstallationByToken(string) (*store.AppInstallation, error) {
	return nil, context.Canceled
}
