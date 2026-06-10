# Bundle Ollama in Felix

**Status:** Design approved, ready for implementation plan
**Date:** 2026-04-20
**Supersedes:** `2026-04-20-llamafile-bundle-design.md` (reverted — supervisor complexity and llamafile-specific issues)

## Goal

Ship Felix so the user can install one binary/package and run agents fully offline, with no separate Ollama install, no manual model download instructions, and no API key. Updating to newer/better models is an `ollama pull` away — Felix never needs a release for that.

## Non-goals

- Replacing existing cloud providers (Anthropic, OpenAI, Gemini, Qwen) — they remain first-class.
- Replacing Felix's existing user-installed `ollama` provider — it stays. This design adds a separate, Felix-managed Ollama instance that coexists with no detection logic.
- Hot-swapping engines at runtime — engine is fixed: bundled Ollama.

## Decisions (from brainstorming)

| Decision | Choice | Why |
|---|---|---|
| Engine | Bundle official `ollama` binary per platform | Mature, stable, low surface area; we own none of the inference code |
| Model file | Pulled on first run via `ollama pull` | Keeps release small (~80–100 MB total); user picks model in wizard |
| Wizard | Curated short list (3 options) at first run | Lets user trade size vs. quality without paralysis |
| Lifecycle | Felix spawns `ollama serve` as child, kills on shutdown | Self-contained UX; no per-platform system-service install glue |
| Supervisor | Minimal — spawn + readiness poll + signal-on-exit. No backoff, no auto-restart, no health-poll loop | Direct response to "supervisor complexity" being a reason for the previous revert |
| Port | Bundled Ollama on `:18790`, falling back through `:18791..:18799` | Avoids colliding with a user's system Ollama on `:11434`; 10-port window handles multi-instance |
| Model storage | `~/.felix/ollama/models/` via `OLLAMA_MODELS` env | Keeps Felix's models separate from any system Ollama; uninstall removes cleanly |
| Provider in Felix config | New `local` preset pointing at bound Ollama port (OpenAI-compat endpoint) | Reuses existing OpenAI-compatible client code; existing `ollama/...` provider untouched |
| Release shape | Single release flavor (no more "small vs bundled") | Bundle delta is just the small Ollama binary |

## Architecture

```
felix process
├── gateway (HTTP/WebSocket :18789)
├── agent runtime
├── llm providers
│   ├── anthropic / openai / gemini / qwen        (cloud, unchanged)
│   ├── ollama                                     (user's existing system Ollama, unchanged)
│   └── local                                      (NEW — points at Felix-managed Ollama on :18790+)
└── internal/local/Supervisor                      (NEW — minimal child-process manager)
    └── child: ollama serve
                env: OLLAMA_HOST=127.0.0.1:<bound-port>
                     OLLAMA_MODELS=~/.felix/ollama/models
                     OLLAMA_KEEP_ALIVE=5m
```

### Component responsibilities

- **`internal/local/Supervisor`** — owns the `ollama serve` child. ~150 LOC. Three methods: `Start(ctx)`, `Stop()`, `Healthy() bool`, plus `BoundPort() int`. No restart logic. No exponential backoff. No background health-poll loop. If it crashes, `Healthy()` returns false until next Felix restart and the local provider returns 503.

- **`internal/local/Installer`** — owns model pull/list/delete. Talks to Ollama's `/api/pull`, `/api/tags`, `/api/delete` with streaming progress. Same package backs the `felix model` CLI subcommands.

- **`local` provider** — thin alias over the existing OpenAI-compatible client with `BaseURL = http://127.0.0.1:<bound-port>/v1`. Zero new client code: Ollama's OpenAI-compat endpoint handles streaming, tool calls, and (for vision models) image inputs.

### Key flows

1. **Felix startup:**
   `Supervisor.Start()` → probe `[18790, 18799]` for free port → spawn `ollama serve` → poll `/api/version` (250 ms × 60 s) → ready. If all 10 ports taken, mark `local` provider unavailable.

2. **First-run wizard:**
   No API key detected → wizard shows curated 3-model list → user picks → `Installer.Pull(model)` with progress bar → write `default_model = "local/<picked>"` to `~/.felix/felix.json5`.

3. **Felix shutdown:**
   On SIGTERM, send SIGTERM to `ollama serve`, wait 5 s, then SIGKILL.

