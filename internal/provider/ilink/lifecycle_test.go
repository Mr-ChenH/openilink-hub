package ilink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNotifyLifecycle(t *testing.T) {
	var gotPath string
	var gotVersion string
	var gotAuthorization string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		var body struct {
			BaseInfo struct {
				ChannelVersion string `json:"channel_version"`
			} `json:"base_info"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotVersion = body.BaseInfo.ChannelVersion
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer srv.Close()

	p := &Provider{creds: Credentials{BotToken: "test-token", BaseURL: srv.URL}}
	if err := p.notifyLifecycle(context.Background(), "start"); err != nil {
		t.Fatalf("notifyLifecycle: %v", err)
	}
	if gotPath != "/ilink/bot/msg/notifystart" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuthorization != "Bearer test-token" {
		t.Errorf("authorization = %q", gotAuthorization)
	}
	if gotVersion != "2.4.6" {
		t.Errorf("channel_version = %q", gotVersion)
	}
}

func TestNotifyLifecycleRejectsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-14,"errmsg":"expired"}`))
	}))
	defer srv.Close()

	p := &Provider{creds: Credentials{BotToken: "test-token", BaseURL: srv.URL}}
	if err := p.notifyLifecycle(context.Background(), "stop"); err == nil {
		t.Fatal("expected API error")
	}
}
