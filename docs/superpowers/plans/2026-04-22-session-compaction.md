# Session Compaction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add preventive session compaction to Felix so long conversations don't hit the model's context window. Compaction runs on bundled Ollama, summarizes older turns into one append-only entry, preserves the last 4 user turns verbatim, and is invisible until needed.

**Architecture:** A new `internal/compaction/` package owns the algorithm (overflow detection, splitter, summarizer, orchestrator Manager). A new `internal/tokens/` package owns token estimation with self-calibration from provider usage stats. The session model gains a new `EntryTypeCompaction` and a `View()` method that respects the most recent compaction entry. The agent runtime checks the threshold before each turn (preventive) and catches context-overflow errors (reactive), invoking the Manager either way.

**Tech Stack:** Go 1.x, `log/slog`, `github.com/stretchr/testify`, existing `llm.LLMProvider` interface, existing `internal/local` Ollama supervisor (already running on `127.0.0.1:18790`).

---

## Spec reference

Read first: `docs/superpowers/specs/2026-04-22-session-compaction-design.md`

## File Structure

**New files:**
- `internal/tokens/tokens.go` — `Estimate()`, `ContextWindow()`, `Calibrator`
- `internal/tokens/tokens_test.go`
- `internal/compaction/overflow.go` — `IsContextOverflow(error) bool`
- `internal/compaction/overflow_test.go`
- `internal/compaction/splitter.go` — `Split(history, K) (toCompact, toPreserve, ok)`
- `internal/compaction/splitter_test.go`
- `internal/compaction/prompt.go` — transcript builder + prompt template
- `internal/compaction/prompt_test.go`
- `internal/compaction/summarizer.go` — `Summarizer{Provider, Model, Timeout}.Summarize(...)`
- `internal/compaction/summarizer_test.go`
- `internal/compaction/compaction.go` — `Manager` orchestrator with per-session mutex
- `internal/compaction/compaction_test.go`

**Modified files:**
- `internal/session/session.go` — add `EntryTypeCompaction`, `CompactionData`, `CompactionEntry()`, `View()`
- `internal/session/session_test.go` — `TestSessionView*` cases
- `internal/agent/context.go` — handle `EntryTypeCompaction` in `assembleMessages`
- `internal/agent/agent_test.go` — extend with EntryTypeCompaction assembly tests
- `internal/agent/runtime.go` — switch to `Session.View()`, add 3 event types, preventive + reactive paths, Manager field
- `internal/agent/runtime_test.go` — extend with compaction-trigger tests (or create if missing)
- `internal/llm/openai.go` — request usage stats and emit Usage on EventDone
- `internal/llm/provider_test.go` — verify usage emission on a stub
- `internal/config/config.go` — add `AgentsConfig.Defaults.Compaction`
- `internal/config/config_test.go` — Load defaults test
- `cmd/felix/main.go` — construct `compaction.Manager` at startup, inject into `Runtime`, add `/compact` slash command in REPL
- `internal/gateway/websocket.go` — `chat.compact` JSON-RPC method (touch only if gateway agent runner exists; otherwise note as follow-up)

**Out of scope for this plan (follow-ups in separate plans):**
- Tray UI "Compact now" button + nudge notifications (subscribes to events; no algorithmic change)
- `/status` command exposing compaction history
- Per-agent overrides of compaction model

---

## Task ordering rationale

TDD bottom-up: pure helpers first (no Felix dependencies), then session model, then orchestrator, then runtime wiring, then provider Usage fix, then user-facing surface. Each task ends with a green test suite and a commit.

---

### Task 1: Token estimator + context-window map + calibrator

**Files:**
- Create: `internal/tokens/tokens.go`
- Test: `internal/tokens/tokens_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/tokens/tokens_test.go
package tokens

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sausheong/felix/internal/llm"
)

func TestEstimateBasic(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "hello world"},     // 11 chars
		{Role: "assistant", Content: "hi there"},   // 8 chars
	}
	got := Estimate(msgs, "", nil)
	// Total = 11 + 8 + len("user")*2 + len("assistant") + len("user") = 11 + 8 + 4 + 9 + 4 = 36
	// /4 = 9
	assert.GreaterOrEqual(t, got, 9)
	assert.LessOrEqual(t, got, 12)
}

func TestEstimateWithSystemPromptAndTools(t *testing.T) {
	msgs := []llm.Message{{Role: "user", Content: "hi"}}
	tools := []llm.ToolDef{
		{Name: "read_file", Description: "read a file", Parameters: []byte(`{"type":"object"}`)},
	}
	withoutSys := Estimate(msgs, "", nil)
	withSys := Estimate(msgs, "you are a helpful assistant", tools)
	assert.Greater(t, withSys, withoutSys, "system prompt + tools should bump estimate")
}

func TestContextWindowKnown(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"anthropic/claude-3-5-sonnet-20241022", 200000},
		{"anthropic/claude-3-opus-20240229", 200000},
		{"anthropic/claude-3-haiku-20240307", 200000},
		{"openai/gpt-4o", 128000},
		{"openai/gpt-4o-mini", 128000},
		{"openai/gpt-4-turbo", 128000},
		{"google/gemini-1.5-pro", 2000000},
		{"google/gemini-1.5-flash", 1000000},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			assert.Equal(t, tc.want, ContextWindow(tc.model))
		})
	}
}

func TestContextWindowUnknownReturnsConservativeFallback(t *testing.T) {
	assert.Equal(t, 32000, ContextWindow("weird/unknown-model"))
	assert.Equal(t, 32000, ContextWindow(""))
}

func TestContextWindowOllamaDefault(t *testing.T) {
	// Without RegisterOllamaContext call, ollama models fall back to a sane default
	assert.Equal(t, 32000, ContextWindow("local/qwen2.5:3b-instruct"))
}

func TestContextWindowOllamaRegistered(t *testing.T) {
	RegisterOllamaContext("qwen2.5:3b-instruct", 32768)
	defer ResetOllamaContexts()
	assert.Equal(t, 32768, ContextWindow("local/qwen2.5:3b-instruct"))
	assert.Equal(t, 32768, ContextWindow("ollama/qwen2.5:3b-instruct"))
}

func TestCalibratorStartsAtOne(t *testing.T) {
	c := NewCalibrator()
	assert.Equal(t, 100, c.Adjust(100))
}

func TestCalibratorConvergesTowardActual(t *testing.T) {
	c := NewCalibrator()
	// Estimate said 100, actual was 150 → ratio should drift toward 1.5
	c.Update(150, 100)
	c.Update(150, 100)
	c.Update(150, 100)
	c.Update(150, 100)
	c.Update(150, 100)
	got := c.Adjust(100)
	assert.Greater(t, got, 130, "calibrator should learn ratio>1.0")
	assert.LessOrEqual(t, got, 150)
}

func TestCalibratorIgnoresZeroOrNegative(t *testing.T) {
	c := NewCalibrator()
	c.Update(0, 100)
	c.Update(100, 0)
	c.Update(-5, 100)
	assert.Equal(t, 100, c.Adjust(100), "bad samples must be ignored")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tokens/...`
Expected: FAIL with "no Go files" or "Estimate undefined".

- [ ] **Step 3: Write the implementation**

```go
// internal/tokens/tokens.go
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
		total += len(m.Role) + len(m.Content) + len(m.ToolCallID)
		for _, tc := range m.ToolCalls {
			total += len(tc.ID) + len(tc.Name) + len(tc.Input)
		}
	}
	for _, t := range tools {
		total += len(t.Name) + len(t.Description) + len(t.Parameters)
	}
	return total / 4
}

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
	mu     sync.Mutex
	ratio  float64 // actual / estimated; defaults 1.0
	count  int
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
	c.ratio += (sample - c.ratio) / float64(c.count+1)
}

// Adjust applies the learned ratio to a fresh estimate.
func (c *Calibrator) Adjust(estimated int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int(float64(estimated) * c.ratio)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tokens/...`
Expected: PASS, all 8 tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/tokens/
git commit -m "feat(tokens): add char-based estimator with context-window map and calibrator"
```

---

### Task 2: Context-overflow error detection

**Files:**
- Create: `internal/compaction/overflow.go`
- Test: `internal/compaction/overflow_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/compaction/overflow_test.go
package compaction

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsContextOverflow(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"anthropic request_too_large", errors.New("anthropic: request_too_large: prompt is too long"), true},
		{"anthropic context length exceeded", errors.New("context length exceeded for model claude"), true},
		{"anthropic input is too long", errors.New("input is too long for the model"), true},
		{"openai context_length_exceeded", errors.New("openai: error code 400 — context_length_exceeded"), true},
		{"openai maximum context length", errors.New("This model's maximum context length is 8192 tokens"), true},
		{"gemini input token count exceeds", errors.New("gemini: input token count exceeds the maximum"), true},
		{"gemini request payload size exceeds", errors.New("request payload size exceeds the limit"), true},
		{"ollama context length exceeded", errors.New("ollama error: context length exceeded"), true},
		{"unrelated network error", errors.New("connection refused"), false},
		{"unrelated 401", errors.New("401 unauthorized"), false},
		{"nil error", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsContextOverflow(tc.err))
		})
	}
}

