# Cortex Recall-First and Full-Thread Ingest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Query Cortex once before the think-act loop (not per turn), and ingest the full conversation thread (user message + tool calls + tool results + final reply) in the background on every exit path.

**Architecture:** Two files change. `internal/cortex/cortex.go` gets `ShouldIngest` updated to accept `[]conversation.Message` and `IngestConversation` renamed to `IngestThread` with that same slice signature. `internal/agent/runtime.go` moves the Cortex recall before the for-loop, builds a thread slice as the run progresses, and uses `defer` to guarantee ingest fires on every exit.

**Tech Stack:** Go, `github.com/sausheong/cortex`, `github.com/sausheong/cortex/connector/conversation`

---

## Files

| File | Action |
|---|---|
| `internal/cortex/cortex.go` | Update `ShouldIngest` signature; rename `IngestConversation` → `IngestThread` |
| `internal/cortex/cortex_test.go` | New — unit tests for `ShouldIngest` and `IngestThread` |
| `internal/agent/runtime.go` | Move recall before loop; add thread accumulation; add deferred ingest; remove old ingest call; add `conversation` import |

---

### Task 1: Update `cortex.go` — new `ShouldIngest` and `IngestThread`

**Files:**
- Modify: `internal/cortex/cortex.go`

- [ ] **Step 1: Read the file**

```bash
cat internal/cortex/cortex.go
```

- [ ] **Step 2: Replace `ShouldIngest` with the multi-message version**

Replace the entire `ShouldIngest` function (lines 88–102 in the original file) with:

```go
// ShouldIngest returns true if the conversation thread contains enough
// substance to be worth storing in the knowledge graph.
func ShouldIngest(thread []conversation.Message) bool {
	if len(thread) == 0 {
		return false
	}
	// Skip if the first user message is a trivial phrase.
	if trivialPhrases[strings.ToLower(strings.TrimSpace(thread[0].Content))] {
		return false
	}
	// Require at least one assistant message and enough combined content.
	total := 0
	hasAssistant := false
	for _, m := range thread {
		total += len(strings.TrimSpace(m.Content))
		if m.Role == "assistant" {
			hasAssistant = true
		}
	}
	return hasAssistant && total >= minIngestLen
}
```

- [ ] **Step 3: Replace `IngestConversation` with `IngestThread`**

Replace the entire `IngestConversation` function (lines 108–123 in the original file) with:

```go
// IngestThread feeds a completed conversation thread into the Cortex knowledge
// graph. The thread should contain all messages for the exchange: user message,
// tool calls (as assistant messages), tool results (as user messages), and the
// final assistant reply. It skips trivial or short threads.
// It runs synchronously; callers should run it in a goroutine if they
// don't want to block.
func IngestThread(ctx context.Context, cx *cortex.Cortex, thread []conversation.Message) {
	if !ShouldIngest(thread) {
		slog.Debug("cortex: skipping trivial thread ingest", "len", len(thread))
		return
	}
	conn := conversation.New()
	if err := conn.Ingest(ctx, cx, thread); err != nil {
		slog.Warn("cortex: thread ingest failed", "error", err)
	}
}
```

- [ ] **Step 4: Verify the file compiles**

```bash
go build ./internal/cortex/
```

Expected: no output (success).

- [ ] **Step 5: Commit**

```bash
git add internal/cortex/cortex.go
git commit -m "feat: replace IngestConversation with IngestThread accepting full message thread"
```

---

### Task 2: Write and pass tests for `cortex.go`

**Files:**
- Create: `internal/cortex/cortex_test.go`

- [ ] **Step 1: Create the test file**

```go
package cortex

import (
	"testing"

	"github.com/sausheong/cortex/connector/conversation"
	"github.com/stretchr/testify/assert"
)

func msgs(pairs ...string) []conversation.Message {
	out := make([]conversation.Message, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, conversation.Message{Role: pairs[i], Content: pairs[i+1]})
	}
	return out
}

func TestShouldIngestNilAndEmpty(t *testing.T) {
	assert.False(t, ShouldIngest(nil))
	assert.False(t, ShouldIngest([]conversation.Message{}))
}

func TestShouldIngestTrivialUserMessage(t *testing.T) {
	thread := msgs("user", "ok", "assistant", "Understood, got it, no problem at all.")
	assert.False(t, ShouldIngest(thread))
}

func TestShouldIngestTooShort(t *testing.T) {
	thread := msgs("user", "hi there", "assistant", "Hello!")
	assert.False(t, ShouldIngest(thread))
}

func TestShouldIngestNoAssistantMessage(t *testing.T) {
	// Only user messages — no assistant reply yet
	thread := msgs("user", "What are the main principles of software architecture and design patterns?")
	assert.False(t, ShouldIngest(thread))
}

func TestShouldIngestValidTwoMessage(t *testing.T) {
	thread := msgs(
		"user", "What are the main principles of clean code architecture?",
		"assistant", "Clean code follows separation of concerns, single responsibility, and dependency inversion.",
	)
	assert.True(t, ShouldIngest(thread))
}

func TestShouldIngestValidWithToolCalls(t *testing.T) {
	thread := msgs(
		"user", "What files are in the project?",
		"assistant", "[tool: bash]\n{\"command\":\"ls -la\"}",
		"user", "main.go\ngo.mod\nREADME.md\ninternal/\ncmd/",
		"assistant", "The project contains main.go, go.mod, README.md, and the internal/ and cmd/ directories.",
	)
	assert.True(t, ShouldIngest(thread))
}

func TestShouldIngestTrivialCaseInsensitive(t *testing.T) {
	thread := msgs("user", "THANKS", "assistant", "You are welcome! Glad I could help with that.")
	assert.False(t, ShouldIngest(thread))
}
```

