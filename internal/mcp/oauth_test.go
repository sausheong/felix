package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClientCredentialsHTTPClient_InjectsBearer(t *testing.T) {
	// Token server: accepts client_credentials grant, returns a fixed token.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		assert.Equal(t, "client_credentials", form.Get("grant_type"))
		assert.Equal(t, "test-scope", form.Get("scope"))
		// Cognito accepts client creds via Basic auth or form fields. The oauth2
		// library tries Basic first; we accept either to keep the test flexible.
		if user, pass, ok := r.BasicAuth(); ok {
			assert.Equal(t, "id-x", user)
			assert.Equal(t, "secret-y", pass)
		} else {
			assert.Equal(t, "id-x", form.Get("client_id"))
			assert.Equal(t, "secret-y", form.Get("client_secret"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-abc",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer tokenServer.Close()

	// Resource server: asserts the bearer token arrived.
	var seenAuth string
	resourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer resourceServer.Close()

	client := NewClientCredentialsHTTPClient(ClientCredentialsConfig{
		TokenURL:     tokenServer.URL,
		ClientID:     "id-x",
		ClientSecret: "secret-y",
		Scope:        "test-scope",
	})

	resp, err := client.Get(resourceServer.URL + "/whatever")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(seenAuth, "Bearer "), "expected Bearer header, got %q", seenAuth)
	assert.Equal(t, "Bearer tok-abc", seenAuth)
}
