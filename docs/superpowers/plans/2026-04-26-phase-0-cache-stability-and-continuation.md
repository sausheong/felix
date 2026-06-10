# Phase 0 — Cache Stability & Continuation Wrapper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the "I'm ready to help! Our previous conversation covered Colima…" framing-drift symptom and make prompt-prefix byte-stability a regression-testable property. No architectural change.

**Architecture:** Three small, independent changes: (1) inject a continuation directive after the compaction summary so the model resumes rather than restarts; (2) add a regression test that captures the LLM request prefix across turns and asserts it is byte-stable; (3) fix the concrete cache-stability bug in `tools.Registry.ToolDefs()` (which iterates a Go map) plus an audit pass for similar issues. Each change ships independently.

**Tech Stack:** Go 1.x, `github.com/stretchr/testify`, existing `mockLLMProvider` test pattern in `internal/agent/agent_test.go`.

---

## Spec reference

Read first: `docs/superpowers/specs/2026-04-26-context-engineering-roadmap.md` (Phase 0 section).

Inspirations: Claude Code's `getCompactUserSummaryMessage` (`/Users/sausheong/projects/claude-code-source/src/services/compact/prompt.ts:337-374`) for the continuation wrapper text. OpenClaw's cache-stability rule (`/Users/sausheong/projects/openclaw/AGENTS.md:161-167`) for the test discipline.

## File Structure

**New files:**
- `internal/agent/cache_stability_test.go` — request-prefix invariance regression test
- `internal/tools/tool_test.go` — *if* it doesn't already exist; otherwise a new test in the existing file

**Modified files:**
- `internal/agent/context.go` — extend the compaction-summary user message with the continuation directive
- `internal/agent/agent_test.go` — new test asserting the continuation directive is present in assembled messages
- `internal/tools/tool.go` — sort `ToolDefs()` and `Names()` output by tool name

## Task ordering rationale

TDD order: continuation wrapper first because it's the smallest and addresses the visible user-facing bug. Then the deterministic `ToolDefs()` fix (write the failing test, watch it fail, sort, watch it pass). Then the broader request-prefix-stability test that exercises the agent loop end-to-end. Each task ends with a green test suite and a commit.

---

### Task 1: Continuation directive on compaction summary message

**Files:**
- Test: `internal/agent/agent_test.go` (extend existing test file)
- Modify: `internal/agent/context.go:167-176`

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/agent_test.go`:

```go
// internal/agent/agent_test.go (append)

