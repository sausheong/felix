# Context Engineering Roadmap — Design

**Date:** 2026-04-26
**Status:** Draft (awaiting user review)
**Author:** Synthesis of Claude Code source-read + OpenClaw source-read + Felix compaction-bug post-mortem

---

## Problem

On 2026-04-26 we shipped a fix for a compaction bug where `Session.View()` walked back from the leaf and broke on the just-appended compaction entry, returning `[summary]` only and silently dropping every preserved turn. Symptoms: the model lost the most recent assistant response (Wasm/Extism explanation) and replied "I'm ready to help! Our previous conversation covered Colima…", framing the chat as if it were starting fresh.

The fix re-links the preserved range so View walks correctly. But the symptom revealed a deeper issue: Felix's context-management layer is one function bolted onto the agent loop, with several quality and robustness gaps relative to the two reference implementations we studied:

- **Claude Code** (TS, owns inference) — read from `/Users/sausheong/projects/claude-code-source/src/services/compact/`. Strongest patterns: forward-slice boundary marker, token-based threshold with reserved output budget, 9-section summary prompt with anti-drift guardrails, comprehensive post-compact re-injection (files/skills/tools), microcompact tier, "resume directly" continuation wrapper, prompt-cache-break detection.
- **OpenClaw** (TS, multi-provider gateway like Felix) — read from `/Users/sausheong/projects/openclaw/src/`. Strongest patterns: `ContextEngine` interface that subsumes compaction + skills + memory + assembly under one seam, prompt-cache stability as a correctness rule, three-axis sandbox/policy/elevated model, manifest-first plugin contract, identifier-preservation in summarization, splitter that respects tool-call/tool-result pairing, multi-stage `summarizeWithFallback`, `stripToolResultDetails` before sending to summarizer.

This roadmap consolidates the highest-leverage learnings into a sequenced plan that respects Felix's core constraints.

## Goals

- Eliminate the class of "compaction silently drops context" failure modes for good.
- Make prompt-cache stability a hard correctness property (10× cost lever; OC treats it as a rule).
- Normalize provider quirks at the LLM-provider boundary so the agent loop is provider-agnostic.
- Eventually extract context-assembly behind a `ContextEngine` interface so compaction/skills/memory/post-compact re-injection are pluggable.
- Ship in small, independently-valuable phases so each phase justifies itself before the next is started.

## Non-goals

- **No plugin SDK** in this roadmap. OpenClaw's manifest-first contract is real architecture, but Felix has no third-party plugin demand yet. Defer until a concrete plugin we don't want to compile in shows up. (Phase 7 in the roadmap is a placeholder, not work.)
- **No hooks system** (lifecycle scripts). Wait for plugins.
- **No isolated-cron-lane / delivery-plan / session-reaper** machinery. Felix's heartbeat is fine for one daemon.
- **No auth-profile rotation** (multi-key per provider). Single-key model is fine until users complain about rate limits.
- **No three-phase dreaming-style memory consolidation** in this roadmap. If memory write-side becomes painful, a simpler MEMORY.md curator agent is the right first step.
- **No channel registry refactor** until Felix has 5+ channels.
- **No `*.runtime.ts`-style lazy-loading boundary discipline.** Go's linker handles this for free.

## Constraints

- **Single binary.** All work compiles into one Go executable; no runtime extension loading.
- **<50MB binary, <100ms startup.** No new heavy dependencies.
- **Multi-provider.** Anthropic, OpenAI, Gemini, Ollama, DeepSeek today; more later. Patterns must work across all.
- **Felix doesn't own inference.** No tricks that depend on controlling the model or provider transport.
- **Append-only JSONL session model.** No on-disk rewrites.

## Architecture vision

Two seams matter most:

1. **The `ContextEngine` seam** — between the agent runtime and the prompt-construction logic. Today the runtime calls `assembleMessages` + `MaybeCompact` directly. Target: the runtime calls `engine.Assemble(turn) → []Message + systemPromptAddition + tokenBudget` and `engine.AfterTurn(turn)` only. Engines internally own compaction strategy, skills injection, memory recall, and post-compact re-injection.
2. **The `LLMProvider` normalization seam** — between the agent runtime and provider-specific quirks. Today providers leak their differences (tool-schema flavors, reasoning configs, streaming idioms). Target: providers expose `NormalizeToolSchema`, `ReasoningMode` enum, and stream-wrapper composition; the runtime is provider-agnostic.

These are independent. They land in parallel after the immediate stability work.

## Phase summary

