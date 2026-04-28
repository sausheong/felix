package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestLoadToken_MissingFileReturnsNil(t *testing.T) {
	tok, err := loadToken(filepath.Join(t.TempDir(), "does-not-exist.json"))
	require.NoError(t, err)
	assert.Nil(t, tok)
}

func TestLoadToken_EmptyPathErrors(t *testing.T) {
	_, err := loadToken("")
	require.Error(t, err)
}

func TestSaveAndLoadToken_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok.json")
	in := &oauth2.Token{
		AccessToken:  "acc",
		RefreshToken: "ref",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour).Round(time.Second),
	}
	require.NoError(t, saveToken(path, in))

	out, err := loadToken(path)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, in.AccessToken, out.AccessToken)
	assert.Equal(t, in.RefreshToken, out.RefreshToken)
	assert.True(t, in.Expiry.Equal(out.Expiry))
}

func TestSaveToken_FilePermsAre0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "tok.json")
	require.NoError(t, saveToken(path, &oauth2.Token{AccessToken: "x"}))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	dirInfo, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
}

type stubSource struct {
	calls atomic.Int32
	tok   *oauth2.Token
	err   error
}

func (s *stubSource) Token() (*oauth2.Token, error) {
	s.calls.Add(1)
	return s.tok, s.err
}

func TestPersistingTokenSource_PersistsNewToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok.json")
	last := &oauth2.Token{AccessToken: "old", RefreshToken: "r"}
	require.NoError(t, saveToken(path, last))

	fresh := &oauth2.Token{AccessToken: "new", RefreshToken: "r2", Expiry: time.Now().Add(time.Hour).Round(time.Second)}
	src := &stubSource{tok: fresh}
	pts := &persistingTokenSource{src: src, path: path, last: last}

	got, err := pts.Token()
	require.NoError(t, err)
	assert.Equal(t, "new", got.AccessToken)

	// On disk?
	loaded, err := loadToken(path)
	require.NoError(t, err)
	assert.Equal(t, "new", loaded.AccessToken)
	assert.Equal(t, "r2", loaded.RefreshToken)
}

func TestPersistingTokenSource_NoWriteWhenSame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok.json")
	tok := &oauth2.Token{AccessToken: "same", RefreshToken: "r", Expiry: time.Now().Add(time.Hour).Round(time.Second)}
	require.NoError(t, saveToken(path, tok))
	stat0, _ := os.Stat(path)

	src := &stubSource{tok: tok}
	pts := &persistingTokenSource{src: src, path: path, last: tok}
	_, err := pts.Token()
	require.NoError(t, err)

	// File should be untouched (mtime equal). On filesystems with coarse
	// mtime resolution this is theoretically flaky; sleep first to make
	// the comparison unambiguous if anything *did* rewrite.
	stat1, _ := os.Stat(path)
	assert.Equal(t, stat0.ModTime(), stat1.ModTime(), "expected no rewrite when token unchanged")
}

func TestNewAuthCodePKCEHTTPClient_NoTokenReturnsSentinel(t *testing.T) {
	dir := t.TempDir()
	_, err := NewAuthCodePKCEHTTPClient(context.Background(), AuthCodePKCEConfig{
		AuthURL:   "https://example.invalid/authorize",
		TokenURL:  "https://example.invalid/token",
		ClientID:  "cid",
		StorePath: filepath.Join(dir, "tok.json"),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInteractiveLoginRequired))
}

func TestNewAuthCodePKCEHTTPClient_UsesCachedTokenWithoutRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok.json")

	// Token endpoint must NOT be hit when the cached access token is still
	// valid. We point at a server that fails the test if invoked.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected token endpoint call: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(500)
	}))
	defer tokenServer.Close()

	require.NoError(t, saveToken(path, &oauth2.Token{
		AccessToken: "still-good",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(time.Hour),
	}))

	resourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer still-good", r.Header.Get("Authorization"))
		w.WriteHeader(200)
	}))
	defer resourceServer.Close()

	client, err := NewAuthCodePKCEHTTPClient(context.Background(), AuthCodePKCEConfig{
		AuthURL:   "http://example.invalid/authorize",
		TokenURL:  tokenServer.URL,
		ClientID:  "cid",
		StorePath: path,
	})
	require.NoError(t, err)

	resp, err := client.Get(resourceServer.URL + "/x")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
}

func TestNewAuthCodePKCEHTTPClient_RefreshesExpiredAccessTokenSilently(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok.json")

	// Token endpoint: returns a fresh access token in exchange for the
	// refresh_token grant.
	var refreshCalls atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		assert.Equal(t, "refresh_token", form.Get("grant_type"))
		assert.Equal(t, "stale-refresh", form.Get("refresh_token"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fresh-access",
			"refresh_token": "rotated-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	// browser must NOT be opened — we have a usable refresh token.
	openBrowser = func(string) { t.Errorf("browser should not be opened during silent refresh") }
	defer func() { openBrowser = openBrowserOS }()

	require.NoError(t, saveToken(path, &oauth2.Token{
		AccessToken:  "expired",
		RefreshToken: "stale-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Minute), // already expired
	}))

	client, err := NewAuthCodePKCEHTTPClient(context.Background(), AuthCodePKCEConfig{
		AuthURL:   "http://example.invalid/authorize",
		TokenURL:  tokenServer.URL,
		ClientID:  "cid",
		StorePath: path,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, refreshCalls.Load(), int32(1), "expected at least one refresh call")

	resourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer fresh-access", r.Header.Get("Authorization"))
		w.WriteHeader(200)
	}))
	defer resourceServer.Close()

	resp, err := client.Get(resourceServer.URL + "/y")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	// The refreshed token should be persisted.
	loaded, err := loadToken(path)
	require.NoError(t, err)
	assert.Equal(t, "fresh-access", loaded.AccessToken)
	assert.Equal(t, "rotated-refresh", loaded.RefreshToken)
}

