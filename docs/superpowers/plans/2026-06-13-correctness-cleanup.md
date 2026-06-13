# Correctness Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix correctness bugs (provider stream determinism, memory index/atomicity, slice aliasing, ctx honoring, HTML entities) and wire up cortex auto-recall across `harness` and `felix`.

**Architecture:** Four groups. G1 wires the harness KnowledgeGraph hook to Felix's cortex (new adapter + KGFn). G2 fixes the four provider stream loops. G3 fixes memory index ordering + write atomicity + races. G4 is small defensive cleanups.

**Tech Stack:** Go 1.25, harness runtime KnowledgeGraph interface, cortex library, chromem-go, testify.

**Cross-cutting rules:**
- harness and felix share a `go.mod replace`. After ANY harness change: `cd /Users/sausheong/projects/felix && go build ./...`.
- Run `go test -race` in the repo you changed.
- Bar: observable improvement, no existing flow breaks. R7 = content-identical (only order/count/alloc change). R9 = no-op when cortex disabled.
- Commit messages: NO `Co-Authored-By` trailer.
- Spec: `docs/superpowers/specs/2026-06-13-correctness-cleanup-design.md`.
- If a referenced line number drifted, match on the code snippet, not the number.

---

### Task 1: R7a — deterministic tool-call emission order (OpenAI)

**Files:**
- Modify: `/Users/sausheong/projects/harness/providers/openai/openai.go` (emit loop ~347)
- Test: `/Users/sausheong/projects/harness/providers/openai/openai_test.go` (create if absent)

Context: `toolCalls` is a `map[int]*pendingTC` (key = tool-call index). The emit loop `for _, tc := range toolCalls` iterates in random order, so parallel tool calls emit nondeterministically, breaking prompt-cache stability next turn.

- [ ] **Step 1: Write the failing test**

Read the file first to find existing test helpers / how to drive a synthetic stream. If there's no easy synthetic-stream harness, test the ordering logic by extracting it. Simplest robust approach: add a small helper `emitToolCalls(events chan<- llm.ChatEvent, toolCalls map[int]*pendingTC)` and test it directly. In `openai_test.go`:

```go
func TestEmitToolCalls_OrderedByIndex(t *testing.T) {
	toolCalls := map[int]*pendingTC{
		2: {id: "c", name: "third", argsJSON: "{}"},
		0: {id: "a", name: "first", argsJSON: "{}"},
		1: {id: "b", name: "second", argsJSON: "{}"},
	}
	ch := make(chan llm.ChatEvent, 10)
	emitToolCalls(ch, toolCalls)
	close(ch)
	var ids []string
	for ev := range ch {
		if ev.Type == llm.EventToolCallDone {
			ids = append(ids, ev.ToolCall.ID)
		}
	}
	require.Equal(t, []string{"a", "b", "c"}, ids, "tool calls must emit in index order")
}
```
Add imports: `testing`, `github.com/stretchr/testify/require`, and the package's llm import path (`github.com/sausheong/harness/llm`).

- [ ] **Step 2: Run, confirm fail**

Run: `cd /Users/sausheong/projects/harness && go test ./providers/openai/ -run TestEmitToolCalls -v`
Expected: FAIL — `undefined: emitToolCalls`.

- [ ] **Step 3: Extract + fix the emit loop**

Replace the in-line emit block (currently `for _, tc := range toolCalls { if tc.name != "" { events <- ... } }` around lines 347-358) with a call `emitToolCalls(events, toolCalls)`, and add the helper at package scope:

```go
// emitToolCalls sends EventToolCallDone for each completed tool call in
// ascending index order. The map key is the streamed tool-call index;
// iterating in order keeps the assistant message's tool_use block order
// deterministic across turns so the prompt cache stays warm.
func emitToolCalls(events chan<- llm.ChatEvent, toolCalls map[int]*pendingTC) {
	maxIdx := -1
	for idx := range toolCalls {
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	for idx := 0; idx <= maxIdx; idx++ {
		tc, ok := toolCalls[idx]
		if !ok || tc.name == "" {
			continue
		}
		events <- llm.ChatEvent{
			Type: llm.EventToolCallDone,
			ToolCall: &llm.ToolCall{
				ID:    tc.id,
				Name:  tc.name,
				Input: json.RawMessage(tc.argsJSON),
			},
		}
	}
}
```

- [ ] **Step 4: Run, confirm pass**

Run: `cd /Users/sausheong/projects/harness && go test ./providers/openai/ -v`
Expected: PASS.

- [ ] **Step 5: Verify felix builds, commit**

```bash
cd /Users/sausheong/projects/felix && go build ./...
cd /Users/sausheong/projects/harness
git add providers/openai/openai.go providers/openai/openai_test.go
git commit -m "fix(openai): emit tool calls in index order for prompt-cache determinism (R7a)"
```

---

### Task 2: R7a + R7c — Qwen tool-call order + Builder accumulation

**Files:**
- Modify: `/Users/sausheong/projects/harness/providers/qwen/qwen.go` (pendingTC ~220, accum ~274, emit ~281)
- Test: `/Users/sausheong/projects/harness/providers/qwen/qwen_test.go` (create if absent)

