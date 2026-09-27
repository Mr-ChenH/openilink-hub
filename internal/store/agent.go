package store

import "encoding/json"

const (
	AgentRunQueued              = "queued"
	AgentRunRunning             = "running"
	AgentRunWaitingTool         = "waiting_tool"
	AgentRunWaitingConfirmation = "waiting_confirmation"
	AgentRunCompleted           = "completed"
	AgentRunFailed              = "failed"
	AgentRunCancelled           = "cancelled"
	AgentRunInterrupted         = "interrupted"

	AgentToolCreated              = "created"
	AgentToolAwaitingConfirmation = "awaiting_confirmation"
	AgentToolAuthorized           = "authorized"
	AgentToolDispatched           = "dispatched"
	AgentToolSucceeded            = "succeeded"
	AgentToolFailed               = "failed"
	AgentToolTimedOut             = "timed_out"
	AgentToolUnknown              = "unknown"

	AgentOutboxPending         = "pending"
	AgentOutboxSending         = "sending"
	AgentOutboxSent            = "sent"
	AgentOutboxFailed          = "failed"
	AgentOutboxDeliveryBlocked = "delivery_blocked"
)

func ValidAgentRunTransition(from, to string) bool {
	switch from {
	case AgentRunQueued:
		return to == AgentRunRunning || to == AgentRunFailed || to == AgentRunCancelled || to == AgentRunInterrupted
	case AgentRunRunning:
		return to == AgentRunWaitingTool || to == AgentRunWaitingConfirmation || to == AgentRunCompleted || to == AgentRunFailed || to == AgentRunCancelled || to == AgentRunInterrupted
	case AgentRunWaitingTool, AgentRunWaitingConfirmation:
		return to == AgentRunRunning || to == AgentRunFailed || to == AgentRunCancelled || to == AgentRunInterrupted
	default:
		return false
	}
}

func ValidAgentToolTransition(from, to string) bool {
	switch from {
	case AgentToolCreated:
		return to == AgentToolAwaitingConfirmation || to == AgentToolAuthorized || to == AgentToolFailed
	case AgentToolAwaitingConfirmation:
		return to == AgentToolAuthorized || to == AgentToolFailed || to == AgentToolTimedOut
	case AgentToolAuthorized:
		return to == AgentToolDispatched || to == AgentToolFailed
	case AgentToolDispatched:
		return to == AgentToolSucceeded || to == AgentToolFailed || to == AgentToolTimedOut || to == AgentToolUnknown
	default:
		return false
	}
}

func ValidAgentOutboxTransition(from, to string) bool {
	switch from {
	case AgentOutboxPending:
		return to == AgentOutboxSending || to == AgentOutboxFailed || to == AgentOutboxDeliveryBlocked
	case AgentOutboxSending:
		return to == AgentOutboxSent || to == AgentOutboxFailed || to == AgentOutboxDeliveryBlocked
	case AgentOutboxFailed:
		return to == AgentOutboxSending || to == AgentOutboxDeliveryBlocked
	default:
		return false
	}
}

type AgentProfile struct {
	ID            string          `json:"id"`
	OwnerID       string          `json:"owner_id"`
	Runtime       string          `json:"runtime"`
	ModelProfile  string          `json:"model_profile"`
	PromptVersion string          `json:"prompt_version"`
	Limits        json.RawMessage `json:"limits"`
	Enabled       bool            `json:"enabled"`
	CreatedAt     int64           `json:"created_at"`
	UpdatedAt     int64           `json:"updated_at"`
}

type BotAgentSettings struct {
	BotID         string          `json:"bot_id"`
	ProfileID     string          `json:"profile_id"`
	RoutingMode   string          `json:"routing_mode"`
	TriggerPolicy json.RawMessage `json:"trigger_policy"`
	ToolPolicy    json.RawMessage `json:"tool_policy"`
	CreatedAt     int64           `json:"created_at"`
	UpdatedAt     int64           `json:"updated_at"`
}

