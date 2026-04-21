//go:build local

package local

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Run with: FELIX_OLLAMA_BIN=$(which ollama) go test -tags local -v ./internal/local/
func TestIntegrationStartAndPullTinyModel(t *testing.T) {
	bin := os.Getenv("FELIX_OLLAMA_BIN")
	if bin == "" {
		t.Skip("set FELIX_OLLAMA_BIN to run integration test")
	}
	dir := t.TempDir()
	s := New(Options{BinPath: bin, ModelsDir: dir})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, s.Start(ctx))
	defer s.Stop()

	port := s.BoundPort()
	require.NotZero(t, port)

	// Hit the version endpoint to confirm the OpenAI-compat surface is mounted.
	resp, err := http.Get(versionURL(port))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	// Pull a very small model and verify it shows up in /api/tags.
	inst := NewInstaller(baseURL(port))
	pullCtx, pullCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer pullCancel()
	require.NoError(t, inst.Pull(pullCtx, "qwen2.5:0.5b", nil))

	models, err := inst.List(context.Background())
	require.NoError(t, err)
	found := false
	for _, m := range models {
		if m.Name == "qwen2.5:0.5b" {
			found = true
		}
	}
	require.True(t, found, "expected qwen2.5:0.5b in /api/tags")
}

func versionURL(port int) string {
	return baseURL(port) + "/api/version"
}

func baseURL(port int) string {
	return "http://127.0.0.1:" + itoa(port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
