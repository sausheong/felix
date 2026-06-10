# Cortex + Embeddings Default to Local Ollama — Design

**Date:** 2026-04-22
**Status:** Draft (awaiting user review)
**Author:** Brainstorming session with Felix maintainer

---

## Problem

Three cheap-frequent LLM operations in Felix currently default to cloud providers:

1. **Cortex extraction** — `CortexConfig.Provider` defaults to `"openai"` (in `Validate()`), `LLMModel` to `"gpt-5-mini"`. Every conversation thread that meets `ShouldIngest` triggers a paid extraction call.
2. **Cortex embeddings** — same path; OpenAI `text-embedding-3-small` (1536d) per entity.
3. **Memory embeddings** — `MemoryConfig.EmbeddingProvider`/`EmbeddingModel` are unset by default; if memory is enabled, the embedder falls through to OpenAI `ada-002`.

Felix already bundles Ollama (a local OpenAI-compatible LLM runtime) and onboarding pulls a primary model (`qwen3.5:9b` or `gemma4:latest`). Routing these three consumers through the bundled Ollama eliminates the only paid surprise for users who deliberately picked a local LLM, and keeps Felix's "self-contained, easy for non-tech users" promise.

This is **defaults work**: existing functionality is preserved; only the out-of-the-box choice changes.

## Goals

- Default Cortex (LLM + embedder) and Memory embeddings to bundled Ollama.
- Cortex auto-mirrors the default agent's model — one source of truth for "which local model is in use."
- First-ever Felix launch background-pulls the LLM and the embedding model so semantic search and KG ingest "just work" without manual `felix model pull`.
- Drop the qwen/gemma onboarding prompt — `gemma4:latest` is the single recommended local default.
- Skip-and-nudge fallback when the local model isn't available (mirrors compaction's pattern).
- No regression for cloud users: explicit Cortex/Memory config is preserved.

## Non-goals

- New provider abstraction. Cortex's existing `cortexoai` package already supports OpenAI-compatible base URLs, which is exactly what bundled Ollama exposes.
- Centralized model resolver across all roles (agent, cortex, embed, compaction). YAGNI for 4 roles; defaults + a small auto-mirror in `cortex.Init` is enough.
- Eager rewriting of `felix.json5` to copy resolved values into Cortex/Memory fields. Sentinels (empty strings) are sufficient and propagate when the agent's model changes.
- Auto-pull of arbitrary models on demand. Only the two well-known defaults (`gemma4:latest` + `nomic-embed-text`) get the first-run treatment.
- Migration logic for existing users — there are none.

## Approach

**A — Defaults + auto-mirror + first-run bootstrap.**

1. Config defaults change so Memory and the default agent point at `local`/well-known model names; Cortex's provider/model fields stay empty as a sentinel meaning "mirror the default agent's model."
2. `cortex.Init` resolves the sentinel by parsing the default agent's `Model` field (e.g. `local/gemma4:latest` → provider=`local`, model=`gemma4:latest`).
3. A new `internal/local/bootstrap.go` runs at startup; if `~/.felix/.first-run-done` is missing, it kicks off a background `ollama pull` of `nomic-embed-text` then `gemma4:latest`, then writes the sentinel.
4. Onboarding loses the qwen/gemma prompt — it just sets `local/gemma4:latest` and moves on.
5. Cortex and Memory tolerate a missing model with a `slog.Warn` plus one tray nudge per Felix process per cause.

(Rejected alternatives: eager copy of resolved values at install — config drift risk; centralized resolver — over-engineered for 4 roles.)

---

## Section 1 — Config defaults + auto-mirror + embedding wiring

### Config defaults

In `config.DefaultConfig()` and `Validate()`:

| Field | Old default | New default |
|---|---|---|
| `Agents.List[default].Model` | (set by onboarding) | `local/gemma4:latest` |
| `Memory.Enabled` | (caller-set) | `true` |
| `Memory.EmbeddingProvider` | `""` (→ ada-002) | `"local"` |
| `Memory.EmbeddingModel` | `""` (→ ada-002) | `"nomic-embed-text"` |
| `Cortex.Provider` | `"openai"` (Validate backfill) | `""` (sentinel) |
| `Cortex.LLMModel` | `"gpt-5-mini"` | `""` (sentinel) |

The `Validate()` block

```go
if c.Cortex.Enabled && c.Cortex.Provider == "" {
    c.Cortex.Provider = "openai"
}
```

is **removed** — emptiness is now the sentinel for "mirror the default agent's model."

### Cortex auto-mirror

`cortex.Init` signature changes to:

```go
func Init(cfg config.CortexConfig,
          memCfg config.MemoryConfig,
          defaultAgentModel string,
          getProvider func(name string) config.ProviderConfig,
         ) (*cortex.Cortex, error)
```