Context: Qwen mirrors OpenAI — `toolCalls map[int]*pendingTC` with `argsJSON string` accumulated via `+=` and emitted via `range` over the map.

- [ ] **Step 1: Write the failing test**

```go
func TestEmitToolCalls_OrderedByIndex(t *testing.T) {
	toolCalls := map[int]*pendingTC{
		1: {id: "b", name: "second"},
		0: {id: "a", name: "first"},
	}
	toolCalls[0].args.WriteString("{}")
	toolCalls[1].args.WriteString("{}")
	ch := make(chan llm.ChatEvent, 10)
	emitToolCalls(ch, toolCalls)
	close(ch)
	var ids []string
	for ev := range ch {
		if ev.Type == llm.EventToolCallDone {
			ids = append(ids, ev.ToolCall.ID)
		}
	}
	require.Equal(t, []string{"a", "b"}, ids)
}
```
NOTE: this test assumes the pendingTC field becomes `args strings.Builder` (Step 3). Imports: `testing`, `strings` (transitively via Builder), `require`, llm.

- [ ] **Step 2: Run, confirm fail**

Run: `cd /Users/sausheong/projects/harness && go test ./providers/qwen/ -run TestEmitToolCalls -v`
Expected: FAIL — undefined emitToolCalls / field mismatch.

- [ ] **Step 3: Change accumulator to strings.Builder + add ordered emit**

In the `pendingTC` struct (around line 220-224), change `argsJSON string` to `args strings.Builder`. Update the accumulation site (around line 274) from `pending.argsJSON += tc.Function.Arguments` to `pending.args.WriteString(tc.Function.Arguments)`. Replace the emit loop (around 281) with `emitToolCalls(events, toolCalls)` and add the helper:

```go
func emitToolCalls(events chan<- llm.ChatEvent, toolCalls map[int]*pendingTC) {
	maxIdx := -1
	for idx := range toolCalls {
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	for idx := 0; idx <= maxIdx; idx++ {
		tc, ok := toolCalls[idx]
		if !ok || tc.name == "" {
			continue
		}
		events <- llm.ChatEvent{
			Type: llm.EventToolCallDone,
			ToolCall: &llm.ToolCall{
				ID:    tc.id,
				Name:  tc.name,
				Input: json.RawMessage(tc.args.String()),
			},
		}
	}
}
```
Ensure `strings` is imported. If `pendingTC` is initialized as `&pendingTC{}` (it is), the zero-value Builder is ready to use.

- [ ] **Step 4: Run, confirm pass**

Run: `cd /Users/sausheong/projects/harness && go test ./providers/qwen/ -v`
Expected: PASS.

- [ ] **Step 5: Verify felix builds, commit**

```bash
cd /Users/sausheong/projects/felix && go build ./...
cd /Users/sausheong/projects/harness
git add providers/qwen/qwen.go providers/qwen/qwen_test.go
git commit -m "fix(qwen): index-order tool emit + strings.Builder accumulation (R7a/R7c)"
```

---

### Task 3: R7c — OpenAI strings.Builder accumulation

**Files:**
- Modify: `/Users/sausheong/projects/harness/providers/openai/openai.go` (pendingTC ~274, accum ~340, emit helper from Task 1)

Context: Task 1 left `argsJSON string` accumulated via `+=` at line 340. Switch to `strings.Builder` for O(n).

- [ ] **Step 1: Update the test from Task 1**

Change the `TestEmitToolCalls_OrderedByIndex` test in `openai_test.go` to build args via a Builder field (matching the struct change): replace `argsJSON: "{}"` literals with constructing the struct then `.args.WriteString("{}")`, OR keep argsJSON if you prefer — but the struct field is changing, so update the test to set `args` Builder. Use:
```go
	mk := func(id, name string) *pendingTC {
		p := &pendingTC{id: id, name: name}
		p.args.WriteString("{}")
		return p
	}
	toolCalls := map[int]*pendingTC{2: mk("c","third"), 0: mk("a","first"), 1: mk("b","second")}
```

- [ ] **Step 2: Run, confirm fail (field mismatch)**

Run: `cd /Users/sausheong/projects/harness && go test ./providers/openai/ -run TestEmitToolCalls -v`
Expected: FAIL — `pendingTC has no field args` (until Step 3).

- [ ] **Step 3: Change struct + accum + helper**

In `pendingTC` (line ~274), change `argsJSON string` → `args strings.Builder`. At the accumulation site (line ~340), change `pending.argsJSON += tc.Function.Arguments` → `pending.args.WriteString(tc.Function.Arguments)`. In `emitToolCalls` (added in Task 1), change `Input: json.RawMessage(tc.argsJSON)` → `Input: json.RawMessage(tc.args.String())`. Ensure `strings` imported.

- [ ] **Step 4: Run, confirm pass**

Run: `cd /Users/sausheong/projects/harness && go test ./providers/openai/ -v`
Expected: PASS.

- [ ] **Step 5: Verify felix builds, commit**