| # | Phase | Scope | Effort | Status | Dependencies |
|---|-------|-------|--------|--------|--------------|
| 0 | Stop the bleeding | Continuation wrapper on summary, cache-stability test helper, map-iteration audit | 1-2 days | **Plan written** | — |
| 1 | Compaction quality | 9-section prompt, identifier preservation, `stripToolResultDetails`, token-based threshold, three-stage fallback, circuit breaker | 3-5 days | **Plan written** | Phase 0 |
| 2 | Provider portability | `NormalizeToolSchema`, `ReasoningMode` enum, stream-wrapper composition, model-ID normalization | 1-2 weeks | Roadmap entry | — |
| 3 | `ContextEngine` interface | Extract interface, route current code through `legacy` impl, identical behavior | 1-2 weeks | Roadmap entry | Phase 1 |
| 4 | Tool sandbox 3-axis model | Separate where/which/escape; `deny`-wins cascade; bind-mount safety; macOS `sandbox-exec` first | 1 week | Roadmap entry | — |
| 5 | Microcompact tier | Drop stale tool results in place, no LLM call; time-based eviction | 3-5 days | Roadmap entry | Phase 3 |
| 6 | Post-compact re-injection | Recently-read files re-attached, skills re-attached, tool/MCP listings re-announced, cache-break notified | 1 week | Roadmap entry | Phase 3 |
| 7 | Plugin contract | (Deferred — gated on real third-party plugin trigger) | — | Deferred | gated |

## Sequencing dependencies

```
Phase 0 ──┬─► Phase 1 ──┐
          │             ├─► Phase 3 ──┬─► Phase 5
          └─► Phase 2 ──┘             └─► Phase 6

Phase 4 (tool sandbox) — independent, do whenever
Phase 7 — gated on external trigger
```

## Phase 0 — Stop the bleeding

**Goal:** ship within 2 days; no architectural change; addresses the immediate framing-drift symptom and locks in cache-stability as a regression-testable property.

**Scope:**
- **Continuation wrapper on the compaction summary user-message.** Currently `internal/agent/context.go:172-175` injects `"[Previous conversation summary]\n\n" + cd.Summary` as a bare user message. Add the CC-style "Resume directly — do not acknowledge the summary…" directive after the summary so the model continues rather than restarting.
- **Cache-stability test helper.** A Go test that runs the agent loop through several turns with a fake provider, captures the LLM-request prefix at each turn, and asserts: every turn's prefix is a strict extension of the previous turn's prefix (or differs only in explicitly-changed positions like the new user message). Would have caught the compaction-View bug.
- **Map-iteration audit.** Grep every `for k := range m` where `m` is `Tools`, `Skills`, `Providers`, `MCPServers`, `Bindings`, `Agents`, etc. Replace with sorted-key iteration where the result feeds the LLM request. Go map iteration order is randomized; even if a test passes today it would break the next run.

**Out of scope:** prompt rewrite (Phase 1), threshold change (Phase 1), tool-result stripping (Phase 1).

**Plan:** `docs/superpowers/plans/2026-04-26-phase-0-cache-stability-and-continuation.md`

## Phase 1 — Compaction quality

**Goal:** raise compaction's intrinsic quality so summaries don't lose user-visible content even on adversarial sessions; remove the message-count trigger that fires too eagerly on tool-heavy turns.

**Scope:**
- **9-section summary prompt** ported from CC's `BASE_COMPACT_PROMPT` (`prompt.ts:61-143`). Mandatory sections: Primary Request, Key Tech, Files+Code, Errors+Fixes, Problem Solving, **All user messages enumerated** (this is the anti-drift mechanism), Pending Tasks, Current Work, **Optional Next Step with verbatim quotes**.
- **`<analysis>` scratchpad block** in the prompt (drafting space; stripped before injection).
- **`stripToolResultDetails`** before sending to the summarizer. Tool results can contain prompt injection; the summarizer LLM should never see raw `details` blobs.
- **Identifier-preservation policy** in the prompt: UUIDs, file paths, IDs verbatim.
- **Token-based threshold** replacing the current `compactMsgsTrigger = 20` message-count cap. Compute `effectiveWindow = contextWindow - 20K_for_summary`, `threshold = effectiveWindow - 13K_buffer`. Felix already has `internal/tokens/Estimate`.
- **Three-stage `summarizeWithFallback`** ported from CC: full → small-only-with-oversized-notes → final placeholder. Plus retry with backoff.
- **Circuit breaker.** After 3 consecutive autocompact failures on a session, stop trying for the session. Avoids the runaway-failure pattern CC documented.