Resolution at the top of `Init`:

```go
provider := cfg.Provider
model    := cfg.LLMModel
if provider == "" && model == "" {
    provider, model = llm.ParseProviderModel(defaultAgentModel)
    slog.Info("cortex auto-mirror",
        "agent_model", defaultAgentModel,
        "resolved_provider", provider,
        "resolved_model", model)
}
pcfg := getProvider(provider)
```

### Cortex local-provider branch (new)

When `provider == "local"`, build cortex with the OpenAI-compatible Ollama endpoint:

```go
case "local":
    llm := cortexoai.NewLLM("",
        cortexoai.WithBaseURL(pcfg.BaseURL),
        cortexoai.WithModel(model))

    embModel, embDims := localpkg.EmbeddingDims(memCfg.EmbeddingModel)
    embedder := cortexoai.NewEmbedder("",
        cortexoai.WithEmbedderBaseURL(pcfg.BaseURL),
        cortexoai.WithEmbeddingModel(oai.EmbeddingModel(embModel), embDims))

    extractor := hybrid.New(deterministic.New(), llmext.New(llm))
    opts = append(opts,
        cortex.WithLLM(llm),
        cortex.WithEmbedder(embedder),
        cortex.WithExtractor(extractor))
```

The existing `openai` and `anthropic` branches are unchanged.

### Embedding-dimension lookup

New file `internal/local/embed_dims.go`:

```go
package local

var embedDims = map[string]int{
    "nomic-embed-text":       768,
    "mxbai-embed-large":      1024,
    "all-minilm":             384,
    "text-embedding-3-small": 1536,
    "text-embedding-3-large": 3072,
    "text-embedding-ada-002": 1536,
}

// EmbeddingDims returns (modelName, dimensions). Unknown models fall back
// to 1536 (OpenAI's standard) so cortex doesn't refuse to start.
func EmbeddingDims(model string) (string, int) {
    if d, ok := embedDims[model]; ok {
        return model, d
    }
    return model, 1536
}
```

### Memory wiring

`memory.NewOpenAIEmbedder(apiKey, baseURL, model)` already accepts a base URL and model. The default flip happens entirely in config — when `Memory.EmbeddingProvider == "local"`, the existing call sites in `cmd/felix/main.go:278` and `internal/startup/startup.go:255` will look up `cfg.GetProvider("local")` and pass its `BaseURL`. Model name passes through as-is.

`internal/memory/embedder.go::NewOpenAIEmbedder` has a `switch model { case "text-embedding-3-small": ... default: m = openai.EmbeddingModel(model) }`. The `default` branch already accepts arbitrary names like `nomic-embed-text` — no code change needed.

### Memory tolerates missing embedder

Existing behavior: `Manager.embedder == nil` → BM25 only. Extension: at the call sites (`cmd/felix/main.go:278` and `internal/startup/startup.go:255`), after constructing the embedder, run a single probe call `embedder.Embed(ctx, []string{"probe"})` with a 5-second timeout. On error → `slog.Warn("memory: embedder unavailable, BM25-only", "reason", err)`, fire one-time tray nudge, call `memMgr.SetEmbedder(nil)` (or skip the SetEmbedder call entirely). On success → wire the embedder normally.

---

## Section 2 — First-run bootstrap

### Trigger

On Felix process startup, after `local.InjectLocalProvider` succeeds (i.e., bundled Ollama port is bound), call `local.EnsureFirstRunModels`. If the sentinel `~/.felix/.first-run-done` exists, no-op. Otherwise spawn a background goroutine that pulls the defaults sequentially.

### `internal/local/bootstrap.go` (new)

```go
package local

import (
    "context"
    "log/slog"
    "os"
    "path/filepath"
    "time"
)

// PullFunc is the function signature for pulling an Ollama model.
// Injected for testability.
type PullFunc func(ctx context.Context, ollamaURL, model string,
                   onProgress func(percent float32)) error

// EnsureFirstRunModels kicks off background pulls of the default LLM and
// embedding model on first ever Felix run. No-op on subsequent runs.
// Returns immediately; pulls run in their own goroutine.
func EnsureFirstRunModels(ctx context.Context, dataDir, ollamaURL string,
                          pull PullFunc, events chan<- BootstrapEvent) {
    sentinel := filepath.Join(dataDir, ".first-run-done")
    if _, err := os.Stat(sentinel); err == nil {
        return // already bootstrapped
    }

    models := []string{"nomic-embed-text", "gemma4:latest"}
    go func() {
        events <- BootstrapEvent{Type: BootstrapStart, Models: models}
        start := time.Now()
        for _, m := range models {
            mStart := time.Now()
            err := pull(ctx, ollamaURL, m, func(p float32) {
                events <- BootstrapEvent{Type: BootstrapProgress, Model: m, Percent: p}
            })
            if err != nil {
                slog.Warn("first-run model pull failed", "model", m, "error", err)
                events <- BootstrapEvent{Type: BootstrapFailed, Model: m, Error: err.Error()}
                return // sentinel NOT written → retry on next launch
            }
            slog.Info("first-run model pulled", "model", m,
                "duration_ms", time.Since(mStart).Milliseconds())
        }
        slog.Info("first-run bootstrap complete",
            "duration_ms", time.Since(start).Milliseconds())
        events <- BootstrapEvent{Type: BootstrapDone, Models: models,
            DurationSec: int(time.Since(start).Seconds())}
        _ = os.WriteFile(sentinel, []byte(time.Now().Format(time.RFC3339)), 0o644)
    }()
}
```

