package memstore

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/openilink/openilink-hub/internal/store"
)

func cloneJSON(v json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), v...) }
func profileCopy(p *store.AgentProfile) *store.AgentProfile {
	c := *p
	c.Limits = cloneJSON(p.Limits)
	return &c
}
func settingsCopy(v *store.BotAgentSettings) *store.BotAgentSettings {
	c := *v
	c.TriggerPolicy = cloneJSON(v.TriggerPolicy)
	c.ToolPolicy = cloneJSON(v.ToolPolicy)
	return &c
}
func conversationCopy(v *store.AgentConversation) *store.AgentConversation { c := *v; return &c }
func runCopy(v *store.AgentRun) *store.AgentRun                            { c := *v; return &c }
func callCopy(v *store.AgentToolCall) *store.AgentToolCall {
	c := *v
	c.Arguments = cloneJSON(v.Arguments)
	c.Result = cloneJSON(v.Result)
	return &c
}
func confirmationCopy(v *store.AgentConfirmation) *store.AgentConfirmation { c := *v; return &c }
func outboxCopy(v *store.AgentOutboxItem) *store.AgentOutboxItem {
	c := *v
	c.Content = cloneJSON(v.Content)
	return &c
}
func unixNow() int64 { return time.Now().Unix() }

func (s *Store) CreateAgentProfile(p *store.AgentProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if _, ok := s.agentProfiles[p.ID]; ok {
		return fmt.Errorf("agent profile already exists")
	}
	now := unixNow()
	p.CreatedAt = now
	p.UpdatedAt = now
	s.agentProfiles[p.ID] = profileCopy(p)
	return nil
}
func (s *Store) GetAgentProfile(id string) (*store.AgentProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.agentProfiles[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return profileCopy(p), nil
}
func (s *Store) ListAgentProfilesByOwner(ownerID string) ([]store.AgentProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.AgentProfile
	for _, p := range s.agentProfiles {
		if p.OwnerID == ownerID {
			out = append(out, *profileCopy(p))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out, nil
}
func (s *Store) UpdateAgentProfile(p *store.AgentProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.agentProfiles[p.ID]
	if !ok {
		return sql.ErrNoRows
	}
	p.CreatedAt = old.CreatedAt
	p.UpdatedAt = unixNow()
	s.agentProfiles[p.ID] = profileCopy(p)
	return nil
}
func (s *Store) PutBotAgentSettings(v *store.BotAgentSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := unixNow()
	if old := s.botAgentSettings[v.BotID]; old != nil {
		v.CreatedAt = old.CreatedAt
	} else {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	s.botAgentSettings[v.BotID] = settingsCopy(v)
	return nil
}
func (s *Store) GetBotAgentSettings(id string) (*store.BotAgentSettings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.botAgentSettings[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return settingsCopy(v), nil
}

func conversationKey(c *store.AgentConversation) string {
	return c.TenantID + "\x00" + c.BotID + "\x00" + c.Provider + "\x00" + c.SenderID + "\x00" + c.GroupID
}
func (s *Store) GetOrCreateAgentConversation(c *store.AgentConversation) (*store.AgentConversation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := conversationKey(c)
	if id, ok := s.conversationIndex[key]; ok {
		return conversationCopy(s.agentConversations[id]), false, nil
	}
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if c.Epoch < 1 {
		c.Epoch = 1
	}
	now := unixNow()
	c.CreatedAt = now
	c.UpdatedAt = now
	s.agentConversations[c.ID] = conversationCopy(c)
	s.conversationIndex[key] = c.ID
	return conversationCopy(c), true, nil
}
func (s *Store) GetAgentConversation(id string) (*store.AgentConversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.agentConversations[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return conversationCopy(v), nil
}
func (s *Store) ListAgentConversationsByBot(botID string, limit int) ([]store.AgentConversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []store.AgentConversation
	for _, v := range s.agentConversations {
		if v.BotID == botID {
			out = append(out, *conversationCopy(v))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].ID > out[j].ID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *Store) ResetAgentConversation(id, ref string) (*store.AgentConversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.agentConversations[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	v.SessionRef = ref
	v.Epoch++
	v.LastCompletedRunID = ""
	v.UpdatedAt = unixNow()
	return conversationCopy(v), nil
}

func runKey(v *store.AgentRun) string {
	return v.BotID + "\x00" + v.InboundMessageID + "\x00" + v.RunKind
}
func (s *Store) CreateAgentRun(v *store.AgentRun) (*store.AgentRun, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.RunKind == "" {
		v.RunKind = "message"
	}
	key := runKey(v)
	if id, ok := s.runIndex[key]; ok {
		got := s.agentRuns[id]
		if got.ConversationID != v.ConversationID || got.Runtime != v.Runtime || got.CatalogVersion != v.CatalogVersion {
			return nil, false, fmt.Errorf("agent run idempotency conflict")
		}
		return runCopy(got), false, nil
	}
	if _, ok := s.agentConversations[v.ConversationID]; !ok {
		return nil, false, fmt.Errorf("conversation: %w", sql.ErrNoRows)
	}
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	if v.Status == "" {
		v.Status = store.AgentRunQueued
	}
	now := unixNow()
	v.CreatedAt = now
	v.UpdatedAt = now
	s.agentRuns[v.ID] = runCopy(v)
	s.runIndex[key] = v.ID
	return runCopy(v), true, nil
}
func (s *Store) GetAgentRun(id string) (*store.AgentRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.agentRuns[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return runCopy(v), nil
}
func (s *Store) ListAgentRunsByBot(botID string, beforeCreatedAt int64, beforeID string, limit int) ([]store.AgentRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var out []store.AgentRun
	for _, v := range s.agentRuns {
		before := beforeCreatedAt == 0 || v.CreatedAt < beforeCreatedAt || (v.CreatedAt == beforeCreatedAt && v.ID < beforeID)
		if v.BotID == botID && before {
			out = append(out, *runCopy(v))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *Store) ListNonterminalAgentRuns(limit int) ([]store.AgentRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []store.AgentRun
	for _, v := range s.agentRuns {
		if v.Status == store.AgentRunQueued || v.Status == store.AgentRunRunning || v.Status == store.AgentRunWaitingTool || v.Status == store.AgentRunWaitingConfirmation {
			out = append(out, *runCopy(v))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *Store) TransitionAgentRun(id, from, to, code, message string) (bool, error) {
	if !store.ValidAgentRunTransition(from, to) {
		return false, fmt.Errorf("invalid agent run transition %s -> %s", from, to)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.agentRuns[id]
	if !ok || v.Status != from {
		return false, nil
	}
	v.Status = to
	v.ErrorCode = code
	v.ErrorMessage = message
	v.UpdatedAt = unixNow()
	if to == store.AgentRunCompleted {
		if c := s.agentConversations[v.ConversationID]; c != nil {
			c.LastCompletedRunID = id
			c.UpdatedAt = v.UpdatedAt
		}
	}
	return true, nil
}
func (s *Store) TransitionAgentRunFenced(id string, fence int64, from, to, code, message string) (bool, error) {
	if !store.ValidAgentRunTransition(from, to) {
		return false, fmt.Errorf("invalid agent run transition %s -> %s", from, to)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.agentRuns[id]
	if !ok || v.Status != from || v.Fence != fence {
		return false, nil
	}
	v.Status = to
	v.ErrorCode = code
	v.ErrorMessage = message
	v.UpdatedAt = unixNow()
	if to == store.AgentRunCompleted {
		if c := s.agentConversations[v.ConversationID]; c != nil {
			c.LastCompletedRunID = id
			c.UpdatedAt = v.UpdatedAt
		}
	}
	return true, nil
}
func (s *Store) AcquireAgentRunLease(id, owner string, now, until int64) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.agentRuns[id]
	if !ok {
		return 0, false, nil
	}
	if v.Status == store.AgentRunCompleted || v.Status == store.AgentRunFailed || v.Status == store.AgentRunCancelled || v.Status == store.AgentRunInterrupted {
		return 0, false, nil
	}
	if v.LeaseUntil > now && v.LeaseOwner != owner {
		return 0, false, nil
	}
	v.LeaseOwner = owner
	v.LeaseUntil = until
	v.Fence++
	v.UpdatedAt = unixNow()
	return v.Fence, true, nil
}
func (s *Store) RenewAgentRunLease(id, owner string, fence, until int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.agentRuns[id]
	if !ok || v.LeaseOwner != owner || v.Fence != fence || v.Status == store.AgentRunCompleted || v.Status == store.AgentRunFailed || v.Status == store.AgentRunCancelled || v.Status == store.AgentRunInterrupted {
		return false, nil
	}
	v.LeaseUntil = until
	v.UpdatedAt = unixNow()
	return true, nil
}

func callKey(run, id string) string { return run + "\x00" + id }
func (s *Store) CreateAgentToolCall(v *store.AgentToolCall) (*store.AgentToolCall, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	key := callKey(v.RunID, v.ID)
	if got, ok := s.agentToolCalls[key]; ok {
		if got.InstallationID != v.InstallationID || got.ToolName != v.ToolName || got.ArgsHash != v.ArgsHash || got.SchemaHash != v.SchemaHash {
			return nil, false, fmt.Errorf("agent tool call idempotency conflict")
		}
		return callCopy(got), false, nil
	}
	if _, ok := s.agentRuns[v.RunID]; !ok {
		return nil, false, fmt.Errorf("run: %w", sql.ErrNoRows)
	}
	if v.Status == "" {
		v.Status = store.AgentToolCreated
	}
	if v.Effect == "" {
		v.Effect = "unknown"
	}
	now := unixNow()
	v.CreatedAt = now
	v.UpdatedAt = now
	s.agentToolCalls[key] = callCopy(v)
	return callCopy(v), true, nil
}
func (s *Store) GetAgentToolCall(run, id string) (*store.AgentToolCall, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.agentToolCalls[callKey(run, id)]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return callCopy(v), nil
}
func (s *Store) ListAgentToolCalls(runID string) ([]store.AgentToolCall, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.AgentToolCall
	for _, v := range s.agentToolCalls {
		if v.RunID == runID {
			out = append(out, *callCopy(v))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out, nil
}
func (s *Store) TransitionAgentToolCall(run, id, from, to string, result json.RawMessage, resultRef, code, message string) (bool, error) {
	if !store.ValidAgentToolTransition(from, to) {
		return false, fmt.Errorf("invalid agent tool transition %s -> %s", from, to)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.agentToolCalls[callKey(run, id)]
	if !ok || v.Status != from {
		return false, nil
	}
	v.Status = to
	v.Result = cloneJSON(result)
	v.ResultRef = resultRef
	v.ErrorCode = code
	v.ErrorMessage = message
	if to == store.AgentToolDispatched {
		v.Attempt++
	}
	v.UpdatedAt = unixNow()
	return true, nil
}

func (s *Store) CreateAgentConfirmation(v *store.AgentConfirmation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	if _, ok := s.agentConfirmations[v.ID]; ok {
		return fmt.Errorf("confirmation already exists")
	}
	v.CreatedAt = unixNow()
	s.agentConfirmations[v.ID] = confirmationCopy(v)
	return nil
}
func (s *Store) GetAgentConfirmation(id string) (*store.AgentConfirmation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.agentConfirmations[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return confirmationCopy(v), nil
}
func (s *Store) ConsumeAgentConfirmation(id, sender, code, args string, now int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.agentConfirmations[id]
	if !ok || v.SenderID != sender || v.CodeHash != code || v.ArgsHash != args || v.UsedAt != 0 || v.ExpiresAt < now {
		return false, nil
	}
	v.UsedAt = now
	return true, nil
}

func (s *Store) AwaitAgentToolConfirmation(runID, callID string, confirmation *store.AgentConfirmation) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := s.agentToolCalls[callKey(runID, callID)]
	run := s.agentRuns[runID]
	if call == nil || run == nil || call.Status != store.AgentToolCreated || (run.Status != store.AgentRunRunning && run.Status != store.AgentRunWaitingTool) {
		return false, nil
	}
	if _, exists := s.agentConfirmations[confirmation.ID]; exists {
		return false, fmt.Errorf("confirmation already exists")
	}
	now := unixNow()
	confirmation.RunID, confirmation.CallID, confirmation.CreatedAt = runID, callID, now
	call.Status, call.ConfirmationID, call.UpdatedAt = store.AgentToolAwaitingConfirmation, confirmation.ID, now
	run.Status, run.UpdatedAt = store.AgentRunWaitingConfirmation, now
	s.agentConfirmations[confirmation.ID] = confirmationCopy(confirmation)
	return true, nil
}

func (s *Store) ResolveAgentToolConfirmation(id, runID, callID, ownerID, argsHash, decision string, now int64) (bool, error) {
	if decision != "approve" && decision != "deny" {
		return false, fmt.Errorf("invalid confirmation decision")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	confirmation := s.agentConfirmations[id]
	call := s.agentToolCalls[callKey(runID, callID)]
	run := s.agentRuns[runID]
	if confirmation == nil || call == nil || run == nil || confirmation.RunID != runID || confirmation.CallID != callID || confirmation.OwnerID != ownerID || confirmation.ArgsHash != argsHash || confirmation.UsedAt != 0 || confirmation.ExpiresAt < now || call.Status != store.AgentToolAwaitingConfirmation || call.ConfirmationID != id || run.Status != store.AgentRunWaitingConfirmation {
		return false, nil
	}
	confirmation.UsedAt, confirmation.Decision = now, decision
	if decision == "approve" {
		call.Status = store.AgentToolAuthorized
	} else {
		call.Status, call.ErrorCode, call.ErrorMessage = store.AgentToolFailed, "permission_denied", "confirmation denied"
	}
	call.UpdatedAt = now
	run.Status, run.UpdatedAt = store.AgentRunRunning, now
	return true, nil
}

func (s *Store) ExpireAgentToolConfirmation(id, runID, callID string, now int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	confirmation := s.agentConfirmations[id]
	call := s.agentToolCalls[callKey(runID, callID)]
	run := s.agentRuns[runID]
	if confirmation == nil || call == nil || run == nil || confirmation.RunID != runID || confirmation.CallID != callID || confirmation.UsedAt != 0 || confirmation.ExpiresAt >= now || call.Status != store.AgentToolAwaitingConfirmation || call.ConfirmationID != id || run.Status != store.AgentRunWaitingConfirmation {
		return false, nil
	}
	confirmation.UsedAt, confirmation.Decision = now, "deny"
	call.Status, call.ErrorCode, call.ErrorMessage, call.UpdatedAt = store.AgentToolTimedOut, "confirmation_expired", "confirmation expired", now
	run.Status, run.UpdatedAt = store.AgentRunRunning, now
	return true, nil
}

func (s *Store) AppendAgentRunEvent(v *store.AgentRunEvent) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentEvents[v.RunID] == nil {
		s.agentEvents[v.RunID] = make(map[int64]store.AgentRunEvent)
	}
	if got, ok := s.agentEvents[v.RunID][v.Seq]; ok {
		if got.EventType != v.EventType || !bytes.Equal(got.SanitizedPayload, v.SanitizedPayload) {
			return false, fmt.Errorf("agent event idempotency conflict")
		}
		return false, nil
	}
	c := *v
	c.SanitizedPayload = cloneJSON(v.SanitizedPayload)
	c.CreatedAt = unixNow()
	s.agentEvents[v.RunID][v.Seq] = c
	return true, nil
}
func (s *Store) AppendAgentRunEventFenced(v *store.AgentRunEvent, fence int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.agentRuns[v.RunID]
	if !ok || run.Fence != fence {
		return false, nil
	}
	if s.agentEvents[v.RunID] == nil {
		s.agentEvents[v.RunID] = make(map[int64]store.AgentRunEvent)
	}
	if got, ok := s.agentEvents[v.RunID][v.Seq]; ok {
		if got.EventType != v.EventType || !bytes.Equal(got.SanitizedPayload, v.SanitizedPayload) {
			return false, fmt.Errorf("agent event idempotency conflict")
		}
		return false, nil
	}
	c := *v
	c.SanitizedPayload = cloneJSON(v.SanitizedPayload)
	c.CreatedAt = unixNow()
	s.agentEvents[v.RunID][v.Seq] = c
	return true, nil
}
func (s *Store) ListAgentRunEvents(run string, after int64, limit int) ([]store.AgentRunEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var out []store.AgentRunEvent
	for seq, v := range s.agentEvents[run] {
		if seq > after {
			v.SanitizedPayload = cloneJSON(v.SanitizedPayload)
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func outboxKey(v *store.AgentOutboxItem) string { return v.RunID + "\x00" + v.Kind }
func (s *Store) CreateAgentOutboxItem(v *store.AgentOutboxItem) (*store.AgentOutboxItem, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.Kind == "" {
		v.Kind = "final"
	}
	key := outboxKey(v)
	if id, ok := s.outboxIndex[key]; ok {
		got := s.agentOutbox[id]
		if got.Recipient != v.Recipient || got.ContentRef != v.ContentRef || !bytes.Equal(got.Content, v.Content) {
			return nil, false, fmt.Errorf("agent outbox idempotency conflict")
		}
		return outboxCopy(got), false, nil
	}
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	if v.Status == "" {
		v.Status = store.AgentOutboxPending
	}
	now := unixNow()
	v.CreatedAt = now
	v.UpdatedAt = now
	s.agentOutbox[v.ID] = outboxCopy(v)
	s.outboxIndex[key] = v.ID
	return outboxCopy(v), true, nil
}
func (s *Store) GetAgentOutboxItem(id string) (*store.AgentOutboxItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.agentOutbox[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return outboxCopy(v), nil
}
func (s *Store) ListPendingAgentOutbox(limit int) ([]store.AgentOutboxItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var out []store.AgentOutboxItem
	for _, v := range s.agentOutbox {
		if v.Status == store.AgentOutboxPending || v.Status == store.AgentOutboxFailed {
			out = append(out, *outboxCopy(v))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *Store) TransitionAgentOutboxItem(id, from, to, client, lastError string) (bool, error) {
	if !store.ValidAgentOutboxTransition(from, to) {
		return false, fmt.Errorf("invalid agent outbox transition %s -> %s", from, to)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.agentOutbox[id]
	if !ok || v.Status != from {
		return false, nil
	}
	v.Status = to
	v.ProviderClientID = client
	v.LastError = lastError
	if to == store.AgentOutboxSending {
		v.Attempt++
	}
	v.UpdatedAt = unixNow()
	return true, nil
}