**Out of scope:** the `ContextEngine` interface (Phase 3), microcompact (Phase 5), post-compact re-injection (Phase 6).

**Plan:** `docs/superpowers/plans/2026-04-26-phase-1-compaction-quality.md`

## Phase 2 — Provider portability (roadmap entry, plan to come)

When ready, brainstorm and write a plan covering:
- **`LLMProvider.NormalizeToolSchema(*Tool) error`** — each provider strips/rewrites unsupported fields (Gemini rejects `anyOf`; others reject `format`) and emits diagnostics rather than silently break.
- **`ReasoningMode` enum** (`off|minimal|low|medium|high|adaptive`) — normalized at the provider boundary. Anthropic thinking, OpenAI reasoning effort, Gemini thinking config all map to this.
- **Stream-wrapper composition** (OC `composeProviderStreamWrappers`). Each provider registers wrappers for its quirks; currently embedded inline.
- **Provider model-ID normalization** — single alias map at the boundary.

**Why now is too early:** Phases 0+1 stabilize the most-used path. Provider portability work depends on understanding how the runtime *currently* uses providers, which the Phase 1 changes will refactor.

## Phase 3 — `ContextEngine` interface (roadmap entry, plan to come)

When ready, brainstorm and write a plan covering:
- Define a Go interface roughly: `Bootstrap(*Session)`, `Ingest(Entry)`, `Assemble(Turn) ([]llm.Message, string, int)`, `Compact(*Session)`, `AfterTurn(*Session)`, `Maintain(*Session)`.
- Route current code through a `legacy` implementation. Identical behavior, new seam. Extensive snapshot/golden tests to prove no regression.
- Microcompact (Phase 5) and post-compact re-injection (Phase 6) ship as new engine operations or as a second engine.

**Why this comes after 1 and not before:** the Phase 1 changes refactor the inside of the compaction step. Doing the interface extraction over a stable Phase 1 codebase is much safer than racing with it.

## Phase 4 — Tool sandbox three-axis model (roadmap entry, plan to come)

When ready, brainstorm and write a plan covering:
- Separate the three axes explicitly: **where** (sandbox: host / Docker / namespace) × **which** (policy: allow/deny/groups) × **escape** (elevated exec).
- Implement `deny`-always-wins, non-empty-`allow`-blocks-everything-else cascade.
- Bind-mount safety from OC: symlink-parent escape detection, blocked-path validation **after** path resolution.
- Wire one real sandbox mode first. macOS `sandbox-exec` is the smallest viable starting point on Felix's primary dev platform; namespaces + Docker as follow-ups.

**Independent of context-engineering work** — can land in parallel with any other phase.

## Phase 5 — Microcompact tier (roadmap entry, plan to come)

When ready, brainstorm and write a plan covering:
- Drop stale tool results in place — no LLM call. Only operates on file reads (now stale), shell outputs (already processed), web fetches.
- Time-based: tool results past TTL get cleared.
- Reclaims 30-70% of context cheaply and reduces full-summary frequency.

**Slot in as a `ContextEngine` operation** once Phase 3 lands.

## Phase 6 — Post-compact re-injection (roadmap entry, plan to come)

When ready, brainstorm and write a plan covering:
- Recently-read files re-injected (token budget cap, per-file cap).
- Skill definitions re-attached.
- MCP / tool / agent listings re-announced.
- Notify cache-break detection so the post-compact miss isn't flagged as drift.

**Slot in as a `ContextEngine` operation** once Phase 3 lands.

## Phase 7 — Plugin contract (deferred)

Not work; placeholder. Trigger: a concrete third party plugin Felix doesn't want to compile in. When that day comes, brainstorm covers: SDK surface (start with `Channel`, `LLMProvider`, `Tool`, `Skill`); manifest schema; one provider (Ollama) and one channel (CLI) converted as proof; build-tag-driven compile-time bundling.

## Open questions

- **Where does the cache-stability test live?** Suggested: `internal/agent/cache_stability_test.go`. Calls into `runtime.go`'s LLM-request construction; uses the existing fake-provider patterns.
- **Should the continuation wrapper be configurable?** No — it's pure model-control text; users don't need to tune it.
- **Should the 9-section prompt include Felix-specific sections (e.g., MEMORY, Cortex hits)?** No in Phase 1; revisit when Phase 6 (post-compact re-injection) lands and we have a clearer picture of what's worth re-surfacing.
- **What's the right default for the token-based threshold?** Match CC's 13K-buffer / 20K-output-reserve numbers initially; tune from telemetry once collected.
