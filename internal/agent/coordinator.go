package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openilink/openilink-hub/internal/store"
)

type EventRuntime interface {
	Runtime
	StreamEvents(ctx context.Context, runID, lastEventID string, yield func(RuntimeEvent) error) error
}

type Inbound struct {
	BotID, TenantID, Provider, SenderID, GroupID, MessageID, Text string
}

type OutboundSender interface {
	SendAgentReply(ctx context.Context, botID, recipient, text string) (string, error)
}

type Coordinator struct {
	Store        store.Store
	Runtime      EventRuntime
	Catalog      CatalogResolver
	Sender       OutboundSender
	ServiceToken string
	Timeout      time.Duration
	MaxToolCalls int
}

func (c *Coordinator) Enabled(botID string) bool {
	if c == nil || c.Runtime == nil || c.Store == nil || c.Catalog == nil || c.ServiceToken == "" {
		return false
	}
	settings, err := c.Store.GetBotAgentSettings(botID)
	if err != nil || settings.ProfileID == "" || settings.RoutingMode == "off" {
		return false
	}
	profile, err := c.Store.GetAgentProfile(settings.ProfileID)
	return err == nil && profile.Enabled && profile.Runtime == "pi"
}

func (c *Coordinator) StartMessage(ctx context.Context, in Inbound) (bool, error) {
	if !c.Enabled(in.BotID) {
		return false, nil
	}
	settings, _ := c.Store.GetBotAgentSettings(in.BotID)
	profile, err := c.Store.GetAgentProfile(settings.ProfileID)
	if err != nil {
		return true, err
	}
	conversation, _, err := c.Store.GetOrCreateAgentConversation(&store.AgentConversation{
		ID: uuid.NewString(), TenantID: in.TenantID, BotID: in.BotID, Provider: in.Provider,
		SenderID: in.SenderID, GroupID: in.GroupID, SessionRef: uuid.NewString(), Epoch: 1,
	})
	if err != nil {
		return true, fmt.Errorf("agent: create conversation: %w", err)
	}
	catalog, err := c.Catalog.ResolveEffectiveTools(ctx, in.BotID)
	if err != nil {
		return true, err
	}
	limits := c.limits(profile)
	run, inserted, err := c.Store.CreateAgentRun(&store.AgentRun{
		ID: uuid.NewString(), ConversationID: conversation.ID, BotID: in.BotID,
		InboundMessageID: in.MessageID, RunKind: "message", Status: store.AgentRunQueued,
		Runtime: "pi", CatalogVersion: catalog.Version, Deadline: time.Now().Add(time.Duration(limits.TimeoutMS) * time.Millisecond).Unix(),
	})
	if err != nil {
		return true, fmt.Errorf("agent: create run: %w", err)
	}
	request := RunRequest{
		ProtocolVersion: ProtocolVersion, RunID: run.ID, ConversationID: conversation.ID,
		SessionEpoch: conversation.Epoch, Input: RunInput{MessageID: in.MessageID, Text: in.Text},
		ModelProfile: profile.ModelProfile, SystemPrompt: profile.PromptVersion,
		CatalogVersion: catalog.Version, Tools: catalog.RuntimeTools(), ToolCapability: c.RunCapability(run.ID, in.BotID), Limits: limits,
	}
	if _, err := c.Runtime.Start(ctx, request); err != nil {
		if inserted {
			_, _ = c.Store.TransitionAgentRun(run.ID, store.AgentRunQueued, store.AgentRunFailed, "runtime_start_failed", err.Error())
		}
		return true, err
	}
	go c.observe(run.ID, in.SenderID)
	return true, nil
}

func (c *Coordinator) limits(profile *store.AgentProfile) RunLimits {
	limits := RunLimits{MaxToolCalls: c.MaxToolCalls, TimeoutMS: c.Timeout.Milliseconds()}
	if limits.MaxToolCalls <= 0 {
		limits.MaxToolCalls = 8
	}
	if limits.TimeoutMS <= 0 {
		limits.TimeoutMS = 90000
	}
	if len(profile.Limits) > 0 {
		_ = json.Unmarshal(profile.Limits, &limits)
	}
	if limits.MaxToolCalls <= 0 {
		limits.MaxToolCalls = 8
	}
	if limits.TimeoutMS <= 0 {
		limits.TimeoutMS = 90000
	}
	return limits
}

