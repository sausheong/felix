# Performance Round — Design

**Date:** 2026-06-13
**Status:** Design (pending implementation plan)
**Scope:** Execution-order step 4 from `optimisation.md` — performance on the agent hot path.
Seven findings: P2, P3, P8, P1, P5 (harness), P4, P6 (Felix).
**Repos touched:** `harness` and `felix` (wired via `go.mod replace`).

---

## 1. Goal & constraint

Every chat turn pays these costs today: an O(n²) message re-assembly plus a dead full-payload
unmarshal (P2), a full tool-def normalize/sort pipeline re-run inside the turn loop (P3), a
multi-MB re-spill of every oversized tool result (P1), a disk write of the calibrator on every
LLM round (P8), and disk latency held inside the session lock (P5). Felix additionally rebuilds
the entire Runtime — including disk reads and prompt concatenation — on every message (P4), and
startup serializes for up to ~90s behind Ollama readiness then MCP connect before the HTTP
server binds (P6).

**Hard constraint:** these are optimizations. The bar is **zero observable behavior change** —
identical LLM requests, identical session contents, identical tool outputs. Any existing test
whose output changes indicates a bug in the optimization, not a test to update. All work is
validated under `go test -race` with the existing suites kept green.

All seven findings were re-verified against `main` on 2026-06-13 (post rounds 1–2).

---

## 2. Architecture: three groups, one spec

| Group | Findings | Repo | Risk |
|-------|----------|------|------|
| 1. Hot-path micro-opts | P2, P3, P8 | harness | low (mechanical / additive) |
| 2. I/O off the critical path | P1, P5 | harness | medium (correctness/concurrency) |
| 3. Felix-side | P4, P6 | felix | medium (caching / startup ordering) |

Findings within a group share files (P1/P2/P3 in `runtime/context.go` + `runtime/runtime.go`;
P4/P6 in Felix startup-adjacent code), so grouping avoids touching the same code twice.

**P7 (memory embedding) is excluded** — it requires the separate "wire up `Manager.Search`
into a real tool, or delete the vector/embedder machinery" intent decision. Not in this round.

---

## 3. Group 1 — Hot-path micro-opts (P2, P3, P8)

### 3.1 P2 — drop the O(n²) assembly + dead `resultIDs` map

**Where:** `harness/runtime/context.go:170-178` (the `resultIDs` first pass),
`:224` (mid-loop `injectMissingToolResults(msgs)`), `:319` (terminal
`injectMissingToolResults` pass), `:335` (`injectMissingToolResults` definition).

**Problem:** `assembleMessages` builds `resultIDs` by `json.Unmarshal`-ing every historical
tool-result `Data` (the largest payloads in the session) into a `map[string]bool` that is
**never read**. Separately, it calls `injectMissingToolResults(msgs)` *inside* the per-entry
loop (`:224`), re-scanning and re-allocating over everything assembled so far (O(entries ×
messages)) — while the terminal pass at `:319` already injects all orphans correctly.

**Fix:**
1. Delete the `resultIDs` first-pass loop (`:172-178`) entirely — it's dead.
2. Delete the mid-loop `injectMissingToolResults(msgs)` call at `:224`. Keep the terminal pass
   at `:319` (it is the authoritative orphan-injection).
3. Leave `injectMissingToolResults` itself unchanged.

**Validation:** the existing orphan-injection test must stay green (it exercises the terminal
pass). Assembled output for any history must be byte-identical to before.

### 3.2 P3 — hoist tool-def normalization out of the turn loop

**Where:** `harness/runtime/runtime.go:381-388` (inside the `for turn` loop:
`ToolDefs() → Permission.FilterToolDefs → sort.SliceStable → LLM.NormalizeToolSchema`).

**Problem:** tools cannot change mid-`Run`, yet the whole filter/sort/normalize pipeline runs
every turn over ~30–60 tools, repeatedly marshalling/parsing ~50–150 KB of schema JSON.

