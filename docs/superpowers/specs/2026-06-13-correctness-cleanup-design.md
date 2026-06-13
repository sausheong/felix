# Correctness Cleanup — Design

**Date:** 2026-06-13
**Status:** Design (pending implementation plan)
**Scope:** Execution-order step 5 from `optimisation.md` — correctness cleanup.
Findings: R9 (cortex auto-recall wire-up), R7 (provider stream correctness ×4), R8 + R10 + L6
(memory correctness), N9/N10/N11/N12 (small cleanups).
**Repos touched:** `harness` and `felix` (wired via `go.mod replace`).

---

## 1. Goal & constraint

Rounds 1–4 cleared the security tiers (steps 1–2), the process-wedging reliability tier (step 3),
and the performance tier (step 4). This round takes step 5: correctness cleanup — the remaining
behavior bugs and one deliberate feature wire-up.

**Constraint — observable improvement without breaking existing flows.** Most fixes correct
behavior; the bar is that no existing flow breaks:
- **R7** must preserve *content* — only emission ordering, event count, and allocation change.
- **R9** *adds* auto-recall, but must be a no-op when cortex is disabled (the default), and
  bounded (the harness already caps recall at 800ms).
- **R8/R10** fix silent data loss and data races.

Each finding ships paired tests (the bug's symptom + the corrected behavior). All validated under
`go test -race` with existing suites kept green.

All findings re-verified against `main` on 2026-06-13 (post rounds 1–4). Two intent decisions
were made during brainstorming:
- **R9: wire it up** (enable auto-recall), not delete the dead wiring.
- **P7: defer** (the memory vector/embedding search wire-or-delete is its own round; only R10's
  concurrency/atomicity bugs are fixed here).

---

## 2. Architecture: four groups, one spec

| Group | Findings | Repo | Risk |
|-------|----------|------|------|
| 1. Cortex auto-recall wire-up | R9 | felix | medium-high (new code on the turn hot path) |
| 2. Provider stream correctness | R7 (×4) | harness | medium (all 4 provider stream loops) |
| 3. Memory correctness | R8, R10, L6 | felix | medium (concurrency + atomicity) |
| 4. Small cleanups | N9, N10, N11, N12 | both | low (defensive / mechanical) |

R9 is its own group: it is the only finding that *adds* a feature, it is on the hot path, and it
has the most design surface. The other three are bug-fixes.

**Adjacent siblings pulled in** (cheap when the file is already open): **L6** (UTF-8-safe
truncation, same `memory.go` lines as R8/R10) and **N10** (`DeleteRun` `[:0]` aliasing, identical
to N9). Everything else in the L-series / step 5 stays deferred.

---

## 3. Group 1 — Cortex auto-recall wire-up (R9)

### 3.1 Current state

- The harness runtime already implements the full KG pathway (`harness/runtime/runtime.go:340-412`):
  if `r.KG != nil`, it calls `KG.ShouldRecall(userMsg)`; if true, runs `KG.Recall` in a goroutine
  and waits with an **800ms cap** (`runtime.go:408-412`), injects the hint into the prompt; and
  `defer`s `KG.Ingest(thread)` gated on `IngestSource == "" || "chat"` (`runtime.go:342`).
- The harness interface (`harness/runtime/builder.go:34`, `types.go`):
  ```go
  type KnowledgeGraph interface {
      ShouldRecall(query string) bool
      Recall(ctx context.Context, query string) string
      Ingest(ctx context.Context, thread []Message)
  }
  // RuntimeDeps.KGFn func(model string) KnowledgeGraph   // builder.go:34, nil-safe
  ```
- **Felix never sets `KGFn`.** It sets `CortexFn func(model string) *cortex.Cortex` in two places
  (`internal/chatexec/chatexec.go:168`, `internal/startup/startup.go:826`) but reads it in **zero**
  places. `BuildDynamicSystemPromptSuffix` (`internal/agent/agent.go:64`) is a stub returning `""`
  with no callers.
- The cortex adapter (`internal/cortex/cortex.go:124-160`) **intentionally omits**
  `cortex.WithLLM` because the library only uses the LLM in `Recall.decomposeQuery` (a 1–3s
  round-trip), which would blow the 800ms budget; the keyword+memory recall fallback is fast and
  near-equivalent (documented at `cortex.go:138-144`).

### 3.2 Fix

**New file `internal/cortex/kg.go`** — a `cortexKG` adapter implementing `KnowledgeGraph` over
`*cortex.Cortex`:

```go
package cortex

import (
    "context"
    "fmt"
    "strings"

    "github.com/sausheong/cortex"
    hrt "github.com/sausheong/harness/runtime"
)

// cortexKG adapts a *cortex.Cortex to the harness KnowledgeGraph interface,
// enabling the runtime's bounded (800ms) auto-recall + deferred-async ingest
// pathway. Recall deliberately relies on cortex's keyword+memory fallback (no
// WithLLM/decomposeQuery) so it stays within the recall budget — see the note
// in build() about not calling cortex.WithLLM.
type cortexKG struct {
    cx *cortex.Cortex
}

// NewKnowledgeGraph wraps cx as a harness KnowledgeGraph. Returns nil when cx
// is nil so callers can pass the result straight to KGFn (nil disables KG).
func NewKnowledgeGraph(cx *cortex.Cortex) hrt.KnowledgeGraph {
    if cx == nil {
        return nil
    }
    return &cortexKG{cx: cx}
}

const minRecallQueryLen = 8 // skip trivial/greeting queries

func (k *cortexKG) ShouldRecall(query string) bool {
    return len(strings.TrimSpace(query)) >= minRecallQueryLen
}

func (k *cortexKG) Recall(ctx context.Context, query string) string {
    results, err := k.cx.Recall(ctx, query)
    if err != nil || len(results) == 0 {
        return ""
    }
    return formatRecall(results) // shared with RecallTool; see 3.3
}

func (k *cortexKG) Ingest(ctx context.Context, thread []hrt.Message) {
    if len(thread) == 0 {
        return
    }
    var b strings.Builder
    for _, m := range thread {
        fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
    }
    // Best-effort; ingest failures must not surface to the chat.
    _ = k.cx.Remember(ctx, strings.TrimSpace(b.String()))
}
```

> The exact `cortex.Recall` return type (`[]cortex.Result`) and `Remember` signature
> (`Remember(ctx, content string, opts ...RememberOption) error`) are confirmed against the
> pinned cortex version; the plan double-checks field names when writing `formatRecall`.

### 3.3 Recall formatter

`internal/tools/cortextools/recall.go` already has an unexported `formatRecallResults(results
[]cortex.Result) string` (called by `RecallTool.Execute`). The KG adapter needs the same
rendering. **Decision:** the KG adapter's `Recall` lives in `internal/cortex` and renders results
with its **own** small formatter in `kg.go` (the recall hint for the prompt can be a compact
form — id/title/snippet per result — and need not be byte-identical to the tool's user-facing
output). Do **not** try to share across packages: `internal/cortex` must not import
`internal/tools/cortextools` (wrong dependency direction; the tools package wraps cortex, not the
reverse), and exporting the tool's formatter into a shared spot adds coupling for no real benefit.
A self-contained `formatRecall` in `kg.go` keeps the adapter cohesive. Its output format is
covered by the kg_test; the tool's existing formatter is untouched.

### 3.4 Wiring (`internal/agent/agent.go`)

Felix builds `hrt.RuntimeDeps` in **two** places: `agent.go:178` (the main
`BuildRuntimeForAgent`) and `agent.go:250` (inside `MakeSubagentFactory`). **Set `KGFn` only at
`:178`** — the main runtime gets auto-recall + ingest. **Leave `KGFn` nil at `:250`** (subagents):
subagents should not auto-recall or ingest into the knowledge graph, matching the harness's own
precedent (`runtime/review.go:304` "KGFn nil — reviewer never recalls or ingests"). This keeps
ingest single-sourced from the top-level chat turn and avoids subagents polluting the graph.

1. **Set `KGFn` at `agent.go:178`** from the existing cortex resolution. Today `CortexFn
   func(model string) *cortex.Cortex` is carried on Felix's deps struct (`agent.go:92`) and
   populated at `startup.go:826` / `chatexec.go:168`. Build the harness `KGFn` as a closure over
   that same resolver:
   ```go
   hdeps.KGFn = func(model string) hrt.KnowledgeGraph {
       if deps.CortexFn == nil {
           return nil
       }
       return cortexadapter.NewKnowledgeGraph(deps.CortexFn(model))
   }
   ```
   `NewKnowledgeGraph(nil)` returns nil, so a disabled/unconfigured cortex yields a nil KG and the
   harness skips the whole block — **turns are byte-identical to today when cortex is off.**
2. **Resolve the `CortexFn` redundancy:** `CortexFn` stops being dead — it is now read by the
   `KGFn` closure. Keep the field and its two set-sites; they feed both the explicit cortex tools
   (via the chat overlay) and auto-recall (via KGFn) from one resolution path. No dead field
   remains.
3. **Delete the `BuildDynamicSystemPromptSuffix` stub** (`agent.go:64-71`) and its re-export — it
   has no callers and the harness now injects the recall hint itself. Confirm zero callers
   (`grep -rn BuildDynamicSystemPromptSuffix`) before deleting.

### 3.5 Hard constraints

- **Do NOT add `cortex.WithLLM`.** The 800ms budget depends on `Recall` skipping `decomposeQuery`.
  If recall ever exceeds budget, the harness times it out (`runtime.go:411`) and proceeds — no
  correctness impact, just a missed hint.
- **Cortex disabled → no behavior change.** This is the load-bearing guarantee; the wiring test
  asserts `KGFn` returns nil when `CortexFn` is nil/returns nil.
- **Ingest stays deferred-async + chat-only** — already enforced by the harness
  (`IngestSource` gate); Felix sets `IngestSource` for cron runs to a non-"chat" value already
  (verify during impl; if not, that is out of scope — the harness default `""` also ingests, so
  confirm cron runs set IngestSource to exclude themselves, matching the existing design note in
  CLAUDE.md "cron runs do not ingest").

### 3.6 Testing

- `internal/cortex/kg_test.go`: `ShouldRecall` false for short/empty, true for real queries;
  `Recall` returns "" on error/empty and formatted text on results (fake/stub cortex or a
  temp SQLite cortex); `Recall` honors ctx cancellation; `Ingest` calls `Remember` with the
  rendered thread; `NewKnowledgeGraph(nil) == nil`.
- `internal/agent` wiring test: `KGFn` is nil-yielding when `CortexFn` is nil; non-nil adapter
  when `CortexFn` returns a cortex.
- Rely on the harness `runtime/runturn_kg_test.go` for the 800ms/ingest mechanics (already tests
  the runtime's KG consumption with a fake) — do not duplicate.

---

## 4. Group 2 — Provider stream correctness (R7)

Four independent sub-fixes. All preserve final content; only ordering, event count, and
allocation change.

### 4.1 R7a — deterministic tool-call emission order (OpenAI + Qwen)

**Where:** `harness/providers/openai/openai.go:347` (`for _, tc := range toolCalls` over
`map[int]*pendingTC` declared at `:279`); the same pattern in `harness/providers/qwen/qwen.go`
(`toolCalls := make(map[int]*pendingTC)` at `:225`).

**Problem:** Go randomizes map iteration, so completed tool calls emit in nondeterministic order,
defeating the codebase's prompt-cache determinism on the next turn (the assistant message's
tool_use block order varies).

**Fix:** emit in ascending index order. The map key is the index, so:
```go
// find max index, iterate 0..max, emit present entries in order
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
    events <- llm.ChatEvent{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
        ID: tc.id, Name: tc.name, Input: json.RawMessage(tc.argsJSON), // or .String() after R7c
    }}
}
```
Tool-call content is unchanged; only emission order becomes stable.

### 4.2 R7b — Gemini single EventDone

**Where:** `harness/providers/gemini/gemini.go:191-199` emits `EventDone` with usage on every
chunk that carries `UsageMetadata`; the loop also has a terminal `EventDone` at `:243`.

**Problem:** one calibrator update (and, pre-round-3, one disk write) per chunk, with a skewed
ratio.

**Fix:** buffer the latest usage in a `lastUsage *llm.Usage` local (mirroring OpenAI's
`lastUsage` at `openai.go:363`). In the chunk loop, when `UsageMetadata != nil`, update
`lastUsage` instead of emitting. Emit exactly one `EventDone` (with `lastUsage`) after the
iterator finishes — fold into the existing terminal `EventDone` at `:243`. Net: one calibrator
update per turn carrying the final cumulative usage.

### 4.3 R7c — Anthropic error extraction + O(n) accumulation

**Part 1 — error extraction.** `harness/providers/anthropic/anthropic.go:283` has a
`case "error":` that does nothing — an in-band error event yields a silent empty success. **Fix:**
extract the error payload from the stream event and emit `llm.ChatEvent{Type: llm.EventError,
Err: fmt.Errorf(...)}` so the runtime's existing error handling (incl. round-2 R1 recovery) sees
it. The plan reads the SDK event shape to extract the message/type fields correctly.

**Part 2 — O(n²) → O(n) accumulation.** Argument/JSON accumulation via `+=`:
- `anthropic.go:223` `pendingTools[...].inputJSON += delta.PartialJSON`
- `openai.go:340` `pending.argsJSON += tc.Function.Arguments`
- `qwen.go` equivalent
**Fix:** change the accumulator field on the per-provider pending struct from `string` to
`strings.Builder`; `WriteString` per delta; materialize once at emission (`.String()`). Same final
bytes, O(n). The pending struct is a provider-local type, so the change is contained. Update the
emission sites (R7a's emit uses `.String()` instead of the string field).

### 4.4 Testing

Provider-package unit tests against synthetic streams (no live API):
- R7a: tool calls arriving at indices 0,1,2 out of order → assert index-ordered emission
  (OpenAI + Qwen).
- R7b: Gemini stream with usage on multiple chunks → assert exactly one EventDone with final
  cumulative usage.
- R7c-error: Anthropic `error` event → assert an EventError with the payload surfaces.
- R7c-builder: large multi-delta argument stream → assert accumulated args equal the
  concatenation (behavior-identical).

Reuse each provider's existing test helpers / synthetic-stream scaffolding.

---

## 5. Group 3 — Memory correctness (R8, R10, L6)

All in `internal/memory/memory.go`.

### 5.1 R8 — index shows newest, not oldest

**Where:** `FormatIndex` (`:415-450`) sorts by ID ascending (`sort.Slice ... entries[i].ID <
entries[j].ID`, `:427`) then caps at `MaxMemoryIndexEntries = 200` (`:403`).

**Problem:** agent IDs are `agent-<unix-ms>-…` (ascending = oldest-first), so past 200 entries the
**newest** memories silently fall off the only discovery surface; writes "succeed" but become
undiscoverable.

**Fix:**
1. Sort by `ModTime` **descending** before capping (entries carry `ModTime` — `:114`, `:252`),
   secondary-sort by ID descending for a stable tie-break (the index sits in the cached static
   prompt — ordering must be deterministic across turns for the same entry set).
2. Append a `"\n…and N more (use the memory list tool)\n"` line when `len(entries) > cap`.
3. `slog.Warn` once when the cap is exceeded (id count).

> Prompt-cache note: ModTime-descending ordering is stable as long as the entry set + their
> ModTimes are stable across turns, which they are within a session. A new save changes the set
> (and the cache prefix) anyway — acceptable and unchanged from today's behavior on write.

### 5.2 R10 — atomic write + race-free vector add + bounded goroutines

**Where:** `Save` (`:236-278`).

**Problem A — non-atomic write:** `os.WriteFile(path, …, 0o600)` at `:243` truncates in place,
*before* `m.mu` (`:255`) — a crash mid-write leaves a torn `.md`, and two concurrent saves to the
same id can interleave disk vs. `m.entries[id]`.

**Fix A:** write via tmp-file + `os.Rename` (atomic). Prefer reusing an existing atomic-write
helper; `internal/config/writefile.go` has `WriteFileAtomic`, but importing `config` from
`memory` may introduce an undesirable dependency. **Decision:** add a small package-local
`writeFileAtomic(path string, data []byte, perm os.FileMode) error` in the memory package
(tmp in the same dir + `os.Rename`) to avoid the cross-package import. ~8 lines.

**Problem B — unlocked `m.vecColl` read in goroutine:** the `go func()` at `:268` dereferences
`m.vecColl` (`:271`) after `Save` returns and `defer m.mu.Unlock()` fires, racing `Load`/`Delete`
which reassign `m.vecColl` (`:81`, `:227`).

**Fix B:** capture `coll := m.vecColl` **under the lock** (before spawning the goroutine) and have
the goroutine use the captured `coll`, never `m.vecColl`:
```go
if coll := m.vecColl; coll != nil {
    m.vecSem <- struct{}{} // bounded; see Fix C
    go func() {
        defer func() { <-m.vecSem }()
        doc := chromem.Document{ID: id, Content: content}
        if err := coll.AddDocument(context.Background(), doc); err != nil {
            slog.Warn("vector index add failed", "id", id, "error", err)
        }
    }()
}
```

**Problem C — unbounded goroutines:** one network call per save, no ceiling.

**Fix C:** add a buffered semaphore `vecSem chan struct{}` (cap 4) to `Manager`, init in the
constructor. Acquire before spawn, release in the goroutine's defer. (Acquire is a blocking send —
bounded to 4 concurrent embed calls; acceptable since Save is not on the latency-critical chat
path the way recall is.)

> Verify in the plan: `Manager` construction site(s) to add `vecSem` init; the `Delete` path
> (`:345-366`) which also touches `m.vecColl` — confirm it captures under lock too (apply the same
> capture pattern if it has the same goroutine shape).

### 5.3 L6 — UTF-8-safe truncation

**Where:** content/description truncation does byte-slicing — `content[:2000]` (`:389`) and the
`indexDescription` ~120-char trim — which can split a multibyte rune, injecting invalid UTF-8 into
the system prompt.

**Fix:** truncate on a rune boundary. Simplest correct form: convert to `[]rune`, slice, convert
back (`r := []rune(s); if len(r) > n { s = string(r[:n]) }`) for the short description; for the
2000-byte content cap, back off to the nearest rune start (`for len > 0 && !utf8.RuneStart(s[len])
{ len-- }`) or use the rune-slice form. Apply consistently to both truncation sites in the file.

### 5.4 Testing

- R8: save 205 entries with increasing IDs/ModTimes → assert FormatIndex lists the **newest** 200,
  includes the "…and N more" line, and a warning fired (capture via a test slog handler or assert
  the count line).
- R10-atomic: assert `Save` round-trips content and that a tmp file is used / no partial file on a
  simulated rename path (at minimum: content written and reloadable; helper unit-tested for
  atomicity).
- R10-race: `go test -race` with concurrent `Save` + `Load`/`Delete` → no race on `m.vecColl`.
- R10-bound: not directly asserted (timing), but the semaphore presence is covered by the
  compile + the race test.
- L6: truncate a string whose rune crosses the boundary → assert `utf8.ValidString` on the result.

---

## 6. Group 4 — Small cleanups (N9, N10, N11, N12)

### 6.1 N9 — overlay slice-aliasing (`internal/chatexec/overlay.go:71,95,118`)

`ToolDefs`/`Names` filter with `defs[:0]` / `names[:0]`, reusing the backing array of the slice
returned by `e.Base.ToolDefs()`/`Base.Names()`. Safe today only because `Registry.ToolDefs`
returns a fresh slice; fragile if any `Executor.ToolDefs` ever returns a shared slice.

**Fix:** allocate instead of alias: `filtered := make([]llm.ToolDef, 0, len(defs))` (and the
`[]string` equivalent for Names). Behavior identical; removes the landmine.

### 6.2 N10 — same aliasing in `DeleteRun` (`internal/gateway/runs/registry.go:489-495`)

`idx.Runs[:0]` filters in place over the just-loaded index slice — same pattern, safe only because
`loadIndex` returns a fresh decode.

**Fix:** allocate a new slice rather than `[:0]`-alias.

### 6.3 N11 — HTML entity decoding (`harness/tools/web/websearch.go:256-276`)

`cleanHTMLText` hand-decodes only 6 entities; numeric (`&#8217;`) and most named entities pass
through raw.

**Fix:** keep the tag-strip loop; replace the six manual `strings.ReplaceAll` entity calls with a
single `html.UnescapeString(...)` (stdlib `html` package) on the tag-stripped result. Correct and
simpler. Add `"html"` to imports.

### 6.4 N12 — read_file/edit_file honor ctx (`harness/tools/file/readfile.go:75`, `editfile.go:55`)

Both `Execute(_ context.Context, …)` ignore ctx — a large synchronous read can't be cancelled.

**Fix:** name the parameter `ctx context.Context` and add an early `if err := ctx.Err(); err !=
nil { return tool.ToolResult{Error: err.Error()}, nil }` after input validation, before the size
check / open (coexisting with round-4's N5 stat+LimitReader). Minimal cancellation honor, not full
streaming cancellation.

### 6.5 Testing

- N9/N10: mutate the returned slice / call twice → assert independence (Base's slice unmodified;
  second call unaffected).
- N11: `cleanHTMLText("&#8217;tis &amp; so")` → decodes the numeric entity and `&amp;`.
- N12: `Execute` with a pre-cancelled ctx → returns the ctx error without reading.

---

## 7. Testing strategy (cross-cutting)

- **Paired tests per finding**: symptom + corrected behavior.
- **Full suites green** under `go test -race` on both repos; after each harness change,
  `cd felix && go build ./...`.
- **No-break bar**: R7 content-identical; R9 no-op when cortex disabled (the load-bearing test);
  R8/R10 fix loss/races without changing the happy path.
- **Final adversarial review subagent** after all tasks (caught 3 real bugs in round 4).

---

## 8. Files touched

**harness:**
- `providers/openai/openai.go` — R7a (index-order emit), R7c (Builder).
- `providers/qwen/qwen.go` — R7a, R7c.
- `providers/gemini/gemini.go` — R7b (single EventDone).
- `providers/anthropic/anthropic.go` — R7c (error extract + Builder).
- `tools/web/websearch.go` — N11.
- `tools/file/readfile.go`, `tools/file/editfile.go` — N12.
- provider + tool `*_test.go`.

**felix:**
- `internal/cortex/kg.go` (**new**), `internal/cortex/cortex.go` — R9 adapter + `FormatRecall`.
- `internal/tools/cortextools/recall.go` — call shared `FormatRecall` (R9).
- `internal/agent/agent.go` — R9 wiring (set `KGFn`, delete stub, read `CortexFn`).
- `internal/memory/memory.go` — R8, R10, L6.
- `internal/chatexec/overlay.go` — N9.
- `internal/gateway/runs/registry.go` — N10.
- new/updated `*_test.go`.

---

## 9. Deferred (explicitly not in this round)

| Item | Why |
|------|-----|
| P7 memory vector/embedding search (wire `Manager.Search` to a `search_memory` tool, or delete the embedder/vector machinery) | Intent-deferred this round; its own focused round. Only R10's memory-write bugs are fixed now. |
| Deeper provider refactors beyond the 4 named R7 fixes | Out of scope. |
| L1–L5, L7–L11; G6–G9 | Opportunistic later. |
| Per-session store mutex; remaining items from earlier steps | Already deferred in prior rounds. |
