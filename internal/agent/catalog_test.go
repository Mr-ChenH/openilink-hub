package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/openilink/openilink-hub/internal/store"
)

type catalogStoreStub struct {
	installations []store.AppInstallation
	apps          map[string]*store.App
}

func (s *catalogStoreStub) ListInstallationsByBot(string) ([]store.AppInstallation, error) {
	return append([]store.AppInstallation(nil), s.installations...), nil
}
func (s *catalogStoreStub) GetApp(id string) (*store.App, error) { return s.apps[id], nil }

func TestResolveEffectiveToolsOverridesAndPreservesSchema(t *testing.T) {
	appTools := json.RawMessage(`[
		{"name":"search","description":"app search","parameters":{"type":"object","properties":{"state":{"type":"string","enum":["open","closed"]}},"required":["state"],"additionalProperties":false},"execution":{"effect":"read","idempotent":true}},
		{"name":"remove","parameters":{"type":"object"},"execution":{"effect":"destructive"}},
		{"name":"disabled","parameters":{"type":"object"}}
	]`)
	installationTools := json.RawMessage(`[
		{"name":"search","description":"installation search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]},"execution":{"effect":"read"}},
		{"name":"disabled","enabled":false},
		{"name":"create","parameters":{"type":"object"},"execution":{"effect":"write"}}
	]`)
	resolver := &StoreCatalogResolver{Store: &catalogStoreStub{
		installations: []store.AppInstallation{{ID: "inst/a", AppID: "app-1", BotID: "bot-1", Enabled: true, Tools: installationTools}},
		apps:          map[string]*store.App{"app-1": {ID: "app-1", Tools: appTools}},
	}}

	catalog, err := resolver.ResolveEffectiveTools(context.Background(), "bot-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Tools) != 3 {
		t.Fatalf("got %d tools, want 3", len(catalog.Tools))
	}
	byOriginal := make(map[string]EffectiveTool)
	for _, tool := range catalog.Tools {
		byOriginal[tool.Name] = tool
		if !regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`).MatchString(tool.ModelName) {
			t.Errorf("model name %q is not provider-safe", tool.ModelName)
		}
		if catalog.Lookup[tool.ModelName].InstallationID != "inst/a" {
			t.Errorf("lookup lost server-owned route for %q", tool.ModelName)
		}
	}
	search := byOriginal["search"]
	if search.Source != ToolSourceInstallation || search.Description != "installation search" {
		t.Fatalf("installation override not effective: %+v", search)
	}
	wantSchema := `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`
	if string(search.Parameters) != wantSchema {
		t.Fatalf("schema changed:\n got %s\nwant %s", search.Parameters, wantSchema)
	}
	if byOriginal["remove"].Policy != PolicyDeny || byOriginal["create"].Policy != PolicyDeny {
		t.Error("side-effecting tools must be denied by the default policy")
	}
	if search.Policy != PolicyAllow {
		t.Error("explicit read tool should use the default allow policy")
	}
}

func TestCatalogNamesAndVersionAreStableAcrossStoreOrder(t *testing.T) {
	tools := json.RawMessage(`[{"name":"Same name!","parameters":{"type":"object"},"execution":{"effect":"read"}}]`)
	first := store.AppInstallation{ID: "inst-1", AppID: "app-1", BotID: "bot", Enabled: true}
	second := store.AppInstallation{ID: "inst-2", AppID: "app-1", BotID: "bot", Enabled: true}
	app := &store.App{ID: "app-1", Tools: tools}
	resolve := func(installations []store.AppInstallation) *ToolCatalog {
		result, err := (&StoreCatalogResolver{Store: &catalogStoreStub{
			installations: installations, apps: map[string]*store.App{"app-1": app},
		}}).ResolveEffectiveTools(context.Background(), "bot")
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	a := resolve([]store.AppInstallation{first, second})
	b := resolve([]store.AppInstallation{second, first})
	if a.Version != b.Version {
		t.Fatalf("catalog version depends on store order: %q != %q", a.Version, b.Version)
	}
	if a.Tools[0].ModelName == a.Tools[1].ModelName {
		t.Fatal("same-named tools on different installations collided")
	}
	if a.Tools[0].ModelName != b.Tools[0].ModelName || a.Tools[1].ModelName != b.Tools[1].ModelName {
		t.Fatal("model names are not stable")
	}
}

func TestResolveEffectiveToolsRejectsCrossBotInstallation(t *testing.T) {
	resolver := &StoreCatalogResolver{Store: &catalogStoreStub{
		installations: []store.AppInstallation{{ID: "inst", AppID: "app", BotID: "other", Enabled: true}},
		apps:          map[string]*store.App{"app": {ID: "app", Tools: json.RawMessage(`[]`)}},
	}}
	if _, err := resolver.ResolveEffectiveTools(context.Background(), "bot"); err == nil {
		t.Fatal("expected ownership error")
	}
}

func TestDisabledOverrideRemovesAppTool(t *testing.T) {
	definitions, err := mergeToolDefinitions(
		json.RawMessage(`[{"name":"one"},{"name":"two"}]`),
		json.RawMessage(`[{"name":"one","enabled":false}]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || definitions[0].definition.Name != "two" {
		t.Fatalf("unexpected definitions: %+v", definitions)
	}
}