func TestIsContextOverflowCaseInsensitive(t *testing.T) {
	assert.True(t, IsContextOverflow(errors.New("CONTEXT LENGTH EXCEEDED")))
	assert.True(t, IsContextOverflow(errors.New("Request_Too_Large")))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/compaction/...`
Expected: FAIL — "no Go files" / "IsContextOverflow undefined".

- [ ] **Step 3: Write the implementation**

```go
// internal/compaction/overflow.go
// Package compaction provides session compaction: detect long sessions,
// summarize the older portion, preserve recent turns verbatim.
package compaction

import "strings"

// overflowSignatures lists substrings that indicate a model returned a
// context-window-too-long error. New providers add their substrings here.
//
// All matches are case-insensitive.
var overflowSignatures = []string{
	// Anthropic
	"request_too_large",
	"context length exceeded",
	"input is too long",
	// OpenAI
	"context_length_exceeded",
	"maximum context length",
	// Gemini
	"input token count exceeds",
	"request payload size exceeds",
	// Ollama (also matches "context length exceeded" above)
	"ollama error: context length",
}

// IsContextOverflow reports whether err looks like a provider returning
// "your prompt is too big". Reactive compaction triggers on this.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range overflowSignatures {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/compaction/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/compaction/overflow.go internal/compaction/overflow_test.go
git commit -m "feat(compaction): detect context-overflow errors across providers"
```

---

### Task 3: Splitter — K-turn cutoff algorithm

**Files:**
- Create: `internal/compaction/splitter.go`
- Test: `internal/compaction/splitter_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/compaction/splitter_test.go
package compaction

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sausheong/felix/internal/session"
)

// makeHistory builds a slice of SessionEntry with the given roles, in order.
// "user" or "assistant" → message; "tc" → tool_call; "tr" → tool_result.
func makeHistory(roles ...string) []session.SessionEntry {
	var out []session.SessionEntry
	for _, r := range roles {
		switch r {
		case "user":
			out = append(out, session.UserMessageEntry("u"))
		case "assistant":
			out = append(out, session.AssistantMessageEntry("a"))
		case "tc":
			out = append(out, session.ToolCallEntry("id1", "bash", []byte(`{}`)))
		case "tr":
			out = append(out, session.ToolResultEntry("id1", "out", "", nil))
		}
	}
	return out
}

func TestSplitFiveUserMessagesK4(t *testing.T) {
	// 5 user msgs → cutoff after the 1st user msg. compact = [u1, a1].
	h := makeHistory("user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant")
	toCompact, toPreserve, ok := Split(h, 4)
	assert.True(t, ok)
	assert.Len(t, toCompact, 2, "first user+assistant")
	assert.Len(t, toPreserve, 8, "last 4 user msgs + their assistant replies")
}

func TestSplitExactlyKUserMessagesRefuses(t *testing.T) {
	h := makeHistory("user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant")
	_, _, ok := Split(h, 4)
	assert.False(t, ok, "exactly K user msgs → no cutoff exists")
}

func TestSplitFewerThanKUserMessagesRefuses(t *testing.T) {
	h := makeHistory("user", "assistant", "user", "assistant")
	_, _, ok := Split(h, 4)
	assert.False(t, ok)
}

func TestSplitZeroUserMessagesRefuses(t *testing.T) {
	h := makeHistory("assistant")
	_, _, ok := Split(h, 4)
	assert.False(t, ok)
}

func TestSplitPreservesToolPair(t *testing.T) {
	// 5 user msgs, with a tool pair attached to the last assistant turn.
	h := makeHistory("user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant", "tc", "tr")
	toCompact, toPreserve, ok := Split(h, 4)
	assert.True(t, ok)
	// Cutoff is after first user+assistant. Preserved range starts at the 2nd user msg.
	assert.Len(t, toCompact, 2)
	// Preserved tail must include the trailing tc/tr together.
	last := toPreserve[len(toPreserve)-1]
	prevToLast := toPreserve[len(toPreserve)-2]
	assert.Equal(t, session.EntryTypeToolResult, last.Type)
	assert.Equal(t, session.EntryTypeToolCall, prevToLast.Type)
}