4. **Updating models:**
   User runs `felix model pull <name>` (or `OLLAMA_HOST=127.0.0.1:18790 ollama pull <name>` directly). Felix never needs a release for this.

### Deliberate non-features (lessons from the revert)

- No exponential-backoff restart — one shot, then unhealthy.
- No background health-poll loop — health is process-alive + last-request-succeeded.
- No SHA verification of the engine binary at runtime — verified at build time, runtime trusts the bundle.
- No graceful crash recovery — first crash is the last crash; user restarts Felix.

## First-run UX

### Trigger

During onboarding (postinstall on packaged installs, first `felix start` otherwise):

```
if any of {OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY} env vars set
   OR any provider with non-empty api_key in felix.json5:
       default_model = "openai/gpt-5.4"          (current default, unchanged)
else:
       run local-first wizard
```

### Wizard

```
Felix didn't detect a cloud API key. Pick a local model to download:

  1) Llama 4.1 8B Instruct                ~4.7 GB   recommended — good general agent
  2) Qwen 3.5 Coder 7B                    ~4.0 GB   best for code-heavy tasks
  3) Gemma 4 E4B (multimodal)             ~5.5 GB   vision-capable
  4) Skip — I'll configure a cloud key later

> _
```

Picking 1–3:
- Free-disk pre-check; abort with helpful message if insufficient.
- Stream-pull via `/api/pull` with progress bar (bytes / total / throughput).
- On success: write `default_model = "local/<picked>"` and `providers.local = { kind: "local", base_url: "http://127.0.0.1:<bound-port>/v1" }` to `~/.felix/felix.json5`.
- Ctrl-C cancels — partial blob is left to Ollama's resume mechanism.

Picking 4: skip wizard, leave Felix without a default model; existing "configure provider" flow available later.

### `felix model` CLI subcommands

| Subcommand | Effect |
|---|---|
| `felix model pull <name>` | Pull any Ollama-registry model into `~/.felix/ollama/models/`. |
| `felix model list` | Show locally-pulled models with sizes (wraps `/api/tags`). |
| `felix model rm <name>` | Delete a model (wraps `/api/delete`). |
| `felix model status` | Show: supervisor state, bound port, model dir, default model, free disk. |

## Supervisor

### Package layout — `internal/local/`

```
internal/local/
    supervisor.go      ~150 LOC — Start, Stop, BoundPort, Healthy
    installer.go       ~200 LOC — Pull, List, Delete, Status (Ollama HTTP API wrapper)
    discover.go        ~50 LOC  — binary path resolution
    config.go          ~50 LOC  — provider injection helper
```

### `Supervisor` interface

```go
type Supervisor struct {
    binPath    string         // path to bundled ollama binary
    modelsDir  string         // ~/.felix/ollama/models
    boundPort  int            // set after Start
    cmd        *exec.Cmd
    cancelCtx  context.CancelFunc
}

func New(binPath, modelsDir string) *Supervisor
func (s *Supervisor) Start(ctx context.Context) error  // blocks until ready or fails
func (s *Supervisor) Stop() error                       // SIGTERM → 5 s → SIGKILL
func (s *Supervisor) BoundPort() int
func (s *Supervisor) Healthy() bool                     // is child process alive?
```

### Start sequence

1. Find a free port in `[18790, 18799]` via `net.Listen` probe; close listener immediately. If all 10 fail → return `ErrNoFreePort`.
2. `exec.Command(binPath, "serve")` with env: `OLLAMA_HOST=127.0.0.1:<port>`, `OLLAMA_MODELS=<modelsDir>`, `OLLAMA_KEEP_ALIVE=5m`.
3. Wire `cmd.Stdout` / `cmd.Stderr` to `slog` at debug level (one goroutine each).
4. `cmd.Start()`.
5. Poll `GET http://127.0.0.1:<port>/api/version` every 250 ms for up to 60 s. First 200 → ready.
6. If readiness times out → SIGKILL child, return `ErrNotReady`.

### Stop sequence

1. SIGTERM child.
2. Wait up to 5 s for exit.
3. SIGKILL if still running.
4. Cancel the stdout/stderr forwarder goroutines.

### Runtime behavior — explicit non-features