### Pull mechanism

Existing `pullLocalModel` in `cmd/felix/main.go` is **extracted** into `internal/local/pull.go` so both call paths share one implementation:

```go
package local

// PullModel POSTs to the Ollama /api/pull endpoint and streams progress.
// onProgress receives percent (0-100) deltas; it may be nil.
func PullModel(ctx context.Context, ollamaURL, model string,
               onProgress func(percent float32)) error {
    // ... (relocated from cmd/felix/main.go::pullLocalModel,
    //      with progress callback added)
}
```

`cmd/felix/main.go::pullLocalModel` becomes a thin wrapper that prints progress to stdout (preserving the foreground onboarding UX).

### Order

`nomic-embed-text` first (~270 MB, completes in seconds → semantic search online quickly). Then `gemma4:latest` (~9.6 GB, slow). Sequential — avoids saturating the user's connection.

### Sentinel-only-on-success

If either pull fails, the sentinel is **not** written. Next Felix launch retries. Flaky network → eventually succeeds without user intervention.

### Bootstrap events

New types in `internal/local/bootstrap.go`:

```go
type BootstrapEventType int

const (
    BootstrapStart BootstrapEventType = iota
    BootstrapProgress
    BootstrapDone
    BootstrapFailed
)

type BootstrapEvent struct {
    Type        BootstrapEventType
    Models      []string  // populated for Start, Done
    Model       string    // populated for Progress, Failed
    Percent     float32   // populated for Progress
    DurationSec int       // populated for Done
    Error       string    // populated for Failed
}
```

Forwarded by `internal/agent/runtime.go` as `AgentEvent`s of corresponding types (`EventBootstrapStart`, `EventBootstrapProgress`, `EventBootstrapDone`, `EventBootstrapFailed`) so existing CLI and WebSocket consumers can render them.

### Wiring

Called from both startup paths after `local.InjectLocalProvider` succeeds:

- `cmd/felix/main.go` (CLI chat startup)
- `internal/startup/startup.go` (gateway server / tray)

If `cfg.Local.Enabled == false`, skip bootstrap entirely (no sentinel written — re-enabling Local later triggers it).

---

## Section 3 — Onboarding, error handling, observability, testing

### Onboarding (`cmd/felix/main.go`)

The qwen/gemma `choose()` prompt block is **removed**. Replacement:

```go
if !hasCloudKey {
    fmt.Println("No cloud API key found. Felix will use the bundled local model.")
    fmt.Println("`gemma4:latest` (~9.6 GB) and `nomic-embed-text` (~270 MB)")
    fmt.Println("will download automatically in the background on first launch.")
    cfg.Agents.List[0].Model = "local/gemma4:latest"
    cfg.Providers["local"] = config.ProviderConfig{
        Kind:    "local",
        BaseURL: "http://127.0.0.1:18790/v1",
    }
    return finishOnboard(cfg)
}
```

The cloud-key path is unchanged — the user picks a cloud provider, but bootstrap still pulls `nomic-embed-text` if `Local.Enabled` is true (cheap, useful for memory embeddings even when the LLM is cloud).

### Error handling

| Failure | Behavior |
|---|---|
| Bundled Ollama daemon down at `cortex.Init` | `Init` returns nil + warn; agent runs without Cortex (existing behavior) |
| Cortex extraction model not pulled | `cortexadapter.IngestThread` call fails per-thread; `slog.Warn("cortex: ingest failed")`; one tray nudge per Felix process: "Pull `gemma4:latest` to enable knowledge graph." |
| Embedding model not pulled (Cortex side) | Cortex falls back to deterministic-only extraction. Warn once per process. |
| Embedding model not pulled (Memory side) | Memory falls back to BM25-only via existing nil-embedder path. Warn once per process. |
| Embedder probe fails (Ollama up but model missing) | Skip embedder construction; nudge: "Pull `nomic-embed-text` to enable semantic search." |
| First-run pull fails partway | Sentinel not written; warn + tray nudge: "Background download interrupted — will retry on next Felix launch." |
| User has no `local` provider configured (`Local.Enabled = false`) | Bootstrap is a no-op. Cortex/Memory fall back to whatever is explicitly configured. |