```bash
cd /Users/sausheong/projects/felix && go build ./...
cd /Users/sausheong/projects/harness
git add providers/openai/openai.go providers/openai/openai_test.go
git commit -m "fix(openai): strings.Builder for tool-arg accumulation (R7c)"
```

---

### Task 4: R7b — Gemini single EventDone

**Files:**
- Modify: `/Users/sausheong/projects/harness/providers/gemini/gemini.go` (per-chunk EventDone ~191-199; terminal ~243)
- Test: `/Users/sausheong/projects/harness/providers/gemini/gemini_test.go` (create if absent)

Context: Gemini emits `EventDone` with usage on EVERY chunk carrying `UsageMetadata` (line ~191), plus a terminal `EventDone` at ~243. This causes one calibrator update per chunk and a skewed ratio. Fix: buffer usage, emit one EventDone at stream end.

- [ ] **Step 1: Write the failing test**

The stream loop is hard to unit-test without the genai client. Extract the usage-buffering decision into a testable shape OR test via a thin helper. Pragmatic approach: add a helper that converts `*genai.GenerateContentResponseUsageMetadata` to `*llm.Usage` and assert the loop keeps only the last. Given the genai types are awkward to construct, instead write a behavioral test on a small extracted function `lastUsageFrom(prompt, candidates int) *llm.Usage` is overkill — prefer a documented manual-reasoning fix with a compile-time guard. **Minimal real test:** assert that `llm.Usage` buffering helper returns the newest:

```go
func TestGeminiUsageBuffer_KeepsLast(t *testing.T) {
	var last *llm.Usage
	last = updateUsage(last, 10, 5)
	last = updateUsage(last, 20, 9) // cumulative final
	require.NotNil(t, last)
	require.Equal(t, 20, last.InputTokens)
	require.Equal(t, 9, last.OutputTokens)
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `cd /Users/sausheong/projects/harness && go test ./providers/gemini/ -run TestGeminiUsageBuffer -v`
Expected: FAIL — `undefined: updateUsage`.

- [ ] **Step 3: Add helper + rewire the loop**

Add the helper:
```go
// updateUsage returns a *llm.Usage carrying the latest cumulative counts.
// Gemini sends cumulative usage on multiple chunks; we keep only the last
// and emit a single EventDone at stream end (mirrors the OpenAI provider).
func updateUsage(_ *llm.Usage, prompt, candidates int) *llm.Usage {
	return &llm.Usage{InputTokens: prompt, OutputTokens: candidates}
}
```
In the stream loop, replace the per-chunk emit block (lines ~191-199):
```go
			if resp.UsageMetadata != nil {
				events <- llm.ChatEvent{
					Type: llm.EventDone,
					Usage: &llm.Usage{ ... },
				}
			}
```
with:
```go
			if resp.UsageMetadata != nil {
				lastUsage = updateUsage(lastUsage,
					int(resp.UsageMetadata.PromptTokenCount),
					int(resp.UsageMetadata.CandidatesTokenCount))
			}
```
Declare `var lastUsage *llm.Usage` just inside the goroutine before the `for` loop. Change the terminal emit (line ~243) from `events <- llm.ChatEvent{Type: llm.EventDone}` to:
```go
		events <- llm.ChatEvent{Type: llm.EventDone, Usage: lastUsage}
```

- [ ] **Step 4: Run, confirm pass**

Run: `cd /Users/sausheong/projects/harness && go test ./providers/gemini/ -v`
Expected: PASS.

- [ ] **Step 5: Verify felix builds, commit**

```bash
cd /Users/sausheong/projects/felix && go build ./...
cd /Users/sausheong/projects/harness
git add providers/gemini/gemini.go providers/gemini/gemini_test.go
git commit -m "fix(gemini): emit a single EventDone with final usage (R7b)"
```

---

### Task 5: R7c — Anthropic error surfacing + Builder accumulation

**Files:**
- Modify: `/Users/sausheong/projects/harness/providers/anthropic/anthropic.go` (pendingTC ~164-170, accum ~223, error case ~283)
- Test: `/Users/sausheong/projects/harness/providers/anthropic/anthropic_test.go` (create if absent)

Context: (1) the `case "error":` at line 283 only logs — it must also emit an EventError so a silent empty success can't happen. NOTE: the SDK's `MessageStreamEventUnion` has NO structured error payload (real errors surface via `stream.Err()`, already handled at ~288). So the fix emits a generic EventError from the in-band error branch; detail still comes from stream.Err(). (2) `pendingTools[...].inputJSON += delta.PartialJSON` at line 223 is O(n²) — switch to strings.Builder.

- [ ] **Step 1: Write the failing test (Builder accumulation behavior)**

The error-emit and the Builder are both in the stream goroutine. Test the Builder via a small extracted accumulation, and assert the error branch emits. Simplest: test that the inputJSON accumulation via Builder yields the concatenation. Extract nothing if not needed — instead add a focused test for an accumulator helper if one is introduced. Given the pending struct is local, write a test on a tiny helper `accumulate`:

```go
func TestAnthropicInputAccumulation(t *testing.T) {
	var b strings.Builder
	for _, part := range []string{`{"a"`, `:1`, `}`} {
		b.WriteString(part)
	}
	require.Equal(t, `{"a":1}`, b.String())
}
```
(This is a guard test documenting the Builder semantics; the real change is in-loop.)

- [ ] **Step 2: Run it (passes trivially — it's a semantics guard)**

Run: `cd /Users/sausheong/projects/harness && go test ./providers/anthropic/ -run TestAnthropicInputAccumulation -v`
Expected: PASS (this asserts the Builder concatenation contract the fix relies on).

- [ ] **Step 3: Change struct field to Builder**

In `pendingTC` (lines ~164-170), change `inputJSON string` → `inputJSON strings.Builder`. Update line ~223 from `pendingTools[len(pendingTools)-1].inputJSON += delta.PartialJSON` to `pendingTools[len(pendingTools)-1].inputJSON.WriteString(delta.PartialJSON)`. At the emit/`content_block_stop` site (lines ~228-249), change `inp := tc.inputJSON` to `inp := tc.inputJSON.String()`. Ensure `strings` imported.

- [ ] **Step 4: Surface the in-band error**

Replace the error case (line ~283-284):
```go
			case "error":
				slog.Error("anthropic stream error", "event", event.Type)