**Fix:** compute `toolDefs` (filter → sort → normalize) **once**, just above the `for turn`
loop, and reference the result inside the loop. The `diags` logging stays at the computation
site (it now logs once per Run instead of once per turn — an improvement). Verify whether
`r.Tools.ToolDefs()` already returns a sorted slice; if so, the `sort.SliceStable` is
redundant — keep exactly one deterministic sort before normalize to preserve prompt-cache
stability (the ordering of the emitted tool list must be identical to today's).

> Caution: confirm nothing in the turn loop mutates `toolDefs` or depends on it being
> recomputed (e.g. a per-turn permission change). `Permission` is fixed for the Run, so this is
> safe — but the plan verifies by reading the full loop body.

### 3.3 P8 — debounce calibrator persistence to end of Run

**Where:** `harness/runtime/runtime.go:550-555` (`EventDone`: `calibrator.Update(...,
tokens.Estimate(...))` then `CalibratorStore.Save(...)` every round),
`harness/tokens/persist.go:85-116` (`Save` does MkdirAll+WriteFile+Rename under a global mutex).

**Problem:** every `EventDone` (every LLM call, including each tool round) does a full
disk write under a process-wide mutex, and recomputes `tokens.Estimate` (a full O(chars)
re-scan) that was already computed earlier that turn. The calibration ratio barely moves
round-to-round, so per-round persistence is wasteful.

**Fix:**
1. `EventDone` keeps `calibrator.Update(...)` (cheap, in-memory) but **removes** the inline
   `CalibratorStore.Save`. Set a `calibratorDirty` flag on the Runtime when an update occurs.
2. Persist once at end of `Run` via a deferred flush: if `calibratorDirty`, snapshot and
   `CalibratorStore.Save`. Place the deferred flush in the `Run` goroutine alongside the
   existing `defer close(r.events)` / hook defers (`runtime.go:254-263`) so it runs on every
   exit path (normal, error, abort).
3. Reuse the per-turn `tokens.Estimate`. Verified: `tokens.Estimate(msgs,
   JoinSystemPromptParts(parts), toolDefs)` is computed at three sites — `runtime.go:168` and
   `:415` (`calibrator.Adjust(...)` for the preventive-compaction estimate) and `:552` (the
   `EventDone` `calibrator.Update`). The `:415` value is computed once per turn just before the
   stream; capture it in a turn-local (e.g. `turnEstimate`) and pass that to the `EventDone`
   `Update` instead of recomputing at `:552`. (Do not disturb `:168`, which is the pre-loop
   preventive path.) This removes one full O(chars) re-scan per LLM round.

> Net: one calibrator write per Run instead of per round, and one fewer `tokens.Estimate`
> re-scan per round. The persisted ratio is at most one Run stale on a crash — acceptable,
> since it's a self-correcting estimator that re-learns on the next Run.

---

## 4. Group 2 — I/O off the critical path (P1, P5)

### 4.1 P1 — stop re-spilling oversized results every turn

**Where:** `harness/runtime/context.go:464-494` (`pruneToolResults`), `:428-441`
(`spillToolResult`), called from `runtime.go:379`.

**Problem:** the "already spilled" signal is the `spillMarker` in the *transient* per-turn
message slice (rebuilt from `session.View()` each turn), while the session entry on disk keeps
the full original output. So across turns every oversized result is re-detected as un-spilled
and **re-written to disk with its full multi-MB content** before every LLM call.

**Fix:** track spilled ToolCallIDs on the Runtime.
1. Add `spilledIDs map[string]bool` to `Runtime` (guarded by a small mutex, since
   `pruneToolResults` may run while streaming-tool kickoffs touch Runtime state; a plain map
   with a dedicated `sync.Mutex` is sufficient — or document single-goroutine access if the
   assemble path is always on the main loop goroutine; the plan verifies and picks the minimal
   safe option).
2. Thread the set into `pruneToolResults` (new parameter, e.g. `spilled map[string]bool` + its
   guard, or a small interface `markSpilled(id)/isSpilled(id)`).
3. In `pruneToolResults`, when a tool result exceeds `maxLen` and is not already marker-tagged:
   if `isSpilled(ToolCallID)` is true, **skip the disk write** and rewrite the in-memory
   message to the spill marker pointing at the deterministic path
   (`<Workspace>/.harness/spill/<SessionKey>/<ToolCallID>.txt`); otherwise write (via the
   existing `spillToolResult`), then `markSpilled(ToolCallID)`.

**Safety:** a tool result's content is immutable for a given `ToolCallID` (it's the recorded
output of one tool call), so the on-disk spill file is correct for the life of the session. The
deterministic path is already how `spillToolResult` computes it, so the marker text is identical
whether freshly written or reconstructed.

**Validation:** existing spill tests stay green. New test: assemble+prune the same oversized
result across two turns and assert the spill write happens once (e.g. via a spilled-ID set
assertion, or a write-counting hook). Output messages identical across both turns.

### 4.2 P5 — release the session lock before the disk write

**Where:** `harness/session/session.go:101-122` (`Append` holds `s.mu` across
`store.AppendEntry`), `harness/session/store.go:90-119` (`AppendEntry`, global `Store.mu`).

