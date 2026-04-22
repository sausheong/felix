package memory

import (
	"context"
	"log/slog"
	"time"
)

// AttachWithProbe attaches embedder to mgr only if a single probe embed call
// succeeds within 5s. Logs warn + leaves the embedder unset on failure
// (memory falls back to BM25-only — its existing nil-embedder path).
func AttachWithProbe(mgr *Manager, embedder Embedder, modelName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := embedder.Embed(ctx, []string{"probe"}); err != nil {
		slog.Warn("memory: embedder unavailable, BM25-only", "model", modelName, "reason", err)
		return
	}
	mgr.SetEmbedder(embedder)
	slog.Info("memory vector search enabled", "model", modelName)
}
