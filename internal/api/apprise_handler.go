package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/openilink/openilink-hub/internal/provider"
	"github.com/openilink/openilink-hub/internal/store"
)

const maxAppriseBodyBytes = 1 << 20

type appriseJSONRequest struct {
	Version string `json:"version"`
	Title   string `json:"title"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

// handleAppriseJSON accepts Apprise's json/jsons notification format. The
// app_token is supplied as the HTTP Basic username so Apprise URLs do not need
// custom headers: jsons://<app_token>:x@hub.example.com/bot/v1/apprise.
func (s *Server) handleAppriseJSON(w http.ResponseWriter, r *http.Request) {
	token, _, ok := r.BasicAuth()
	if !ok || token == "" {
		botAPIError(w, "app token must be provided as the HTTP Basic username", http.StatusUnauthorized)
		return
	}

	inst, err := s.Store.GetInstallationByToken(token)
	if err != nil {
		botAPIError(w, "invalid app token", http.StatusUnauthorized)
		return
	}
	if !inst.Enabled {
		botAPIError(w, "app installation is disabled", http.StatusForbidden)
		return
	}
	if !s.requireScope(inst, "message:write") {
		botAPIError(w, "missing scope: message:write", http.StatusForbidden)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAppriseBodyBytes)
	var payload appriseJSONRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		botAPIError(w, "invalid Apprise JSON body", http.StatusBadRequest)
		return
	}
	payload.Title = strings.TrimSpace(payload.Title)
	payload.Message = strings.TrimSpace(payload.Message)
	if payload.Message == "" {
		botAPIError(w, "message is required", http.StatusBadRequest)
		return
	}

	content := payload.Message
	if payload.Title != "" {
		content = payload.Title + "\n" + payload.Message
	}
	if payload.Type != "" && payload.Type != "info" {
		content = fmt.Sprintf("[%s] %s", strings.ToUpper(payload.Type), content)
	}

	recipient := strings.TrimSpace(r.URL.Query().Get("to"))
	if recipient == "" {
		var config struct {
			Recipient string `json:"recipient"`
		}
		if json.Unmarshal(inst.Config, &config) == nil {
			recipient = strings.TrimSpace(config.Recipient)
		}
	}
	botInst, ok := s.BotManager.GetInstance(inst.BotID)
	if !ok {
		botAPIError(w, "bot not connected", http.StatusServiceUnavailable)
		return
	}
	if canSend, reason := s.checkSendability(inst.BotID, recipient, botInst.Status()); !canSend {
		botAPIError(w, reason, http.StatusConflict)
		return
	}

	clientID, err := botInst.Send(r.Context(), provider.OutboundMessage{
		Recipient:    recipient,
		Text:         content,
		ContextToken: s.contextTokenForRecipient(inst.BotID, recipient),
	})
	if err != nil {
		slog.Error("apprise send failed", "installation", inst.ID, "bot", inst.BotID, "err", err)
		botAPIError(w, "send failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	itemList, _ := json.Marshal([]map[string]any{{"type": "text", "text": content}})
	_, _ = s.Store.SaveMessage(&store.Message{
		BotID:       inst.BotID,
		Direction:   "outbound",
		ToUserID:    recipient,
		MessageType: 2,
		ItemList:    itemList,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "client_id": clientID})
}