**Problem:** `Session.Append` holds the session `s.mu` across `store.AppendEntry` (a full
open/write/close), injecting disk latency into the session lock and blocking concurrent
`View()` / parallel tool-dispatch appends.

**Fix:** in `Session.Append`, do all in-memory mutation (id/timestamp/parent assignment,
`s.entries` append, `entryMap`, `leafID`) under `s.mu`, take a **copy** of the finalized entry,
release `s.mu`, then call `s.store.AppendEntry(s, entryCopy)`:

```go
func (s *Session) Append(entry SessionEntry) {
	s.mu.Lock()
	// ...assign ID/Timestamp/ParentID; append to s.entries; update entryMap; set leafID...
	finalized := entry // value copy after fields are set
	store := s.store
	s.mu.Unlock()
	if store != nil {
		store.AppendEntry(s, finalized)
	}
}
```

`Store.mu` continues to serialize the actual file write globally, preserving append ordering
(each caller copies under its own `s.mu` section and the store write happens in call order). The
broader "per-session-file store mutex" optimization is **out of scope** — the global store
mutex is retained.

**Validation:** existing session round-trip + DAG tests stay green. New `-race` test: concurrent
`Append` + `View()` on one session reports no race and preserves entry order.

> Ordering note: `AppendEntry` no longer runs under `s.mu`, but it still runs under `Store.mu`,
> and each `Append` call returns to its caller only after issuing the store write. Within a
> single Runtime the agent loop appends sequentially, so per-session disk order matches logical
> order. The plan adds a test asserting this.

---

## 5. Group 3 — Felix-side (P4, P6)

### 5.1 P4 — cache the per-turn Runtime prompt inputs

**Where:** `internal/agent/agent.go:117-209` (`BuildRuntimeForAgent`), `:127` + `:199`
(double `buildConfigSummary`), `:128` (`loadAgentMemoryFiles` — disk), `:270`
(`loadAgentMemoryFiles`), called per turn from `internal/chatexec/chatexec.go:172`.

**Problem:** `BuildRuntimeForAgent` runs per chat message and recomputes invariants:
`loadAgentMemoryFiles` (up to 4 `os.ReadFile`), `buildConfigSummary` (twice — once at `:127`,
once inside `MakeSubagentFactory` at `:199`), `FormatIndex()` for skills+memory, the static
prompt concatenation, and a `CalibratorStore.Load`. All invariant until config hot-reload or a
workspace memory-file edit.

**Fix:**
1. **Generation-keyed cache** in the `agent` package: a `sync.Map` (or mutex-guarded map) keyed
   by `agentID`, holding `{gen int64, configSummary, memoryFiles, staticPrompt}`. A
   package-level `atomic.Int64 configGeneration` is the validity token.
2. On `BuildRuntimeForAgent`: read `gen := configGeneration.Load()`; if the cached entry for
   `agentID` matches `gen`, reuse its parts; else recompute (the disk reads + concat) and store
   `{gen, ...}`.
3. The fsnotify reload callback in `internal/startup/startup.go` calls
   `agent.BumpConfigGeneration()` (which does `configGeneration.Add(1)`) so the next build
   recomputes. This piggybacks on the existing reload callback that already rebuilds providers /
   permission / allowlists.
4. **Dedupe `buildConfigSummary`**: compute it once in `BuildRuntimeForAgent` and pass the
   string into `MakeSubagentFactory` (add a parameter) instead of recomputing at `:199`.
   `MakeSubagentFactory` has two real Felix call sites that must be updated for the new
   signature: `internal/chatexec/chatexec.go:190` and `internal/startup/startup.go:891`. (The
   `hrt.MakeSubagentFactory` call at `agent.go:208` is the *harness* function — different, not
   touched.) When the cache is in play, the cached `configSummary` is the value passed in, so
   the subagent factory and the parent Runtime share one computation.

> Invalidation correctness: the cache key is `(agentID, gen)`; any config edit bumps `gen`
> globally, so a stale entry can never be served after a reload. A workspace memory-file edit
> *without* a config reload is not auto-invalidated — this matches today's behavior only if
> memory files are re-read per turn; since we're now caching them, the plan notes this as an
> accepted trade-off (memory-file edits take effect on next reload/restart) OR, if cheap,
> stats the memory files' mtimes as part of the key. **Decision:** accept the trade-off (config
> reload or restart picks up memory-file edits); document it. This keeps the cache simple and
> matches how skills/memory indexes are already treated as reload-scoped.

