package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openilink/openilink-hub/internal/store"
)

type contextKey string

const installationKey contextKey = "installation"

// installationFromContext returns the AppInstallation stored in the request context.
func installationFromContext(ctx context.Context) *store.AppInstallation {
	if v, ok := ctx.Value(installationKey).(*store.AppInstallation); ok {
		return v
	}
	return nil
}

// botAPIError writes a Bot API error response.
func botAPIError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

// appTokenAuth is middleware that authenticates requests using Bearer app_token
// and logs each API call.
func (s *Server) appTokenAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Extract Bearer token
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			botAPIError(w, "missing authorization header", http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(authHeader, "Bearer ") {
			botAPIError(w, "invalid authorization format", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == "" {
			botAPIError(w, "empty token", http.StatusUnauthorized)
			return
		}

		// Look up installation
		inst, err := s.Store.GetInstallationByToken(token)
		if err != nil {
			slog.Warn("bot api auth: token lookup failed", "err", err)
			botAPIError(w, "invalid token", http.StatusUnauthorized)
			return
		}

		// Check enabled
		if !inst.Enabled {
			botAPIError(w, "app installation is disabled", http.StatusForbidden)
			return
		}

		// Read request body for logging (buffer it so handler can re-read)
		var reqBody string
		if r.Body != nil {
			bodyBytes, _ := io.ReadAll(io.LimitReader(r.Body, 1024*1024+1))
			r.Body.Close()
			if r.URL.Path != "/bot/v1/agent/tool-results" {
				reqBody = string(bodyBytes)
				if len(reqBody) > 4096 {
					reqBody = reqBody[:4096]
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		// Store installation in context
		ctx := context.WithValue(r.Context(), installationKey, inst)
		r = r.WithContext(ctx)

		// Wrap response writer to capture status code.
		lw := &loggingResponseWriter{ResponseWriter: w, statusCode: 200}

		// Serve the request
		next.ServeHTTP(lw, r)

		// Log the API call
		traceID := r.Header.Get("X-Trace-Id")
		duration := time.Since(start)
		// Response bodies may contain application results or credentials and are
		// deliberately excluded from persistent API logs.
		apiLog := &store.AppAPILog{
			InstallationID: inst.ID,
			TraceID:        traceID,
			Method:         r.Method,
			Path:           r.URL.Path,
			RequestBody:    reqBody,
			StatusCode:     lw.statusCode,
			DurationMs:     int(duration.Milliseconds()),
		}
		if err := s.Store.CreateAPILog(apiLog); err != nil {
			slog.Error("bot api: failed to log api call", "err", err)
		}
	})
}

// loggingResponseWriter wraps http.ResponseWriter to capture the status code.
type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (lw *loggingResponseWriter) WriteHeader(code int) {
	if !lw.written {
		lw.statusCode = code
		lw.written = true
	}
	lw.ResponseWriter.WriteHeader(code)
}

func (lw *loggingResponseWriter) Write(b []byte) (int, error) {
	if !lw.written {
		lw.written = true
	}
	return lw.ResponseWriter.Write(b)
}

// requireScope checks that the installation's scopes include the required scope.
// Scopes are snapshotted at install time (Slack model) — only installation scopes are checked.
func (s *Server) requireScope(inst *store.AppInstallation, scope string) bool {
	if len(inst.Scopes) == 0 || string(inst.Scopes) == "[]" || string(inst.Scopes) == "null" {
		return false
	}
	var scopes []string
	if err := json.Unmarshal(inst.Scopes, &scopes); err != nil {
		return false
	}
	for _, sc := range scopes {
		if sc == scope {
			return true
		}
	}
	return false
}
