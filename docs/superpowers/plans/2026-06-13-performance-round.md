# Performance Round Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cut latency and disk I/O on the agent hot path (every chat turn) and parallelize startup, with zero observable behavior change.

**Architecture:** Three groups. Group 1 (harness micro-opts) removes dead/quadratic assembly work, hoists tool-def normalization out of the turn loop, and debounces the calibrator write. Group 2 (harness I/O) stops re-spilling oversized results and moves the session disk write off the session lock. Group 3 (Felix) caches per-turn Runtime prompt inputs and parallelizes startup.

**Tech Stack:** Go 1.25, `stretchr/testify`, `sync/atomic`, `golang.org/x/sync` (already an indirect dep). Two repos: `felix` (`/Users/sausheong/projects/felix`) and `harness` (`/Users/sausheong/projects/harness`, wired via `go.mod replace`).

**Spec:** `docs/superpowers/specs/2026-06-13-performance-round-design.md`

**HARD CONSTRAINT — zero observable behavior change.** These are optimizations: identical LLM requests, session contents, and tool outputs. If an existing test's output changes, that's a bug in the optimization, not a test to update. Validate every task under `go test` and the touched packages under `go test -race`.

**Conventions:**
- Tests use `testify` (`require`/`assert`).
- Commit messages omit any Co-Authored-By trailer.
- After any harness change, run `cd /Users/sausheong/projects/felix && go build ./...` to confirm the replace-wired build compiles.

---

## File Structure

**Group 1 — harness micro-opts**
- Modify: `runtime/context.go` — P2 (delete `resultIDs` + mid-loop inject)
- Modify: `runtime/runtime.go` — P3 (hoist tool-def normalize), P8 (debounce calibrator)
- Test: `runtime/context_test.go` or `runtime/agent_test.go` (P2 assembly), `runtime/*_test.go` (P8)

**Group 2 — harness I/O**
- Modify: `runtime/runtime.go` — P1 (`spilledIDs` field + thread into prune)
- Modify: `runtime/context.go` — P1 (`pruneToolResults` skips write when spilled)
- Modify: `session/session.go` — P5 (release `s.mu` before store write)
- Test: `runtime/agent_test.go` (P1), `session/*_test.go` (P5)

**Group 3 — Felix**
- Modify: `internal/agent/agent.go` — P4 (generation cache, dedupe summary, factory param)
- Modify: `internal/chatexec/chatexec.go`, `internal/startup/startup.go` — P4 factory call sites + `BumpConfigGeneration`
- Modify: `internal/startup/startup.go` — P6 (parallel startup)
- Modify: `internal/local/supervisor.go` — P6 (split Spawn/WaitReady)
- Test: `internal/agent/*_test.go` (P4), `internal/startup/*_test.go` (P6)

---

## Group 1 — Hot-path micro-opts

### Task 1: P2 — delete dead `resultIDs` map + mid-loop inject

**Files:**
- Modify: `/Users/sausheong/projects/harness/runtime/context.go` (`assembleMessages`)
- Test: `/Users/sausheong/projects/harness/runtime/context_test.go` (new assertion)

- [ ] **Step 1: Read the function**

Run: `cd /Users/sausheong/projects/harness && sed -n '170,322p' runtime/context.go`
Confirm: (a) the `resultIDs` first-pass loop at `:172-178` builds a `map[string]bool` by unmarshalling every `EntryTypeToolResult` and is never read afterward; (b) the `case session.EntryTypeMessage:` branch calls `msgs = injectMissingToolResults(msgs)` at `:224` (inside the loop); (c) the terminal `msgs = injectMissingToolResults(msgs)` at `:319` (after the loop). Grep to be 100% sure `resultIDs` is unused: `grep -n "resultIDs" runtime/context.go` — it must appear ONLY in the first-pass loop.

- [ ] **Step 2: Write a characterization test (guards behavior preservation)**

