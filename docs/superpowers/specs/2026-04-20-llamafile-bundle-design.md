# Bundle Gemma 4 E4B llamafile in Felix

**Status:** Design approved, ready for implementation plan
**Date:** 2026-04-20

## Goal

Ship a self-contained local LLM with Felix so that a user can install Felix and run agents fully offline without needing an API key, an external Ollama install, or any model-management knowledge. The bundled model is Gemma 4 E4B (multimodal, 131K-capable) running through `llamafile` as a supervised child process exposing an OpenAI-compatible HTTP endpoint.

## Non-goals

- Replacing existing cloud providers (OpenAI, Anthropic, Gemini, Qwen) — they remain first-class.
- Replacing the existing Ollama integration — it stays as an independent provider.
- Supporting model swapping at runtime — the bundled model is fixed per release.
- GPU-specific build flavors — `llamafile` autodetects Metal / CUDA / CPU.

## Decisions (from brainstorming)

| Decision | Choice | Why |
|---|---|---|
| Model + quant | `gemma-4-E4B-it-Q4_K_M.gguf` (4.63 GB) + `mmproj-F16.gguf` (0.92 GB) | Best quality + vision; side-car GGUF avoids Windows 4 GB single-exe limit |
| Source | `huggingface.co/unsloth/gemma-4-E4B-it-GGUF` | Unsloth quant, Apache-2.0 |
| Distribution | Two release flavors: `felix` (~50 MB) and `felix-bundled` (~5.6 GB) | Lets cloud-only users skip the download |
| Small-build local UX | First-run prompt offers download | Discoverable for new users without forcing 5.6 GB |
| Default model | Smart detection: API key present → cloud; absent → local | Best out-of-box experience either way |
| Process model | Felix spawns and supervises `llamafile` as a child | Fully integrated lifecycle, single binary feel |
| Ollama coexistence | Independent providers (`local/...` vs `ollama/...`) | No detection logic; users opt in per agent |
| Context size | 64K (`-c 65536`) | Comfortable on 16 GB machines; well under 131K max |

## Architecture

```
felix process
├── gateway (HTTP/WebSocket :18789)
├── agent runtime
├── llm providers
│   ├── anthropic / openai / gemini / qwen   (cloud)
│   └── local                                 (NEW — OpenAI-compat client → 127.0.0.1:18790)
└── internal/local/Supervisor
    └── child: llamafile --server --port 18790 -m model.gguf --mmproj mmproj.gguf -c 65536 -ngl 999
```

The `local` provider is a thin alias over the existing OpenAI-compatible client with `BaseURL = http://127.0.0.1:18790/v1`. All multimodal and tool-call handling reuses existing code paths; the OpenAI client already encodes `ImageContent` as `image_url` content blocks, which `llamafile` accepts when `--mmproj` is loaded.

## File layout

### Bundled flavor — installed locations

**macOS (`Felix.app`, self-contained for Gatekeeper):**
```
Felix.app/Contents/MacOS/felix-app
Felix.app/Contents/Resources/skills/*.md
Felix.app/Contents/Resources/models/llamafile          (engine, ~30 MB)
Felix.app/Contents/Resources/models/gemma-4-e4b/
    model.gguf         (Q4_K_M, 4.63 GB)
    mmproj.gguf        (F16, 0.92 GB)
    SHA256SUMS
```

The `.pkg` symlinks `/usr/local/share/felix/models -> /Applications/Felix.app/Contents/Resources/models` so CLI users resolve the same files without doubling install size.

**Linux / Windows zips:**
```
felix-bundled-vX.Y.Z-{os}-{arch}/
    felix(.exe)
    skills/*.md
    models/llamafile(.exe)
    models/gemma-4-e4b/{model.gguf, mmproj.gguf, SHA256SUMS}
```

### Runtime model resolution (search order)