func TestCompactionMessageIncludesContinuationDirective(t *testing.T) {
	sess := session.NewSession("test-agent", "test-key")
	sess.Append(session.UserMessageEntry("first user msg"))
	sess.Append(session.AssistantMessageEntry("first reply"))
	sess.Append(session.CompactionEntry(
		"User asked about Wasm; we recommended Extism. They then asked for details on how it works.",
		"", "", "test-model", 0, 0, 2,
	))

	msgs := assembleMessages(sess.View())
	require.NotEmpty(t, msgs)

	// The compaction summary becomes a user message. It must include both
	// the summary text and the continuation directive that tells the model
	// to resume rather than restart.
	var summaryMsg *llm.Message
	for i := range msgs {
		if strings.Contains(msgs[i].Content, "Previous conversation summary") {
			summaryMsg = &msgs[i]
			break
		}
	}
	require.NotNil(t, summaryMsg, "compaction entry must produce a user message")

	assert.Contains(t, summaryMsg.Content, "Wasm", "summary text must be present")
	assert.Contains(t, summaryMsg.Content, "Resume directly",
		"continuation directive must instruct the model to resume")
	assert.Contains(t, summaryMsg.Content, "do not acknowledge the summary",
		"continuation directive must forbid restarting")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestCompactionMessageIncludesContinuationDirective -v`
Expected: FAIL — assertion on `"Resume directly"` fails because `context.go:174` only emits `"[Previous conversation summary]\n\n" + cd.Summary`.

- [ ] **Step 3: Implement minimal fix**

Edit `internal/agent/context.go` lines 167-176 (the `case session.EntryTypeCompaction:` block in `assembleMessages`). Replace the `msgs = append(...)` block with:

```go
		case session.EntryTypeCompaction:
			var cd session.CompactionData
			if err := json.Unmarshal(entry.Data, &cd); err != nil {
				continue
			}
			// The summary is followed by an explicit continuation directive
			// so the model resumes the conversation rather than treating it
			// as a fresh start. Without this, models tend to reply with
			// openers like "I'm ready to help! Our previous conversation
			// covered X" — which loses the in-flight task context that
			// the user's next message implicitly relies on.
			content := "[Previous conversation summary]\n\n" + cd.Summary +
				"\n\nContinue the conversation from where it left off without asking the user any further questions. " +
				"Resume directly — do not acknowledge the summary, do not recap what was happening, " +
				"do not preface with \"I'll continue\" or similar. Pick up the last task as if the break never happened."
			msgs = append(msgs, llm.Message{
				Role:    "user",
				Content: content,
			})
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestCompactionMessageIncludesContinuationDirective -v`
Expected: PASS.

- [ ] **Step 5: Run the full agent test suite to verify no regressions**

Run: `go test ./internal/agent/ -race`
Expected: PASS (all tests).

- [ ] **Step 6: Commit**

```bash
git add internal/agent/context.go internal/agent/agent_test.go
git commit -m "feat(compaction): add continuation directive to summary message

Without this, models reply to the just-summarized session with openers
like \"I'm ready to help!\" — treating the resumption as a fresh
conversation. The directive tells the model to pick up the last task
as if the break never happened.

Pattern from Claude Code (getCompactUserSummaryMessage in
src/services/compact/prompt.ts:337-374). Closes the framing-drift
symptom that surfaced after the compaction-View bug fix."
```

---

### Task 2: Make `tools.Registry.ToolDefs()` deterministic

**Files:**
- Test: `internal/tools/tool_test.go` (new file or append to existing)
- Modify: `internal/tools/tool.go:220-244`

- [ ] **Step 1: Check whether `tool_test.go` already exists**

Run: `ls internal/tools/tool_test.go 2>/dev/null && echo EXISTS || echo NEW`

If EXISTS, append the test in Step 2 to that file. If NEW, create it with package declaration and imports.

- [ ] **Step 2: Write the failing test**

Add to `internal/tools/tool_test.go` (create file if needed):

```go
package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// stubTool is a minimal Tool implementation for ordering tests.
type stubTool struct {
	name string
}

func (s *stubTool) Name() string                                   { return s.name }
func (s *stubTool) Description() string                            { return s.name + " description" }
func (s *stubTool) Parameters() json.RawMessage                    { return json.RawMessage(`{}`) }
func (s *stubTool) Execute(ctx context.Context, in json.RawMessage) (ToolResult, error) {
	return ToolResult{}, nil
}

// TestToolDefsAreDeterministic guards against map-iteration cache breakage.
// Tool definitions feed directly into the LLM prefix; nondeterministic order
// invalidates the prompt cache on every turn — both a 10× cost regression
// and a real correctness issue when summaries depend on stable history.
func TestToolDefsAreDeterministic(t *testing.T) {
	reg := NewRegistry()
	// Register in non-alphabetical order so a sort is the only way to
	// produce a stable result.
	for _, name := range []string{"zebra", "alpha", "mango", "banana", "kiwi"} {
		reg.Register(&stubTool{name: name})
	}

	first := reg.ToolDefs()
	for i := 0; i < 50; i++ {
		got := reg.ToolDefs()
		assert.Equal(t, len(first), len(got), "length must be stable")
		for j := range first {
			assert.Equal(t, first[j].Name, got[j].Name,
				"position %d must be stable across calls (iteration %d)", j, i)
		}
	}

	// The result must also be sorted — that's the documented invariant
	// callers can rely on, not just "happens to be stable in this run".
	for i := 1; i < len(first); i++ {
		assert.LessOrEqual(t, first[i-1].Name, first[i].Name,
			"ToolDefs must return tools sorted by name")
	}
}

// TestNamesAreDeterministic — same property for Names(), which is reachable
// from the system-prompt assembly code in agent/context.go (toolNames feeds
// buildDefaultIdentity).
func TestNamesAreDeterministic(t *testing.T) {
	reg := NewRegistry()
	for _, name := range []string{"zebra", "alpha", "mango"} {
		reg.Register(&stubTool{name: name})
	}

	got := reg.Names()
	assert.Equal(t, []string{"alpha", "mango", "zebra"}, got,
		"Names() must return tools sorted by name")
}
```

- [ ] **Step 3: Run tests to verify they fail (or flake)**

Run: `go test ./internal/tools/ -run "TestToolDefsAreDeterministic|TestNamesAreDeterministic" -v -count=10`
Expected: FAIL on at least one iteration. With 5 tools the chance of map iteration accidentally producing alphabetical order is ~1/120; running with `-count=10` makes the failure deterministic.

If the tests pass on the very first run, increase `-count` to 50 — Go map iteration is randomized but the seed varies per invocation.

- [ ] **Step 4: Implement minimal fix**

Edit `internal/tools/tool.go`. Add `"sort"` to the import block if not already present. Replace `ToolDefs()` (lines 220-233) and `Names()` (lines 236-244):

```go
// ToolDefs returns the tool definitions for the LLM API.
// Output is sorted by tool name so the LLM-request prefix is stable across
// turns — required for prompt-cache hits and for compaction-summary
// reproducibility. Map iteration in Go is randomized; without sorting,
// every turn would invalidate the cache and a single tool reordering
// could change a generated summary's content.
func (r *Registry) ToolDefs() []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	defs := make([]llm.ToolDef, 0, len(names))
	for _, name := range names {
		t := r.tools[name]
		defs = append(defs, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}
	return defs
}

// Names returns the names of all registered tools, sorted alphabetically.
// See ToolDefs() for why ordering matters.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/tools/ -run "TestToolDefsAreDeterministic|TestNamesAreDeterministic" -v -count=20`
Expected: PASS on every iteration.

- [ ] **Step 6: Run the full tools test suite to verify no regressions**

Run: `go test ./internal/tools/ -race`
Expected: PASS (all tests).

- [ ] **Step 7: Commit**

```bash
git add internal/tools/tool.go internal/tools/tool_test.go
git commit -m "fix(tools): sort ToolDefs() and Names() for cache stability

Go map iteration is randomized. Tools were emitted in nondeterministic
order on every call, which broke prompt-cache hits on every turn and
made compaction summaries nondeterministic across reruns of the same
session. Sort by tool name to fix both."
```

---

### Task 3: Request-prefix stability regression test

**Files:**
- Create: `internal/agent/cache_stability_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/agent/cache_stability_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/tools"
)

// recordingProvider captures every ChatRequest it receives and emits a
// canned text response. Used to inspect what the runtime sends to the
// LLM across turns.
type recordingProvider struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
	reply    string
}

func (r *recordingProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()

	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: r.reply}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (r *recordingProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "rec", Name: "Recording", Provider: "rec"}}
}

// requestPrefixSignature renders the cache-relevant portion of a request:
// system prompt, sorted tool definitions, and the message list excluding
// the final user turn. Two calls in the same session that differ only in
// the freshly-arrived user message must produce identical signatures.
func requestPrefixSignature(t *testing.T, req llm.ChatRequest) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("SYS:")
	sb.WriteString(req.SystemPrompt)
	sb.WriteString("\nTOOLS:")
	// Tool defs must already be sorted by the registry; we re-sort here
	// to make the test independent of any per-test ordering accident.
	names := make([]string, len(req.Tools))
	descByName := make(map[string]string, len(req.Tools))
	paramsByName := make(map[string]string, len(req.Tools))
	for i, td := range req.Tools {
		names[i] = td.Name
		descByName[td.Name] = td.Description
		paramsByName[td.Name] = string(td.Parameters)
	}
	sort.Strings(names)
	for _, n := range names {
		sb.WriteString("\n  ")
		sb.WriteString(n)
		sb.WriteString("|")
		sb.WriteString(descByName[n])
		sb.WriteString("|")
		sb.WriteString(paramsByName[n])
	}
	sb.WriteString("\nMSGS_EXCL_LAST:")
	// All messages except the last one (which is the freshly-arrived user
	// message and is allowed to differ across turns).
	for i := 0; i < len(req.Messages)-1; i++ {
		sb.WriteString("\n  ")
		sb.WriteString(req.Messages[i].Role)
		sb.WriteString(":")
		sb.WriteString(req.Messages[i].Content)
	}
	return sb.String()
}

// TestRequestPrefixIsByteStableAcrossTurns runs two consecutive turns of the
// agent loop with identical inputs. The second request's prefix (system
// prompt + tool defs + all-but-last message) must be byte-identical to the
// first request's full content (system prompt + tool defs + all messages).
//
// This is the cache-stability invariant: turn N+1's prefix is turn N's full
// prompt. Anthropic and OpenAI prompt caches both depend on this. The
// compaction-View bug we shipped a fix for would have been caught by an
// earlier version of this test (it changed the prefix dramatically when
// compaction fired).
func TestRequestPrefixIsByteStableAcrossTurns(t *testing.T) {
	rec := &recordingProvider{reply: "ok"}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	// Register multiple tools in non-alphabetical order so we exercise the
	// ToolDefs sort path.
	reg.Register(&mockTool{name: "zebra", output: "z"})
	reg.Register(&mockTool{name: "alpha", output: "a"})
	reg.Register(&mockTool{name: "mango", output: "m"})

	rt := &Runtime{
		LLM:       rec,
		Tools:     reg,
		Session:   sess,
		Model:     "rec-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	// Turn 1
	events, err := rt.Run(context.Background(), "hello", nil)
	require.NoError(t, err)
	for range events {
	}

	// Turn 2 — same session, same agent, same tools.
	events, err = rt.Run(context.Background(), "world", nil)
	require.NoError(t, err)
	for range events {
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.GreaterOrEqual(t, len(rec.requests), 2,
		"expected at least 2 ChatStream calls, got %d", len(rec.requests))

	req1 := rec.requests[0]
	req2 := rec.requests[1]

	// The prefix signature of turn 2 must equal the FULL signature of turn 1
	// (turn 2's prefix = turn 1's everything-up-to-the-new-user-msg).
	turn1Full := fullSignature(t, req1)
	turn2Prefix := prefixWithoutLastMessage(t, req2)
	assert.Equal(t, turn1Full, turn2Prefix,
		"turn 2 prefix must byte-match turn 1's full request")
}

// fullSignature renders the entire request the same way requestPrefixSignature
// does, but includes the final message too. Used as the turn-N reference
// against which turn-N+1's prefix is compared.
func fullSignature(t *testing.T, req llm.ChatRequest) string {
	t.Helper()
	sig := requestPrefixSignature(t, req)
	// Append the last message (which prefix-signature excluded).
	if len(req.Messages) > 0 {
		last := req.Messages[len(req.Messages)-1]
		sig += "\n  " + last.Role + ":" + last.Content
	}
	return sig
}

// prefixWithoutLastMessage is requestPrefixSignature but with the last
// message of the previous turn (the assistant reply that turn N's run
// appended) re-included, since that's part of turn N+1's prefix.
//
// turn N's request: [sys, tools, ...msgs, userN]
// after turn N: session also contains [...msgs, userN, assistantN]
// turn N+1's request: [sys, tools, ...msgs, userN, assistantN, userN+1]
//                                                  ^^^^^^^^^^^^^^^^^^^^^^
//                                                  the prefix at turn N+1
//                                                  excluding the new user msg
//
// So turn N+1's prefix = turn N's full request + assistantN.
// We test that subset by stripping the last message of turn N+1.
func prefixWithoutLastMessage(t *testing.T, req llm.ChatRequest) string {
	t.Helper()
	if len(req.Messages) == 0 {
		return requestPrefixSignature(t, req)
	}
	clone := req
	clone.Messages = req.Messages[:len(req.Messages)-1]
	return fullSignature(t, clone)
}

// Hint to readers grepping for json: imported above only to keep
// json.RawMessage compile-checkable in mockTool below. Actual test does
// not deserialize anything.
var _ = json.RawMessage(nil)
```

- [ ] **Step 2: Run test to verify it fails before the Task 2 fix is in**

If you executed the tasks in order, Task 2's fix is already merged and this test will pass on the first try. To prove the test catches the bug, temporarily revert Task 2's `sort.Strings(names)` calls and re-run:

Run: `go test ./internal/agent/ -run TestRequestPrefixIsByteStableAcrossTurns -v -count=10`
Expected with Task 2 reverted: FAIL on at least one iteration (turn 2 prefix differs from turn 1 full because tool order is randomized).
Expected with Task 2 in place: PASS on every iteration.

Restore Task 2's sort calls before continuing.

- [ ] **Step 3: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestRequestPrefixIsByteStableAcrossTurns -v -count=20`
Expected: PASS on every iteration.

- [ ] **Step 4: Run the full agent test suite to verify no regressions**

Run: `go test ./internal/agent/ -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/cache_stability_test.go
git commit -m "test(agent): add request-prefix stability regression test

Asserts that turn N+1's request prefix (system prompt + tools +
all-but-new-user-message) is byte-identical to turn N's full request.
This is the cache-stability invariant Anthropic and OpenAI prompt
caches depend on. An earlier version of this test would have caught
the compaction-View bug — it changed the prefix dramatically when
compaction fired."
```

---

### Task 4: Audit other prompt-bound map iterations

The cache-stability test in Task 3 only catches issues reachable from a normal turn. We grep the codebase for additional map iterations that produce output the LLM sees, and fix any nondeterministic ones.

**Files:**
- Modify: any file identified by the grep that builds prompt-bound output from a map.

- [ ] **Step 1: Run the audit grep**

Run:
```bash
grep -rn "for [a-zA-Z_][a-zA-Z_]*, [a-zA-Z_][a-zA-Z_]* := range " \
  internal/agent/ internal/skill/ internal/mcp/ internal/tools/ internal/config/ \
  internal/cortex/ internal/memory/ internal/router/ internal/heartbeat/ \
  | grep -v _test.go
