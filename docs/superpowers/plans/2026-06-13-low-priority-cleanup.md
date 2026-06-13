# Low-Priority Cleanup Batch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sweep 12 verified low-severity findings (L1/L3/L4/L5/L7/L8/L9/L10/L11 + G6/G7/G8) across the harness and felix repos in one round.

**Architecture:** harness changes land first (6 tasks: L1, L3, L4, L5, L10 — L8-skill is felix-only), verified by felix building against them via go.mod replace; then felix changes (6 tasks: L7, L8, L9, L11, G6, G7, G8). Each fix is independent and localized; TDD where hermetically testable.

**Tech Stack:** Go 1.25; testify; harness `llm`/`tokens`/`session`/`providers`/`runtime`; felix `internal/skill`/`internal/router`/`internal/mcp`/`internal/gateway`/`cmd/felix`.

**Repos:** harness at `/Users/sausheong/projects/harness`, felix at `/Users/sausheong/projects/felix`. After ANY harness change, `cd /Users/sausheong/projects/felix && go build ./...` must pass (go.mod replace wiring).

**Commit convention:** NO `Co-Authored-By` trailer (project memory).

---

## PART A — harness (do these first, on a harness branch)

All harness tasks happen in `/Users/sausheong/projects/harness`. Create branch `cleanup/low-batch` there before Task A1:
```bash
cd /Users/sausheong/projects/harness && git checkout -b cleanup/low-batch
```

### Task A1: L1 — `tokens.Estimate` accounts for images

**Files:**
- Modify: `tokens/tokens.go`
- Test: `tokens/tokens_test.go` (create if absent; check first)

- [ ] **Step 1: Write the failing test**

Check whether `tokens/tokens_test.go` exists (`ls tokens/`). If it exists, append; if not, create it with a package clause `package tokens` and imports `testing`, `github.com/sausheong/harness/llm`, `github.com/stretchr/testify/require`.

