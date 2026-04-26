# Phase 2 — Provider Portability

**Date:** 2026-04-26
**Status:** Draft (awaiting user review)
**Roadmap:** [`2026-04-26-context-engineering-roadmap.md`](./2026-04-26-context-engineering-roadmap.md) Phase 2

---

## Problem

Felix has four LLM providers (Anthropic, OpenAI, Gemini, Qwen) plus an OpenAI-compatible adapter that fronts Ollama and proxies. Two cross-provider gaps cost real value today:

1. **Tool-schema portability.** Each provider accepts a different JSON Schema dialect. Anthropic accepts full draft-7. Gemini's "OpenAPI 3.0 subset" rejects `anyOf`, `oneOf`, `not`, `$ref`, and `format`. OpenAI rejects `$ref` and `definitions`. A tool author writes one schema and only finds out at runtime — via a 400 from one specific provider — that their schema isn't portable. Worse, the failure is opaque (the SDK error rarely names the offending field).

2. **Reasoning/thinking is unreachable.** Anthropic Claude 4.5 has thinking budgets; OpenAI o-series and GPT-5 have `reasoning_effort`; Gemini 2.0/2.5 have `ThinkingConfig`; Qwen QwQ and Qwen3-thinking have `enable_thinking`. Felix can't ask any of them to think harder — the provider just runs at default. For agentic coding/debugging this leaves a meaningful quality lift on the table.

The roadmap also lists stream-wrapper composition and model-ID normalization. Both are deferred (see §Out of scope) — Felix has no concrete drivers for them today, and YAGNI.

## Goals

- Eliminate the class of "tool schema rejected by one provider" silent failure modes.
- Make extended reasoning a first-class, single-string knob (`reasoning: high`) that works across providers.
- Land observability for both: every stripped field and every clamped/ignored reasoning request is logged via slog.
- Preserve cache stability — added request fields must be deterministic across turns.
- Keep blast radius bounded: no plugin SDK, no new provider, no stream-wrapper machinery.

## Non-goals

- **Stream-wrapper composition.** OpenClaw uses it because it has many quirks worth wrapping (cache-break detection, retry-on-rate-limit, replay-policy). Felix has zero such quirks inline today. Defer until a concrete driver appears.
- **Model-ID alias map.** Cosmetic; the binding system already makes users set the ID once. Defer.
- **Ollama-native provider.** Required to actually plumb thinking on Gemma 3 / Qwen3-thinking / DeepSeek-R1 / GPT-OSS via Ollama's `"think": true`. Real value, ~200-300 lines (client + message conversion + stream parser). Out of Phase 2 to keep scope tight; tracked as roadmap entry "Phase 2.5 — Ollama-native provider".
- **Reasoning-aware token budgeting.** Reasoning tokens count toward output but not input — compaction's threshold math (Phase 1) doesn't account for it. Probably negligible at first; revisit when telemetry shows drift.
- **Per-tool schema overrides.** A tool author cannot opt a particular field out of normalization. If Phase 2 strips too aggressively, fix the rule in the provider, not the tool.

## Constraints

- Single binary, <50MB, <100ms startup, no new heavy dependencies.
- Must not regress cache stability (Phase 0 invariant).
- Must not break existing call sites that don't opt into reasoning (zero-value default).
- Must not require migration for existing tools that already happen to be cross-provider-clean.

## Architecture

### New types in `internal/llm/provider.go`

```go
// Diagnostic describes a single normalization or clamping event.
// Diagnostics flow from the provider back to the caller (typically the agent
// runtime), which logs them via slog.
type Diagnostic struct {
    ToolName string // empty for reasoning diagnostics
    Field    string // dotted JSON path, e.g. "properties.url.format"
    Action   string // "stripped" | "rewritten" | "rejected" | "clamped" | "ignored"
    Reason   string // human-readable; safe to log
}

// ReasoningMode is the unified reasoning/thinking knob across providers.
// Zero value (ReasoningOff / "") means no reasoning — safe default for
// existing call sites that don't set the field.
type ReasoningMode string

const (
    ReasoningOff    ReasoningMode = ""
    ReasoningLow    ReasoningMode = "low"
    ReasoningMedium ReasoningMode = "medium"
    ReasoningHigh   ReasoningMode = "high"
)
```

### Interface change

```go
type LLMProvider interface {
    ChatStream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
    Models() []ModelInfo
    NormalizeToolSchema(tools []ToolDef) ([]ToolDef, []Diagnostic) // NEW
}
```

### ChatRequest gains one field

```go
type ChatRequest struct {
    Model        string
    Messages     []Message
    Tools        []ToolDef
    MaxTokens    int
    Temperature  float64
    SystemPrompt string
    Reasoning    ReasoningMode // NEW; zero value = off
}
```

