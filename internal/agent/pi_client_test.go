package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestPiClientStartUsesProtocolAndAuthentication(t *testing.T) {
	var received RunRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/runs" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer service-secret" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"run_id":"run-1","status":"queued"}`)
	}))
	defer server.Close()

	client, err := NewPiClient(server.URL+"/", "service-secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := client.Start(context.Background(), RunRequest{RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if handle.RunID != "run-1" || handle.Status != RunQueued {
		t.Fatalf("unexpected handle: %+v", handle)
	}
	if received.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol version = %d", received.ProtocolVersion)
	}
}

func TestPiClientStreamEventsReplaysWithoutStartingRun(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/runs/run-1/events" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Last-Event-ID"); got != "7" {
			t.Errorf("Last-Event-ID = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "id: 8\nevent: run.completed\ndata: {\"seq\":8,\"run_id\":\"run-1\",\"type\":\"run.completed\",\"timestamp\":\"2026-09-27T00:00:00Z\"}\n\n")
	}))
	defer server.Close()
	client, _ := NewPiClient(server.URL, "", server.Client())
	var sequence []int64
	if err := client.StreamEvents(context.Background(), "run-1", "7", func(event RuntimeEvent) error {
		sequence = append(sequence, event.Seq)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sequence, []int64{8}) || requests != 1 {
		t.Fatalf("sequence=%v requests=%d", sequence, requests)
	}
}

func TestPiClientSurfacesBoundedHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, "different payload for existing run")
	}))
	defer server.Close()
	client, _ := NewPiClient(server.URL, "", server.Client())
	if _, err := client.Start(context.Background(), RunRequest{RunID: "run"}); err == nil {
		t.Fatal("expected conflict error")
	}
}

func TestRuntimeToolsDoNotExposeRoutingIdentifiers(t *testing.T) {
	catalog := testCatalog(PolicyAllow, EffectRead)
	tools := catalog.RuntimeTools()
	encoded, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || containsAny(string(encoded), "installation_id", "app_id", "original_name") {
		t.Fatalf("runtime tools expose server routing data: %s", encoded)
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
