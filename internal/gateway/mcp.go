package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/mcp"
)

// ConfigProvider is the narrow surface MCPHandlers needs from the
// Config holder so we don't have to depend on the full *Config (which
// would create import cycles in tests). The startup wires this with a
// closure that returns the live config.
type ConfigProvider func() *config.Config

// MCPHandlers serves the in-process MCP re-auth endpoint that lets the
// chat UI reconnect a server with an expired token without restarting
// the gateway. POST /api/mcp/reauth/{id} runs the full PKCE flow
// against the server's configured IdP, persists the refreshed token to
// auth.token_store_path, and calls Manager.ReconnectServer to swap in
// the new client. The chat UI's "Re-authenticate" button (rendered
// when a tool result carries auth_required metadata) is the primary
// caller; the endpoint is also usable from curl for diagnostics.
type MCPHandlers struct {
	Manager      *mcp.Manager
	ConfigSource ConfigProvider

	// inFlight protects against concurrent re-auths for the same server
	// (user double-clicks the button, or multiple agent calls land at
	// once each rendering their own button). The PKCE flow binds a
	// loopback port; two in flight on the same server would race for
	// the port and one would fail.
	mu       sync.Mutex
	inFlight map[string]bool
}

// NewMCPHandlers constructs a handler set bound to the live manager
// and a closure that returns the current config. Pass nil for either
// to get a no-op (the route still mounts but every call returns 503).
func NewMCPHandlers(mgr *mcp.Manager, cfgSrc ConfigProvider) *MCPHandlers {
	return &MCPHandlers{
		Manager:      mgr,
		ConfigSource: cfgSrc,
		inFlight:     map[string]bool{},
	}
}

// Reauth runs the OAuth Authorization Code + PKCE flow for the named
// server, persists the new token, and reconnects the MCP client
// in-process. The user's browser opens to the IdP and the redirect
// lands on the gateway's loopback listener exactly as `felix mcp
// login` does — but at the end the session swap happens here so no
// restart is needed.
//
// Possible responses:
//   - 200 {ok: true, expiry: "..."} — login succeeded and reconnect
//     succeeded.
//   - 200 {ok: true, expiry: "...", warning: "..."} — login OK but
//     reconnect failed (rare; the new token is still on disk so the
//     next agent run picks it up after a restart).
//   - 400 {ok: false, error: "..."} — server unknown, not an OAuth
//     server, or login was cancelled.
//   - 500 {ok: false, error: "..."} — internal failure.
//   - 503 {ok: false, error: "..."} — manager not configured.
func (h *MCPHandlers) Reauth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h == nil || h.Manager == nil || h.ConfigSource == nil {
		writeMCPError(w, http.StatusServiceUnavailable, "MCP manager not configured")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeMCPError(w, http.StatusBadRequest, "missing server id in URL")
		return
	}

	if !h.tryClaim(id) {
		writeMCPError(w, http.StatusConflict, "another re-authentication is already running for this server")
		return
	}
	defer h.release(id)

	cfg := h.ConfigSource()
	if cfg == nil {
		writeMCPError(w, http.StatusServiceUnavailable, "config not loaded")
		return
	}
	resolved, err := cfg.ResolveMCPServers()
	if err != nil {
		writeMCPError(w, http.StatusInternalServerError, fmt.Sprintf("resolve mcp servers: %v", err))
		return
	}
	var entry *mcp.ManagerServerConfig
	for i := range resolved {
		if resolved[i].ID == id {
			entry = &resolved[i]
			break
		}
	}
	if entry == nil {
		writeMCPError(w, http.StatusBadRequest, fmt.Sprintf("mcp server %q not found, disabled, or its secret env var isn't set", id))
		return
	}
	if entry.Transport != "http" || entry.HTTP == nil {
		writeMCPError(w, http.StatusBadRequest, fmt.Sprintf("mcp server %q is not an HTTP server (transport=%q)", id, entry.Transport))
		return
	}
	auth := entry.HTTP.Auth
	if auth.Kind != "oauth2_authorization_code" {
		writeMCPError(w, http.StatusBadRequest, fmt.Sprintf("mcp server %q uses auth.kind=%q; re-auth only applies to oauth2_authorization_code", id, auth.Kind))
		return
	}

	// The PKCE flow needs to outlive the HTTP request — a slow user
	// who takes 90 s to click "Allow" in the IdP would otherwise hit
	// the request timeout and 502. Use a generous independent context.
	loginCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tok, err := mcp.RunInteractiveLogin(loginCtx, mcp.AuthCodePKCEConfig{
		AuthURL:      auth.AuthURL,
		TokenURL:     auth.TokenURL,
		ClientID:     auth.ClientID,
		ClientSecret: auth.ClientSecret,
		Scope:        auth.Scope,
		RedirectURI:  auth.RedirectURI,
		StorePath:    auth.TokenStorePath,
	})
	if err != nil {
		slog.Warn("mcp reauth: interactive login failed", "id", id, "error", err)
		writeMCPError(w, http.StatusBadRequest, fmt.Sprintf("login failed: %v", err))
		return
	}
	slog.Info("mcp reauth: token refreshed", "id", id, "expiry", tok.Expiry)

	// Reconnect in-process. If this fails, the token is still
	// persisted so a manual restart would pick it up — surface the
	// situation as a warning rather than a hard error.
	rcCtx, rcCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rcCancel()
	if err := h.Manager.ReconnectServer(rcCtx, id); err != nil {
		slog.Warn("mcp reauth: reconnect failed after successful login", "id", id, "error", err)
		writeMCPJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"expiry":  tok.Expiry,
			"warning": fmt.Sprintf("token refreshed but in-process reconnect failed: %v. Restart Felix to pick up the new token.", err),
		})
		return
	}
	slog.Info("mcp reauth: reconnected", "id", id)

	writeMCPJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"expiry": tok.Expiry,
	})
}

func (h *MCPHandlers) tryClaim(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inFlight[id] {
		return false
	}
	h.inFlight[id] = true
	return true
}

func (h *MCPHandlers) release(id string) {
	h.mu.Lock()
	delete(h.inFlight, id)
	h.mu.Unlock()
}

func writeMCPJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeMCPError(w http.ResponseWriter, status int, msg string) {
	writeMCPJSON(w, status, map[string]any{"ok": false, "error": msg})
}
