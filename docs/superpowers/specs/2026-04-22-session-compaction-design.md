# Session Compaction — Design

**Date:** 2026-04-22
**Status:** Draft (awaiting user review)
**Author:** Brainstorming session with Felix maintainer

---

## Problem

Felix's current "long-conversation" handling is just `pruneToolResults(msgs, maxToolResultLen=10000)` in `internal/agent/context.go` — tool results over 10k chars are truncated; nothing else changes. Long Telegram or CLI sessions will eventually exceed the model's context window and either error from the provider or silently degrade.

This is **preventive work**: Felix users haven't hit it yet. The goal is to make compaction invisible — it should "just work" when needed and stay out of the way otherwise.

## Goals

- Auto-compact long sessions before they hit context limits.
- Recover gracefully if a context-overflow error happens despite the preventive check.
- Use **bundled Ollama** for the compaction call (free, local, no surprise API charges).
- Preserve Felix's **append-only JSONL** session model — never rewrite history on disk.
- Zero new settings UI surface initially. One on/off knob in `felix.json5` for power users; sensible defaults for everyone else.
- No regression: short sessions behave identically to today.

## Non-goals

- Pre-compaction memory flush (OpenClaw-style silent turn that asks the agent to write notes to `MEMORY.md`). Felix already auto-ingests every turn into the **Cortex** knowledge graph; pre-flush would duplicate that work and cost a primary-model turn.
- Pluggable compaction providers / plugin SDK. Felix is a self-contained app, not a platform.
- Per-provider tokenizers. Char-based estimation calibrated by provider usage stats is good enough for the threshold check.
- Hierarchical multi-tier compaction. One code path is enough.
- Settings UI for the threshold or split-point K initially. Both are tunable in config but no UI knobs.

## Approach

**Approach A — single-shot summary.** When the threshold is hit, summarize everything before the last K user turns into one summary entry; preserve the last K turns verbatim. Each subsequent compaction re-summarizes (older summary + new middle turns) into a new summary, again keeping last K. Predictable, single code path, easy to debug.

(Rejected alternatives: rolling running-summary — accumulates drift; hierarchical Ollama+primary — over-engineered for the scope.)

---

## Section 1 — Trigger logic + token estimation

### When compaction fires (priority order)

1. **Manual** — `/compact` slash command in CLI chat (with optional `focus on …` instructions); "Compact now" button in tray UI; `chat.compact` WebSocket RPC.
2. **Reactive** — agent runtime catches a context-overflow error from any provider. Compacts, then retries the same turn **once**. If the retry also overflows, surface the error to the user.

   Provider error signatures to recognize (string-match on error messages):
   - Anthropic: `request_too_large`, `context length exceeded`, `input is too long`
   - OpenAI: `context_length_exceeded`, `maximum context length`
   - Gemini: `input token count exceeds`, `request payload size exceeds`
   - Ollama: `context length exceeded`

   These will live as a slice of substrings in `internal/compaction/overflow.go`. New providers add their signatures here.

3. **Preventive** — at the start of every turn, before calling the LLM, check estimated input tokens against `0.6 × modelContextWindow`. If over, compact first.

### Token estimation (`internal/tokens/`)

- **Estimator**: `Estimate(messages []llm.Message, systemPrompt string, tools []llm.ToolDef) int` returns a char-based estimate (`totalChars / 4`).
- **Calibration**: each completion's `usage.input_tokens` (when reported) is compared against the estimate that was sent. The per-session ratio `actual/estimated` is kept as a running average (initial 1.0). Future estimates are multiplied by it, so they self-correct.
- **Model context windows**: a static map keyed by `provider/model-id`. Felix only supports 4 providers — limits are stable and well-known.

  ```
  anthropic/claude-*-sonnet → 200000
  anthropic/claude-*-opus   → 200000
  anthropic/claude-*-haiku  → 200000
  openai/gpt-4o*            → 128000
  openai/gpt-4-turbo        → 128000
  google/gemini-1.5-pro     → 2000000
  google/gemini-1.5-flash   → 1000000
  ollama/*                  → read from /api/show at startup; cached
  unknown                   → 32000 (conservative fallback)
  ```

- **Threshold**: `0.6` as a constant in `internal/compaction/`. Leaves ~40% of the window for response + new tool calls + a few more user turns before the next compaction. Tunable via `felix.json5` (`agent.compaction.threshold`) but not exposed in any UI.

---

## Section 2 — Compaction execution + storage

### Storage model (append-only)

Add `EntryTypeCompaction = "compaction"` to `internal/session/session.go`. JSONL on disk is **never rewritten**. Compaction appends one new entry at the current leaf, just like any other session entry.

