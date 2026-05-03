package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// AuthCodePKCEConfig is the resolved-secret shape needed to authenticate
// against an MCP server using OAuth 2.0 Authorization Code + PKCE
// (RFC 7636) with a loopback redirect (RFC 8252). Construction is the
// caller's responsibility — typically resolved by config.ResolveMCPServers.
type AuthCodePKCEConfig struct {
	AuthURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string // empty => public PKCE client; some IdPs (Cognito) require non-empty
	Scope        string // space-separated; defaulted upstream to "openid offline_access"
	RedirectURI  string // must be http://127.0.0.1:PORT/... or http://localhost:PORT/...
	StorePath    string // absolute path to per-server token cache file (mode 0600)
}

// ErrInteractiveLoginRequired is returned when no usable token is cached
// (no access token still valid AND no refresh token), so the caller must
// drive the interactive PKCE flow before the *http.Client is usable.
// Manager surfaces this to skip the server with a hint to run
// `felix mcp login <id>`.
var ErrInteractiveLoginRequired = errors.New("mcp: interactive auth-code login required")

// loginTimeout caps how long we wait for the user to complete the browser
// dance before tearing down the loopback callback server. Cognito's hosted
// UI session sticks around for hours, so this just bounds resource use.
const loginTimeout = 5 * time.Minute

// openBrowser is package-level so tests can swap in a fake.
var openBrowser = openBrowserOS

// SetOpenBrowserForTest swaps the package-level browser opener. Pass nil
// to restore the OS default. Intended for tests that need to drive the
// PKCE flow without launching a real browser; not part of the supported
// API surface.
func SetOpenBrowserForTest(fn func(string)) {
	if fn == nil {
		openBrowser = openBrowserOS
		return
	}
	openBrowser = fn
}

// NewAuthCodePKCEHTTPClient returns an *http.Client whose RoundTripper
// injects a Bearer token from cfg.StorePath. The token is refreshed
// transparently via the IdP's refresh_token grant when it nears expiry,
// and every refresh result is persisted back to the store.
//
// Behaviour by cache state:
//   - Valid access token (not expired with 30s buffer): returned wrapped,
//     no network call.
//   - Expired access token but refresh token present: refreshed inline now;
//     the new token is persisted and the client is returned.
//   - No usable token: returns (nil, ErrInteractiveLoginRequired). The
//     caller decides whether to run RunInteractiveLogin or skip the
//     server. The Manager skips; `felix mcp login` runs the flow.
func NewAuthCodePKCEHTTPClient(ctx context.Context, cfg AuthCodePKCEConfig) (*http.Client, error) {
	tok, err := loadToken(cfg.StorePath)
	if err != nil {
		return nil, fmt.Errorf("load cached token: %w", err)
	}

	oauthCfg := oauthConfigFor(cfg)

	if tok == nil || (tok.AccessToken == "" && tok.RefreshToken == "") {
		return nil, ErrInteractiveLoginRequired
	}

	// Ask oauth2 for a TokenSource; ReuseTokenSource will use tok as-is
	// when valid, and call the underlying source (which uses the
	// refresh_token grant) when it isn't. If we have only a refresh token,
	// pass an empty *oauth2.Token{RefreshToken: x} — oauth2 will refresh
	// on first Token() call.
	//
	// IMPORTANT: pass context.Background() — not the caller's ctx — to
	// both TokenSource and NewClient. Both bake the ctx into the
	// long-lived refresh transport, so refreshes minutes or hours later
	// would fail with "context canceled" if we used a short-lived ctx
	// (e.g. the 30s mcpInitCtx from startup, or a per-tool-call ctx
	// from the auto-Reconnect retry path). The returned *http.Client
	// outlives any per-call deadline; it must carry its own.
	bgSrc := oauthCfg.TokenSource(context.Background(), tok)
	src := oauth2.ReuseTokenSource(tok, bgSrc)
	persisting := &persistingTokenSource{src: src, path: cfg.StorePath, last: tok}

	// Force a refresh now if the current access token is unusable; this
	// surfaces a stale-refresh-token error early instead of on the first
	// MCP request. (This refresh runs on the long-lived bgSrc above, so
	// the caller's ctx only bounds how long we'll wait for it indirectly
	// via the persistingTokenSource — there's no clean way to cancel an
	// in-flight oauth2 refresh from outside, but in practice IdP token
	// endpoints respond in well under any sensible startup budget.)
	if !tokenUsable(tok) {
		fresh, err := persisting.Token()
		if err != nil {
			return nil, fmt.Errorf("refresh cached token: %w", err)
		}
		_ = fresh
	}

	return oauth2.NewClient(context.Background(), persisting), nil
}

