# Cloudcat UI Merge Design

**Date:** 2026-05-23  
**Scope:** UI-first merge of cloudcat's sidebar shell, files panel, and file attachment into Felix. Backend-heavy features (runs tracking, session metadata) deferred.

---

## Goal

Reduce Felix's menubar to two items (Open Chat, Quit) and bring cloudcat's richer chat UI into Felix: collapsible sidebar with in-app navigation, iframe-embedded Settings/Jobs/Logs, a file-explorer panel, and file attachment in the chat input.

---

## Commit Plan

Two commits in order:

1. **Menubar simplification** — self-contained, low-risk, immediately useful.
2. **Chat UI + files backend** — the bulk of the work; one coherent PR covering the new `chat.go`, `files.go`, and `files_page.go`.

---

## Commit 1: Menubar Simplification

**File:** `cmd/felix-app/main.go`

Remove from `onReady()`:
- `mJobs` — Jobs menu item and its click handler
- `mLogs` — Logs menu item and its click handler
- `mSettings` — Settings menu item and its click handler
- `mRestart` — Restart menu item and its click handler

Rename "Chat" → "Open Chat".

Result: two items remain — `Open Chat` and `Quit`, separated by the existing `systray.AddSeparator()`.

**Rationale:** Settings/Jobs/Logs are now reachable from inside the chat sidebar via iframe embed. Restart is an operational escape hatch that belongs in the logs or a future diagnostics panel, not the top-level menubar.

---

## Commit 2: Chat UI + Files Backend

### 2a. `internal/gateway/chat.go` — wholesale replacement

Source: cloudcat's `internal/gateway/chat.go` (4,530 lines), adapted for Felix.

**Adaptations (strip/replace):**

| Cloudcat element | Felix treatment |
|---|---|
| OAuth user menu (avatar, sign-in, sign-out) | Removed entirely — the `#user-row` becomes theme-toggle only |
| CloudCat wordmark + cat logo | Replaced with "Felix" text; logo `<img>` removed |
| `/auth/login`, `/auth/logout`, `/auth/me` fetch calls | Removed |
| `cloudcat-theme` localStorage key | Renamed to `felix-theme` |
| `/check` health endpoint references | Removed |
| `workspacemcp` OAuth callback references | Removed |
| `/admin/restart`, `/admin/recreate` menu items | Removed |
| `Reply to Cloudcat...` placeholder | Changed to `Message Felix...` |

**Structure retained from cloudcat (no changes):**

- Collapsible sidebar (64px icon rail when collapsed; full width when expanded)
- Session list with filter input in sidebar
- Agent selector in sidebar
- Sidebar footer navigation: Files, Settings, Jobs, Logs
- Theme toggle in sidebar footer
- Topbar: Tools toggle, Trace toggle, status dot, token chip
- Embed view: Settings/Jobs/Logs load inside `<iframe id="embed-frame">` replacing the chat view
- Files panel: `<aside id="files-panel">` overlay from right edge of main column
- File attachment: paperclip button, drag-and-drop into chat, clipboard paste; images/PDFs/text sent as content blocks in `chat.send`
- Light/dark theme: warm cream + forest green (`oklch` colour tokens), coherent across chat and files pages
- Mobile-responsive: sidebar slides off-screen on narrow viewports; hamburger button reveals it

**CSS colour token rename:** `cloudcat-theme` → `felix-theme` (localStorage key only; CSS variable names unchanged).

### 2b. `internal/gateway/files.go` — new file

Ported from cloudcat verbatim; only the import path changes (`cloudcat` → `felix`).

`FilesHandlers` struct with `cfgFunc func() *config.Config` closure and `diskCheck` hook.

Endpoints:

| Method | Path | Action |
|--------|------|--------|
| GET | `/files/list` | Directory listing; sorted dirs-first |
| GET | `/files/raw` | Raw file download / inline view |
| POST | `/files/upload` | Multipart upload; 100 MiB cap |
| DELETE | `/files` | Delete file or recursive directory |
| POST | `/files/move` | Move across paths |
| POST | `/files/rename` | Rename within directory |
| POST | `/files/mkdir` | Create directory |

All paths clamped to agent workspace via `resolveAgentPath`. No new dependencies.

### 2c. `internal/gateway/files_page.go` — new file

Ported from cloudcat verbatim (import path only).

`NewFilesPageHandler()` serves the self-contained file-explorer HTML: toolbar (refresh, new folder, upload, download, rename, move, delete), breadcrumb navigation, inline filter, multi-select with shift-click and Cmd/Ctrl-A, modal dialogs for prompt/confirm. Inherits theme from `felix-theme` localStorage key.

### 2d. `internal/gateway/server.go` — update

Add `Files *FilesHandlers` to `ServerOptions`. Wire 8 routes under the authenticated group:

```go
r.Get("/files", NewFilesPageHandler())
r.Get("/files/list",    s.opts.Files.List)
r.Get("/files/raw",     s.opts.Files.Raw)
r.Post("/files/upload", s.opts.Files.Upload)
r.Delete("/files",      s.opts.Files.Delete)
r.Post("/files/move",   s.opts.Files.Move)
r.Post("/files/rename", s.opts.Files.Rename)
r.Post("/files/mkdir",  s.opts.Files.MkDir)
```

### 2e. `internal/startup/startup.go` — update

Instantiate `FilesHandlers` and pass into `ServerOptions`:

```go
filesHandlers := gateway.NewFilesHandlers(func() *config.Config { return cfg })
```

One call; the config closure pattern already exists in startup for other handlers.

---

## What Is Deferred

| Feature | Reason |
|---|---|
| Session metadata / titles | Requires `.meta.json` sidecar files + WebSocket `session.rename` method |
| Runs tracking (`runs/` package) | Backend-heavy; significant new surface |
| File attachment content-block support | Needs verification that Felix's harness `chat.send` already handles multi-content messages; if not, small WS handler change needed |

File attachment is included in the chat UI HTML/JS (the drag-and-drop, paperclip, paste logic). Whether the server-side WS handler correctly passes content blocks through to the LLM is a verification step during implementation — if not, it's a small targeted fix in `internal/gateway/websocket.go`.

---

## Files Changed

| File | Change |
|---|---|
| `cmd/felix-app/main.go` | Remove 4 menu items + handlers (~25 lines deleted) |
| `internal/gateway/chat.go` | Wholesale replacement (~4,100 lines) |
| `internal/gateway/files.go` | New (~230 lines) |
| `internal/gateway/files_page.go` | New (~710 lines) |
| `internal/gateway/server.go` | Add FilesHandlers to ServerOptions + 8 routes (~20 lines) |
| `internal/startup/startup.go` | Instantiate FilesHandlers (~5 lines) |

No new Go module dependencies.