| What the previous supervisor did | What this one does |
|---|---|
| Exponential-backoff restart (1 s → 30 s, 5 attempts in 10 min) | **Nothing.** Child exit → `Healthy()` false, requests get 503, log a clear error. User restarts Felix. |
| Background health-poll loop | **Nothing.** Health is process-alive + last-request-succeeded. |
| Model SHA verification at startup | **Nothing.** Ollama owns its models directory. |
| Crash counter / failure window tracking | **Nothing.** First crash is the last crash. |

### Failure isolation

If `Start()` fails for any reason, Felix continues to start — `local` provider is just marked unavailable. Cloud providers (if configured) still work; CLI/UI render the helpful error. **Felix never crashes because Ollama failed.**

## Provider integration

### `internal/llm/provider.go` — one new switch case

```go
case "local":
    return NewOpenAIProvider("", opts.BaseURL), nil
```

Zero new client code: the existing OpenAI client handles streaming, tool calls, and `image_url` content blocks; Ollama's OpenAI-compat endpoint accepts all of these.

### `felix.json5` — new top-level `local` block

```json5
{
  "local": {
    "enabled": true,             // master switch; false disables supervisor entirely
    "models_dir": "",            // optional override; empty → ~/.felix/ollama/models
    "keep_alive": "5m"           // OLLAMA_KEEP_ALIVE
  },
  "providers": {
    "local": {
      "kind": "local",
      "base_url": "http://127.0.0.1:18790/v1"   // updated by supervisor on startup if port falls back
    }
  }
}
```

### Hot-reload behavior (existing `fsnotify`)

