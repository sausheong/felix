# How to Use Felix

Felix is a self-hosted AI agent gateway. It runs as a single binary on your machine and connects you (via CLI or web chat) to LLM providers (Claude, GPT, Gemini, Qwen, Ollama, and any OpenAI-compatible API). You talk to it, and it talks back — with the ability to read files, write code, run commands, browse the web, and delegate subtasks to other agents on your behalf.

## Table of Contents

- [Install (macOS .pkg)](#install-macos-pkg)
- [Quick Start (build from source)](#quick-start-build-from-source)
- [CLI Commands](#cli-commands)
- [System Tray App (macOS & Windows)](#system-tray-app-macos--windows)
- [Configuration](#configuration)
- [LLM Providers](#llm-providers)
- [Messaging Channels](#messaging-channels)
- [Image / Vision Support](#image--vision-support)
- [Multiple Agents](#multiple-agents)
- [Message Routing](#message-routing)
- [Skills](#skills)
- [Memory](#memory)
- [Cron Jobs](#cron-jobs)
- [Browser Tool](#browser-tool)
- [Send Message Tool](#send-message-tool)
- [Dynamic Cron Tool](#dynamic-cron-tool)
- [Subagents and the `task` tool](#subagents-and-the-task-tool)
- [Tool Policies](#tool-policies)
- [WebSocket API](#websocket-api)
- [OpenTelemetry Export](#opentelemetry-export)
- [Security](#security)
- [Example Configurations](#example-configurations)

---

## Install (macOS .pkg)

If you just want to **use** Felix on a Mac, the signed and notarized
installer is the path. You don't need Go, you don't need to clone the
repo, and you don't need to run `felix onboard` — the installer ships
Felix with sensible defaults already in place.

### 1. Download the installer

Grab the latest `Felix-<version>-signed.pkg` from the
[GitHub Releases page](https://github.com/sausheong/felix/releases).
The installer is signed with a Developer ID Application certificate and
notarized by Apple, so Gatekeeper won't bounce it.

### 2. Double-click to install

The installer drops `Felix.app` into `/Applications` and bundles
everything Felix needs to run:

- The `felix` gateway binary
- The `felix-app` menubar launcher
- A bundled `ollama` binary (so you can run agents with no API key)
- The full embedded skill set (cortex, ffmpeg, imagemagick, pandoc, pdftotext)

### 3. Launch Felix.app

Open `Felix.app` from `/Applications` (or Spotlight). On first launch:

1. The menubar tray icon appears.
2. The bundled Ollama supervisor starts on a free port in `:18790–:18799`.
3. Felix opens your browser to `http://localhost:18789/settings#models`
   showing a progress bar for the first-run model pulls
   (`gemma4:latest` for chat ~9.6 GB, `nomic-embed-text` for memory
   ~270 MB). The download runs in the background.
4. Once the chat model finishes downloading, you can click **Chat**
   in the menubar (or open `http://localhost:18789/chat`) and start
   talking to Felix immediately. Zero configuration, no API keys.

### 4. (Optional) Add a cloud provider

If you want to use Claude / GPT / Gemini / Qwen instead of (or alongside)
the local model, open **Settings → Providers** in the web UI, add your
API key, and create or edit an agent in **Settings → Agents** to use it.
No restart required — the config hot-reloads on save.

### Where Felix puts things

The installer drops `Felix.app` in `/Applications` and creates `~/.felix/`
on first launch. All state — sessions, memory, skills, downloaded
Ollama models, OAuth tokens — lives under `~/.felix/`. Uninstalling is
`rm -rf /Applications/Felix.app ~/.felix/`.

### Updates

Future versions: download the new `.pkg` and run it. Your `~/.felix/`
data directory is preserved across upgrades — only the application
bundle is replaced.

---

## Quick Start (build from source)

This path is for developers who want to compile Felix from the repo —
typically because they're contributing patches, testing on Linux /
Windows, or running the Windows menubar app (which is not yet shipped
as an installer). End-users on macOS should use the installer above.

### 1. Build

```bash
make build              # CLI binary
make build-app          # macOS menu bar app (Felix.app)
make build-app-windows  # Windows system tray app (felix-app.exe)
```

### 2. Run the setup wizard

```bash
./felix onboard
```

The wizard walks you through choosing an LLM provider and entering your
API key (or picking a bundled local model if you want to run fully
offline). This is the from-source equivalent of the installer's
zero-config first launch — it writes a starter `~/.felix/felix.json5`
with sensible defaults.

If you skip every cloud-provider question, the wizard configures the
bundled Ollama with `local/gemma4:latest` so you have a working agent
with zero credentials, just like the installer.

### 3. Start chatting

**Interactive CLI session (no gateway needed):**
```bash
./felix chat
```

**Or start the full gateway (web chat, Settings UI, WebSocket API, cron):**
```bash
./felix start
```

**Or launch the system tray app:**
```bash
open Felix.app    # macOS
felix-app.exe     # Windows
```

### 4. Verify your setup

```bash
./felix doctor
```

This checks your config file, data directories, API keys, agent workspaces, and channel configurations.

---

## CLI Commands

| Command | Description |
|---------|-------------|
| `felix onboard` | Interactive setup wizard |
| `felix start` | Start the gateway server (HTTP + WebSocket + web chat + cron) |
| `felix start -c path/to/config.json5` | Start with a custom config path |
| `felix chat` | Start an interactive CLI chat session with the default agent |
| `felix chat myagent` | Chat with a specific agent |
| `felix chat -m openai/gpt-4o` | Chat with a model override for this session only |
| `felix clear [agent]` | Clear local CLI session history for an agent |
| `felix sessions [agent]` | List all sessions for an agent |
| `felix model list \| pull <name> \| rm <name> \| status` | Manage local Ollama models pulled by Felix into the bundled runtime |
| `felix status` | Query the running gateway for agent status |
| `felix doctor` | Run diagnostic checks (config, data dirs, API keys, agent workspaces) |
| `felix doctor -c /path/to/config.json5` | Doctor with a custom config path |
| `felix version` | Print version and commit info |

### Chat session commands

Inside a `felix chat` session:

```
> Hello, what files are in this directory?
> Describe this image ~/Downloads/photo.png
> /screenshot What's in this window?
> /quit
> /exit
```

The agent can read files, write files, edit files, run shell commands, fetch web pages, and search the web — all on your local machine. You can also send images for vision analysis (see [Image / Vision Support](#image--vision-support)).

---

## System Tray App (macOS & Windows)

Felix includes a system tray app that runs the gateway in the background. No terminal window needed — just double-click to launch. Supported on macOS and Windows.

### Build

```bash
make build-app          # macOS — produces Felix.app
make build-app-windows  # Windows — produces felix-app.exe
```

### Usage

**macOS:** Double-click `Felix.app`, drag it to `/Applications`, or launch from the terminal:

```bash
open Felix.app
```

**Windows:** Double-click `felix-app.exe`. The app runs as a system tray icon in the taskbar notification area.

> **Note:** On Windows 10/11, new tray icons may be hidden in the overflow area. Click the `^` arrow in the bottom-right of the taskbar to find it. To keep it always visible, go to **Settings → Personalization → Taskbar → Other system tray icons** and enable Felix.

A claw machine icon appears in the system tray / menu bar. The gateway starts automatically in the background.

### Menu items

| Item | Action |
|------|--------|
| **Chat** | Opens `http://localhost:18789/chat` in your default browser |
| **Jobs** | Opens the cron jobs dashboard (`http://localhost:18789/jobs`) showing active scheduled tasks |
| **Logs** | Opens the logs viewer (`http://localhost:18789/logs`) |
| **Settings** | Opens `~/.felix/felix.json5` in your default text editor |
| **Restart** | Restarts the gateway without quitting the app |
| **Quit** | Gracefully shuts down the gateway and exits the app |

### Web chat interface

Clicking **Chat** (or visiting `http://localhost:18789/chat` directly) opens a web-based chat interface with:

- **Agent selector** — a dropdown in the header lists all configured agents; switch between them without leaving the page. Each agent maintains its own session history, which is loaded when you select it.
- **Streaming responses** — text appears as the LLM generates it
- **Abort** — click the Stop button to cancel a response in progress
- **Light/dark mode** — toggle via the moon/sun button in the header; preference is saved across sessions
- **Session management** — Clear button wipes the current agent's session; switching agents loads that agent's history
- **Tool call display** — tool invocations appear inline with collapsible output
- **Markdown rendering** — headings (h1–h6), code blocks, ordered and unordered lists, horizontal rules, bold, italic, and links are rendered
- **Auto-reconnect** — if the WebSocket connection drops, it reconnects automatically

The root URL `http://localhost:18789` redirects to `/chat` for convenience.

### Environment variables and API keys

**macOS:** `.app` bundles do not inherit environment variables from your shell profile (`.zshrc`, `.bashrc`, etc.). Felix.app handles this by automatically sourcing your shell environment at startup, so API keys like `ANTHROPIC_API_KEY` work as expected.

**Windows:** Set environment variables via System Settings, or permanently via PowerShell:

```powershell
# Set API key permanently for your user account
[System.Environment]::SetEnvironmentVariable("ANTHROPIC_API_KEY", "sk-ant-...", "User")

# Or set temporarily for the current session
$env:ANTHROPIC_API_KEY = "sk-ant-..."
```

**Both platforms:** You can set API keys directly in the config file instead of using environment variables:

```json5
{
  "providers": {
    "anthropic": {
      "kind": "anthropic",
      "api_key": "sk-ant-..."
    }
  }
}
```

### How it differs from `felix start`

Both `felix start` and the tray app run the same gateway with the same config file. The differences:

| | `felix start` | Tray App |
|---|---|---|
| Runs in | Terminal (foreground) | System tray (background) |
| Chat interface | WebSocket API / web chat | Web chat in browser |
| Quit | Ctrl+C | Tray menu > Quit |
| Environment vars | Inherited from shell | macOS: loaded from shell profile; Windows: system env vars |
| Logs | Printed to terminal | Written to `~/.felix/felix-app.log` |
| Error display | Terminal output | macOS: log file; Windows: message box dialog |

---

## Configuration

Felix uses a JSON5 config file at `~/.felix/felix.json5` (on Windows: `C:\Users\<you>\.felix\felix.json5`). JSON5 supports comments and trailing commas, making it easier to maintain.

### Minimal config

```json5
{
  "providers": {
    "anthropic": {
      "kind": "anthropic",
      "api_key": "sk-ant-..."
    }
  },
  "agents": {
    "list": [
      {
        "id": "default",
        "name": "Assistant",
        "model": "anthropic/claude-sonnet-4-5-20250514"
      }
    ]
  },
  "bindings": [
    { "agentId": "default", "match": { "channel": "cli" } }
  ],
  "channels": {
    "cli": { "enabled": true, "interactive": true }
  }
}
```

### Environment variables

API keys can be set via environment variables instead of (or in addition to) the config file. Environment variables take precedence.

**macOS / Linux:**

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
export OPENAI_API_KEY="sk-..."
export OLLAMA_BASE_URL="http://localhost:11434/v1"
```

**Windows (PowerShell — permanent):**

```powershell
[System.Environment]::SetEnvironmentVariable("ANTHROPIC_API_KEY", "sk-ant-...", "User")
[System.Environment]::SetEnvironmentVariable("OPENAI_API_KEY", "sk-...", "User")
```

**Windows (Command Prompt — current session only):**

```cmd
set ANTHROPIC_API_KEY=sk-ant-...
set OPENAI_API_KEY=sk-...
```

The naming convention is `{PROVIDER}_API_KEY` or `{PROVIDER}_AUTH_TOKEN`, and `{PROVIDER}_BASE_URL` for custom endpoints. The `{PROVIDER}` part is the uppercased version of the provider name from your config (e.g., a provider named `"deepseek"` uses `DEEPSEEK_API_KEY`).

### Config hot-reload

Felix watches the config file for changes. When you edit `felix.json5` while the gateway is running, it hot-reloads automatically — no restart needed.

---

## LLM Providers

Felix supports multiple LLM providers simultaneously. Each provider is defined in the `providers` section of the config with a unique name and a `kind` that determines how Felix communicates with it.

### Provider kinds

| Kind | Description | Use for |
|------|-------------|---------|
| `anthropic` | Anthropic's native API (custom SSE streaming) | Claude models |
| `openai` | OpenAI's native API | GPT models |
| `gemini` | Google's Gemini API (native SDK) | Gemini models |
| `qwen` | Alibaba Cloud's Qwen (DashScope) | Qwen models |
| `openai-compatible` | Any API that implements the OpenAI chat completions spec | Ollama, LM Studio, DeepSeek, LiteLLM, vLLM, and more |
| `local` | Bundled Ollama runtime supervised by Felix; wired up by the onboarding wizard | Fully offline / no API key |

### Provider config fields

| Field | Type | Description |
|-------|------|-------------|
| `kind` | string | Required. One of `"anthropic"`, `"openai"`, `"gemini"`, `"qwen"`, `"openai-compatible"`, or `"local"` |
| `api_key` | string | API key or auth token. Not needed for `local` or for self-hosted servers like Ollama |
| `base_url` | string | Custom API endpoint. Required for `"openai-compatible"`, optional for others |

### Setting up standard providers

#### Anthropic (Claude)

1. Get an API key from [console.anthropic.com](https://console.anthropic.com/)
2. Add to your config:

```json5
{
  "providers": {
    "anthropic": {
      "kind": "anthropic",
      "api_key": "sk-ant-api03-..."
    }
  }
}
```

Or set via environment variable:

```bash
export ANTHROPIC_API_KEY="sk-ant-api03-..."
```

Available models (use with `"model": "anthropic/<model-name>"`):

| Model | Description |
|-------|-------------|
| `claude-sonnet-4-5-20250514` | Best balance of speed and capability |
| `claude-opus-4-0-20250514` | Most capable, best for complex tasks |
| `claude-haiku-3-5-20241022` | Fastest, best for simple tasks |

Example agent config:

```json5
{
  "id": "default",
  "model": "anthropic/claude-sonnet-4-5-20250514"
}
```

#### OpenAI (GPT)

1. Get an API key from [platform.openai.com](https://platform.openai.com/api-keys)
2. Add to your config:

```json5
{
  "providers": {
    "openai": {
      "kind": "openai",
      "api_key": "sk-proj-..."
    }
  }
}
```

Or set via environment variable:

```bash
export OPENAI_API_KEY="sk-proj-..."
```

Available models (use with `"model": "openai/<model-name>"`):

| Model | Description |
|-------|-------------|
| `gpt-4o` | Most capable GPT model |
| `gpt-4o-mini` | Faster, cheaper alternative |
| `gpt-4-turbo` | Previous generation |
| `o3-mini` | Reasoning model |

Example agent config:

```json5
{
  "id": "researcher",
  "model": "openai/gpt-4o"
}
```

### Setting up custom / OpenAI-compatible providers

Any service that exposes an OpenAI-compatible API (i.e., a `/v1/chat/completions` endpoint) can be used with kind `"openai-compatible"`. Set `base_url` to the API root (the part before `/chat/completions`).

#### Ollama (local models)

[Ollama](https://ollama.com/) runs open-source models locally. No API key needed.

1. Install and start Ollama: `ollama serve`
2. Pull a model: `ollama pull llama3`
3. Add to your config:

```json5
{
  "providers": {
    "ollama": {
      "kind": "openai-compatible",
      "base_url": "http://localhost:11434/v1"
    }
  }
}
```

Example agent config:

```json5
{
  "id": "local",
  "model": "ollama/llama3"
}
```

Popular Ollama models: `llama3`, `llama3.1`, `mistral`, `codellama`, `qwen2.5`, `deepseek-coder`, `phi3`, `gemma2`.

For vision-capable models, use `llava` or `bakllava`.

#### LM Studio (local models)

[LM Studio](https://lmstudio.ai/) provides a GUI for running local models with a built-in OpenAI-compatible server.

1. Download and install LM Studio
2. Load a model and start the local server (default port: 1234)
3. Add to your config:

```json5
{
  "providers": {
    "lmstudio": {
      "kind": "openai-compatible",
      "base_url": "http://localhost:1234/v1"
    }
  }
}
```

Example agent config:

```json5
{
  "id": "local",
  "model": "lmstudio/qwen2.5-coder-14b"
}
```

The model name after the slash should match what LM Studio reports for the loaded model.

#### DeepSeek

[DeepSeek](https://platform.deepseek.com/) offers cloud-hosted models with an OpenAI-compatible API.

1. Get an API key from [platform.deepseek.com](https://platform.deepseek.com/)
2. Add to your config:

```json5
{
  "providers": {
    "deepseek": {
      "kind": "openai-compatible",
      "api_key": "sk-...",
      "base_url": "https://api.deepseek.com/v1"
    }
  }
}
```

Or set via environment variables:

```bash
export DEEPSEEK_API_KEY="sk-..."
export DEEPSEEK_BASE_URL="https://api.deepseek.com/v1"
```

Example agent config:

```json5
{
  "id": "coder",
  "model": "deepseek/deepseek-chat"
}
```

Available models: `deepseek-chat`, `deepseek-coder`, `deepseek-reasoner`.

#### Google Gemini

Felix has a native Gemini provider using the official Google GenAI SDK.

1. Get an API key from [aistudio.google.com](https://aistudio.google.com/apikey)
2. Add to your config:

```json5
{
  "providers": {
    "gemini": {
      "kind": "gemini",
      "api_key": "AIza..."
    }
  }
}
```

Or set via environment variables:

```bash
export GEMINI_API_KEY="AIza..."
```

Example agent config:

```json5
{
  "id": "default",
  "model": "gemini/gemini-2.5-flash"
}
```

Available models: `gemini-2.5-flash`, `gemini-2.5-pro`, `gemini-2.0-flash`.

#### LiteLLM / Other proxies

[LiteLLM](https://github.com/BerriAI/litellm) is a proxy that exposes 100+ LLMs through a single OpenAI-compatible API. The same pattern works for vLLM, Anyscale, Together AI, or any OpenAI-compatible proxy.

```json5
{
  "providers": {
    "litellm": {
      "kind": "openai-compatible",
      "base_url": "http://localhost:4000/v1"
    }
  }
}
```

### Model reference format

Agents reference models as `provider/model-name`, where the provider name matches a key in your `providers` section:

```
anthropic/claude-sonnet-4-5-20250514    → uses the "anthropic" provider
openai/gpt-4o                          → uses the "openai" provider
ollama/llama3                           → uses the "ollama" provider
deepseek/deepseek-chat                  → uses the "deepseek" provider
gemini/gemini-2.5-flash                 → uses the "gemini" provider
lmstudio/qwen2.5-coder-14b             → uses the "lmstudio" provider
```

You can override the model for a CLI chat session without changing the config:

```bash
felix chat -m openai/gpt-4o
felix chat -m ollama/codellama
```

### Using multiple providers

You can configure multiple providers and assign different agents to different models:

```json5
{
  "providers": {
    "anthropic": { "kind": "anthropic", "api_key": "sk-ant-..." },
    "openai":    { "kind": "openai",    "api_key": "sk-..." },
    "ollama":    { "kind": "openai-compatible", "base_url": "http://localhost:11434/v1" },
    "deepseek":  { "kind": "openai-compatible", "api_key": "sk-...", "base_url": "https://api.deepseek.com/v1" }
  },
  "agents": {
    "list": [
      { "id": "default",  "model": "anthropic/claude-sonnet-4-5-20250514" },
      { "id": "reviewer", "model": "openai/gpt-4o" },
      { "id": "quick",    "model": "ollama/llama3" },
      { "id": "coder",    "model": "deepseek/deepseek-chat" }
    ]
  }
}
```

Only providers that are actually referenced by agents are initialized at startup.

---

## Messaging Channels

### CLI

The CLI is the only inbound channel currently shipped. It is always
available via `felix chat` and renders Markdown responses in your
terminal.

```bash
# Default agent
felix chat

# Specific agent
felix chat coder

# Override the model for this session
felix chat -m anthropic/claude-opus-4-0-20250514
```

The web chat page served by the gateway at `http://127.0.0.1:18789/chat`
talks to the same agent runtime over the WebSocket API, so any agent
bound to the `cli` channel is reachable from the browser too.

### Outbound-only Telegram via the `send_message` tool

There is no inbound Telegram or WhatsApp channel adapter — Felix used to
ship those but they were retired. What remains is the **outbound**
`send_message` tool, which can push a message to a Telegram chat via
the Bot API. Use this for cron alerts when you want notifications on
your phone but the agent itself runs on your machine and is reached
via CLI or the web chat. See
[Send Message Tool](#send-message-tool) below.

---

## Image / Vision Support

Felix supports sending images to vision-capable LLMs (Claude, GPT-4o,
Gemini, multimodal Ollama models). The LLM sees the actual image
pixels — not just metadata. Images can be attached from `felix chat`
(by typing a file path) or from the web chat page (paste / drag-drop).

### How it works

1. **You attach an image** via the CLI (file path in your message), the web chat page (paste or drag-drop), or the WebSocket API
2. **Felix reads the image bytes** from the local filesystem (CLI) or from the WebSocket payload (web chat / API)
3. **The image is passed to the LLM** as a multipart content block alongside your text
4. **The LLM responds** with a description, analysis, or answer about the image
5. **Images are persisted** in the session history (base64-encoded in JSONL) so the LLM can reference them in follow-up messages

### CLI

In `felix chat`, include an image file path in your message. Felix detects image paths, reads the file, and sends the bytes to the LLM.

**Supported input formats:**

```bash
# Bare path
> What's in this image? /Users/me/photo.png

# Tilde expansion
> Describe ~/Downloads/screenshot.jpg

# Drag-and-drop from Finder (macOS pastes a quoted path)
> Tell me about this '/Users/me/My Photos/vacation.png'

# Drag-and-drop with escaped spaces
> Analyze /Users/me/My\ Photos/vacation.png

# Image path only (defaults to "What's in this image?")
> ~/Downloads/photo.jpg
```

**Supported image formats:** `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.bmp` (max 10MB)

### Screenshots

Use the `/screenshot` command in `felix chat` to interactively capture a window and send it to the LLM.

```bash
# Capture a window (click to select) and ask the LLM about it
> /screenshot

# Capture with a specific prompt
> /screenshot What's wrong with this UI?
> /screenshot Summarize the text in this window
> /screenshot Convert this table to CSV
```

**How it works:**

1. Type `/screenshot` (optionally followed by a prompt)
2. Your cursor changes to a crosshair — click on the window you want to capture
3. The screenshot is captured, sent to the LLM, and the LLM responds

**Platform support:**

| Platform | Tool used | Selection mode |
|----------|-----------|----------------|
| macOS | `screencapture` (built-in) | Click a window |
| Linux | `maim`, `gnome-screenshot`, or `scrot` | Click a window or drag to select |
| Windows | PowerShell + .NET `System.Windows.Forms` | Full screen capture |

### Web chat

The web chat page at `http://127.0.0.1:18789/chat` accepts pasted
images and drag-and-drop. Bytes are sent over the WebSocket to the
agent, which forwards them to the LLM as a multipart content block.

### Supported LLM providers

Image/vision support works with providers that support multimodal input:

| Provider | Vision support |
|----------|---------------|
| Anthropic (Claude) | Yes — uses `image` content blocks |
| OpenAI (GPT-4o, etc.) | Yes — uses `image_url` with data URIs |
| Gemini | Yes — uses inline image parts |
| Ollama | Depends on the model (e.g., `llava`, `bakllava`, `gemma3`) |

If the model doesn't support vision, it will typically ignore the image or return an error.

---

## Multiple Agents

You can run multiple agents, each with its own model, workspace, and tool permissions.

```json5
{
  "agents": {
    "list": [
      {
        "id": "coder",
        "name": "Coder",
        "model": "anthropic/claude-sonnet-4-5-20250514",
        "workspace": "~/.felix/workspace-coder",
        "tools": {
          "allow": ["read_file", "write_file", "edit_file", "bash"]
        }
      },
      {
        "id": "researcher",
        "name": "Researcher",
        "model": "openai/gpt-4o",
        "workspace": "~/.felix/workspace-researcher",
        "tools": {
          "allow": ["read_file", "web_fetch", "web_search"]
        }
      },
      {
        "id": "local",
        "name": "Local Assistant",
        "model": "ollama/llama3",
        "workspace": "~/.felix/workspace-local",
        "tools": {
          "allow": ["read_file"]
        }
      }
    ]
  },
  "providers": {
    "anthropic": { "kind": "anthropic", "api_key": "sk-ant-..." },
    "openai":    { "kind": "openai",    "api_key": "sk-..." },
    "ollama":    { "kind": "openai-compatible", "base_url": "http://localhost:11434/v1" }
  }
}
```

Chat with a specific agent:

```bash
felix chat coder
felix chat researcher
felix chat local
```

### Inter-agent delegation

Agents can delegate subtasks to other agents using the `task` tool. This enables supervisor/worker patterns where a powerful model orchestrates cheaper or specialized models. See [Subagents and the `task` tool](#subagents-and-the-task-tool) for details.

### Agent identity

Each agent's system prompt is resolved in this priority order:

1. **`system_prompt` in config** — if the agent config has a non-empty `system_prompt` field, it is used directly
2. **`IDENTITY.md` in workspace** — if the file exists in the agent's workspace directory
3. **Built-in default** — a generic helpful-assistant prompt

Inline config example:

```json5
{
  "id": "coder",
  "model": "anthropic/claude-sonnet-4-5-20250514",
  "system_prompt": "You are a senior Go developer. You write clean, idiomatic Go code. Always write tests for new code."
}
```

Or use an `IDENTITY.md` file in the workspace:

```bash
cat > ~/.felix/workspace-coder/IDENTITY.md << 'EOF'
You are a senior Go developer. You write clean, idiomatic Go code.
You prefer the standard library over external dependencies.
Always write tests for new code.
EOF
```

### Agent self-awareness

Every agent automatically knows:

- **Who it is** — its own name and ID (e.g., "You are the 'Coder' agent (id: coder)")
- **Where its config lives** — the path to `felix.json5` and the data directory
- **What other agents exist** — a summary of all configured agents with their IDs, models, and tools
- **What channels are connected** — currently `cli` (the only inbound channel; web chat shares the same agent runtime)

This is injected automatically into every agent's system prompt, so agents can reference each other and understand the broader system topology without any manual configuration.

### Dynamic default identity

When no `system_prompt` or `IDENTITY.md` is provided, the built-in default identity is tailored to the agent's actual tool set. An agent with only `read_file` and `bash` will be told it can read files and execute commands — but won't claim it can search the web or send messages. This prevents agents from hallucinating capabilities they don't have.

### Model fallbacks

If the primary model's provider is unavailable, the agent can fall back to alternatives:

```json5
{
  "id": "resilient",
  "model": "anthropic/claude-sonnet-4-5-20250514",
  "fallbacks": [
    "openai/gpt-4o",
    "ollama/llama3"
  ]
}
```

---

## Message Routing

Bindings control which agent handles messages from which channel/sender.
Today the only inbound channel is `cli`, so routing in practice means
"which agent does the CLI talk to" (and which agent the web chat
selector defaults to). The matcher itself is more general — kept that
way because future channel adapters and the existing web-chat agent
selector both go through it — but a current config is usually one
binding per agent against `channel: "cli"`.

**Priority order:** `peer.id` > `peer.kind` > `accountId` > `channel` > default

```json5
{
  "bindings": [
    // The default agent handles the CLI / web chat
    { "agentId": "default", "match": { "channel": "cli" } }

    // (Multi-agent setups: bind each agent to channel cli; the user
    //  picks the agent in the web chat selector or via
    //  `felix chat <agent-id>` on the CLI.)
  ]
}
```

---

## Skills

Skills are Markdown files with YAML frontmatter that get injected into the agent's context when relevant. They teach agents domain-specific knowledge without modifying code.

### Skill file format

You can either drop a flat `<name>.md` directly into `~/.felix/skills/`, or create a `SKILL.md` file in `~/.felix/skills/<skill-name>/SKILL.md` (or the agent's workspace at `<workspace>/skills/<skill-name>/SKILL.md`). The flat form is what the Settings UI uploads:

```markdown
---
name: git-workflow
description: Guidelines for using git in this project
tags:
  - git
  - version-control
  - commit
---

## Git Workflow

- Always create feature branches from `main`
- Use conventional commit messages: `feat:`, `fix:`, `docs:`, `refactor:`
- Run tests before committing
- Squash merge into main
```

### How skills are matched

When a user sends a message, Felix matches it against skill names, descriptions, and tags using keyword scoring. The top 3 matching skills are injected into the agent's system prompt for that turn.

For example, if the user says "commit my changes", skills tagged with `git` or `commit` will be included.

### Skill directories

Skills are loaded from:
1. `~/.felix/skills/` — shared across all agents
2. `<agent-workspace>/skills/` — agent-specific skills

### Manage skills via the Settings UI

Open `http://127.0.0.1:18789/settings` and click the **Skills** tab. From there you can:

- **Upload** a `.md` file (256 KB max). The file is written to `~/.felix/skills/` and immediately picked up — the loader refreshes in place, so the new skill is available on the very next chat turn with no restart needed.
- **View** the raw markdown of any uploaded skill in a side panel.
- **Delete** an uploaded skill. The file is removed from disk and dropped from the loader on the same turn.

Skills with malformed YAML frontmatter or missing required binaries (declared via `metadata.openclaw.requires.bins`) still appear in the list with a warning indicator so you can fix or remove them — they're just not loaded into the agent's context.

The same operations are available over REST:

| Method | Path | Body | Response |
|--------|------|------|----------|
| `GET` | `/settings/api/skills` | — | `{"skills":[{name,filename,description,tags,size_bytes,modified,unavailable,missing_bins,parse_error}, ...]}` |
| `GET` | `/settings/api/skills/{name}` | — | Raw markdown as `text/plain` |
| `POST` | `/settings/api/skills` | `multipart/form-data` with field `file` | `{"ok":true,"name":"...","filename":"..."}`; `409` if a file with that name already exists |
| `DELETE` | `/settings/api/skills/{name}` | — | `{"ok":true}`; `404` if missing |

All four routes inherit the gateway's bearer-auth middleware (when `gateway.auth.token` is set in `felix.json5`).

---

## Memory

Memory gives agents persistent knowledge across conversations. Entries are stored as Markdown files and retrieved via BM25 text search.

### Enable memory

```json5
{
  "memory": {
    "enabled": true
  }
}
```

### How it works

- Memory entries are stored as `.md` files in `~/.felix/memory/entries/`
- When enabled, relevant memories are automatically retrieved and injected into the agent's context each turn
- The agent can create, update, and delete memory entries during conversations
- Retrieval uses BM25 keyword search over entry content

### Manually add a memory entry

Create a Markdown file directly:

```bash
cat > ~/.felix/memory/entries/project-conventions.md << 'EOF'
# Project Conventions

- We use Go 1.22+ with generics where appropriate
- All HTTP handlers use chi router
- Tests use testify/assert
- Errors are wrapped with fmt.Errorf("context: %w", err)
EOF
```

The agent will recall this information when it's relevant to the conversation.

---

## Cron Jobs

Schedule recurring prompts for an agent. Cron is the single recurring-work mechanism in Felix — a previous "heartbeat" daemon (which read `HEARTBEAT.md` per agent on a fixed interval) was removed in favour of explicit cron jobs that you create and manage individually. To run a periodic checklist (uptime check, disk warnings, etc.), schedule a cron job whose prompt embeds the checklist or instructs the agent to read a file you maintain.

```json5
{
  "agents": {
    "list": [
      {
        "id": "ops",
        "name": "Ops Agent",
        "model": "anthropic/claude-sonnet-4-5-20250514",
        "cron": [
          {
            "name": "daily-summary",
            "schedule": "24h",
            "prompt": "Generate a summary of all log files in /var/log/ from the past 24 hours. Highlight any errors or warnings."
          },
          {
            "name": "hourly-check",
            "schedule": "1h",
            "prompt": "Check if the API at https://api.example.com/health returns 200. If not, write the error to /tmp/api-alert.txt."
          }
        ]
      }
    ]
  }
}
```

Schedule values are Go duration strings: `30m`, `1h`, `6h`, `24h`, etc.

---

## Browser Tool

The `browser` tool gives agents headless Chrome automation capabilities — navigate to pages, click elements, type text, extract content, take screenshots, and run JavaScript.

### Enable for an agent

```json5
{
  "tools": {
    "allow": ["browser"]
  }
}
```

### Available actions

| Action | Description |
|--------|-------------|
| `navigate` | Navigate to a URL. Requires `url` |
| `click` | Click an element by CSS selector. Optional `url` to navigate first |
| `type` | Type text into an input by CSS selector. Optional `url` to navigate first |
| `get_text` | Extract text content from an element or full page. Optional `url` to navigate first |
| `screenshot` | Take a screenshot. Optional `url` to navigate first |
| `evaluate` | Run arbitrary JavaScript and return the result. Optional `url` to navigate first |

All actions except `navigate` accept an optional `url` parameter. When provided, the browser navigates to that URL before performing the action. This lets you fetch page content, take screenshots, or interact with elements in a single tool call without a separate `navigate` step.

### Example conversation

```
You: Go to https://news.ycombinator.com and get the title of the top story.
Agent: [uses browser tool: get_text with url="https://news.ycombinator.com", selector=".titleline"]
       The top story is: "Show HN: Felix — self-hosted AI agent gateway in Go"

You: Take a screenshot of that page.
Agent: [uses browser tool: screenshot with url="https://news.ycombinator.com"]
       Here's the screenshot of the Hacker News front page.

You: Click on the "new" link at the top.
Agent: [uses browser tool: click with url="https://news.ycombinator.com", selector="a.new"]
       Done. I've navigated to the newest submissions page.
```

### How it works

Each invocation creates a fresh headless Chrome context with these flags:
- `--headless` — no visible browser window
- `--disable-gpu` — for compatibility on servers
- `--no-sandbox` — required in some container environments

The browser context is destroyed after the action completes, so state (cookies, sessions) does not persist between calls. For multi-step workflows, chain actions in a single conversation turn.

**Requirement:** Chrome or Chromium must be installed on the host machine.

---

## Send Message Tool

The `send_message` tool lets agents proactively push messages to a
Telegram chat via the Bot API. This is the **only** outbound channel
currently wired up — useful for cron alerts so you get notified on
your phone without running an inbound channel adapter.

### Configure

Set the bot token (from [@BotFather](https://t.me/BotFather)) under
`telegram.bot_token` in your config, then allow the tool on the
agent that needs it:

```json5
{
  "telegram": {
    "enabled": true,
    "bot_token": "123456:ABC-DEF...",
    "default_chat_id": "123456789"  // optional fallback when the agent omits chat_id
  },
  "agents": {
    "list": [
      { "id": "default", "tools": { "allow": ["send_message"] } }
    ]
  }
}
```

### Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `chat_id` | string | Telegram chat id. Optional if `telegram.default_chat_id` is set in the config |
| `text` | string | Message content (Markdown rendering follows Telegram's MarkdownV2 rules) |

### Example use

```
You: If the API health check fails, alert me.
Agent: [checks API, finds it's down]
       [send_message: chat_id="123456789",
        text="Alert: API health check failed at 14:32 UTC"]
       Sent.
```

### Finding your chat id

Open a chat with your bot in Telegram, send any message, and watch
the gateway log — Felix logs the `chat_id` of incoming Bot API
updates as it polls. You can also use any third-party "userinfobot"
to look up your numeric Telegram user id.

---

## Dynamic Cron Tool

The `cron` tool lets agents dynamically create, list, pause, resume, remove, and update recurring scheduled tasks at runtime — without editing the config file.

### Enable for an agent

```json5
{
  "tools": {
    "allow": ["cron"]
  }
}
```

### Actions

**Add a job:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `action` | string | `"add"` |
| `name` | string | Unique job name |
| `schedule` | string | Go duration string (`"30m"`, `"1h"`, `"24h"`) |
| `prompt` | string | The prompt to send to the agent on each tick |

**List jobs:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `action` | string | `"list"` |

**Remove a job:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `action` | string | `"remove"` |
| `name` | string | Name of the job to remove |

**Pause a job:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `action` | string | `"pause"` |
| `name` | string | Name of the job to pause |

**Resume a job:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `action` | string | `"resume"` |
| `name` | string | Name of the job to resume |

**Update a job's schedule:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `action` | string | `"update"` |
| `name` | string | Name of the job to update |
| `schedule` | string | New schedule duration |

### Example conversation

```
You: Check the disk usage every hour and warn me if it's above 80%.
Agent: [uses cron tool: action="add", name="disk-check", schedule="1h",
        prompt="Check disk usage with 'df -h'. If any partition is above 80%, alert me."]
       Done. I've scheduled a recurring disk check every hour.

You: What cron jobs are currently running?
Agent: [uses cron tool: action="list"]
       Currently scheduled jobs:
       1. disk-check — every 1h — "Check disk usage with 'df -h'..."
       2. daily-summary — every 24h — "Generate a summary of..."

You: Pause the disk check job for now.
Agent: [uses cron tool: action="pause", name="disk-check"]
       Done. The disk-check job is paused. It won't run until you resume it.

You: Change the daily summary to run every 12 hours instead.
Agent: [uses cron tool: action="update", name="daily-summary", schedule="12h"]
       Done. The daily-summary job now runs every 12 hours.

You: Remove the disk check job entirely.
Agent: [uses cron tool: action="remove", name="disk-check"]
       Done. The disk-check job has been removed.
```

### Static vs dynamic cron

Felix supports cron jobs in two ways:

1. **Static (config file)** — Define cron jobs in `felix.json5` under the agent's `cron` array. These persist across restarts.
2. **Dynamic (cron tool)** — Agents create jobs at runtime via the `cron` tool. These are created on-the-fly during conversations.

Both use the same underlying scheduler. Static jobs are ideal for always-on tasks, while dynamic jobs let agents self-organize based on user requests.

---

## Subagents and the `task` tool

A **subagent** is an agent another agent can delegate work to via the built-in `task` tool. The supervisor's LLM sees `task` like any other tool, picks a subagent by ID, and gets back the subagent's final text. The subagent runs with its own model, system prompt, tool policy, and workspace — so you can mix Sonnet for reasoning, Haiku for cheap research, Gemma for fully-local lookups, all in one chat.

### The two flags

- **`subagent: true`** on the *target* agent — the opt-in. Without this, the agent is invisible to `task`.
- **`task` in the *supervisor's* `tools.allow`** — without this, the supervisor's LLM never sees the tool and never delegates.

(If you use `tools.allow: []` for allow-all on the supervisor, `task` is included automatically when at least one subagent is configured.)

### Worked example: Coder + Researcher + Reviewer

Edit `~/.felix/felix.json5` (or use Settings → Agents). Hot-reload picks the changes up on the next chat send — no restart needed.

```json5
{
  "providers": {
    "anthropic": { "kind": "anthropic", "api_key": "sk-ant-..." },
    "local":     { "kind": "local" }
  },
  "agents": {
    "list": [
      // SUPERVISOR — this is the agent you chat with.
      {
        "id": "coder",
        "name": "Coder",
        "model": "anthropic/claude-sonnet-4-5-20250514",
        "workspace": "~/code/myproject",
        "tools": {
          "allow": ["read_file", "write_file", "edit_file", "bash", "task"]
        }
      },

      // SUBAGENT 1 — cheap web research, runs locally.
      {
        "id": "researcher",
        "name": "Researcher",
        "model": "local/gemma4",
        "workspace": "~/.felix/workspace-researcher",
        "subagent": true,
        "description": "Searches the web and summarises sources. Returns a short bulleted brief with citations. Use for any 'find me...' or 'what's the latest on...' question.",
        "tools": {
          "allow": ["web_search", "web_fetch", "read_file"]
        }
      },

      // SUBAGENT 2 — careful code review on Opus.
      {
        "id": "reviewer",
        "name": "Reviewer",
        "model": "anthropic/claude-opus-4-1-20250805",
        "workspace": "~/code/myproject",   // same workspace as coder so it can read the diff
        "subagent": true,
        "description": "Reviews code changes for correctness, security, and clarity. Pass it the file path or the diff text. Returns a verdict + actionable findings.",
        "inheritContext": true,            // reviewer sees the supervisor's conversation
        "tools": {
          "allow": ["read_file", "bash"]   // read-only; can't edit or write
        }
      }
    ]
  }
}
```

### Verify the wiring

Restart `felix start` (or just open a new chat — the per-chat tool overlay rebuilds), pick the **Coder** agent in the chat header, and click the **Tools** button. You should see `task` listed. Hovering it reveals the description block:

```
Available subagents:
  - researcher: Searches the web and summarises sources...
  - reviewer:   Reviews code changes for correctness, security, and clarity...
```

### Try it

> **You:** Look up best practices for Go context cancellation in long-running goroutines, then refactor `internal/agent/runtime.go` to apply them. Have the reviewer check your work.

What you'll see in the chat:

1. **Coder** calls `task({"agent_id": "researcher", "prompt": "Find current best practices..."})`. The chat shows a `task` tool-call expanding inline; the **researcher**'s own `web_search` and `web_fetch` calls stream as nested tool entries.
2. Researcher returns a brief; coder reads files, edits `runtime.go`.
3. Coder calls `task({"agent_id": "reviewer", "prompt": "Review the changes I just made to internal/agent/runtime.go"})`. Because `reviewer` has `inheritContext: true`, it starts with the coder's conversation history loaded — it knows what changes were made without you having to repeat.
4. Reviewer reads the file, returns a verdict.
5. Coder writes back a final answer summarising both delegations.

### Parameters reference

| Parameter | Type | Description |
|-----------|------|-------------|
| `agent_id` | string | The ID of the subagent. Must match a `subagent: true` agent in your config. |
| `prompt` | string | What to ask the subagent. The subagent has no access to your conversation unless you set `inheritContext: true` on it, so the prompt must be self-contained. |

### `inheritContext` — when to use it

Default is `false` (cold start). Set `inheritContext: true` on the subagent's config when:

- The subagent's job is to **review or comment on** what the supervisor just did (a reviewer doesn't need a fresh prompt explaining everything; it just needs the history).
- You're using the subagent as a **second opinion** on a long conversation.
- The subagent is an **explorer/searcher** that benefits from knowing the project context the supervisor built up.

Leave it `false` (default) when:

- The subagent does an isolated, well-scoped task (web research, a one-off file generation).
- You want to **reduce token usage** — inheriting context means the subagent pays for the full history on its first turn.
- The subagent uses a smaller model that would be confused by a long, off-topic history.

### How it works under the hood

- **Per-chat overlay registration** — every time you start a chat with a supervisor agent, Felix checks `cfg.EligibleSubagents()` and, if non-empty, wires the `task` tool into that supervisor's tool registry. Adding/removing subagents in config is hot-reloaded; the next `chat.send` picks up the change.
- **Subagent session is ephemeral** — a fresh in-memory session keyed `subagent`, never persisted to disk. The supervisor's session is the durable record of the conversation; the subagent's transcript lives only in memory and is lost when its `Run` returns.
- **Event forwarding** — the subagent's tool calls and tool results are forwarded to the supervisor's event channel so the chat UI shows them inline. The subagent's *final assistant text* becomes the `task` tool's `Output` that the supervisor's LLM sees as the result.
- **Workspace separation** — file tools the subagent runs are scoped to *its* workspace, not the supervisor's. If the subagent needs to read files in the supervisor's project, point both `workspace` paths at the same directory (as the `reviewer` does above).
- **No Cortex ingest from subagents** — the conversation isn't ingested into the knowledge graph; only the supervisor's main loop ingests on completion.

### Recursion and depth cap

Subagents do **not** themselves get the `task` tool registered, so a subagent cannot delegate further out of the box. The runtime additionally enforces a depth cap (`agentLoop.maxAgentDepth`, default 3) as defense-in-depth — if you ever flip the registration rule to allow recursive delegation, the cap stops runaway loops.

### Common gotchas

- **`task` not in supervisor's `tools.allow`** → the supervisor's LLM never sees the tool. Either add `task` explicitly or use `allow: []` (allow-all).
- **`subagent: true` missing on the target** → `task` returns `"agent X is not registered as a subagent"`.
- **Wrong `workspace` on the subagent** → `bash`/file tools the subagent runs hit *its* workspace. To share files with the supervisor, set both `workspace` paths the same.
- **Subagent uses the same expensive model** → defeats one of the main reasons to delegate. Pick a cheaper subagent model when the task allows it.
- **Multiple parallel `task` calls** → not supported (the tool reports `IsConcurrencySafe = false`). The supervisor will dispatch them sequentially.

### Use cases at a glance

- **Cost / latency optimisation** — expensive supervisor (Sonnet/Opus) orchestrates; cheap subagents (Haiku/Gemma/local) do bulk work.
- **Capability mixing** — different providers' strengths in one chat (e.g., Claude for reasoning + Gemini Flash for long-context summarisation + local model for offline lookups).
- **Specialisation by tool policy** — a `web` subagent that only has `web_*` tools, a `coder` subagent with file I/O, a read-only `reviewer` with no write capability — security boundary by config, not code.
- **Second-opinion patterns** — supervisor proposes; reviewer (with `inheritContext: true`) critiques; supervisor revises.

---

## Tool Policies

Each agent can have its own tool allow/deny list, controlling what actions it can perform.

### Available tools

| Tool | Description |
|------|-------------|
| `read_file` | Read file contents |
| `write_file` | Create or overwrite files |
| `edit_file` | Make targeted edits to existing files |
| `bash` | Execute shell commands (uses `bash` on macOS/Linux, `cmd.exe` on Windows) |
| `web_fetch` | Fetch a URL and return its content |
| `web_search` | Search the web |
| `browser` | Headless Chrome automation (navigate, click, type, screenshot, evaluate JS). All actions accept an optional `url` to navigate before acting |
| `send_message` | Push a message to a Telegram chat via the Bot API (outbound only) |
| `cron` | Dynamically schedule, list, pause, resume, remove, and update recurring tasks |
| `task` | Delegate a subtask to another configured agent and get back the result |
| `todo_write` | Per-workspace persistent todo list at `<workspace>/.felix/todos.json`. Used by the agent to externalise plans for any task with 3+ distinct steps |
| `load_skill` | Load a skill body on demand by name. Felix injects only the skills index into every turn — bodies are fetched only when the agent decides it needs one |
| `load_memory` | Same on-demand pattern for memory entries — load a single entry by id when the memory index says it's relevant |

### Policy examples

**Full access (default):**
```json5
{
  "tools": {
    "allow": ["read_file", "write_file", "edit_file", "bash", "web_fetch", "web_search"]
  }
}
```

**Read-only agent (safe for untrusted users):**
```json5
{
  "tools": {
    "allow": ["read_file", "web_fetch", "web_search"]
  }
}
```

**Everything except shell commands:**
```json5
{
  "tools": {
    "allow": ["*"],
    "deny": ["bash"]
  }
}
```

### Execution approvals

For additional safety, you can require approval for specific commands:

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

Approval levels:
- `"full"` — all commands allowed
- `"allowlist"` — only listed commands allowed
- `"deny"` — no command execution

---

## WebSocket API

When the gateway is running (`felix start`), it exposes a JSON-RPC 2.0 API over WebSocket at `ws://127.0.0.1:18789/ws`.

### HTTP endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /` | Redirects to `/chat` |
| `GET /health` | Health check (returns `{"status":"ok"}`) |
| `GET /ws` | WebSocket endpoint |
| `GET /metrics` | Prometheus-style metrics (if enabled) |
| `GET /ui` | Control panel UI |
| `GET /chat` | Web chat interface (light/dark mode, streaming, agent selector) |
| `GET /jobs` | Cron jobs dashboard (view active scheduled tasks) |
| `GET /logs` | Recent gateway log viewer (live tail at `/logs/stream`) |
| `GET /settings` | Settings UI — Agents / Providers / Models / Intelligence / Security / Messaging / MCP / Gateway / Skills tabs |
| `GET/POST/DELETE /settings/api/skills*` | Manage skills in `~/.felix/skills/` (list / view / upload / delete). See the [Skills](#skills) section. |

### Send a chat message

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
  const response = JSON.parse(event.data);
  // response.result.type is one of:
  //   "text_delta"      — streaming text chunk
  //   "tool_call_start" — agent is calling a tool
  //   "tool_result"     — tool execution result
  //   "done"            — response complete
  //   "error"           — error occurred
  console.log(response.result);
};
```

### Query agent status

```javascript
ws.send(JSON.stringify({
  jsonrpc: "2.0",
  method: "agent.status",
  id: 2
}));
// Returns: { agents: [{ id, name, model, workspace }, ...] }
```

### Available methods

| Method | Description |
|--------|-------------|
| `chat.send` | Send a message to an agent (streams response events) |
| `chat.abort` | Cancel the active response for this connection |
| `chat.compact` | Force-compact the active session immediately |
| `agent.status` | List all configured agents and their state |
| `session.list` | List sessions for an agent |
| `session.new` | Start a fresh session for an agent |
| `session.switch` | Switch the active session for an agent |
| `session.history` | Load conversation history for an agent |
| `session.clear` | Clear an agent's session history |

### Using curl to check health

```bash
curl http://127.0.0.1:18789/health
```

---

## OpenTelemetry Export

Felix can export traces, metrics, and logs to any OTLP/HTTP-compatible
collector (Tempo, Jaeger, Loki, Grafana Cloud, Honeycomb, your own
OpenTelemetry Collector, etc.). This is in addition to the local
`/metrics` and `/logs` views — Felix never replaces them with the
remote pipeline. Default: **disabled**.

When enabled, every chat turn becomes one OTel span named `agent.run`
with span events for every phase the agent loop emits today
(`cortex.recall`, `context.assemble`, `llm.request_sent`,
`llm.first_token`, `llm.stream_end`, `tool.exec`, `agent.done`, etc.).
Each event carries the same key/value attributes the perf-log lines
already include — turn number, model, error flag, output size, etc.
You see the per-turn timeline in any OTel-compatible UI without
parsing logs.

Metrics emitted:

| Metric | Type | Notes |
|---|---|---|
| `felix.uptime.seconds` | gauge | seconds since gateway start |
| `felix.http.requests` | counter | HTTP requests handled |
| `felix.ws.connections.active` | up-down counter | live WebSocket connections |
| `felix.ws.messages` | counter | WebSocket messages received |
| `felix.tool.calls` | counter | tagged with `tool.name=<tool>` |
| `felix.llm.calls` | counter | LLM API requests |
| `felix.errors` | counter | error events |

Logs are forwarded via the standard `slog`-to-OTLP bridge (every
`slog` record becomes one OTLP log record with the level, message,
and structured attributes intact).

### Enable via felix.json5

```json5
{
  "otel": {
    "enabled": true,
    "endpoint": "http://collector.example.com:4318/",
    "serviceName": "felix",
    "sampleRatio": 1.0,           // 0.0..1.0; 1.0 = export every span
    "signals": {
      "traces": true,
      "metrics": true,
      "logs": true
    }
    // Optional: extra HTTP headers for tenant routing, auth, etc.
    // "headers": { "X-Scope-OrgID": "tenant-1" }
  }
}
```

The endpoint is a full URL (scheme + host + optional port). Felix
appends `/v1/{traces,metrics,logs}` per signal — don't include those
paths in the URL. The scheme decides http vs https.

OTel changes require a **restart** to take effect. The OTel SDK does
not support swapping providers in flight, so the Settings UI surfaces
this with a "Restart required" note.

### Enable via env vars (no config edit needed)

Standard OTel SDK environment variables override the file values and
**implicitly enable** OTel when `OTEL_EXPORTER_OTLP_ENDPOINT` is set.
This is the easiest way to point Felix at a collector for an ad-hoc
debugging session:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT="http://collector.example.com:4318/" \
OTEL_SERVICE_NAME="felix-prod" \
./felix start
```

Recognised variables (precedence over file values):

| Var | Effect |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | sets `endpoint` and **implicitly enables OTel** |
| `OTEL_SERVICE_NAME` | sets `serviceName` |
| `OTEL_EXPORTER_OTLP_HEADERS` | parsed as `key1=v1,key2=v2`, merged into `headers` |
| `OTEL_TRACES_SAMPLER_ARG` | sets `sampleRatio` (float 0..1) |
| `OTEL_SDK_DISABLED=true` | forces OTel off, even if config or env enabled it |

### Settings UI

Settings → **Gateway** tab → **OpenTelemetry** section. The same
fields as the JSON config plus a Headers free-text box (key=value pairs,
comma-separated). Changes require a restart.

### Failure mode

Telemetry is auxiliary; if the collector is unreachable, exporter
init still succeeds (the OTel SDK retries in the background) and
Felix serves chat normally. You'll see periodic warnings in
`~/.felix/felix-app.log` along the lines of "context deadline
exceeded" — those are the failed batch posts. Felix never crashes
because the collector is down.

---

## Security

### Gateway auth

Protect the WebSocket API with a bearer token:

```json5
{
  "gateway": {
    "auth": {
      "token": "my-secret-token"
    }
  }
}
```

WebSocket clients must include the token in the connection header.

### Group policy (vestige from inbound channel adapters)

The `groupPolicy.requireMention` field is preserved in the config
schema for forward compatibility with future group-chat channel
adapters. With only the `cli` channel today it has no effect — the
default value (`true`) is fine. Leaving it documented so downgrading
back to a prior Felix that had Telegram/WhatsApp inbound doesn't
silently change behaviour.

```json5
{
  "security": {
    "groupPolicy": { "requireMention": true }
  }
}
```

---

## Example Configurations

### Personal assistant (Claude + outbound Telegram alerts)

The assistant chats over CLI / web; it can also push notifications to
your phone via the `send_message` tool when something interesting
happens during a cron tick.

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
        "model": "anthropic/claude-sonnet-4-5-20250514",
        "workspace": "~/.felix/workspace-assistant",
        "tools": {
          "allow": ["read_file", "write_file", "edit_file", "bash",
                    "web_fetch", "web_search", "send_message"]
        }
      }
    ]
  },
  "bindings": [
    { "agentId": "assistant", "match": { "channel": "cli" } }
  ],
  "channels": { "cli": { "enabled": true, "interactive": true } },
  "telegram": {
    "enabled": true,
    "bot_token": "123456:ABC...",
    "default_chat_id": "123456789"
  },
  "memory":    { "enabled": true }
}
```

### Multi-agent dev team (Claude + GPT + Ollama, supervisor delegates with `task`)

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
        "id": "lead",
        "name": "Tech Lead",
        "model": "anthropic/claude-sonnet-4-5-20250514",
        // The lead can delegate to any subagent via the `task` tool.
        "tools": { "allow": ["read_file", "bash", "task", "todo_write"] }
      },
      {
        "id": "coder",
        "name": "Senior Developer",
        "model": "anthropic/claude-sonnet-4-5-20250514",
        // subagent: true is the opt-in. Without it, `task` won't see this agent.
        "subagent": true,
        "description": "Writes and edits code. Use for any 'implement X' or 'refactor Y' subtask.",
        "tools": { "allow": ["read_file", "write_file", "edit_file", "bash"] }
      },
      {
        "id": "reviewer",
        "name": "Code Reviewer",
        "model": "openai/gpt-4o",
        "subagent": true,
        "description": "Reviews code changes for correctness and style. Read-only.",
        // inheritContext: true so the reviewer sees what the lead just discussed
        // without needing to re-explain the work in the prompt.
        "inheritContext": true,
        "tools": { "allow": ["read_file"] }
      },
      {
        "id": "quick",
        "name": "Quick Helper",
        "model": "ollama/llama3",
        "subagent": true,
        "description": "Fast local lookups: man pages, syntax checks, single-file reads. Use for trivial tasks where Claude/GPT would be overkill.",
        "tools": { "allow": ["read_file", "web_search"] }
      }
    ]
  },
  "bindings": [
    { "agentId": "lead", "match": { "channel": "cli" } }
    // The other agents are reachable via the web chat agent selector
    // and via `task` delegation from the lead.
  ],
  "channels": { "cli": { "enabled": true } }
}
```

The lead agent uses `task` to delegate coding to the coder and review to
the reviewer; `todo_write` lets it externalise its plan so the user can
see what's queued. Each delegated agent runs with its own model + tool
policy + isolated session.

### Locked-down read-only agent (safe for shared use)

```json5
{
  "providers": {
    "anthropic": { "kind": "anthropic", "api_key": "sk-ant-..." }
  },
  "agents": {
    "list": [
      {
        "id": "safe",
        "name": "Read-Only Helper",
        "model": "anthropic/claude-sonnet-4-5-20250514",
        "tools": { "allow": ["read_file"] }
      }
    ]
  },
  "bindings": [
    { "agentId": "safe", "match": { "channel": "cli" } }
  ],
  "channels": { "cli": { "enabled": true } },
  "security": {
    "execApprovals": { "level": "deny" }
  },
  "gateway": {
    "auth": { "token": "my-secret-token" }
  }
}
```

### Ollama-only (fully offline, no API keys)

```json5
{
  "providers": {
    "ollama": {
      "kind": "openai-compatible",
      "base_url": "http://localhost:11434/v1"
    }
  },
  "agents": {
    "list": [
      {
        "id": "local",
        "name": "Local Assistant",
        "model": "ollama/llama3",
        "tools": { "allow": ["read_file", "write_file", "bash"] }
      }
    ]
  },
  "bindings": [
    { "agentId": "local", "match": { "channel": "cli" } }
  ],
  "channels": {
    "cli": { "enabled": true }
  }
}
```

---

## Data Directory

All Felix state lives in `~/.felix/` (on Windows: `C:\Users\<you>\.felix\`):

```
~/.felix/
  felix.json5            # Configuration file
  felix-app.log          # Tray app log file (macOS & Windows)
  sessions/              # Conversation history (JSONL files)
  memory/entries/        # Memory entries (Markdown files)
  skills/                # Shared skills (SKILL.md files; bundled
                         # starter skills seeded on first run)
  brain.db               # Cortex knowledge graph (SQLite, when enabled)
  ollama/                # Bundled Ollama model store (when enabled)
  workspace-default/     # Default agent workspace
    IDENTITY.md          # Agent identity/personality
    skills/              # Agent-specific skills
    .felix/todos.json    # Per-workspace todo list (todo_write tool)
```

No external database is required. Everything is files on disk.

## Using the bundled local model

Felix ships with a bundled Ollama runtime so you can run agents fully
offline. If you launch Felix with no `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`,
or `GEMINI_API_KEY` set, the onboarding wizard offers two curated local
models:

| Choice | Size | Best for |
|---|---|---|
| Qwen 3.5 9B | ~5.0 GB | General agent tasks (recommended default) |
| Gemma 4 (multimodal) | ~9.6 GB | Tasks involving images |

After picking a model, Felix downloads it via the bundled Ollama. The
download runs once; subsequent starts reuse the cached weights in
`~/.felix/ollama/models/`.

### Switching between local and cloud

The agent's model is set in `~/.felix/felix.json5` under
`agents.list[*].model`. Use the `local/` prefix for the bundled runtime,
or any of the cloud prefixes:

```json5
{
  "agents": {
    "list": [
      { "id": "default", "model": "local/qwen3.5:9b" },
      { "id": "online",  "model": "openai/gpt-5.4" }
    ]
  }
}
```

### Adding more local models

Any model in the Ollama registry can be pulled into Felix's bundled
runtime:

```bash
felix model pull qwen2.5:7b-instruct     # add another model
felix model list                         # see what's installed
felix model status                       # check the supervisor
felix model rm gemma4:latest             # free disk space
```

### Coexistence with system Ollama

Felix's bundled Ollama runs on `127.0.0.1:18790` (or the next free port
in the range `:18790–:18799`) — it does **not** touch a system Ollama on
the default `:11434`. If you already use Ollama for other tools, both
keep running side by side.
