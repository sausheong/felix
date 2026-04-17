// Package google manages a per-installation Google OAuth client used for
// Gmail/Drive/Calendar access. The user registers their own OAuth client in
// the Google Cloud Console (Felix's settings UI guides them through this);
// Felix then runs a standard authorization-code flow against localhost and
// stores the resulting refresh token, encrypted, on disk.
package google

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"
	googleOAuth "golang.org/x/oauth2/google"
)

// Scopes requested at consent time. Includes the union of what current and
// near-future Google tools need so the user only consents once. The proof
// tool uses gmail.readonly only; the rest are pre-requested for follow-on
// tools (gmail send, drive read/list, calendar list/create).
var Scopes = []string{
	"openid", "email", "profile",
	"https://www.googleapis.com/auth/gmail.modify",
	"https://www.googleapis.com/auth/drive",
	"https://www.googleapis.com/auth/calendar",
}

// CallbackPath is the path the user's browser is redirected back to after
// consenting in Google. Mounted on the gateway HTTP server.
const CallbackPath = "/google/oauth/callback"

// Manager owns the OAuth state for one installation: client credentials,
// the encrypted refresh token, and the user identity discovered at consent.
type Manager struct {
	dataDir   string // ~/.felix or equivalent
	keyPath   string // ~/.felix/.encryption_key
	tokenPath string // ~/.felix/google_token.enc
	infoPath  string // ~/.felix/google_user.json (display name + email, not sensitive)

	mu       sync.RWMutex
	clientID string
	clientSecret string
	redirectURL  string

	// In-memory cached identity for status display
	cachedEmail string
}

// UserInfo is the small bit of identity we cache so the UI can render
// "Connected as you@gmail.com" without round-tripping to Google on every
// status fetch.
type UserInfo struct {
	Email string `json:"email"`
}

// NewManager constructs a Manager rooted at the given data directory.
// The OAuth credentials are configured separately via SetCredentials so
// they can be updated at runtime without restarting Felix.
func NewManager(dataDir string) (*Manager, error) {
	m := &Manager{
		dataDir:   dataDir,
		keyPath:   filepath.Join(dataDir, ".encryption_key"),
		tokenPath: filepath.Join(dataDir, "google_token.enc"),
		infoPath:  filepath.Join(dataDir, "google_user.json"),
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create google data dir: %w", err)
	}
	if _, err := m.encryptionKey(); err != nil {
		return nil, fmt.Errorf("init encryption key: %w", err)
	}
	if info, err := m.readUserInfo(); err == nil {
		m.cachedEmail = info.Email
	}
	return m, nil
}

// SetCredentials updates the OAuth client credentials and the redirect URL
// used by the local callback handler. Empty values disable Google integration.
func (m *Manager) SetCredentials(clientID, clientSecret, redirectURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clientID = clientID
	m.clientSecret = clientSecret
	m.redirectURL = redirectURL
}

// HasCredentials reports whether OAuth client credentials are configured.
func (m *Manager) HasCredentials() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clientID != "" && m.clientSecret != ""
}

// IsConnected reports whether a usable refresh token is on disk.
func (m *Manager) IsConnected() bool {
	if _, err := os.Stat(m.tokenPath); err != nil {
		return false
	}
	return m.HasCredentials()
}

// ConnectedEmail returns the email of the connected Google account, or empty
// if not connected.
func (m *Manager) ConnectedEmail() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cachedEmail
}

func (m *Manager) oauthConfig() (*oauth2.Config, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.clientID == "" || m.clientSecret == "" {
		return nil, errors.New("google oauth client not configured")
	}
	return &oauth2.Config{
		ClientID:     m.clientID,
		ClientSecret: m.clientSecret,
		RedirectURL:  m.redirectURL,
		Scopes:       Scopes,
		Endpoint:     googleOAuth.Endpoint,
	}, nil
}