Tray nudges deduped by an in-memory `map[string]bool`, same pattern as compaction.

### Observability

```
slog.Info("first-run bootstrap start", "models", [...])
slog.Info("first-run model pulled", "model", "nomic-embed-text", "duration_ms", 4123)
slog.Info("first-run bootstrap complete", "duration_ms", 412345)
slog.Warn("first-run pull failed", "model", "...", "error", "...")
slog.Info("cortex auto-mirror", "agent_model", "local/gemma4:latest", "resolved_provider", "local", "resolved_model", "gemma4:latest")
slog.Warn("cortex: embedder unavailable, deterministic-only extraction", "reason", "...")
slog.Warn("memory: embedder unavailable, BM25-only", "reason", "...")
```

CLI renders one line per pull: `📥 Downloading nomic-embed-text … 47%`. Tray shows a notification on done/fail.

### Testing

| Package | What's tested |
|---|---|
| `internal/config/config_test.go` | New defaults present in `DefaultConfig()`. `Validate()` no longer backfills `Cortex.Provider`. Round-trip (load → save → load) preserves empty Cortex sentinels. |
| `internal/cortex/cortex_test.go` | Auto-mirror: empty `Cortex.Provider`+`LLMModel` parses default agent's model correctly. Local-provider branch attaches both LLM and embedder. Existing openai + anthropic branches unchanged. Test with fake `getProvider` callback returning canned `BaseURL`. |
| `internal/local/embed_dims_test.go` (new) | Lookup returns correct dims for known models; unknown returns sensible fallback (1536). |
| `internal/local/bootstrap_test.go` (new) | Sentinel present → no pull. Sentinel absent → pulls fired in order (nomic first, gemma4 second). Pull failure → sentinel NOT written. Re-run after success → no pull. `Local.Enabled = false` → no-op. Uses fake `PullFunc` injected via parameter. |
| `internal/local/pull_test.go` (new) | HTTP mock of Ollama `/api/pull` streaming response; verifies progress callbacks and error surfacing. |
| `internal/memory/embedder_test.go` (new) | Default model `"nomic-embed-text"` routes through OpenAI client with custom base URL; passes model name verbatim (not silently mapped to ada). |

### Code surface

| Where | What |
|---|---|
| `internal/config/config.go` | New defaults; remove Cortex.Provider backfill in `Validate()`. |
| `internal/cortex/cortex.go` | `Init` signature change; auto-mirror block; new `local` branch with embedder. |
| `internal/local/embed_dims.go` (new) | Embedding-model → dimensions map + `EmbeddingDims` lookup. |
| `internal/local/bootstrap.go` (new) | `EnsureFirstRunModels`; sentinel logic; goroutine kick-off; `BootstrapEvent` types. |
| `internal/local/pull.go` (new) | `PullModel` extracted from `cmd/felix/main.go` with progress callback. |
| `cmd/felix/main.go` | Drop qwen/gemma prompt; `pullLocalModel` becomes a thin wrapper around `local.PullModel`; call `EnsureFirstRunModels` after `InjectLocalProvider`; render bootstrap events in REPL. |
| `internal/startup/startup.go` | Same: call `EnsureFirstRunModels`; pass updated `cortex.Init` arguments. |
| `internal/agent/runtime.go` | New `EventBootstrap*` types added to existing `AgentEvent` union. |
| `internal/gateway/websocket.go` | Forward bootstrap events to subscribers. |
| `cmd/felix-app/` (tray) | Notification handlers for bootstrap + nudge events. |

### Defaults summary

| Knob | Default |
|---|---|
| Default agent model | `local/gemma4:latest` |
| Cortex provider/model | `""` / `""` (auto-mirror agent) |
| Memory embedding provider | `local` |
| Memory embedding model | `nomic-embed-text` |
| First-run bootstrap | enabled when `Local.Enabled = true` |
| Sentinel path | `~/.felix/.first-run-done` |
| Pull order | `nomic-embed-text` → `gemma4:latest` (sequential) |

---

## Open questions

None blocking. Possible follow-ups:

- Per-agent embedding overrides (some agents pin their own model).
- Re-pull/refresh button in the tray for users who want to re-grab the defaults after a deliberate `felix model rm`.
- Configurable bootstrap model list (advanced users add a code-tuned model to the first-run pull set).