// RunInteractiveLogin performs the full PKCE dance: generates a verifier
// and S256 challenge, listens on the redirect URI's loopback port, opens
// the OS browser to the IdP's authorize endpoint, waits for the callback,
// validates state, exchanges the code for a token, and persists.
//
// Returns the token (also written to cfg.StorePath). The caller does not
// need to persist it again.
func RunInteractiveLogin(ctx context.Context, cfg AuthCodePKCEConfig) (*oauth2.Token, error) {
	if cfg.RedirectURI == "" || cfg.AuthURL == "" || cfg.TokenURL == "" || cfg.ClientID == "" {
		return nil, errors.New("auth-code login requires auth_url, token_url, client_id, redirect_uri")
	}

	redirect, err := url.Parse(cfg.RedirectURI)
	if err != nil {
		return nil, fmt.Errorf("parse redirect_uri: %w", err)
	}
	host := redirect.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		// RFC 8252 §7.3 — native apps MUST use loopback. Anything else is
		// almost certainly a misconfiguration that would leak the auth
		// code to a remote server.
		return nil, fmt.Errorf("redirect_uri host %q is not a loopback address", host)
	}
	port := redirect.Port()
	if port == "" {
		return nil, errors.New("redirect_uri must include an explicit port")
	}

	verifier, challenge, err := newPKCEPair()
	if err != nil {
		return nil, fmt.Errorf("generate pkce: %w", err)
	}
	state, err := randString(32)
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return nil, fmt.Errorf("bind callback port %s: %w (something else is using it; close that process or change redirect_uri port)", port, err)
	}

	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(redirect.Path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if errCode := q.Get("error"); errCode != "" {
			desc := q.Get("error_description")
			fmt.Fprintf(w, "<h2>Login failed: %s</h2><p>%s</p><p>You can close this tab.</p>", htmlEscape(errCode), htmlEscape(desc))
			resultCh <- result{err: fmt.Errorf("authorization server returned error %q: %s", errCode, desc)}
			return
		}
		if got := q.Get("state"); got != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resultCh <- result{err: errors.New("state mismatch in callback (possible CSRF)")}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			resultCh <- result{err: errors.New("callback missing code parameter")}
			return
		}
		fmt.Fprint(w, "<h2>Login complete.</h2><p>You can close this tab and return to Felix.</p>")
		resultCh <- result{code: code}
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	oauthCfg := oauthConfigFor(cfg)
	authURL := oauthCfg.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)

	slog.Info("mcp: opening browser for OAuth login",
		"auth_url", authURL,
		"callback", cfg.RedirectURI,
		"hint", "if the browser doesn't open, visit the auth_url manually",
	)
	openBrowser(authURL)

	timeoutCtx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	var code string
	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, res.err
		}
		code = res.code
	case <-timeoutCtx.Done():
		return nil, fmt.Errorf("waiting for OAuth callback: %w", timeoutCtx.Err())
	}

	tok, err := oauthCfg.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", verifier),
	)
	if err != nil {
		return nil, fmt.Errorf("exchange code for token: %w", err)
	}

	if err := saveToken(cfg.StorePath, tok); err != nil {
		return nil, fmt.Errorf("persist token: %w", err)
	}
	return tok, nil
}

