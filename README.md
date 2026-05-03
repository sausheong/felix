![Felix](infographic.png)

# Felix

A self-hosted AI agent gateway written in Go. Single binary, low memory, runs entirely on your own machine.

Felix connects you (via CLI or web chat) to LLMs — Claude, GPT, Gemini, Qwen, Ollama, or any OpenAI-compatible endpoint — and lets agents execute tasks on your hardware using a fixed registry of in-process tools plus any number of remote MCP servers.

## Design philosophy

1. **Self-sufficient.** One binary, one directory of state, no required network dependency. The LLM can be local. The vector index is in-process. The knowledge graph is a SQLite file. There is no Felix cloud, no Felix account, no Felix backend that anyone could turn off.
2. **Robust.** Long-running agents touch files, shell out, talk to flaky APIs, and accumulate state across restarts. Every external call has a timeout. Every queue has a cap. Every per-call resource has a paired cleanup. On-disk state heals itself on the next load.
3. **Usable out of the box by non-technical people.** The default install — no config edits, no API keys, no `vim` — must just work. Advanced configuration can be as complex as it needs to be, but it must not be in the way of the default path.
4. **Secure by default.** An agent that can read files, run shell commands, and make web requests is genuinely powerful — defaults have to protect users who won't read the security docs. Felix binds to localhost only, ships the bash tool in allowlist mode rather than full shell access, blocks web requests to internal IP ranges and cloud metadata endpoints, contains file access to each agent's workspace with symlink resolution, and writes config and session files with owner-only permissions. You can relax any of it deliberately; you don't have to opt out of it.



## Features

**Interfaces**
- Single binary, no runtime dependencies. Download and run.
- macOS / Windows system tray app that runs the gateway in the background and serves a web chat at `http://127.0.0.1:18789/chat`.
- `felix chat` CLI that auto-detects a running gateway and shares its session, memory, and MCP state — start in the browser, continue in the terminal.
- WebSocket JSON-RPC 2.0 control plane for programmatic access.

**Models**
- Claude, GPT, Gemini, Qwen, Ollama, LM Studio, DeepSeek, or any OpenAI-compatible API.
- Bundled Ollama runtime so you can run agents with no API key. Downloads `gemma4` on first startup if no other models are available.
- Per-agent extended reasoning (`off|low|medium|high`) mapped to Claude thinking budgets, OpenAI `reasoning_effort`, Gemini `ThinkingConfig`, and Qwen `enable_thinking`.
- Context-window auto-detection from the model identifier (handles proxy prefixes correctly), with a per-agent `contextWindow` override for unusual fine-tunes.
- Cross-provider tool portability: JSON Schema fields one provider rejects (Gemini drops `anyOf`/`oneOf`/`format`; OpenAI drops `$ref`/`definitions`) are stripped at the provider boundary.

**Memory & knowledge**
- Persistent memory: BM25 lexical search over Markdown files, recalled automatically each turn. Optional vector search via `chromem-go` when an embedding provider is configured.
- Cortex knowledge graph (SQLite) that ingests completed conversations and surfaces relevant facts on subsequent turns.
- Skill system: Markdown files with YAML frontmatter, lazily loaded by the agent on demand from a system-prompt index. Bundled starters (`ffmpeg`, `imagemagick`, `pandoc`, `pdftotext`, `cortex`) seeded on first run; user skills are managed live from the Settings UI.

**Agents & tools**
- Multiple agents per install, each with its own model, workspace, persona, and tool policy.
- Subagents invocable via the `task` tool, so a supervisor can delegate to a specialist with a different model.
- Per-agent allow/deny lists for every built-in and MCP-provided tool.
- Vision: paste image paths in the CLI or drop them in web chat; bytes go straight to the model.
- Cron jobs: recurring prompts on configurable intervals, with pause/resume/remove management.
- MCP client: Streamable-HTTP and stdio transports, OAuth2 (client credentials, authorization code + PKCE) and bearer auth, in-chat re-authentication, per-server circuit breaker.

**Robustness**
- Append-only JSONL session storage with a DAG view; compaction is splice-based and never destructive.
- Smart compaction: token-threshold or message-count triggered, three-stage fallback chain, per-session circuit breaker, runs asynchronously between turns.
- Cache-stability invariant: request prefixes are byte-stable across turns (sorted tool defs, deterministic schema normalization) so Anthropic and OpenAI prompt caches keep hitting.
- Stream-failure resilience: when a streaming response dies mid-flight, the runtime discards the partial output and retries via the provider's non-streaming endpoint, preserving the byte-identical prompt prefix.
- Config hot-reload: edit `felix.json5` while running, changes apply immediately.

