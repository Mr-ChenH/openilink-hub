package agent

import (
	"context"
	"encoding/json"
	"time"
)

const ProtocolVersion = 1

// Runtime is the model-runtime boundary implemented by PiClient.
type Runtime interface {
	Start(ctx context.Context, request RunRequest) (RunHandle, error)
	Cancel(ctx context.Context, runID string) error
	Health(ctx context.Context) error
}

// RunRequest is the Hub-to-runtime v1 create contract. Credentials and run
// capabilities are HTTP headers and must never be placed in this model input.
type RunRequest struct {
	ProtocolVersion int           `json:"protocol_version"`
	RunID           string        `json:"run_id"`
	ConversationID  string        `json:"conversation_id"`
	SessionEpoch    int64         `json:"session_epoch"`
	Input           RunInput      `json:"input"`
	ModelProfile    string        `json:"model_profile"`
	SystemPrompt    string        `json:"system_prompt_version,omitempty"`
	CatalogVersion  string        `json:"catalog_version"`
	Tools           []RuntimeTool `json:"tools"`
	ToolCapability  string        `json:"tool_capability,omitempty"`
	Limits          RunLimits     `json:"limits"`
}

// RunInput identifies the once-only inbound message.
type RunInput struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

// RuntimeTool is the model-visible subset of EffectiveTool.
type RuntimeTool struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Parameters  json.RawMessage   `json:"parameters"`
	Execution   ExecutionMetadata `json:"execution"`
	Policy      PolicyAction      `json:"policy"`
}

// RuntimeTools returns a detached model-facing copy of a catalog.
func (c *ToolCatalog) RuntimeTools() []RuntimeTool {
	if c == nil {
		return nil
	}
	result := make([]RuntimeTool, len(c.Tools))
	for i, tool := range c.Tools {
		description := tool.Description
		if description == "" {
			description = tool.Name
		}
		result[i] = RuntimeTool{
			Name: tool.ModelName, Description: description,
			Parameters: cloneRaw(tool.Parameters), Execution: tool.Execution, Policy: tool.Policy,
		}
	}
	return result
}

// RunLimits are enforced independently by Hub and runtime.
type RunLimits struct {
	MaxToolCalls int   `json:"max_tool_calls"`
	TimeoutMS    int64 `json:"timeout_ms"`
}

// RunHandle is returned by an accepted or already-existing run.
type RunHandle struct {
	RunID  string    `json:"run_id"`
	Status RunStatus `json:"status"`
}

// RunStatus uses stable protocol values shared with persisted runtime state.
type RunStatus string

const (
	RunQueued              RunStatus = "queued"
	RunRunning             RunStatus = "running"
	RunWaitingTool         RunStatus = "waiting_tool"
	RunWaitingConfirmation RunStatus = "waiting_confirmation"
	RunCompleted           RunStatus = "completed"
	RunFailed              RunStatus = "failed"
	RunCancelled           RunStatus = "cancelled"
	RunInterrupted         RunStatus = "interrupted"
)

// RunState is returned by GET /v1/runs/{id}.
type RunState struct {
	RunHandle
	Text      string          `json:"text,omitempty"`
	ErrorCode string          `json:"error_code,omitempty"`
	Error     string          `json:"error,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
}

// RuntimeEvent is the persisted SSE event envelope. Data remains raw because
// each event type has its own versioned shape.
type RuntimeEvent struct {
	Seq       int64           `json:"seq"`
	RunID     string          `json:"run_id"`
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
}