Add to `/Users/sausheong/projects/harness/runtime/context_test.go` (create the file if it doesn't exist — `package runtime`, import `session`, `llm`, testify, `encoding/json`). This test asserts that an assistant message with an orphaned tool call (no matching tool result) still gets a synthetic result injected, exercising the terminal pass that must remain:

```go
func TestAssembleMessagesInjectsOrphanedToolResult(t *testing.T) {
	history := []session.SessionEntry{
		{Type: session.EntryTypeMessage, Role: "user", Data: mustJSON(t, session.MessageData{Text: "hi"})},
		{Type: session.EntryTypeToolCall, Role: "assistant", Data: mustJSON(t, session.ToolCallData{
			ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "bash", Arguments: json.RawMessage(`{}`)}},
		})},
		// No matching tool result for call_1 -> orphan.
		{Type: session.EntryTypeMessage, Role: "user", Data: mustJSON(t, session.MessageData{Text: "still there?"})},
	}
	msgs := assembleMessages(history)
	// There must be a synthetic tool result for call_1 somewhere in the output.
	var found bool
	for _, m := range msgs {
		if m.ToolCallID == "call_1" {
			found = true
		}
	}
	require.True(t, found, "orphaned tool call must get a synthetic tool result from the terminal pass")
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
```

> Verify the exact field names of `session.ToolCallData` / `session.MessageData` / `session.ToolResultData` and the `EntryType*` constants by reading `session/` first; adjust the literals to match. If a `mustJSON` helper already exists in the package, reuse it and drop the duplicate. The load-bearing assertion is that `call_1` gets a result after the dead-code removal.

- [ ] **Step 3: Run the test (should PASS now — it characterizes existing behavior)**

Run: `cd /Users/sausheong/projects/harness && go test ./runtime/ -run TestAssembleMessagesInjectsOrphanedToolResult -v`
Expected: PASS (behavior exists today via both the mid-loop and terminal passes).

- [ ] **Step 4: Delete the dead code**

In `runtime/context.go`:
1. Delete the entire `resultIDs` first-pass loop (the `resultIDs := make(map[string]bool)` declaration and the `for _, entry := range history { ... }` block that populates it — `:172-178`). Keep the comment-free rest of the function.
2. Delete the single mid-loop call `msgs = injectMissingToolResults(msgs)` at `:224` (inside `case session.EntryTypeMessage:`). Leave the surrounding message-append logic intact.
3. Keep the terminal `msgs = injectMissingToolResults(msgs)` at `:319` and the `injectMissingToolResults` definition unchanged.

After editing, `grep -n "resultIDs" runtime/context.go` must return nothing.

- [ ] **Step 5: Run the test + full package**

Run: `cd /Users/sausheong/projects/harness && go test ./runtime/ -run TestAssembleMessagesInjectsOrphanedToolResult -v && go test ./runtime/`
Expected: PASS, and the entire runtime suite stays green (especially any existing assembly/orphan tests).

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/harness
git add runtime/context.go runtime/context_test.go
git commit -m "perf(runtime): drop dead resultIDs map and mid-loop tool-result injection (P2)"
```

---

### Task 2: P3 — hoist tool-def normalization out of the turn loop

**Files:**
- Modify: `/Users/sausheong/projects/harness/runtime/runtime.go` (`Run`, tool-def block at `:381-396`)

- [ ] **Step 1: Read the turn loop boundary**

Run: `cd /Users/sausheong/projects/harness && sed -n '340,396p' runtime/runtime.go`
Identify: the `for turn := ...` loop start, and the block at `:381-388`:
```go
toolDefs := r.Tools.ToolDefs()
if r.Permission != nil {
    toolDefs = r.Permission.FilterToolDefs(toolDefs, r.AgentID)
}
sort.SliceStable(toolDefs, func(i, j int) bool { return toolDefs[i].Name < toolDefs[j].Name })
toolDefs, diags := r.LLM.NormalizeToolSchema(toolDefs)
for _, d := range diags { slog.Info("tool schema normalized", ...) }
```
Confirm `toolDefs` is used later in the loop (e.g. in `tokens.Estimate(msgs, ..., toolDefs)` at `:415`/`:552` and the `req` build). Confirm nothing in the loop MUTATES `toolDefs` or depends on per-turn recomputation (Permission is fixed for the Run).

> Note on the redundant sort: `tool.Registry.ToolDefs()` already sorts by name (`tool/tool.go:243`, `sort.Strings(names)`). But `FilterToolDefs` and `NormalizeToolSchema` may reorder/reshape, so keep exactly ONE deterministic `sort.SliceStable` by `Name` AFTER filtering and BEFORE normalize (preserving today's emitted order for prompt-cache stability). Do not add a second sort.

- [ ] **Step 2: Hoist the block above the loop**

Move the entire tool-def block (ToolDefs → FilterToolDefs → sort → NormalizeToolSchema → diags logging) to immediately BEFORE the `for turn := ...` loop. Declare `toolDefs` (and consume `diags` once) there:
```go
	// Tool defs are invariant for the whole Run (tools and permission don't
	// change mid-Run), so normalize once instead of per turn (P3).
	toolDefs := r.Tools.ToolDefs()
	if r.Permission != nil {
		toolDefs = r.Permission.FilterToolDefs(toolDefs, r.AgentID)
	}
	sort.SliceStable(toolDefs, func(i, j int) bool { return toolDefs[i].Name < toolDefs[j].Name })
	toolDefs, diags := r.LLM.NormalizeToolSchema(toolDefs)
	for _, d := range diags {
		slog.Info("tool schema normalized", "tool", d.ToolName, "field", d.Field, "action", d.Action, "reason", d.Reason)
	}
```
Remove the original block from inside the loop. The in-loop references to `toolDefs` now read the hoisted variable (closure capture — same `toolDefs` is in scope since the loop is in the same function body). Reproduce the exact `slog.Info` arg list you saw in Step 1.

> Watch for: the `tr.Mark("context.assemble", ... "tools", len(toolDefs) ...)` line inside the loop still references `toolDefs` — that's fine, it reads the hoisted value. Confirm the build.

- [ ] **Step 3: Build + behavior check**

Run: `cd /Users/sausheong/projects/harness && go build ./... && go test ./runtime/`
Expected: clean build, full runtime suite green (tool-def ordering identical → cache-stability tests like `cache_stability_test.go` must pass).

- [ ] **Step 4: Race check**

Run: `cd /Users/sausheong/projects/harness && go test -race ./runtime/`
Expected: PASS, no race (hoisting a read-only slice out of the loop introduces no sharing).

- [ ] **Step 5: Commit**

```bash
cd /Users/sausheong/projects/harness
git add runtime/runtime.go
git commit -m "perf(runtime): hoist tool-def filter/sort/normalize out of the turn loop (P3)"
```

---

### Task 3: P8 — debounce calibrator persistence to end of Run

**Files:**
- Modify: `/Users/sausheong/projects/harness/runtime/runtime.go` (`Runtime` struct, `Run` goroutine defers, `EventDone` at `:550-555`, turn-estimate at `:415`)
- Test: `/Users/sausheong/projects/harness/runtime/calibrator_persist_test.go` (new)

- [ ] **Step 1: Read the sites**

Run: `cd /Users/sausheong/projects/harness && sed -n '248,270p;410,420p;544,556p' runtime/runtime.go`
Confirm: (a) the `Run` goroutine has `defer close(r.events)` / `defer tr.Summary()` / a deferred OnStop hook (`:254-263`); (b) `:415` computes `estimate := r.calibrator.Adjust(tokens.Estimate(msgs, llm.JoinSystemPromptParts(parts), toolDefs))` once per turn (pre-stream); (c) `:552` does `r.calibrator.Update(event.Usage.InputTokens, tokens.Estimate(msgs, llm.JoinSystemPromptParts(parts), toolDefs))` then `:553-554` calls `r.CalibratorStore.Save(...)` every `EventDone`.

- [ ] **Step 2: Write the failing test**

Create `/Users/sausheong/projects/harness/runtime/calibrator_persist_test.go`. Use a fake provider that drives multiple tool rounds (so multiple `EventDone`s occur) and a `CalibratorStore` pointed at a temp dir; count writes by checking the calibrator file's mtime/content stability OR by wrapping. Simplest robust approach: assert the persisted file is written but the calibrator's in-memory ratio after Run matches a single coherent value, AND that intermediate rounds did NOT each rewrite. Since counting Save calls requires a seam, add a tiny indirection:

First check whether `CalibratorStore.Save` can be observed. The pragmatic test: run a 2-round agent and assert the on-disk calibrator file exists and is valid after Run, and that removing the file mid-run (can't easily) — instead, assert via a write-counting wrapper. Define a minimal counting store in the test:

```go
func TestCalibratorPersistsOncePerRun(t *testing.T) {
	dir := t.TempDir()
	store := tokens.NewCalibratorStore(dir) // verify constructor name/signature
	// Provider that emits one tool call (round 1) then a final text answer (round 2),
	// producing two EventDone events.
	prov := &twoRoundProvider{}
	rt := &Runtime{
		LLM:             prov,
		Tools:           tool.NewRegistry(),
		Session:         session.NewSession("a", "k"),
		AgentID:         "a",
		Model:           "claude-sonnet-4-5",
		Provider:        "anthropic",
		MaxTurns:        4,
		Workspace:       t.TempDir(),
		CalibratorStore: store,
	}
	_, err := rt.RunSync(context.Background(), "go", nil)
	require.NoError(t, err)

	// After the run, the calibrator file exists and parses.
	// (The behavioral guarantee — one write per Run — is asserted by the
	// write-count seam below if available; otherwise this confirms persistence
	// still happens at all.)
	got := tokens.NewCalibratorStore(dir)
	ratio, count := got.Load("a", "k") // verify Load signature
	require.Greater(t, count, 0, "calibrator must be persisted by end of Run")
	require.Greater(t, ratio, 0.0)
}
```

> **Implementer:** verify `tokens.NewCalibratorStore`, `.Load`, and the `twoRoundProvider` shape against the package. For the write-count assertion, the cleanest seam is to check `os.Stat` mtime before/after is unchanged across the second round — but that's flaky. Prefer this: make the test assert persistence-after-Run (above), AND add a focused unit test that the inline per-round Save is gone by asserting the calibrator file does NOT exist immediately after the FIRST EventDone. Since you can't hook mid-run easily, the PRIMARY correctness guard is: the existing calibrator tests still pass, persistence still happens at Run end, and code review confirms the per-round Save call was removed. If a write-counting `CalibratorStore` seam is impractical, state that and rely on the after-Run assertion + the diff.

- [ ] **Step 3: Run the test**

Run: `cd /Users/sausheong/projects/harness && go test ./runtime/ -run TestCalibratorPersistsOncePerRun -v`
Expected: PASS already with current code (persistence happens — per round). This is a regression guard for "persistence still works after we move it to Run-end."

- [ ] **Step 4: Implement the debounce**

In `runtime/runtime.go`:
1. Add an unexported field to the `Runtime` struct near `calibrator` (`:113`): `calibratorDirty bool`. (Single-goroutine access on the main Run loop — no mutex needed; the calibrator Update/Save all happen on the Run goroutine. Confirm EventDone is handled on the Run goroutine, not a kickoff goroutine — it is, it's in the stream loop.)
2. At `:415`, the per-turn `estimate` is already computed. Hoist it so the `EventDone` handler can reuse it: introduce a turn-scoped variable `turnEstimate := tokens.Estimate(msgs, llm.JoinSystemPromptParts(parts), toolDefs)` where `:415` currently inlines it, use it for the existing `Adjust` call, AND reference it in EventDone. (If `:415`'s estimate and the EventDone estimate are computed from the same `msgs`/`parts`/`toolDefs`, they're equal — reuse is safe.)
3. At `:552-554`, replace:
   ```go
   r.calibrator.Update(event.Usage.InputTokens, tokens.Estimate(msgs, llm.JoinSystemPromptParts(parts), toolDefs))
   if r.CalibratorStore != nil && r.Session != nil {
       ratio, count := r.calibrator.Snapshot()
       r.CalibratorStore.Save(r.AgentID, r.Session.Key, ratio, count)
   }
   ```
   with:
   ```go
   r.calibrator.Update(event.Usage.InputTokens, turnEstimate)
   r.calibratorDirty = true
   ```
4. Add a deferred flush in the `Run` goroutine, alongside the existing defers (`:254-263`). Place it so it runs on every exit path:
   ```go
   defer func() {
       if r.calibratorDirty && r.CalibratorStore != nil && r.Session != nil && r.calibrator != nil {
           ratio, count := r.calibrator.Snapshot()
           r.CalibratorStore.Save(r.AgentID, r.Session.Key, ratio, count)
       }
   }()
   ```
   Verify `Calibrator.Snapshot()` returns `(ratio float64, count int)` (used at the old site).

> Scope: do NOT change `tokens/persist.go`. The flush is driven from the runtime. If `turnEstimate` can't be cleanly threaded because `:415` is in a different scope than `EventDone`, declare `turnEstimate` in the turn-loop scope before the stream starts and assign it where `:415` computes it; EventDone (same turn iteration) reads it.

- [ ] **Step 5: Run the test + full package + race**

Run: `cd /Users/sausheong/projects/harness && go test ./runtime/ -run TestCalibratorPersistsOncePerRun -v && go test ./runtime/ && go test -race ./runtime/`
Expected: PASS; full suite green; no race.

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/harness
git add runtime/runtime.go runtime/calibrator_persist_test.go
git commit -m "perf(runtime): persist calibrator once per Run; reuse per-turn estimate (P8)"
```

---

## Group 2 — I/O off the critical path

### Task 4: P1 — stop re-spilling oversized tool results every turn

**Files:**
- Modify: `/Users/sausheong/projects/harness/runtime/runtime.go` (`Runtime` struct + 5 `pruneToolResults` call sites)
- Modify: `/Users/sausheong/projects/harness/runtime/context.go` (`pruneToolResults` signature + body)
- Test: `/Users/sausheong/projects/harness/runtime/agent_test.go` (P1 idempotency)

- [ ] **Step 1: Read prune + call sites**

Run: `cd /Users/sausheong/projects/harness && sed -n '464,494p' runtime/context.go && grep -n "pruneToolResults" runtime/runtime.go`
Confirm the call sites in `runtime.go`: `:379`, `:405`, `:431`, `:464`, `:773` (all `pruneToolResults(msgs, r.maxToolResultLen(), spillCfg)` — except `:773` uses `newMsgs`). And the 7 test call sites in `agent_test.go` (which pass `spillConfig{}` directly).

- [ ] **Step 2: Write the failing test**

Add to `/Users/sausheong/projects/harness/runtime/agent_test.go`:

```go
func TestPruneToolResultsSpillsOncePerID(t *testing.T) {
	workspace := t.TempDir()
	cfg := spillConfig{Workspace: workspace, SessionKey: "sess_once"}
	spilled := map[string]bool{}

	big := strings.Repeat("x", 50000)
	mk := func() []llm.Message {
		return []llm.Message{{ToolCallID: "call_big", Content: big}}
	}

	// Turn 1: fresh message slice rebuilt from session.View() -> must spill (write).
	m1 := mk()
	pruneToolResults(m1, 10000, cfg, spilled)
	require.Contains(t, m1[0].Content, spillMarker)
	spillPath := filepath.Join(workspace, ".harness", "spill", "sess_once", "call_big.txt")
	require.FileExists(t, spillPath)
	require.True(t, spilled["call_big"], "ID recorded as spilled")

	// Capture the spill file's mtime, then delete it to PROVE turn 2 does not rewrite it.
	info1, err := os.Stat(spillPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(spillPath))

	// Turn 2: fresh slice again (simulating a new turn's assembleMessages), same ID.
	m2 := mk()
	pruneToolResults(m2, 10000, cfg, spilled)
	require.Contains(t, m2[0].Content, spillMarker, "message still rewritten to the marker")
	require.NoFileExists(t, spillPath, "turn 2 must NOT re-write the spill file (idempotent via spilled set)")
	_ = info1
}
```

> Verify `spillMarker` is accessible from the test (same package `runtime`) — it is (`context.go:406`). Adjust imports (`strings`, `os`, `path/filepath`, testify) at the top of `agent_test.go` if missing.

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd /Users/sausheong/projects/harness && go test ./runtime/ -run TestPruneToolResultsSpillsOncePerID -v`
Expected: FAIL to compile — `pruneToolResults` takes 3 args, not 4 (`spilled` param doesn't exist yet).

- [ ] **Step 4: Add `spilledIDs` to Runtime + change `pruneToolResults` signature**

In `runtime/runtime.go`, add to the `Runtime` struct near `touchedMu`/`touchedFiles` (`:103-105`):
```go
	// spilledMu guards spilledIDs: the set of ToolCallIDs whose oversized
	// output has already been written to the workspace spill dir this Run.
	// pruneToolResults consults it to avoid re-writing the same (immutable)
	// content to disk every turn (P1).
	spilledMu  sync.Mutex
	spilledIDs map[string]bool
```
Initialize `spilledIDs` where the Runtime is constructed (find `BuildRuntime` in `runtime/builder.go` — search `grep -n "&Runtime{" runtime/*.go`; add `spilledIDs: map[string]bool{}`). If construction is scattered, instead lazily init inside the helper (see below) to avoid nil-map panics.

In `runtime/context.go`, change `pruneToolResults` to accept the set. Since tests call it directly with a plain `map[string]bool`, pass the map (not the Runtime):
```go
func pruneToolResults(msgs []llm.Message, maxLen int, cfg spillConfig, spilled map[string]bool) {
	for i := range msgs {
		if msgs[i].ToolCallID == "" || len(msgs[i].Content) <= maxLen {
			continue
		}
		if strings.Contains(msgs[i].Content, truncationMarker) ||
			strings.Contains(msgs[i].Content, spillMarker) {
			continue
		}
		originalLen := len(msgs[i].Content)
		head := msgs[i].Content[:maxLen]
		if idx := strings.LastIndex(head, "\n"); idx > maxLen/2 {
			head = head[:idx]
		}

		if cfg.Workspace != "" && cfg.SessionKey != "" {
			id := msgs[i].ToolCallID
			path := filepath.Join(cfg.Workspace, ".harness", "spill", cfg.SessionKey, id+".txt")
			if spilled != nil && spilled[id] {
				// Already written this Run; reconstruct the marker without re-writing.
				msgs[i].Content = fmt.Sprintf("%s\n\n%s%d of %d chars saved to %s; use read_file to access the full output]",
					head, spillMarker, len(head), originalLen, path)
				continue
			}
			if writtenPath, err := spillToolResult(cfg, id, msgs[i].Content); err == nil {
				if spilled != nil {
					spilled[id] = true
				}
				msgs[i].Content = fmt.Sprintf("%s\n\n%s%d of %d chars saved to %s; use read_file to access the full output]",
					head, spillMarker, len(head), originalLen, writtenPath)
				continue
			}
		}

		msgs[i].Content = fmt.Sprintf("%s\n\n%s%d of %d chars; re-run the tool with offset/limit to see more]",
			head, truncationMarker, len(head), originalLen)
	}
}
```
(`writtenPath` from `spillToolResult` equals the deterministic `path`; using it keeps the existing behavior on the write branch.)

- [ ] **Step 5: Update the 5 runtime.go call sites + 7 test call sites**

In `runtime.go`, each `pruneToolResults(msgs, r.maxToolResultLen(), spillCfg)` becomes a call that passes the Runtime's set under its lock. Add a tiny helper method to keep the call sites clean and the locking correct:
```go
func (r *Runtime) prune(msgs []llm.Message, spillCfg spillConfig) {
	r.spilledMu.Lock()
	if r.spilledIDs == nil {
		r.spilledIDs = map[string]bool{}
	}
	pruneToolResults(msgs, r.maxToolResultLen(), spillCfg, r.spilledIDs)
	r.spilledMu.Unlock()
}
```
Replace all 5 call sites (`:379`, `:405`, `:431`, `:464`, `:773`) with `r.prune(msgs, spillCfg)` (and `r.prune(newMsgs, spillCfg)` at `:773`).

> Holding `spilledMu` across `pruneToolResults` is fine — the disk write only happens on first spill; subsequent turns are map-lookup-only under the lock. This serializes prune calls, but prune is already only called on the main Run goroutine in practice; the lock is belt-and-suspenders for the streaming-tools path.

In `agent_test.go`, update the 7 existing `pruneToolResults(...)` calls to pass a final `nil` (they don't test the spilled-set behavior): e.g. `pruneToolResults(msgs, 10000, spillConfig{}, nil)`. The new `TestPruneToolResultsSpillsOncePerID` passes its own `spilled` map.

- [ ] **Step 6: Run the test + full package + race**

Run: `cd /Users/sausheong/projects/harness && go test ./runtime/ -run TestPruneToolResultsSpillsOncePerID -v && go test ./runtime/ && go test -race ./runtime/`
Expected: PASS; full suite green (existing spill tests with `nil` set still spill every call, which is their existing single-call behavior); no race.

- [ ] **Step 7: Verify Felix builds**

Run: `cd /Users/sausheong/projects/felix && go build ./...`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
cd /Users/sausheong/projects/harness
git add runtime/runtime.go runtime/context.go runtime/agent_test.go
git commit -m "perf(runtime): track spilled tool-call IDs to skip re-writing spills each turn (P1)"
```

---

### Task 5: P5 — release session lock before the disk write

**Files:**
- Modify: `/Users/sausheong/projects/harness/session/session.go` (`Append` at `:101-122`)
- Test: `/Users/sausheong/projects/harness/session/session_test.go` (new `-race` test)

- [ ] **Step 1: Read Append**

Run: `cd /Users/sausheong/projects/harness && sed -n '100,122p' session/session.go`
Confirm the current body: locks `s.mu`, assigns ID/Timestamp/ParentID, appends to `s.entries`, sets `entryMap`, sets `leafID`, then (still locked) calls `s.store.AppendEntry(s, entry)`.

- [ ] **Step 2: Write the failing/guard test**

Add to `/Users/sausheong/projects/harness/session/session_test.go` (create if absent, `package session`):

```go
func TestConcurrentAppendAndViewNoRace(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	require.NoError(t, store.Create("a", "k"))
	sess := NewSession("a", "k")
	sess.SetStore(store) // wires store -> session for automatic persistence

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			sess.Append(SessionEntry{Type: EntryTypeMessage, Role: "user", Data: []byte(`{"text":"x"}`)})
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = sess.View()
		}
	}()
	wg.Wait()

	// All 500 appends persisted and ordering preserved on reload.
	require.Len(t, sess.Entries(), 500)
}
```

> Verify the real API: how a `*Store` is attached to a `*Session` (the production path sets `s.store` — find the constructor/wirer, e.g. `NewSession` + a setter, or `store.NewSession`). Use whatever the real code uses; if sessions are created via `store.Load`/`store.NewSession`, use that instead of `AttachStore`. Confirm `View()`, `Entries()`, `EntryTypeMessage`, `NewStore`, `Create` names. The load-bearing part: one goroutine Appends, another Views, under `-race`.

- [ ] **Step 3: Run under -race to verify current state**

Run: `cd /Users/sausheong/projects/harness && go test -race ./session/ -run TestConcurrentAppendAndViewNoRace -v`
Expected: PASS today (the current code is correct — it just holds the lock too long). This test guards that the optimization doesn't INTRODUCE a race. (`NewStore`, `NewSession`, `SetStore`, `View`, `Entries`, `EntryTypeMessage` are all confirmed real APIs.)

- [ ] **Step 4: Move the disk write outside the lock**

Rewrite `Append` in `session/session.go`:
```go
// Append adds an entry to the session.
func (s *Session) Append(entry SessionEntry) {
	s.mu.Lock()
	if entry.ID == "" {
		entry.ID = generateID("e")
	}
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().Unix()
	}
	if s.leafID != "" && entry.ParentID == "" {
		entry.ParentID = s.leafID
	}

	s.entries = append(s.entries, entry)
	s.entryMap[entry.ID] = &s.entries[len(s.entries)-1]
	s.leafID = entry.ID

	finalized := entry // value copy with all fields now set
	store := s.store
	s.mu.Unlock()

	// Persist after releasing s.mu so disk latency doesn't block concurrent
	// View()/append callers. Store.mu still serializes the file write and
	// preserves append ordering (callers reach this point in lock order). (P5)
	if store != nil {
		store.AppendEntry(s, finalized)
	}
}
```

> Key correctness points: the entry's fields (ID/Timestamp/ParentID) are all assigned BEFORE the copy, so `finalized` is complete. `entryMap` stores `&s.entries[...]` (a pointer into the slice) — that's set under the lock and unaffected by the copy. The copy avoids handing `AppendEntry` a reference that could be mutated by a later append reallocating `s.entries`.

- [ ] **Step 5: Run the test + full package + race**

Run: `cd /Users/sausheong/projects/harness && go test ./session/ && go test -race ./session/`
Expected: PASS; the concurrent test green; all existing session round-trip/DAG tests green.

- [ ] **Step 6: Verify Felix builds**

Run: `cd /Users/sausheong/projects/felix && go build ./...`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
cd /Users/sausheong/projects/harness
git add session/session.go session/session_test.go
git commit -m "perf(session): persist entry after releasing the session lock (P5)"
```

---

## Group 3 — Felix-side

### Task 6: P4 — cache per-turn Runtime prompt inputs + dedupe config summary

**Files:**
- Modify: `/Users/sausheong/projects/felix/internal/agent/agent.go`
- Modify: `/Users/sausheong/projects/felix/internal/chatexec/chatexec.go` (`MakeSubagentFactory` call at `:190`)
- Modify: `/Users/sausheong/projects/felix/internal/startup/startup.go` (`MakeSubagentFactory` call at `:891`; `BumpConfigGeneration` in reload callback)
- Test: `/Users/sausheong/projects/felix/internal/agent/promptcache_test.go` (new)

- [ ] **Step 1: Read the build path**

Run: `cd /Users/sausheong/projects/felix && sed -n '113,160p' internal/agent/agent.go && grep -rn "MakeSubagentFactory" internal/`
Confirm: `BuildRuntimeForAgent` computes `buildConfigSummary(deps.Config)` at `:127` and `loadAgentMemoryFiles(a.Workspace) + felixEnvHint() + cortexStaticHint(deps.Config)` at `:128`; `MakeSubagentFactory` recomputes `buildConfigSummary` at `:199`. Felix call sites of `MakeSubagentFactory`: `chatexec.go:190`, `startup.go:891` (the `agent.go:208` one is `hrt.MakeSubagentFactory` — harness, leave it).

- [ ] **Step 2: Write the failing test**

Create `/Users/sausheong/projects/felix/internal/agent/promptcache_test.go`:

```go
package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigGenerationBumpInvalidates(t *testing.T) {
	start := configGeneration.Load()
	BumpConfigGeneration()
	require.Equal(t, start+1, configGeneration.Load(), "bump must increment the generation")
}

func TestPromptCacheReusesWithinGeneration(t *testing.T) {
	// Two reads at the same generation for the same agentID return the same
	// cached parts pointer/value without recomputation.
	agentID := "cache-test-agent"
	gen := configGeneration.Load()
	_, hit1 := promptCacheGet(agentID, gen)
	require.False(t, hit1, "first get is a miss")

	promptCachePut(agentID, gen, cachedPrompt{configSummary: "S", memoryFiles: "M"})
	parts2, hit2 := promptCacheGet(agentID, gen)
	require.True(t, hit2, "second get at same gen is a hit")
	require.Equal(t, "S", parts2.configSummary)

	// After a bump, the old gen entry no longer matches.
	BumpConfigGeneration()
	_, hit3 := promptCacheGet(agentID, configGeneration.Load())
	require.False(t, hit3, "after bump, new gen is a miss")
}
```

> This test pins the cache primitives' names: `configGeneration` (`atomic.Int64`), `BumpConfigGeneration()`, `promptCacheGet(agentID string, gen int64) (cachedPrompt, bool)`, `promptCachePut(agentID string, gen int64, p cachedPrompt)`, and a `cachedPrompt` struct with at least `configSummary`/`memoryFiles` fields. The test imports only `testing` + testify (`configGeneration.Load()` is a method on the package var, so no `sync/atomic` import in the test). Keep names consistent with Step 3.

- [ ] **Step 3: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/agent/ -run 'TestConfigGeneration|TestPromptCache' -v`
Expected: FAIL (undefined `configGeneration`, `BumpConfigGeneration`, `promptCacheGet`, `promptCachePut`, `cachedPrompt`).

- [ ] **Step 4: Implement the cache primitives**

Add to `internal/agent/agent.go` (package scope; import `sync` and `sync/atomic`):

```go
// configGeneration is bumped on every config hot-reload. The prompt cache is
// keyed by (agentID, generation), so a reload invalidates all cached prompts
// without per-entry bookkeeping (P4).
var configGeneration atomic.Int64

// BumpConfigGeneration invalidates the prompt cache. Called from the startup
// fsnotify reload callback.
func BumpConfigGeneration() { configGeneration.Add(1) }

// cachedPrompt holds the per-agent invariants recomputed only on a generation
// change: the config summary, the assembled memory-files block, and the
// composed memory-files string handed to the harness (configSummary +
// memoryFiles are the disk-derived parts).
type cachedPrompt struct {
	gen           int64
	configSummary string
	memoryFiles   string
}

var (
	promptCacheMu sync.Mutex
	promptCache   = map[string]cachedPrompt{}
)

func promptCacheGet(agentID string, gen int64) (cachedPrompt, bool) {
	promptCacheMu.Lock()
	defer promptCacheMu.Unlock()
	c, ok := promptCache[agentID]
	if ok && c.gen == gen {
		return c, true
	}
	return cachedPrompt{}, false
}

func promptCachePut(agentID string, gen int64, p cachedPrompt) {
	p.gen = gen
	promptCacheMu.Lock()
	promptCache[agentID] = p
	promptCacheMu.Unlock()
}
```

- [ ] **Step 5: Use the cache in `BuildRuntimeForAgent` + dedupe summary**

Rewrite the `hdeps` construction (`:118-129`) to consult the cache:
```go
	gen := configGeneration.Load()
	cached, ok := promptCacheGet(a.ID, gen)
	if !ok {
		cached = cachedPrompt{
			configSummary: buildConfigSummary(deps.Config),
			memoryFiles:   loadAgentMemoryFiles(a.Workspace) + felixEnvHint() + cortexStaticHint(deps.Config),
		}
		promptCachePut(a.ID, gen, cached)
	}
	hdeps := hrt.RuntimeDeps{
		Permission: deps.Permission,
		AgentLoop: hrt.LoopConfig{
			MaxToolConcurrency: deps.AgentLoop.MaxToolConcurrency,
			MaxAgentDepth:      deps.AgentLoop.MaxAgentDepth,
			StreamingTools:     deps.AgentLoop.StreamingTools,
			MaxToolResultLen:   effectiveMaxToolResultLen(deps.AgentLoop.MaxToolResultLen),
		},
		CalibratorStore: deps.CalibratorStore,
		ConfigSummary:   cached.configSummary,
		MemoryFiles:     cached.memoryFiles,
	}
```
Then change `MakeSubagentFactory` to ACCEPT the config summary instead of recomputing it. Update its signature (`:156`) to add a `configSummary string` param, and at `:199` use that param instead of `buildConfigSummary(deps.Config)`.

- [ ] **Step 6: Update the two `MakeSubagentFactory` call sites**

- `chatexec.go:190`: `factory := agent.MakeSubagentFactory(deps.Config, runtimeDeps, deps.SubagentBuild, rt)` → pass the summary. The cleanest source is `runtimeDeps`/the cached value; compute once via `agent`'s cache. Simplest: expose the summary the parent already built. Since `RunTurn` calls `BuildRuntimeForAgent` (which now caches), fetch the summary from the cache here too:
  ```go
  cs := agent.ConfigSummaryFor(deps.Config) // see helper below
  factory := agent.MakeSubagentFactory(deps.Config, runtimeDeps, deps.SubagentBuild, cs, rt)
  ```
- `startup.go:891`: similarly `factory := agent.MakeSubagentFactory(cfg, runtimeDeps, buildSubagentInputs, agent.ConfigSummaryFor(cfg), rt)`.

Add a small exported helper to `agent.go` so call sites don't duplicate the cache logic:
```go
// ConfigSummaryFor returns the (cached) config summary for the current
// generation. Used by subagent-factory call sites so they share the same
// computation as BuildRuntimeForAgent.
func ConfigSummaryFor(cfg *config.Config) string {
	// agentID-agnostic: the summary depends only on cfg, so cache under a
	// fixed key per generation.
	const key = "__config_summary__"
	gen := configGeneration.Load()
	if c, ok := promptCacheGet(key, gen); ok {
		return c.configSummary
	}
	cs := buildConfigSummary(cfg)
	promptCachePut(key, gen, cachedPrompt{configSummary: cs})
	return cs
}
```

> This keeps `buildConfigSummary` called at most once per generation across all sites. Verify `MakeSubagentFactory`'s body at `:199` now uses the passed-in `configSummary` param (not `ConfigSummaryFor`, to avoid a redundant lookup — the caller already resolved it).

- [ ] **Step 7: Bump generation on reload**

In `internal/startup/startup.go`, in the fsnotify watcher callback (where `applyAutoAddedAllowlists(newCfg, mcpNames)` and `wsHandler.SetPermission(...)` are called — round 2's R2 site), add:
```go
			agent.BumpConfigGeneration()
```
(near the other reload-callback updates, before/after the permission rebuild — order doesn't matter, it just invalidates the next build). Confirm `agent` is imported in startup.go (it is).

- [ ] **Step 8: Run tests + build**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/agent/ -run 'TestConfigGeneration|TestPromptCache' -v && go build ./... && go test ./internal/agent/ ./internal/chatexec/ ./internal/startup/`
Expected: PASS / clean.

- [ ] **Step 9: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/agent/agent.go internal/agent/promptcache_test.go internal/chatexec/chatexec.go internal/startup/startup.go
git commit -m "perf(agent): cache per-generation prompt inputs; dedupe config summary (P4)"
```

---

### Task 7: P6 — parallelize startup (background Ollama-ready + concurrent MCP)

**Files:**
- Modify: `/Users/sausheong/projects/felix/internal/local/supervisor.go` (split `Start` into `Spawn` + `WaitReady`)
- Modify: `/Users/sausheong/projects/felix/internal/startup/startup.go` (background ready, parallel MCP)

- [ ] **Step 1: Read the supervisor + startup sites**

Run: `cd /Users/sausheong/projects/felix && sed -n '100,160p' internal/local/supervisor.go && sed -n '428,460p;554,570p' internal/startup/startup.go`
Confirm: `Start(ctx)` does reap+spawn (`:104-148`, sets `s.boundPort`) then `waitReady` (`:150-159`). Startup calls `localSup.Start(startCtx)` at `:429`, then `InjectLocalProvider(configPath, localSup.BoundPort())` at `:433`. MCP at `startup.go:560`.

- [ ] **Step 2: Split the supervisor (no test change — internal refactor, guarded by existing local tests)**

In `internal/local/supervisor.go`, split `Start` into two exported methods:
```go
// Spawn starts the ollama child and binds its port, returning as soon as the
// process is launched (does NOT wait for readiness). BoundPort() is valid
// after Spawn returns nil.
func (s *Supervisor) Spawn() error {
	s.reapStaleChild()
	port, err := probeFreePort(s.portLow, s.portHigh)
	if err != nil {
		return err
	}
	childCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(childCtx, s.binPath, "serve")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("OLLAMA_HOST=127.0.0.1:%d", port),
		fmt.Sprintf("OLLAMA_MODELS=%s", s.modelsDir),
		fmt.Sprintf("OLLAMA_KEEP_ALIVE=%s", s.keepAlive),
	)
	setProcAttr(cmd)
	pipeStderr, _ := cmd.StderrPipe()
	pipeStdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("ollama: start: %w", err)
	}
	go forwardLogs(pipeStdout, "ollama-stdout")
	go forwardLogs(pipeStderr, "ollama-stderr")
	s.mu.Lock()
	s.cmd = cmd
	s.cancelCtx = cancel
	s.boundPort = port
	s.mu.Unlock()
	s.alive.Store(true)
	s.writePIDFile(cmd.Process.Pid)
	s.exited = make(chan struct{})
	go func() {
		_ = cmd.Wait()
		s.alive.Store(false)
		close(s.exited)
		slog.Warn("ollama exited; local provider is now unhealthy. Restart felix to recover.")
	}()
	return nil
}

// WaitReady blocks until the spawned ollama answers /api/version or ctx/timeout
// elapses. Call after Spawn.
func (s *Supervisor) WaitReady(ctx context.Context) error {
	s.mu.Lock()
	port := s.boundPort
	s.mu.Unlock()
	timeout := s.readyTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	if err := s.waitReady(ctx, port, timeout); err != nil {
		_ = s.Stop()
		return err
	}
	slog.Info("ollama supervisor ready", "port", port, "models_dir", s.modelsDir)
	return nil
}

// Start preserves the original blocking behavior (Spawn + WaitReady) for any
// caller that wants it.
func (s *Supervisor) Start(ctx context.Context) error {
	if err := s.Spawn(); err != nil {
		return err
	}
	return s.WaitReady(ctx)
}
```
Reproduce the EXACT spawn body you read in Step 1 (env vars, `setProcAttr`, pipes, pid file, exit goroutine) — this is a mechanical extraction, not a rewrite.

- [ ] **Step 3: Build + run local tests**

Run: `cd /Users/sausheong/projects/felix && go build ./... && go test ./internal/local/`
Expected: clean / green (the extraction preserves `Start`'s behavior; existing tests that call `Start` still pass).

- [ ] **Step 4: Rewire startup to Spawn-then-background-ready**

In `internal/startup/startup.go` around `:428-460`, change the blocking `localSup.Start(startCtx)` to `Spawn()` synchronously, inject the provider using `BoundPort()`, then run `WaitReady` + the warmups in a goroutine:
```go
			localSup = local.New(local.Options{ /* unchanged */ })
			if err := localSup.Spawn(); err != nil {
				slog.Warn("failed to spawn bundled ollama; local provider disabled", "error", err)
				localSup = nil
			} else {
				if ierr := local.InjectLocalProvider(configPath, localSup.BoundPort()); ierr != nil {
					slog.Warn("failed to inject local provider config", "error", ierr)
				}
				if reloaded, rerr := config.Load(configPath); rerr == nil {
					cfg.UpdateFrom(reloaded)
				}
				// Wait-for-ready + model warmups in the background so startup
				// doesn't block ~60s on Ollama readiness. The local provider's
				// port is already known from Spawn. (P6)
				sup := localSup
				go func() {
					rctx, rcancel := context.WithTimeout(context.Background(), 70*time.Second)
					defer rcancel()
					if err := sup.WaitReady(rctx); err != nil {
						slog.Warn("bundled ollama did not become ready", "error", err)
						return
					}
					// <<< move the existing first-run model-pull / warmup block here,
					//     verbatim from its current location (the `if cfg.Local.Enabled {`
					//     block that followed Start) >>>
				}()
			}
