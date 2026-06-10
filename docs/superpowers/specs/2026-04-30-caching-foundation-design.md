# Caching Foundation — Anthropic Prompt Caching + Static System Prompt

**Status:** Draft
**Date:** 2026-04-30
**Scope:** `internal/llm/`, `internal/agent/`
**Part of:** Context-management improvements series (sub-project #1 of 6, derived from harness.md gap analysis on 2026-04-30).

## Context

Felix today does not use Anthropic's prompt caching at all. `internal/llm/anthropic.go:90-94` builds `MessageNewParams.System` as a single un-cached `TextBlockParam`, and no `cache_control` markers are attached anywhere. This is the single biggest performance miss vs. Claude Code, which describes cache-prefix work as "the single biggest performance lever" (see `harness.md` §2.3).

Felix already maintains the *invariant* required for caching to work — `internal/agent/cache_stability_test.go:88` (`TestRequestPrefixIsByteStableAcrossTurns`) asserts that turn N+1's prefix is byte-identical to turn N's full prompt — but never asks the provider to actually cache that prefix. Estimated impact of fixing this: ~50–90% reduction in prefill cost and 200–500 ms TTFT improvement on multi-turn Anthropic chats.

A second, related miss: `internal/agent/context.go:113` (`configSummary`) calls `config.Load("")` from the per-turn hot path, so every turn does a disk read of `felix.json5`. The skills index is also rebuilt every turn even though skills don't change between turns within a Run.

This sub-project addresses both: add real prompt caching for Anthropic, and exit the per-turn hot path for static system-prompt construction.

## Goals

- Anthropic requests carry `cache_control` markers such that multi-turn sessions hit cache on the static system prefix and the prior conversation.
- Tool definitions are sent in deterministic (sorted-by-name) order so the byte-stable prefix invariant survives across turns and across processes.
- Static system-prompt construction (identity + agent metadata + config summary + skills index) happens once at Runtime construction, not on every loop iteration.
- `config.Load("")` is removed from the per-turn hot path (called at most once per Runtime build, or zero if a `*config.Config` is already in scope).
- OpenAI and Gemini get implicit caching wins for free via the same byte-stable prefix work — no provider-specific markers.
- Backward compatible: existing `ChatRequest.SystemPrompt` (string) callers continue to work unchanged.

## Non-Goals

- Sectioned/named system-prompt memoization à la Claude Code's `systemPromptSection(name, fn)`. Felix has few enough sections that two cache zones (static system + last user message) capture the value without that infrastructure. Revisit if the spec proves insufficient.
- Gemini explicit caching API. Gemini's implicit caching kicks in on prefixes ≥4096 tokens for Pro models; explicit caching is a separate optimization deferred to a future sub-project.
- Changing how skills/memory matching works (that's sub-project #5: on-demand memory + skills via tool calls).
- Per-turn dynamic context injection (date, git status, walk-up `FELIX.md`) — that's sub-project #2.
- Mid-turn cache invalidation on schema changes. We treat tool schemas and the static system prompt as fixed for a Runtime's lifetime; if either changes (config hot-reload, skills directory rescan), the next Runtime built will simply have a different cached prefix.

## Design

### 1. LLM contract: `SystemPromptPart`, structured `ChatRequest`

`internal/llm/provider.go`:

```go
// SystemPromptPart is one segment of the system prompt. Providers that
// support prompt caching attach a cache marker to parts where Cache=true;
// providers that don't simply concatenate Text fields together.
type SystemPromptPart struct {
    Text  string
    Cache bool // request that the prefix up to and including this part be
               // cached. Anthropic-only; ignored elsewhere.
}

type ChatRequest struct {
    // ... existing fields ...

    // SystemPrompt is the legacy single-string form. Kept for callers that
    // don't care about caching (compaction summarizer, tests). When
    // SystemPromptParts is non-empty, providers MUST prefer it and ignore
    // SystemPrompt.
    SystemPrompt string

    // SystemPromptParts, when non-empty, replaces SystemPrompt. Providers
    // that support caching emit one block per part, attaching cache markers
    // per Cache flag. Providers that don't support caching concatenate
    // Text fields with "\n" separators.
    SystemPromptParts []SystemPromptPart

    // CacheLastMessage requests that the final block of the final user
    // message also be cache-marked. Anthropic-only; ignored elsewhere.
    // Used to capture the conversation-so-far in the cache prefix so turn
    // N+1 reads turn N's full message stream from cache.
    CacheLastMessage bool
}
```

A small helper lives alongside in `provider.go`:

```go
// concatSystemPromptParts joins parts back into a single string with "\n"
// separators. Used by every provider that doesn't implement caching.
// Empty Text fields are skipped.
func concatSystemPromptParts(parts []SystemPromptPart) string { ... }
```

### 2. Anthropic provider — emit cache markers

`internal/llm/anthropic.go` — two surgical changes inside `ChatStream`.

**System block construction** (replaces lines 90-94):

```go
if len(req.SystemPromptParts) > 0 {
    blocks := make([]anthropic.TextBlockParam, 0, len(req.SystemPromptParts))
    for _, p := range req.SystemPromptParts {
        if p.Text == "" {
            continue
        }
        b := anthropic.TextBlockParam{Text: p.Text}
        if p.Cache {
            b.CacheControl = anthropic.CacheControlEphemeralParam{
                Type: "ephemeral",
            }
        }
        blocks = append(blocks, b)
    }
    if len(blocks) > 0 {
        params.System = blocks
    }
} else if req.SystemPrompt != "" {
    params.System = []anthropic.TextBlockParam{{Text: req.SystemPrompt}}
}
```

**Last-message cache marker** in `buildAnthropicMessages`:

`buildAnthropicMessages` gains a `cacheLast bool` parameter. After the loop, if `cacheLast && len(msgs) > 0`, walk the last message's content blocks and attach `CacheControl: {Type: "ephemeral"}` to the last block. Works for plain text, tool-result, and image blocks alike — each block type's union has a `CacheControl` field on its inner param.

The `ChatStream` call site updates from `buildAnthropicMessages(req.Messages)` to `buildAnthropicMessages(req.Messages, req.CacheLastMessage)`.

**Marker count safety:** at most 2 markers per request (Anthropic allows 4).

**Telemetry:** the existing usage logging in the `EventDone` emission gains two fields when present in the response: `cache_creation_input_tokens` and `cache_read_input_tokens`. These come from the streaming `message_start` and `message_delta` events; the SDK exposes them on the `Usage` struct. Surface them in the existing `slog` line so production logs show cache hit rates without needing a separate integration test.

### 3. Other providers — concat-and-ignore

`internal/llm/openai.go`, `gemini.go`, `qwen.go`: at the top of each `ChatStream`, derive the effective system prompt:

```go
sysPrompt := req.SystemPrompt
if len(req.SystemPromptParts) > 0 {
    sysPrompt = concatSystemPromptParts(req.SystemPromptParts)
}
```

Then proceed exactly as today. `req.CacheLastMessage` is silently ignored.

OpenAI returns `usage.prompt_tokens_details.cached_tokens` when its automatic prefix caching hits. Add this to the existing usage logging for OpenAI. Gemini exposes `usage_metadata.cached_content_token_count`; add it to Gemini's logging similarly.

### 4. Agent runtime — split static and dynamic

`internal/agent/context.go`:

```go
// BuildStaticSystemPrompt assembles the portion of the system prompt that
// does not change across turns within a Run: identity, agent self-id, config
// + data dir paths, configSummary, skills index. All inputs are resolved at
// Runtime construction time.
//
// Pure: no I/O. Tests can call this directly.
func BuildStaticSystemPrompt(
    workspace, systemPrompt, agentID, agentName string,
    toolNames []string,
    configSummary string, // pre-computed from *config.Config
    skillsIndex   string, // pre-computed from *skill.Loader
) string { ... }

// BuildConfigSummary takes a *config.Config (already in scope at Runtime
// build time) and returns the same string today's configSummary() builds.
// Pure: no I/O. Replaces the config.Load("") inside configSummary().
func BuildConfigSummary(cfg *config.Config) string { ... }
```

`internal/agent/runtime.go` adds two pre-computed fields:

```go
type Runtime struct {
    // ... existing fields ...
    StaticSystemPrompt string // pre-built; does not change across turns
    Provider           string // "anthropic" | "openai" | "gemini" | "local" | etc.
                              // Set by BuildRuntimeForAgent from
                              // llm.ParseProviderModel(a.Model). Used by
                              // providerSupportsCaching() to decide whether
                              // to set CacheLastMessage. Today only Runtime.Model
                              // (bare name) is stored — adding Provider so
                              // caching gating doesn't have to re-parse.
}
```

The per-turn loop body (`runtime.go:230-296`) is reorganized:

```go
// Was: assembleSystemPrompt(...) called every turn, doing IDENTITY.md read
// and config.Load() and skills index build every iteration.
// Now: dynamic suffix only.
dynamicSuffix := buildDynamicSystemPromptSuffix(matchedSkills, matchedMemory, cortexContext)

// Two parts: static (cached) + dynamic (not cached).
parts := []llm.SystemPromptPart{
    {Text: r.StaticSystemPrompt, Cache: true},
}
if dynamicSuffix != "" {
    parts = append(parts, llm.SystemPromptPart{Text: dynamicSuffix, Cache: false})
}

req := llm.ChatRequest{
    Model:             r.Model,
    Messages:          msgs,
    Tools:             toolDefs,
    MaxTokens:         8192,
    SystemPromptParts: parts,
    CacheLastMessage:  r.providerSupportsCaching(),
    Reasoning:         r.Reasoning,
}
```

`r.providerSupportsCaching()` returns `true` when `r.Provider == "anthropic"`. Setting `CacheLastMessage: true` for non-Anthropic providers is harmless (they ignore it) but explicit gating keeps the request payload deterministic per provider.

Sub-pieces still injected each turn (matched skills, memory, cortex hint, IDENTITY.md re-read on every Run) move into `buildDynamicSystemPromptSuffix`. **Note on IDENTITY.md:** today it's read inside `assembleSystemPrompt` on every loop iteration. Static-prefix construction reads it once at Runtime build. Hot-reload of IDENTITY.md mid-Run is not supported (was effectively unsupported before too — the file was read every turn but no tests exercised in-Run modification).

### 5. Wire up the Runtime builder

`internal/agent/builder.go` — `BuildRuntimeForAgent` already receives the `*config.AgentConfig`. Extend its caller `RuntimeDeps` to pass the live `*config.Config` (already available — both `Permission` and `CortexFn` are derived from it). Inside `BuildRuntimeForAgent`:

```go
provider, modelName := llm.ParseProviderModel(a.Model)

configSummary := BuildConfigSummary(deps.Config)
skillsIndex   := ""
if deps.Skills != nil {
    skillsIndex = deps.Skills.FormatIndex()
}
toolNames := inputs.Tools.Names()

staticPrompt := BuildStaticSystemPrompt(
    a.Workspace, a.SystemPrompt, a.ID, a.Name,
    toolNames, configSummary, skillsIndex,
)

return &Runtime{
    // ... existing fields ...
    Model:              modelName,
    Provider:           provider,
    StaticSystemPrompt: staticPrompt,
}, nil
```

`assembleSystemPrompt` and `configSummary` (the old per-turn versions) are deleted. Test fixtures that constructed `Runtime` directly (bypassing `BuildRuntimeForAgent`) get a small helper `BuildRuntimeForTest` that pre-builds `StaticSystemPrompt` from sensible defaults — or they can set the field directly.

### 6. Tool ordering

`internal/agent/runtime.go`, just before `NormalizeToolSchema`:

```go
toolDefs := r.Tools.ToolDefs()
if r.Permission != nil {
    toolDefs = r.Permission.FilterToolDefs(toolDefs, r.AgentID)
}
sort.SliceStable(toolDefs, func(i, j int) bool {
    return toolDefs[i].Name < toolDefs[j].Name
})
toolDefs, diags := r.LLM.NormalizeToolSchema(toolDefs)
```

`SliceStable` so ties (impossible — names are unique — but stable is cheap insurance) preserve registration order. The existing `cache_stability_test.go` will be extended to assert the sort, so any future regression that re-introduces non-deterministic ordering fails CI.

## Testing

### Extended tests

`internal/agent/cache_stability_test.go`:

- `TestToolDefsSortedByName` — register tools in non-alphabetical order; run a turn; assert `req.Tools` is alphabetically sorted in the recorded request.
- Augment `TestRequestPrefixIsByteStableAcrossTurns` to assert that `req.SystemPromptParts[0].Text` (the static part) is byte-identical across both recorded turns, AND that `SystemPromptParts[0].Cache == true`.

### New tests — Anthropic provider

`internal/llm/anthropic_test.go`:

- `TestSystemPromptPartsEmitCacheControl` — request with two parts (one cached, one not); capture SDK params; assert `params.System[0].CacheControl` is set and `params.System[1].CacheControl` is zero-valued.
- `TestCacheLastMessageMarksFinalBlock` — request with `CacheLastMessage: true` and a multi-message history; assert the last block of the final user message has `CacheControl` set. Repeat for tool-result and image-bearing tail messages.
- `TestSystemPromptStringFallback` — request with only the legacy `SystemPrompt` string set; assert one un-cached `TextBlockParam` is sent.

### New tests — agent runtime

`internal/agent/agent_test.go` (or new file `runtime_caching_test.go`):

- `TestStaticSystemPromptPrecomputed` — build a Runtime via `BuildRuntimeForAgent`; assert `r.StaticSystemPrompt` is non-empty; run two turns via the recording provider; assert both recorded requests have identical `SystemPromptParts[0].Text`.
- `TestConfigLoadNotCalledInHotPath` — replace the path to `felix.json5` with a counter-incrementing fake (or use a build-tag seam); run two turns; assert read count is at most 1 (the build-time read), zero during the loop. Implementation seam: pass `*config.Config` into `BuildRuntimeForAgent` so the loop never needs to call `config.Load`.

### New tests — cross-provider

`internal/llm/provider_interface_test.go` (extend) or new `parts_concat_test.go`:

- `TestSystemPromptPartsConcatenateForNonAnthropic` — drive OpenAI, Gemini, Qwen mocks with `SystemPromptParts: [{Text:"A", Cache:true}, {Text:"B", Cache:false}]` and `SystemPrompt: ""`; assert each provider sees an effective system prompt of `"A\nB"`.

### Manual verification

After deploying:

1. Send 5 messages in a CLI chat session against an Anthropic model.
2. Inspect `~/.felix/logs/felix.log` (or whatever log target is configured) for `cache_read_input_tokens` and `cache_creation_input_tokens`.
3. On turn 1: `cache_creation_input_tokens` ≈ static system tokens + tool def tokens, `cache_read_input_tokens` ≈ 0.
4. On turns 2-5: `cache_read_input_tokens` ≈ static system + tools + prior conversation, `cache_creation_input_tokens` ≈ tokens added since last turn.
5. If turns 2-5 show `cache_read_input_tokens == 0`, something invalidated the prefix — bisect by inspecting the recorded `SystemPromptParts[0].Text` for non-determinism.

Document this verification recipe in the phase-completion notes.

## Risks & mitigations

| Risk | Mitigation |
|------|------------|
| Static prefix accidentally embeds non-deterministic content (e.g., a timestamp, a `time.Now()` formatted string) | `BuildStaticSystemPrompt` is pure and takes only string inputs. The extended cache-stability test asserts byte-equality across turns and would fail loudly. |
| Anthropic SDK shape for `CacheControl` differs from what's coded above | Verify against installed `github.com/anthropics/anthropic-sdk-go` version during implementation. The field name (`CacheControl`) and type (`CacheControlEphemeralParam`) are based on observed SDK types but may need a one-line adjustment. |
| Tool schema changes mid-Runtime (e.g., MCP server reconnects) silently invalidate cache | Out of scope; sub-project #6 (sub-agent + provider fallback) is a better place to handle dynamic tool pool changes. For now, a Runtime built before the MCP reconnect simply gets a cache miss on the next turn — degradation, not a bug. |
| Compaction summarizer (`compaction/summarizer.go:101`) uses the legacy `SystemPrompt: ""` path (none, in fact) | No change required. The summarizer doesn't pass a system prompt. The legacy field path is preserved for any other caller that depends on it. |
| Test fixtures break because they construct `Runtime` directly without `StaticSystemPrompt` | Provide a `BuildRuntimeForTest` helper with sensible defaults; update affected tests as part of this sub-project's commits. |

## Out of scope (deferred to later sub-projects)

- Per-turn dynamic context (date, git status, FELIX.md) — sub-project #2.
- Tool-result disk spillover — sub-project #3.
- Compaction-friendly POST_COMPACT file restore + lift `turn==0` restriction — sub-project #4.
- Memory + skills as on-demand tools — sub-project #5.
- Sub-agent cache reuse + provider fallback — sub-project #6 (depends on this one).

## File touch list

- `internal/llm/provider.go` — add `SystemPromptPart`, extend `ChatRequest`, add `concatSystemPromptParts` helper.
- `internal/llm/anthropic.go` — emit cache markers on system blocks and last message; add cache-token telemetry to `EventDone`.
- `internal/llm/openai.go` — derive `sysPrompt` from parts; add `cached_tokens` telemetry.
- `internal/llm/gemini.go` — derive `sysPrompt` from parts; add cached-token telemetry.
- `internal/llm/qwen.go` — derive `sysPrompt` from parts.
- `internal/agent/context.go` — replace `assembleSystemPrompt` with `BuildStaticSystemPrompt` + `buildDynamicSystemPromptSuffix`; replace `configSummary()` with pure `BuildConfigSummary(*config.Config)`.
- `internal/agent/runtime.go` — add `StaticSystemPrompt` field; replace per-turn assembly with dynamic-only; add tool sorting; pass `SystemPromptParts` + `CacheLastMessage` in the request.
- `internal/agent/builder.go` — add `*config.Config` to `RuntimeDeps`; pre-compute `StaticSystemPrompt` and the input strings.
- `internal/agent/cache_stability_test.go` — extend assertions for sort order and static-prefix stability.
- `internal/llm/anthropic_test.go` — new tests for cache marker emission.
- `internal/agent/runtime_caching_test.go` — new tests for static-prefix precomputation and zero `config.Load` in the hot path.
- `internal/llm/parts_concat_test.go` — new tests for cross-provider parts concatenation.
- Call sites of `BuildRuntimeForAgent` (e.g. `cmd/felix/main.go`, `internal/startup/`) — pass the live `*config.Config` via `RuntimeDeps`.

## Verified

- **Date:** 2026-05-01
- **Model:** `platformai/claude-sonnet-4-6-asia-southeast1`
- **Branch:** `feat/caching-foundation` (commits `49e7d5a..0b30bef`)

**Result — primary goal achieved.** The static system + tool definitions block (7874 tokens) is cached on the first request and read by every subsequent turn:

| Session | Turn | `cache_creation_input_tokens` | `cache_read_input_tokens` |
|---------|------|-------------------------------|---------------------------|
| First request   | 0 | **7874** | 0     |
| First request   | 1 | 0     | **7874** |
| Subsequent (×N) | * | 0     | **7874** |

Cache survived across multiple separate chat sessions over a ~4-minute window (the default 5-min ephemeral TTL) and across one in-flight compaction event. Per-turn `config.Load` removed from the hot path; `slog.Info("anthropic stream usage", ...)` line landed and exposes the cache-token metrics in production logs.

**Finding — message-tail caching not producing additional cache entries.** `cache_read_input_tokens` stays at exactly 7874 across turns even when `prefill_chars` grows from 12k → 57k as the conversation accumulates tool results. The `CacheLastMessage` marker on the user-message tail appears to be silently ignored, most likely because Anthropic enforces a minimum cache-breakpoint size of ~1024 tokens (Sonnet/Opus) past the previous marker — and most user messages don't grow the prefix by that much before the next turn. Worth investigating in a follow-up: either (a) move the message-tail marker further back (e.g., on the second-to-last message so the breakpoint covers more accumulated history), or (b) accept that ~70–90% of the available benefit comes from the static prefix marker alone and remove the second marker as dead code. The static-cache win alone is the bulk of the projected savings.

**Follow-up minor items** (none blocking ship):
- Add `RLock` snapshot to `BuildConfigSummary` for strict concurrency safety against `Config.UpdateFrom` (current behavior is documented but not enforced).
- Investigate the message-tail caching behavior above.
