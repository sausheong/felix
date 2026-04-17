package gateway

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/google"
)

// GoogleHandlers wires the Google integration HTTP endpoints to the OAuth
// manager and the live Config (so saving credentials persists them).
type GoogleHandlers struct {
	Manager   *google.Manager
	Config    *config.Config
	OnUpdate  func(*config.Config)
	BaseURL   string // e.g. http://127.0.0.1:18789, used to build the OAuth redirect
}

// NewGoogleHandlers wires the dependencies.
func NewGoogleHandlers(mgr *google.Manager, cfg *config.Config, onUpdate func(*config.Config), baseURL string) *GoogleHandlers {
	return &GoogleHandlers{Manager: mgr, Config: cfg, OnUpdate: onUpdate, BaseURL: baseURL}
}

// Status returns whether Google is configured and connected, plus the
// connected user's email when available.
func (h *GoogleHandlers) Status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"configured": h.Manager != nil && h.Manager.HasCredentials(),
		"connected":  h.Manager != nil && h.Manager.IsConnected(),
	}
	if h.Manager != nil {
		resp["email"] = h.Manager.ConnectedEmail()
	}
	json.NewEncoder(w).Encode(resp)
}

type saveCredsBody struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// SaveCredentials persists the user-supplied OAuth client_id/secret to
// felix.json5 and refreshes the in-memory manager so the next OAuth start
// uses the new values.
func (h *GoogleHandlers) SaveCredentials(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err != nil {
		http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
		return
	}
	var in saveCredsBody
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	in.ClientID = strings.TrimSpace(in.ClientID)
	in.ClientSecret = strings.TrimSpace(in.ClientSecret)
	if in.ClientID == "" || in.ClientSecret == "" {
		http.Error(w, `{"error":"client_id and client_secret are required"}`, http.StatusBadRequest)
		return
	}
	// Sanity check the format — Google OAuth client IDs end in
	// .apps.googleusercontent.com and Desktop client secrets start with GOCSPX-.
	if !strings.HasSuffix(in.ClientID, ".apps.googleusercontent.com") {
		http.Error(w, `{"error":"client_id should end with .apps.googleusercontent.com"}`, http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(in.ClientSecret, "GOCSPX-") {
		http.Error(w, `{"error":"client_secret should start with GOCSPX- (Desktop app type)"}`, http.StatusBadRequest)
		return
	}

	h.Config.Google.ClientID = in.ClientID
	h.Config.Google.ClientSecret = in.ClientSecret
	if err := h.Config.Save(); err != nil {
		slog.Error("save google credentials", "error", err)
		http.Error(w, `{"error":"save failed"}`, http.StatusInternalServerError)
		return
	}
	h.Manager.SetCredentials(in.ClientID, in.ClientSecret, h.BaseURL+google.CallbackPath)
	if h.OnUpdate != nil {
		h.OnUpdate(h.Config)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// OAuthStart begins the authorization-code flow. Generates a CSRF state
// token, stores it in a cookie, and redirects to Google.
func (h *GoogleHandlers) OAuthStart(w http.ResponseWriter, r *http.Request) {
	if h.Manager == nil || !h.Manager.HasCredentials() {
		http.Error(w, "Google credentials not configured", http.StatusBadRequest)
		return
	}
	state := randomHex(16)
	http.SetCookie(w, &http.Cookie{
		Name:     "google_oauth_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	url, err := h.Manager.AuthCodeURL(state)
	if err != nil {
		http.Error(w, "build auth url: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// OAuthCallback handles Google's redirect after consent. Verifies the state
// cookie, exchanges the code for tokens, stores the encrypted refresh token,
// and redirects back to /settings.
func (h *GoogleHandlers) OAuthCallback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie("google_oauth_state")
	if err != nil {
		http.Error(w, "missing state cookie", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(stateCookie.Value), []byte(r.URL.Query().Get("state"))) != 1 {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	// Clear the state cookie regardless of outcome
	http.SetCookie(w, &http.Cookie{Name: "google_oauth_state", Path: "/", MaxAge: -1})

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		http.Redirect(w, r, "/settings?google_error="+errParam, http.StatusSeeOther)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	if err := h.Manager.ExchangeAndStore(r.Context(), code); err != nil {
		slog.Error("google oauth exchange", "error", err)
		http.Redirect(w, r, "/settings?google_error="+err.Error(), http.StatusSeeOther)
		return
	}
	slog.Info("google connected", "email", h.Manager.ConnectedEmail())
	http.Redirect(w, r, "/settings?google_connected=1#google", http.StatusSeeOther)
}

// Disconnect removes stored tokens.
func (h *GoogleHandlers) Disconnect(w http.ResponseWriter, r *http.Request) {
	if h.Manager == nil {
		w.Write([]byte(`{"ok":true}`))
		return
	}
	if err := h.Manager.Disconnect(); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func randomHex(nBytes int) string {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		// extremely unlikely; fall back to a fixed-but-distinct value so we
		// don't crash. State validation will fail and the user can retry.
		return "fallback-state"
	}
	return hex.EncodeToString(buf)
}
