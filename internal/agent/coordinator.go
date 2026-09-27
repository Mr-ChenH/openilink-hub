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
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openilink/openilink-hub/internal/store"
)

type EventRuntime interface {
	Runtime
	StreamEvents(ctx context.Context, runID, lastEventID string, yield func(RuntimeEvent) error) error
}

type RecoverableEventRuntime interface {
	EventRuntime
	GetRun(ctx context.Context, runID string) (RunState, error)
}

type Inbound struct {
	BotID, TenantID, Provider, SenderID, GroupID, MessageID, Text string
	Explicit                                                      bool
}

type TriggerAuthorizer interface {
	AllowsMessage(ctx context.Context, settings *store.BotAgentSettings, in Inbound) (bool, error)
}

type OutboundSender interface {
	SendAgentReply(ctx context.Context, botID, recipient, text string) (string, error)
}

type DefinitelyUnsentError interface {
	error
	DefinitelyUnsent() bool
}

type KnownUnsentError struct{ Err error }

var errMalformedTerminalResult = errors.New("malformed terminal runtime result")

func (e *KnownUnsentError) Error() string          { return e.Err.Error() }
func (e *KnownUnsentError) Unwrap() error          { return e.Err }
func (e *KnownUnsentError) DefinitelyUnsent() bool { return true }

type Coordinator struct {
	Store           store.Store
	Runtime         EventRuntime
	Catalog         CatalogResolver
	Sender          OutboundSender
	ServiceToken    string
	Timeout         time.Duration
	MaxToolCalls    int
	OutboxBaseDelay time.Duration
	Policy          TriggerAuthorizer

	ownerOnce sync.Once
	ownerID   string
	active    sync.Map
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
	settings, err := c.Store.GetBotAgentSettings(in.BotID)
	if err != nil {
		return false, err
	}
	bot, err := c.Store.GetBot(in.BotID)
	if err != nil || bot.UserID != in.TenantID {
		return false, fmt.Errorf("agent: bot tenant mismatch")
	}
	var allowed bool
	if c.Policy == nil {
		policy, parseErr := ParseTriggerPolicy(settings.TriggerPolicy)
		if parseErr != nil {
			return false, parseErr
		}
		allowed = policy.Allows(in)
	} else {
		allowed, err = c.Policy.AllowsMessage(ctx, settings, in)
		if err != nil {
			return false, err
		}
	}
	if !allowed {
		return false, nil
	}
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
	if inserted {
		startCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err = c.Runtime.Start(startCtx, request)
		cancel()
		if err != nil {
			// A timed-out create may already have been accepted. Preserve the
			// durable run and reconcile by run_id instead of issuing a second run.
			c.startObserver(run.ID)
			return true, err
		}
	}
	c.startObserver(run.ID)
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

func (c *Coordinator) owner() string {
	c.ownerOnce.Do(func() { c.ownerID = "hub-" + uuid.NewString() })
	return c.ownerID
}

func (c *Coordinator) Run(ctx context.Context) {
	if c == nil || c.Store == nil || c.Runtime == nil {
		return
	}
	c.reconcile()
	c.DrainOutbox(ctx)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.reconcile()
			c.DrainOutbox(ctx)
		}
	}
}

func (c *Coordinator) reconcile() {
	runs, err := c.Store.ListNonterminalAgentRuns(500)
	if err != nil {
		slog.Error("agent run reconciliation failed", "err", err)
		return
	}
	for i := range runs {
		c.startObserver(runs[i].ID)
	}
}

func (c *Coordinator) startObserver(runID string) {
	if _, loaded := c.active.LoadOrStore(runID, struct{}{}); loaded {
		return
	}
	go func() {
		defer c.active.Delete(runID)
		c.observe(runID)
	}()
}