**Operations**
- All state in `~/.felix/` as plain files. No external database.
- OpenTelemetry export (opt-in): traces, metrics, and logs to any OTLP/HTTP collector via config or standard `OTEL_*` env vars.
- Localhost-only by default; optional bearer token auth on all HTTP and WebSocket endpoints.


## Install

### macOS — signed `.pkg` (recommended)

Download the latest `Felix-vX.Y.Z-signed.pkg` from the [GitHub Releases](https://github.com/sausheong/felix/releases) page. Signed with Developer ID and notarized by Apple, so Gatekeeper accepts it.

The installer drops `Felix.app` into `/Applications`, bundles the `felix` and `felix-app` binaries plus a copy of `ollama`, seeds the bundled starter skills, and symlinks the CLI at `/usr/local/bin/felix` so `felix chat` / `felix doctor` work in any terminal.

On first launch, Felix.app opens `http://127.0.0.1:18789/settings#models` and starts pulling `gemma4` (~9.6 GB chat model) and `nomic-embed-text` (~270 MB embeddings) in the background. Once the chat model is on disk, click **Chat** in the menu bar to start talking. Zero config, no API keys.

To uninstall: `rm /usr/local/bin/felix && rm -rf /Applications/Felix.app ~/.felix/`.

### Build from source (Linux, Windows, or developers)

```bash
make build               # CLI binary -> ./felix
make build-app           # macOS menu bar app -> Felix.app  (also bundles felix and ollama)
make build-app-windows   # Windows system tray app -> felix-app.exe
```

Then run `./felix onboard` to walk through provider setup. If you skip every cloud provider, the wizard configures the bundled Ollama with `gemma4` so you have a working agent with zero credentials.


## First steps

```bash
./felix onboard       # First-time setup wizard (writes ~/.felix/felix.json5)
./felix chat          # Interactive CLI chat
./felix start         # Run the gateway (web chat at http://127.0.0.1:18789/chat)
open Felix.app        # macOS tray launcher (auto-spawns the gateway)
./felix doctor        # Sanity-checks config, data dirs, API keys, workspaces
```

`./felix chat` automatically detects a running gateway and shares its session state with the web chat. Pass `--no-gateway` to force an isolated in-process runtime; pass `-m provider/model` to override the model for one session (also forces in-process).

Inside `felix chat`, slash commands manage sessions and screenshots:

```
> /sessions             # list sessions for this agent
> /new myproject        # start a new named session
> /switch myproject     # switch to an existing session
> /compact              # manually compact the active session
> /screenshot           # capture a window and analyze it (in-process mode only)
> /quit
```

Image attachments work too — type a file path in the message:

```
> What's in this image? ~/Downloads/photo.png
> Describe '/Users/me/My Photos/vacation.png'
> Tell me about this /Users/me/My\ Photos/diagram.png
```

Supported formats: `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.bmp` (max 10 MB).



## CLI commands

| Command | Description |
|---------|-------------|
| `felix onboard` | Interactive setup wizard |
| `felix start` | Start the gateway server |
| `felix start -c path/to/config.json5` | Custom config |
| `felix chat [agent]` | Interactive CLI chat (auto-detects running gateway) |
| `felix chat --no-gateway` | Force in-process runtime, ignore any running gateway |
| `felix chat -m provider/model` | Override model for this session (forces in-process) |
| `felix clear [agent]` | Clear local CLI session history |
| `felix sessions [agent]` | List sessions for an agent |
| `felix model list \| pull <name> \| rm <name> \| status` | Manage local Ollama models |
| `felix mcp login <id>` | Run interactive OAuth login for an MCP server |
| `felix status` | Query the running gateway for agent status |
| `felix doctor` | Diagnostic checks |
| `felix version` | Print version + commit |



## System tray app

A thin launcher around the gateway, on macOS and Windows. Spawns `felix start` as a separate child process so a tray reap (display sleep, fast user switching, memory pressure) doesn't take your active chat down — only the icon disappears, and relaunching reattaches via `/health`.

Menu: **Chat**, **Jobs**, **Logs**, **Settings**, **Restart**, **Quit**.

The Settings page has tabs for Agents, Providers, Models, Intelligence, Security, Messaging, MCP, Skills, Memory, and Gateway — most things you'd otherwise edit in `felix.json5` are reachable here.

**Web chat** at `/chat`: agent + session selectors, streaming responses, light/dark toggle, inline tool-call display with collapsible output, inline "Re-authenticate" button when an MCP token expires, live trace panel.

**Environment variables.** macOS `.app` bundles don't inherit shell environment variables; Felix.app loads `~/.zshrc` / `~/.bashrc` at startup, so `export ANTHROPIC_API_KEY=...` works. On Windows, set via System Settings or PowerShell `[System.Environment]::SetEnvironmentVariable("ANTHROPIC_API_KEY","sk-ant-...","User")`. Either way, you can put keys directly in `felix.json5` instead.



## Configuration

`~/.felix/felix.json5` (Windows: `C:\Users\<you>\.felix\felix.json5`). JSON5 means comments and trailing commas are allowed. Hot-reloaded — edits apply immediately, no restart needed.

### Minimal config

```json5
{
  "providers": {
    "anthropic": { "kind": "anthropic", "api_key": "sk-ant-..." }
  },
  "agents": {
    "list": [
      { "id": "default", "name": "Felix", "model": "anthropic/claude-sonnet-4-5" }
    ]
  }
}
```

### API keys via environment

Environment variables take precedence over config-file values. The convention is `{PROVIDER}_API_KEY` (or `{PROVIDER}_AUTH_TOKEN`) and `{PROVIDER}_BASE_URL`, where `{PROVIDER}` is the uppercased provider name from your config:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
export OPENAI_API_KEY="sk-proj-..."
export GEMINI_API_KEY="AIza..."
export DEEPSEEK_API_KEY="sk-..."
```



## LLM providers

Felix supports multiple providers simultaneously. Each is defined in the `providers` block with a unique name and a `kind`:

| Kind | Description | Use for |
|------|-------------|---------|
| `anthropic` | Anthropic's native API | Claude models |
| `openai` | OpenAI's native API | GPT models |
| `gemini` | Google's native Gemini SDK | Gemini models |
| `qwen` | Alibaba Cloud DashScope | Qwen models |
| `openai-compatible` | Anything implementing `/v1/chat/completions` | Ollama, LM Studio, DeepSeek, LiteLLM, vLLM |
| `local` | Bundled Ollama supervised by Felix | Fully offline / no API key |

### Per-provider setup

```json5
// Anthropic — get a key at https://console.anthropic.com
"anthropic": { "kind": "anthropic", "api_key": "sk-ant-..." }
// Models: claude-sonnet-4-5, claude-opus-4, claude-haiku-3-5

// OpenAI — get a key at https://platform.openai.com/api-keys
"openai": { "kind": "openai", "api_key": "sk-proj-..." }
// Models: gpt-5, gpt-5-mini, o3-mini

// Google Gemini — get a key at https://aistudio.google.com/apikey
"gemini": { "kind": "gemini", "api_key": "AIza..." }
// Models: gemini-2.5-flash, gemini-2.5-pro, gemini-2.0-flash

// Qwen (Alibaba)
"qwen": { "kind": "qwen", "api_key": "sk-..." }
// Models: qwen-plus, qwen-max, qwen-turbo

// External Ollama (running outside Felix)
"ollama": { "kind": "openai-compatible", "base_url": "http://localhost:11434/v1" }
// Models: any tag pulled into Ollama (llama3.2, qwen2.5, mistral, llava, ...)

// LM Studio (default port 1234)
"lmstudio": { "kind": "openai-compatible", "base_url": "http://localhost:1234/v1" }

// DeepSeek — get a key at https://platform.deepseek.com
"deepseek": {
  "kind": "openai-compatible",
  "api_key": "sk-...",
  "base_url": "https://api.deepseek.com/v1"
}
// Models: deepseek-chat, deepseek-coder, deepseek-reasoner

// Bundled Ollama (wired up automatically by `felix onboard`)
"local": { "kind": "local", "base_url": "http://127.0.0.1:18790/v1" }
```

### Model references

Agents reference models as `provider/model-name` where the provider name matches a key in the `providers` block:

```
anthropic/claude-sonnet-4-5    → uses the "anthropic" provider
openai/gpt-5                   → uses the "openai" provider
ollama/llama3.2                → uses the "ollama" provider
local/gemma4                   → uses the bundled local Ollama
deepseek/deepseek-chat         → uses the "deepseek" provider
```

You can override the model for one CLI session: `felix chat -m openai/gpt-5`.

### Reasoning levels

Per-agent `reasoning: off|low|medium|high` (default `off`). Maps to Anthropic thinking budgets, OpenAI `reasoning_effort`, Gemini `ThinkingConfig`, and Qwen `enable_thinking`. Models that don't support extended reasoning log `reasoning ignored` and proceed normally. Editable in the Settings UI's Agents tab.



## Multiple agents

You can run multiple agents per install, each with its own model, workspace, system prompt, and tool policy:

```json5
{
  "agents": {
    "list": [
      {
        "id": "coder",
        "name": "Coder",
        "model": "anthropic/claude-sonnet-4-5",
        "workspace": "~/code/myproject",
        "tools": { "allow": ["read_file", "write_file", "edit_file", "bash"] }
      },
      {
        "id": "researcher",
        "name": "Researcher",
        "model": "openai/gpt-5",
        "workspace": "~/.felix/workspace-researcher",
        "tools": { "allow": ["read_file", "web_fetch", "web_search"] }
      },
      {
        "id": "local",
        "name": "Local Assistant",
        "model": "local/gemma4",
        "workspace": "~/.felix/workspace-local",
        "tools": { "allow": ["read_file"] }
      }
    ]
  }
}
```

Chat with a specific agent: `felix chat coder` (or pick from the dropdown in the web chat header).

### Agent identity

Each agent's system prompt is resolved in this priority order:

1. `system_prompt` field in the agent's config (if non-empty)
2. `IDENTITY.md` in the agent's workspace directory
3. Built-in default — a generic helpful-assistant prompt, dynamically tailored to whichever tools the agent actually has (an agent without `web_search` won't claim it can search the web)

Inline:

```json5
{
  "id": "coder",
  "system_prompt": "You are a senior Go developer. Idiomatic stdlib-first code. Always write tests."
}
```

Or in `~/.felix/workspace-coder/IDENTITY.md` for prompts long enough to be uncomfortable in JSON.

Each agent automatically knows its own name and ID, what other agents exist (so it can suggest delegation), and which tools it has. No need to repeat any of that in the prompt.

### Subagents and the `task` tool

A **subagent** is an agent another agent can delegate work to via the built-in `task` tool. The supervisor's LLM sees `task` like any other tool, picks a subagent by ID, and gets back the subagent's final text. The subagent runs with its own model, workspace, and tool policy.

Two flags wire it up:

- `subagent: true` on the *target* agent — the opt-in. Without this, the agent is invisible to `task`.
- `task` in the *supervisor's* `tools.allow` — without this, the supervisor's LLM never sees the tool.

```json5
{
  "agents": {
    "list": [
      // Supervisor — the agent you chat with
      {
        "id": "lead",
        "name": "Tech Lead",
        "model": "anthropic/claude-sonnet-4-5",
        "tools": { "allow": ["read_file", "bash", "task", "todo_write"] }
      },

      // Subagent: cheap web research
      {
        "id": "researcher",
        "model": "local/gemma4",
        "subagent": true,
        "description": "Searches the web and summarises sources. Returns a short bulleted brief with citations.",
        "tools": { "allow": ["web_search", "web_fetch", "read_file"] }
      },

      // Subagent: read-only code review on a more careful model
      {
        "id": "reviewer",
        "model": "anthropic/claude-opus-4",
        "workspace": "~/code/myproject",   // share the supervisor's workspace
        "subagent": true,
        "description": "Reviews code changes for correctness, security, and clarity.",
        "inheritContext": true,            // sees the supervisor's conversation
        "tools": { "allow": ["read_file", "bash"] }
      }
    ]
  }
}
```

**`inheritContext: true`** loads the supervisor's conversation into the subagent's first turn. Useful for "review what I just did" patterns; expensive in tokens. Default is `false` (cold start with just the prompt).

**Common gotchas:**
- `task` not in supervisor's `tools.allow` → supervisor never sees the tool.
- `subagent: true` missing on the target → `task` returns `agent X is not registered as a subagent`.
- Subagents can't themselves delegate (no `task` tool registered for them); a depth cap of 3 is enforced as defense-in-depth.
- Multiple parallel `task` calls aren't supported — they run sequentially.



## Tools

Built-in tools the agent can use:

| Tool | Description |
|------|-------------|
| `read_file` | Read file contents (text + images for vision-capable models) |
| `write_file` | Create or overwrite files |
| `edit_file` | Targeted edits to existing files |
| `bash` | Execute shell commands (with `deny` / `allowlist` / `full` exec-approval levels) |
| `web_fetch` | Fetch a URL and return its content |
| `web_search` | Web search (DuckDuckGo by default; pluggable backend) |
| `browser` | Headless Chrome (navigate, click, type, screenshot, evaluate JS) |
| `cron` | Dynamically schedule, list, pause, resume, remove, update recurring tasks |
| `send_message` | Send outbound messages (currently Telegram via Bot API) |
| `todo_write` | Per-workspace persistent todo list for long, multi-stage work |
| `task` | Delegate a subtask to another configured agent |
| `load_skill` | Load a single skill body on demand by name |
| `load_memory` | Load a single memory entry body by id |

Tool access is per-agent allow/deny, configurable from the Settings UI's Agents tab. **MCP-provided tools** are wrapped to the same `Tool` interface and gated by the same allow/deny mechanism — the LLM can't tell the difference.

### Tool policies

```json5
// Read-only agent (safe for untrusted use)
"tools": { "allow": ["read_file", "web_fetch", "web_search"] }

// Everything except shell
"tools": { "allow": ["*"], "deny": ["bash"] }

// Full default access
"tools": {
  "allow": ["read_file", "write_file", "edit_file", "bash",
            "web_fetch", "web_search", "browser"]
}
```

### Bash exec policy

For additional safety, `bash` has its own command-level gate independent of the tool allowlist:

```json5
{
  "security": {
    "execApprovals": {
      "level": "allowlist",
      "allowlist": ["ls", "cat", "find", "grep", "head", "tail", "wc", "pwd", "date"]
    }
  }
}
```

Levels: `full` (default — anything goes), `allowlist` (only listed commands; shell metacharacters like `$(...)` and backticks are blocked), `deny` (no execution at all).

### Browser tool

Headless Chrome automation; requires Chrome or Chromium installed on the host. Each invocation creates a fresh context, so cookies don't persist across calls — chain actions in one conversation turn for multi-step workflows.

| Action | Description |
|--------|-------------|
| `navigate` | Navigate to a URL |
| `click` | Click an element by CSS selector |
| `type` | Type text into an input by CSS selector |
| `get_text` | Extract text from an element or full page |
| `screenshot` | Take a screenshot |
| `evaluate` | Run arbitrary JavaScript and return the result |

All actions except `navigate` accept an optional `url` parameter — provide it and the browser navigates first, all in one tool call.

### Send message (Telegram outbound)

Felix can push messages to a Telegram chat via the Bot API. **Outbound only** — there is no inbound Telegram channel.

```json5
{
  "telegram": {
    "enabled": true,
    "bot_token": "123456:ABC-DEF...",         // from @BotFather
    "default_chat_id": "123456789"            // optional fallback
  },
  "agents": {
    "list": [
      { "id": "default", "tools": { "allow": ["send_message"] } }
    ]
  }
}
```

Useful for cron-driven alerts ("ping me when the build fails"). To find your chat ID, message your bot once and read the `chat_id` from the gateway log, or use any "userinfobot".

### Dynamic cron

The `cron` tool lets the agent create, list, pause, resume, remove, and update recurring scheduled tasks at runtime — without editing the config:

```
You: Check disk usage every hour and warn me if it's above 80%.
Agent: [cron: action="add", name="disk-check", schedule="1h",
        prompt="Check disk usage with 'df -h'. Alert me if any partition >80%."]
       Done.

You: Pause the disk check job.
Agent: [cron: action="pause", name="disk-check"]
```

Static cron jobs go in `agents[].cron[]` in the config and persist across restarts. Dynamic jobs added via the tool also persist (`~/.felix/cron-jobs.json`). Both use the same scheduler. Schedule values are Go duration strings (`30m`, `1h`, `24h`).

### MCP servers

Felix can connect to external [Model Context Protocol](https://modelcontextprotocol.io) servers and expose their tools alongside built-ins. Two transports: **HTTP** (Streamable HTTP, with OAuth2 / bearer / no auth) and **stdio** (Felix spawns the child process).

```json5
{
  "mcp_servers": [
    // OAuth2 client-credentials (machine-to-machine)
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

    // OAuth2 authorization code + PKCE (user login)
    // Initial login: `felix mcp login user-tools`
    // Refresh: automatic, persisted to token_store_path
    // Expiry mid-chat: inline "Re-authenticate" button in the web UI
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

    // Stdio: Felix spawns the child process, inherits PATH
    {
      "id": "fs-tools",
      "transport": "stdio",
      "enabled": true,
      "stdio": {
        "command": "uvx",
        "args": ["mcp-server-filesystem", "/Users/me/projects"]
      }
    }
  ]
}
```

Tools discovered from an MCP server are auto-added to agent allowlists at startup. Servers can also be edited from the Settings UI's MCP tab. A per-server circuit breaker stops calling a stuck upstream after 3 consecutive auth failures so the agent can't fall into a token-burning self-heal loop.



## Skills

Skills are Markdown files with YAML frontmatter that get injected into the agent's context when relevant. They teach domain-specific knowledge without modifying code.

```markdown
---
name: git-workflow
description: Guidelines for using git in this project
tags: [git, version-control, commit]
---

## Git Workflow

- Always create feature branches from `main`
- Use conventional commit messages: `feat:`, `fix:`, `docs:`, `refactor:`
- Run tests before committing
- Squash merge into main
```

**Where they live:**
- `~/.felix/skills/` — shared across all agents
- `<agent-workspace>/skills/` — agent-specific

**How they're matched.** Felix injects only the *index* (name + description + tags) into every turn's system prompt. The model decides when it needs a skill body and calls the `load_skill` tool to fetch it on demand. This keeps the cached prompt prefix small and stable, which keeps prompt caches hitting.

**Manage from the UI.** Open `/settings` → Skills. Upload a `.md` file (256 KB max) and it's available on the next chat turn — no restart. View raw markdown, delete, or check for parse warnings inline. The same operations are exposed via REST at `GET/POST/DELETE /settings/api/skills*`.

The default install seeds `cortex`, `ffmpeg`, `imagemagick`, `pandoc`, and `pdftotext` so the agent arrives knowing how to reason about common command-line tools.



## Memory

```json5
{ "memory": { "enabled": true } }
```

Memory entries are Markdown files in `~/.felix/memory/entries/`. The agent can create, update, and delete entries during conversations, and BM25 search surfaces relevant ones each turn. The model sees only the index in the system prompt; bodies are loaded via the `load_memory` tool on demand (same lazy-hydration pattern as skills).

You can manually drop entries in too:

```bash
cat > ~/.felix/memory/entries/project-conventions.md << 'EOF'
# Project Conventions
- Go 1.22+ with generics where appropriate
- chi router for HTTP handlers
- testify/assert for tests
- Errors wrapped with fmt.Errorf("context: %w", err)
EOF
```

If you also configure an `embeddingProvider` and `embeddingModel` under `memory`, vector search via `chromem-go` runs alongside BM25.



## Local LLM (bundled Ollama)

Felix ships with a bundled Ollama binary so you can run agents offline with no API key. On first run it pulls `gemma4` (chat) and `nomic-embed-text` (memory embeddings) in the background.

```bash
felix model pull qwen2.5:7b   # add another model
felix model list              # see what's installed
felix model status            # check the supervisor
felix model rm gemma4         # free disk space
```

The bundled Ollama runs as a child of Felix on a free port in `127.0.0.1:18790–18799` and shuts down when Felix exits. It does not interfere with any system Ollama you may have on `:11434`.



## WebSocket API

JSON-RPC 2.0 over WebSocket at `ws://127.0.0.1:18789/ws`.

| Method | Description |
|--------|-------------|
| `chat.send` | Send a message to an agent (streams response events) |
| `chat.abort` | Cancel the active response for this connection |
| `chat.compact` | Force-compact the active session immediately |
| `agent.status` | List all configured agents and their state |
| `session.list / new / switch / history / clear` | Session management |
| `jobs.list / add / pause / resume / remove / update` | Cron job management |

HTTP endpoints: `GET /health`, `/ws`, `/metrics` (when enabled), `/chat`, `/jobs`, `/settings`, `/logs` (+ SSE), `POST /api/mcp/reauth/{id}`.

```javascript
const ws = new WebSocket("ws://127.0.0.1:18789/ws");

ws.onopen = () => {
  ws.send(JSON.stringify({
    jsonrpc: "2.0",
    method: "chat.send",
    params: { agentId: "default", text: "What files are in the current directory?" },
    id: 1
  }));
};

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  // msg.result.type is one of:
  //   "text_delta"      — streaming text chunk
  //   "tool_call_start" — agent is calling a tool
  //   "tool_result"     — tool execution result
  //   "compaction.start" / "compaction.done" / "compaction.skipped"
  //   "done"            — response complete
  //   "error" / "aborted"
};
```

If `gateway.auth.token` is set in `felix.json5`, include `Authorization: Bearer <token>` on the WebSocket upgrade.



## Observability

**Logs.** `/logs` shows the live tail of the gateway's structured logs (slog) with an SSE stream at `/logs/stream`. Tool inputs and outputs log at DEBUG, not INFO, to keep sensitive data out of casual viewing.

**Metrics.** `/metrics` exposes Prometheus-style metrics when enabled in config.

**OpenTelemetry export.** Opt-in OTLP/HTTP exporter for traces, metrics, and logs to any compatible collector (Tempo, Jaeger, Loki, Grafana Cloud, Honeycomb, your own collector). Each chat turn becomes one `agent.run` span with phase events for `cortex.recall`, `context.assemble`, `llm.request_sent`, `llm.first_token`, `llm.stream_end`, `tool.exec`, `agent.done`. Exporter init is non-fatal — Felix serves chat normally even if the collector is unreachable.

```json5
{
  "otel": {
    "enabled": true,
    "endpoint": "http://collector.example.com:4318/",
    "serviceName": "felix",
    "sampleRatio": 1.0,
    "signals": { "traces": true, "metrics": true, "logs": true }
  }
}
```

Or via standard env vars (which implicitly enable OTel when `OTEL_EXPORTER_OTLP_ENDPOINT` is set):

```bash
OTEL_EXPORTER_OTLP_ENDPOINT="http://collector.example.com:4318/" \
OTEL_SERVICE_NAME="felix-prod" \
./felix start
```

OTel changes require a **restart** (the SDK doesn't support swapping providers in flight).



## Security

Felix is designed to run on your own hardware. The defaults protect you from the common ways an agent with broad capabilities can go wrong; you relax them deliberately, not opt out of them.

**Network & transport.** Localhost-only by default (`127.0.0.1:18789`). Optional bearer-token auth on all HTTP and WebSocket endpoints with constant-time comparison. WebSocket origin checking restricted to localhost by default. 5 s `ReadHeaderTimeout` against slowloris. Web chat sets `X-Frame-Options: DENY`, `Content-Security-Policy`, `X-Content-Type-Options: nosniff`.

**Tool execution.** Per-agent allow/deny lists for every tool. Bash exec policy — `deny` (no shell), `allowlist` (only listed commands; shell metacharacters blocked), or `full` (default). File tools validate paths against the agent's workspace with symlink resolution to prevent traversal.

**Input validation.** `web_fetch` and `browser` resolve hostnames and block private IP ranges (RFC 1918, loopback, link-local, IPv6 ULA) and cloud metadata endpoints, fail-closed on DNS errors, re-validate redirects. The web chat escapes HTML before applying markdown and blocks `javascript:` / `data:` / `vbscript:` URLs. WebSocket per-connection rate limit (30 msg/sec) and 1 MiB message cap.

**Credentials & data.** No hardcoded secrets. `onboard` writes config with `0o600`; warning logged at startup if it's group/world-readable. Session files are `0o600`. Tool inputs/outputs (which may contain sensitive data) log at DEBUG, not INFO.

**Optional bearer auth.**

```json5
{ "gateway": { "auth": { "token": "my-secret-token" } } }
```

WebSocket clients then need `Authorization: Bearer my-secret-token` on the upgrade.



## Example configurations

### Personal assistant (Claude + Telegram alerts)

```json5
{
  "providers": {
    "anthropic": { "kind": "anthropic", "api_key": "sk-ant-..." }
  },
  "agents": {
    "list": [
      {
        "id": "assistant",
        "name": "Personal Assistant",
        "model": "anthropic/claude-sonnet-4-5",
        "workspace": "~/.felix/workspace-assistant",
        "tools": {
          "allow": ["read_file", "write_file", "edit_file", "bash",
                    "web_fetch", "web_search", "send_message", "cron"]
        }
      }
    ]
  },
  "telegram": { "enabled": true, "bot_token": "...", "default_chat_id": "..." },
  "memory":   { "enabled": true },
  "cortex":   { "enabled": true }
}
```

### Multi-agent dev team (supervisor + delegated workers)

```json5
{
  "providers": {
    "anthropic": { "kind": "anthropic", "api_key": "sk-ant-..." },
    "openai":    { "kind": "openai",    "api_key": "sk-..." },
    "local":     { "kind": "local" }
  },
  "agents": {
    "list": [
      {
        "id": "lead",
        "name": "Tech Lead",
        "model": "anthropic/claude-sonnet-4-5",
        "tools": { "allow": ["read_file", "bash", "task", "todo_write"] }
      },
      {
        "id": "coder",
        "model": "anthropic/claude-sonnet-4-5",
        "subagent": true,
        "description": "Writes and edits code. Use for any 'implement X' or 'refactor Y' task.",
        "tools": { "allow": ["read_file", "write_file", "edit_file", "bash"] }
      },
      {
        "id": "reviewer",
        "model": "openai/gpt-5",
        "subagent": true,
        "inheritContext": true,
        "description": "Reviews code changes for correctness and style. Read-only.",
        "tools": { "allow": ["read_file"] }
      },
      {
        "id": "quick",
        "model": "local/gemma4",
        "subagent": true,
        "description": "Fast local lookups: man pages, syntax checks, single-file reads.",
        "tools": { "allow": ["read_file", "web_search"] }
      }
    ]
  }
}
```

### Locked-down read-only (safe for shared use)

```json5
{
  "providers": {
    "anthropic": { "kind": "anthropic", "api_key": "sk-ant-..." }
  },
  "agents": {
    "list": [
      { "id": "safe", "model": "anthropic/claude-sonnet-4-5",
        "tools": { "allow": ["read_file"] } }
    ]
  },
  "security": { "execApprovals": { "level": "deny" } },
  "gateway":  { "auth": { "token": "my-secret-token" } }
}
```

### Fully offline (bundled Ollama, no API keys)

```json5
{
  "providers": {
    "local": { "kind": "local", "base_url": "http://127.0.0.1:18790/v1" }
  },
  "agents": {
    "list": [
      { "id": "default", "model": "local/gemma4",
        "tools": { "allow": ["read_file", "write_file", "bash"] } }
    ]
  },
  "memory": { "enabled": true }
}
```



## Architecture

Single-process, hub-and-spoke. All components run in one binary.

- **Gateway** — `chi` HTTP router + `gorilla/websocket` on `:18789`.
- **Agent runtime** — assemble static + dynamic system prompt, stream LLM response, partition tool calls into parallel batches, dispatch and re-loop.
- **LLM provider abstraction** — one `LLMProvider` interface, six implementations (`anthropic`, `openai`, `gemini`, `qwen`, `local`, `openai-compatible`).
- **Session manager** — append-only JSONL with a DAG view; splice-based compaction.
- **Memory manager** — BM25 always present, vector search optional via `chromem-go`.
- **Cortex** — optional SQLite knowledge graph for cross-conversation recall.
- **Skill loader** — embedded starters + user skills, hot-reloaded.
- **Compaction manager** — three-stage fallback, per-session circuit breaker, async between-turns.
- **MCP manager** — Streamable-HTTP and stdio clients with OAuth2 / bearer auth and in-process re-authentication.
- **Cron** — recurring prompts on schedules, with pause/resume/remove management.
- **Bundled Ollama supervisor** — keeps a local LLM available without external setup.



## Data directory

All state lives in `~/.felix/` (Windows: `C:\Users\<you>\.felix\`):

```
felix.json5             # Configuration
sessions/               # Conversation history (JSONL, one file per agent+session)
memory/entries/         # Memory entries (Markdown)
skills/                 # User skills (SKILL.md); bundled starters seeded on first run
workspace-<agentId>/    # Per-agent workspace (IDENTITY.md, FELIX.md, AGENTS.md, skills/)
brain.db                # Cortex knowledge graph (SQLite)
cron-jobs.json          # Persisted dynamic cron jobs
mcp-tokens/             # OAuth refresh tokens per MCP server
ollama/                 # Bundled Ollama model store
```

Plain files. Inspect with a text editor; back up with `rsync`; copy to another machine and pick up exactly where you left off.



## Development

```bash
make build               # CLI binary
make build-app           # macOS menu bar app (Felix.app)
make build-app-windows   # Windows system tray app (felix-app.exe)
make test                # Run all tests
make test-race           # With race detector
make lint                # golangci-lint
make sign                # Sign + notarize + staple the macOS PKG -> dist/Felix-<VERSION>-signed.pkg
make publish-release     # Publish a GitHub release for the latest tag
make build-release       # Cross-compile binaries for all platforms
make help                # All targets
```

### Key dependencies

| Purpose | Package |
|---------|---------|
| HTTP router | `github.com/go-chi/chi/v5` |
| WebSocket | `github.com/gorilla/websocket` |
| CLI framework | `github.com/spf13/cobra` |
| Anthropic | `github.com/anthropics/anthropic-sdk-go` |
| OpenAI / Qwen / local Ollama | `github.com/sashabaranov/go-openai` |
| Gemini | `google.golang.org/genai` |
| MCP client | `github.com/modelcontextprotocol/go-sdk` |
| Knowledge graph | `github.com/sausheong/cortex` |
| Vector index | `github.com/philippgille/chromem-go` |
| HTML → Markdown | `github.com/JohannesKaufmann/html-to-markdown/v2` |
| Markdown rendering (CLI) | `github.com/charmbracelet/glamour` |
| Browser automation | `github.com/chromedp/chromedp` |
| OAuth2 (MCP auth) | `golang.org/x/oauth2` |
| System tray | `fyne.io/systray` |

