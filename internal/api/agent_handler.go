package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
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
			if agent.IsNotFound(err) {
				writeJSON(w, http.StatusOK, map[string]any{"bot_id": bot.ID, "routing_mode": "off"})
				return
			}
			jsonError(w, "load settings failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, settings)
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
		if _, err := s.Store.GetAgentProfile(req.ProfileID); err != nil {
			jsonError(w, "profile not found", http.StatusBadRequest)
			return
		}
	}
	settings := &store.BotAgentSettings{BotID: bot.ID, ProfileID: req.ProfileID, RoutingMode: req.RoutingMode, TriggerPolicy: req.TriggerPolicy, ToolPolicy: req.ToolPolicy}
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

func (s *Server) handleAgentRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.ownedRun(w, r)
	if !ok {
		return
	}
	events, _ := s.Store.ListAgentRunEvents(run.ID, 0, 1000)
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "events": events})
}

func (s *Server) handleAgentCancel(w http.ResponseWriter, r *http.Request) {
	_, ok := s.ownedRun(w, r)
	if !ok {
		return
	}
	if s.AgentCoordinator == nil {
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

func (s *Server) handleAgentProfileCreate(w http.ResponseWriter, r *http.Request) {
	var profile store.AgentProfile
	if json.NewDecoder(r.Body).Decode(&profile) != nil || profile.ModelProfile == "" {
		jsonError(w, "invalid profile", http.StatusBadRequest)
		return
	}
	profile.OwnerID = auth.UserIDFromContext(r.Context())
	profile.Runtime = "pi"
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
	var req store.AgentProfile
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		jsonError(w, "invalid profile", http.StatusBadRequest)
		return
	}
	req.ID = profile.ID
	req.OwnerID = profile.OwnerID
	req.Runtime = "pi"
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
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