func (c *Coordinator) observe(runID string) {
	run, err := c.Store.GetAgentRun(runID)
	if err != nil {
		return
	}
	now := time.Now()
	fence, acquired, err := c.Store.AcquireAgentRunLease(runID, c.owner(), now.Unix(), now.Add(45*time.Second).Unix())
	if err != nil || !acquired {
		return
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(run.Deadline, 0).Add(30*time.Second))
	defer cancel()
	go c.renewLease(ctx, runID, fence)

	lastSeq, err := c.lastEventSeq(runID)
	if err != nil {
		slog.Error("agent event recovery failed", "run", runID, "err", err)
		return
	}
	for ctx.Err() == nil {
		streamCtx, streamCancel := context.WithTimeout(ctx, 30*time.Second)
		err = c.Runtime.StreamEvents(streamCtx, runID, strconv.FormatInt(lastSeq, 10), func(event RuntimeEvent) error {
			if event.Seq <= lastSeq {
				return nil
			}
			if event.RunID != runID || event.Seq != lastSeq+1 {
				return fmt.Errorf("agent: invalid event sequence for run %s", runID)
			}
			if err := c.handleEvent(runID, fence, event); err != nil {
				return err
			}
			lastSeq = event.Seq
			return nil
		})
		streamCancel()
		state, stateErr := c.runtimeState(runID)
		if stateErr == nil {
			if c.reconcileState(runID, fence, state) {
				return
			}
		} else if IsRuntimeNotFound(stateErr) {
			if current, getErr := c.Store.GetAgentRun(runID); getErr == nil {
				c.interrupt(current, fence, "runtime_state_lost", "The agent restarted before it could finish. Please try again.")
			}
			return
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			slog.Warn("agent event stream reconnecting", "run", runID, "after_seq", lastSeq, "err", err)
		}
		select {
		case <-ctx.Done():
			break
		case <-time.After(time.Second):
		}
	}
	if current, getErr := c.Store.GetAgentRun(runID); getErr == nil && current.Status != store.AgentRunCompleted && current.Status != store.AgentRunFailed && current.Status != store.AgentRunCancelled && current.Status != store.AgentRunInterrupted {
		cancelCtx, cancelRuntime := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.Runtime.Cancel(cancelCtx, runID)
		cancelRuntime()
		c.interrupt(current, fence, "runtime_observation_timeout", "The agent could not finish in time. Please try again.")
	}
}

func (c *Coordinator) renewLease(ctx context.Context, runID string, fence int64) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, err := c.Store.RenewAgentRunLease(runID, c.owner(), fence, time.Now().Add(45*time.Second).Unix())
			if err != nil || !ok {
				return
			}
		}
	}
}

func (c *Coordinator) lastEventSeq(runID string) (int64, error) {
	var last int64
	for {
		events, err := c.Store.ListAgentRunEvents(runID, last, 1000)
		if err != nil {
			return 0, err
		}
		if len(events) == 0 {
			return last, nil
		}
		last = events[len(events)-1].Seq
		if len(events) < 1000 {
			return last, nil
		}
	}
}

func (c *Coordinator) handleEvent(runID string, fence int64, event RuntimeEvent) error {
	payload := event.Data
	if len(payload) > maxRuntimeEventBytes {
		if run, err := c.Store.GetAgentRun(runID); err == nil && run.Fence == fence {
			_, _ = c.Store.TransitionAgentRunFenced(runID, fence, run.Status, store.AgentRunFailed, "runtime_event_too_large", "agent runtime event payload exceeds limit")
		}
		return fmt.Errorf("agent: runtime event payload exceeds limit")
	}
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if sanitized, allowed := sanitizeRuntimeEvent(event); allowed {
		if _, err := c.Store.AppendAgentRunEventFenced(&store.AgentRunEvent{RunID: runID, Seq: event.Seq, EventType: event.Type, SanitizedPayload: sanitized}, fence); err != nil {
			return err
		}
	}
	run, err := c.Store.GetAgentRun(runID)
	if err != nil || run.Fence != fence {
		return errors.New("agent: observer lease lost")
	}
	switch event.Type {
	case "run.started":
		_, err = c.Store.TransitionAgentRunFenced(runID, fence, store.AgentRunQueued, store.AgentRunRunning, "", "")
	case "run.completed":
		err = c.complete(run, fence, payload)
	case "run.failed":
		var data struct{ Code, Message string }
		_ = json.Unmarshal(payload, &data)
		_, err = c.Store.TransitionAgentRunFenced(runID, fence, run.Status, store.AgentRunFailed, data.Code, data.Message)
	case "run.cancelled":
		_, err = c.Store.TransitionAgentRunFenced(runID, fence, run.Status, store.AgentRunCancelled, "", "")
	case "run.interrupted":
		var data struct{ Code, Message string }
		_ = json.Unmarshal(payload, &data)
		c.interrupt(run, fence, data.Code, "The agent restarted before it could finish. Please try again.")
	}
	return err
}

