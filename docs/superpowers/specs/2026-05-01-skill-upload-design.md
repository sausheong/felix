# Skill Upload via Settings Page — Design

**Date:** 2026-05-01
**Status:** Approved (pending implementation plan)

## Goal

Let users add, view, and remove skill markdown files through Felix's existing Settings page. Newly added or removed skills become active without restarting the process.

## Non-Goals

- In-browser editing of skill contents.
- Filesystem watcher (`fsnotify`) on the skills directory. API-triggered reload only.
- Managing skills inside per-agent workspace directories. Only `~/.felix/skills/` is mutable through the UI.
- Per-skill enable/disable toggle. Delete is the only "off" switch.
- Multi-user concerns (locking, audit log, fine-grained permissions). Felix is single-user behind a bearer token.

## Architecture

A new file `internal/gateway/skills.go` exposes a `SkillHandlers` struct containing four `http.HandlerFunc`s, mounted by `internal/gateway/server.go` alongside the existing `/settings/api/*` routes. The Settings page (embedded HTML in `internal/gateway/settings.go`) gains a "Skills" tab that talks to those endpoints via `fetch`.

After every write or delete, the handler calls `loader.LoadFrom(reloadDirs...)` on the same `*skill.Loader` instance that was created at startup. Because every runtime caller (`wsHandler`, heartbeat, cron, subagents) holds that loader by pointer, the in-place refresh is observed everywhere on the next chat turn — no rewiring required.

```
                                    ┌──────────────────────────┐
  Browser (Settings page)           │  internal/gateway/       │
  ─────────────────────────────────►│    server.go (router)    │
   GET    /settings/api/skills      │                          │
   GET    /settings/api/skills/{n}  │  ┌────────────────────┐  │
   POST   /settings/api/skills      └─►│ skills.go          │  │
   DELETE /settings/api/skills/{n}     │  SkillHandlers     │  │
                                       └─────────┬──────────┘  │
                                                 │             │
                                                 ▼             │
                                       ┌────────────────────┐  │
                                       │ ~/.felix/skills/   │  │
                                       │   *.md             │  │
                                       └─────────┬──────────┘  │
                                                 │             │
                                                 ▼             │
                                       ┌────────────────────┐  │
                                       │ *skill.Loader      │  │
                                       │   .LoadFrom(...)   │  │
                                       └─────────┬──────────┘  │
                                                 │             │
                shared pointer ──────────────────┴────────►    │
                  wsHandler, heartbeat, cron, subagents        │
                                                               │
                                                               ▼
                                                       (next chat turn
                                                        sees new skills)
```

## Components

### `SkillHandlers` (new, `internal/gateway/skills.go`)

```go
type SkillHandlers struct {
    List   http.HandlerFunc  // GET    /settings/api/skills
    Get    http.HandlerFunc  // GET    /settings/api/skills/{name}
    Upload http.HandlerFunc  // POST   /settings/api/skills
    Delete http.HandlerFunc  // DELETE /settings/api/skills/{name}
}

// skillReloader matches *skill.Loader's LoadFrom; an interface so tests
// can inject a fake that fails reload.
type skillReloader interface {
    LoadFrom(dirs ...string) error
}

func NewSkillHandlers(loader skillReloader, skillsDir string, reloadDirs []string) *SkillHandlers
```