// AuthCodeURL returns the URL the user should visit to grant access.
// state is an opaque value the caller verifies in the callback to prevent CSRF.
// AccessTypeOffline + prompt=consent forces Google to issue a refresh token,
// which is required for long-lived access. Without prompt=consent, returning
// users sometimes get an access token only.
func (m *Manager) AuthCodeURL(state string) (string, error) {
	cfg, err := m.oauthConfig()
	if err != nil {
		return "", err
	}
	return cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	), nil
}

// ExchangeAndStore exchanges the authorization code for tokens, writes the
// encrypted refresh token to disk, looks up the user's email, and caches it.
func (m *Manager) ExchangeAndStore(ctx context.Context, code string) error {
	cfg, err := m.oauthConfig()
	if err != nil {
		return err
	}
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("exchange code: %w", err)
	}
	if tok.RefreshToken == "" {
		return errors.New("google did not return a refresh token (try removing the app from your Google account permissions and reconnecting)")
	}
	if err := m.encryptAndWriteToken(tok.RefreshToken); err != nil {
		return fmt.Errorf("write encrypted token: %w", err)
	}

	// Fetch the user's email so the UI can display it. Failure here is
	// non-fatal: the token is already saved; we just won't show a name.
	email := fetchUserEmail(ctx, cfg.Client(ctx, tok))
	if email != "" {
		m.mu.Lock()
		m.cachedEmail = email
		m.mu.Unlock()
		_ = m.writeUserInfo(UserInfo{Email: email})
	}
	return nil
}

// HTTPClient returns an HTTP client with bearer-token auth for the connected
// user. Tokens auto-refresh as needed.
func (m *Manager) HTTPClient(ctx context.Context) (*http.Client, error) {
	cfg, err := m.oauthConfig()
	if err != nil {
		return nil, err
	}
	refresh, err := m.readAndDecryptToken()
	if err != nil {
		return nil, fmt.Errorf("read token: %w", err)
	}
	tok := &oauth2.Token{
		RefreshToken: refresh,
		Expiry:       time.Now().Add(-1 * time.Hour), // force refresh on first use
	}
	return cfg.Client(ctx, tok), nil
}

// Disconnect removes stored tokens and cached identity. Does NOT revoke the
// grant on Google's side — the user can do that from their Google account
// settings if they want (or by deleting the OAuth client entirely).
func (m *Manager) Disconnect() error {
	m.mu.Lock()
	m.cachedEmail = ""
	m.mu.Unlock()
	for _, p := range []string{m.tokenPath, m.infoPath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return nil
}

// === Encryption ===

// encryptionKey returns the raw 32-byte AES-256 key, generating one on first
// use. The key is stored in a separate file at 0600 so leaking felix.json5
// (e.g. via backup or accidental sharing) does not expose the token.
func (m *Manager) encryptionKey() ([]byte, error) {
	if data, err := os.ReadFile(m.keyPath); err == nil {
		decoded, derr := base64.StdEncoding.DecodeString(string(data))
		if derr == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	// Generate fresh key
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := os.WriteFile(m.keyPath, []byte(encoded), 0o600); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}
	return key, nil
}

func (m *Manager) encryptAndWriteToken(plaintext string) error {
	key, err := m.encryptionKey()
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	encoded := base64.StdEncoding.EncodeToString(sealed)
	return os.WriteFile(m.tokenPath, []byte(encoded), 0o600)
}

func (m *Manager) readAndDecryptToken() (string, error) {
	data, err := os.ReadFile(m.tokenPath)
	if err != nil {
		return "", err
	}
	sealed, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return "", err
	}
	key, err := m.encryptionKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("token file too short")
	}
	nonce, ct := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// === User info cache ===

func (m *Manager) writeUserInfo(info UserInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return os.WriteFile(m.infoPath, data, 0o600)
}

func (m *Manager) readUserInfo() (UserInfo, error) {
	var info UserInfo
	data, err := os.ReadFile(m.infoPath)
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(data, &info)
	return info, err
}

// fetchUserEmail calls Google's userinfo endpoint to look up the email of the
// account that just consented. The endpoint requires the openid+email scopes,
// which we always request.
func fetchUserEmail(ctx context.Context, client *http.Client) string {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://openidconnect.googleapis.com/v1/userinfo", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return ""
	}
	return info.Email
}