### Per-provider normalization rules (initial set, conservative)

| Provider     | Strip                                     | Keep                                | Notes                                  |
|--------------|-------------------------------------------|-------------------------------------|----------------------------------------|
| Anthropic    | nothing                                   | everything                          | Baseline; full draft-7 supported.      |
| OpenAI       | `$ref`, `definitions`                     | `anyOf`, `oneOf`, `format`, `not`   | Function-calling schema is restricted. |
| Gemini       | `anyOf`, `oneOf`, `not`, `$ref`, `format` | OpenAPI 3.0 subset                  | The big one — most likely to bite.     |
| Qwen         | `$ref`, `definitions`                     | same as OpenAI                      | DashScope tracks OpenAI's shape.       |

Stripping is recursive — the walker descends into `properties.*`, `items`, and `additionalProperties` to catch nested occurrences. Each strip emits one `Diagnostic`.

For the OpenAI-compatible / `local` kinds, normalization uses the OpenAI ruleset since the wire format is OpenAI-shaped.

### Per-provider reasoning mapping

| Mode    | Anthropic (budget tokens) | OpenAI (effort) | Gemini (thinking budget) | Qwen (enable_thinking) | OpenAI-compat / local |
|---------|---------------------------|-----------------|--------------------------|------------------------|-----------------------|
| off     | omit thinking block       | omit reasoning  | thinking_budget=0        | false                  | omit reasoning        |
| low     | budget=1024               | "low"           | thinking_budget=1024     | true                   | omit + diag           |
| medium  | budget=4096               | "medium"        | thinking_budget=4096     | true                   | omit + diag           |
| high    | budget=16384              | "high"          | thinking_budget=16384    | true                   | omit + diag           |

