package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBearerHTTPClient_InjectsAuthorization(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewBearerHTTPClient("tok-123")
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	assert.Equal(t, "Bearer tok-123", got)
}

func TestBearerHTTPClient_EmptyToken_NoHeader(t *testing.T) {
	var got string
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		seen = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewBearerHTTPClient("")
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	assert.True(t, seen)
	assert.Empty(t, got, "no Authorization header should be set when token is empty")
}

func TestBearerHTTPClient_PreservesExistingHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewBearerHTTPClient("tok-default")
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Basic preset")
	resp, err := client.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	assert.True(t, strings.HasPrefix(got, "Basic "), "caller-set Authorization should not be overwritten; got %q", got)
}
