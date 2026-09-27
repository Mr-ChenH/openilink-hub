package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openilink/openilink-hub/internal/store"
)

// Stable broker error codes are safe to expose across the runtime protocol.
const (
	CodeInvalidRequest     = "invalid_request"
	CodeInvalidArguments   = "invalid_arguments"
	CodeCatalogChanged     = "catalog_changed"
	CodeToolNotFound       = "tool_not_found"
	CodePermissionDenied   = "permission_denied"
	CodeConfirmationNeeded = "confirmation_required"
	CodeAppUnavailable     = "app_unavailable"
	CodeExecutionUnknown   = "execution_unknown"
)

// BrokerError is a classified tool-call failure.
type BrokerError struct {
	Code    string
	Message string
	Err     error
}

func (e *BrokerError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

func (e *BrokerError) Unwrap() error { return e.Err }

// InstallationReader rechecks ownership and enabled state at dispatch time.
type InstallationReader interface {
	GetInstallation(id string) (*store.AppInstallation, error)
}

// AuthorizationRequest contains only server-resolved identity and metadata.
type AuthorizationRequest struct {
	RunID     string
	CallID    string
	BotID     string
	Tool      EffectiveTool
	Arguments json.RawMessage
}

// Authorizer applies contact, allowlist, and confirmation policy. It must
// return a BrokerError when a stable error code is needed by the runtime.
type Authorizer interface {
	AuthorizeTool(ctx context.Context, request AuthorizationRequest) error
}

// DispatchRequest is sent to exactly one injected application adapter.
type DispatchRequest struct {
	RunID          string            `json:"run_id"`
	CallID         string            `json:"tool_call_id"`
	BotID          string            `json:"bot_id"`
	AppID          string            `json:"app_id"`
	InstallationID string            `json:"installation_id"`
	ToolName       string            `json:"tool_name"`
	Command        string            `json:"command,omitempty"`
	Arguments      json.RawMessage   `json:"arguments"`
	Execution      ExecutionMetadata `json:"execution"`
}

// ToolResult is the transport-neutral application result.
type ToolResult struct {
	Status string          `json:"status"`
	Output json.RawMessage `json:"output,omitempty"`
	Text   string          `json:"text,omitempty"`
	Code   string          `json:"code,omitempty"`
}

// ToolDispatcher selects and invokes the application transport. Broker invokes
// it once. Any safe retry for an explicitly idempotent read belongs in an
// adapter with durable attempt records, never as an implicit Broker fallback.
type ToolDispatcher interface {
	DispatchTool(ctx context.Context, request DispatchRequest) (ToolResult, error)
}

// ToolCallRequest is the runtime-to-Broker request. BotID is expected to come
// from an authenticated run capability, not an untrusted model argument.
type ToolCallRequest struct {
	RunID          string          `json:"run_id"`
	CallID         string          `json:"call_id"`
	BotID          string          `json:"-"`
	CatalogVersion string          `json:"catalog_version"`
	ToolName       string          `json:"tool_name"`
	Arguments      json.RawMessage `json:"arguments"`
}

// Broker validates current catalog, route ownership, and policy before one
// dispatch through injected interfaces.
type Broker struct {
	Catalog       CatalogResolver
	Installations InstallationReader
	Authorizer    Authorizer
	Dispatcher    ToolDispatcher
	AgentStore    store.AgentStore
}

// Execute resolves the current catalog on every call, preventing a stale model
// catalog from becoming permanent authorization.
func (b *Broker) Execute(ctx context.Context, request ToolCallRequest) (ToolResult, error) {
	if b == nil || b.Catalog == nil || b.Installations == nil || b.Dispatcher == nil {
		return ToolResult{}, brokerError(CodeInvalidRequest, "agent: broker dependencies are incomplete", nil)
	}
	if request.RunID == "" || request.CallID == "" || request.BotID == "" || request.CatalogVersion == "" || request.ToolName == "" {
		return ToolResult{}, brokerError(CodeInvalidRequest, "agent: run, call, bot, catalog, and tool are required", nil)
	}
	arguments := bytes.TrimSpace(request.Arguments)
	if len(arguments) == 0 {
		arguments = []byte(`{}`)
	}
	var object map[string]any
	if err := json.Unmarshal(arguments, &object); err != nil || object == nil {
		return ToolResult{}, brokerError(CodeInvalidRequest, "agent: tool arguments must be a JSON object", err)
	}

	catalog, err := b.Catalog.ResolveEffectiveTools(ctx, request.BotID)
	if err != nil {
		return ToolResult{}, fmt.Errorf("agent: resolve current catalog: %w", err)
	}
	if catalog == nil || catalog.BotID != request.BotID {
		return ToolResult{}, brokerError(CodePermissionDenied, "agent: catalog ownership mismatch", nil)
	}
	if catalog.Version != request.CatalogVersion {
		return ToolResult{}, brokerError(CodeCatalogChanged, "agent: tool catalog changed", nil)
	}
	tool, ok := catalog.Lookup[request.ToolName]
	if !ok {
		return ToolResult{}, brokerError(CodeToolNotFound, "agent: tool is not in the authorized catalog", nil)
	}
	resolvedSchema, err := resolveSchema(tool.Parameters)
	if err != nil {
		return ToolResult{}, brokerError(CodeCatalogChanged, "agent: tool schema is no longer valid", err)
	}
	if err := resolvedSchema.Validate(object); err != nil {
		return ToolResult{}, brokerError(CodeInvalidArguments, "agent: tool arguments do not match the catalog schema", err)
	}
	installation, err := b.Installations.GetInstallation(tool.InstallationID)
	if err != nil {
		return ToolResult{}, brokerError(CodeAppUnavailable, "agent: installation is unavailable", err)
	}
	if installation == nil || !installation.Enabled || installation.ID != tool.InstallationID || installation.BotID != request.BotID || installation.AppID != tool.AppID {
		return ToolResult{}, brokerError(CodePermissionDenied, "agent: installation ownership or authorization changed", nil)
	}
	if tool.Policy == PolicyDeny {
		return ToolResult{}, brokerError(CodePermissionDenied, "agent: tool policy denies execution", nil)
	}
	if tool.Policy == PolicyConfirm {
		return ToolResult{}, brokerError(CodeConfirmationNeeded, "agent: tool requires confirmation", nil)
	}
	authorization := AuthorizationRequest{
		RunID: request.RunID, CallID: request.CallID, BotID: request.BotID,
		Tool: tool, Arguments: cloneRaw(arguments),
	}
	if b.AgentStore != nil {
		sum := sha256.Sum256(arguments)
		call, inserted, err := b.AgentStore.CreateAgentToolCall(&store.AgentToolCall{
			ID: request.CallID, RunID: request.RunID, InstallationID: tool.InstallationID,
			ToolName: tool.Name, Arguments: cloneRaw(arguments), ArgsHash: hex.EncodeToString(sum[:]),
			SchemaHash: tool.SchemaHash, Effect: string(tool.Execution.Effect), Status: store.AgentToolCreated,
		})
		if err != nil {
			return ToolResult{}, brokerError(CodeInvalidRequest, "agent: conflicting tool call replay", err)
		}
		if !inserted {
			switch call.Status {
			case store.AgentToolSucceeded:
				var result ToolResult
				if err := json.Unmarshal(call.Result, &result); err != nil {
					return ToolResult{}, err
				}
				return result, nil
			case store.AgentToolDispatched, store.AgentToolUnknown:
				return ToolResult{}, brokerError(CodeExecutionUnknown, "agent: prior tool execution outcome is unknown", nil)
			default:
				return ToolResult{}, brokerError(CodeInvalidRequest, "agent: tool call replay is not executable", nil)
			}
		}
	}
	if b.Authorizer != nil {
		if err := b.Authorizer.AuthorizeTool(ctx, authorization); err != nil {
			var classified *BrokerError
			if errors.As(err, &classified) {
				return ToolResult{}, err
			}
			return ToolResult{}, brokerError(CodePermissionDenied, "agent: tool authorization denied", err)
		}
	}

	if b.AgentStore != nil {
		_, _ = b.AgentStore.TransitionAgentToolCall(request.RunID, request.CallID, store.AgentToolCreated, store.AgentToolAuthorized, nil, "", "", "")
		_, _ = b.AgentStore.TransitionAgentToolCall(request.RunID, request.CallID, store.AgentToolAuthorized, store.AgentToolDispatched, nil, "", "", "")
	}
	result, err := b.Dispatcher.DispatchTool(ctx, DispatchRequest{
		RunID: request.RunID, CallID: request.CallID, BotID: request.BotID,
		AppID: tool.AppID, InstallationID: tool.InstallationID,
		ToolName: tool.Name, Command: tool.Command, Arguments: cloneRaw(arguments),
		Execution: tool.Execution,
	})
	if err != nil {
		// A transport error after dispatch cannot prove that a write did not occur.
		if b.AgentStore != nil {
			var classified *BrokerError
			code := CodeExecutionUnknown
			if errors.As(err, &classified) && classified.Code != "" {
				code = classified.Code
			}
			_, _ = b.AgentStore.TransitionAgentToolCall(request.RunID, request.CallID, store.AgentToolDispatched, store.AgentToolUnknown, nil, "", code, err.Error())
		}
		return ToolResult{}, err
	}
	if result.Status == "failed" {
		err := brokerError(result.Code, result.Text, nil)
		if result.Code == "" {
			err = brokerError(CodeExecutionUnknown, "agent: application reported tool failure", nil)
		}
		if b.AgentStore != nil {
			encoded, _ := json.Marshal(result)
			_, _ = b.AgentStore.TransitionAgentToolCall(request.RunID, request.CallID, store.AgentToolDispatched, store.AgentToolFailed, encoded, "", result.Code, result.Text)
		}
		return ToolResult{}, err
	}
	if b.AgentStore != nil {
		encoded, _ := json.Marshal(result)
		_, _ = b.AgentStore.TransitionAgentToolCall(request.RunID, request.CallID, store.AgentToolDispatched, store.AgentToolSucceeded, encoded, "", "", "")
	}
	return result, nil
}

func brokerError(code, message string, err error) error {
	return &BrokerError{Code: code, Message: message, Err: err}
}
