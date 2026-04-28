package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/mcp"
)

// TestRunMcpLogin_EndToEnd builds a minimal config file with one
// auth-code MCP server, points it at httptest IdP servers, swaps the
// browser opener for a fake that completes the redirect, and asserts the
// token is persisted under the configured data dir.
func TestRunMcpLogin_EndToEnd(t *testing.T) {
	dataDir := t.TempDir()

	// Pick a free loopback port for the redirect URI.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/cb", port)

	// Token endpoint: returns a minted token for the auth-code grant.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		assert.Equal(t, "authorization_code", form.Get("grant_type"))
		assert.Equal(t, "the-code", form.Get("code"))
		assert.NotEmpty(t, form.Get("code_verifier"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "minted",
			"refresh_token": "ref",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	// Authorize endpoint redirects back to the loopback callback.
	authorizeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		http.Redirect(w, r, fmt.Sprintf("%s?code=the-code&state=%s", redirectURI, url.QueryEscape(state)), http.StatusFound)
	}))
	defer authorizeServer.Close()

	// Write a config file referencing those servers under dataDir, so
	// runMcpLogin's resolution computes the token store under dataDir
	// rather than the user's real ~/.felix.
	cfgPath := filepath.Join(dataDir, "felix.json5")
	cfg := map[string]any{
		"agents": map[string]any{
			"list": []map[string]any{{
				"id":    "default",
				"model": "claude-sonnet-4-5-20250929",
			}},
		},
		"mcp_servers": []map[string]any{{
			"id":      "test-gw",
			"enabled": true,
			"http": map[string]any{
				"url": "http://example.invalid/mcp",
				"auth": map[string]any{
					"kind":         "oauth2_authorization_code",
					"auth_url":     authorizeServer.URL + "/authorize",
					"token_url":    tokenServer.URL,
					"client_id":    "cid",
					"redirect_uri": redirectURI,
					// scope omitted — let the default kick in.
				},
			},
		}},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfgPath, data, 0o600))

	// Swap the browser opener so the test drives the redirect itself.
	mcp.SetOpenBrowserForTest(func(rawURL string) {
		go func() {
			req, _ := http.NewRequest("GET", rawURL, nil)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
	})
	defer mcp.SetOpenBrowserForTest(nil)

	var buf bytes.Buffer
	require.NoError(t, runMcpLogin(context.Background(), cfgPath, "test-gw", &buf))
	assert.Contains(t, buf.String(), "Logged in to test-gw")

	// Token should be persisted at <dataDir>/mcp-tokens/test-gw.json.
	tokFile := filepath.Join(dataDir, "mcp-tokens", "test-gw.json")
	got, err := os.ReadFile(tokFile)
	require.NoError(t, err)
	assert.Contains(t, string(got), `"access_token": "minted"`)
}

func TestRunMcpLogin_UnknownServerErrors(t *testing.T) {
	dataDir := t.TempDir()
	cfgPath := filepath.Join(dataDir, "felix.json5")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`{
		"agents": {"list": [{"id": "default", "model": "claude-sonnet-4-5-20250929"}]},
		"mcp_servers": []
	}`), 0o600))

	var buf bytes.Buffer
	err := runMcpLogin(context.Background(), cfgPath, "ghost", &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `not found`)
}