```

For each result, classify:
- **PROMPT-BOUND** — the iteration produces a string or slice that ends up in the LLM request (system prompt, tool list, message content, etc.). Must be sorted.
- **INTERNAL** — the iteration is for purposes that don't reach the LLM (lookup, dispatch, internal indexing). No fix needed.

Document the classification inline as a comment for any iteration you DO touch, so the next reader doesn't re-debate it.

- [ ] **Step 2: For each PROMPT-BOUND iteration, write a targeted determinism test**

Pattern (adapt for the actual file):

```go
// internal/<pkg>/<file>_test.go (append)
func TestXOutputIsDeterministic(t *testing.T) {
	// Construct the input map with N entries in non-alphabetical order.
	// Call the function under test.
	// Repeat 20 times and assert byte-equal output every time.
	// Optionally: assert the output is sorted (documents the invariant).
}
```

For the symbols below, the analysis at plan-write time identified:

- `internal/tools/tool.go:225` — `ToolDefs()` map iteration: **PROMPT-BOUND**, fixed in Task 2.
- `internal/tools/tool.go:240` — `Names()` map iteration: **PROMPT-BOUND** (feeds `buildDefaultIdentity` via `agent/context.go`), fixed in Task 2.

Other candidates: re-examine after running the grep in Step 1. Likely INTERNAL (config-loading lookups, MCP server registries used for dispatch only, memory/cortex indexing). If grep produces a hit you cannot confidently classify as INTERNAL, treat it as PROMPT-BOUND.

- [ ] **Step 3: For each PROMPT-BOUND iteration found in Step 1, apply the sort fix**

Pattern (adapt for the actual file):

```go
import "sort"