### 5.2 P6 — parallelize startup

**Where:** `internal/startup/startup.go:429` (`localSup.Start` → `waitReady` blocks up to 60s),
`:560` (`mcp.NewManager` blocks up to 30s), HTTP server constructed/started after both.

**Problem:** `localSup.Start` polls Ollama `/api/version` for up to 60s, then MCP connect runs
serially for up to 30s, all before the HTTP server binds — `/health` is dead for that window.
Nothing downstream needs Ollama *ready*, only its bound port (known immediately after spawn).

**Fix:**
1. Split the Ollama supervisor start: spawn the process + capture the bound port synchronously
   (milliseconds), and move `waitReady` + model warmups into a goroutine. The `local` provider
   block (which needs only the port) is injected synchronously as today.
2. Run `mcp.NewManager` concurrently with provider/session/skill initialization using a
   `sync.WaitGroup` (or `golang.org/x/sync/errgroup` if already a dependency; else WaitGroup +
   captured error). Join before the tool-registry wiring that consumes the MCP manager.
3. The HTTP server starts after the join (unchanged health contract — no "starting" state).

> This must preserve the exact wiring order *dependencies*: MCP tools must be registered before
> the permission checker's allowlist auto-add (which reads MCP tool names — see round 2's R2
> work), and the cron daemon must start after the scheduler is built. The plan maps the
> dependency DAG and only parallelizes independent branches. The bundled-Ollama port injection
> stays on the synchronous path because provider init reads it.

**Validation:** existing startup tests stay green. The parallelized path must produce the same
final component graph; a test asserts MCP tools are registered and allowlists applied before the
server is returned.

---

## 6. Testing strategy

- **Behavior-preservation (all):** run the full harness + felix suites under `go test -race`;
  every existing test stays green with no output changes.
- **P2:** orphan-injection test green; add an assembly test asserting identical `[]llm.Message`
  for a representative history (incl. orphaned tool calls).
- **P3:** assert the normalized tool-def list is identical to pre-change for a fixed tool set;
  optional benchmark showing the per-turn cost moved out of the loop.
- **P8:** test that a multi-round Run persists the calibrator exactly once (write-counting
  `CalibratorStore` or a spy); ratio after Run equals the pre-change ratio.
- **P1:** two-turn test over the same oversized result asserts one spill write; messages
  identical both turns.
- **P5:** `-race` concurrent Append/View test; entry order preserved; round-trip unchanged.
- **P4:** cache-hit test (same gen ⇒ no recompute, e.g. via a read-counting memory loader);
  invalidation test (`BumpConfigGeneration` ⇒ recompute); subagent factory gets the shared
  summary.
- **P6:** startup test green; assert final wiring (MCP tools registered, allowlists applied)
  before server return; no goroutine leak (the background Ollama-ready goroutine must exit on
  shutdown ctx).

---

## 7. Files touched

**harness:**
- `runtime/context.go` — P2 (delete `resultIDs` + mid-loop inject); P1 (`pruneToolResults`
  skips write when spilled).
- `runtime/runtime.go` — P3 (hoist tool-def normalize); P8 (in-memory update + deferred flush);
  P1 (`spilledIDs` field + thread into `pruneToolResults`).
- `session/session.go` — P5 (release `s.mu` before store write).
- `tokens/persist.go` — P8 (no signature change expected; flush is driven from runtime).

**felix:**
- `internal/agent/agent.go` — P4 (generation cache, `BumpConfigGeneration`, dedupe
  `buildConfigSummary`, `MakeSubagentFactory` gets summary param).
- `internal/startup/startup.go` — P6 (background Ollama-ready, parallel MCP); P4 (call
  `agent.BumpConfigGeneration()` in the reload callback).

**New tests:** harness `runtime/*_test.go` (P1, P2, P3, P8), `session/*_test.go` (P5);
felix `internal/agent/*_test.go` (P4), `internal/startup/*_test.go` (P6).

---

## 8. Deferred (explicitly not in this round)

| Item | Why |
|------|-----|
| P7 memory embedding/BM25 + search-intent decision | Needs wire-up-or-delete judgment call |
| Per-session-file store mutex (P5 extension) | Global store mutex retained; only matters under many concurrent sessions |
| Early HTTP server + "starting" health state (P6 extension) | Health-contract change not needed; join-before-serve is enough |
| Memory-file-edit cache invalidation without reload (P4) | Accepted trade-off: reload/restart picks up edits |
| R7–R10, remaining security (S2/S3/N4/…), L-series | Later execution-order steps |