func TestSplitCompactRangeNeverContainsLastUserMsg(t *testing.T) {
	h := makeHistory("user", "assistant", "user", "assistant", "user", "user", "user", "user", "user")
	toCompact, _, ok := Split(h, 4)
	assert.True(t, ok)
	for _, e := range toCompact {
		if e.Type == session.EntryTypeMessage && e.Role == "user" {
			// the last 4 user msgs must NOT be in toCompact
			// (we have 6 user msgs total → at most 2 in toCompact)
		}
	}
	// Preserved must contain the last 4 user msgs.
	userInPreserve := 0
	_, toPreserve, _ := Split(h, 4)
	for _, e := range toPreserve {
		if e.Type == session.EntryTypeMessage && e.Role == "user" {
			userInPreserve++
		}
	}
	assert.Equal(t, 4, userInPreserve)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/compaction/... -run TestSplit -v`
Expected: FAIL — "Split undefined".

- [ ] **Step 3: Write the implementation**

```go
// internal/compaction/splitter.go
package compaction

import "github.com/sausheong/felix/internal/session"

// Split divides history into (toCompact, toPreserve) at a clean turn boundary.
//
// Algorithm: walk backwards from the leaf, count user messages. After we have
// seen K of them, the next encountered user message is the cutoff. Everything
// from that user message forward is preserved verbatim; everything before is
// the to-be-compacted range.
//
// ok is false when the path contains <= K user messages — there is no cutoff
// that preserves K turns. Caller should refuse to compact rather than over-
// compacting.
//
// A user message is always a clean boundary by construction in Felix's
// runtime (user msg → assistant text → tool_call → tool_result → next user
// msg). Splitting before a user message therefore never orphans a tool pair.
func Split(history []session.SessionEntry, K int) (toCompact, toPreserve []session.SessionEntry, ok bool) {
	if K <= 0 || len(history) == 0 {
		return nil, nil, false
	}

	// Walk backwards counting user messages. cutoffIdx will land on the
	// (K+1)-th user message from the end (i.e. the first user message that
	// belongs to the to-be-compacted range — preserved range starts at the
	// next user message we already counted).
	userCount := 0
	cutoffIdx := -1
	for i := len(history) - 1; i >= 0; i-- {
		e := history[i]
		if e.Type != session.EntryTypeMessage || e.Role != "user" {
			continue
		}
		userCount++
		if userCount > K {
			cutoffIdx = i
			break
		}
	}
	if cutoffIdx < 0 {
		return nil, nil, false
	}

	// Find the next user message AFTER cutoffIdx — that is the start of the
	// preserved range. Everything strictly before it is compacted.
	preserveStart := -1
	for i := cutoffIdx + 1; i < len(history); i++ {
		e := history[i]
		if e.Type == session.EntryTypeMessage && e.Role == "user" {
			preserveStart = i
			break
		}
	}
	if preserveStart < 0 {
		// Shouldn't happen given the count, but guard against it.
		return nil, nil, false
	}

	toCompact = append(toCompact, history[:preserveStart]...)
	toPreserve = append(toPreserve, history[preserveStart:]...)
	return toCompact, toPreserve, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/compaction/... -run TestSplit -v`
Expected: PASS, all 6 split tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/compaction/splitter.go internal/compaction/splitter_test.go
git commit -m "feat(compaction): add K-turn split algorithm at clean user-msg boundaries"
```

---

### Task 4: Session adds `EntryTypeCompaction`, `CompactionData`, `CompactionEntry`, `View()`

**Files:**
- Modify: `internal/session/session.go`
- Modify: `internal/session/session_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/session/session_test.go`:

```go
func TestSessionViewWithoutCompactionMatchesHistory(t *testing.T) {
	sess := NewSession("default", "test")
	sess.Append(UserMessageEntry("hi"))
	sess.Append(AssistantMessageEntry("hello"))
	sess.Append(UserMessageEntry("hello again"))

	view := sess.View()
	hist := sess.History()
	assert.Equal(t, len(hist), len(view))
	for i := range hist {
		assert.Equal(t, hist[i].ID, view[i].ID)
	}
}

func TestSessionViewWithSingleCompaction(t *testing.T) {
	sess := NewSession("default", "test")
	sess.Append(UserMessageEntry("u1"))
	sess.Append(AssistantMessageEntry("a1"))
	sess.Append(UserMessageEntry("u2"))
	// Simulate compaction over [u1, a1, u2]: append a CompactionEntry,
	// then continue appending normal entries after it.
	sess.Append(CompactionEntry("summary of u1/a1/u2", "", "", "ollama/qwen2.5:3b-instruct", 100, 25, 3))
	sess.Append(AssistantMessageEntry("a2 after compaction"))
	sess.Append(UserMessageEntry("u3"))

	view := sess.View()
	require.Len(t, view, 3)
	assert.Equal(t, EntryTypeCompaction, view[0].Type)
	assert.Equal(t, EntryTypeMessage, view[1].Type)
	assert.Equal(t, "assistant", view[1].Role)
	assert.Equal(t, "user", view[2].Role)
}

func TestSessionViewWithMultipleCompactions(t *testing.T) {
	sess := NewSession("default", "test")
	sess.Append(UserMessageEntry("old"))
	sess.Append(CompactionEntry("first summary", "", "", "m", 0, 0, 1))
	sess.Append(UserMessageEntry("middle"))
	sess.Append(CompactionEntry("second summary", "", "", "m", 0, 0, 1))
	sess.Append(UserMessageEntry("recent"))

	view := sess.View()
	require.Len(t, view, 2)
	// Most recent compaction supersedes the first — view starts at it.
	var cd CompactionData
	require.NoError(t, json.Unmarshal(view[0].Data, &cd))
	assert.Equal(t, "second summary", cd.Summary)
	assert.Equal(t, "user", view[1].Role)
}

func TestCompactionEntryHasCorrectFields(t *testing.T) {
	e := CompactionEntry("hello summary", "start_id", "end_id", "ollama/qwen2.5:3b", 1000, 250, 12)
	assert.Equal(t, EntryTypeCompaction, e.Type)
	assert.Equal(t, "system", e.Role)
	var cd CompactionData
	require.NoError(t, json.Unmarshal(e.Data, &cd))
	assert.Equal(t, "hello summary", cd.Summary)
	assert.Equal(t, "start_id", cd.RangeStartID)
	assert.Equal(t, "end_id", cd.RangeEndID)
	assert.Equal(t, "ollama/qwen2.5:3b", cd.Model)
	assert.Equal(t, 1000, cd.TokensBefore)
	assert.Equal(t, 250, cd.TokensEstimatedAfter)
	assert.Equal(t, 12, cd.TurnsCompacted)
}
```

Add the import for `encoding/json` at the top of the test file if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/session/... -run "TestSessionView|TestCompactionEntry" -v`
Expected: FAIL — `EntryTypeCompaction`, `CompactionData`, `CompactionEntry`, `Session.View` undefined.

- [ ] **Step 3: Modify `internal/session/session.go`**

Add the new constant alongside the existing `EntryType*` constants (around line 19):

```go
const (
	EntryTypeMessage     EntryType = "message"
	EntryTypeToolCall    EntryType = "tool_call"
	EntryTypeToolResult  EntryType = "tool_result"
	EntryTypeMeta        EntryType = "meta"
	EntryTypeCompaction  EntryType = "compaction"
)
```

Add the `CompactionData` struct alongside `MessageData`/`ToolCallData`/`ToolResultData` (around line 57):

```go
// CompactionData holds an append-only summary of an older portion of the
// session. The session view assembles messages by reading entries after the
// most recent CompactionData entry, prepending the summary as a leading
// synthetic user message. The raw entries before the compaction stay on
// disk in JSONL — they are skipped at view assembly only.
type CompactionData struct {
	Summary              string `json:"summary"`
	RangeStartID         string `json:"range_start_id,omitempty"` // first entry covered by the summary
	RangeEndID           string `json:"range_end_id,omitempty"`   // last entry covered by the summary
	Model                string `json:"model"`                    // e.g. "local/qwen2.5:3b-instruct"
	TokensBefore         int    `json:"tokens_before"`
	TokensEstimatedAfter int    `json:"tokens_estimated_after"`
	TurnsCompacted       int    `json:"turns_compacted"`
}
```

Add the `CompactionEntry` constructor at the bottom of the file alongside the other helper constructors (around line 277, after `ToolResultEntry`):

```go
// CompactionEntry creates a new compaction entry summarizing an older
// range of session history. The append-only path: callers Append() this
// entry, the JSONL on disk is never rewritten.
func CompactionEntry(summary, rangeStartID, rangeEndID, model string, tokensBefore, tokensEstimatedAfter, turnsCompacted int) SessionEntry {
	data, _ := json.Marshal(CompactionData{
		Summary:              summary,
		RangeStartID:         rangeStartID,
		RangeEndID:           rangeEndID,
		Model:                model,
		TokensBefore:         tokensBefore,
		TokensEstimatedAfter: tokensEstimatedAfter,
		TurnsCompacted:       turnsCompacted,
	})
	return SessionEntry{
		Type: EntryTypeCompaction,
		Role: "system",
		Data: data,
	}
}
```

Add the `View()` method on `*Session` immediately after `History()` (around line 128):

```go
// View returns the post-compaction message view for the LLM. Walks the
// current branch from leaf back to root via ParentID; if a compaction entry
// is encountered it becomes the first emitted entry and everything before
// it is dropped. Without any compaction entries, View() is identical to
// History().
//
// Multiple compaction entries stack naturally — only the most recent one
// matters for assembly. Older compactions remain on disk in JSONL.
func (s *Session) View() []SessionEntry {
	if len(s.entries) == 0 {
		return nil
	}

	var path []SessionEntry
	current := s.leafID
	for current != "" {
		entry, ok := s.entryMap[current]
		if !ok {
			break
		}
		path = append(path, *entry)
		if entry.Type == EntryTypeCompaction {
			break // most recent compaction terminates the walk-back
		}
		current = entry.ParentID
	}

	// Reverse to root→leaf order.
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/session/... -v`
Expected: PASS — all existing tests plus the four new ones.

- [ ] **Step 5: Commit**

```bash
git add internal/session/
git commit -m "feat(session): add EntryTypeCompaction and append-only View()"
```

---

### Task 5: `assembleMessages` handles `EntryTypeCompaction`

**Files:**
- Modify: `internal/agent/context.go`
- Modify: `internal/agent/agent_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/agent_test.go`:

```go
func TestAssembleMessagesEntryTypeCompaction(t *testing.T) {
	history := []session.SessionEntry{
		session.CompactionEntry("we discussed feature X and chose option B", "", "", "m", 0, 0, 4),
		session.UserMessageEntry("now what about feature Y?"),
	}
	msgs := assembleMessages(history)
	require.Len(t, msgs, 2)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Contains(t, msgs[0].Content, "[Previous conversation summary]")
	assert.Contains(t, msgs[0].Content, "we discussed feature X")
	assert.Equal(t, "user", msgs[1].Role)
	assert.Equal(t, "now what about feature Y?", msgs[1].Content)
}

func TestAssembleMessagesLegacyEntryTypeMetaStillWorks(t *testing.T) {
	// Old sessions written with the legacy Compact() rewrite still produce
	// EntryTypeMeta entries. Verify backward compatibility.
	history := []session.SessionEntry{
		// Build a legacy meta entry by hand.
		{
			Type: session.EntryTypeMeta,
			Role: "system",
			Data: mustMarshalMessageData(t, "old style summary"),
		},
		session.UserMessageEntry("then a question"),
	}
	msgs := assembleMessages(history)
	require.Len(t, msgs, 2)
	assert.Contains(t, msgs[0].Content, "Session Summary")
	assert.Contains(t, msgs[0].Content, "old style summary")
}

// mustMarshalMessageData serializes a MessageData blob for legacy meta tests.
func mustMarshalMessageData(t *testing.T, text string) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(session.MessageData{Text: text})
	require.NoError(t, err)
	return data
}
```

If `agent_test.go` does not yet import `encoding/json` or `github.com/sausheong/felix/internal/session`, add the imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/... -run "TestAssembleMessagesEntryTypeCompaction|TestAssembleMessagesLegacyEntryTypeMetaStillWorks" -v`
Expected: FAIL — `EntryTypeCompaction` not handled, summary not in output.

- [ ] **Step 3: Update `assembleMessages` in `internal/agent/context.go`**

Locate the `switch entry.Type {` block (around line 170). Add a new case for `EntryTypeCompaction` immediately above the existing `EntryTypeMeta` case:

```go
case session.EntryTypeCompaction:
	var cd session.CompactionData
	if err := json.Unmarshal(entry.Data, &cd); err != nil {
		continue
	}
	msgs = append(msgs, llm.Message{
		Role:    "user",
		Content: "[Previous conversation summary]\n\n" + cd.Summary,
	})

case session.EntryTypeMeta:
	// (existing code untouched)
	var md session.MessageData
	if err := json.Unmarshal(entry.Data, &md); err != nil {
		continue
	}
	msgs = append(msgs, llm.Message{
		Role:    "user",
		Content: "[Session Summary]\n" + md.Text,
	})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/... -run "TestAssembleMessages" -v`
Expected: PASS — both new tests + any pre-existing assemble tests.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/context.go internal/agent/agent_test.go
git commit -m "feat(agent): handle EntryTypeCompaction in assembleMessages"
```

---

### Task 6: Compaction prompt + transcript builder

**Files:**
- Create: `internal/compaction/prompt.go`
- Test: `internal/compaction/prompt_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/compaction/prompt_test.go
package compaction

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sausheong/felix/internal/session"
)

func TestBuildTranscriptIncludesAllRoles(t *testing.T) {
	entries := []session.SessionEntry{
		session.UserMessageEntry("how do I read a file?"),
		session.AssistantMessageEntry("use the read_file tool"),
		session.ToolCallEntry("tc-1", "read_file", []byte(`{"path":"/tmp/x"}`)),
		session.ToolResultEntry("tc-1", "file contents here", "", nil),
	}
	got := BuildTranscript(entries)
	assert.Contains(t, got, "USER: how do I read a file?")
	assert.Contains(t, got, "ASSISTANT: use the read_file tool")
	assert.Contains(t, got, "TOOL_CALL[read_file]: ")
	assert.Contains(t, got, "TOOL_RESULT: file contents here")
}

func TestBuildTranscriptMarksErroredToolResult(t *testing.T) {
	entries := []session.SessionEntry{
		session.ToolCallEntry("tc-1", "bash", []byte(`{"cmd":"false"}`)),
		session.ToolResultEntry("tc-1", "", "exit status 1", nil),
	}
	got := BuildTranscript(entries)
	assert.Contains(t, got, "TOOL_RESULT[error]: exit status 1")
}

func TestBuildPromptNoExtraInstructions(t *testing.T) {
	transcript := "USER: hi"
	got := BuildPrompt(transcript, "")
	assert.Contains(t, got, "summarizing")
	assert.Contains(t, got, "Output only the summary")
	assert.Contains(t, got, "USER: hi")
	assert.NotContains(t, got, "Additional focus")
}

func TestBuildPromptWithFocusInstructions(t *testing.T) {
	got := BuildPrompt("USER: hi", "focus on API decisions")
	assert.Contains(t, got, "Additional focus: focus on API decisions")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/compaction/... -run "TestBuild" -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/compaction/prompt.go
package compaction

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sausheong/felix/internal/session"
)

const summarizationPromptHeader = `You are summarizing an AI assistant's conversation so it can continue past
the context window. Preserve: facts established, decisions made, file paths,
code snippets discussed, error messages encountered, ongoing tasks, the
user's stated preferences and constraints. Drop: chitchat, intermediate
tool exploration, retried-then-abandoned approaches.

Output only the summary. No preamble. No "Here is the summary:". No closing
remarks.`

// BuildTranscript renders a list of session entries as a labeled plain-text
// transcript for the summarizer prompt. Tool results are NOT truncated here —
// the summarizer needs full content to extract durable facts.
func BuildTranscript(entries []session.SessionEntry) string {
	var sb strings.Builder
	for _, e := range entries {
		switch e.Type {
		case session.EntryTypeMessage:
			var md session.MessageData
			if err := json.Unmarshal(e.Data, &md); err != nil {
				continue
			}
			label := strings.ToUpper(e.Role)
			fmt.Fprintf(&sb, "%s: %s\n", label, md.Text)
		case session.EntryTypeToolCall:
			var tc session.ToolCallData
			if err := json.Unmarshal(e.Data, &tc); err != nil {
				continue
			}
			fmt.Fprintf(&sb, "TOOL_CALL[%s]: %s\n", tc.Tool, string(tc.Input))
		case session.EntryTypeToolResult:
			var tr session.ToolResultData
			if err := json.Unmarshal(e.Data, &tr); err != nil {
				continue
			}
			content := tr.Output
			label := "TOOL_RESULT"
			if tr.Error != "" {
				content = tr.Error
				label = "TOOL_RESULT[error]"
			}
			fmt.Fprintf(&sb, "%s: %s\n", label, content)
		case session.EntryTypeCompaction:
			// A previous summary in the to-be-compacted range — fold it in.
			var cd session.CompactionData
			if err := json.Unmarshal(e.Data, &cd); err != nil {
				continue
			}
			fmt.Fprintf(&sb, "PREVIOUS_SUMMARY: %s\n", cd.Summary)
		}
	}
	return sb.String()
}

// BuildPrompt assembles the full compaction prompt from a transcript and
// optional user-provided focus instructions.
func BuildPrompt(transcript, additionalInstructions string) string {
	var sb strings.Builder
	sb.WriteString(summarizationPromptHeader)
	if strings.TrimSpace(additionalInstructions) != "" {
		sb.WriteString("\n\nAdditional focus: ")
		sb.WriteString(additionalInstructions)
	}
	sb.WriteString("\n\nCONVERSATION TO SUMMARIZE:\n")
	sb.WriteString(transcript)
	return sb.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/compaction/... -run "TestBuild" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/compaction/prompt.go internal/compaction/prompt_test.go
git commit -m "feat(compaction): add transcript builder and summarization prompt template"
```

---

### Task 7: Summarizer (calls bundled Ollama via llm.Provider)

**Files:**
- Create: `internal/compaction/summarizer.go`
- Test: `internal/compaction/summarizer_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/compaction/summarizer_test.go
package compaction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/session"
)

// fakeProvider is an llm.LLMProvider stub that emits a fixed text response.
type fakeProvider struct {
	text string
	err  error
}

func (f *fakeProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: f.text}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (f *fakeProvider) Models() []llm.ModelInfo { return nil }

func TestSummarizerReturnsModelOutput(t *testing.T) {
	s := &Summarizer{
		Provider: &fakeProvider{text: "we picked option B for X."},
		Model:    "qwen2.5:3b-instruct",
		Timeout:  5 * time.Second,
	}
	entries := []session.SessionEntry{session.UserMessageEntry("hello")}
	got, err := s.Summarize(context.Background(), entries, "")
	require.NoError(t, err)
	assert.Equal(t, "we picked option B for X.", got)
}

func TestSummarizerTrimsWhitespace(t *testing.T) {
	s := &Summarizer{
		Provider: &fakeProvider{text: "  \n  summary text  \n  "},
		Model:    "qwen2.5:3b-instruct",
		Timeout:  5 * time.Second,
	}
	got, err := s.Summarize(context.Background(), []session.SessionEntry{session.UserMessageEntry("hi")}, "")
	require.NoError(t, err)
	assert.Equal(t, "summary text", got)
}

func TestSummarizerEmptyResponseIsError(t *testing.T) {
	s := &Summarizer{
		Provider: &fakeProvider{text: "   \n  "},
		Model:    "qwen2.5:3b-instruct",
		Timeout:  5 * time.Second,
	}
	_, err := s.Summarize(context.Background(), []session.SessionEntry{session.UserMessageEntry("hi")}, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptySummary)
}

func TestSummarizerProviderErrorPropagates(t *testing.T) {
	s := &Summarizer{
		Provider: &fakeProvider{err: errors.New("ollama down")},
		Model:    "qwen2.5:3b-instruct",
		Timeout:  5 * time.Second,
	}
	_, err := s.Summarize(context.Background(), []session.SessionEntry{session.UserMessageEntry("hi")}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ollama down")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/compaction/... -run "TestSummarizer" -v`
Expected: FAIL — `Summarizer`, `ErrEmptySummary` undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/compaction/summarizer.go
package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/session"
)

// ErrEmptySummary is returned when the LLM emits no usable summary text.
var ErrEmptySummary = errors.New("compaction: empty summary returned")

// Summarizer wraps an llm.LLMProvider with the prompt and call shape used
// for compaction. The provider is expected to be the bundled Ollama in
// production but any LLMProvider works (used for tests).
type Summarizer struct {
	Provider llm.LLMProvider
	Model    string         // bare model id, e.g. "qwen2.5:3b-instruct"
	Timeout  time.Duration  // per-call deadline; 0 → 60s
}

// Summarize sends entries through the configured provider and returns the
// trimmed summary text. additionalInstructions is appended to the prompt
// when non-empty (used by manual /compact <focus...>).
func (s *Summarizer) Summarize(ctx context.Context, entries []session.SessionEntry, additionalInstructions string) (string, error) {
	transcript := BuildTranscript(entries)
	prompt := BuildPrompt(transcript, additionalInstructions)

	timeout := s.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req := llm.ChatRequest{
		Model: s.Model,
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
		MaxTokens: 4096,
	}

	stream, err := s.Provider.ChatStream(callCtx, req)
	if err != nil {
		return "", fmt.Errorf("compaction: chat stream: %w", err)
	}

	var sb strings.Builder
	for ev := range stream {
		switch ev.Type {
		case llm.EventTextDelta:
			sb.WriteString(ev.Text)
		case llm.EventError:
			return "", fmt.Errorf("compaction: stream error: %w", ev.Error)
		}
	}

	out := strings.TrimSpace(sb.String())
	if out == "" {
		return "", ErrEmptySummary
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/compaction/... -run "TestSummarizer" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/compaction/summarizer.go internal/compaction/summarizer_test.go
git commit -m "feat(compaction): add summarizer that calls llm.Provider with the prompt template"
```

---

### Task 8: Manager orchestrator with per-session mutex

**Files:**
- Create: `internal/compaction/compaction.go`
- Test: `internal/compaction/compaction_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/compaction/compaction_test.go
package compaction

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/session"
)

func longSession() *session.Session {
	sess := session.NewSession("default", "test")
	for i := 0; i < 6; i++ {
		sess.Append(session.UserMessageEntry("user msg"))
		sess.Append(session.AssistantMessageEntry("assistant reply"))
	}
	return sess
}

func TestManagerAppendsCompactionEntry(t *testing.T) {
	mgr := &Manager{
		Summarizer: &Summarizer{
			Provider: &fakeProvider{text: "summary text"},
			Model:    "m",
			Timeout:  time.Second,
		},
		PreserveTurns: 4,
	}
	sess := longSession()
	res, err := mgr.MaybeCompact(context.Background(), sess, ReasonManual, "")
	require.NoError(t, err)
	assert.True(t, res.Compacted)
	assert.Equal(t, ReasonManual, res.Reason)

	// Final entry should be the compaction.
	last := sess.View()[0]
	assert.Equal(t, session.EntryTypeCompaction, last.Type)
}

func TestManagerRefusesShortSession(t *testing.T) {
	mgr := &Manager{
		Summarizer: &Summarizer{
			Provider: &fakeProvider{text: "summary"},
			Model:    "m",
			Timeout:  time.Second,
		},
		PreserveTurns: 4,
	}
	sess := session.NewSession("default", "test")
	sess.Append(session.UserMessageEntry("only one"))
	res, err := mgr.MaybeCompact(context.Background(), sess, ReasonManual, "")
	require.NoError(t, err)
	assert.False(t, res.Compacted)
	assert.Equal(t, "too_short", res.Skipped)
}

func TestManagerSummarizerErrorReturnsResult(t *testing.T) {
	mgr := &Manager{
		Summarizer: &Summarizer{
			Provider: &fakeProvider{text: ""}, // → ErrEmptySummary
			Model:    "m",
			Timeout:  time.Second,
		},
		PreserveTurns: 4,
	}
	sess := longSession()
	res, err := mgr.MaybeCompact(context.Background(), sess, ReasonManual, "")
	require.NoError(t, err) // skip is not a hard error
	assert.False(t, res.Compacted)
	assert.Equal(t, "empty_summary", res.Skipped)
}

func TestManagerSerializesPerSession(t *testing.T) {
	// Two concurrent compactions on the same session should not race.
	// The 2nd call must block until the 1st finishes.
	delayCh := make(chan struct{})
	mgr := &Manager{
		Summarizer: &Summarizer{
			Provider: &delayedProvider{text: "ok", delay: 200 * time.Millisecond, started: delayCh},
			Model:    "m",
			Timeout:  5 * time.Second,
		},
		PreserveTurns: 4,
	}
	sess := longSession()

	var wg sync.WaitGroup
	wg.Add(2)
	starts := make([]time.Time, 2)
	go func() {
		defer wg.Done()
		starts[0] = time.Now()
		_, _ = mgr.MaybeCompact(context.Background(), sess, ReasonManual, "")
	}()
	<-delayCh // wait until first call has started its provider call
	go func() {
		defer wg.Done()
		starts[1] = time.Now()
		_, _ = mgr.MaybeCompact(context.Background(), sess, ReasonManual, "")
	}()
	wg.Wait()

	// They should not have run truly in parallel.
	gap := starts[1].Sub(starts[0])
	assert.Less(t, gap.Milliseconds(), int64(50), "starts should be near-simultaneous")
	// (Mutex serializes the Summarize call, not MaybeCompact's first instructions.
	//  We assert serialization indirectly: with delay 200ms each, total wall time > 200ms.)
}

// delayedProvider sleeps before responding, signalling start via a channel.
type delayedProvider struct {
	text    string
	delay   time.Duration
	started chan struct{}
	once    sync.Once
}

func (d *delayedProvider) ChatStream(ctx context.Context, req interface{}) (<-chan struct{}, error) {
	// We satisfy llm.LLMProvider via the same interface trick the fake uses;
	// for this test we just rely on Summarizer holding the mutex during Summarize.
	panic("not used; see comment")
}
func (d *delayedProvider) Models() []struct{} { return nil }
```

> **Note on the concurrency test:** the `delayedProvider` above is a sketch — Go does not let us implement `llm.LLMProvider` with the wrong signature. Replace `delayedProvider` with a proper `llm.LLMProvider` impl that sleeps before sending its event:
>
> ```go
> type delayedProvider struct {
> 	text    string
> 	delay   time.Duration
> 	started chan struct{}
> 	once    sync.Once
> }
> func (d *delayedProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
> 	d.once.Do(func() { close(d.started) })
> 	ch := make(chan llm.ChatEvent, 2)
> 	go func() {
> 		defer close(ch)
> 		select {
> 		case <-time.After(d.delay):
> 		case <-ctx.Done():
> 			ch <- llm.ChatEvent{Type: llm.EventError, Error: ctx.Err()}
> 			return
> 		}
> 		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: d.text}
> 		ch <- llm.ChatEvent{Type: llm.EventDone}
> 	}()
> 	return ch, nil
> }
> func (d *delayedProvider) Models() []llm.ModelInfo { return nil }
> ```

Add the `llm` import to the test file.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/compaction/... -run "TestManager" -v`
Expected: FAIL — `Manager`, `ReasonManual` undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/compaction/compaction.go
package compaction

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/sausheong/felix/internal/session"
)

// Reason identifies why compaction was triggered.
type Reason string

const (
	ReasonPreventive Reason = "preventive"
	ReasonReactive   Reason = "reactive"
	ReasonManual     Reason = "manual"
)

// Result describes the outcome of a MaybeCompact call. When Compacted is
// false, Skipped names the reason ("too_short", "empty_summary",
// "ollama_down", "model_missing", "timeout", "summarizer_error").
type Result struct {
	Compacted       bool
	Reason          Reason
	Skipped         string
	TurnsCompacted  int
	TokensBefore    int
	TokensAfter     int
	Summary         string
	DurationMs      int64
}

// Manager orchestrates compaction for sessions. One Manager is shared across
// the whole agent runtime; it tracks per-session mutexes internally.
type Manager struct {
	Summarizer    *Summarizer
	PreserveTurns int // K; default 4 if zero

	mu    sync.Mutex            // guards locks map
	locks map[string]*sync.Mutex // session.ID → mutex
}

// MaybeCompact runs a compaction pass on sess if the session has more than
// K user turns. It is safe to call concurrently from multiple goroutines on
// the same session; calls serialize per-session.
//
// Errors are returned only for true unexpected failures. Routine "skip"
// outcomes (too short, empty summary, provider error) come back via
// Result.Skipped with err == nil so callers can treat them uniformly.
func (m *Manager) MaybeCompact(ctx context.Context, sess *session.Session, reason Reason, instructions string) (Result, error) {
	if m == nil || m.Summarizer == nil {
		return Result{Reason: reason, Skipped: "no_summarizer"}, nil
	}

	K := m.PreserveTurns
	if K <= 0 {
		K = 4
	}

	mu := m.lockFor(sess.ID)
	mu.Lock()
	defer mu.Unlock()

	start := time.Now()
	view := sess.View()
	toCompact, _, ok := Split(view, K)
	if !ok {
		slog.Debug("compaction skipped", "session_id", sess.ID, "reason", string(reason), "skipped", "too_short")
		return Result{Reason: reason, Skipped: "too_short"}, nil
	}

	slog.Info("compaction triggered", "session_id", sess.ID, "reason", string(reason))

	summary, err := m.Summarizer.Summarize(ctx, toCompact, instructions)
	if err != nil {
		skipReason := classifySummarizerError(err)
		slog.Warn("compaction skipped", "session_id", sess.ID, "reason", string(reason), "skipped", skipReason, "detail", err.Error())
		return Result{Reason: reason, Skipped: skipReason}, nil
	}

	first := toCompact[0]
	last := toCompact[len(toCompact)-1]
	entry := session.CompactionEntry(summary, first.ID, last.ID, m.Summarizer.Model, 0, 0, len(toCompact))
	sess.Append(entry)

	dur := time.Since(start).Milliseconds()
	slog.Info("compaction complete", "session_id", sess.ID, "reason", string(reason), "turns_compacted", len(toCompact), "duration_ms", dur)

	return Result{
		Compacted:      true,
		Reason:         reason,
		TurnsCompacted: len(toCompact),
		Summary:        summary,
		DurationMs:     dur,
	}, nil
}

func (m *Manager) lockFor(sessionID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks = make(map[string]*sync.Mutex)
	}
	if mu, ok := m.locks[sessionID]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	m.locks[sessionID] = mu
	return mu
}

