package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// WarmModel triggers a no-op generation against the bundled Ollama so the
// requested model is loaded into VRAM before the first real user request.
// This eliminates the ~10s cold-load latency on the first chat turn.
//
// Runs synchronously; callers should typically wrap in a goroutine. The
// request itself uses num_predict=1 so it returns within a few seconds.
// modelName accepts the full agent-style "provider/model" form ("local/gemma4")
// or the bare model name; the provider prefix is stripped.
func WarmModel(ctx context.Context, ollamaURL, modelName string) error {
	bare := stripProviderPrefix(modelName)
	if bare == "" {
		return fmt.Errorf("warmup: empty model name")
	}
	body, _ := json.Marshal(map[string]any{
		"model":      bare,
		"prompt":     "hi",
		"stream":     false,
		"keep_alive": "24h",
		"options":    map[string]any{"num_predict": 1},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("warmup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("warmup: ollama returned %s", resp.Status)
	}
	slog.Info("ollama model warmed", "model", bare, "dur_ms", time.Since(start).Milliseconds())
	return nil
}

// stripProviderPrefix turns "local/gemma4" into "gemma4". Bare names pass
// through unchanged.
func stripProviderPrefix(model string) string {
	if i := strings.Index(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
}
