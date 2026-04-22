// Package tokens provides char-based token estimation for LLM payloads
// with a self-calibrating ratio learned from provider usage stats.
package tokens

import (
	"strings"
	"sync"

	"github.com/sausheong/felix/internal/llm"
)

// Estimate returns a rough token count for the given LLM payload using the
// industry-standard chars/4 heuristic. It is intentionally cheap and free
// of provider-specific tokenizer dependencies.
func Estimate(msgs []llm.Message, systemPrompt string, tools []llm.ToolDef) int {
	total := len(systemPrompt)
	for _, m := range msgs {
		// Per-message framing overhead (role markers, separators) — mirrors
		// the ~3-token-per-message budget used by tiktoken-style estimators.
		total += len(m.Role) + len(m.Content) + len(m.ToolCallID) + perMessageOverhead
		for _, tc := range m.ToolCalls {
			total += len(tc.ID) + len(tc.Name) + len(tc.Input)
		}
	}
	for _, t := range tools {
		total += len(t.Name) + len(t.Description) + len(t.Parameters)
	}
	return total / 4
}

// perMessageOverhead approximates the per-message framing tokens emitted by
// every chat-style provider (role tags, separators). Expressed in characters
// since the final divide-by-4 maps it back to tokens.
const perMessageOverhead = 3

// ContextWindow returns the maximum input tokens for the given
// "provider/model" identifier. Unknown models get a conservative 32k fallback.
func ContextWindow(model string) int {
	if model == "" {
		return defaultUnknownWindow
	}
	provider, modelID := splitProviderModel(model)

	switch provider {
	case "anthropic":
		// All current Anthropic chat models share a 200k window.
		if strings.Contains(modelID, "claude") {
			return 200000
		}
	case "openai":
		switch {
		case strings.HasPrefix(modelID, "gpt-4o"):
			return 128000
		case strings.HasPrefix(modelID, "gpt-4-turbo"):
			return 128000
		case strings.HasPrefix(modelID, "gpt-4"):
			return 8192
		case strings.HasPrefix(modelID, "gpt-3.5"):
			return 16385
		}
	case "google", "gemini":
		switch {
		case strings.Contains(modelID, "gemini-1.5-pro"):
			return 2000000
		case strings.Contains(modelID, "gemini-1.5-flash"):
			return 1000000
		case strings.Contains(modelID, "gemini-2"):
			return 1000000
		}
	case "local", "ollama":
		ollamaCtxMu.RLock()
		defer ollamaCtxMu.RUnlock()
		if v, ok := ollamaCtx[modelID]; ok {
			return v
		}
	}
	return defaultUnknownWindow
}

const defaultUnknownWindow = 32000

func splitProviderModel(s string) (string, string) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", s
}

// RegisterOllamaContext records the context length advertised by an Ollama
// model. Call this at startup after probing /api/show.
func RegisterOllamaContext(modelID string, ctx int) {
	ollamaCtxMu.Lock()
	defer ollamaCtxMu.Unlock()
	if ollamaCtx == nil {
		ollamaCtx = make(map[string]int)
	}
	ollamaCtx[modelID] = ctx
}

// ResetOllamaContexts is for tests.
func ResetOllamaContexts() {
	ollamaCtxMu.Lock()
	defer ollamaCtxMu.Unlock()
	ollamaCtx = nil
}

var (
	ollamaCtxMu sync.RWMutex
	ollamaCtx   map[string]int
)

// Calibrator learns a per-session multiplier between Estimate() output and
// the provider-reported actual input tokens. Use one instance per session.
type Calibrator struct {
	mu    sync.Mutex
	ratio float64 // actual / estimated; defaults 1.0
	count int
}

// NewCalibrator returns a Calibrator with ratio 1.0.
func NewCalibrator() *Calibrator {
	return &Calibrator{ratio: 1.0}
}

// Update folds a new observation into the running ratio. Both inputs must
// be positive; bad samples are silently ignored so the calibrator does not
// drift on the back of a single bad usage report.
func (c *Calibrator) Update(actual, estimated int) {
	if actual <= 0 || estimated <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	sample := float64(actual) / float64(estimated)
	c.count++
	// Simple running mean — converges, never gets stuck on early outliers.
	c.ratio += (sample - c.ratio) / float64(c.count)
}

// Adjust applies the learned ratio to a fresh estimate.
func (c *Calibrator) Adjust(estimated int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int(float64(estimated) * c.ratio)
}