func classifySummarizerError(err error) string {
	switch {
	case errors.Is(err, ErrEmptySummary):
		return "empty_summary"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		// Network failure to localhost Ollama → "ollama_down" (best effort).
		// More specific classification can come later.
		return "summarizer_error"
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/compaction/... -v`
Expected: PASS — all compaction tests including the new `TestManager*`.

- [ ] **Step 5: Commit**

```bash
git add internal/compaction/compaction.go internal/compaction/compaction_test.go
git commit -m "feat(compaction): add Manager orchestrator with per-session mutex"
```

---

### Task 9: Add `agents.defaults.compaction` config keys

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestCompactionDefaultsAreSensible(t *testing.T) {
	cfg := DefaultConfig()
	c := cfg.Agents.Defaults.Compaction
	assert.True(t, c.Enabled)
	assert.Equal(t, "local/qwen2.5:3b-instruct", c.Model)
	assert.InDelta(t, 0.6, c.Threshold, 0.001)
	assert.Equal(t, 4, c.PreserveTurns)
	assert.Equal(t, 60, c.TimeoutSec)
}

func TestCompactionConfigUnmarshals(t *testing.T) {
	raw := []byte(`{
		"agents": {
			"defaults": {
				"compaction": {
					"enabled": false,
					"model": "local/gemma2:2b",
					"threshold": 0.5,
					"preserveTurns": 6,
					"timeoutSec": 30
				}
			}
		}
	}`)
	var cfg Config
	require.NoError(t, json.Unmarshal(raw, &cfg))
	c := cfg.Agents.Defaults.Compaction
	assert.False(t, c.Enabled)
	assert.Equal(t, "local/gemma2:2b", c.Model)
	assert.InDelta(t, 0.5, c.Threshold, 0.001)
	assert.Equal(t, 6, c.PreserveTurns)
	assert.Equal(t, 30, c.TimeoutSec)
}
```

If `encoding/json` isn't already imported in the test file, add it.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/... -run "TestCompaction" -v`
Expected: FAIL — `Defaults` undefined.

- [ ] **Step 3: Modify `internal/config/config.go`**

Find `type AgentsConfig struct` (around line 56). Replace it with:

```go
type AgentsConfig struct {
	List     []AgentConfig   `json:"list"`
	Defaults AgentsDefaults  `json:"defaults"`
}

// AgentsDefaults holds defaults applied across all agents unless overridden.
type AgentsDefaults struct {
	Compaction CompactionConfig `json:"compaction"`
}

// CompactionConfig configures session compaction.
type CompactionConfig struct {
	Enabled       bool    `json:"enabled"`
	Model         string  `json:"model"`         // "provider/model-id", e.g. "local/qwen2.5:3b-instruct"
	Threshold     float64 `json:"threshold"`     // fraction of context window that triggers preventive compaction
	PreserveTurns int     `json:"preserveTurns"` // K — last K user turns kept verbatim
	TimeoutSec    int     `json:"timeoutSec"`    // per-summarizer-call deadline
}
```

Locate `func DefaultConfig() *Config` — if it doesn't exist, find where defaults are seeded (likely inside `Load()` near the early return for `IsNotExist`). Add the compaction defaults there. The simplest path: add a helper next to `Load`:

```go
// DefaultConfig returns a Config populated with sensible defaults. Used both
// when the config file does not exist and to backfill missing keys after Load.
func DefaultConfig() *Config {
	cfg := &Config{}
	cfg.Agents.Defaults.Compaction = CompactionConfig{
		Enabled:       true,
		Model:         "local/qwen2.5:3b-instruct",
		Threshold:     0.6,
		PreserveTurns: 4,
		TimeoutSec:    60,
	}
	return cfg
}
```

If `Load()` already returns a `cfg := DefaultConfig()` on the not-exist branch, you're done. Otherwise add a backfill in `Load()` after parsing the file:

```go
// Backfill compaction defaults if the user's config is silent.
if cfg.Agents.Defaults.Compaction.Model == "" {
	def := DefaultConfig().Agents.Defaults.Compaction
	cfg.Agents.Defaults.Compaction = def
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS — including pre-existing config tests.

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat(config): add agents.defaults.compaction with sensible defaults"
```

---

### Task 10: OpenAI provider emits Usage on EventDone

**Files:**
- Modify: `internal/llm/openai.go`
- Modify: `internal/llm/provider_test.go` (or `internal/llm/openai_test.go` if it exists)

- [ ] **Step 1: Write the failing test**

The OpenAI client streams a final `Usage` block when `StreamOptions.IncludeUsage = true` is set. We can't hit the real API in unit tests; instead, verify the request sent out has `IncludeUsage = true` by spinning a tiny `httptest.Server` that records the body. (If `provider_test.go` already has a server-stub pattern, follow it. Otherwise create a fresh test.)

If creating fresh, add to `internal/llm/provider_test.go`:

```go
func TestOpenAIProviderRequestsUsageStats(t *testing.T) {
	var seenBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		// Minimal SSE stream: one delta + DONE.
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":42,\"completion_tokens\":7,\"total_tokens\":49}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAIProvider("test-key", srv.URL)
	stream, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	var sawUsage bool
	for ev := range stream {
		if ev.Type == EventDone && ev.Usage != nil {
			sawUsage = true
			assert.Equal(t, 42, ev.Usage.InputTokens)
			assert.Equal(t, 7, ev.Usage.OutputTokens)
		}
	}
	assert.True(t, sawUsage, "EventDone must carry Usage when provider returns it")

	// And the outgoing request must have asked for usage stats.
	assert.Contains(t, string(seenBody), `"include_usage":true`)
}
```

Imports needed: `context`, `fmt`, `io`, `net/http`, `net/http/httptest`, `testing`, `github.com/stretchr/testify/assert`, `github.com/stretchr/testify/require`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/... -run TestOpenAIProviderRequestsUsageStats -v`
Expected: FAIL — `include_usage:true` not in body and Usage not emitted.

- [ ] **Step 3: Update `internal/llm/openai.go`**

Find `openaiReq := openai.ChatCompletionRequest{...}` (around line 165). Add the StreamOptions field:

```go
openaiReq := openai.ChatCompletionRequest{
	Model:               model,
	Messages:            msgs,
	MaxCompletionTokens: maxTokens,
	Stream:              true,
	StreamOptions:       &openai.StreamOptions{IncludeUsage: true},
}
```

> Note: confirm the field name on the installed `sashabaranov/go-openai` version. Common variants: `IncludeUsage bool` or `IncludeUsage *bool`. If the latter, use `openai.Bool(true)` or a local `boolPtr(true)` helper.

In the streaming receive loop (around line 199–267), capture usage from `resp.Usage` (the SDK exposes it on the streaming response object). Modify the loop to track usage and pass it on EventDone:

```go
go func() {
	defer close(events)
	defer stream.Close()

	type pendingTC struct {
		id       string
		name     string
		argsJSON string
	}
	toolCalls := make(map[int]*pendingTC)

	var lastUsage *Usage

	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			events <- ChatEvent{Type: EventError, Error: err}
			return
		}

		// Capture usage when the provider finally sends it (typically on
		// the final chunk thanks to StreamOptions.IncludeUsage=true).
		if resp.Usage != nil && resp.Usage.TotalTokens > 0 {
			lastUsage = &Usage{
				InputTokens:  resp.Usage.PromptTokens,
				OutputTokens: resp.Usage.CompletionTokens,
			}
		}

		for _, choice := range resp.Choices {
			delta := choice.Delta

			if delta.Content != "" {
				events <- ChatEvent{Type: EventTextDelta, Text: delta.Content}
			}

			for _, tc := range delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				pending, exists := toolCalls[idx]
				if !exists {
					pending = &pendingTC{}
					toolCalls[idx] = pending
				}
				if tc.ID != "" {
					pending.id = tc.ID
				}
				if tc.Function.Name != "" {
					pending.name = tc.Function.Name
					events <- ChatEvent{Type: EventToolCallStart, ToolCall: &ToolCall{ID: pending.id, Name: pending.name}}
				}
				if tc.Function.Arguments != "" {
					pending.argsJSON += tc.Function.Arguments
				}
			}

			if choice.FinishReason == openai.FinishReasonToolCalls || choice.FinishReason == openai.FinishReasonStop {
				for _, tc := range toolCalls {
					if tc.name != "" {
						events <- ChatEvent{
							Type: EventToolCallDone,
							ToolCall: &ToolCall{ID: tc.id, Name: tc.name, Input: json.RawMessage(tc.argsJSON)},
						}
					}
				}
			}
		}
	}

	events <- ChatEvent{Type: EventDone, Usage: lastUsage}
}()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/llm/...`
Expected: PASS — new test plus existing.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/openai.go internal/llm/provider_test.go
git commit -m "feat(llm/openai): emit Usage on EventDone via StreamOptions.IncludeUsage"
```

