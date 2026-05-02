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
//
// For "local"/"ollama" providers, the registered ollama context (probed
// at startup via /api/show) is used. For all other providers, lookup is
// driven by the modelID family — not the provider prefix — so proxies
// and relays that expose Claude/GPT/Gemini under a custom provider name
// (e.g. "platformai/claude-sonnet-4-6-asia-southeast1", AWS Bedrock,
// Vertex AI) still get the right window.
func ContextWindow(model string) int {
	if model == "" {
		return defaultUnknownWindow
	}
	provider, modelID := splitProviderModel(model)

	// Ollama-bundled local models register their advertised window at
	// startup; honour that before falling through to family detection.
	if provider == "local" || provider == "ollama" {
		ollamaCtxMu.RLock()
		v, ok := ollamaCtx[modelID]
		ollamaCtxMu.RUnlock()
		if ok {
			return v
		}
	}

	if w := windowByModelFamily(modelID); w > 0 {
		return w
	}
	return defaultUnknownWindow
}

// windowByModelFamily picks the context window from the model identifier
// alone, ignoring provider prefix. This is the path that handles
// proxies (platformai, openrouter, bedrock, vertex) where the provider
// label doesn't match the underlying model family. modelID may itself
// be nested (e.g. "openai/gpt-4o-2024-08-06" passed by openrouter); we
// match on both the full id and its leaf segment.
func windowByModelFamily(modelID string) int {
	id := strings.ToLower(modelID)
	leaf := id
	if i := strings.LastIndex(leaf, "/"); i >= 0 {
		leaf = leaf[i+1:]
	}
	switch {
	case strings.Contains(id, "claude"):
		// All current Claude chat models share a 200k window.
		return 200000
	case strings.HasPrefix(leaf, "gpt-4o"), strings.HasPrefix(leaf, "gpt-4-turbo"):
		return 128000
	case strings.HasPrefix(leaf, "gpt-4"):
		return 8192
	case strings.HasPrefix(leaf, "gpt-3.5"):
		return 16385
	case strings.Contains(id, "gemini-1.5-pro"):
		return 2000000
	case strings.Contains(id, "gemini-1.5-flash"),
		strings.Contains(id, "gemini-2"):
		return 1000000
	}
	return 0
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

// Snapshot returns the current (ratio, count) so the value can be persisted
// across Runtime reconstructions. Both fields are read under the same lock
// so a concurrent Update() can't tear them.
func (c *Calibrator) Snapshot() (ratio float64, count int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ratio, c.count
}

// Restore overwrites the calibrator's ratio + count from previously
// snapshotted values. Used at Runtime construction to seed the calibrator
// with whatever was learned in earlier turns of the same session — without
// this, every chat.send rebuild loses the calibration and starts at 1.0
// again, defeating the point of preventive compaction during the first few
// turns of a long session.
//
// Bad inputs (non-positive ratio, negative count) are silently ignored so
// a corrupt persistence file can't poison the in-memory state.
func (c *Calibrator) Restore(ratio float64, count int) {
	if ratio <= 0 || count < 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ratio = ratio
	c.count = count
}