1. `$FELIX_MODELS_DIR` (env override)
2. `~/.felix/models/gemma-4-e4b/` (small-build first-run download lands here)
3. Bundled location (`/Applications/Felix.app/Contents/Resources/models/gemma-4-e4b/` on macOS — also reachable via the `/usr/local/share/felix/models` symlink the `.pkg` installs; `<binary-dir>/models/gemma-4-e4b/` on Linux/Windows)

The supervisor uses the first directory that contains both `model.gguf` and `mmproj.gguf` with matching SHA256 sums.

## Provider integration

### `internal/llm/provider.go` — one new switch case

```go
case "local":
    return NewOpenAIProvider("", opts.BaseURL), nil
```

The `local` provider surfaces `local/gemma-4-e4b-it` as a model option. No new client code; the existing OpenAI client handles streaming, tool calls, and vision.

### `felix.json5` — new top-level `local` block (bundled flavor only by default)

```json5
{
  "local": {
    "enabled": true,
    "port": 18790,
    "context_size": 65536,
    "gpu_layers": 999,        // 0 = force CPU
    "model_dir": ""           // optional override; empty = use search order
  },
  "providers": {
    "local": { "kind": "local", "base_url": "http://127.0.0.1:18790/v1" }
  }
}
```

`fsnotify` hot-reload applies most fields without restart; `port` and `model_dir` changes require supervisor restart.

## Process supervisor

### New package: `internal/local/`

```
internal/local/
    supervisor.go    // Start, Stop, IsReady, restart-with-backoff
    discover.go      // ResolveModelPaths(), Verify()
    config.go        // bundled-provider injection helper
```

### Lifecycle

On `felix start`, if `local.enabled == true` AND `discover.ResolveModelPaths()` succeeds:

```
llamafile --server --nobrowser \
          --host 127.0.0.1 --port 18790 \
          -m model.gguf --mmproj mmproj.gguf \
          -c 65536 -ngl 999 \
          --log-disable
```

- **Stdout/stderr** → `slog` at `debug` level.
- **Readiness** — poll `GET http://127.0.0.1:18790/health` with 30 s timeout; provider is marked unhealthy until ready.
- **Crash supervision** — exponential backoff `1s → 2s → 4s → 8s → 16s → 30s`, max 5 attempts within a 10-min window; after exhaustion, mark `local` provider unavailable.
- **Graceful shutdown** — on Felix SIGTERM, send SIGTERM to child, wait 5 s, then SIGKILL.
- **Port collision** — `net.Listen` probe before spawn; if `:18790` is taken, try `:18791..:18799`; if all taken, mark unavailable.

## Default-model selection (smart detection)

Runs once during onboarding (postinstall for `felix-bundled`, `felix start` first-run for both flavors):

```
if any of {OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY} env vars set
   OR any provider with non-empty api_key in felix.json5:
       default_model = "openai/gpt-5.4"           (current default, unchanged)
else if local model files resolve:
       default_model = "local/gemma-4-e4b-it"
else:
       fall through to existing onboarding wizard
```

## First-run UX

### Small `felix` build

The existing wizard (`cmd/felix/main.go:1157`) gains a new top option:

```
Pick your starting model:
  1) Local — Gemma 4 E4B (download ~5.6 GB, runs offline)   ← new, recommended
  2) OpenAI / GPT
  3) Anthropic / Claude
  4) Google / Gemini
  5) Ollama (local — no key needed)
```

Picking #1 streams `Q4_K_M` + `mmproj-F16` from Hugging Face and the platform-appropriate `llamafile` binary from its GitHub release into `~/.felix/models/gemma-4-e4b/`, with progress bar, HTTP `Range` resume, and SHA256 verify. Aborting cleans up partial files.

### `felix-bundled` build

Files already present → wizard skips download, confirms `local/gemma-4-e4b-it` as default, starts supervisor.

## New CLI subcommand: `felix model`