```
Read the existing warmup/model-pull block that currently follows `Start` (the `if cfg.Local.Enabled { ... }` first-run pulls) and MOVE it inside the goroutine after `WaitReady` succeeds. Preserve its exact logic.

> The `local` provider block in `cfg` is injected synchronously (port known), so `InitProviders` downstream still sees it. Only readiness/warmups are deferred.

- [ ] **Step 5: Run MCP init concurrently with provider/skill init**

The MCP `NewManager` at `:560` (up to 30s) currently runs serially. Wrap it and the independent init work in a small concurrent section using `golang.org/x/sync/errgroup` (already an indirect dep; will become direct). The dependency constraint: `mcp.RegisterTools` + `applyAutoAddedAllowlists` + `BuildPermissionChecker` must run AFTER both the MCP manager exists AND providers are initialized. So:
- Identify the work between provider init and `mcp.NewManager` that does NOT depend on the MCP manager (provider init, session store, skill loader). If that work currently runs BEFORE `:560`, the simplest safe win is: run `mcp.NewManager` in an errgroup goroutine started EARLY (right after `mcpServerCfgs` is resolved), and `eg.Wait()` for it just before `mcp.RegisterTools` at `:565`, while provider/skill/session init proceeds in the main goroutine in between.

Concretely:
```go
	mcpServerCfgs, err := cfg.ResolveMCPServers()
	if err != nil {
		return nil, fmt.Errorf("resolve mcp_servers: %w", err)
	}
	var mcpMgr *mcp.Manager
	var eg errgroup.Group
	eg.Go(func() error {
		mctx, mcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer mcancel()
		m, mErr := mcp.NewManager(mctx, mcpServerCfgs)
		if mErr != nil {
			return fmt.Errorf("init mcp manager: %w", mErr)
		}
		mcpMgr = m
		return nil
	})

	// ... any provider/session/skill init that does NOT need mcpMgr runs here,
	//     concurrently with the MCP connect ...

	if err := eg.Wait(); err != nil {
		return nil, err
	}
	mcpNames, err := mcp.RegisterTools(toolReg, mcpMgr, cfg.IsServerParallelSafe)
	// ... unchanged from here ...