func (c *Coordinator) observe(runID, recipient string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err := c.Runtime.StreamEvents(ctx, runID, "", func(event RuntimeEvent) error {
		if event.RunID != "" && event.RunID != runID {
			return fmt.Errorf("agent: runtime event run mismatch")
		}
		payload := event.Data
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		if sanitized, allowed := sanitizeRuntimeEvent(event); allowed {
			if _, err := c.Store.AppendAgentRunEvent(&store.AgentRunEvent{RunID: runID, Seq: event.Seq, EventType: event.Type, SanitizedPayload: sanitized}); err != nil {
				return err
			}
		}
		switch event.Type {
		case "run.started":
			_, _ = c.Store.TransitionAgentRun(runID, store.AgentRunQueued, store.AgentRunRunning, "", "")
		case "run.completed":
			return c.complete(runID, recipient, payload)
		case "run.failed":
			var data struct{ Code, Message string }
			_ = json.Unmarshal(payload, &data)
			if run, err := c.Store.GetAgentRun(runID); err == nil {
				_, _ = c.Store.TransitionAgentRun(runID, run.Status, store.AgentRunFailed, data.Code, data.Message)
			}
		case "run.cancelled":
			if run, err := c.Store.GetAgentRun(runID); err == nil {
				_, _ = c.Store.TransitionAgentRun(runID, run.Status, store.AgentRunCancelled, "", "")
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("agent event stream ended", "run", runID, "err", err)
	}
}

func (c *Coordinator) complete(runID, recipient string, payload json.RawMessage) error {
	if len(payload) > maxRuntimeEventBytes {
		return fmt.Errorf("agent: completed run payload exceeds limit")
	}
	var data struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &data); err != nil || strings.TrimSpace(data.Text) == "" {
		return fmt.Errorf("agent: completed run has no text")
	}
	run, err := c.Store.GetAgentRun(runID)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(map[string]string{"text": data.Text})
	item, _, err := c.Store.CreateAgentOutboxItem(&store.AgentOutboxItem{
		ID: uuid.NewString(), RunID: runID, Kind: "final", Recipient: recipient, Content: content, Status: store.AgentOutboxPending,
	})
	if err != nil {
		return err
	}
	_, _ = c.Store.TransitionAgentRun(runID, run.Status, store.AgentRunCompleted, "", "")
	if c.Sender == nil {
		return nil
	}
	changed, err := c.Store.TransitionAgentOutboxItem(item.ID, store.AgentOutboxPending, store.AgentOutboxSending, "", "")
	if err != nil || !changed {
		return err
	}
	clientID, sendErr := c.Sender.SendAgentReply(context.Background(), run.BotID, recipient, data.Text)
	if sendErr != nil {
		_, _ = c.Store.TransitionAgentOutboxItem(item.ID, store.AgentOutboxSending, store.AgentOutboxFailed, "", sendErr.Error())
		return sendErr
	}
	_, err = c.Store.TransitionAgentOutboxItem(item.ID, store.AgentOutboxSending, store.AgentOutboxSent, clientID, "")
	return err
}

func (c *Coordinator) DrainOutbox(ctx context.Context) {
	if c == nil || c.Sender == nil {
		return
	}
	items, err := c.Store.ListPendingAgentOutbox(100)
	if err != nil {
		slog.Error("agent outbox recovery failed", "err", err)
		return
	}
	for _, item := range items {
		// Failed and sending deliveries have an unknown external effect and are
		// deliberately left for explicit operator action.
		if item.Status != store.AgentOutboxPending {
			continue
		}
		var content struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item.Content, &content) != nil || content.Text == "" {
			continue
		}
		run, err := c.Store.GetAgentRun(item.RunID)
		if err != nil {
			continue
		}
		changed, err := c.Store.TransitionAgentOutboxItem(item.ID, store.AgentOutboxPending, store.AgentOutboxSending, "", "")
		if err != nil || !changed {
			continue
		}
		clientID, sendErr := c.Sender.SendAgentReply(ctx, run.BotID, item.Recipient, content.Text)
		if sendErr != nil {
			_, _ = c.Store.TransitionAgentOutboxItem(item.ID, store.AgentOutboxSending, store.AgentOutboxFailed, "", sendErr.Error())
			continue
		}
		_, _ = c.Store.TransitionAgentOutboxItem(item.ID, store.AgentOutboxSending, store.AgentOutboxSent, clientID, "")
	}
}

func (c *Coordinator) RunCapability(runID, botID string) string {
	mac := hmac.New(sha256.New, []byte(c.ServiceToken))
	mac.Write([]byte(runID + "\x00" + botID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c *Coordinator) AuthenticateToolCall(serviceToken, capability, runID string) (*store.AgentRun, bool) {
	if c == nil || c.ServiceToken == "" || subtle.ConstantTimeCompare([]byte(serviceToken), []byte(c.ServiceToken)) != 1 {
		return nil, false
	}
	run, err := c.Store.GetAgentRun(runID)
	if err != nil {
		return nil, false
	}
	switch run.Status {
	case store.AgentRunCompleted, store.AgentRunFailed, store.AgentRunCancelled, store.AgentRunInterrupted:
		return nil, false
	}
	expected := c.RunCapability(runID, run.BotID)
	return run, subtle.ConstantTimeCompare([]byte(capability), []byte(expected)) == 1
}

func (c *Coordinator) Cancel(ctx context.Context, runID string) error {
	run, err := c.Store.GetAgentRun(runID)
	if err != nil {
		return err
	}
	if err := c.Runtime.Cancel(ctx, runID); err != nil {
		return err
	}
	_, err = c.Store.TransitionAgentRun(runID, run.Status, store.AgentRunCancelled, "", "")
	return err
}

func (c *Coordinator) ResetConversation(id string) (*store.AgentConversation, error) {
	return c.Store.ResetAgentConversation(id, uuid.NewString())
}

func Bearer(header string) string { return strings.TrimPrefix(header, "Bearer ") }
func MessageKey(external string, seq int64) string {
	if external != "" {
		return external
	}
	return strconv.FormatInt(seq, 10)
}
func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
