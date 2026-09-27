// Package agent defines the Hub's runtime and tool-execution boundary.
//
// The package intentionally contains no persistence or HTTP routing. Callers
// inject the narrow stores, policy checks, and transports used by the catalog
// resolver and Broker.
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/openilink/openilink-hub/internal/store"
)

const maxModelToolName = 64

// Effect describes the externally visible side effect of a tool. Missing or
// unrecognized effects are deliberately treated as unknown, never as read.
type Effect string

const (
	EffectRead        Effect = "read"
	EffectWrite       Effect = "write"
	EffectDestructive Effect = "destructive"
	EffectUnknown     Effect = "unknown"
)

// PolicyAction is the effective tenant policy attached to a catalog entry.
type PolicyAction string

const (
	PolicyAllow   PolicyAction = "allow"
	PolicyConfirm PolicyAction = "confirm"
	PolicyDeny    PolicyAction = "deny"
)

// ExecutionMetadata is supplied by an app author. Idempotent only affects
// metadata exposed to a dispatcher; Broker itself never retries a dispatch.
type ExecutionMetadata struct {
	Effect     Effect `json:"effect"`
	Idempotent bool   `json:"idempotent"`
}

// ToolDefinition is the stored App.Tools/installation.Tools wire contract.
// Parameters is kept as raw JSON so constraints such as required, enum, and
// additionalProperties reach runtimes without lossy reconstruction.
type ToolDefinition struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Command     string            `json:"command,omitempty"`
	Parameters  json.RawMessage   `json:"parameters,omitempty"`
	Execution   ExecutionMetadata `json:"execution,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
}

// ToolSource identifies which level supplied the effective definition.
type ToolSource string

const (
	ToolSourceApp          ToolSource = "app"
	ToolSourceInstallation ToolSource = "installation"
)

// EffectiveTool is an authorized catalog entry presented to a model.
type EffectiveTool struct {
	ModelName      string            `json:"name"`
	Name           string            `json:"original_name"`
	Description    string            `json:"description"`
	Command        string            `json:"command,omitempty"`
	Parameters     json.RawMessage   `json:"parameters"`
	Execution      ExecutionMetadata `json:"execution"`
	Policy         PolicyAction      `json:"policy"`
	Source         ToolSource        `json:"source"`
	AppID          string            `json:"app_id"`
	InstallationID string            `json:"installation_id"`
	SchemaHash     string            `json:"schema_hash"`
}

// ToolCatalog is an immutable authorization snapshot. Lookup maps the model
// name back to server-owned routing data; callers must not parse ModelName.
type ToolCatalog struct {
	BotID   string                   `json:"bot_id"`
	Version string                   `json:"catalog_version"`
	Tools   []EffectiveTool          `json:"tools"`
	Lookup  map[string]EffectiveTool `json:"-"`
}

// CatalogStore is the minimum existing store surface needed for resolution.
type CatalogStore interface {
	ListInstallationsByBot(botID string) ([]store.AppInstallation, error)
	GetApp(id string) (*store.App, error)
}

// PolicyProvider resolves tenant policy after the app definition is known.
// A nil provider uses conservative defaults: explicit read tools require no
// confirmation; every other effect is denied.
type PolicyProvider interface {
	PolicyForTool(ctx context.Context, botID string, installation store.AppInstallation, tool ToolDefinition) (PolicyAction, error)
}

// StoreCatalogResolver constructs current tool catalogs from existing stores.
type StoreCatalogResolver struct {
	Store    CatalogStore
	Policies PolicyProvider
}

// ResolveEffectiveTools resolves all enabled installations owned by botID.
func (r *StoreCatalogResolver) ResolveEffectiveTools(ctx context.Context, botID string) (*ToolCatalog, error) {
	if r == nil || r.Store == nil {
		return nil, errors.New("agent: catalog store is required")
	}
	installations, err := r.Store.ListInstallationsByBot(botID)
	if err != nil {
		return nil, fmt.Errorf("agent: list installations: %w", err)
	}

	var effective []EffectiveTool
	for _, installation := range installations {
		if !installation.Enabled {
			continue
		}
		if installation.BotID != botID {
			return nil, fmt.Errorf("agent: installation %q does not belong to bot %q", installation.ID, botID)
		}
		app, err := r.Store.GetApp(installation.AppID)
		if err != nil {
			return nil, fmt.Errorf("agent: get app %q: %w", installation.AppID, err)
		}
		if app == nil || app.ID != installation.AppID {
			return nil, fmt.Errorf("agent: app %q is unavailable or mismatched", installation.AppID)
		}
		tools, err := mergeToolDefinitions(app.Tools, installation.Tools)
		if err != nil {
			return nil, fmt.Errorf("agent: resolve installation %q: %w", installation.ID, err)
		}
		for _, resolved := range tools {
			policy := defaultPolicy(resolved.definition.Execution.Effect)
			if r.Policies != nil {
				policy, err = r.Policies.PolicyForTool(ctx, botID, installation, resolved.definition)
				if err != nil {
					return nil, fmt.Errorf("agent: resolve policy for %q: %w", resolved.definition.Name, err)
				}
				if !validPolicy(policy) {
					return nil, fmt.Errorf("agent: invalid policy %q for tool %q", policy, resolved.definition.Name)
				}
			}
			schema := normalizeToolSchema(resolved.definition.Parameters)
			if len(bytes.TrimSpace(schema)) == 0 || bytes.Equal(bytes.TrimSpace(schema), []byte("null")) {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			if err := validateSchemaDocument(schema); err != nil {
				return nil, fmt.Errorf("agent: tool %q parameters: %w", resolved.definition.Name, err)
			}
			effect := normalizeEffect(resolved.definition.Execution.Effect)
			effective = append(effective, EffectiveTool{
				Name: resolved.definition.Name, Description: resolved.definition.Description,
				Command: resolved.definition.Command, Parameters: schema,
				Execution: ExecutionMetadata{Effect: effect, Idempotent: resolved.definition.Execution.Idempotent},
				Policy:    policy, Source: resolved.source, AppID: app.ID,
				InstallationID: installation.ID, SchemaHash: hashJSON(schema),
			})
		}
	}

	sort.Slice(effective, func(i, j int) bool {
		if effective[i].InstallationID != effective[j].InstallationID {
			return effective[i].InstallationID < effective[j].InstallationID
		}
		return effective[i].Name < effective[j].Name
	})
	lookup := make(map[string]EffectiveTool, len(effective))
	for i := range effective {
		effective[i].ModelName = modelSafeName(effective[i].InstallationID, effective[i].Name)
		if _, exists := lookup[effective[i].ModelName]; exists {
			return nil, fmt.Errorf("agent: model tool name collision for %q", effective[i].Name)
		}
		lookup[effective[i].ModelName] = effective[i]
	}
	catalog := &ToolCatalog{BotID: botID, Tools: effective, Lookup: lookup}
	catalog.Version = catalogVersion(catalog.Tools)
	return catalog, nil
}

// CatalogResolver is the boundary consumed by Broker.
type CatalogResolver interface {
	ResolveEffectiveTools(ctx context.Context, botID string) (*ToolCatalog, error)
}

type resolvedDefinition struct {
	definition ToolDefinition
	source     ToolSource
}

func mergeToolDefinitions(appRaw, installationRaw json.RawMessage) ([]resolvedDefinition, error) {
	appTools, err := decodeDefinitions(appRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid app tools: %w", err)
	}
	overrides, err := decodeDefinitions(installationRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid installation tools: %w", err)
	}
	byName := make(map[string]resolvedDefinition, len(appTools)+len(overrides))
	for _, tool := range appTools {
		if tool.Enabled != nil && !*tool.Enabled {
			continue
		}
		byName[tool.Name] = resolvedDefinition{definition: tool, source: ToolSourceApp}
	}
	for _, tool := range overrides {
		if tool.Enabled != nil && !*tool.Enabled {
			delete(byName, tool.Name)
			continue
		}
		byName[tool.Name] = resolvedDefinition{definition: tool, source: ToolSourceInstallation}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]resolvedDefinition, 0, len(names))
	for _, name := range names {
		result = append(result, byName[name])
	}
	return result, nil
}

func decodeDefinitions(raw json.RawMessage) ([]ToolDefinition, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var tools []ToolDefinition
	if err := json.Unmarshal(trimmed, &tools); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(tools))
	for i := range tools {
		tools[i].Name = strings.TrimSpace(tools[i].Name)
		if tools[i].Name == "" {
			return nil, errors.New("tool name is required")
		}
		if _, exists := seen[tools[i].Name]; exists {
			return nil, fmt.Errorf("duplicate tool name %q", tools[i].Name)
		}
		seen[tools[i].Name] = struct{}{}
	}
	return tools, nil
}

func normalizeToolSchema(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return cloneRaw(raw)
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(trimmed, &document) != nil {
		return cloneRaw(raw)
	}
	if _, ok := document["type"]; ok {
		return cloneRaw(raw)
	}
	// The established App API accepted a bare property map. Preserve that
	// contract while exposing a strict object schema to both AI runtimes.
	properties := make(map[string]any, len(document))
	var required []string
	for name, value := range document {
		var property map[string]any
		if json.Unmarshal(value, &property) != nil {
			properties[name] = value
			continue
		}
		if flag, ok := property["required"].(bool); ok {
			if flag {
				required = append(required, name)
			}
			delete(property, "required")
		}
		properties[name] = property
	}
	sort.Strings(required)
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return cloneRaw(raw)
	}
	return encoded
}

func validateSchemaDocument(raw json.RawMessage) error {
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("invalid JSON Schema JSON: %w", err)
	}
	if document == nil {
		return errors.New("JSON Schema must be an object")
	}
	_, err := resolveSchema(raw)
	return err
}

func resolveSchema(raw json.RawMessage) (*jsonschema.Resolved, error) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("decode JSON Schema: %w", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve JSON Schema: %w", err)
	}
	return resolved, nil
}

func normalizeEffect(effect Effect) Effect {
	switch effect {
	case EffectRead, EffectWrite, EffectDestructive:
		return effect
	default:
		return EffectUnknown
	}
}

func defaultPolicy(effect Effect) PolicyAction {
	if normalizeEffect(effect) == EffectRead {
		return PolicyAllow
	}
	return PolicyDeny
}

func validPolicy(policy PolicyAction) bool {
	return policy == PolicyAllow || policy == PolicyConfirm || policy == PolicyDeny
}

func modelSafeName(installationID, toolName string) string {
	prefix := "app_" + shortHash(installationID, 10) + "_"
	var b strings.Builder
	for _, r := range strings.ToLower(toolName) {
		if unicode.IsLetter(r) && r <= unicode.MaxASCII || unicode.IsDigit(r) && r <= unicode.MaxASCII || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	safe := strings.Trim(b.String(), "_")
	if safe == "" {
		safe = "tool"
	}
	suffix := "_" + shortHash(toolName, 8)
	maxBase := maxModelToolName - len(prefix) - len(suffix)
	if len(safe) > maxBase {
		safe = safe[:maxBase]
	}
	return prefix + safe + suffix
}

func catalogVersion(tools []EffectiveTool) string {
	h := sha256.New()
	for _, tool := range tools {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t\x00%s\n",
			tool.ModelName, tool.InstallationID, tool.AppID, tool.Name, tool.Description,
			tool.Command, tool.Source, tool.SchemaHash, tool.Execution.Effect,
			tool.Execution.Idempotent, tool.Policy)
	}
	return "cat_" + hex.EncodeToString(h.Sum(nil))[:20]
}

func hashJSON(raw json.RawMessage) string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		compact.Write(raw)
	}
	sum := sha256.Sum256(compact.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:])
}

func shortHash(value string, size int) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:size]
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