```
Add `"golang.org/x/sync/errgroup"` to imports and run `go mod tidy` (it moves x/sync from indirect to direct).

> IMPORTANT: only move init work into the concurrent window if it genuinely does not touch `mcpMgr` or `toolReg`'s MCP tools. If the code between `mcpServerCfgs` and `NewManager` is minimal, the win is mainly from overlapping the 30s MCP connect with whatever init follows. Keep it conservative: if unsure whether a piece is independent, leave it after `eg.Wait()`. The correctness bar (MCP tools registered → allowlists applied → permission built → server started, all before return) MUST hold. Read the full `:554-600` region and place `eg.Wait()` so that invariant is preserved.

- [ ] **Step 6: Build + vet + startup tests**

Run: `cd /Users/sausheong/projects/felix && go build ./... && go vet ./internal/startup/ ./internal/local/ && go test ./internal/startup/ ./internal/local/`
Expected: clean / green. The existing startup test must still pass (it asserts the wired components exist).

- [ ] **Step 7: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/local/supervisor.go internal/startup/startup.go go.mod go.sum
git commit -m "perf(startup): background ollama readiness + concurrent MCP init (P6)"
```

---

## Final verification

### Task 8: Full test + race + vet across both repos

- [ ] **Step 1: harness**

Run: `cd /Users/sausheong/projects/harness && go build ./... && go test ./... && go vet ./... && go test -race ./runtime/ ./session/`
Expected: clean build, all tests pass, vet clean, no races.

- [ ] **Step 2: felix**

Run: `cd /Users/sausheong/projects/felix && go build ./... && go test ./... && go vet ./...`
Expected: clean build, all tests pass, vet clean.

- [ ] **Step 3: felix race on touched packages**

Run: `cd /Users/sausheong/projects/felix && go test -race ./internal/agent/ ./internal/startup/ ./internal/local/`
Expected: PASS, no races.

---

## Self-Review Notes (coverage map)

| Spec section | Finding | Task |
|--------------|---------|------|
| §3.1 | P2 dead assembly code | Task 1 |
| §3.2 | P3 hoist tool-def normalize | Task 2 |
| §3.3 | P8 debounce calibrator | Task 3 |
| §4.1 | P1 spill idempotency | Task 4 |
| §4.2 | P5 session lock | Task 5 |
| §5.1 | P4 prompt cache | Task 6 |
| §5.2 | P6 parallel startup | Task 7 |
| §6 | testing | per-task + Task 8 |

All seven findings map to tasks. Deferred items (P7, per-session store mutex, early HTTP/starting-state, memory-file mtime invalidation) are intentionally absent per spec §8.
