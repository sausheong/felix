package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/skill"
	"github.com/sausheong/felix/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	cfg := config.DefaultConfig()
	providers := map[string]llm.LLMProvider{}
	reg := tools.NewRegistry()
	store := session.NewStore(t.TempDir())

	wsHandler := NewWebSocketHandler(providers, reg, store, cfg)
	return NewServer("127.0.0.1", 0, wsHandler)
}

func TestHealthEndpoint(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	s.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.Equal(t, "ok", body["status"])
	assert.NotEmpty(t, body["timestamp"])
}

func TestHealthEndpointContentType(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	s.router.ServeHTTP(w, req)

	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}

// TestShutdownBeforeStart — Shutdown must be safe to call before
// Start has run. The startup.StartGateway cleanup chain calls
// Shutdown unconditionally on error teardown paths that may run
// before the caller has launched Start in its own goroutine; without
// nil-safety here the cleanup panics and leaks every other resource
// that was supposed to be released after it.
func TestShutdownBeforeStart(t *testing.T) {
	s := newTestServer(t)
	require.NotPanics(t, func() {
		err := s.Shutdown(t.Context())
		require.NoError(t, err)
	})
}

// TestShutdownOnNilReceiver — defensive: the cleanup func captures
// `srv` by closure value; even if a future refactor accidentally
// initialises that to nil for some teardown path, Shutdown should
// degrade to a no-op rather than crash the whole shutdown chain.
func TestShutdownOnNilReceiver(t *testing.T) {
	var s *Server
	require.NotPanics(t, func() {
		err := s.Shutdown(t.Context())
		require.NoError(t, err)
	})
}

func TestSkillRoutesMounted(t *testing.T) {
	dir := t.TempDir()
	loader := skill.NewLoader()
	require.NoError(t, loader.LoadFrom(dir))

	cfg := config.DefaultConfig()
	providers := map[string]llm.LLMProvider{}
	reg := tools.NewRegistry()
	store := session.NewStore(t.TempDir())
	wsHandler := NewWebSocketHandler(providers, reg, store, cfg)

	srv := NewServer("127.0.0.1", 0, wsHandler, ServerOptions{
		Skills: NewSkillHandlers(loader, dir, []string{dir}),
	})

	// GET list — should be 200, not 404
	req := httptest.NewRequest(http.MethodGet, "/settings/api/skills", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "list route not mounted")

	// GET specific — empty dir, should be 404 (route is mounted, file missing)
	req = httptest.NewRequest(http.MethodGet, "/settings/api/skills/anything.md", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code, "get route not mounted")

	// DELETE specific — empty dir, should be 404
	req = httptest.NewRequest(http.MethodDelete, "/settings/api/skills/anything.md", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code, "delete route not mounted")
}