- `loader` — the `*skill.Loader` constructed at startup (`internal/startup/startup.go:440`).
- `skillsDir` — `~/.felix/skills/`. The only directory writes/deletes touch.
- `reloadDirs` — the full list passed to `LoadFrom` initially (`skillsDir` + every agent workspace's `skills/`). Reusing the same list ensures workspace skills aren't accidentally dropped on refresh.

### `gateway.Options` (modified, `internal/gateway/server.go`)

Add `Skills *SkillHandlers` field. `server.go` mounts:

```go
if s.opts.Skills != nil {
    s.router.Get("/settings/api/skills", s.opts.Skills.List)
    s.router.Get("/settings/api/skills/{name}", s.opts.Skills.Get)
    s.router.Post("/settings/api/skills", s.opts.Skills.Upload)
    s.router.Delete("/settings/api/skills/{name}", s.opts.Skills.Delete)
}
```

These inherit the global bearer-auth middleware applied at `server.go:49`.

### Bootstrap wiring (`internal/startup/startup.go` and `cmd/felix/main.go`)

After `skillLoader.LoadFrom(skillDirs...)`, build the handlers and pass them to `gateway.Options`:

```go
skillHandlers := gateway.NewSkillHandlers(skillLoader, filepath.Join(dataDir, "skills"), skillDirs)
// later, when constructing gateway.Options:
opts.Skills = skillHandlers
```

### Settings UI (`internal/gateway/settings.go`)

Add a "Skills" tab to the existing tabbed settings page. The tab contains:

- A list rendered from `GET /settings/api/skills`. Each row shows name, description (one line, ellipsised), tags, file size, and modification date.
- A muted/warning style for rows where the response sets `unavailable: true` (missing required binary) or `parse_error`.
- An "Upload .md" button using `<input type="file" accept=".md">`.
- A "View" action per row that opens a side panel containing the raw file in a `<pre>` block (fetched from `GET /settings/api/skills/{name}`).
- A "Delete" action per row that confirms and then issues `DELETE`.

Re-fetch the list after every successful upload/delete. Show toasts using whatever pattern the existing settings page uses for save success/failure.

## Data Flow

### List — `GET /settings/api/skills`

1. Read directory entries from `skillsDir`.
2. For each `*.md` file (top-level only — no recursion): call `parseSkillFile` from `internal/skill/skill.go`.
3. For each parsed skill, check `metadata.openclaw.requires.bins`; if any are missing on `$PATH`, mark `unavailable: true`.
4. If frontmatter parse failed, include the file with `parse_error` set and an empty description.
5. Return:

```json
{
  "skills": [
    {
      "name": "cortex",
      "filename": "cortex.md",
      "description": "Knowledge-graph recall and ingest",
      "tags": ["memory"],
      "size_bytes": 1234,
      "modified": "2026-05-01T11:30:00Z",
      "unavailable": false,
      "missing_bins": [],
      "parse_error": ""
    }
  ]
}
```

Source-of-truth is the filesystem, not `loader.Skills()`, so unavailable/malformed files remain visible.

### Get one — `GET /settings/api/skills/{name}`

1. Sanitize: `name = filepath.Base(chi.URLParam(r, "name"))`, then validate against `^[A-Za-z0-9._-]+\.md$`.
2. Read `filepath.Join(skillsDir, name)`.
3. Return raw bytes as `text/plain; charset=utf-8`.
4. 404 if missing, 400 if regex fails.

### Upload — `POST /settings/api/skills`

1. Wrap the request body with `http.MaxBytesReader(w, r.Body, 256*1024)`.
2. `r.ParseMultipartForm(256 * 1024)`. Expect a single `file` field.
3. Determine target filename:
   - If the multipart part has a non-empty filename, use `filepath.Base(part.FileName())`.
   - Validate against `^[A-Za-z0-9._-]+\.md$`. 400 on failure.
4. Read the part contents into memory.
5. Validate frontmatter:
   - Run `splitFrontmatter` (currently unexported in `skill.go` — promote to exported `SplitFrontmatter` or duplicate the small helper).
   - If frontmatter is non-empty, run `yaml.Unmarshal` into a `Skill` struct. 422 on YAML error.
   - Body-only files (no frontmatter) are valid.
6. Check for existing target file. If present, return 409 with the existing filename.
7. Write atomically: `os.WriteFile(target+".tmp", data, 0o644)` then `os.Rename(target+".tmp", target)`.
8. Call `loader.LoadFrom(reloadDirs...)`. On error, log and return `200 { "ok": true, "warning": "reload failed: ..." }`.
9. On reload success, return `200 { "ok": true, "name": "...", "filename": "..." }`.

### Delete — `DELETE /settings/api/skills/{name}`

1. Sanitize `{name}` as in Get.
2. `os.Remove(filepath.Join(skillsDir, name))`. 404 if missing.
3. Call `loader.LoadFrom(reloadDirs...)`. Same warning behavior as upload.
4. Return `{ "ok": true }`.

### UI flow (Skills tab)

- Tab activates → `fetch GET /settings/api/skills` → render rows.
- Click row "View" → `fetch GET /settings/api/skills/{name}` → side panel with raw `<pre>`.
- Click "Upload .md" → file picker → `fetch POST` with `FormData` → on success, re-fetch list and toast.
- Click "Delete" → confirm dialog → `fetch DELETE` → on success, re-fetch list and close panel if it showed that skill.

## Error Handling

All error responses use `application/json` with `{ "error": "..." }`.

| Condition | Status |
|---|---|
| Filename fails regex / has path separators | 400 |
| Multipart parse fails / no `file` field | 400 |
| Body exceeds 256KB | 413 |
| Frontmatter present but YAML invalid | 422 (include yaml error verbatim — the user owns the file) |
| Target file already exists (upload) | 409 |
| Target file missing (get/delete) | 404 |
| Filesystem write/rename/remove fails | 500 (log via `slog`, return generic message) |
| `LoadFrom` reload fails after successful write | 200 with `warning` field (the file is on disk and will be picked up on next process start) |

**List endpoint never fails on individual files.** A malformed file appears with `parse_error` set, not omitted. A file requiring a missing binary appears with `unavailable: true` and `missing_bins: [...]`.

**Path safety:** `filepath.Base` plus the regex is the single defense. We never follow user-supplied paths — only the validated basename joined to the fixed `skillsDir`. No symlink concern since we use `os.WriteFile` / `os.Remove` against the joined path.

**Concurrency:** Two simultaneous uploads of the same name — atomic rename makes the last writer win at the OS level; the pre-write existence check catches the common case. The 409 race window is acceptable for a single-user gateway.

**Auth:** Inherited from the global bearer middleware (`server.go:49`). No per-handler auth code.

## Testing

Unit tests in `internal/gateway/skills_test.go` build handlers against a `t.TempDir()` skillsDir and a fresh `skill.NewLoader()`, then drive them via `httptest.NewRecorder` — no live HTTP server.

| Test | Asserts |
|---|---|
| `TestList_Empty` | 200, `{"skills": []}` |
| `TestList_ParsesFrontmatter` | Two seeded files appear with name/description/tags |
| `TestList_MalformedFrontmatter` | Bad file appears with `parse_error` set, response still 200 |
| `TestList_MissingBins` | File requiring `nonexistent-binary-xyz` is flagged `unavailable` |
| `TestGet_Found` | Returns raw bytes, `Content-Type: text/plain` |
| `TestGet_NotFound` | 404 |
| `TestGet_PathTraversal` | `../etc/passwd`, `foo/bar.md` → 400 |
| `TestUpload_Happy` | Multipart write lands on disk, loader reflects it after, response `ok:true` |
| `TestUpload_BadFilename` | `foo.txt`, `../foo.md`, `foo bar.md` → 400 |
| `TestUpload_TooLarge` | 257KB → 413 |
| `TestUpload_BadYAML` | `---\nname: [unclosed\n---\nbody` → 422 |
| `TestUpload_AlreadyExists` | Second upload of same name → 409 |
| `TestUpload_ReloadFailure` | Loader fake returns error from `LoadFrom` → 200 with `warning` |
| `TestDelete_Happy` | File gone from disk and loader |
| `TestDelete_NotFound` | 404 |
| `TestDelete_PathTraversal` | 400 |

The reload-failure test depends on the `skillReloader` interface — `*skill.Loader` already satisfies it; a fake type implements it for the test.

UI testing is manual: build felix, start it, open `http://127.0.0.1:18789/settings`, exercise upload / list / view / delete.

Out of scope: load tests, fuzzing, browser automation. The endpoint is local-only behind bearer auth on a single-user gateway.

## Open Implementation Notes

- `splitFrontmatter` in `internal/skill/skill.go` is currently unexported. Either export it (`SplitFrontmatter`) or extract a tiny helper used by both the loader and the upload handler. Don't duplicate the parser.
- `parseSkillFile` is also unexported; the list endpoint will need either an exported wrapper or the same small refactor. Both functions already have the right shape — only visibility changes.
- Prefer `chi.URLParam(r, "name")` for path extraction; the chi router is already in use throughout `server.go`.