```go
type CompactionData struct {
    Summary               string `json:"summary"`
    RangeStartID          string `json:"range_start_id"`
    RangeEndID            string `json:"range_end_id"`
    Model                 string `json:"model"`              // e.g. "ollama/qwen2.5:3b-instruct"
    TokensBefore          int    `json:"tokens_before"`
    TokensEstimatedAfter  int    `json:"tokens_estimated_after"`
    TurnsCompacted        int    `json:"turns_compacted"`
}
```

### `Session.View()` — what assembleMessages reads

New method `View() []SessionEntry` on `*Session`:

1. Walk the current branch from leaf back to root via `ParentID` (the same single-path traversal that `History()` already does — DAG branching from `/branch` doesn't change this; we always assemble the active leaf's lineage).
2. Find the **most recent** compaction entry along that path.
3. If found: emit a synthetic leading user message `"[Previous conversation summary]\n\n<summary text>"`, then all entries chronologically *after* that compaction.
4. If not found: behaves identically to `History()`.

Multiple compactions stack naturally — only the most recent matters for the assembled view. Older compactions remain in the JSONL for forensic / debugging value.

`internal/agent/context.go::assembleMessages` is updated to read from `Session.View()` instead of `Session.History()`. No other call sites change.

### Split-point algorithm

The cutoff between "summarized" and "preserved" must land at a turn boundary so a `tool_call` is never separated from its `tool_result`.

Algorithm:
1. Walk entries from leaf back to root (or to the most recent compaction).
2. Count user messages encountered.
3. When count reaches **K=4**, the *next* user message we encounter (the (K+1)th from the end) is the cutoff.
4. Everything from that user message *forward* is preserved verbatim. Everything before is the to-be-compacted range.
5. If total user messages along the path < K (rare but possible), **refuse compaction** even when the threshold is hit — log warn and proceed with the original (oversized) request. The provider may then error and reactive path takes over.

A user message is always a clean boundary by construction in Felix (the agent runtime appends user messages first, then assistant + tool entries until the next user turn). Splitting *between* turns never orphans a tool pair.

`K=4` is a constant in `internal/compaction/`. Tunable via `felix.json5` (`agent.compaction.preserveTurns`).

### Compaction execution flow

1. Call `Splitter.Split(session.View(), K)` → returns `(toCompact, toPreserve []SessionEntry)`.
2. Call `Summarizer.Summarize(ctx, toCompact, opts)`:
   - Serialize `toCompact` as a labeled transcript: `USER: …\nASSISTANT: …\nTOOL_CALL[bash]: <input>\nTOOL_RESULT: <output>\n…`.
   - Tool results in the to-be-compacted range are **not** further truncated — the summarizer needs full context.
   - Build prompt (template below) and call bundled Ollama via the existing `llm.Provider` interface (with `provider="ollama"`, `model=` the configured compaction model).
3. Append a `SessionEntry{Type: EntryTypeCompaction, Data: CompactionData{...}}` to the session.
4. Emit `AgentEvent{Type: EventCompactionDone, ...}` so the CLI/tray UI can render `🧹 Compacted 24 turns → ~1.2k tokens`.

### Summarization prompt

```
You are summarizing an AI assistant's conversation so it can continue past
the context window. Preserve: facts established, decisions made, file paths,
code snippets discussed, error messages encountered, ongoing tasks, the
user's stated preferences and constraints. Drop: chitchat, intermediate
tool exploration, retried-then-abandoned approaches.

Output only the summary. No preamble. No "Here is the summary:". No closing
remarks.

[Additional instructions from the user, if /compact had a focus argument:]
Additional focus: <text>

CONVERSATION TO SUMMARIZE:
<labeled transcript>
```

### Default compaction model

`ollama/qwen2.5:3b-instruct`.

Rationale: small (~2GB), fast, in the Felix curated model list shown during onboarding (so most users will already have it pulled), good at following structured instructions. `gemma2:2b` is a viable alternative — both can be the default with similar results.

Note: Felix bundles the **Ollama daemon binary** but not any specific model — models are downloaded on demand via `felix model pull`. If the configured compaction model isn't on disk when compaction triggers, the "model missing" tray nudge fires (see error handling) and the compaction is skipped that turn. Felix will not auto-pull the model in the background to avoid surprise multi-GB downloads.

Configurable via `felix.json5` (`agent.compaction.model = "ollama/<model-id>"`).

---

## Section 3 — Manual /compact, error handling, observability, testing

### Manual compaction

| Surface | Form |
|---|---|
| CLI chat | `/compact` or `/compact focus on the API design decisions` |
| Tray UI | "Compact now" button in the active session view |
| WebSocket | `chat.compact` RPC, params: `sessionId`, optional `instructions` |

Manual ignores the threshold. Refuses only if total turns < K (returns "session too short to compact").

### Error handling