- `keep_alive` change → requires Felix restart (Ollama reads `OLLAMA_KEEP_ALIVE` from spawn env).
- `enabled` toggle → requires Felix restart (don't start/stop supervisor mid-session).
- `models_dir` change → requires Felix restart.

### Model surfacing

Supervisor calls `/api/tags` at startup and after each `felix model pull`/`rm`, exposes the list to the existing model picker. Each model appears as `local/<ollama-name>` (e.g. `local/llama4.1:8b`, `local/qwen3.5-coder:7b`).

### Coexistence with existing `ollama` provider

The user's separately-configured `ollama` provider (typically pointing at `:11434`) continues to work unchanged. The new `local` provider is an additional, Felix-managed instance. Two providers, no detection, no conflict.

## File layout, build, and release

### Bundled engine path inside the Felix release

```
felix-vX.Y.Z-{os}-{arch}/
    felix(.exe)
    skills/*.md
    bin/ollama(.exe)              ← NEW: bundled Ollama binary, ~30–50 MB per platform
```

For `Felix.app` (macOS):

```
Felix.app/Contents/MacOS/felix-app
Felix.app/Contents/Resources/skills/*.md
Felix.app/Contents/Resources/bin/ollama         ← bundled
```

### Runtime resolution of the Ollama binary (search order)

1. `$FELIX_OLLAMA_BIN` (env override — for dev/testing).
2. `<dir-of-felix-binary>/bin/ollama(.exe)` — works for the unpacked zip.
3. Platform-specific bundle path:
   - macOS: `<Felix.app>/Contents/Resources/bin/ollama`.
   - Linux/Windows: same as #2.
4. `$PATH` — last resort, lets devs run against system Ollama.

If none resolve → log clear error, mark `local` provider unavailable, Felix continues.

### Models directory

- Default: `~/.felix/ollama/models/` (set via `OLLAMA_MODELS` env).
- Override: `local.models_dir` in `felix.json5`.
- Deliberately **not** `~/.ollama/models/` — keeps Felix's model store separate from any system Ollama install.

### Build & release additions (`Makefile`)

```makefile
## ollama-fetch: download platform-specific Ollama binary into bin/
ollama-fetch:
	mkdir -p bin
	# pulls Ollama release pinned by OLLAMA_VERSION for darwin/linux/windows × amd64/arm64

## release: build felix per-platform with bundled Ollama
release: ollama-fetch
	# per-platform: zip felix binary + skills + bin/ollama
	# single release flavor — bundle is small enough to skip splits
```

`OLLAMA_VERSION` is pinned in the Makefile; bumping it is a one-line PR. The pinned binary's SHA256 lives in a checked-in `OLLAMA-SHA256SUMS` and is verified at build time (not runtime).

### Single release flavor

No more "small vs bundled". Release size goes from ~50 MB → ~80–100 MB depending on platform.

### GitHub release artifacts

- `felix-vX.Y.Z-{os}-{arch}.zip` — single flavor with bundled Ollama.
- `Felix-vX.Y.Z.pkg` — macOS installer (single flavor).
- `OLLAMA-SHA256SUMS` — for transparency / supply-chain auditing.

### License/attribution

Ollama is MIT-licensed; bundle a `LICENSE-OLLAMA` file alongside Felix's own license.

## Error handling

| Failure | Detection | Response |
|---|---|---|
| Ollama binary missing or non-executable | `os.Stat` + exec test at `Supervisor.Start()` | Clear error pointing at expected path; mark `local` unavailable; Felix continues |
| All ports `:18790–:18799` taken | `net.Listen` probe loop | Log error listing range; `local` unavailable; suggest checking for other Felix instances |
| Ollama fails to become ready in 60 s | Readiness poll timeout | SIGKILL child; log error; `local` unavailable |
| Ollama crashes during runtime | `cmd.Wait()` in goroutine | `Healthy()` flips to false; in-flight + new requests get HTTP 503; log "Ollama exited unexpectedly, restart Felix"; **no restart attempt** |
| Model pull fails (network, disk full, etc.) | Error from `/api/pull` stream | Surface to wizard / CLI with underlying error; partial blob left for Ollama's resume |
| Insufficient disk for model pull | Free-space check before pull | Refuse with message: "Need ~5 GB free, have X GB" |
| Model not found in UI after pull | `/api/tags` cache stale | `felix model pull` and `rm` invalidate the cache; UI refreshes |
| User has system Ollama on `:11434` | N/A — bundled instance uses `:18790+` | No conflict; existing user-configured `ollama` provider continues to point at `:11434` |
| User runs two Felix instances | Second one's port probe falls through to `:18791..:18799` | Both work, each with its own bound port; their `local` provider URLs differ |
| `felix.json5` has `local.enabled: false` | Read at startup | Supervisor never starts; `local` provider unavailable; no error |
| Bundled Ollama version drifts from Felix expectations | Future risk | Pinned version per release; bumping Ollama is a deliberate PR |

## Test plan

| Layer | Path | What it verifies |
|---|---|---|
| Unit | `internal/local/supervisor_test.go` | Fake binary scripts (sleep, exit, hang); assert: port-probe loop tries `18790..18799` then errors, readiness timeout SIGKILLs child, Stop() escalates SIGTERM → SIGKILL after 5 s, crash flips `Healthy()` to false with no restart |
| Unit | `internal/local/installer_test.go` | Mock Ollama HTTP server (`/api/pull`, `/api/tags`, `/api/delete`); assert: progress streaming, error surfacing, free-disk pre-check |
| Unit | `internal/local/discover_test.go` | Binary resolution search order (env → bin/ → bundle path → PATH); each path tested in temp dir |
| Unit | `internal/local/config_test.go` | Provider injection helper writes correct `local` block with bound port |
| Integration | `internal/local/integration_test.go` (build tag `local`, env `FELIX_OLLAMA_BIN`) | Real bundled Ollama against tiny model (`qwen2.5:0.5b`); assert end-to-end OpenAI-compat round-trip including streaming and tool calls |
| Integration | `cmd/felix/model_cmd_test.go` | Run `felix model pull/list/rm/status` against real Ollama with tiny model; assert exit codes and stdout |
| E2E | `make test-release` | Extract built release zip into temp dir, run `felix start`, send chat over WebSocket targeting `local/qwen2.5:0.5b`, assert response |

### What's deliberately not tested

- Crash recovery (no recovery exists by design).
- Port-hopping under stress (10-port range, fail-fast semantics make this trivial).
- SHA verification (not done at runtime).

## Open questions for implementation plan

1. Exact Ollama version pin — pick latest stable at implementation time; record in `Makefile` as `OLLAMA_VERSION` and in `OLLAMA-SHA256SUMS`.
2. Final curated wizard list — Qwen 3.5 9B / Gemma 4 E4B is a placeholder; confirm exact Ollama tags + sizes at implementation time.
3. Default quant per model (e.g., `llama4.1:8b-instruct-q4_K_M` vs. `llama4.1:8b`). Pick after a quick local sanity check.
4. Postinstall vs. first-`felix start` wizard placement — keep the previous design's split (postinstall for packaged, first-start for zip) or unify on first-start? Implementation choice.
5. Whether `felix model` should pass-through arbitrary `ollama` commands (e.g., `felix model show`, `felix model cp`). Start with the four documented subcommands; add more if user demand emerges.
