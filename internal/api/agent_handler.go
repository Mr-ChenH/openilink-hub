package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openilink/openilink-hub/internal/agent"
	"github.com/openilink/openilink-hub/internal/auth"
	"github.com/openilink/openilink-hub/internal/store"
)

func (s *Server) handleAgentToolCall(w http.ResponseWriter, r *http.Request) {
	if s.AgentCoordinator == nil || s.AgentBroker == nil {
		jsonError(w, "agent runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	runID := r.PathValue("runID")
	run, ok := s.AgentCoordinator.AuthenticateToolCall(agent.Bearer(r.Header.Get("Authorization")), r.Header.Get("X-Agent-Run-Capability"), runID)
	if !ok {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		CallID         string          `json:"call_id"`
		ToolName       string          `json:"tool_name"`
		CatalogVersion string          `json:"catalog_version"`
		Arguments      json.RawMessage `json:"arguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	result, err := s.AgentBroker.Execute(r.Context(), agent.ToolCallRequest{
		RunID: runID, CallID: req.CallID, BotID: run.BotID, CatalogVersion: req.CatalogVersion,
		ToolName: req.ToolName, Arguments: req.Arguments,
	})
	if err != nil {
		status := http.StatusBadRequest
		code := agent.CodeInvalidRequest
		var brokerErr *agent.BrokerError
		if errors.As(err, &brokerErr) {
			code = brokerErr.Code
			switch code {
			case agent.CodePermissionDenied:
				status = http.StatusForbidden
			case agent.CodeConfirmationNeeded:
				status = http.StatusConflict
			case agent.CodeAppUnavailable:
				status = http.StatusServiceUnavailable
			case agent.CodeExecutionUnknown:
				status = http.StatusGatewayTimeout
			}
		}
		writeJSON(w, status, map[string]any{"status": "failed", "error": map[string]string{"code": code, "message": err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleAgentSettings(w http.ResponseWriter, r *http.Request) {
	bot, ok := s.ownedBot(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		settings, err := s.Store.GetBotAgentSettings(bot.ID)
		if err != nil {
			if !agent.IsNotFound(err) {
				jsonError(w, "load settings failed", http.StatusInternalServerError)
				return
			}
			settings = defaultBotAgentSettings(bot.ID)
		} else if settings == nil {
			settings = defaultBotAgentSettings(bot.ID)
		} else {
			normalizeBotAgentSettings(settings)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"settings":          settings,
			"runtime_available": s.agentRuntimeAvailable(),
		})
		return
	}
	var req struct {
		ProfileID     string          `json:"profile_id"`
		RoutingMode   string          `json:"routing_mode"`
		TriggerPolicy json.RawMessage `json:"trigger_policy"`
		ToolPolicy    json.RawMessage `json:"tool_policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.RoutingMode != "off" && req.ProfileID == "") {
		jsonError(w, "invalid settings", http.StatusBadRequest)
		return
	}
	if req.RoutingMode == "" {
		req.RoutingMode = "agent"
	}
	if req.RoutingMode != "off" && req.RoutingMode != "agent" {
		jsonError(w, "invalid routing_mode", http.StatusBadRequest)
		return
	}
	if req.ProfileID != "" {
		profile, err := s.Store.GetAgentProfile(req.ProfileID)
		if err != nil || profile.OwnerID != bot.UserID || profile.Runtime != "pi" || !profile.Enabled {
			jsonError(w, "profile not found", http.StatusBadRequest)
			return
		}
	}
	settings := &store.BotAgentSettings{BotID: bot.ID, ProfileID: req.ProfileID, RoutingMode: req.RoutingMode, TriggerPolicy: req.TriggerPolicy, ToolPolicy: req.ToolPolicy}
	normalizeBotAgentSettings(settings)
	if err := s.Store.PutBotAgentSettings(settings); err != nil {
		jsonError(w, "save settings failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleAgentTools(w http.ResponseWriter, r *http.Request) {
	bot, ok := s.ownedBot(w, r)
	if !ok {
		return
	}
	if s.AgentCatalog == nil {
		jsonError(w, "agent runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	catalog, err := s.AgentCatalog.ResolveEffectiveTools(r.Context(), bot.ID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, catalog)
}

func (s *Server) handleAgentRuns(w http.ResponseWriter, r *http.Request) {
	bot, ok := s.ownedBot(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	beforeAt, beforeID, err := parseAgentRunCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		jsonError(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	runs, err := s.Store.ListAgentRunsByBot(bot.ID, beforeAt, beforeID, limit+1)
	if err != nil {
		jsonError(w, "load runs failed", http.StatusInternalServerError)
		return
	}
	next := ""
	if len(runs) > limit {
		runs = runs[:limit]
		last := runs[len(runs)-1]
		next = fmt.Sprintf("%d.%s", last.CreatedAt, last.ID)
	}
	views := make([]agentRunView, len(runs))
	for i := range runs {
		views[i] = newAgentRunView(&runs[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": views, "next_cursor": next})
}

func (s *Server) handleAgentConversations(w http.ResponseWriter, r *http.Request) {
	bot, ok := s.ownedBot(w, r)
	if !ok {
		return
	}
	conversations, err := s.Store.ListAgentConversationsByBot(bot.ID, 100)
	if err != nil {
		jsonError(w, "load conversations failed", http.StatusInternalServerError)
		return
	}
	views := make([]agentConversationView, len(conversations))
	for i := range conversations {
		views[i] = newAgentConversationView(&conversations[i])
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleAgentRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.ownedRun(w, r)
	if !ok {
		return
	}
	events, err := s.Store.ListAgentRunEvents(run.ID, 0, 1000)
	if err != nil {
		jsonError(w, "load run events failed", http.StatusInternalServerError)
		return
	}
	conversation, err := s.Store.GetAgentConversation(run.ConversationID)
	if err != nil || conversation.BotID != run.BotID {
		jsonError(w, "conversation not found", http.StatusNotFound)
		return
	}
	calls, err := s.Store.ListAgentToolCalls(run.ID)
	if err != nil {
		jsonError(w, "load tool calls failed", http.StatusInternalServerError)
		return
	}
	callViews := make([]agentToolCallView, len(calls))
	for i := range calls {
		callViews[i] = agentToolCallView{
			ID: calls[i].ID, ToolName: calls[i].ToolName, Effect: calls[i].Effect,
			Status: calls[i].Status, ErrorCode: calls[i].ErrorCode,
			CreatedAt: calls[i].CreatedAt, UpdatedAt: calls[i].UpdatedAt,
		}
	}
	eventViews := make([]agentRunEventView, len(events))
	for i := range events {
		eventViews[i] = agentRunEventView{
			RunID: events[i].RunID, Seq: events[i].Seq,
			EventType: events[i].EventType, CreatedAt: events[i].CreatedAt,
		}
		var payload struct {
			ConfirmationID string `json:"confirmation_id"`
		}
		if json.Unmarshal(events[i].SanitizedPayload, &payload) == nil && payload.ConfirmationID != "" {
			eventViews[i].SanitizedPayload = map[string]string{"confirmation_id": payload.ConfirmationID}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run": newAgentRunView(run), "conversation": newAgentConversationView(conversation),
		"events": eventViews, "tool_calls": callViews,
	})
}

func (s *Server) handleAgentCancel(w http.ResponseWriter, r *http.Request) {
	_, ok := s.ownedRun(w, r)
	if !ok {
		return
	}
	if !s.agentRuntimeAvailable() {
		jsonError(w, "agent runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.AgentCoordinator.Cancel(r.Context(), r.PathValue("runID")); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentReset(w http.ResponseWriter, r *http.Request) {
	bot, ok := s.ownedBot(w, r)
	if !ok {
		return
	}
	if s.AgentCoordinator == nil {
		jsonError(w, "agent runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	conversation, err := s.Store.GetAgentConversation(r.PathValue("conversationID"))
	if err != nil || conversation.BotID != bot.ID {
		jsonError(w, "conversation not found", http.StatusNotFound)
		return
	}
	conversation, err = s.AgentCoordinator.ResetConversation(conversation.ID)
	if err != nil {
		jsonError(w, "reset failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, conversation)
}

func (s *Server) handleAgentConfirm(w http.ResponseWriter, r *http.Request) {
	run, ok := s.ownedRun(w, r)
	if !ok {
		return
	}
	confirmation, err := s.Store.GetAgentConfirmation(r.PathValue("confirmationID"))
	if err != nil {
		jsonError(w, "confirmation not found", http.StatusNotFound)
		return
	}
	call, err := s.Store.GetAgentToolCall(run.ID, confirmation.CallID)
	if err != nil {
		jsonError(w, "tool call not found", http.StatusNotFound)
		return
	}
	conversation, err := s.Store.GetAgentConversation(run.ConversationID)
	if err != nil {
		jsonError(w, "conversation not found", http.StatusNotFound)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Code == "" {
		jsonError(w, "code required", http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256([]byte(req.Code))
	consumed, err := s.Store.ConsumeAgentConfirmation(confirmation.ID, conversation.SenderID, hex.EncodeToString(sum[:]), call.ArgsHash, time.Now().Unix())
	if err != nil || !consumed {
		jsonError(w, "confirmation invalid or expired", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentProfileList(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.Store.ListAgentProfilesByOwner(auth.UserIDFromContext(r.Context()))
	if err != nil {
		jsonError(w, "load profiles failed", http.StatusInternalServerError)
		return
	}
	for i := range profiles {
		profiles[i].Limits = defaultJSON(profiles[i].Limits)
	}
	writeJSON(w, http.StatusOK, profiles)
}

func (s *Server) handleAgentProfileCreate(w http.ResponseWriter, r *http.Request) {
	var profile store.AgentProfile
	if json.NewDecoder(r.Body).Decode(&profile) != nil || profile.ModelProfile == "" {
		jsonError(w, "invalid profile", http.StatusBadRequest)
		return
	}
	profile.OwnerID = auth.UserIDFromContext(r.Context())
	profile.Runtime = "pi"
	profile.Limits = defaultJSON(profile.Limits)
	if err := s.Store.CreateAgentProfile(&profile); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusCreated, &profile)
}

func (s *Server) handleAgentProfileUpdate(w http.ResponseWriter, r *http.Request) {
	profile, err := s.Store.GetAgentProfile(r.PathValue("profileID"))
	if err != nil {
		jsonError(w, "profile not found", http.StatusNotFound)
		return
	}
	isAdminRoute := strings.HasPrefix(r.URL.Path, "/api/admin/")
	if !isAdminRoute && profile.OwnerID != auth.UserIDFromContext(r.Context()) {
		jsonError(w, "profile not found", http.StatusNotFound)
		return
	}
	var req store.AgentProfile
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		jsonError(w, "invalid profile", http.StatusBadRequest)
		return
	}
	req.ID = profile.ID
	req.OwnerID = profile.OwnerID
	req.Runtime = "pi"
	req.Limits = defaultJSON(req.Limits)
	if err := s.Store.UpdateAgentProfile(&req); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, &req)
}

func (s *Server) handleBotAPIToolResult(w http.ResponseWriter, r *http.Request) {
	inst := installationFromContext(r.Context())
	var req struct {
		CallID string          `json:"tool_call_id"`
		Status string          `json:"status"`
		Text   string          `json:"text"`
		Code   string          `json:"code"`
		Output json.RawMessage `json:"output"`
	}
	if s.AgentTransport == nil {
		botAPIError(w, "agent runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.CallID == "" {
		botAPIError(w, "invalid tool result", http.StatusBadRequest)
		return
	}
	if err := s.AgentTransport.ResolveToolResult(inst.ID, req.CallID, agent.ToolResult{Status: req.Status, Text: req.Text, Code: req.Code, Output: req.Output}); err != nil {
		botAPIError(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type agentRunView struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	BotID          string `json:"bot_id"`
	RunKind        string `json:"run_kind"`
	Status         string `json:"status"`
	Runtime        string `json:"runtime"`
	ErrorCode      string `json:"error_code,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

func newAgentRunView(run *store.AgentRun) agentRunView {
	return agentRunView{
		ID: run.ID, ConversationID: run.ConversationID, BotID: run.BotID,
		RunKind: run.RunKind, Status: run.Status, Runtime: run.Runtime,
		ErrorCode: run.ErrorCode, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
	}
}

type agentConversationView struct {
	ID                 string `json:"id"`
	BotID              string `json:"bot_id"`
	Provider           string `json:"provider"`
	Epoch              int64  `json:"epoch"`
	LastCompletedRunID string `json:"last_completed_run_id"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

func newAgentConversationView(conversation *store.AgentConversation) agentConversationView {
	return agentConversationView{
		ID: conversation.ID, BotID: conversation.BotID, Provider: conversation.Provider,
		Epoch: conversation.Epoch, LastCompletedRunID: conversation.LastCompletedRunID,
		CreatedAt: conversation.CreatedAt, UpdatedAt: conversation.UpdatedAt,
	}
}

type agentRunEventView struct {
	RunID            string            `json:"run_id"`
	Seq              int64             `json:"seq"`
	EventType        string            `json:"event_type"`
	SanitizedPayload map[string]string `json:"sanitized_payload"`
	CreatedAt        int64             `json:"created_at"`
}

type agentToolCallView struct {
	ID        string `json:"id"`
	ToolName  string `json:"tool_name"`
	Effect    string `json:"effect"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s *Server) agentRuntimeAvailable() bool {
	return s.AgentCoordinator != nil && s.AgentCoordinator.Runtime != nil && s.AgentCoordinator.Catalog != nil && s.AgentCoordinator.ServiceToken != ""
}

func parseAgentRunCursor(cursor string) (int64, string, error) {
	if cursor == "" {
		return 0, "", nil
	}
	separator := strings.IndexByte(cursor, '.')
	if separator < 1 || separator == len(cursor)-1 {
		return 0, "", errors.New("invalid cursor")
	}
	createdAt, err := strconv.ParseInt(cursor[:separator], 10, 64)
	if err != nil || createdAt <= 0 {
		return 0, "", errors.New("invalid cursor")
	}
	return createdAt, cursor[separator+1:], nil
}

func (s *Server) ownedBot(w http.ResponseWriter, r *http.Request) (*store.Bot, bool) {
	bot, err := s.Store.GetBot(r.PathValue("id"))
	if err != nil || bot.UserID != auth.UserIDFromContext(r.Context()) {
		jsonError(w, "bot not found", http.StatusNotFound)
		return nil, false
	}
	return bot, true
}
func (s *Server) ownedRun(w http.ResponseWriter, r *http.Request) (*store.AgentRun, bool) {
	run, err := s.Store.GetAgentRun(r.PathValue("runID"))
	if err != nil {
		jsonError(w, "run not found", http.StatusNotFound)
		return nil, false
	}
	bot, err := s.Store.GetBot(run.BotID)
	if err != nil || bot.UserID != auth.UserIDFromContext(r.Context()) {
		jsonError(w, "run not found", http.StatusNotFound)
		return nil, false
	}
	return run, true
}
func defaultJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func defaultBotAgentSettings(botID string) *store.BotAgentSettings {
	settings := &store.BotAgentSettings{BotID: botID, RoutingMode: "off"}
	normalizeBotAgentSettings(settings)
	return settings
}

func normalizeBotAgentSettings(settings *store.BotAgentSettings) {
	settings.TriggerPolicy = defaultJSON(settings.TriggerPolicy)
	settings.ToolPolicy = defaultJSON(settings.ToolPolicy)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		jsonError(w, "encode response failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
