package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/openilink/openilink-hub/internal/store"
)

// TriggerPolicy controls which inbound messages may allocate an agent run.
// Pointer booleans preserve the conservative rollout defaults when omitted.
type TriggerPolicy struct {
	Private         *bool    `json:"private,omitempty"`
	Groups          *bool    `json:"groups,omitempty"`
	RequireExplicit bool     `json:"require_explicit,omitempty"`
	Senders         []string `json:"senders,omitempty"`
	GroupIDs        []string `json:"group_ids,omitempty"`
}

type ToolPolicy struct {
	Default PolicyAction            `json:"default,omitempty"`
	Tools   map[string]PolicyAction `json:"tools,omitempty"`
}

// StorePolicy is the production settings-backed trigger and tool policy.
type StorePolicy struct{ Store store.Store }

func decodeStrictPolicy(raw json.RawMessage, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		trimmed = []byte(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func ParseTriggerPolicy(raw json.RawMessage) (TriggerPolicy, error) {
	var policy TriggerPolicy
	if err := decodeStrictPolicy(raw, &policy); err != nil {
		return policy, fmt.Errorf("invalid trigger_policy: %w", err)
	}
	if slices.Contains(policy.Senders, "") || slices.Contains(policy.GroupIDs, "") {
		return policy, errors.New("invalid trigger_policy: allowlist values must not be empty")
	}
	return policy, nil
}

func ParseToolPolicy(raw json.RawMessage) (ToolPolicy, error) {
	var policy ToolPolicy
	if err := decodeStrictPolicy(raw, &policy); err != nil {
		return policy, fmt.Errorf("invalid tool_policy: %w", err)
	}
	if policy.Default != "" && !validPolicy(policy.Default) {
		return policy, fmt.Errorf("invalid tool_policy default %q", policy.Default)
	}
	for name, action := range policy.Tools {
		if strings.TrimSpace(name) == "" || !validPolicy(action) {
			return policy, fmt.Errorf("invalid tool_policy entry %q", name)
		}
	}
	return policy, nil
}

func (p TriggerPolicy) Allows(in Inbound) bool {
	if in.SenderID == "" {
		return false
	}
	if len(p.Senders) > 0 && !slices.Contains(p.Senders, in.SenderID) {
		return false
	}
	if in.GroupID == "" {
		return p.Private == nil || *p.Private
	}
	if p.Groups == nil || !*p.Groups || len(p.GroupIDs) > 0 && !slices.Contains(p.GroupIDs, in.GroupID) {
		return false
	}
	return !p.RequireExplicit || in.Explicit
}

func StableToolPolicyName(installationID, toolName string) string {
	return installationID + "/" + toolName
}

func (p *StorePolicy) AllowsMessage(_ context.Context, settings *store.BotAgentSettings, in Inbound) (bool, error) {
	policy, err := ParseTriggerPolicy(settings.TriggerPolicy)
	if err != nil {
		return false, err
	}
	return policy.Allows(in), nil
}

func (p *StorePolicy) PolicyForTool(_ context.Context, botID string, installation store.AppInstallation, tool ToolDefinition) (PolicyAction, error) {
	if p == nil || p.Store == nil || installation.BotID != botID {
		return PolicyDeny, errors.New("agent: policy store or ownership is invalid")
	}
	settings, err := p.Store.GetBotAgentSettings(botID)
	if err != nil {
		return PolicyDeny, err
	}
	policy, err := ParseToolPolicy(settings.ToolPolicy)
	if err != nil {
		return PolicyDeny, err
	}
	if action, ok := policy.Tools[StableToolPolicyName(installation.ID, tool.Name)]; ok {
		return action, nil
	}
	if policy.Default != "" {
		return policy.Default, nil
	}
	return defaultPolicy(tool.Execution.Effect), nil
}

func (p *StorePolicy) AuthorizeTool(ctx context.Context, request AuthorizationRequest) error {
	installation, err := p.Store.GetInstallation(request.Tool.InstallationID)
	if err != nil || installation == nil {
		return brokerError(CodePermissionDenied, "agent: installation policy is unavailable", err)
	}
	action, err := p.PolicyForTool(ctx, request.BotID, *installation, ToolDefinition{Name: request.Tool.Name, Execution: request.Tool.Execution})
	if err != nil || action != request.Tool.Policy || action == PolicyDeny {
		return brokerError(CodePermissionDenied, "agent: tool authorization changed", err)
	}
	return nil
}