func (c *Coordinator) runtimeState(runID string) (RunState, error) {
	runtime, ok := c.Runtime.(RecoverableEventRuntime)
	if !ok {
		return RunState{}, errors.New("agent: runtime does not support recovery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return runtime.GetRun(ctx, runID)
}

func (c *Coordinator) reconcileState(runID string, fence int64, state RunState) bool {
	run, err := c.Store.GetAgentRun(runID)
	if err != nil || run.Fence != fence {
		return true
	}
	switch state.Status {
	case RunCompleted:
		payload, _ := json.Marshal(map[string]string{"text": state.Text})
		if completeErr := c.complete(run, fence, payload); completeErr != nil {
			if !errors.Is(completeErr, errMalformedTerminalResult) {
				return false
			}
			changed, transitionErr := c.Store.TransitionAgentRunFenced(run.ID, fence, run.Status, store.AgentRunFailed, "invalid_runtime_result", completeErr.Error())
			return transitionErr == nil && changed
		}
		return true
	case RunFailed:
		_, _ = c.Store.TransitionAgentRunFenced(run.ID, fence, run.Status, store.AgentRunFailed, state.ErrorCode, state.Error)
		return true
	case RunCancelled:
		_, _ = c.Store.TransitionAgentRunFenced(run.ID, fence, run.Status, store.AgentRunCancelled, "", "")
		return true
	case RunInterrupted:
		c.interrupt(run, fence, state.ErrorCode, "The agent restarted before it could finish. Please try again.")
		return true
	default:
		return false
	}
}

func (c *Coordinator) complete(run *store.AgentRun, fence int64, payload json.RawMessage) error {
	if len(payload) > maxRuntimeEventBytes {
		return fmt.Errorf("%w: payload exceeds limit", errMalformedTerminalResult)
	}
	var data struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &data); err != nil || strings.TrimSpace(data.Text) == "" {
		return fmt.Errorf("%w: completed run has no text", errMalformedTerminalResult)
	}
	item, err := c.enqueueReply(run, "final", data.Text)
	if err != nil {
		return err
	}
	changed, err := c.Store.TransitionAgentRunFenced(run.ID, fence, run.Status, store.AgentRunCompleted, "", "")
	if changed && c.Sender != nil {
		c.deliverOutbox(context.Background(), item)
	}
	return err
}

func (c *Coordinator) interrupt(run *store.AgentRun, fence int64, code, message string) {
	if code == "" {
		code = "runtime_interrupted"
	}
	item, err := c.enqueueReply(run, "failure", message)
	changed, _ := c.Store.TransitionAgentRunFenced(run.ID, fence, run.Status, store.AgentRunInterrupted, code, message)
	if err == nil && changed && c.Sender != nil {
		c.deliverOutbox(context.Background(), item)
	}
}

func (c *Coordinator) enqueueReply(run *store.AgentRun, kind, text string) (*store.AgentOutboxItem, error) {
	conversation, err := c.Store.GetAgentConversation(run.ConversationID)
	if err != nil {
		return nil, err
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	id := uuid.NewString()
	item, _, err := c.Store.CreateAgentOutboxItem(&store.AgentOutboxItem{
		ID: id, RunID: run.ID, Kind: kind, Recipient: conversation.SenderID, Content: content,
		ContentRef: "agent-outbox:" + id, Status: store.AgentOutboxPending,
	})
	return item, err
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
	for i := range items {
		c.deliverOutbox(ctx, &items[i])
	}
}

func (c *Coordinator) deliverOutbox(parent context.Context, item *store.AgentOutboxItem) {
	if item.Status == store.AgentOutboxFailed {
		if !strings.HasPrefix(item.LastError, "known_unsent:") || time.Now().Before(time.Unix(item.UpdatedAt, 0).Add(outboxBackoff(c.OutboxBaseDelay, item.Attempt))) {
			return
		}
	} else if item.Status != store.AgentOutboxPending {
		return
	}
	var content struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(item.Content, &content) != nil || strings.TrimSpace(content.Text) == "" {
		return
	}
	run, err := c.Store.GetAgentRun(item.RunID)
	if err != nil || (run.Status != store.AgentRunCompleted && run.Status != store.AgentRunInterrupted) {
		return
	}
	changed, err := c.Store.TransitionAgentOutboxItem(item.ID, item.Status, store.AgentOutboxSending, "", "")
	if err != nil || !changed {
		return
	}
	sendCtx, cancel := context.WithTimeout(parent, 15*time.Second)
	clientID, sendErr := c.Sender.SendAgentReply(sendCtx, run.BotID, item.Recipient, content.Text)
	cancel()
	if sendErr != nil {
		var known DefinitelyUnsentError
		if errors.As(sendErr, &known) && known.DefinitelyUnsent() {
			_, _ = c.Store.TransitionAgentOutboxItem(item.ID, store.AgentOutboxSending, store.AgentOutboxFailed, "", "known_unsent:"+sendErr.Error())
		} else {
			_, _ = c.Store.TransitionAgentOutboxItem(item.ID, store.AgentOutboxSending, store.AgentOutboxDeliveryBlocked, "", "execution_unknown:"+sendErr.Error())
		}
		return
	}
	_, _ = c.Store.TransitionAgentOutboxItem(item.ID, store.AgentOutboxSending, store.AgentOutboxSent, clientID, "")
}

func outboxBackoff(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = 5 * time.Second
	}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<(attempt-1)) * base
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
	cancelCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.Runtime.Cancel(cancelCtx, runID); err != nil {
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
