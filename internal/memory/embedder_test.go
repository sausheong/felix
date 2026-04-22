package memory

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewOpenAIEmbedderPassesNomicModelVerbatim(t *testing.T) {
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}],"model":"nomic-embed-text"}`))
	}))
	defer srv.Close()

	emb := NewOpenAIEmbedder("dummy-key", srv.URL, "nomic-embed-text")
	if emb == nil {
		t.Fatal("NewOpenAIEmbedder returned nil")
	}

	// Trigger an actual call so we can inspect the wire payload.
	if _, err := emb.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}

	if !strings.Contains(capturedBody, "nomic-embed-text") {
		t.Errorf("request payload should pass model verbatim; body=%q", capturedBody)
	}
	// Sanity: ensure we didn't silently downgrade to ada-002.
	if strings.Contains(capturedBody, "text-embedding-ada-002") {
		t.Errorf("model was silently rewritten to ada-002; body=%q", capturedBody)
	}
}