type AgentConversation struct {
	ID                 string `json:"id"`
	TenantID           string `json:"tenant_id"`
	BotID              string `json:"bot_id"`
	Provider           string `json:"provider"`
	SenderID           string `json:"sender_id"`
	GroupID            string `json:"group_id"`
	SessionRef         string `json:"session_ref"`
	Epoch              int64  `json:"epoch"`
	LastCompletedRunID string `json:"last_completed_run_id"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

type AgentRun struct {
	ID               string `json:"id"`
	ConversationID   string `json:"conversation_id"`
	BotID            string `json:"bot_id"`
	InboundMessageID string `json:"inbound_message_id"`
	RunKind          string `json:"run_kind"`
	Status           string `json:"status"`
	Runtime          string `json:"runtime"`
	CatalogVersion   string `json:"catalog_version"`
	Deadline         int64  `json:"deadline"`
	LeaseOwner       string `json:"lease_owner"`
	LeaseUntil       int64  `json:"lease_until"`
	Fence            int64  `json:"fence"`
	ErrorCode        string `json:"error_code"`
	ErrorMessage     string `json:"error_message"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

type AgentToolCall struct {
	ID             string          `json:"id"`
	RunID          string          `json:"run_id"`
	InstallationID string          `json:"installation_id"`
	ToolName       string          `json:"tool_name"`
	Arguments      json.RawMessage `json:"arguments"`
	ArgsHash       string          `json:"args_hash"`
	SchemaHash     string          `json:"schema_hash"`
	Effect         string          `json:"effect"`
	Status         string          `json:"status"`
	Attempt        int             `json:"attempt"`
	Result         json.RawMessage `json:"result"`
	ResultRef      string          `json:"result_ref"`
	ErrorCode      string          `json:"error_code"`
	ErrorMessage   string          `json:"error_message"`
	CreatedAt      int64           `json:"created_at"`
	UpdatedAt      int64           `json:"updated_at"`
}

type AgentConfirmation struct {
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	SenderID  string `json:"sender_id"`
	CodeHash  string `json:"code_hash"`
	ArgsHash  string `json:"args_hash"`
	ExpiresAt int64  `json:"expires_at"`
	UsedAt    int64  `json:"used_at"`
	CreatedAt int64  `json:"created_at"`
}

type AgentRunEvent struct {
	RunID            string          `json:"run_id"`
	Seq              int64           `json:"seq"`
	EventType        string          `json:"event_type"`
	SanitizedPayload json.RawMessage `json:"sanitized_payload"`
	CreatedAt        int64           `json:"created_at"`
}

type AgentOutboxItem struct {
	ID               string          `json:"id"`
	RunID            string          `json:"run_id"`
	Kind             string          `json:"kind"`
	Recipient        string          `json:"recipient"`
	Content          json.RawMessage `json:"content"`
	ContentRef       string          `json:"content_ref"`
	Status           string          `json:"status"`
	ProviderClientID string          `json:"provider_client_id"`
	Attempt          int             `json:"attempt"`
	LastError        string          `json:"last_error"`
	CreatedAt        int64           `json:"created_at"`
	UpdatedAt        int64           `json:"updated_at"`
}

// AgentStore persists the durable state shared by agent runtimes and the Hub.
// Create methods are idempotent: inserted is false when the same logical key
// and immutable payload already exist, and return an error on a conflicting replay.
type AgentStore interface {
	CreateAgentProfile(profile *AgentProfile) error
	GetAgentProfile(id string) (*AgentProfile, error)
	ListAgentProfilesByOwner(ownerID string) ([]AgentProfile, error)
	UpdateAgentProfile(profile *AgentProfile) error
	PutBotAgentSettings(settings *BotAgentSettings) error
	GetBotAgentSettings(botID string) (*BotAgentSettings, error)

	GetOrCreateAgentConversation(conversation *AgentConversation) (*AgentConversation, bool, error)
	GetAgentConversation(id string) (*AgentConversation, error)
	ListAgentConversationsByBot(botID string, limit int) ([]AgentConversation, error)
	ResetAgentConversation(id, sessionRef string) (*AgentConversation, error)

	CreateAgentRun(run *AgentRun) (*AgentRun, bool, error)
	GetAgentRun(id string) (*AgentRun, error)
	ListAgentRunsByBot(botID string, beforeCreatedAt int64, beforeID string, limit int) ([]AgentRun, error)
	TransitionAgentRun(id, fromStatus, toStatus, errorCode, errorMessage string) (bool, error)
	AcquireAgentRunLease(id, owner string, now, leaseUntil int64) (fence int64, acquired bool, err error)

	CreateAgentToolCall(call *AgentToolCall) (*AgentToolCall, bool, error)
	GetAgentToolCall(runID, callID string) (*AgentToolCall, error)
	ListAgentToolCalls(runID string) ([]AgentToolCall, error)
	TransitionAgentToolCall(runID, callID, fromStatus, toStatus string, result json.RawMessage, resultRef, errorCode, errorMessage string) (bool, error)

	CreateAgentConfirmation(confirmation *AgentConfirmation) error
	GetAgentConfirmation(id string) (*AgentConfirmation, error)
	ConsumeAgentConfirmation(id, senderID, codeHash, argsHash string, now int64) (bool, error)

	AppendAgentRunEvent(event *AgentRunEvent) (bool, error)
	ListAgentRunEvents(runID string, afterSeq int64, limit int) ([]AgentRunEvent, error)

	CreateAgentOutboxItem(item *AgentOutboxItem) (*AgentOutboxItem, bool, error)
	GetAgentOutboxItem(id string) (*AgentOutboxItem, error)
	ListPendingAgentOutbox(limit int) ([]AgentOutboxItem, error)
	TransitionAgentOutboxItem(id, fromStatus, toStatus, providerClientID, lastError string) (bool, error)
}