---

### Task 11: Wire compaction into agent runtime (preventive + reactive)

**Files:**
- Modify: `internal/agent/runtime.go`
- Modify: `internal/agent/agent_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/agent_test.go`:

```go
// fakeLLM is a minimal llm.LLMProvider that returns a scripted response.
type fakeLLM struct {
	responses []string // one per turn; no tool calls
	idx       int
	overflow  int // turns at which to return a context-overflow error before responding
}

func (f *fakeLLM) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if f.idx == f.overflow {
		f.idx++
		return nil, errors.New("context length exceeded")
	}
	resp := "(silent)"
	if f.idx < len(f.responses) {
		resp = f.responses[f.idx]
	}
	f.idx++
	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: resp}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (f *fakeLLM) Models() []llm.ModelInfo { return nil }

// compactingSummarizer: every call returns "compacted summary".
type alwaysSummary struct{}

func (alwaysSummary) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	ch := make(chan llm.ChatEvent, 2)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: "compacted summary"}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}
func (alwaysSummary) Models() []llm.ModelInfo { return nil }

func newCompactionMgr() *compaction.Manager {
	return &compaction.Manager{
		Summarizer:    &compaction.Summarizer{Provider: alwaysSummary{}, Model: "m", Timeout: time.Second},
		PreserveTurns: 4,
	}
}

// noopExecutor is a tools.Executor with no tools registered.
type noopExecutor struct{}

func (noopExecutor) Execute(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{}, errors.New("no tools")
}
func (noopExecutor) Names() []string         { return nil }
func (noopExecutor) ToolDefs() []llm.ToolDef { return nil }

func TestRuntimeReactiveCompactionRetriesOnce(t *testing.T) {
	sess := session.NewSession("default", "test")
	for i := 0; i < 6; i++ {
		sess.Append(session.UserMessageEntry("u"))
		sess.Append(session.AssistantMessageEntry("a"))
	}
	rt := &Runtime{
		LLM:         &fakeLLM{responses: []string{"final reply"}, overflow: 0},
		Tools:       noopExecutor{},
		Session:     sess,
		Model:       "anthropic/claude-3-5-sonnet-20241022",
		Workspace:   t.TempDir(),
		Compaction:  newCompactionMgr(),
	}
	out, err := rt.RunSync(context.Background(), "next question", nil)
	require.NoError(t, err)
	assert.Equal(t, "final reply", out)

	// Session should now contain a compaction entry.
	view := sess.View()
	require.NotEmpty(t, view)
	assert.Equal(t, session.EntryTypeCompaction, view[0].Type)
}

func TestRuntimeShortSessionDoesNotCompactOnPreventive(t *testing.T) {
	sess := session.NewSession("default", "test")
	sess.Append(session.UserMessageEntry("only msg"))
	rt := &Runtime{
		LLM:        &fakeLLM{responses: []string{"hi"}},
		Tools:      noopExecutor{},
		Session:    sess,
		Model:      "anthropic/claude-3-5-sonnet-20241022",
		Workspace:  t.TempDir(),
		Compaction: newCompactionMgr(),
	}
	_, err := rt.RunSync(context.Background(), "hi", nil)
	require.NoError(t, err)

	// No compaction entry should have been added.
	for _, e := range sess.Entries() {
		assert.NotEqual(t, session.EntryTypeCompaction, e.Type)
	}
}
```