// oauthConfigFor builds the oauth2.Config used for both AuthCodeURL and
// Exchange/refresh. AuthStyleAutoDetect lets the library try Basic auth
// first (Cognito's preferred) and fall back to form-encoded creds — works
// against every common IdP without per-provider tuning.
func oauthConfigFor(cfg AuthCodePKCEConfig) *oauth2.Config {
	scopes := []string(nil)
	if cfg.Scope != "" {
		// oauth2 joins these with " " when building the request, which is
		// exactly the OAuth wire format. Splitting on space here would
		// double-encode; pass through as a single element when scopes are
		// already space-separated.
		scopes = []string{cfg.Scope}
	}
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   cfg.AuthURL,
			TokenURL:  cfg.TokenURL,
			AuthStyle: oauth2.AuthStyleAutoDetect,
		},
		RedirectURL: cfg.RedirectURI,
		Scopes:      scopes,
	}
}

// tokenUsable returns true when the access token exists and isn't due to
// expire within 30 seconds. The 30s buffer matches oauth2's internal
// expiryDelta and avoids a race where a request lands just as the token
// expires server-side.
func tokenUsable(tok *oauth2.Token) bool {
	if tok == nil || tok.AccessToken == "" {
		return false
	}
	if tok.Expiry.IsZero() {
		// IdPs that omit expires_in (rare) — treat as long-lived.
		return true
	}
	return time.Until(tok.Expiry) > 30*time.Second
}

// persistingTokenSource wraps an oauth2.TokenSource so that any token
// minted during a refresh is written back to disk. Without this, refresh
// tokens that the IdP rotates (Cognito does not, but Google/GitHub do)
// would be lost and the next process start would force a re-login.
type persistingTokenSource struct {
	src  oauth2.TokenSource
	path string

	mu   sync.Mutex
	last *oauth2.Token
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !sameToken(tok, p.last) {
		if err := saveToken(p.path, tok); err != nil {
			// Log but don't fail the request — the token is usable in
			// memory; we just won't have it cached for next start.
			slog.Warn("mcp: failed to persist refreshed token", "path", p.path, "error", err)
		}
		p.last = tok
	}
	return tok, nil
}

func sameToken(a, b *oauth2.Token) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.AccessToken == b.AccessToken && a.RefreshToken == b.RefreshToken && a.Expiry.Equal(b.Expiry)
}

// loadToken reads a persisted oauth2.Token. Returns (nil, nil) when the
// file doesn't exist — that's the "first start, no cached token" case
// and isn't an error.
func loadToken(path string) (*oauth2.Token, error) {
	if path == "" {
		return nil, errors.New("token store path is empty")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("decode token file %s: %w", path, err)
	}
	return &tok, nil
}

// saveToken writes the token to path with mode 0600, creating the parent
// directory at 0700 if needed. Atomic via write-temp-then-rename so a
// crash mid-write can't leave a corrupt file that breaks the next start.
func saveToken(path string, tok *oauth2.Token) error {
	if path == "" {
		return errors.New("token store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("encode token: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup if rename never happens.
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename token file: %w", err)
	}
	return nil
}

// newPKCEPair generates a 64-byte verifier (RFC 7636 §4.1 allows 43-128
// chars from the URL-safe alphabet) and its S256 challenge.
func newPKCEPair() (verifier, challenge string, err error) {
	verifier, err = randString(64)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randString(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// htmlEscape avoids pulling in html/template for a one-line response.
// Sufficient for the small set of characters that can appear in OAuth
// error codes/descriptions.
func htmlEscape(s string) string {
	repl := []struct{ old, new string }{
		{"&", "&amp;"},
		{"<", "&lt;"},
		{">", "&gt;"},
		{"\"", "&quot;"},
	}
	for _, r := range repl {
		for {
			i := indexOf(s, r.old)
			if i < 0 {
				break
			}
			s = s[:i] + r.new + s[i+len(r.old):]
		}
	}
	return s
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// openBrowserOS launches the user's default browser. Fire-and-forget — if
// it fails (no DISPLAY, sandboxed daemon, headless server) the user can
// still copy the auth URL we logged.
func openBrowserOS(rawURL string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "linux":
		cmd = exec.Command("xdg-open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		slog.Warn("mcp: unsupported OS for opening browser", "os", runtime.GOOS)
		return
	}
	if err := cmd.Start(); err != nil {
		slog.Warn("mcp: failed to launch browser", "error", err, "hint", "copy the auth_url from the log line above")
	}
}
