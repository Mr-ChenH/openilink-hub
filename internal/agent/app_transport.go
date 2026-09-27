package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	appdelivery "github.com/openilink/openilink-hub/internal/app"
	"github.com/openilink/openilink-hub/internal/store"
)

type pendingTool struct {
	installationID string
	result         chan ToolResult
}

// AppTransport performs exactly one transport dispatch. It never falls back
// from WebSocket to HTTP because the effect of a failed write may be unknown.
type AppTransport struct {
	Store      store.Store
	Dispatcher *appdelivery.Dispatcher
	WSHub      *appdelivery.WSHub
	mu         sync.Mutex
	pending    map[string]pendingTool
}

func NewAppTransport(s store.Store, dispatcher *appdelivery.Dispatcher, hub *appdelivery.WSHub) *AppTransport {
	return &AppTransport{Store: s, Dispatcher: dispatcher, WSHub: hub, pending: make(map[string]pendingTool)}
}

func (t *AppTransport) DispatchTool(ctx context.Context, request DispatchRequest) (ToolResult, error) {
	installation, err := t.Store.GetInstallation(request.InstallationID)
	if err != nil || installation == nil || !installation.Enabled || installation.BotID != request.BotID || installation.AppID != request.AppID {
		return ToolResult{}, &BrokerError{Code: CodePermissionDenied, Message: "agent: installation is no longer authorized", Err: err}
	}
	command := request.Command
	if command == "" {
		command = request.ToolName
	}
	if t.WSHub != nil {
		conn := t.WSHub.Get(installation.ID)
		if conn == nil {
			conn = t.WSHub.GetAppLevel(installation.AppID)
		}
		if conn != nil {
			wait := make(chan ToolResult, 1)
			t.mu.Lock()
			if _, exists := t.pending[request.CallID]; exists {
				t.mu.Unlock()
				return ToolResult{}, &BrokerError{Code: CodeInvalidRequest, Message: "agent: tool call is already pending"}
			}
			t.pending[request.CallID] = pendingTool{installationID: installation.ID, result: wait}
			t.mu.Unlock()
			defer func() { t.mu.Lock(); delete(t.pending, request.CallID); t.mu.Unlock() }()
			err := conn.SendJSON(map[string]any{"type": "tool_call", "data": map[string]any{
				"run_id": request.RunID, "tool_call_id": request.CallID, "installation_id": installation.ID,
				"bot_id": request.BotID, "tool_name": request.ToolName, "command": command, "arguments": json.RawMessage(request.Arguments),
			}})
			if err != nil {
				return ToolResult{}, &BrokerError{Code: CodeExecutionUnknown, Message: "agent: WebSocket dispatch outcome is unknown", Err: err}
			}
			select {
			case result := <-wait:
				return result, nil
			case <-ctx.Done():
				return ToolResult{}, &BrokerError{Code: CodeExecutionUnknown, Message: "agent: tool result timed out after dispatch", Err: ctx.Err()}
			}
		}
	}
	if installation.AppWebhookURL == "" || t.Dispatcher == nil {
		return ToolResult{}, &BrokerError{Code: CodeAppUnavailable, Message: "agent: application has no available transport"}
	}
	var args map[string]any
	if err := json.Unmarshal(request.Arguments, &args); err != nil {
		return ToolResult{}, err
	}
	event := appdelivery.NewEvent("tool_call", map[string]any{
		"run_id": request.RunID, "tool_call_id": request.CallID, "command": command,
		"args": args, "sender": map[string]any{"role": "agent"},
	})
	result, err := t.Dispatcher.DeliverEvent(installation, event)
	if err != nil {
		return ToolResult{}, &BrokerError{Code: CodeExecutionUnknown, Message: "agent: HTTP tool execution outcome is unknown", Err: err}
	}
	if result == nil {
		return ToolResult{}, errors.New("agent: application returned no result")
	}
	output, _ := json.Marshal(map[string]any{"reply_type": result.ReplyType, "reply_url": result.ReplyURL, "reply_name": result.ReplyName})
	return ToolResult{Status: "succeeded", Text: result.Reply, Output: output}, nil
}

// ResolveToolResult correlates an app response and enforces installation binding.
// Duplicate or late results are rejected and cannot complete another call.
func (t *AppTransport) ResolveToolResult(installationID, callID string, result ToolResult) error {
	if installationID == "" || callID == "" {
		return errors.New("installation_id and tool_call_id are required")
	}
	t.mu.Lock()
	pending, ok := t.pending[callID]
	if ok && pending.installationID == installationID {
		delete(t.pending, callID)
	}
	t.mu.Unlock()
	if !ok || pending.installationID != installationID {
		return fmt.Errorf("tool call is not pending for this installation")
	}
	select {
	case pending.result <- result:
		return nil
	default:
		return errors.New("tool result was already resolved")
	}
}