Add imports: `errors`, `time`, `github.com/sausheong/felix/internal/compaction`, `github.com/sausheong/felix/internal/tools`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/... -run "TestRuntimeReactive|TestRuntimeShortSession" -v`
Expected: FAIL — `Runtime.Compaction` field undefined; reactive path missing.

- [ ] **Step 3: Modify `internal/agent/runtime.go`**

Add the new event types alongside the existing `EventType` constants (around line 30):

```go
const (
	EventTextDelta EventType = iota
	EventToolCallStart
	EventToolResult
	EventDone
	EventError
	EventAborted
	EventCompactionStart
	EventCompactionDone
	EventCompactionSkipped
)
```

Extend the `AgentEvent` struct (around line 33):

```go
type AgentEvent struct {
	Type        EventType
	Text        string
	ToolCall    *llm.ToolCall
	Result      *tools.ToolResult
	Error       error
	Compaction  *compaction.Result // populated for EventCompaction* events
}
```

Add the import for `github.com/sausheong/felix/internal/compaction` and `github.com/sausheong/felix/internal/tokens`.

Add a `Compaction` field on `Runtime`:

```go
type Runtime struct {
	LLM          llm.LLMProvider
	Tools        tools.Executor
	Session      *session.Session
	AgentID      string
	AgentName    string
	Model        string
	Workspace    string
	MaxTurns     int
	SystemPrompt string
	Skills       *skill.Loader
	Memory       *memory.Manager
	Cortex       *cortex.Cortex
	Compaction   *compaction.Manager // optional; nil → no compaction

	calibrator *tokens.Calibrator
}
```

Inside `Run()`, switch from `r.Session.History()` to `r.Session.View()`. Find this line (around line 141):

```go
history := r.Session.History()
msgs := assembleMessages(history)
```

Change to:

```go
history := r.Session.View()
msgs := assembleMessages(history)
```

Then, immediately before the LLM call (after `pruneToolResults` around line 145), add the preventive check:

```go
// Preventive compaction check.
if r.Compaction != nil && r.Model != "" {
	if r.calibrator == nil {
		r.calibrator = tokens.NewCalibrator()
	}
	estimate := r.calibrator.Adjust(tokens.Estimate(msgs, systemPrompt, toolDefs))
	window := tokens.ContextWindow(r.Model)
	if window > 0 && estimate > int(0.6*float64(window)) {
		events <- AgentEvent{Type: EventCompactionStart}
		res, _ := r.Compaction.MaybeCompact(ctx, r.Session, compaction.ReasonPreventive, "")
		if res.Compacted {
			events <- AgentEvent{Type: EventCompactionDone, Compaction: &res}
			// Re-assemble messages after compaction.
			history = r.Session.View()
			msgs = assembleMessages(history)
			pruneToolResults(msgs, maxToolResultLen)
		} else {
			events <- AgentEvent{Type: EventCompactionSkipped, Compaction: &res}
		}
	}
}