**OpenAI-compatible / local:** when `Kind == "openai-compatible"` or `"local"`, reasoning is suppressed and a `Diagnostic{Action: "ignored", Reason: "endpoint may not support reasoning_effort"}` is emitted. This avoids breaking Ollama-served requests (Ollama tolerates unknown JSON keys but doesn't act on `reasoning_effort`). Real reasoning on Ollama-served Gemma/Qwen3/DeepSeek-R1 needs the deferred Ollama-native provider.

**Qwen clamping:** Qwen's `enable_thinking` is boolean. `low/medium/high` all map to `true`; a `Diagnostic{Action: "clamped", Reason: "qwen reasoning is boolean; granularity ignored"}` is emitted for any non-off level since the mapping is lossy regardless of which level was requested.

### Capability detection

Each provider has a `supportsReasoning(modelID string) bool` helper, table-driven against known reasoning-capable IDs:

- Anthropic: `claude-sonnet-4-*`, `claude-opus-4-*`, `claude-3-7-sonnet-*`
- OpenAI: `o1-*`, `o3-*`, `o4-*`, `gpt-5-*`
- Gemini: `gemini-2.0-flash-thinking-*`, `gemini-2.5-*`
- Qwen: `qwen-qwq-*`, `qwen3-*` (the thinking variants)

Unknown model IDs default to "supported" — better to try and let the API reject than to silently strip something the user wanted. When a known-unsupported model is paired with non-off reasoning, emit `Diagnostic{Action: "ignored", Reason: "model does not support reasoning"}` and omit the config from the API call.

### Runtime wiring (`internal/agent/runtime.go`)

```go
toolDefs := r.Tools.ToolDefs()
toolDefs, diags := r.LLM.NormalizeToolSchema(toolDefs)
for _, d := range diags {
    slog.Warn("tool schema normalized",
        "tool", d.ToolName, "field", d.Field, "action", d.Action, "reason", d.Reason)
}
// existing ChatStream call uses normalized toolDefs
req := llm.ChatRequest{
    // ... existing fields ...
    Reasoning: r.Reasoning, // from agent config
}
```

`NormalizeToolSchema` is called once per turn, just before `ChatStream`. The cost is microseconds — schema walks over 9 tools.

### Agent config

One new field in agent YAML/JSON:

```yaml
reasoning: high   # off | low | medium | high; default off
```

Plumbed through `internal/agent/config.go` → `runtime.go` → `ChatRequest.Reasoning`. Validation: parse as `ReasoningMode`; reject unknown strings at config load with a clear error.

### Test infrastructure: `internal/llm/llmtest`

Phase 2 starts with a consolidation task. The interface widens, which would require adding `NormalizeToolSchema` to all 8 ad-hoc `LLMProvider` test stubs scattered across `internal/compaction/`, `internal/agent/`, and `internal/tools/`. A new shared package collapses them into one configurable Stub:

```go
package llmtest

type Stub struct {
    Text     string
    Delay    time.Duration
    Started  chan struct{}
    ChatErr  error
    ChatHook func(req llm.ChatRequest)              // observe each request (for cache-stability/recording tests)
    NormHook func([]llm.ToolDef) ([]llm.ToolDef, []llm.Diagnostic)
    Models_  []llm.ModelInfo
}

func (s *Stub) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error)
func (s *Stub) Models() []llm.ModelInfo
func (s *Stub) NormalizeToolSchema(tools []llm.ToolDef) ([]llm.ToolDef, []llm.Diagnostic)
```

Defaults: no delay, no error, identity normalization, single text response. Each existing stub becomes a few-line `&llmtest.Stub{...}` initializer.

Doing this first means subsequent Phase 2 tasks add the new method/field in *one place*, not eight.

## Cache-stability invariants

Two new things must hold across turns to preserve prompt cache hits:

1. **NormalizeToolSchema is deterministic.** Given the same input, it returns the same output and the same diagnostics in the same order. Guaranteed by: input order is sorted (Phase 0), strip rules are static, walker order is deterministic.
2. **Reasoning level is part of the cached prefix.** Same agent + same model + same `reasoning` setting produces the same prefix. The field flows through `ChatRequest` once; provider mapping is pure. No conditional branching keyed on time, request count, or random state.

Both invariants get a regression test extending `internal/agent/cache_stability_test.go`.

## Testing strategy

Per-task TDD with two-stage review (spec compliance → code quality) per the existing `subagent-driven-development` workflow. Test layers:

- **Unit:** each provider's `NormalizeToolSchema` has table-driven tests (input schema → expected output + expected diagnostics).
- **Unit:** each provider's reasoning mapping has tests against a recorded outgoing request (HTTP transport mocked; assert `reasoning_effort` / `thinking` / `thinkingConfig` field appears as expected, or is absent for off / unsupported / openai-compatible).
- **Integration:** `cache_stability_test.go` extended with: (a) reasoning level included in cached prefix, (b) NormalizeToolSchema output is deterministic across turns, (c) diagnostics list doesn't accumulate (no slice growth between turns).
- **Race:** full suite with `-race` after each task.

## Task order

Each task below is one logical step; tasks 3 and 6 expand to one commit per provider (4 each). Total ~14 commits. TDD per commit: failing test → minimal fix → verify → commit.

1. **`internal/llm/llmtest` package** — new shared Stub; migrate the 8 existing ad-hoc stubs; tests pass.
2. **`Diagnostic` type + `NormalizeToolSchema` interface method** with no-op default in Stub. Interface widens; all callers compile.
3. **Per-provider normalization rules** (4 commits: Anthropic identity baseline → OpenAI restricted strip → Qwen same-as-OpenAI → Gemini OpenAPI-subset strip). Each provider's commit includes its own table-driven tests.
4. **Runtime wiring.** Agent runtime calls `NormalizeToolSchema` pre-flight, logs diagnostics. Cache-stability test extended for determinism.
5. **`ReasoningMode` type + `ChatRequest.Reasoning` field.** Default off keeps everything working.
6. **Per-provider reasoning mapping** (4 commits: Anthropic budget tokens → OpenAI effort + Kind-based suppression for openai-compatible/local → Gemini ThinkingConfig → Qwen enable_thinking with clamping diagnostic).
7. **Agent config plumbing.** YAML/JSON5 `reasoning` field flows through to `ChatRequest`. Capability detection + clamping diagnostics. Validation at config load.
8. **Cache-stability test extension.** Assert reasoning level is in the cached prefix and stable across turns.

## Out of scope (explicit deferrals)

- Stream-wrapper composition — no concrete driver today.
- Model-ID alias map — cosmetic.
- Ollama-native provider (Phase 2.5) — needed for real Gemma 3 / Qwen3-thinking / DeepSeek-R1 / GPT-OSS reasoning via Ollama's `"think": true` API.
- Reasoning-aware token budgeting in compaction.
- Per-tool schema overrides.

## Open questions

- **Where does the `supportsReasoning` table live?** Suggested: as a private slice/map in each provider file (`internal/llm/anthropic.go`, etc.) so the table sits next to the provider that owns it. Alternative: a shared `model_capabilities.go` registry. Probably won't matter until the second phase that needs capability detection.
- **Should reasoning diagnostics be Warn or Info?** Tool-schema strips are Warn (the schema the model sees differs from what the author wrote — surprising). Reasoning ignored/clamped is Info (the model just runs at default; user can read the log if they wonder why). Confirm in implementation if this feels wrong in practice.
- **Anthropic budget defaults — are 1024/4096/16384 right?** These match Claude Code's defaults. They're tunable later via per-agent override if a real driver appears.
