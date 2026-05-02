![Felix](felix.jpg)

# Felix

A self-hosted AI agent gateway written in Go. Single binary, low memory, runs entirely on your own machine.

Felix connects you (via CLI or web chat) to LLMs — Claude, GPT, Gemini, Qwen, Ollama, or any OpenAI-compatible endpoint — and lets agents execute tasks on your hardware using a fixed registry of in-process tools plus any number of remote MCP servers.

---

## Three pillars

Every design decision in Felix flows from three commitments:

1. **Self-sufficient.** Felix runs on one machine, owns its own state, and has no required network dependency. The LLM can be local. The vector index is in-process. The knowledge graph is a SQLite file. There is no Felix cloud, no Felix account, no Felix backend that anyone could turn off.
2. **Robust.** Long-running agents touch files, shell out, talk to flaky APIs, and accumulate state across restarts. Every external call has a timeout. Every queue has a cap. Every per-call resource has a paired cleanup. On-disk state heals itself on the next load.
3. **Usable out of the box by non-technical people.** The default install — no config edits, no API keys, no `vim` — must just work. A first-time user installs Felix, clicks through onboarding, and has a useful agent running locally within minutes. Advanced configuration can be as complex as it needs to be, but it must not be in the way of the default path.

---

## Features

- **Single binary** — no runtime dependencies, no Node.js, no npm. Download and run.
- **System tray app** — runs the gateway in the background with a tray icon, web chat, and one-click access to settings (macOS and Windows).
- **Two interfaces** — local CLI (`felix chat`) and a web chat page served by the gateway.
- **Bundled local LLM** — ships an Ollama binary so you can run agents with no API key. Downloads Gemma4 on first startup if it doesn't find any other models.
- **Model-agnostic** — Claude, GPT, Gemini, Qwen, Ollama, LM Studio, DeepSeek, or any OpenAI-compatible API.
- **Multi-agent** — multiple agents with different models, tools, and personas.
- **Extended reasoning** — `reasoning: off|low|medium|high` per-agent, mapped to Claude thinking budgets, OpenAI o-series `reasoning_effort`, and Gemini 2.5 thinking config.
- **Context window auto-detection with override** — Felix infers a model's context window from its identifier (handles proxy prefixes like `platformai/claude-sonnet-…` correctly); a per-agent `contextWindow` config field lets you pin a specific value when the proxy or fine-tune doesn't match a known family. Defaults: 32k for unknown local/Ollama models, 128k for unknown remote models.
- **Cross-provider tool portability** — JSON Schema fields one provider rejects (Gemini drops `anyOf`/`oneOf`/`format`; OpenAI drops `$ref`/`definitions`) are stripped at the provider boundary so a single tool definition works across providers.
- **MCP client** — connect to external [Model Context Protocol](https://modelcontextprotocol.io) servers over Streamable-HTTP or stdio. OAuth2 (client credentials, authorization code + PKCE) and bearer auth supported. Expired tokens trigger an inline "Re-authenticate" button in the chat — no restart needed.
- **Persistent memory** — BM25 lexical search over Markdown files, recalled automatically each turn. Optional vector search via `chromem-go` when an embedding provider is configured.
- **Cortex knowledge graph** — optional SQLite-backed knowledge graph (enabled by default) that ingests completed conversations and surfaces relevant facts on subsequent turns.
- **Skill system** — Markdown files with YAML frontmatter, selectively injected per-turn based on relevance. Bundled starter skills (`ffmpeg`, `imagemagick`, `pandoc`, `pdftotext`, `cortex`) seeded on first run; manage user skills from the Settings UI without restart.
- **Cron jobs** — recurring prompts on configurable intervals, with pause/resume/remove management.
- **Subagents** — opt-in agents become invocable via the `task` tool, so a supervisor can delegate work to a specialist with its own model and tool policy.
- **Vision/image support** — paste or drop image paths in CLI/web chat and the LLM analyzes them.
- **Tool policies** — per-agent allow/deny lists for every built-in and MCP-provided tool.
- **Session persistence** — append-only JSONL files with DAG structure and branching. Compaction is splice-based, never destructive.
- **Smart compaction** — token-threshold or message-count triggered, with a structured prompt; three-stage fallback chain (full → small-only → placeholder) and a per-session circuit breaker. Compaction can run asynchronously between turns: when the next turn would push past the threshold, Felix kicks off summarization in the background and the just-finished turn returns immediately; the next user message either finds the work done or briefly waits on the in-flight handle.
- **Cache-stability invariant** — request prefixes are byte-stable across turns (sorted tool definitions, deterministic schema normalization) so Anthropic and OpenAI prompt caches keep hitting.
- **Stream-failure resilience** — when a streaming response dies mid-flight (TCP reset, idle timeout, partial SSE) the runtime discards the partial output and retries via the provider's non-streaming endpoint, preserving the byte-identical prompt prefix.
- **Config hot-reload** — edit `felix.json5` while running, changes apply immediately.
- **WebSocket API** — JSON-RPC 2.0 control plane for programmatic access.
- **Local-first** — all data lives on your filesystem, no external database required.

---

## Quick Start

### Build

```bash
make build              # Build the CLI binary
make build-app          # Build the macOS menu bar app (Felix.app)
make build-app-windows  # Build the Windows system tray app (felix-app.exe)
```

### Setup

```bash
./felix onboard
```

The wizard walks you through choosing an LLM provider and entering your API key. If you skip the cloud providers, Felix configures the bundled Ollama with `gemma4:latest` so you have a working agent with zero credentials.

### Chat

```bash
# Interactive CLI session (no gateway needed)
./felix chat

# Start the full gateway (web chat, settings UI, WebSocket API)
./felix start

# Or launch the system tray app
open Felix.app              # macOS
felix-app.exe               # Windows
```

### Verify

```bash
./felix doctor
```

### Bundled local LLM (no API key needed)

Felix ships with a bundled Ollama binary so you can run agents offline with no API key. On first run it pulls `gemma4:latest` (chat) and `nomic-embed-text` (memory embeddings) in the background.

To pull additional models later:

```bash
felix model pull qwen2.5:0.5b
felix model list
felix model status
felix model rm qwen2.5:0.5b
```

The bundled Ollama runs as a child of Felix on `127.0.0.1:18790` (next free port in `:18790–:18799`) and shuts down when Felix exits. It does not interfere with any system Ollama you may have on `:11434`.

---

## CLI Commands

| Command | Description |
|---------|-------------|
| `felix onboard` | Interactive setup wizard |
| `felix start` | Start the gateway server |
| `felix start -c path/to/config.json5` | Start with a custom config |
| `felix chat` | Interactive CLI chat with the default agent |
| `felix chat myagent` | Chat with a specific agent |
| `felix chat -m openai/gpt-5.4` | Chat with a model override |
| `felix clear [agent]` | Clear the local CLI session history for an agent |
| `felix sessions [agent]` | List all sessions for an agent |
| `felix model list \| pull <name> \| rm <name> \| status` | Manage local Ollama models pulled by Felix |
| `felix mcp login <id>` | Run interactive OAuth login for an MCP server (alternative to the in-chat re-auth button) |
| `felix status` | Query the running gateway for agent status |
| `felix doctor` | Run diagnostic checks |
| `felix version` | Print version and commit info |

---

## System Tray App

Felix ships a system tray app that runs the gateway as a background service. Supported on macOS and Windows.

The tray app is a thin launcher: it spawns `felix start` as a separate child process, polls `/health` for readiness, then opens the chat in your browser. The gateway runs in its own process group, so when macOS reaps the menubar app under workspace events (display sleep, fast user switching, screen lock, memory pressure), only the tray dies — the gateway is reparented to launchd and your active chat keeps working in the browser. Relaunching the tray detects the live gateway via `/health` and reattaches instead of spawning a duplicate.

### Build

```bash
make build-app          # macOS — produces Felix.app (bundles felix-app + felix + ollama)
make build-app-windows  # Windows — produces felix-app.exe and felix.exe (ship side-by-side)
```

### Launch

- **macOS:** Double-click `Felix.app` or drag it to `/Applications`.
- **Windows:** Double-click `felix-app.exe`.

### Menu items

| Item | Action |
|------|--------|
| **Chat** | Opens the web chat in your default browser |
| **Jobs** | Opens the cron jobs dashboard (`/jobs`) |
| **Logs** | Opens the live logs view (`/logs`) |
| **Settings** | Opens the Settings UI (`/settings`) — tabs: Agents, Providers, Models, Intelligence, Security, Messaging, MCP, Skills, Memory, Gateway |
| **Restart** | Stops the gateway subprocess and respawns a fresh one |
| **Quit** | Sends SIGTERM to the gateway's process group (with a 15s graceful-exit budget; SIGKILL fallback). Cleanup runs the bundled Ollama supervisor first so a force-killed gateway still leaves no orphaned ollama. The whole sequence is bounded by an outer 25s deadline. |

### Web chat interface

The app serves a chat page at `http://localhost:18789/chat`. Features:

- Agent + session selectors — switch between configured agents and prior sessions without leaving the page.
- Streaming responses via WebSocket.
- Light/dark mode toggle (persisted in browser).
- Inline tool-call display with collapsible output.
- Inline "Re-authenticate" button when an MCP server's OAuth token expires.
- Always-visible token chip showing usage vs. context window for the current agent.
- Live trace panel for per-turn timing and phase breakdown.
- Markdown rendering (headings, code blocks, tables, lists, bold, italic, links).

### Environment variables

**macOS:** `.app` bundles don't inherit shell environment variables. Felix.app automatically loads your shell profile (`~/.zshrc`, `~/.bashrc`) at startup, so API keys set via `export ANTHROPIC_API_KEY=...` work as expected.

**Windows:** Set environment variables via System Settings or PowerShell:

```powershell
[System.Environment]::SetEnvironmentVariable("ANTHROPIC_API_KEY", "sk-ant-...", "User")
```

On both platforms, you can set API keys directly in the config file instead of using environment variables.

---

## Architecture

Single-process, hub-and-spoke. All components run in one binary.

- **Gateway Server** — HTTP + WebSocket on `:18789` using chi router + gorilla/websocket.
- **Agent Runtime** — the think-act loop: assemble static + dynamic system prompt, stream LLM response, partition tool calls into parallel batches, dispatch and re-loop.
- **LLM Provider abstraction** — one `LLMProvider` interface, six implementations (`anthropic`, `openai`, `gemini`, `qwen`, `local`, `openai-compatible`).
- **Session Manager** — append-only JSONL with a DAG view; compaction is splice-based.
- **Memory Manager** — BM25 always present, vector search optional via `chromem-go`.
- **Cortex** — optional SQLite knowledge graph for cross-conversation recall.
- **Skill loader** — embedded starter skills + user skills, hot-reloaded.
- **Compaction Manager** — three-stage fallback, per-session circuit breaker, async between-turns.
- **MCP Manager** — Streamable-HTTP and stdio clients with OAuth2 and bearer auth, with in-process re-authentication.
- **Cron** — recurring prompts on schedules; jobs added via the `cron` tool or static config, with pause/resume/remove management.
- **Bundled Ollama supervisor** — keeps a local LLM available without external setup.

The system tray app is a thin launcher around the gateway — see the next section.

---

## Configuration

All configuration lives in `~/.felix/felix.json5` (JSON5 format for comments and trailing commas).

### LLM Providers

| Kind | Description | Requires |
|------|-------------|----------|
| `anthropic` | Anthropic's Claude API | `api_key` |
| `openai` | OpenAI's API (GPT models) | `api_key` |
| `gemini` | Google's Gemini API | `api_key` |
| `qwen` | Alibaba Cloud's Qwen (Tongyi Qianwen) API | `api_key` |
| `openai-compatible` | Any OpenAI-compatible API (Ollama, LM Studio, DeepSeek, LiteLLM, etc.) | `base_url`, optionally `api_key` |
| `local` | Bundled local LLM runtime (Ollama supervised by Felix) — wired up automatically by onboarding | none |

```json5
{
  "providers": {
    "anthropic":   { "kind": "anthropic", "api_key": "sk-ant-..." },
    "openai":      { "kind": "openai",    "api_key": "sk-..." },
    "gemini":      { "kind": "gemini",    "api_key": "AIza..." },
    "qwen":        { "kind": "qwen",      "api_key": "sk-..." },
    "ollama":      { "kind": "openai-compatible", "base_url": "http://localhost:11434/v1" },
    "lmstudio":    { "kind": "openai-compatible", "base_url": "http://localhost:1234/v1" },
    "deepseek":    { "kind": "openai-compatible", "api_key": "sk-...", "base_url": "https://api.deepseek.com/v1" }
  }
}
```

### Model references

Agents reference models as `provider/model-name`, where the provider name matches a key in the `providers` section:

```json5
"model": "anthropic/claude-sonnet-4-5-20250514"
"model": "openai/gpt-5.4"
"model": "gemini/gemini-2.5-flash"
"model": "qwen/qwen-plus"
"model": "local/gemma4"
"model": "deepseek/deepseek-chat"
```

### API keys via environment variables

Environment variables take precedence over config file values:

```bash
export ANTHROPIC_API_KEY="sk-ant-api03-..."
export OPENAI_API_KEY="sk-proj-..."
export GEMINI_API_KEY="AIza..."
export QWEN_API_KEY="sk-..."
```

The naming convention is `{PROVIDER}_API_KEY` (or `{PROVIDER}_AUTH_TOKEN`), and `{PROVIDER}_BASE_URL` for custom endpoints — where `{PROVIDER}` is the uppercased provider name from your config.

### MCP servers

Felix can connect to external [Model Context Protocol](https://modelcontextprotocol.io) servers and expose their tools to agents alongside built-ins:

```json5
{
  "mcp_servers": [
    // HTTP transport with OAuth2 client-credentials.
    {
      "id": "remote-tools",
      "transport": "http",
      "enabled": true,
      "tool_prefix": "remote_",
      "http": {
        "url": "https://mcp.example.com/v1",
        "auth": {
          "kind": "oauth2_client_credentials",
          "token_url": "https://auth.example.com/oauth/token",
          "client_id": "felix-prod",
          "client_secret_env": "REMOTE_MCP_SECRET",
          "scope": "mcp.read mcp.write"
        }
      }
    },

    // HTTP transport with OAuth2 authorization code + PKCE.
    // First-time setup via `felix mcp login <id>` or the in-chat
    // re-auth button after a token expires.
    {
      "id": "user-tools",
      "transport": "http",
      "enabled": true,
      "http": {
        "url": "https://mcp.example.com/v1",
        "auth": {
          "kind": "oauth2_authorization_code",
          "auth_url": "https://auth.example.com/oauth/authorize",
          "token_url": "https://auth.example.com/oauth/token",
          "client_id": "felix-cli",
          "redirect_uri": "http://127.0.0.1:18800/callback",
          "scope": "openid offline_access mcp.user",
          "token_store_path": "~/.felix/mcp-tokens/user-tools.json"
        }
      }
    },

    // Stdio transport — Felix spawns the child process and inherits PATH.
    {
      "id": "fs-tools",
      "transport": "stdio",
      "enabled": true,
      "stdio": {
        "command": "uvx",
        "args": ["mcp-server-filesystem", "/Users/me/projects"],
        "env": { "DEBUG": "1" }
      }
    }
  ]
}
```

Tools discovered from an MCP server are auto-added to agent allowlists at startup. Servers can also be edited from the Settings UI's MCP tab.

### Full config example

```json5
{
  "providers": {
    "anthropic": { "kind": "anthropic", "api_key": "sk-ant-..." },
    "openai":    { "kind": "openai",    "api_key": "sk-..." },
    "ollama":    { "kind": "openai-compatible", "base_url": "http://localhost:11434/v1" }
  },
  "agents": {
    "list": [
      {
        "id": "default",
        "name": "Felix",
        "model": "anthropic/claude-sonnet-4-5-20250514",
        "reasoning": "high",
        "workspace": "~/.felix/workspace-default",
        "tools": {
          "allow": ["read_file", "write_file", "edit_file", "bash", "web_fetch", "web_search", "browser", "cron", "send_message"]
        }
      }
    ]
  },
  "memory": { "enabled": true },
  "cortex": { "enabled": true },
  "security": {
    "execApprovals": { "level": "full" }
  }
}
```

---

## Tools

Built-in tools agents can use:

| Tool | Description |
|------|-------------|
| `read_file` | Read file contents (text + images for vision-capable models) |
| `write_file` | Create or overwrite files |
| `edit_file` | Make targeted edits to existing files |
| `bash` | Execute shell commands (with `deny`/`allowlist`/`full` execApproval levels) |
| `web_fetch` | Fetch a URL and return its content |
| `web_search` | Search the web (DuckDuckGo by default; pluggable backend) |
| `browser` | Headless Chrome automation (navigate, click, type, screenshot, evaluate JS) |
| `cron` | Dynamically schedule, list, pause, resume, remove, and update recurring tasks |
| `send_message` | Send outbound messages over a configured channel (currently Telegram via Bot API) |
| `todo_write` | Per-workspace persistent todo list for tracking long, multi-stage work |
| `task` | Delegate a subtask to another configured agent |
| `load_skill` | Load a single skill body on demand by name (the Skills Index in the system prompt lists every available skill) |
| `load_memory` | Load a single memory entry body by id (the Memory Index in the system prompt lists candidates) |

Tool access is controlled per-agent via allow/deny policies, configurable from the Settings UI's Agents tab.

**MCP-provided tools** — any [Model Context Protocol](https://modelcontextprotocol.io) server declared in `mcp_servers` exposes its tools through the same `Tool` interface. They appear in agent registries alongside built-ins, can be allow/deny-listed per agent, and are auto-added to agent allowlists at startup.

---

## Data Directory

All state lives in `~/.felix/` (on Windows: `C:\Users\<you>\.felix\`) — no external database required.

```
~/.felix/
  felix.json5             # Configuration file
  sessions/                # Conversation history (JSONL, one file per agent+session)
  memory/entries/          # Memory entries (Markdown)
  skills/                  # User skills (SKILL.md files); bundled starter skills
                           # (ffmpeg, imagemagick, pandoc, pdftotext, cortex) are
                           # seeded here on first run
  workspace-<agentId>/     # Per-agent workspace; can hold IDENTITY.md, FELIX.md,
                           # AGENTS.md, agent-specific skills/
  brain.db                 # Cortex knowledge graph (SQLite)
  cron-jobs.json           # Persisted dynamic cron jobs
  mcp-tokens/              # OAuth refresh tokens per MCP server
  ollama/                  # Bundled Ollama model store
```

Everything is human-readable files you can inspect, edit, and version-control.

---

## WebSocket API

JSON-RPC 2.0 over WebSocket at `ws://127.0.0.1:18789/ws`.

| Method | Description |
|--------|-------------|
| `chat.send` | Send a message to an agent (streams response events) |
| `chat.abort` | Cancel the active response for this connection |
| `chat.compact` | Force-compact the active session immediately |
| `agent.status` | List all configured agents and their state (includes per-agent context window) |
| `session.list` | List sessions for an agent |
| `session.new` | Start a fresh session for an agent |
| `session.switch` | Switch the active session for an agent |
| `session.history` | Load conversation history for an agent |
| `session.clear` | Clear an agent's session history |
| `jobs.list` / `jobs.add` / `jobs.pause` / `jobs.resume` / `jobs.remove` / `jobs.update` | Manage cron jobs |

HTTP endpoints: `GET /health`, `GET /ws`, `GET /metrics` (when enabled), `GET /chat`, `GET /jobs`, `GET /settings` (Agents, Providers, Models, Intelligence, Security, Messaging, MCP, Skills, Memory, Gateway tabs), `GET /logs` (+ SSE), `POST /api/mcp/reauth/{id}`.

---

## Security

Felix is designed to run on your own hardware. The following measures protect your system, credentials, and data.

### Network & Transport

- **Localhost-only by default** — gateway binds to `127.0.0.1:18789`, never exposed to the network unless you change the config.
- **Bearer token auth** — optional token protects all HTTP and WebSocket endpoints; uses constant-time comparison to prevent timing attacks.
- **WebSocket origin checking** — only connections from localhost origins are accepted by default; configurable allowlist for custom origins.
- **ReadHeaderTimeout** — 5-second header timeout defends against slowloris attacks.
- **Security headers** — the web chat page sets `X-Frame-Options: DENY`, `Content-Security-Policy`, and `X-Content-Type-Options: nosniff`.

### Tool Execution

- **Tool policies** — per-agent allow/deny lists control which tools each agent can use.
- **Exec approval policy** — three levels for the bash tool:
  - `deny` — all shell execution blocked.
  - `allowlist` — only commands in the allowlist can run; shell metacharacters (`$(...)`, backticks, process substitution) are blocked.
  - `full` — unrestricted (default).
- **Workspace containment** — file tools (`read_file`, `write_file`, `edit_file`) validate paths against the agent's workspace directory with symlink resolution to prevent path traversal.

### Input Validation

- **SSRF protection** — `web_fetch` and `browser` tools resolve hostnames and block private IP ranges (RFC 1918, loopback, link-local, IPv6 ULA) and cloud metadata endpoints. DNS resolution failures are blocked (fail-closed). Redirect targets are re-validated at each hop.
- **XSS prevention** — the web chat UI escapes HTML before applying markdown formatting, and blocks `javascript:`, `data:`, and `vbscript:` URL schemes in rendered links.
- **WebSocket rate limiting** — per-connection token bucket (30 messages/sec) prevents flooding.
- **WebSocket message size limit** — 1 MiB max prevents memory exhaustion from oversized payloads.

### Credentials & Data

- **No hardcoded secrets** — all API keys and tokens come from config or environment variables.
- **Config file permissions** — the `onboard` command writes config with `0o600` (owner-only). At startup, a warning is logged if the config file is readable by group or others.
- **Session file permissions** — conversation history files use `0o600` (owner-only).
- **DEBUG-level tool logging** — tool inputs and outputs (which may contain sensitive data) are logged at DEBUG, not INFO.
- **API keys via environment** — credentials can be set as `{PROVIDER}_API_KEY` environment variables to keep them out of config files entirely.

---

## Development

```bash
make build                  # Build the CLI binary
make build-app              # Build the macOS menu bar app (Felix.app)
make build-app-windows      # Build the Windows system tray app (felix-app.exe)
make test                   # Run all tests
make test-race              # Run tests with race detector
make lint                   # Run golangci-lint
make fmt                    # Format source files
make tidy                   # Tidy module dependencies
make sign                   # Build, sign, notarize, and staple the macOS PKG installer (output → dist/Felix-<VERSION>-signed.pkg)
make publish-release        # Publish a GitHub release for the latest tag with notes from the previous tag, attaching dist/*.{zip,pkg} matching the tag
make build-release          # Cross-compile binaries without creating a GitHub release
make snapshot               # Cross-platform build via goreleaser
make help                   # Show all targets
```

### Dependencies

| Purpose | Package |
|---------|---------|
| HTTP router | `github.com/go-chi/chi/v5` |
| WebSocket | `github.com/gorilla/websocket` |
| CLI framework | `github.com/spf13/cobra` |
| Anthropic client | `github.com/anthropics/anthropic-sdk-go` |
| OpenAI / OpenAI-compatible / Qwen / local Ollama client | `github.com/sashabaranov/go-openai` |
| Gemini client | `google.golang.org/genai` |
| MCP client SDK | `github.com/modelcontextprotocol/go-sdk` |
| Knowledge graph | `github.com/sausheong/cortex` |
| Vector index | `github.com/philippgille/chromem-go` |
| HTML → Markdown | `github.com/JohannesKaufmann/html-to-markdown/v2` |
| Markdown rendering (CLI) | `github.com/charmbracelet/glamour` |
| File watching | `github.com/fsnotify/fsnotify` |
| Browser automation | `github.com/chromedp/chromedp` |
| OAuth2 (MCP auth) | `golang.org/x/oauth2` |
| System tray | `fyne.io/systray` |
| YAML (skill frontmatter) | `gopkg.in/yaml.v3` |
| Testing | `github.com/stretchr/testify` |
| Logging | `log/slog` (stdlib) |

### Testing

```bash
go test ./...              # Run all tests
go test -cover ./...       # Run tests with per-package coverage
go test -race ./...        # Run tests with race detector
```

---

## Documentation

- [howtouse.md](howtouse.md) — examples, use cases, and example configurations.