- [ ] **Step 2: Run tests to verify they pass**

```bash
go test ./internal/cortex/ -v
```

Expected: all 7 tests PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/cortex/cortex_test.go
git commit -m "test: add ShouldIngest unit tests for thread-based signature"
```

---

### Task 3: Update `runtime.go` — recall before loop, thread accumulation, deferred ingest

**Files:**
- Modify: `internal/agent/runtime.go`

- [ ] **Step 1: Read the file**

```bash
cat internal/agent/runtime.go
```

- [ ] **Step 2: Add `conversation` import**

In the import block, add the conversation package after the cortex import:

```go
import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/connector/conversation"
	cortexadapter "github.com/sausheong/felix/internal/cortex"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/memory"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/skill"
	"github.com/sausheong/felix/internal/tools"
)
```

- [ ] **Step 3: Add thread initialisation and recall before the loop**

After the block that appends the user message to the session (around line 76, after `r.Session.Append(...)`), and before `maxTurns := r.MaxTurns`, insert:

```go
		// Initialise Cortex thread and recall (once, before the loop).
		var thread []conversation.Message
		cortexContext := ""
		if r.Cortex != nil {
			thread = []conversation.Message{{Role: "user", Content: userMsg}}

			results, err := r.Cortex.Recall(ctx, userMsg, cortex.WithLimit(5))
			if err != nil {
				slog.Debug("cortex recall error", "error", err)
				cortexContext = cortexadapter.CortexHint
			} else {
				cortexContext = cortexadapter.CortexHint
				if extra := cortexadapter.FormatResults(results); extra != "" {
					cortexContext += extra
				}
			}

			// Deferred ingest fires on every goroutine exit path.
			cx := r.Cortex
			defer func() {
				if len(thread) > 1 {
					go cortexadapter.IngestThread(context.Background(), cx, thread)
				}
			}()
		}
```

- [ ] **Step 4: Replace the in-loop Cortex recall block with cached injection**

Inside the for loop, find and replace this block:

```go
			// Inject Cortex knowledge graph context
			if r.Cortex != nil {
				systemPrompt += cortexadapter.CortexHint
				results, err := r.Cortex.Recall(ctx, userMsg, cortex.WithLimit(5))
				if err != nil {
					slog.Debug("cortex recall error", "error", err)
				} else if extra := cortexadapter.FormatResults(results); extra != "" {
					systemPrompt += extra
				}
			}
```

Replace with:

```go
			// Inject Cortex context (recalled once before the loop).
			if cortexContext != "" {
				systemPrompt += cortexContext
			}
```

- [ ] **Step 5: Accumulate assistant text in the thread**

Find the block that saves the assistant response to the session:

```go
			// Save assistant response to session
			if textContent.Len() > 0 {
				r.Session.Append(session.AssistantMessageEntry(textContent.String()))
			}
```

Replace with:

```go
			// Save assistant response to session
			if textContent.Len() > 0 {
				r.Session.Append(session.AssistantMessageEntry(textContent.String()))
				if r.Cortex != nil {
					thread = append(thread, conversation.Message{
						Role:    "assistant",
						Content: textContent.String(),
					})
				}
			}
```

- [ ] **Step 6: Remove the old inline Cortex ingest**

Find and delete this block (it was inside the `if len(toolCalls) == 0` branch):

```go
				// Auto-ingest conversation into Cortex in background
				if r.Cortex != nil && textContent.Len() > 0 {
					cx := r.Cortex
					uMsg := userMsg
					aMsg := textContent.String()
					go cortexadapter.IngestConversation(context.Background(), cx, uMsg, aMsg)
				}
```

The `defer` added in Step 3 replaces this entirely.

- [ ] **Step 7: Accumulate tool calls and results in the thread**

In the tool execution loop, find the block that saves the tool call to the session:

```go
			// Save tool calls to session
			for _, tc := range toolCalls {
				r.Session.Append(session.ToolCallEntry(tc.ID, tc.Name, tc.Input))
			}
```

Replace with:

```go
			// Save tool calls to session and accumulate in Cortex thread.
			for _, tc := range toolCalls {
				r.Session.Append(session.ToolCallEntry(tc.ID, tc.Name, tc.Input))
				if r.Cortex != nil {
					thread = append(thread, conversation.Message{
						Role:    "assistant",
						Content: fmt.Sprintf("[tool: %s]\n%s", tc.Name, string(tc.Input)),
					})
				}
			}
```

Then find where the tool result is saved to the session:

```go
				// Save tool result to session
				r.Session.Append(session.ToolResultEntry(tc.ID, result.Output, result.Error, imgData))
```

Replace with:

```go
				// Save tool result to session and accumulate in Cortex thread.
				r.Session.Append(session.ToolResultEntry(tc.ID, result.Output, result.Error, imgData))
				if r.Cortex != nil {
					content := result.Output
					if result.Error != "" {
						content = "[error] " + result.Error
					}
					thread = append(thread, conversation.Message{Role: "user", Content: content})
				}
```

- [ ] **Step 8: Build to verify no compile errors**

```bash
go build ./internal/agent/
```

Expected: no output (success).

- [ ] **Step 9: Run all tests**

```bash
go test ./...
```

Expected: same results as before this change. Tests that were passing continue to pass. The `TestAssembleSystemPromptDefault` failure is pre-existing and unrelated to this change.

- [ ] **Step 10: Commit**

```bash
git add internal/agent/runtime.go
git commit -m "feat: recall Cortex once before loop and ingest full thread in background"
```

---

### Task 4: Push

- [ ] **Step 1: Push to remote**

```bash
git push
```
