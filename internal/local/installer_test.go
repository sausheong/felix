package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMockOllama(t *testing.T, handler http.HandlerFunc) (*Installer, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	inst := NewInstaller(srv.URL)
	return inst, srv.Close
}

func TestInstallerList(t *testing.T) {
	inst, closeFn := newMockOllama(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/tags", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "qwen2.5:0.5b", "size": 394 << 20},
				{"name": "llama4.1:8b", "size": int64(4700) << 20},
			},
		})
	})
	defer closeFn()

	models, err := inst.List(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 2)
	assert.Equal(t, "qwen2.5:0.5b", models[0].Name)
	assert.Equal(t, int64(394<<20), models[0].SizeBytes)
}

func TestInstallerDelete(t *testing.T) {
	var gotBody map[string]string
	inst, closeFn := newMockOllama(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/delete", r.URL.Path)
		assert.Equal(t, http.MethodDelete, r.Method)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	})
	defer closeFn()

	err := inst.Delete(context.Background(), "qwen2.5:0.5b")
	require.NoError(t, err)
	assert.Equal(t, "qwen2.5:0.5b", gotBody["name"])
}

func TestInstallerDeleteNotFound(t *testing.T) {
	inst, closeFn := newMockOllama(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer closeFn()

	err := inst.Delete(context.Background(), "nope")
	require.Error(t, err)
}