| Failure | Behavior |
|---|---|
| Bundled Ollama daemon down | Skip; one tray nudge per Felix process lifetime: "Long conversation. Start the bundled local model in Settings to enable auto-compaction." |
| Compaction model not pulled | Skip; nudge: "Pull `qwen2.5:3b-instruct` in Settings → Models to enable auto-compaction." |
| Ollama call times out (60s default, configurable) | Skip; `slog.Warn`; preserve existing history |
| Empty or whitespace-only summary returned | Skip; `slog.Warn`; preserve existing history |
| Reactive path fails AND original turn errored with overflow | Surface to user: `"context limit reached and auto-compaction failed: <reason>. Try /compact manually or shorten the conversation."` |
| Concurrent compaction on same session | Per-session `sync.Mutex`; second caller waits |
| Underlying `llm.Provider` returns a non-overflow error during compaction | Skip; `slog.Warn`; do not retry (caller will see the threshold check pass next turn anyway) |

The tray nudge fires at most once per Felix process lifetime per cause, tracked in an in-memory `map[string]bool`. Restart clears the dedupe.

### Observability

- `slog.Info("compaction triggered", "session_id", id, "reason", "preventive|reactive|manual", "tokens_estimated", n)`
- `slog.Info("compaction complete", "session_id", id, "turns_compacted", n, "tokens_before", x, "tokens_after", y, "duration_ms", d)`
- `slog.Warn("compaction skipped", "session_id", id, "reason", "ollama_down|model_missing|timeout|empty_summary|too_short", "detail", "...")`
- New `AgentEvent` types in `internal/agent/runtime.go`:
  - `EventCompactionStart` — fields: `SessionID`, `TurnsToCompact`, `TokensEstimated`
  - `EventCompactionDone` — fields: `SessionID`, `TurnsCompacted`, `TokensBefore`, `TokensAfter`, `DurationMs`
  - `EventCompactionSkipped` — fields: `SessionID`, `Reason`

CLI renders `🧹 Compacting…` then `🧹 Compacted 24 turns → ~1.2k tokens (was ~38.5k)`.

### Testing

| Package | What's tested |
|---|---|
| `internal/tokens/` | Char-estimator with known strings; calibration math (initial ratio = 1.0; converges toward actual) |
| `internal/compaction/splitter_test.go` | K-turn cutoff with assistant-only tail; with tool-pair chains; `turns < K` returns refusal; zero user messages edge case |
| `internal/compaction/overflow_test.go` | Provider-error string matching for each provider |
| `internal/session/session_test.go` | Extend with: single compaction `View()`, multiple compactions `View()`, no-compaction `View()` matches `History()` |
| `internal/agent/runtime_test.go` | Fake `llm.Provider` returning deterministic summary on Ollama calls; verify auto-trigger fires when estimate exceeds threshold; verify reactive retry on simulated overflow; verify a too-short session does not compact |
| `internal/agent/runtime_test.go` (concurrency) | Two parallel turns on the same session — second waits for first's compaction to finish |
| `//go:build ollama` integration test | Real bundled-Ollama end-to-end on a tagged build (not in default CI) |

### Code surface

| Where | What |
|---|---|
| `internal/tokens/` (new) | `Estimate(...)`, model context window map, calibration helper |
| `internal/compaction/` (new) | `splitter.go`, `summarizer.go`, `overflow.go`, `prompt.go`, default constants |
| `internal/session/session.go` | `EntryTypeCompaction`, `CompactionData`, `View() []SessionEntry`, `CompactionEntry(...)` constructor |
| `internal/agent/runtime.go` | Pre-turn threshold check, post-error reactive retry, three new `AgentEvent` types, per-session compaction mutex |
| `internal/agent/context.go` | `assembleMessages` reads from `Session.View()` instead of `History()` |
| `cmd/felix/main.go` (chat REPL slash-command parser, alongside existing `/quit`, `/sessions`, `/new`, `/screenshot`) | `/compact [instructions]` parsing |
| `internal/gateway/websocket.go` | `chat.compact` JSON-RPC method |
| `cmd/felix-app/` (tray UI) | "Compact now" button, nudge notification handler subscribing to `EventCompactionSkipped` |
| `internal/config/` | `agent.compaction.{enabled, model, threshold, preserveTurns, timeoutSec}` keys; defaults documented |

### Defaults summary (so users don't have to think)

| Knob | Default |
|---|---|
| Auto-compaction | on |
| Compaction model | `ollama/qwen2.5:3b-instruct` |
| Threshold (fraction of context window) | 0.6 |
| Preserve last K turns | 4 |
| Ollama call timeout | 60s |
| Manual `/compact` | always available |

---

## Open questions

None blocking. Possible follow-ups for later milestones:
- Surface "compactions performed" + "tokens saved" in `/status` once Felix grows a `/status` command (Tier 1 #6 in the broader OpenClaw analysis).
- Show compacted session view in the tray "Sessions" panel with a visual marker where each compaction occurred.
- Allow per-agent overrides of compaction model in `felix.json5` (e.g. high-stakes agents use the primary model instead of Ollama).