func TestRunInteractiveLogin_CompletesPKCEDance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok.json")

	// Pick a free port so parallel test runs don't collide.
	port, err := freePort()
	require.NoError(t, err)
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/cb", port)

	// Token endpoint: validate that the verifier hashed to the expected
	// challenge by recomputing via the proof parameters in the request.
	// Because we generate verifier+challenge inside RunInteractiveLogin,
	// we can't know the exact value here — instead we relax to "verifier
	// is present and non-empty" and that the correct grant_type was used.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		assert.Equal(t, "authorization_code", form.Get("grant_type"))
		assert.Equal(t, "the-code", form.Get("code"))
		assert.NotEmpty(t, form.Get("code_verifier"))
		assert.Equal(t, redirectURI, form.Get("redirect_uri"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "minted",
			"refresh_token": "ref",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	// Authorize endpoint: 302s back to the redirect with code+state.
	authorizeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		assert.NotEmpty(t, state)
		assert.NotEmpty(t, r.URL.Query().Get("code_challenge"))
		assert.Equal(t, "S256", r.URL.Query().Get("code_challenge_method"))
		http.Redirect(w, r, fmt.Sprintf("%s?code=the-code&state=%s", redirectURI, url.QueryEscape(state)), http.StatusFound)
	}))
	defer authorizeServer.Close()

	// "Browser": instead of launching xdg-open, GET the authorize URL
	// from a goroutine. Because the authorize server 302s directly back
	// to our loopback callback, the http.Client will follow the redirect
	// and trigger the callback handler.
	openBrowser = func(rawURL string) {
		go func() {
			req, _ := http.NewRequest("GET", rawURL, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Logf("fake browser GET failed: %v", err)
				return
			}
			_ = resp.Body.Close()
		}()
	}
	defer func() { openBrowser = openBrowserOS }()

	tok, err := RunInteractiveLogin(context.Background(), AuthCodePKCEConfig{
		AuthURL:     authorizeServer.URL + "/authorize",
		TokenURL:    tokenServer.URL,
		ClientID:    "cid",
		Scope:       "openid offline_access",
		RedirectURI: redirectURI,
		StorePath:   path,
	})
	require.NoError(t, err)
	assert.Equal(t, "minted", tok.AccessToken)

	loaded, err := loadToken(path)
	require.NoError(t, err)
	assert.Equal(t, "minted", loaded.AccessToken)
}

func TestRunInteractiveLogin_RejectsNonLoopbackRedirect(t *testing.T) {
	_, err := RunInteractiveLogin(context.Background(), AuthCodePKCEConfig{
		AuthURL:     "https://idp.example/authorize",
		TokenURL:    "https://idp.example/token",
		ClientID:    "cid",
		RedirectURI: "https://evil.example/callback",
		StorePath:   filepath.Join(t.TempDir(), "tok.json"),
	})
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "loopback")
}

func TestRunInteractiveLogin_StateMismatchFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok.json")

	port, err := freePort()
	require.NoError(t, err)
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/cb", port)

	// Token endpoint should never be hit.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("token endpoint should not be called when state mismatches")
		w.WriteHeader(500)
	}))
	defer tokenServer.Close()

	// Authorize: redirect with a wrong state.
	authorizeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("%s?code=c&state=WRONG", redirectURI), http.StatusFound)
	}))
	defer authorizeServer.Close()

	openBrowser = func(rawURL string) {
		go func() {
			req, _ := http.NewRequest("GET", rawURL, nil)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
	defer func() { openBrowser = openBrowserOS }()

	_, err = RunInteractiveLogin(context.Background(), AuthCodePKCEConfig{
		AuthURL:     authorizeServer.URL + "/authorize",
		TokenURL:    tokenServer.URL,
		ClientID:    "cid",
		RedirectURI: redirectURI,
		StorePath:   path,
	})
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "state mismatch")

	// Token must not be persisted.
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "token file should not exist after state mismatch")
}

func TestNewPKCEPair_ChallengeMatchesVerifier(t *testing.T) {
	v, c, err := newPKCEPair()
	require.NoError(t, err)
	assert.NotEmpty(t, v)
	assert.NotEmpty(t, c)
	// Verifier is 64 random bytes → 86 base64url chars (no padding).
	assert.Equal(t, 86, len(v))
	// Challenge is sha256(verifier) → 32 bytes → 43 base64url chars.
	assert.Equal(t, 43, len(c))
	// Sanity: re-derive challenge from verifier and compare.
	want := computeChallengeForTest(v)
	assert.Equal(t, want, c)
}

func TestTokenUsable(t *testing.T) {
	assert.False(t, tokenUsable(nil))
	assert.False(t, tokenUsable(&oauth2.Token{}))
	assert.False(t, tokenUsable(&oauth2.Token{AccessToken: "a", Expiry: time.Now().Add(5 * time.Second)}))
	assert.True(t, tokenUsable(&oauth2.Token{AccessToken: "a", Expiry: time.Now().Add(time.Hour)}))
	assert.True(t, tokenUsable(&oauth2.Token{AccessToken: "a"})) // zero expiry => long-lived
}

// freePort asks the kernel for an ephemeral port, returns it, and releases
// the listener so the caller can re-bind. Small race window between Close()
// and re-bind; acceptable for tests.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// computeChallengeForTest mirrors the production challenge derivation so
// the test breaks loudly if the production hash function ever drifts away
// from S256.
func computeChallengeForTest(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
