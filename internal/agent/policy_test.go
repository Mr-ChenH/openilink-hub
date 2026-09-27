package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openilink/openilink-hub/internal/store"
	"github.com/openilink/openilink-hub/internal/store/memstore"
)

func boolPointer(value bool) *bool { return &value }

func TestTriggerPolicyFailsClosedForGroupsAndAllowlists(t *testing.T) {
	policy, err := ParseTriggerPolicy(json.RawMessage(`{"groups":true,"require_explicit":true,"senders":["alice"],"group_ids":["trusted"]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		in   Inbound
		want bool
	}{
		{"allowed", Inbound{SenderID: "alice", GroupID: "trusted", Explicit: true}, true},
		{"wrong sender", Inbound{SenderID: "mallory", GroupID: "trusted", Explicit: true}, false},
		{"wrong group", Inbound{SenderID: "alice", GroupID: "other", Explicit: true}, false},
		{"implicit", Inbound{SenderID: "alice", GroupID: "trusted"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := policy.Allows(test.in); got != test.want {
				t.Fatalf("Allows()=%v want %v", got, test.want)
			}
		})
	}
	if _, err := ParseTriggerPolicy(json.RawMessage(`{"groups":"yes"}`)); err == nil {
		t.Fatal("accepted malformed trigger policy")
	}
	if _, err := ParseTriggerPolicy(json.RawMessage(`{"unknown":true}`)); err == nil {
		t.Fatal("accepted unknown trigger policy field")
	}
}

func TestStorePolicyUsesStableInstallationToolIdentity(t *testing.T) {
	s := memstore.New()
	s.AddBot(&store.Bot{ID: "bot", UserID: "owner"})
	if err := s.PutBotAgentSettings(&store.BotAgentSettings{
		BotID: "bot", ProfileID: "profile", RoutingMode: "agent",
		ToolPolicy: json.RawMessage(`{"default":"deny","tools":{"trusted/read":"allow","trusted/write":"confirm"}}`),
	}); err != nil {
		t.Fatal(err)
	}
	provider := &StorePolicy{Store: s}
	installation := store.AppInstallation{ID: "trusted", BotID: "bot"}
	for _, test := range []struct {
		name   string
		effect Effect
		want   PolicyAction
	}{{"read", EffectWrite, PolicyAllow}, {"write", EffectRead, PolicyConfirm}, {"other", EffectRead, PolicyDeny}} {
		action, err := provider.PolicyForTool(context.Background(), "bot", installation, ToolDefinition{Name: test.name, Execution: ExecutionMetadata{Effect: test.effect}})
		if err != nil {
			t.Fatal(err)
		}
		if action != test.want {
			t.Fatalf("%s action=%q want %q", test.name, action, test.want)
		}
	}
	other := installation
	other.ID = "attacker"
	action, err := provider.PolicyForTool(context.Background(), "bot", other, ToolDefinition{Name: "read", Execution: ExecutionMetadata{Effect: EffectRead}})
	if err != nil || action != PolicyDeny {
		t.Fatalf("cross-installation override leaked: action=%q err=%v", action, err)
	}
}

func TestTriggerPolicyPrivateDefaultRemainsEnabled(t *testing.T) {
	policy, err := ParseTriggerPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Allows(Inbound{SenderID: "sender"}) || policy.Allows(Inbound{SenderID: "sender", GroupID: "group"}) {
		t.Fatal("default policy must allow private messages and deny groups")
	}
	policy.Private = boolPointer(false)
	if policy.Allows(Inbound{SenderID: "sender"}) {
		t.Fatal("private=false did not deny private message")
	}
}