// Before:
for k, v := range theMap {
    out = append(out, render(k, v))
}

// After:
keys := make([]string, 0, len(theMap))
for k := range theMap {
    keys = append(keys, k)
}
sort.Strings(keys)
for _, k := range keys {
    out = append(out, render(k, theMap[k]))
}
```

- [ ] **Step 4: Run the full test suite**

Run: `go test ./... -race`
Expected: PASS.

- [ ] **Step 5: Commit (only if Step 1 found additional fixes)**

```bash
git add <files-touched>
git commit -m "fix(<pkg>): sort <thing> output for cache stability

Followup to the ToolDefs sort. <Function> iterated a Go map and emitted
output that reached the LLM request. Sort by <key> for prefix stability."
```

If Step 1 found no PROMPT-BOUND iterations beyond those already fixed in Task 2, no commit is needed for this task — the audit's value is the documented review pass and the determinism tests added in Step 2.

---

## Self-review checklist (run before handing off)

- [ ] Each task has a failing test before the fix and a passing test after.
- [ ] No "TBD" / "implement later" / "similar to Task N" placeholders.
- [ ] Type and function names used in later tasks match earlier tasks (`ToolDefs` not `GetToolDefs`, `Names` not `ToolNames`, `recordingProvider` consistent across files).
- [ ] Every code block compiles standalone with the imports shown nearby — no implicit `package` or `import` assumptions.
- [ ] Commit messages reference the *why* not just the *what*; the bug post-mortem context is preserved in the history.
- [ ] No file is created or modified outside the File Structure section.
