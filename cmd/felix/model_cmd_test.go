package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelListPrintsModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{"name": "qwen2.5:0.5b", "size": 394 << 20}},
		})
	}))
	defer srv.Close()

	var buf bytes.Buffer
	require.NoError(t, runModelList(context.Background(), srv.URL, &buf))
	out := buf.String()
	assert.Contains(t, out, "qwen2.5:0.5b")
	assert.Contains(t, out, "MB")
}

func TestModelStatusReportsBaseURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"version":"x"}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	require.NoError(t, runModelStatus(context.Background(), srv.URL, &buf))
	assert.Contains(t, buf.String(), srv.URL)
	assert.Contains(t, buf.String(), "ready")
}

func TestModelRemoveCallsDelete(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/delete", r.URL.Path)
		called = true
		w.WriteHeader(200)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	require.NoError(t, runModelRemove(context.Background(), srv.URL, "qwen2.5:0.5b", &buf))
	assert.True(t, called)
	assert.Contains(t, strings.ToLower(buf.String()), "removed")
}