```
with:
```go
			case "error":
				// The SDK surfaces error detail via stream.Err() (handled
				// below); this in-band branch must not be a silent no-op —
				// emit an EventError so the runtime treats it as a failure,
				// not an empty success.
				slog.Error("anthropic stream error event", "event", event.Type)
				events <- llm.ChatEvent{
					Type:  llm.EventError,
					Error: fmt.Errorf("anthropic stream error event"),
				}
```
Confirm `fmt` is imported (it is). NOTE: `stream.Err()` after the loop will usually carry the real detail and emit its own EventError — duplicate EventErrors are acceptable (the runtime acts on the first); the point is the in-band branch is no longer silent.

- [ ] **Step 5: Run, confirm pass + build**

Run: `cd /Users/sausheong/projects/harness && go test ./providers/anthropic/ -v && cd /Users/sausheong/projects/felix && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/harness
git add providers/anthropic/anthropic.go providers/anthropic/anthropic_test.go
git commit -m "fix(anthropic): surface in-band stream error + Builder accumulation (R7c)"
```

---

### Task 6: N11 — HTML entity decoding via html.UnescapeString

**Files:**
- Modify: `/Users/sausheong/projects/harness/tools/web/websearch.go` (`cleanHTMLText` ~256-276)
- Test: `/Users/sausheong/projects/harness/tools/web/websearch_test.go` (append; exists from round 4)

- [ ] **Step 1: Write the failing test**

```go
func TestCleanHTMLText_DecodesNumericEntities(t *testing.T) {
	out := cleanHTMLText("<b>caf&#233;</b> &amp; t&#8217;is &nbsp;done")
	require.Contains(t, out, "café")
	require.Contains(t, out, "&", "amp must decode")
	require.Contains(t, out, "t’is", "numeric entity must decode")
	require.NotContains(t, out, "&#", "no raw numeric entities remain")
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/web/ -run TestCleanHTMLText -v`
Expected: FAIL — numeric entities pass through raw.

- [ ] **Step 3: Replace manual entity decode**

In `cleanHTMLText`, keep the tag-stripping loop (the `for _, r := range s` that drops `<...>`). Replace the six `strings.ReplaceAll(result, "&xxx;", ...)` lines (274-279) with a single `html.UnescapeString`:
```go
	result := out.String()
	result = html.UnescapeString(result)
	return strings.TrimSpace(result)
```
Add `"html"` to the imports. Remove the now-unused manual replacements.

- [ ] **Step 4: Run, confirm pass + build**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/web/ -v && cd /Users/sausheong/projects/felix && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 5: Commit**

```bash
cd /Users/sausheong/projects/harness
git add tools/web/websearch.go tools/web/websearch_test.go
git commit -m "fix(web): decode all HTML entities via html.UnescapeString (N11)"
```

---

### Task 7: N12 — read_file/edit_file honor ctx cancellation

**Files:**
- Modify: `/Users/sausheong/projects/harness/tools/file/readfile.go` (Execute ~75)
- Modify: `/Users/sausheong/projects/harness/tools/file/editfile.go` (Execute ~55)
- Test: `/Users/sausheong/projects/harness/tools/file/readfile_test.go` (append; exists from round 4)

- [ ] **Step 1: Write the failing test**

```go
func TestReadFile_HonorsCancelledContext(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(p, []byte("hello"), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled
	tool := &ReadFileTool{}
	in, _ := json.Marshal(map[string]string{"path": p})
	res, err := tool.Execute(ctx, in)
	require.NoError(t, err)
	require.NotEmpty(t, res.Error, "cancelled ctx must short-circuit")
}

func TestEditFile_HonorsCancelledContext(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(p, []byte("hello"), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := &EditFileTool{}
	in, _ := json.Marshal(map[string]string{"path": p, "old_string": "h", "new_string": "H"})
	res, err := tool.Execute(ctx, in)
	require.NoError(t, err)
	require.NotEmpty(t, res.Error, "cancelled ctx must short-circuit")
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/file/ -run 'HonorsCancelled' -v`
Expected: FAIL — ctx ignored, reads succeed (res.Error empty).

- [ ] **Step 3: Honor ctx in both**

In `readfile.go`, change the signature `func (t *ReadFileTool) Execute(_ context.Context, input json.RawMessage)` → `Execute(ctx context.Context, input json.RawMessage)`. After the input-unmarshal/path-empty validation and BEFORE the size-check/open (the N5 block from round 4), insert:
```go
	if err := ctx.Err(); err != nil {
		return tool.ToolResult{Error: err.Error()}, nil
	}
```
Do the same in `editfile.go`: rename `_` → `ctx`, add the same check after the path validation, before the size check/read.

- [ ] **Step 4: Run, confirm pass + build**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/file/ -v && cd /Users/sausheong/projects/felix && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 5: Commit**

```bash
cd /Users/sausheong/projects/harness
git add tools/file/readfile.go tools/file/editfile.go tools/file/readfile_test.go
git commit -m "fix(file): honor ctx cancellation in read_file/edit_file (N12)"
```

---

### Task 8: R8 + L6 — memory index recency + UTF-8-safe truncation

**Files:**
- Modify: `/Users/sausheong/projects/felix/internal/memory/memory.go` (`FormatIndex` ~415-450, cap ~403; truncation ~389 + indexDescription)
- Test: `/Users/sausheong/projects/felix/internal/memory/memory_test.go` (append)

- [ ] **Step 1: Write the failing tests**

```go
func TestFormatIndex_ShowsNewestWhenOverCap(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	// Save cap+5 entries; newer entries have later ModTime.
	total := MaxMemoryIndexEntries + 5
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("agent-%010d-x", i) // ascending ids = oldest-first by id
		require.NoError(t, m.Save(id, "# T"+id+"\n\nbody"))
		m.mu.Lock()
		e := m.entries[id]
		e.ModTime = time.Unix(int64(1000+i), 0) // newer i = later mtime
		m.entries[id] = e
		m.mu.Unlock()
	}
	idx := m.FormatIndex()
	// The newest entry must appear; the oldest must NOT.
	require.Contains(t, idx, fmt.Sprintf("agent-%010d-x", total-1), "newest must be listed")
	require.NotContains(t, idx, fmt.Sprintf("agent-%010d-x", 0), "oldest must fall off")
	require.Contains(t, idx, "and ", "truncation notice must appear")
}

func TestTruncateRunes_UTF8Safe(t *testing.T) {
	s := strings.Repeat("é", 100) // 2 bytes each
	out := truncateRunes(s, 10)
	require.True(t, utf8.ValidString(out), "must not split a rune")
	require.Equal(t, 10, utf8.RuneCountInString(out))
}
```
Imports: `fmt`, `strings`, `time`, `unicode/utf8`, `testing`, `require`.

- [ ] **Step 2: Run, confirm fail**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/memory/ -run 'TestFormatIndex_ShowsNewest|TestTruncateRunes' -v`
Expected: FAIL — sort is by ID asc (oldest shown), no notice; `undefined: truncateRunes`.

- [ ] **Step 3: Add the rune-truncation helper**

Add to memory.go:
```go
// truncateRunes returns s limited to n runes, never splitting a multibyte
// rune (byte-slicing UTF-8 can inject invalid bytes into the system prompt).
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
```
Add `"unicode/utf8"` to imports.

- [ ] **Step 4: Fix FormatIndex sort + cap notice**

In `FormatIndex`, change the sort (line ~427) from ID-ascending to ModTime-descending with an ID-descending tie-break:
```go
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].ModTime.Equal(entries[j].ModTime) {
			return entries[i].ModTime.After(entries[j].ModTime)
		}
		return entries[i].ID > entries[j].ID
	})
	omitted := 0
	if len(entries) > MaxMemoryIndexEntries {
		omitted = len(entries) - MaxMemoryIndexEntries
		entries = entries[:MaxMemoryIndexEntries]
		slog.Warn("memory index truncated", "shown", MaxMemoryIndexEntries, "omitted", omitted)
	}
```
After the entry-writing loop, before `return b.String()`, add the notice:
```go
	if omitted > 0 {
		fmt.Fprintf(&b, "\n…and %d more (use the memory list tool to see all).\n", omitted)
	}
```
Confirm `slog` and `fmt` are imported (check the file head; add if missing).

- [ ] **Step 5: Apply L6 truncation at the byte-slice sites**

Find `content[:2000]` (line ~389) and the `indexDescription` ~120-char trim. Replace byte-slicing with `truncateRunes`. For line ~389:
```go
		if utf8.RuneCountInString(content) > 2000 {
			content = truncateRunes(content, 2000) + "\n\n[truncated]"
		}
```
In `indexDescription`, wherever it trims to ~120 chars by byte slice, use `truncateRunes(desc, 120)`.

- [ ] **Step 6: Run, confirm pass**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/memory/ -v`
Expected: PASS (new + existing).

- [ ] **Step 7: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/memory/memory.go internal/memory/memory_test.go
git commit -m "fix(memory): index shows newest entries + UTF-8-safe truncation (R8/L6)"
```

---

### Task 9: R10 — atomic memory write + race-free bounded vector add

**Files:**
- Create: `/Users/sausheong/projects/felix/internal/memory/writefile.go`
- Modify: `/Users/sausheong/projects/felix/internal/memory/memory.go` (`Manager` struct ~28, `NewManager` ~41, `Save` ~236-278)
- Test: `/Users/sausheong/projects/felix/internal/memory/memory_test.go` (append)

- [ ] **Step 1: Write the failing race test**

```go
func TestSave_ConcurrentWithLoadNoRace(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = m.Save(fmt.Sprintf("agent-%d", i), "# t\n\nbody")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = m.Load()
		}
	}()
	wg.Wait()
}

func TestWriteFileAtomic_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.md")
	require.NoError(t, writeFileAtomic(p, []byte("hello"), 0o600))
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "hello", string(b))
}
```
Imports: `sync`, `fmt`, `os`, `path/filepath`, `testing`, `require`.

- [ ] **Step 2: Run, confirm fail**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/memory/ -race -run 'TestSave_Concurrent|TestWriteFileAtomic' -v`
Expected: FAIL — `undefined: writeFileAtomic`, and/or a data race on `m.vecColl` (if an embedder is set; without one the goroutine is skipped — the race test still guards the structure).

- [ ] **Step 3: Add the atomic-write helper**

Create `internal/memory/writefile.go`:
```go
package memory

import (
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to a temp file in the same directory then
// renames it over path, so a crash mid-write never leaves a torn file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
```

- [ ] **Step 4: Add vecSem to Manager + init**

In the `Manager` struct (line ~28), add a field:
```go
	vecSem chan struct{} // bounds concurrent vector-add goroutines
```
In `NewManager` (line ~41), initialize it:
```go
		vecSem: make(chan struct{}, 4),
```

- [ ] **Step 5: Fix Save — atomic write + captured coll + bounded goroutine**

In `Save`, change the write (line ~243) from `os.WriteFile(path, []byte(content), 0o600)` to `writeFileAtomic(path, []byte(content), 0o600)`. Replace the vector-add block (lines ~267-275):
```go
	if m.vecColl != nil {
		go func() {
			ctx := context.Background()
			doc := chromem.Document{ID: id, Content: content}
			if err := m.vecColl.AddDocument(ctx, doc); err != nil {
				slog.Warn("vector index add failed", "id", id, "error", err)
			}
		}()
	}
```
with (capture coll UNDER the lock — Save still holds m.mu here via the earlier `defer`):
```go
	if coll := m.vecColl; coll != nil {
		m.vecSem <- struct{}{}
		go func() {
			defer func() { <-m.vecSem }()
			doc := chromem.Document{ID: id, Content: content}
			if err := coll.AddDocument(context.Background(), doc); err != nil {
				slog.Warn("vector index add failed", "id", id, "error", err)
			}
		}()
	}
```
The goroutine now uses the captured `coll`, never `m.vecColl`.

> Note: `m.vecSem <- struct{}{}` runs while `m.mu` is held (Save's defer-unlock fires on return). With cap 4 this can briefly block under the lock if 4 adds are in flight. Acceptable — Save is not on the chat latency path. If the reviewer flags it, the acquire can move just outside, but keep capture-under-lock.

- [ ] **Step 6: Run, confirm pass under -race**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/memory/ -race -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/memory/writefile.go internal/memory/memory.go internal/memory/memory_test.go
git commit -m "fix(memory): atomic writes + race-free bounded vector add (R10)"
```

---

### Task 10: N9 + N10 — drop slice-aliasing in overlay and run registry

**Files:**
- Modify: `/Users/sausheong/projects/felix/internal/chatexec/overlay.go` (`ToolDefs` ~71,95; `Names` ~118)
- Modify: `/Users/sausheong/projects/felix/internal/gateway/runs/registry.go` (`DeleteRun` ~493)
- Test: `/Users/sausheong/projects/felix/internal/chatexec/overlay_test.go` (append; exists)

- [ ] **Step 1: Write the failing/guard test**

```go
func TestOverlay_ToolDefsDoesNotMutateBase(t *testing.T) {
	base := &fakeExecutor{defs: []llm.ToolDef{{Name: "cron"}, {Name: "read_file"}}}
	ov := &ChatToolOverlay{Base: base, Cron: &fakeCronTool{}}
	_ = ov.ToolDefs()
	// Base's slice must be untouched (still contains "cron" in original order).
	require.Equal(t, "cron", base.defs[0].Name, "overlay must not mutate Base's slice")
}
```
NOTE: read overlay_test.go first — it likely already has a `fakeExecutor`/test scaffolding for ChatToolOverlay. Reuse the existing fakes; adapt names to what's there. If a `fakeExecutor` with a fixed `defs` slice doesn't exist, build the minimal one. The key assertion: after `ToolDefs()`, `base.defs` is unchanged.

- [ ] **Step 2: Run, confirm fail**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/chatexec/ -run TestOverlay_ToolDefsDoesNotMutate -v`
Expected: FAIL — `defs[:0]` aliases and overwrites Base's backing array, so `base.defs[0]` is corrupted.

- [ ] **Step 3: Replace [:0] aliasing in overlay.go**

In `ToolDefs`, both `filtered := defs[:0]` sites (lines ~71 and ~95) → `filtered := make([]llm.ToolDef, 0, len(defs))`. In `Names`, the `filtered := names[:0]` site (~118) → `filtered := make([]string, 0, len(names))`.

- [ ] **Step 4: Replace [:0] aliasing in registry.go**

In `DeleteRun` (line ~493), change `out := idx.Runs[:0]` → `out := make([]RunMeta, 0, len(idx.Runs))`. (Confirm the element type by reading the struct — it's whatever `idx.Runs` holds; match it exactly. If unsure, use `out := make([]<elemType>, 0, len(idx.Runs))` with the real type from the file.)

- [ ] **Step 5: Run, confirm pass + build**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/chatexec/ ./internal/gateway/runs/ -v && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/chatexec/overlay.go internal/gateway/runs/registry.go internal/chatexec/overlay_test.go
git commit -m "fix: allocate instead of aliasing caller slices in overlay/registry (N9/N10)"
```

---

### Task 11: R9 — cortex KnowledgeGraph adapter + wiring

**Files:**
- Create: `/Users/sausheong/projects/felix/internal/cortex/kg.go`
- Create: `/Users/sausheong/projects/felix/internal/cortex/kg_test.go`
- Modify: `/Users/sausheong/projects/felix/internal/agent/agent.go` (hdeps ~178; stub ~64; subagent hdeps ~250 leave KGFn nil)

Context: harness `KnowledgeGraph` interface is `ShouldRecall(query string) bool`, `Recall(ctx, query) string`, `Ingest(ctx, thread []Message)`. `RuntimeDeps.KGFn func(model string) KnowledgeGraph` (nil-safe). Felix has `CortexFn func(model string) *cortex.Cortex` set but unread. `cortex.Cortex` has `Recall(ctx, query, ...opts) ([]Result, error)` and `Remember(ctx, content, ...opts) error`. Do NOT add cortex.WithLLM (800ms budget). Set KGFn ONLY at the main hdeps (agent.go:178); leave nil for subagents (agent.go:250).

- [ ] **Step 1: Write the failing test**

```go
package cortex

import (
	"context"
	"testing"

	hrt "github.com/sausheong/harness/runtime"
	"github.com/stretchr/testify/require"
)

func TestNewKnowledgeGraph_NilCortex(t *testing.T) {
	require.Nil(t, NewKnowledgeGraph(nil), "nil cortex must yield nil KG (disables recall)")
}

func TestCortexKG_ShouldRecall(t *testing.T) {
	kg := &cortexKG{} // cx nil ok for ShouldRecall (pure heuristic)
	require.False(t, kg.ShouldRecall(""))
	require.False(t, kg.ShouldRecall("hi"))
	require.True(t, kg.ShouldRecall("what did we decide about the database schema"))
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/cortex/ -run 'TestNewKnowledgeGraph|TestCortexKG' -v`
Expected: FAIL — `undefined: NewKnowledgeGraph / cortexKG`.

- [ ] **Step 3: Create kg.go**

```go
package cortex

import (
	"context"
	"fmt"
	"strings"

	"github.com/sausheong/cortex"
	hrt "github.com/sausheong/harness/runtime"
)

// cortexKG adapts *cortex.Cortex to the harness KnowledgeGraph interface,
// enabling the runtime's bounded (800ms) auto-recall + deferred-async ingest.
// Recall relies on cortex's keyword+memory fallback (no WithLLM/decomposeQuery)
// so it stays within the recall budget — see build() for why WithLLM is omitted.
type cortexKG struct {
	cx *cortex.Cortex
}

// NewKnowledgeGraph wraps cx; returns nil when cx is nil so the result can be
// passed straight to RuntimeDeps.KGFn (nil disables the whole KG pathway).
func NewKnowledgeGraph(cx *cortex.Cortex) hrt.KnowledgeGraph {
	if cx == nil {
		return nil
	}
	return &cortexKG{cx: cx}
}

const minRecallQueryLen = 8

func (k *cortexKG) ShouldRecall(query string) bool {
	return len(strings.TrimSpace(query)) >= minRecallQueryLen
}

func (k *cortexKG) Recall(ctx context.Context, query string) string {
	results, err := k.cx.Recall(ctx, query, cortex.WithLimit(5))
	if err != nil || len(results) == 0 {
		return ""
	}
	return formatRecall(results)
}

func (k *cortexKG) Ingest(ctx context.Context, thread []hrt.Message) {
	if len(thread) == 0 {
		return
	}
	var b strings.Builder
	for _, m := range thread {
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}
	_ = k.cx.Remember(ctx, strings.TrimSpace(b.String()))
}

// formatRecall renders recall results into a compact prompt hint. Field
// access mirrors internal/tools/cortextools/format.go (cortex.Result has
// Type, Content, Score, Source). Content is trimmed to keep the hint small.
func formatRecall(results []cortex.Result) string {
	var b strings.Builder
	b.WriteString("Relevant context from memory:\n")
	for _, r := range results {
		content := r.Content
		if len(content) > 300 {
			content = content[:300] + "..."
		}
		fmt.Fprintf(&b, "- [%s] %s\n", r.Type, content)
	}
	return b.String()
}
```
The `cortex.Result` fields are confirmed: `Type string`, `Content string`, `Score float64`,
`Source string` (cortex `types.go:49`). This mirrors `cortextools/format.go`'s `formatRecallResults`
field access (no `recallLine` helper needed — the rendering is inline as shown).

- [ ] **Step 4: Run, confirm pass**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/cortex/ -v`
Expected: PASS.

- [ ] **Step 5: Wire KGFn at agent.go:178**

Read `internal/agent/agent.go` around line 178 (the `hdeps := hrt.RuntimeDeps{...}` for the main runtime). The deps struct passed to `BuildRuntimeForAgent` carries `CortexFn func(model string) *cortex.Cortex` (field at ~92). Add a `KGFn` to the hdeps literal:
```go
		KGFn: func(model string) hrt.KnowledgeGraph {
			if deps.CortexFn == nil {
				return nil
			}
			return cortexpkg.NewKnowledgeGraph(deps.CortexFn(model))
		},
```
Use the correct import alias for `internal/cortex` (check how it's imported elsewhere in agent.go — likely `cortexadapter` or similar; match it). If `internal/cortex` is not yet imported in agent.go, add it. `deps` is the `RuntimeDeps` (Felix's) in scope in `BuildRuntimeForAgent`.

- [ ] **Step 6: Leave subagent KGFn nil + delete the stub**

At the second hdeps (line ~250, inside `MakeSubagentFactory`): do NOT add KGFn (subagents must not recall/ingest — matches harness review.go precedent). Add a one-line comment: `// KGFn intentionally unset — subagents do not auto-recall or ingest.`

Delete the `BuildDynamicSystemPromptSuffix` stub (lines ~64-71) and its entry in the type/var block. First confirm zero callers: `grep -rn BuildDynamicSystemPromptSuffix internal/ cmd/` → if any exist, STOP and report; otherwise delete.

- [ ] **Step 7: Add the wiring test**

In `internal/agent/agent_test.go` (or a new `kgwiring_test.go`), add a test that the KGFn closure returns nil when CortexFn is nil and non-nil when it returns a cortex. Since constructing a full RuntimeDeps may be heavy, test the closure logic directly if extractable, OR assert via a minimal BuildRuntimeForAgent path. Pragmatic minimal test:
```go
func TestKGFn_NilWhenCortexDisabled(t *testing.T) {
	var cortexFn func(string) *cortex.Cortex // nil
	kgfn := func(model string) hrt.KnowledgeGraph {
		if cortexFn == nil {
			return nil
		}
		return cortexpkg.NewKnowledgeGraph(cortexFn(model))
	}
	require.Nil(t, kgfn("any"))
}
```
(This documents the load-bearing "nil when disabled" guarantee. If you can test the real closure from BuildRuntimeForAgent without heavy setup, prefer that.)

- [ ] **Step 8: Run tests + build both**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/cortex/ ./internal/agent/ -v && go build ./... && cd /Users/sausheong/projects/harness && go build ./...`
Expected: PASS, clean builds.

- [ ] **Step 9: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/cortex/kg.go internal/cortex/kg_test.go internal/agent/agent.go internal/agent/*_test.go
git commit -m "feat(cortex): wire auto-recall via harness KnowledgeGraph KGFn (R9)"
```

---

### Task 12: Final verification

**Files:** none (verification only)

- [ ] **Step 1: Full harness suite under race**

Run: `cd /Users/sausheong/projects/harness && go test -race ./...`
Expected: PASS.

- [ ] **Step 2: Full felix suite under race**

Run: `cd /Users/sausheong/projects/felix && go test -race ./...`
Expected: PASS.

- [ ] **Step 3: Both build + binaries**

Run: `cd /Users/sausheong/projects/harness && go build ./... && cd /Users/sausheong/projects/felix && go build ./... && go build -o /tmp/felix ./cmd/felix`
Expected: no output.

- [ ] **Step 4: go vet both**

Run: `cd /Users/sausheong/projects/harness && go vet ./... && cd /Users/sausheong/projects/felix && go vet ./...`
Expected: no output.

---

## Notes for the executor

- **harness ↔ felix:** after every harness commit, `cd felix && go build ./...` before moving on.
- **No `Co-Authored-By` trailer** in any commit message.
- **`tool.ToolResult`** has `Output string`, `Error string`, `Images []llm.ImageContent`.
- **`llm.ChatEvent`** has `Type`, `Text`, `ToolCall *llm.ToolCall`, `Usage *llm.Usage`, `Error error` (field is `Error`, not `Err` — verify in `harness/llm`).
- **Cortex `Result` field names:** do not guess — mirror `internal/tools/cortextools/recall.go`'s `formatRecallResults`.
- **Task order:** 1→2→3 (OpenAI/Qwen share helper shape), 4, 5 (providers, independent), 6, 7 (web/file), 8→9 (memory, share file), 10 (cleanups), 11 (R9, the big one), 12 (verify). Each is independently committable.