```go
func TestEstimate_CountsImages(t *testing.T) {
	textOnly := []llm.Message{{Role: "user", Content: "hello"}}
	withImg := []llm.Message{{Role: "user", Content: "hello", Images: []llm.ImageContent{
		{MimeType: "image/png", Data: []byte("x")},
		{MimeType: "image/png", Data: []byte("y")},
	}}}
	base := Estimate(textOnly, "", nil)
	withImages := Estimate(withImg, "", nil)
	// Two images must add at least 2 * perImageTokens tokens.
	require.GreaterOrEqual(t, withImages-base, 2*perImageTokens)
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./tokens/ -run TestEstimate_CountsImages`
Expected: FAIL — `undefined: perImageTokens` (and the count won't include images).

- [ ] **Step 3: Implement**

In `tokens/tokens.go`, inside the `for _, m := range msgs` loop in `Estimate`, after the existing `total += ...` line and the ToolCalls loop, add image accounting. The cleanest spot is right after the `for _, tc := range m.ToolCalls { ... }` block, still inside the message loop:

```go
		// Images are not part of Content (Images is json:"-"); approximate
		// each attached image's token cost. perImageTokens is expressed in
		// the same char units as the rest of this function (divided by 4 at
		// the end), so multiply by 4 to land on the intended token figure.
		total += len(m.Images) * perImageTokens * 4
```

Add the constant near `perMessageOverhead`:

```go
// perImageTokens approximates the token cost of one attached image. Vision
// models bill images by tiles/resolution; ~1500 tokens is a deliberately
// conservative flat estimate so image-heavy sessions trigger preventive
// compaction rather than hitting a reactive overflow.
const perImageTokens = 1500
```

Note: because `Estimate` returns `total / 4`, and `perImageTokens` is already a token figure, we multiply by 4 here so the final divide cancels it back to `perImageTokens` per image. (perMessageOverhead is left as a char-unit value by design; images are specified directly in tokens, hence the ×4.)

- [ ] **Step 4: Run test, verify pass**

Run: `go test ./tokens/ -run TestEstimate_CountsImages`
Expected: PASS. `withImages - base` should be exactly `2*perImageTokens` (2 images × 1500, the text is identical).

- [ ] **Step 5: Full package + vet**

Run: `go vet ./tokens/ && go test ./tokens/`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add tokens/tokens.go tokens/tokens_test.go
git commit -m "fix(tokens): count attached images in Estimate (~1500 tokens each)"
```

---

### Task A2: L3 — Qwen reports usage + uses MaxCompletionTokens

**Files:**
- Modify: `providers/qwen/qwen.go`
- Test: `providers/qwen/qwen_test.go` (check existing pattern first)

- [ ] **Step 1: Inspect the sibling + existing tests**

Read `providers/openai/openai.go:222-232` (the canonical request construction — it uses `MaxCompletionTokens: maxTokens` and `StreamOptions: &openai.StreamOptions{IncludeUsage: true}`). Read `providers/qwen/qwen_test.go` to learn how requests are asserted (if there's a way to capture the constructed `openai.ChatCompletionRequest`). If the qwen tests use a mock/fake openai server or a request-capture hook, mirror it. If there is NO existing seam to assert the request, add a minimal test that exercises the request-building path and asserts via whatever is observable; if truly untestable without a server, note it and rely on build + the openai sibling's existing coverage of the same fields.

- [ ] **Step 2: Write the failing test (if a seam exists)**

If qwen_test.go has a request-capture pattern, add:
```go
func TestQwen_RequestIncludesUsage(t *testing.T) {
	// ... using the existing capture pattern, build a request and assert:
	// req.StreamOptions != nil && req.StreamOptions.IncludeUsage == true
	// req.MaxCompletionTokens > 0 && req.MaxTokens == 0
}
```
Run it; expect FAIL.

If no seam exists, SKIP the test (document why in the commit body) and proceed — this matches the spec's "items hard to unit-test rely on build + sibling coverage" note.

- [ ] **Step 3: Implement**

In `providers/qwen/qwen.go`, change the request construction (currently ~line 205):

From:
```go
	openaiReq := openai.ChatCompletionRequest{
		Model:     model,
		Messages:  msgs,
		MaxTokens: maxTokens,
		Stream:    true,
	}
```
To:
```go
	openaiReq := openai.ChatCompletionRequest{
		Model:               model,
		Messages:            msgs,
		MaxCompletionTokens: maxTokens,
		Stream:              true,
		StreamOptions:       &openai.StreamOptions{IncludeUsage: true},
	}
```

- [ ] **Step 4: Verify**

Run: `go vet ./providers/qwen/ && go test ./providers/qwen/`
Expected: clean (existing tests still pass; new test passes if added).

- [ ] **Step 5: Commit**

```bash
git add providers/qwen/qwen.go providers/qwen/qwen_test.go
git commit -m "fix(qwen): request usage stats (IncludeUsage) and use MaxCompletionTokens"
```

---

### Task A3: L4 — one-time degraded-persistence warning

**Files:**
- Modify: `session/store.go`
- Test: `session/store_test.go` (check existing)

- [ ] **Step 1: Write the failing test**

Read `session/store_test.go` for the construction pattern. Add a test that forces write failures and asserts a single degraded warning. Since the warning goes through slog, capture it with a `slog` handler installed via `slog.SetDefault` for the test (save/restore the prior default). Example:

```go
func TestStore_DegradedPersistenceWarnsOnce(t *testing.T) {
	// Install a capturing slog handler.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	// baseDir that cannot be written: create a file where the agent dir should go.
	tmp := t.TempDir()
	// Force MkdirAll to fail by making baseDir a path under a regular file.
	blocker := filepath.Join(tmp, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	s := NewStore(blocker) // sessionDir(agentID) = blocker/<agentID> → MkdirAll fails (parent is a file)

	sess := &Session{AgentID: "a", Key: "k"}
	s.AppendEntry(sess, SessionEntry{})
	s.AppendEntry(sess, SessionEntry{})
	s.AppendEntry(sess, SessionEntry{})

	got := strings.Count(buf.String(), "session persistence degraded")
	require.Equal(t, 1, got, "degraded warning must fire exactly once")
}
```
Adjust `Session`/`SessionEntry` construction to match the real types (read store.go). The key mechanism: `MkdirAll` on a path whose parent is a regular file returns an error, exercising the failure branch.

- [ ] **Step 2: Run, verify fail**

Run: `go test ./session/ -run TestStore_DegradedPersistenceWarnsOnce`
Expected: FAIL — currently 0 occurrences of "session persistence degraded" (the code logs "failed to create session dir" at Error, not the degraded warning).

- [ ] **Step 3: Implement**

Add an atomic flag to the `Store` struct (`session/store.go`):
```go
type Store struct {
	baseDir  string
	mu       sync.Mutex
	degraded atomic.Bool
}
```
Add `"sync/atomic"` to imports.

Add a helper method:
```go
// markDegraded emits a single warning the first time session persistence
// fails, so a user notices that in-memory state may not survive a restart.
// Subsequent failures stay at their existing Error level to avoid log spam.
func (s *Store) markDegraded(reason string, err error) {
	if s.degraded.CompareAndSwap(false, true) {
		slog.Warn("session persistence degraded; in-memory state may not survive restart",
			"reason", reason, "error", err)
	}
}
```

In `AppendEntry`, at each `slog.Error(...)` + `return` failure site (the MkdirAll, OpenFile, and Write failures — the marshal failure is a programming error, leave it), call `s.markDegraded(...)` immediately before the existing `slog.Error`. Example for the MkdirAll site:
```go
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.markDegraded("create session dir", err)
		slog.Error("failed to create session dir", "error", err)
		return
	}
```
Apply the same `s.markDegraded(...)` call before the `slog.Error` at the OpenFile and Write failure sites. Keep all existing control flow (still return on error).

- [ ] **Step 4: Run, verify pass**

Run: `go test ./session/ -run TestStore_DegradedPersistenceWarnsOnce`
Expected: PASS (exactly one degraded warning across three failed appends).

- [ ] **Step 5: Full package + vet + race**

Run: `go vet ./session/ && go test ./session/ && go test -race ./session/ -run TestStore_DegradedPersistence`
Expected: clean (CompareAndSwap is race-safe).

- [ ] **Step 6: Commit**

```bash
git add session/store.go session/store_test.go
git commit -m "fix(session): emit one-time warning when persistence is degraded"
```

---

### Task A4: L5 — `RunSync` surfaces aborts

**Files:**
- Modify: `runtime/runtime.go` (RunSync, ~line 841)
- Test: `runtime/runtime_test.go` or wherever RunSync is tested (check)

- [ ] **Step 1: Write the failing test**

Find how aborted runs are tested (the codebase emits `EventAborted`; `agent_test.go` references it ~855/986). Add a test that drives a run which aborts and asserts `RunSync` returns a non-nil error matching `context.Canceled`. Reuse the existing test scaffolding for aborts (a tool that blocks + a cancelled ctx, or whatever pattern agent_test.go uses to produce EventAborted). Sketch:
```go
func TestRunSync_ReturnsErrorOnAbort(t *testing.T) {
	// Build a runtime whose run will emit EventAborted (reuse the abort
	// scaffolding from agent_test.go — e.g. cancel ctx mid-run).
	// ...
	_, err := rt.RunSync(ctx, "do something", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}
```
If building a full abort scenario is heavy, prefer a focused unit: if `Run` is mockable or there's a lighter seam, use it. If the only path is the full agent scaffolding, adapt the closest existing abort test.

- [ ] **Step 2: Run, verify fail**

Expected: FAIL — RunSync currently returns `(partialText, nil)` on abort.

- [ ] **Step 3: Implement**

In `runtime/runtime.go` `RunSync`, add a case to the event switch:
```go
	for event := range events {
		switch event.Type {
		case EventTextDelta:
			response.WriteString(event.Text)
		case EventAborted:
			return response.String(), context.Canceled
		case EventError:
			return response.String(), event.Error
		}
	}
```
Confirm `context` is imported in runtime.go (it almost certainly is — RunSync takes a ctx). If the package defines a dedicated abort sentinel error, prefer that; otherwise `context.Canceled` is correct.

- [ ] **Step 4: Run, verify pass**

Expected: PASS.

- [ ] **Step 5: Full package + vet**

Run: `go vet ./runtime/ && go test ./runtime/`
Expected: clean. (This is a behavior change — if any existing test asserted RunSync returns nil on abort, update it to expect the error; report if so.)

- [ ] **Step 6: Commit**

```bash
git add runtime/runtime.go runtime/runtime_test.go
git commit -m "fix(runtime): RunSync returns context.Canceled on aborted run"
```

---

### Task A5: L10 — tighten retry classifier numeric matching

**Files:**
- Modify: `llm/retry.go` (~lines 54-59)
- Test: `llm/retry_test.go`

- [ ] **Step 1: Write the failing test**

Append to `llm/retry_test.go` (read it first for the function name under test — it's the retryable classifier, likely `isRetryable` or similar; match the real name):
```go
func TestRetry_DigitsInRequestIDNotRetryable(t *testing.T) {
	// "429" appearing only inside a request id must NOT be treated as a
	// rate-limit signal.
	err := errors.New("request failed: req_abc429def something went wrong")
	require.False(t, <classifierFunc>(err))
}

func TestRetry_RealStatus429Retryable(t *testing.T) {
	err := errors.New("openai: error, status 429 too many requests")
	require.True(t, <classifierFunc>(err))
}
```
Replace `<classifierFunc>` with the actual exported/unexported function tested elsewhere in the file.

- [ ] **Step 2: Run, verify fail**

Expected: the `req_abc429def` case FAILS (current `strings.Contains(msg, "429")` matches it as retryable).

- [ ] **Step 3: Implement**

In `llm/retry.go`, replace the substring fallback block:
```go
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "429") || strings.Contains(msg, "529") ||
		strings.Contains(msg, "rate limit") || strings.Contains(msg, "overloaded") {
		return true
	}
	return false
```
with bounded numeric forms (keep the word matches):
```go
	msg := strings.ToLower(err.Error())
	// Bounded forms so a bare "429"/"529" inside a request id or unrelated
	// digits doesn't trigger a retry. Word matches stay as-is (specific).
	for _, sig := range []string{
		"status 429", "code: 429", "code:429", " 429 ", "429 too many requests",
		"status 529", "code: 529", "code:529", " 529 ", "529",
	} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	if strings.Contains(msg, "rate limit") || strings.Contains(msg, "overloaded") {
		return true
	}
	return false
```
NOTE on 529: Anthropic's "overloaded" is 529 and its error text reliably contains "overloaded", so the bare `"529"` entry is retained as a low-risk catch (529 is far rarer in request ids than 429); if the reviewer judges bare "529" too loose, narrow it to the bounded forms only and rely on "overloaded". The implementer should keep bare "529" ONLY if the existing retry tests for Anthropic 529 still pass with the bounded set; otherwise drop bare "529". Verify against the existing tests.

- [ ] **Step 4: Run, verify pass**

Run: `go test ./llm/ -run TestRetry`
Expected: all retry tests pass, including the new ones AND the pre-existing 429/529 cases.

- [ ] **Step 5: Full package + vet**

Run: `go vet ./llm/ && go test ./llm/`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add llm/retry.go llm/retry_test.go
git commit -m "fix(llm): scope retry classifier to bounded 429/529 forms"
```

---

### Task A6: harness verification + felix-builds-against-harness gate

- [ ] **Step 1: Full harness suite**

```bash
cd /Users/sausheong/projects/harness
go build ./... && go vet ./... && go test ./... && go test -race ./tokens/ ./session/ ./llm/ ./runtime/ ./providers/qwen/
```
Expected: all green.

- [ ] **Step 2: felix builds against the modified harness**

```bash
cd /Users/sausheong/projects/felix && go build ./...
```
Expected: clean (go.mod replace points at local harness; this proves the harness changes don't break the consumer).

No commit (verification only). PART A complete.

---

## PART B — felix (after PART A; on a felix branch)

All felix tasks happen in `/Users/sausheong/projects/felix`. Create branch `cleanup/low-batch` there before Task B1:
```bash
cd /Users/sausheong/projects/felix && git checkout -b cleanup/low-batch
```

### Task B1: L7 — tighten `extractTitle` + `SplitFrontmatter` matching

**Files:**
- Modify: `internal/memory/memory.go` (`extractTitle`, ~line 505)
- Modify: `internal/skill/skill.go` (`SplitFrontmatter`, ~line 222)
- Test: `internal/memory/memory_test.go`, `internal/skill/skill_test.go`

- [ ] **Step 1: Write failing tests**

memory_test.go:
```go
func TestExtractTitle_LineStartOnly(t *testing.T) {
	// "# " mid-line (e.g. inside prose) must NOT be taken as the title.
	content := "intro text with a # hash not at line start\n# Real Title\nbody"
	require.Equal(t, "Real Title", extractTitle("id1", content))
	// No line-start H1 → falls back to id.
	require.Equal(t, "id2", extractTitle("id2", "no heading here # nope"))
}
```
skill_test.go:
```go
func TestSplitFrontmatter_ExactClosingFence(t *testing.T) {
	// A "----" line must NOT close the frontmatter.
	in := "---\nname: x\n----\nbody"
	fm, _ := SplitFrontmatter(in)
	require.NotContains(t, fm, "name: x\n----", "must not close on ----")
	// A proper "---" line closes it.
	in2 := "---\nname: y\n---\nbody text"
	fm2, body2 := SplitFrontmatter(in2)
	require.Equal(t, "name: y", strings.TrimSpace(fm2))
	require.Equal(t, "body text", strings.TrimSpace(body2))
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/memory/ -run TestExtractTitle_LineStartOnly && go test ./internal/skill/ -run TestSplitFrontmatter_ExactClosingFence`
Expected: FAIL (current `strings.Index(content, "# ")` matches mid-line; current `strings.Index(rest, "\n---")` matches `\n----`).

- [ ] **Step 3: Implement extractTitle**

Replace `extractTitle` (`internal/memory/memory.go:505`):
```go
func extractTitle(id, content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return id
}
```

- [ ] **Step 4: Implement SplitFrontmatter closing-fence check**

In `internal/skill/skill.go` `SplitFrontmatter`, the closing-fence detection currently uses `strings.Index(rest, "\n---")` which matches `\n----` and `\n---x`. Replace the endIdx search with a line-exact scan. Replace:
```go
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return "", content
	}

	frontmatter = rest[:endIdx]
	body = rest[endIdx+4:]
```
with:
```go
	// Find a line whose trimmed content is exactly "---" (not "----" or
	// "---publish:"). Scan line by line tracking byte offsets.
	endIdx := -1
	bodyStart := -1
	off := 0
	for {
		nl := strings.IndexByte(rest[off:], '\n')
		var line string
		if nl < 0 {
			line = rest[off:]
		} else {
			line = rest[off : off+nl]
		}
		if strings.TrimRight(line, "\r") == "---" {
			endIdx = off
			if nl < 0 {
				bodyStart = len(rest)
			} else {
				bodyStart = off + nl + 1
			}
			break
		}
		if nl < 0 {
			break
		}
		off += nl + 1
	}
	if endIdx < 0 {
		return "", content
	}

	frontmatter = strings.TrimRight(rest[:endIdx], "\n")
	body = rest[bodyStart:]
```
This replaces the old `body = rest[endIdx+4:]` and the subsequent "trim the newline after closing ---" block is no longer needed (bodyStart already points past the fence's newline). REMOVE the now-dead trailing block:
```go
	// Trim the newline after closing ---
	if len(body) > 0 && body[0] == '\n' {
		body = body[1:]
	} else if len(body) > 1 && body[0] == '\r' && body[1] == '\n' {
		body = body[2:]
	}
```
Verify the existing `TestSplitFrontmatter` still passes after this restructure — if its expectations differ slightly on trailing-whitespace, reconcile (the body content must be preserved; leading/trailing surrounding whitespace handling should match prior behavior closely enough that the existing test passes; if it legitimately needs a tweak, adjust the test and report).

- [ ] **Step 5: Run, verify pass**

Run: `go test ./internal/memory/ ./internal/skill/`
Expected: new tests pass; existing `TestSplitFrontmatter` and memory tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/memory/memory.go internal/skill/skill.go internal/memory/memory_test.go internal/skill/skill_test.go
git commit -m "fix(memory,skill): line-exact title and frontmatter-fence matching"
```

---

### Task B2: L8 — delete dead code

**Files:**
- Modify: `internal/skill/skill.go` (delete MatchSkills ~109-188, FormatForPrompt ~256-277, fix FormatIndex doc comment ~282)
- Modify: `internal/skill/skill_test.go` (delete TestMatchSkills ~234, TestFormatForPrompt ~259, TestFormatForPromptEmpty ~270)
- Modify: `internal/local/installer.go` (delete shortDeadline ~153-157)

- [ ] **Step 1: Confirm zero callers (guard against regressions)**

```bash
cd /Users/sausheong/projects/felix
grep -rn "MatchSkills\|FormatForPrompt" --include="*.go" . | grep -v "_test.go" | grep -v "/.claude/"
grep -rn "shortDeadline" --include="*.go" internal/local/
```
Expected: the first returns NOTHING (zero non-test callers). `shortDeadline` appears only at its definition. If either shows a real caller, STOP and report — do not delete.

- [ ] **Step 2: Delete the test functions first**

In `internal/skill/skill_test.go`, delete `TestMatchSkills`, `TestFormatForPrompt`, `TestFormatForPromptEmpty` in full. (Deleting tests first means after Step 3 the package still compiles.)

- [ ] **Step 3: Delete the source functions**

In `internal/skill/skill.go`:
- Delete `func (l *Loader) MatchSkills(...)` in full, including its doc comment (the whole block from `// MatchSkills returns skills...` through its closing `}`).
- Delete `func FormatForPrompt(...)` in full, including its doc comment.
- Fix the `FormatIndex` doc comment: it currently ends with "The full body of relevant skills is still injected separately via MatchSkills + FormatForPrompt." Replace that sentence with: "Full skill bodies are loaded on demand via the load_skill tool." (Reflects current reality — there is no MatchSkills injection.)

In `internal/local/installer.go`:
- Delete `func shortDeadline(...)` and its doc comment (lines ~153-157). KEEP `bytesReader`/`bytesReadCloser` (used at 62/94/131).

- [ ] **Step 4: Verify the deletions don't strand imports**

After deleting, check `internal/skill/skill.go` for now-unused imports (MatchSkills used `strings.Fields`/`strings.ToLower`/`strings.Contains` — but SplitFrontmatter and others still use `strings`, so it stays. Confirm via build). Check `internal/local/installer.go` for unused imports after shortDeadline goes (it used `context`/`time` — both near-certainly still used elsewhere; confirm via build).

- [ ] **Step 5: Build + test + vet**

Run: `go build ./... && go vet ./internal/skill/ ./internal/local/ && go test ./internal/skill/ ./internal/local/`
Expected: clean. No undefined references.

- [ ] **Step 6: Commit**

```bash
git add internal/skill/skill.go internal/skill/skill_test.go internal/local/installer.go
git commit -m "refactor: remove dead skill.MatchSkills/FormatForPrompt and unused shortDeadline"
```

---

### Task B3: L9 — router global precedence

**Files:**
- Modify: `internal/router/router.go` (`Route`, ~lines 24-62)
- Test: `internal/router/router_test.go`

- [ ] **Step 1: Write the failing test**

Read `internal/router/router_test.go` for the binding/message construction pattern, then add:
```go
func TestRoute_SpecificBeatsBroadRegardlessOfOrder(t *testing.T) {
	// A broad peer.kind binding declared BEFORE a specific peer.id binding
	// must NOT win — peer.id has higher precedence.
	r := &Router{
		bindings: []Binding{
			{AgentID: "broad", Match: Match{Peer: &PeerMatch{Kind: "group"}}},
			{AgentID: "specific", Match: Match{Peer: &PeerMatch{ID: "u123"}}},
		},
		fallback: "fb",
	}
	msg := channel.InboundMessage{SenderID: "u123", ChatType: "group"}
	require.Equal(t, "specific", r.Route(msg))
}
```
Adjust type names (`Binding`, `Match`, `PeerMatch`, `channel.InboundMessage`, field names `ChatType`/`SenderID`) to the real definitions — READ router.go and the channel types first. The point: peer.id binding placed second still wins over a peer.kind binding placed first.

- [ ] **Step 2: Run, verify fail**

Expected: FAIL — current first-match-wins returns "broad" because the kind binding is evaluated first and returns immediately.

- [ ] **Step 3: Implement**

Rewrite `Route` to score every binding by precedence and return the highest-ranked match. Replace the body of `Route`:
```go
func (r *Router) Route(msg channel.InboundMessage) string {
	const (
		rankNone = iota
		rankChannel
		rankAccount
		rankKind
		rankPeerID
	)
	bestRank := rankNone
	bestAgent := ""

	consider := func(rank int, agentID string) {
		if rank > bestRank {
			bestRank = rank
			bestAgent = agentID
		}
	}

	for _, b := range r.bindings {
		m := b.Match
		channelOK := m.Channel == "" || m.Channel == msg.Channel

		if m.Peer != nil && m.Peer.ID != "" && m.Peer.ID == msg.SenderID && channelOK {
			consider(rankPeerID, b.AgentID)
		}
		if m.Peer != nil && m.Peer.Kind != "" && m.Peer.Kind == string(msg.ChatType) && channelOK {
			consider(rankKind, b.AgentID)
		}
		if m.AccountID != "" && m.AccountID == msg.AccountID && channelOK {
			consider(rankAccount, b.AgentID)
		}
		if m.Channel == msg.Channel && m.Peer == nil && m.AccountID == "" {
			consider(rankChannel, b.AgentID)
		}
	}

	if bestAgent != "" {
		return bestAgent
	}
	return r.fallback
}
```
Preserve the exact field/type names from the real code (the above mirrors the original conditions; just match identifiers). The precedence high→low is peer.id > peer.kind > accountId > channel, matching the documented order.

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/router/`
Expected: new test passes; all existing router tests still pass (they should — single-binding cases are unaffected by ranking).

- [ ] **Step 5: Vet**

Run: `go vet ./internal/router/`

- [ ] **Step 6: Commit**

```bash
git add internal/router/router.go internal/router/router_test.go
git commit -m "fix(router): enforce global match precedence (specific beats broad)"
```

---

### Task B4: G7 — health endpoint uses json encoder

**Files:**
- Modify: `internal/gateway/server.go` (`handleHealth`, ~line 155)
- Test: `internal/gateway/server_test.go` (or wherever health is tested; create test if feasible)

- [ ] **Step 1: Write the failing test**

Check for an existing health test. Add (in the gateway test package):
```go
func TestHandleHealth_ValidJSON(t *testing.T) {
	s := &Server{} // health handler uses no Server state; if it does, construct minimally
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	s.handleHealth(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	var body struct {
		Status    string `json:"status"`
		Timestamp string `json:"timestamp"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "ok", body.Status)
	require.NotEmpty(t, body.Timestamp)
}
```
If `handleHealth` is a method needing a constructed Server, build the minimal one (read server.go). Imports: `net/http`, `net/http/httptest`, `encoding/json`, `testing`, testify.

- [ ] **Step 2: Run, verify fail or pass-trivially**

Run the test. The CURRENT implementation already sets Content-Type and emits valid JSON, so this test may PASS against the current code. That's fine — it's a guard for the refactor. If it passes now, note that; the refactor must keep it passing. (The test still has value: it locks the contract before swapping the implementation.)

- [ ] **Step 3: Implement**

Replace `handleHealth` (`internal/gateway/server.go:155`):
```go
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}
```
Ensure `encoding/json` is imported in server.go (add if missing). `time` is already used.

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/gateway/ -run TestHandleHealth_ValidJSON`
Expected: PASS.

- [ ] **Step 5: Full gateway build + vet**

Run: `go build ./internal/gateway/ && go vet ./internal/gateway/`
Expected: clean. (Don't run the full gateway suite here if it's slow/flaky; the final gate covers it.)

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/server.go internal/gateway/server_test.go
git commit -m "fix(gateway): encode /health response with json encoder"
```

---

### Task B5: G6 + L11 — log level + CLI ergonomics

These are grouped: G6 is a one-line log-level change; L11 is two CLI fixes. No new hermetic tests (signal/log-level/stdin plumbing); verified by build + vet + inspection per spec.

**Files:**
- Modify: `internal/gateway/websocket.go` (`writeJSON`, ~line 1700)
- Modify: `cmd/felix/main.go` (stdin loop ~602; SIGTERM ~206-218)

- [ ] **Step 1: G6 — downgrade writeJSON error**

In `internal/gateway/websocket.go` `writeJSON` (~1700), change:
```go
	if err := conn.WriteJSON(v); err != nil {
		slog.Error("websocket write error", "error", err)
	}
```
to:
```go
	if err := conn.WriteJSON(v); err != nil {
		// A disconnected client is expected, not an error condition; under
		// event fan-out every queued write would otherwise spam Error logs.
		slog.Debug("websocket write failed (client likely disconnected)", "error", err)
	}
```

- [ ] **Step 2: L11 — buffer CLI stdin**

In `cmd/felix/main.go`, the interactive loop (~595-615) reads one byte per syscall via `os.Stdin.Read(buf)`. Replace the inner byte-read loop with a `bufio.Reader` (the file already imports `bufio`). Rewrite the read loop:
```go
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return nil
		}
		input := strings.TrimSpace(line)
		if input == "" {
			if err != nil {
				return nil
			}
			continue
		}
		// ... (rest of the loop body that processes `input` stays unchanged)
```
IMPORTANT: read the FULL existing loop body first (lines ~595 to the loop's end) and preserve everything after `input := strings.TrimSpace(...)` exactly — only the reading mechanism changes. The `reader` must be created ONCE before the `for` loop, not inside it (creating it inside would discard buffered bytes between iterations). Verify the loop's existing exit conditions are preserved.

- [ ] **Step 3: L11 — force-quit on second SIGTERM**

In `runStart` (~204-218), the current code waits on a single `<-stop` then cleans up; a second signal during cleanup is ignored. Change to force-exit on the second signal:
```go
	<-stop
	slog.Info("shutting down gateway... (press Ctrl-C again to force)")
	go func() {
		<-stop
		slog.Warn("forced shutdown")
		os.Exit(1)
	}()
	result.Cleanup()
	return nil
```
This keeps `signal.Notify(stop, ...)` registered (the channel has buffer 1), so a second signal lands and triggers the force-exit goroutine. Confirm `os` is imported (it is).

- [ ] **Step 4: Build both binaries + vet**

```bash
go build -o /tmp/felix-cli ./cmd/felix && go build ./internal/gateway/ && go vet ./cmd/felix/ ./internal/gateway/
```
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/websocket.go cmd/felix/main.go
git commit -m "fix(gateway,cli): debug-level dead-conn log; buffered stdin; force-quit on 2nd signal"
```

---

### Task B6: G8 — `.env` basic quoting + inline comments

**Files:**
- Modify: `internal/mcp/creds.go` (`LoadEnvFile`, ~line 20)
- Test: `internal/mcp/creds_test.go` (check existing)

- [ ] **Step 1: Write failing tests**

Read `internal/mcp/creds_test.go` for the pattern (likely writes a temp file then calls LoadEnvFile). Add:
```go
func TestLoadEnvFile_QuotingAndComments(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	content := strings.Join([]string{
		`A="double"`,
		`B='single'`,
		`C=plain # trailing comment`,
		`D=a#b`,                    // no space before # → literal
		`E="has # hash inside"`,    // # inside quotes → preserved
		`F=  spaced  `,             // surrounding spaces trimmed (existing behavior)
	}, "\n")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	env, err := LoadEnvFile(p)
	require.NoError(t, err)
	require.Equal(t, "double", env["A"])
	require.Equal(t, "single", env["B"])
	require.Equal(t, "plain", env["C"])
	require.Equal(t, "a#b", env["D"])
	require.Equal(t, "has # hash inside", env["E"])
	require.Equal(t, "spaced", env["F"])
}
```

- [ ] **Step 2: Run, verify fail**

Expected: FAIL — current code returns `"double"` WITH quotes as `"double"` (literal), `plain # trailing comment` (comment not stripped), etc.

- [ ] **Step 3: Implement**

In `internal/mcp/creds.go` `LoadEnvFile`, replace the value-extraction line `val := strings.TrimSpace(line[eq+1:])` and the `out[key] = val` with a parse step. Add a helper and use it:
```go
		val := parseEnvValue(line[eq+1:])
		out[key] = val
```
Add the helper function (below LoadEnvFile):
```go
// parseEnvValue applies the common .env conventions: surrounding single or
// double quotes are stripped (and their contents taken literally, including
// any '#'); otherwise a trailing inline comment introduced by whitespace+'#'
// is removed. It intentionally does NOT do shell escaping or variable
// interpolation.
func parseEnvValue(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	// Strip a trailing inline comment: a '#' preceded by whitespace.
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	} else if i := strings.Index(v, "\t#"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}
```
Note: the unquoted-comment strip checks for `" #"`/`"\t#"` so `a#b` (no preceding whitespace) stays literal. A quoted value returns before the comment check, so `"has # hash inside"` is preserved. The existing whole-line `#`-comment skip and surrounding `TrimSpace` on the raw line stay as they are (a line starting with `#` is still skipped entirely upstream).

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/mcp/ -run TestLoadEnvFile`
Expected: PASS (new + existing LoadEnvFile tests).

- [ ] **Step 5: Full package + vet**

Run: `go vet ./internal/mcp/ && go test ./internal/mcp/`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/mcp/creds.go internal/mcp/creds_test.go
git commit -m "feat(mcp): support quoted values and inline comments in LoadEnvFile"
```

---

### Task B7: felix final verification

- [ ] **Step 1: Full felix suite**

```bash
cd /Users/sausheong/projects/felix
go build ./... && go vet ./... && go test ./... && go test -race ./internal/...
```
Expected: all green.

- [ ] **Step 2: Both binaries**

```bash
go build -o /tmp/felix-final ./cmd/felix && go build -o /tmp/felix-app-final ./cmd/felix-app
```
Expected: clean.

No commit (verification). PART B complete.

---

## Final controller checklist (after all tasks)

- [ ] harness: build/vet/test/race green; felix builds against it.
- [ ] felix: build/vet/test/race green; both binaries build.
- [ ] All 12 items addressed: L1, L3, L4, L5, L10 (harness); L7, L8, L9, L11, G6, G7, G8 (felix).
- [ ] No `Co-Authored-By` trailer in any commit.
- [ ] Adversarial final review over both diffs before merge.