| Subcommand | Effect |
|---|---|
| `felix model pull` | Manual download (same code path as wizard option #1) |
| `felix model status` | Show resolved model dir, file sizes, SHA verify result, supervisor state |
| `felix model rm` | Delete files from `~/.felix/models/gemma-4-e4b/` |
| `felix model assemble <prefix>` | Join `split`-produced parts (for offline `felix-bundled` install via download) |

## Build & release

### Makefile additions

```makefile
## models-fetch: download Gemma 4 E4B + mmproj into models/ for bundling
models-fetch:
	mkdir -p models/gemma-4-e4b
	curl -L -o models/gemma-4-e4b/model.gguf \
	     https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/gemma-4-E4B-it-Q4_K_M.gguf
	curl -L -o models/gemma-4-e4b/mmproj.gguf \
	     https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/mmproj-F16.gguf
	cd models/gemma-4-e4b && shasum -a 256 *.gguf > SHA256SUMS

## llamafile-fetch: download platform-specific llamafile engine
llamafile-fetch:
	mkdir -p models/engine
	# pulls latest release for darwin/linux/windows × amd64/arm64

## release-bundled: build felix-bundled flavor
release-bundled: models-fetch llamafile-fetch
	# per-platform: zip binary + skills + models/ + split into 1.9 GB parts
```

### GitHub release artifacts

- `felix-vX.Y.Z-{os}-{arch}.zip` — small flavor (unchanged)
- `felix-bundled-vX.Y.Z-{os}-{arch}.zip.part-{aa,ab,ac}` — split bundled (GitHub asset limit is 2 GB)
- `Felix-vX.Y.Z.pkg` — small macOS installer
- `Felix-bundled-vX.Y.Z.pkg.part-{aa,ab,ac}` — split macOS bundled
- `MODELS-SHA256SUMS` — verifies reassembled model files

### `postinstall` script

When a `models/` directory exists in the payload, skip the provider-key wizard and inject the `local` provider block into `~/.felix/felix.json5`.

## Error handling

| Failure | Detection | Response |
|---|---|---|
| Model files missing or wrong SHA | `Verify()` at supervisor start | Log error, mark `local` unavailable, fall back to next provider; `felix model status` shows mismatch |
| `llamafile` binary missing / non-executable | `os.Stat` + exec test at supervisor start | Provider unavailable; CLI message points to `felix model pull` |
| Port `:18790` already in use | `net.Listen` probe before spawn | Try `:18791..:18799`; if all taken, unavailable |
| `llamafile` crashes during runtime | Child process exit | Backoff restart (1s→30s, 5 attempts); after exhaustion, unhealthy; in-flight requests get HTTP 503 |
| `llamafile` hangs | 30 s readiness timeout; 60 s request timeout | Kill child, restart |
| OOM during inference | Child exits with signal | Treated as crash; log includes "consider lowering `local.context_size`" hint |
| Partial download interrupted | Size mismatch on resume / SHA mismatch | Resume via HTTP `Range`; if SHA still mismatches, delete and require re-pull |
| Disk full during download | `io.Copy` returns ENOSPC | Delete partial file, surface error, suggest `felix model rm` |

## Test plan

| Layer | Path | What it verifies |
|---|---|---|
| Unit | `internal/local/discover_test.go` | Search-order resolution, SHA verification, missing files |
| Unit | `internal/local/supervisor_test.go` | Fake binary exits/hangs/crashes; assert backoff, port probing, graceful shutdown |
| Integration | `internal/local/integration_test.go` (build tag `local`, env `FELIX_LOCAL_MODEL_DIR`) | Real `llamafile` against tiny GGUF (Qwen 0.5B Q4); end-to-end OpenAI-compat round-trip including vision |
| E2E | `make test-bundled` | Extract bundled release into temp dir, run `felix start`, send chat via WebSocket, assert response |

## Open questions for implementation plan

- Exact `llamafile` engine version pin (current latest at time of release).
- Whether `felix model pull` should default to a different quant on memory-constrained hosts (auto-pick `Q3_K_M` when total RAM < 8 GB?). For now: no — keep behavior predictable.
- Whether to expose `local.gpu_layers` in the wizard or keep it config-only. For now: config-only.
