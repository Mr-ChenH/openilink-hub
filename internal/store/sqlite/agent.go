package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/openilink/openilink-hub/internal/store"
)

func jsonText(v json.RawMessage) string {
	if len(v) == 0 {
		return "{}"
	}
	return string(v)
}

func (db *DB) CreateAgentProfile(p *store.AgentProfile) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	_, err := db.Exec(`INSERT INTO agent_profiles (id,owner_id,runtime,model_profile,prompt_version,limits,enabled) VALUES (?,?,?,?,?,?,?)`, p.ID, p.OwnerID, p.Runtime, p.ModelProfile, p.PromptVersion, jsonText(p.Limits), p.Enabled)
	if err != nil {
		return err
	}
	got, err := db.GetAgentProfile(p.ID)
	if err == nil {
		*p = *got
	}
	return err
}
func scanAgentProfile(row interface{ Scan(...any) error }) (*store.AgentProfile, error) {
	var p store.AgentProfile
	var limits string
	err := row.Scan(&p.ID, &p.OwnerID, &p.Runtime, &p.ModelProfile, &p.PromptVersion, &limits, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	p.Limits = json.RawMessage(limits)
	return &p, err
}
func (db *DB) GetAgentProfile(id string) (*store.AgentProfile, error) {
	return scanAgentProfile(db.QueryRow(`SELECT id,owner_id,runtime,model_profile,prompt_version,limits,enabled,created_at,updated_at FROM agent_profiles WHERE id=?`, id))
}
func (db *DB) ListAgentProfilesByOwner(ownerID string) ([]store.AgentProfile, error) {
	rows, err := db.Query(`SELECT id,owner_id,runtime,model_profile,prompt_version,limits,enabled,created_at,updated_at FROM agent_profiles WHERE owner_id=? ORDER BY created_at,id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.AgentProfile
	for rows.Next() {
		p, err := scanAgentProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
func (db *DB) UpdateAgentProfile(p *store.AgentProfile) error {
	r, err := db.Exec(`UPDATE agent_profiles SET owner_id=?,runtime=?,model_profile=?,prompt_version=?,limits=?,enabled=?,updated_at=unixepoch() WHERE id=?`, p.OwnerID, p.Runtime, p.ModelProfile, p.PromptVersion, jsonText(p.Limits), p.Enabled, p.ID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	got, err := db.GetAgentProfile(p.ID)
	if err == nil {
		*p = *got
	}
	return err
}
func (db *DB) PutBotAgentSettings(s *store.BotAgentSettings) error {
	_, err := db.Exec(`INSERT INTO bot_agent_settings (bot_id,profile_id,routing_mode,trigger_policy,tool_policy) VALUES (?,?,?,?,?) ON CONFLICT(bot_id) DO UPDATE SET profile_id=excluded.profile_id,routing_mode=excluded.routing_mode,trigger_policy=excluded.trigger_policy,tool_policy=excluded.tool_policy,updated_at=unixepoch()`, s.BotID, s.ProfileID, s.RoutingMode, jsonText(s.TriggerPolicy), jsonText(s.ToolPolicy))
	if err != nil {
		return err
	}
	got, err := db.GetBotAgentSettings(s.BotID)
	if err == nil {
		*s = *got
	}
	return err
}
func (db *DB) GetBotAgentSettings(id string) (*store.BotAgentSettings, error) {
	var s store.BotAgentSettings
	var trigger, tool string
	if err := db.QueryRow(`SELECT bot_id,profile_id,routing_mode,trigger_policy,tool_policy,created_at,updated_at FROM bot_agent_settings WHERE bot_id=?`, id).Scan(&s.BotID, &s.ProfileID, &s.RoutingMode, &trigger, &tool, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.TriggerPolicy = json.RawMessage(trigger)
	s.ToolPolicy = json.RawMessage(tool)
	return &s, nil
}

func scanConversation(row interface{ Scan(...any) error }) (*store.AgentConversation, error) {
	var c store.AgentConversation
	err := row.Scan(&c.ID, &c.TenantID, &c.BotID, &c.Provider, &c.SenderID, &c.GroupID, &c.SessionRef, &c.Epoch, &c.LastCompletedRunID, &c.CreatedAt, &c.UpdatedAt)
	return &c, err
}

const conversationCols = `id,tenant_id,bot_id,provider,sender_id,group_id,session_ref,epoch,last_completed_run_id,created_at,updated_at`

func (db *DB) GetOrCreateAgentConversation(c *store.AgentConversation) (*store.AgentConversation, bool, error) {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	r, err := db.Exec(`INSERT INTO agent_conversations (id,tenant_id,bot_id,provider,sender_id,group_id,session_ref,epoch) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(tenant_id,bot_id,provider,sender_id,group_id) DO NOTHING`, c.ID, c.TenantID, c.BotID, c.Provider, c.SenderID, c.GroupID, c.SessionRef, max(c.Epoch, 1))
	if err != nil {
		return nil, false, err
	}
	n, _ := r.RowsAffected()
	got, err := scanConversation(db.QueryRow(`SELECT `+conversationCols+` FROM agent_conversations WHERE tenant_id=? AND bot_id=? AND provider=? AND sender_id=? AND group_id=?`, c.TenantID, c.BotID, c.Provider, c.SenderID, c.GroupID))
	return got, n == 1, err
}
func (db *DB) GetAgentConversation(id string) (*store.AgentConversation, error) {
	return scanConversation(db.QueryRow(`SELECT `+conversationCols+` FROM agent_conversations WHERE id=?`, id))
}
func (db *DB) ListAgentConversationsByBot(botID string, limit int) ([]store.AgentConversation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Query(`SELECT `+conversationCols+` FROM agent_conversations WHERE bot_id=? ORDER BY updated_at DESC,id DESC LIMIT ?`, botID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.AgentConversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}
func (db *DB) ResetAgentConversation(id, sessionRef string) (*store.AgentConversation, error) {
	r, err := db.Exec(`UPDATE agent_conversations SET session_ref=?,epoch=epoch+1,last_completed_run_id='',updated_at=unixepoch() WHERE id=?`, sessionRef, id)
	if err != nil {
		return nil, err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return nil, sql.ErrNoRows
	}
	return db.GetAgentConversation(id)
}

const runCols = `id,conversation_id,bot_id,inbound_message_id,run_kind,status,runtime,catalog_version,deadline,lease_owner,lease_until,fence,error_code,error_message,created_at,updated_at`

func scanRun(row interface{ Scan(...any) error }) (*store.AgentRun, error) {
	var r store.AgentRun
	err := row.Scan(&r.ID, &r.ConversationID, &r.BotID, &r.InboundMessageID, &r.RunKind, &r.Status, &r.Runtime, &r.CatalogVersion, &r.Deadline, &r.LeaseOwner, &r.LeaseUntil, &r.Fence, &r.ErrorCode, &r.ErrorMessage, &r.CreatedAt, &r.UpdatedAt)
	return &r, err
}
func (db *DB) CreateAgentRun(r *store.AgentRun) (*store.AgentRun, bool, error) {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.Status == "" {
		r.Status = store.AgentRunQueued
	}
	if r.RunKind == "" {
		r.RunKind = "message"
	}
	res, err := db.Exec(`INSERT INTO agent_runs (id,conversation_id,bot_id,inbound_message_id,run_kind,status,runtime,catalog_version,deadline) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(bot_id,inbound_message_id,run_kind) DO NOTHING`, r.ID, r.ConversationID, r.BotID, r.InboundMessageID, r.RunKind, r.Status, r.Runtime, r.CatalogVersion, r.Deadline)
	if err != nil {
		return nil, false, err
	}
	n, _ := res.RowsAffected()
	got, err := scanRun(db.QueryRow(`SELECT `+runCols+` FROM agent_runs WHERE bot_id=? AND inbound_message_id=? AND run_kind=?`, r.BotID, r.InboundMessageID, r.RunKind))
	if err != nil {
		return nil, false, err
	}
	if n == 0 && (got.ConversationID != r.ConversationID || got.Runtime != r.Runtime || got.CatalogVersion != r.CatalogVersion) {
		return nil, false, fmt.Errorf("agent run idempotency conflict")
	}
	return got, n == 1, nil
}
func (db *DB) GetAgentRun(id string) (*store.AgentRun, error) {
	return scanRun(db.QueryRow(`SELECT `+runCols+` FROM agent_runs WHERE id=?`, id))
}
func (db *DB) ListAgentRunsByBot(botID string, beforeCreatedAt int64, beforeID string, limit int) ([]store.AgentRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	query := `SELECT ` + runCols + ` FROM agent_runs WHERE bot_id=?`
	args := []any{botID}
	if beforeCreatedAt > 0 {
		query += ` AND (created_at<? OR (created_at=? AND id<?))`
		args = append(args, beforeCreatedAt, beforeCreatedAt, beforeID)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.AgentRun
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}
func (db *DB) ListNonterminalAgentRuns(limit int) ([]store.AgentRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(`SELECT `+runCols+` FROM agent_runs WHERE status IN ('queued','running','waiting_tool','waiting_confirmation') ORDER BY created_at,id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.AgentRun
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}
func (db *DB) TransitionAgentRun(id, from, to, code, message string) (bool, error) {
	if !store.ValidAgentRunTransition(from, to) {
		return false, fmt.Errorf("invalid agent run transition %s -> %s", from, to)
	}
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE agent_runs SET status=?,error_code=?,error_message=?,updated_at=unixepoch() WHERE id=? AND status=?`, to, code, message, id, from)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 1 && to == store.AgentRunCompleted {
		_, err = tx.Exec(`UPDATE agent_conversations SET last_completed_run_id=?,updated_at=unixepoch() WHERE id=(SELECT conversation_id FROM agent_runs WHERE id=?)`, id, id)
		if err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}
func (db *DB) TransitionAgentRunFenced(id string, fence int64, from, to, code, message string) (bool, error) {
	if !store.ValidAgentRunTransition(from, to) {
		return false, fmt.Errorf("invalid agent run transition %s -> %s", from, to)
	}
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE agent_runs SET status=?,error_code=?,error_message=?,updated_at=unixepoch() WHERE id=? AND status=? AND fence=?`, to, code, message, id, from, fence)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 1 && to == store.AgentRunCompleted {
		if _, err = tx.Exec(`UPDATE agent_conversations SET last_completed_run_id=?,updated_at=unixepoch() WHERE id=(SELECT conversation_id FROM agent_runs WHERE id=?)`, id, id); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}
func (db *DB) AcquireAgentRunLease(id, owner string, now, until int64) (int64, bool, error) {
	var fence int64
	err := db.QueryRow(`UPDATE agent_runs SET lease_owner=?,lease_until=?,fence=fence+1,updated_at=unixepoch() WHERE id=? AND status IN ('queued','running','waiting_tool','waiting_confirmation') AND (lease_until<=? OR lease_owner=?) RETURNING fence`, owner, until, id, now, owner).Scan(&fence)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return fence, err == nil, err
}
func (db *DB) RenewAgentRunLease(id, owner string, fence, until int64) (bool, error) {
	res, err := db.Exec(`UPDATE agent_runs SET lease_until=?,updated_at=unixepoch() WHERE id=? AND lease_owner=? AND fence=? AND status IN ('queued','running','waiting_tool','waiting_confirmation')`, until, id, owner, fence)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

const callCols = `id,run_id,installation_id,tool_name,arguments,args_hash,schema_hash,effect,status,attempt,result,result_ref,error_code,error_message,confirmation_id,created_at,updated_at`

func scanCall(row interface{ Scan(...any) error }) (*store.AgentToolCall, error) {
	var c store.AgentToolCall
	var args, result string
	err := row.Scan(&c.ID, &c.RunID, &c.InstallationID, &c.ToolName, &args, &c.ArgsHash, &c.SchemaHash, &c.Effect, &c.Status, &c.Attempt, &result, &c.ResultRef, &c.ErrorCode, &c.ErrorMessage, &c.ConfirmationID, &c.CreatedAt, &c.UpdatedAt)
	c.Arguments = json.RawMessage(args)
	c.Result = json.RawMessage(result)
	return &c, err
}
func (db *DB) CreateAgentToolCall(c *store.AgentToolCall) (*store.AgentToolCall, bool, error) {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if c.Status == "" {
		c.Status = store.AgentToolCreated
	}
	if c.Effect == "" {
		c.Effect = "unknown"
	}
	res, err := db.Exec(`INSERT INTO agent_tool_calls (id,run_id,installation_id,tool_name,arguments,args_hash,schema_hash,effect,status) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id,id) DO NOTHING`, c.ID, c.RunID, c.InstallationID, c.ToolName, jsonText(c.Arguments), c.ArgsHash, c.SchemaHash, c.Effect, c.Status)
	if err != nil {
		return nil, false, err
	}
	n, _ := res.RowsAffected()
	got, err := db.GetAgentToolCall(c.RunID, c.ID)
	if err != nil {
		return nil, false, err
	}
	if n == 0 && (got.InstallationID != c.InstallationID || got.ToolName != c.ToolName || got.ArgsHash != c.ArgsHash || got.SchemaHash != c.SchemaHash) {
		return nil, false, fmt.Errorf("agent tool call idempotency conflict")
	}
	return got, n == 1, nil
}
func (db *DB) GetAgentToolCall(run, id string) (*store.AgentToolCall, error) {
	return scanCall(db.QueryRow(`SELECT `+callCols+` FROM agent_tool_calls WHERE run_id=? AND id=?`, run, id))
}
func (db *DB) ListAgentToolCalls(runID string) ([]store.AgentToolCall, error) {
	rows, err := db.Query(`SELECT `+callCols+` FROM agent_tool_calls WHERE run_id=? ORDER BY created_at,id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.AgentToolCall
	for rows.Next() {
		call, err := scanCall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *call)
	}
	return out, rows.Err()
}
func (db *DB) TransitionAgentToolCall(run, id, from, to string, result json.RawMessage, resultRef, code, message string) (bool, error) {
	if !store.ValidAgentToolTransition(from, to) {
		return false, fmt.Errorf("invalid agent tool transition %s -> %s", from, to)
	}
	res, err := db.Exec(`UPDATE agent_tool_calls SET status=?,result=?,result_ref=?,error_code=?,error_message=?,attempt=attempt+CASE WHEN ?='dispatched' THEN 1 ELSE 0 END,updated_at=unixepoch() WHERE run_id=? AND id=? AND status=?`, to, jsonText(result), resultRef, code, message, to, run, id, from)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (db *DB) CreateAgentConfirmation(c *store.AgentConfirmation) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	_, err := db.Exec(`INSERT INTO agent_confirmations (id,run_id,call_id,owner_id,sender_id,code_hash,args_hash,decision,expires_at) VALUES (?,?,?,?,?,?,?,?,?)`, c.ID, c.RunID, c.CallID, c.OwnerID, c.SenderID, c.CodeHash, c.ArgsHash, c.Decision, c.ExpiresAt)
	return err
}
func (db *DB) GetAgentConfirmation(id string) (*store.AgentConfirmation, error) {
	var c store.AgentConfirmation
	err := db.QueryRow(`SELECT id,run_id,call_id,owner_id,sender_id,code_hash,args_hash,decision,expires_at,used_at,created_at FROM agent_confirmations WHERE id=?`, id).Scan(&c.ID, &c.RunID, &c.CallID, &c.OwnerID, &c.SenderID, &c.CodeHash, &c.ArgsHash, &c.Decision, &c.ExpiresAt, &c.UsedAt, &c.CreatedAt)
	return &c, err
}
func (db *DB) ConsumeAgentConfirmation(id, sender, code, args string, now int64) (bool, error) {
	res, err := db.Exec(`UPDATE agent_confirmations SET used_at=? WHERE id=? AND sender_id=? AND code_hash=? AND args_hash=? AND used_at=0 AND expires_at>=?`, now, id, sender, code, args, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (db *DB) AwaitAgentToolConfirmation(runID, callID string, c *store.AgentConfirmation) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	c.RunID, c.CallID = runID, callID
	res, err := tx.Exec(`UPDATE agent_tool_calls SET status='awaiting_confirmation',confirmation_id=?,updated_at=unixepoch() WHERE run_id=? AND id=? AND status='created'`, c.ID, runID, callID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return false, nil
	}
	res, err = tx.Exec(`UPDATE agent_runs SET status='waiting_confirmation',updated_at=unixepoch() WHERE id=? AND status IN ('running','waiting_tool')`, runID)
	if err != nil {
		return false, err
	}
	n, _ = res.RowsAffected()
	if n != 1 {
		return false, nil
	}
	_, err = tx.Exec(`INSERT INTO agent_confirmations (id,run_id,call_id,owner_id,sender_id,code_hash,args_hash,decision,expires_at) VALUES (?,?,?,?,?,?,?,?,?)`, c.ID, runID, callID, c.OwnerID, c.SenderID, c.CodeHash, c.ArgsHash, "", c.ExpiresAt)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (db *DB) ResolveAgentToolConfirmation(id, runID, callID, ownerID, argsHash, decision string, now int64) (bool, error) {
	if decision != "approve" && decision != "deny" {
		return false, fmt.Errorf("invalid confirmation decision")
	}
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE agent_confirmations SET used_at=?,decision=? WHERE id=? AND run_id=? AND call_id=? AND owner_id=? AND args_hash=? AND used_at=0 AND expires_at>=?`, now, decision, id, runID, callID, ownerID, argsHash, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return false, nil
	}
	status, code, message := store.AgentToolAuthorized, "", ""
	if decision == "deny" {
		status, code, message = store.AgentToolFailed, "permission_denied", "confirmation denied"
	}
	res, err = tx.Exec(`UPDATE agent_tool_calls SET status=?,error_code=?,error_message=?,updated_at=unixepoch() WHERE run_id=? AND id=? AND status='awaiting_confirmation' AND confirmation_id=?`, status, code, message, runID, callID, id)
	if err != nil {
		return false, err
	}
	n, _ = res.RowsAffected()
	if n != 1 {
		return false, nil
	}
	res, err = tx.Exec(`UPDATE agent_runs SET status='running',updated_at=unixepoch() WHERE id=? AND status='waiting_confirmation'`, runID)
	if err != nil {
		return false, err
	}
	n, _ = res.RowsAffected()
	if n != 1 {
		return false, nil
	}
	return true, tx.Commit()
}

func (db *DB) ExpireAgentToolConfirmation(id, runID, callID string, now int64) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE agent_confirmations SET used_at=?,decision='deny' WHERE id=? AND run_id=? AND call_id=? AND used_at=0 AND expires_at<?`, now, id, runID, callID, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return false, nil
	}
	res, err = tx.Exec(`UPDATE agent_tool_calls SET status='timed_out',error_code='confirmation_expired',error_message='confirmation expired',updated_at=unixepoch() WHERE run_id=? AND id=? AND status='awaiting_confirmation' AND confirmation_id=?`, runID, callID, id)
	if err != nil {
		return false, err
	}
	n, _ = res.RowsAffected()
	if n != 1 {
		return false, nil
	}
	res, err = tx.Exec(`UPDATE agent_runs SET status='running',updated_at=unixepoch() WHERE id=? AND status='waiting_confirmation'`, runID)
	if err != nil {
		return false, err
	}
	n, _ = res.RowsAffected()
	if n != 1 {
		return false, nil
	}
	return true, tx.Commit()
}

func (db *DB) AppendAgentRunEvent(e *store.AgentRunEvent) (bool, error) {
	res, err := db.Exec(`INSERT INTO agent_run_events (run_id,seq,event_type,sanitized_payload) VALUES (?,?,?,?) ON CONFLICT(run_id,seq) DO NOTHING`, e.RunID, e.Seq, e.EventType, jsonText(e.SanitizedPayload))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var typ, payload string
		if err = db.QueryRow(`SELECT event_type,sanitized_payload FROM agent_run_events WHERE run_id=? AND seq=?`, e.RunID, e.Seq).Scan(&typ, &payload); err != nil {
			return false, err
		}
		if typ != e.EventType || !bytes.Equal([]byte(payload), []byte(jsonText(e.SanitizedPayload))) {
			return false, fmt.Errorf("agent event idempotency conflict")
		}
	}
	return n == 1, nil
}
func (db *DB) AppendAgentRunEventFenced(e *store.AgentRunEvent, fence int64) (bool, error) {
	res, err := db.Exec(`INSERT INTO agent_run_events (run_id,seq,event_type,sanitized_payload) SELECT ?,?,?,? WHERE EXISTS (SELECT 1 FROM agent_runs WHERE id=? AND fence=?) ON CONFLICT(run_id,seq) DO NOTHING`, e.RunID, e.Seq, e.EventType, jsonText(e.SanitizedPayload), e.RunID, fence)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var typ, payload string
		err = db.QueryRow(`SELECT event_type,sanitized_payload FROM agent_run_events WHERE run_id=? AND seq=?`, e.RunID, e.Seq).Scan(&typ, &payload)
		if err == sql.ErrNoRows {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if typ != e.EventType || !bytes.Equal([]byte(payload), []byte(jsonText(e.SanitizedPayload))) {
			return false, fmt.Errorf("agent event idempotency conflict")
		}
	}
	return n == 1, nil
}
func (db *DB) ListAgentRunEvents(run string, after int64, limit int) ([]store.AgentRunEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := db.Query(`SELECT run_id,seq,event_type,sanitized_payload,created_at FROM agent_run_events WHERE run_id=? AND seq>? ORDER BY seq LIMIT ?`, run, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.AgentRunEvent
	for rows.Next() {
		var e store.AgentRunEvent
		var payload string
		if err := rows.Scan(&e.RunID, &e.Seq, &e.EventType, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.SanitizedPayload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

const outboxCols = `id,run_id,kind,recipient,content,content_ref,status,provider_client_id,attempt,last_error,created_at,updated_at`

func scanOutbox(row interface{ Scan(...any) error }) (*store.AgentOutboxItem, error) {
	var i store.AgentOutboxItem
	var content string
	err := row.Scan(&i.ID, &i.RunID, &i.Kind, &i.Recipient, &content, &i.ContentRef, &i.Status, &i.ProviderClientID, &i.Attempt, &i.LastError, &i.CreatedAt, &i.UpdatedAt)
	i.Content = json.RawMessage(content)
	return &i, err
}
func (db *DB) CreateAgentOutboxItem(i *store.AgentOutboxItem) (*store.AgentOutboxItem, bool, error) {
	if i.ID == "" {
		i.ID = uuid.NewString()
	}
	if i.Kind == "" {
		i.Kind = "final"
	}
	if i.Status == "" {
		i.Status = store.AgentOutboxPending
	}
	res, err := db.Exec(`INSERT INTO agent_outbox (id,run_id,kind,recipient,content,content_ref,status) VALUES (?,?,?,?,?,?,?) ON CONFLICT(run_id,kind) DO NOTHING`, i.ID, i.RunID, i.Kind, i.Recipient, jsonText(i.Content), i.ContentRef, i.Status)
	if err != nil {
		return nil, false, err
	}
	n, _ := res.RowsAffected()
	got, err := scanOutbox(db.QueryRow(`SELECT `+outboxCols+` FROM agent_outbox WHERE run_id=? AND kind=?`, i.RunID, i.Kind))
	if err != nil {
		return nil, false, err
	}
	return got, n == 1, nil
}
func (db *DB) GetAgentOutboxItem(id string) (*store.AgentOutboxItem, error) {
	return scanOutbox(db.QueryRow(`SELECT `+outboxCols+` FROM agent_outbox WHERE id=?`, id))
}
func (db *DB) ListPendingAgentOutbox(limit int) ([]store.AgentOutboxItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := db.Query(`SELECT `+outboxCols+` FROM agent_outbox WHERE status IN ('pending','failed') ORDER BY created_at,id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.AgentOutboxItem
	for rows.Next() {
		i, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}
func (db *DB) TransitionAgentOutboxItem(id, from, to, clientID, lastError string) (bool, error) {
	if !store.ValidAgentOutboxTransition(from, to) {
		return false, fmt.Errorf("invalid agent outbox transition %s -> %s", from, to)
	}
	res, err := db.Exec(`UPDATE agent_outbox SET status=?,provider_client_id=?,last_error=?,attempt=attempt+CASE WHEN ?='sending' THEN 1 ELSE 0 END,updated_at=unixepoch() WHERE id=? AND status=?`, to, clientID, lastError, to, id, from)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