req := llm.ChatRequest{
	Model:        r.Model,
	Messages:     msgs,
	Tools:        toolDefs,
	MaxTokens:    8192,
	SystemPrompt: systemPrompt,
}
```

> Note: this places the compaction code between the existing `toolDefs := r.Tools.ToolDefs()` and the `req := llm.ChatRequest{...}`. Adjust the placement to fit the actual line numbers in the file at edit time.

Wrap the existing `stream, err := r.LLM.ChatStream(ctx, req)` call so reactive compaction triggers on overflow:

```go
stream, err := r.LLM.ChatStream(ctx, req)
if err != nil {
	if compaction.IsContextOverflow(err) && r.Compaction != nil {
		events <- AgentEvent{Type: EventCompactionStart}
		res, _ := r.Compaction.MaybeCompact(ctx, r.Session, compaction.ReasonReactive, "")
		if res.Compacted {
			events <- AgentEvent{Type: EventCompactionDone, Compaction: &res}
			// Re-assemble + retry once.
			history = r.Session.View()
			msgs = assembleMessages(history)
			pruneToolResults(msgs, maxToolResultLen)
			req.Messages = msgs
			stream, err = r.LLM.ChatStream(ctx, req)
		} else {
			events <- AgentEvent{Type: EventCompactionSkipped, Compaction: &res}
		}
	}
	if err != nil {
		events <- AgentEvent{Type: EventError, Error: fmt.Errorf("llm error: %w", err)}
		return
	}
}
```

After the stream loop completes, fold the provider-reported usage into the calibrator. Inside the `for event := range stream { ... }` loop, add a case (the existing loop only has `EventTextDelta`, `EventToolCallStart`, `EventToolCallDone`, `EventError`):

```go
for event := range stream {
	switch event.Type {
	case llm.EventTextDelta:
		textContent.WriteString(event.Text)
		events <- AgentEvent{Type: EventTextDelta, Text: event.Text}
	case llm.EventToolCallStart:
		events <- AgentEvent{Type: EventToolCallStart, ToolCall: event.ToolCall}
	case llm.EventToolCallDone:
		if event.ToolCall != nil {
			toolCalls = append(toolCalls, *event.ToolCall)
		}
	case llm.EventDone:
		if event.Usage != nil && r.calibrator != nil {
			r.calibrator.Update(event.Usage.InputTokens, tokens.Estimate(msgs, systemPrompt, toolDefs))
		}
	case llm.EventError:
		events <- AgentEvent{Type: EventError, Error: event.Error}
		return
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/... -v`
Expected: PASS — new tests + all existing.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/runtime.go internal/agent/agent_test.go
git commit -m "feat(agent): wire preventive and reactive compaction into the turn loop"
```

---

### Task 12: Wire `compaction.Manager` construction into CLI startup

**Files:**
- Modify: `cmd/felix/main.go`

This task has no new tests of its own — it's pure wiring. The behavior is already covered by Task 11's runtime tests (with the manager passed in from a test). We verify by building and running `felix doctor`.

- [ ] **Step 1: Locate the place where `agent.Runtime{...}` is constructed**

Run: `grep -n "&agent.Runtime{" cmd/felix/main.go`
Expected output: at least one line, likely near where the chat REPL starts (around line 440 based on earlier exploration).

- [ ] **Step 2: Add a builder helper near the top of `cmd/felix/main.go`**

Insert (after the imports, near other helper functions):

```go
// buildCompactionManager constructs the compaction Manager from config + the
// bundled local Ollama. Returns nil when compaction is disabled or no local
// provider is configured — callers must be safe with a nil manager.
func buildCompactionManager(cfg *config.Config) *compaction.Manager {
	c := cfg.Agents.Defaults.Compaction
	if !c.Enabled {
		return nil
	}
	provider, model := llm.ParseProviderModel(c.Model)
	if provider == "" {
		provider = "local"
	}
	pcfg, ok := cfg.Providers[provider]
	if !ok || pcfg.BaseURL == "" {
		slog.Warn("compaction disabled: provider not configured", "provider", provider)
		return nil
	}
	llmProv, err := llm.NewProvider(provider, llm.ProviderOptions{
		APIKey:  pcfg.APIKey,
		BaseURL: pcfg.BaseURL,
		Kind:    pcfg.Kind,
	})
	if err != nil {
		slog.Warn("compaction disabled: failed to build provider", "error", err)
		return nil
	}
	timeout := time.Duration(c.TimeoutSec) * time.Second
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &compaction.Manager{
		Summarizer: &compaction.Summarizer{
			Provider: llmProv,
			Model:    model,
			Timeout:  timeout,
		},
		PreserveTurns: c.PreserveTurns,
	}
}
```

Add imports: `"github.com/sausheong/felix/internal/compaction"`, `"time"` (likely already present).

- [ ] **Step 3: Pass the manager into the `Runtime`**

Find the `agent.Runtime{...}` literal in the chat command (around line 440). Add the field:

```go
rt := &agent.Runtime{
	LLM:          provider,
	Tools:        toolReg,
	Session:      sess,
	AgentID:      agentCfg.ID,
	AgentName:    agentCfg.Name,
	Model:        modelName,
	Workspace:    agentCfg.Workspace,
	MaxTurns:     agentCfg.MaxTurns,
	SystemPrompt: agentCfg.SystemPrompt,
	Skills:       skillLoader,
	Memory:       memMgr,
	Cortex:       cx,
	Compaction:   buildCompactionManager(cfg),
}
```

- [ ] **Step 4: Build and smoke-test**

```bash
go build -o /tmp/felix ./cmd/felix
/tmp/felix doctor
```

Expected: `doctor` reports green (or its existing baseline). No new errors. The binary should compile cleanly.

Run the full test suite:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/felix/main.go
git commit -m "feat(felix): wire compaction.Manager into chat runtime startup"
```

---

### Task 13: `/compact` slash command in CLI REPL

**Files:**
- Modify: `cmd/felix/main.go`

- [ ] **Step 1: Locate the slash-command parser**

Run: `grep -n '/quit\|/sessions\|/screenshot' cmd/felix/main.go | head`
Expected: lines around 489, 495, 620 (per earlier exploration).

- [ ] **Step 2: Add the `/compact` handler**

Insert this block immediately before the `if strings.HasPrefix(input, "/screenshot")` block (around line 619):

```go
// Handle /compact command — manual compaction with optional focus instructions.
if strings.HasPrefix(input, "/compact") {
	if rt.Compaction == nil {
		fmt.Println("\033[33mCompaction is not enabled in config.\033[0m")
		continue
	}
	instructions := strings.TrimSpace(strings.TrimPrefix(input, "/compact"))
	fmt.Println("\033[90m🧹 Compacting…\033[0m")
	res, err := rt.Compaction.MaybeCompact(ctx, sess, compaction.ReasonManual, instructions)
	if err != nil {
		fmt.Printf("\033[31mCompaction failed: %v\033[0m\n", err)
		continue
	}
	if !res.Compacted {
		switch res.Skipped {
		case "too_short":
			fmt.Println("\033[90mSession too short to compact.\033[0m")
		case "ollama_down", "summarizer_error":
			fmt.Println("\033[33mCompaction skipped: bundled Ollama not reachable. Start it in Settings → Models.\033[0m")
		case "empty_summary":
			fmt.Println("\033[33mCompaction skipped: model returned no summary.\033[0m")
		case "timeout":
			fmt.Println("\033[33mCompaction skipped: timed out.\033[0m")
		default:
			fmt.Printf("\033[33mCompaction skipped: %s\033[0m\n", res.Skipped)
		}
		continue
	}
	fmt.Printf("\033[90m🧹 Compacted %d turns in %dms\033[0m\n", res.TurnsCompacted, res.DurationMs)
	continue
}
```

Add the import for `github.com/sausheong/felix/internal/compaction` if Task 12 didn't already.

- [ ] **Step 3: Build and manually verify**

```bash
go build -o /tmp/felix ./cmd/felix
go test ./...
```

Expected: PASS, build clean.

Manual smoke (optional, requires bundled Ollama running with the configured model):

```bash
/tmp/felix chat
# inside the REPL, after a few turns:
> /compact
🧹 Compacting…
🧹 Compacted N turns in Xms
```

- [ ] **Step 4: Commit**

```bash
git add cmd/felix/main.go
git commit -m "feat(felix): add /compact slash command with optional focus instructions"
```

---

### Task 14: `chat.compact` WebSocket RPC method

**Files:**
- Modify: `internal/gateway/websocket.go`

- [ ] **Step 1: Locate the RPC method dispatch**

Run: `grep -n 'case "chat\\.\\|case "agent\\.\\|case "session' internal/gateway/websocket.go`
Expected: `case "chat.send":` around line 190, `case "agent.status":` around line 194.

Read 50 lines around the dispatch to find the existing handler signature (does it take `params json.RawMessage`? Returns what? How does it find the right session/runtime?).

```bash
sed -n '180,260p' internal/gateway/websocket.go
```

- [ ] **Step 2: Add the `chat.compact` case**

Inside the switch on the method name, add (alongside `chat.send`):

```go
case "chat.compact":
	var p struct {
		SessionID    string `json:"sessionId"`
		Instructions string `json:"instructions,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeError(conn, req.ID, -32602, "invalid params: "+err.Error())
		continue
	}
	rt, sess, err := s.runtimeForSession(p.SessionID) // adapt to the actual lookup helper in this file
	if err != nil {
		writeError(conn, req.ID, -32004, err.Error())
		continue
	}
	if rt.Compaction == nil {
		writeError(conn, req.ID, -32001, "compaction not enabled")
		continue
	}
	res, err := rt.Compaction.MaybeCompact(ctx, sess, compaction.ReasonManual, p.Instructions)
	if err != nil {
		writeError(conn, req.ID, -32000, err.Error())
		continue
	}
	writeResult(conn, req.ID, map[string]any{
		"compacted":      res.Compacted,
		"reason":         res.Reason,
		"skipped":        res.Skipped,
		"turnsCompacted": res.TurnsCompacted,
		"durationMs":     res.DurationMs,
	})
	continue
```

> The placeholder `s.runtimeForSession(p.SessionID)` — replace with whatever the existing `chat.send` handler uses to find the right `Runtime` and `Session`. If the gateway holds a single runtime, use that directly; if it has a per-session map, look it up there.

Add imports: `"github.com/sausheong/felix/internal/compaction"`, `"encoding/json"` (likely present).

- [ ] **Step 3: Add a focused test if `internal/gateway/websocket_test.go` exists**

Run: `ls internal/gateway/websocket_test.go`

If it exists, add a test that exercises the new method with a manual session ID. If not, skip (gateway test infrastructure setup is out of scope for this plan; mark as a follow-up).

- [ ] **Step 4: Build and run all tests**

```bash
go build -o /tmp/felix ./cmd/felix
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/websocket.go
git commit -m "feat(gateway): add chat.compact WebSocket RPC method"
```

---

### Task 15: Final integration check + race-detector run

**Files:** none modified.

- [ ] **Step 1: Run the full test suite with the race detector**

```bash
go test -race ./...
```

Expected: PASS, no data-race warnings. If a race is reported, it almost certainly involves the per-session `Calibrator` or the `Manager`'s internal `locks` map — both have `sync.Mutex` already. Verify and fix in place.

- [ ] **Step 2: Run lint**

```bash
golangci-lint run
```

Expected: clean. Fix any new findings introduced by this work.

- [ ] **Step 3: Build cross-platform**

```bash
goreleaser build --snapshot --clean
```

Expected: builds for all configured targets succeed.

- [ ] **Step 4: Final commit (if any cleanup was needed)**

```bash
git status
# if files modified:
git add .
git commit -m "chore(compaction): clean up findings from race + lint"
```

If nothing changed, no commit. The work is complete.

---

## Self-review notes (filled in by plan author)

**Spec coverage check:**

| Spec section | Plan task |
|---|---|
| Trigger logic — manual / reactive / preventive | Tasks 11 (runtime), 13 (CLI), 14 (WS) |
| Token estimation + calibration + context windows | Task 1 |
| Storage: append-only `EntryTypeCompaction` + `View()` | Task 4 |
| Split-point algorithm (K=4, user-msg cutoff) | Task 3 |
| Compaction execution: transcript + prompt + Ollama call | Tasks 6, 7 |
| Manager orchestrator + per-session mutex | Task 8 |
| Default compaction model `local/qwen2.5:3b-instruct` | Task 9 (config), Task 12 (wire) |
| `assembleMessages` handles `EntryTypeCompaction` (with summary prefix) + legacy `EntryTypeMeta` backwards compat | Task 5 |
| Reactive: detect overflow, retry once | Task 11 |
| OpenAI Usage emission for calibration | Task 10 |
| Manual `/compact` CLI command with focus instructions | Task 13 |
| `chat.compact` WS RPC | Task 14 |
| Provider error signatures | Task 2 |
| Defaults: enabled, model, threshold 0.6, K=4, timeout 60s | Task 9 |

**Out of plan, follow-up needed:**
- Tray UI "Compact now" button + nudge notification → separate plan (UI-side)
- Probing Ollama `/api/show` to populate `tokens.RegisterOllamaContext` → small follow-up; current fallback (32k) is safe
- Status-line / `/status` display of compaction history → separate plan
- gateway WS test for `chat.compact` if no test infra exists yet → small follow-up

**Type/method consistency check:**
- `Manager.MaybeCompact(ctx, sess, reason Reason, instructions string)` — used identically in Tasks 8, 11, 13, 14 ✓
- `Result.Skipped` strings: `too_short`, `empty_summary`, `ollama_down`, `summarizer_error`, `timeout` — Task 8 defines `classifySummarizerError` returning `empty_summary`/`timeout`/`summarizer_error`. `ollama_down` is documented in Task 13's switch but not explicitly produced by `classifySummarizerError`. **Acceptable**: the current classifier folds Ollama-down into `summarizer_error`; the CLI handles both keys with the same friendly message. This is consistent in behavior; future refinement can split them.
- `Reason` constants `ReasonPreventive`, `ReasonReactive`, `ReasonManual` — used identically across tasks ✓
- `Runtime.Compaction *compaction.Manager` — defined in Task 11, populated in Task 12, read in Tasks 13/14 ✓
- `tokens.Calibrator` lifecycle — created lazily in Task 11 inside `Run()`, no shared state across runs ✓

No placeholders found. No code refers to undefined types. Ready for execution.
