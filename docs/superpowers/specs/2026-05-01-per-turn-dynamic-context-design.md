# Per-Turn Dynamic Context — Date + FELIX.md/AGENTS.md Walk-up

**Status:** Draft
**Date:** 2026-05-01
**Scope:** `internal/agent/`
**Builds on:** Caching foundation (`docs/superpowers/specs/2026-04-30-caching-foundation-design.md`, merged `2e197ac`).
**Part of:** Context-management improvements series (sub-project #2 of 6, derived from harness.md gap analysis on 2026-04-30).

## Context

Felix today injects skills, memory, and the cortex hint into the per-turn dynamic suffix, plus identity, agent metadata, config summary, and skills index into the cached static prefix. What's missing relative to Claude Code (per `harness.md` §5):

1. **The agent doesn't know what day it is.** Time-aware reasoning ("send this on Friday", "respond by EOD") fails because the model has no anchor for "today".
2. **No project-level instructions.** Claude Code reads `CLAUDE.md` (and `AGENTS.md`) walked from cwd up to the repo root and home, giving the agent project-specific context. Felix has IDENTITY.md per agent but nothing user-authored that lives alongside the project the agent is operating on.

This sub-project closes both gaps. The original gap-analysis item also included git status, but Felix is intentionally git-agnostic — Felix workspaces are arbitrary directories, not repos — so git status is dropped from scope.

## Goals

- Agent has access to "today's date" in every Run, formatted as a deterministic single-line ISO date (`Today's date is 2026-05-01.`).
- Date is computed per Run (once at the start of `Run()`), kept stable across all turns of that Run for trace correctness.
- Agent has access to project-level instructions via `FELIX.md` and/or `AGENTS.md` placed in the workspace (highest priority) or in `$HOME` (lowest priority).
- Both filenames are recognized at each location. Files are concatenated in priority order with brief section headers identifying their source path.
- FELIX.md / AGENTS.md content lives in the cached static prefix (read once at Runtime construction, same lifecycle as IDENTITY.md). Adds no per-turn cost.
- Total memory-file content capped at 40 KB to bound prefill growth.
- Date lives in the per-turn dynamic suffix so it doesn't invalidate the cache prefix when the date changes (e.g. a long session crosses midnight).

## Non-Goals

- **Git status injection.** Felix is git-agnostic by design.
- **`@import` directive expansion.** Memory files are read verbatim. Future improvement, not in scope.
- **YAML frontmatter parsing.** Files are treated as flat markdown. Frontmatter (if present) appears verbatim in the prompt.
- **Walking parent directories above the workspace.** Discovery is exactly two locations: `r.Workspace` and `$HOME`. Mid-tree project structures (sub-project FELIX.md inside a parent project FELIX.md) are not supported.
- **Hot-reload of FELIX.md mid-session.** Files are read once at `BuildRuntimeForAgent` time; changes pick up on next chat session. Matches the existing IDENTITY.md behavior.
- **File-format validation.** Empty files, missing files, and read errors all silently degrade to "no content" — never error to the user.

## Design

### 1. `LoadAgentMemoryFiles(workspace string) string`

`internal/agent/context.go` adds a new helper:

```go
// MaxAgentMemoryBytes caps the total bytes of FELIX.md / AGENTS.md content
// injected into the static system prompt. Mirrors Claude Code's
// MAX_MEMORY_CHARACTER_COUNT (claudemd.ts) at 40 KB.
const MaxAgentMemoryBytes = 40 * 1024

// LoadAgentMemoryFiles reads FELIX.md and AGENTS.md from workspace and
// from $HOME. Returns the concatenated content with a brief header per
// source, or "" if nothing is found.
//
// Discovery order (highest priority first):
//   1. <workspace>/FELIX.md
//   2. <workspace>/AGENTS.md
//   3. $HOME/FELIX.md
//   4. $HOME/AGENTS.md
//
// Empty files, whitespace-only files, missing files, and unreadable files
// are silently skipped. Files at the same absolute path (e.g., when
// workspace == $HOME) are deduped — each unique file appears once.
//
// Hard cap: total returned content ≤ MaxAgentMemoryBytes (40 KB). When
// adding a file would push past the cap, the file's content is truncated
// at the last newline before the byte limit and a "[truncated — over
// 40 KB total agent memory]" marker is appended. Subsequent files are
// skipped entirely.
//
// Pure (single I/O exception: reads up to 4 files from disk). Suitable
// to call once at Runtime construction.
func LoadAgentMemoryFiles(workspace string) string
```

Output format per file (only emitted when the file has non-empty content after trim). The returned string starts with `\n\n` so it sits cleanly after `skillsIndex` in the static prefix (mirrors how `Skills.FormatIndex()` returns its own leading newlines):

```
\n\n## Project memory: <abs path>\n\n<file content verbatim>\n\n## Project memory: <next path>\n\n<...>
```

`Project memory:` for workspace files; `User memory:` for `$HOME` files. The header includes the absolute path so the agent can reference the source. When no files are found, `LoadAgentMemoryFiles` returns `""` (no leading whitespace).

**Edge cases:**
- Empty `workspace` arg → only `$HOME` files are checked.
- Empty `$HOME` env var → only workspace files are checked. Don't construct `/FELIX.md` from an empty home var.
- Workspace == $HOME → don't read the same files twice (dedupe by absolute path).
- File exists but is empty or whitespace-only → skip the section header too (no empty section).
- File push-over-cap → truncate at last newline before the limit, append marker, set "skip remaining" flag.

### 2. `FormatDateLine(now time.Time) string`

`internal/agent/context.go` adds the date formatter:

```go
// FormatDateLine returns the canonical date line injected into the
// dynamic system suffix every Run. Single-line, deterministic format.
//
//   "Today's date is YYYY-MM-DD."
//
// Uses the caller's process timezone (no UTC normalization) so "today"
// matches the user's local sense of the day.
func FormatDateLine(now time.Time) string {
    return fmt.Sprintf("Today's date is %s.", now.Format("2006-01-02"))
}
```

Tests inject `time.Time` values directly; the runtime always passes `time.Now()`.

### 3. `BuildStaticSystemPrompt` extension

The existing signature gains a new `memoryFiles string` parameter:

```go
func BuildStaticSystemPrompt(
    workspace, systemPrompt, agentID, agentName string,
    toolNames []string,
    configSummary string,
    skillsIndex string,
    memoryFiles string, // NEW — output of LoadAgentMemoryFiles
) string
```

Append order in the output (no separator between `skillsIndex` and `memoryFiles` — both helpers emit their own leading newlines):

```
[identity / IDENTITY.md / built-in default with tool hints]

You are the "<Name>" agent (id: <id>).

Your configuration file is at <path> and your data directory is <path>.

[configSummary, when non-empty]

[skillsIndex, when non-empty]

[memoryFiles, when non-empty]
```

### 4. `buildDynamicSystemPromptSuffix` extension

The existing signature gains a new `dateLine string` parameter:

```go
func buildDynamicSystemPromptSuffix(
    dateLine string,                      // NEW
    matchedSkills []skill.Skill,
    matchedMemory []memory.Entry,
    cortexContext string,
) string
```

`dateLine` is prepended to the suffix output (it's the most stable of the dynamic pieces). Format:

```
<dateLine>

<matched skills format>

<matched memory format>

<cortex hint>
```

When `dateLine == ""` no separator is emitted; the function falls back to current behavior. (Tests calling `buildDynamicSystemPromptSuffix(nil, nil, nil, "")` still get `""`.)

### 5. `BuildRuntimeForAgent` wiring

`internal/agent/builder.go`'s `BuildRuntimeForAgent` calls `LoadAgentMemoryFiles(a.Workspace)` once and passes the result through to `BuildStaticSystemPrompt`:

```go
configSummary := BuildConfigSummary(deps.Config)
skillsIndex := ""
if deps.Skills != nil {
    skillsIndex = deps.Skills.FormatIndex()
}
memoryFiles := LoadAgentMemoryFiles(a.Workspace) // NEW
var toolNames []string
if inputs.Tools != nil {
    toolNames = inputs.Tools.Names()
}
staticPrompt := BuildStaticSystemPrompt(
    a.Workspace, a.SystemPrompt, a.ID, a.Name,
    toolNames, configSummary, skillsIndex,
    memoryFiles, // NEW
)
```

### 6. Per-turn loop wiring

`internal/agent/runtime.go` computes the date once at the top of `Run()`, before the per-turn loop:

```go
dateLine := FormatDateLine(time.Now())
// ... existing setup (cortex recall, skill/memory caches) ...
for turn := 0; turn < maxTurns; turn++ {
    // ...
    dynamicSuffix := buildDynamicSystemPromptSuffix(
        dateLine, // NEW
        matchedSkills,
        matchedMemory,
        cortexContext,
    )
    // ...
}
```

The date is stable across all turns of a single Run (so trace replay and the existing `TestRuntimeStaticPromptByteStableAcrossTurns` and friends stay deterministic) but refreshes on the next user message.

## Testing

### Unit tests (`internal/agent/context_test.go`)

**`LoadAgentMemoryFiles` coverage:**
- `TestLoadAgentMemoryFilesEmpty` — `t.TempDir()` workspace + isolated empty `$HOME` (`t.Setenv("HOME", t.TempDir())`) → returns `""`.
- `TestLoadAgentMemoryFilesWorkspaceOnly` — write workspace `FELIX.md`; assert section header + content present.
- `TestLoadAgentMemoryFilesBothLocations` — write workspace `FELIX.md` and home `AGENTS.md`; assert both sections present, workspace first.
- `TestLoadAgentMemoryFilesAllFour` — all four files written; assert order = workspace FELIX → workspace AGENTS → home FELIX → home AGENTS.
- `TestLoadAgentMemoryFilesSkipsEmptyFile` — write a 0-byte `FELIX.md`; assert no section header for it.
- `TestLoadAgentMemoryFilesSkipsWhitespaceOnly` — write a `FELIX.md` containing only `"\n  \t\n"`; assert no section.
- `TestLoadAgentMemoryFilesTruncatesOverCap` — write a 50 KB workspace `FELIX.md`; assert output ≤ 40 KB and contains the truncation marker.
- `TestLoadAgentMemoryFilesSkipsAfterTruncation` — write a 39 KB workspace `FELIX.md` and a 5 KB workspace `AGENTS.md`; assert AGENTS.md is fully skipped (not partially included).
- `TestLoadAgentMemoryFilesDedupSameDir` — set `$HOME` to the same path as `workspace`; assert each file appears exactly once.
- `TestLoadAgentMemoryFilesEmptyHome` — `t.Setenv("HOME", "")`; assert workspace files still load and no panic from constructing paths against empty `$HOME`.

**`FormatDateLine` + extensions:**
- `TestFormatDateLine` — feed a fixed `time.Time` (e.g., `time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)`); assert exact output `"Today's date is 2026-05-01."`.
- `TestBuildStaticSystemPromptIncludesMemoryFiles` — extend the existing `TestBuildStaticSystemPrompt*` family with one more case proving the `memoryFiles` arg appears in the output.
- `TestBuildDynamicSystemPromptSuffixIncludesDate` — extend the existing dynamic-suffix tests with one case proving the date line appears at the top of the suffix.

### Integration tests (`internal/agent/cache_stability_test.go`)

- `TestRuntimeStaticPromptIncludesMemoryFilesContent` — write a workspace `FELIX.md` containing a unique sentinel string. Build a Runtime via `BuildRuntimeForAgent`. Run one turn and assert `req.SystemPromptParts[0].Text` contains the sentinel.
- `TestRuntimeDynamicSuffixIncludesDate` — run one turn and assert `req.SystemPromptParts[1].Text` (the dynamic part) contains `"Today's date is "`.
- `TestRuntimeDateLineByteStableAcrossTurnsWithinRun` — run a Run that takes multiple turns (force a tool call to trigger turn 2). Assert the dynamic suffix's date-line portion is byte-identical across both turns of the same Run (proves we computed once per Run, not per turn).

### Builder test (`internal/agent/builder_test.go`)

- `TestBuildRuntimeForAgentLoadsMemoryFilesIntoStaticPrompt` — write a workspace `FELIX.md`. Build a Runtime via `BuildRuntimeForAgent`. Assert `rt.StaticSystemPrompt` contains the file's content.

### Manual verification

None required. The behavior change is internal (additional content in the prompt). User-visible verification is anecdotal — start a chat, ask "what day is it?" or "what does FELIX.md say?", confirm sensible answers.

## Risks & mitigations

| Risk | Mitigation |
|------|------------|
| FELIX.md content invalidates the static-prompt cache mid-session if a user edits it | Out of scope (matches IDENTITY.md behavior). Document in spec. Hot-reload is a separate sub-project if anyone asks. |
| 40 KB cap not enough for a real-world FELIX.md | Same cap as Claude Code. Truncation marker tells the agent content was elided. If feedback shows this is too tight, raise the cap; not a structural concern. |
| `$HOME` files leak into agents the user didn't intend (e.g., a shared machine) | The discovery is opt-in: files only load if a user explicitly created them. If they exist, they're load-bearing. Document the precedence (workspace > home) clearly so users know how to override per-project. |
| Date timezone confusion (process tz vs server tz) | Use process timezone (`time.Now()` without normalization). The user's "today" should match what their wall clock says. |
| `LoadAgentMemoryFiles` adds I/O to Runtime construction time | At most 4 file stats + 4 reads, all from local FS. Sub-millisecond cost in the critical-path-after-startup. Skipped entirely when no files exist (the common case). |
| Empty $HOME causes `path.Join("", "FELIX.md")` → "FELIX.md" relative path that resolves to cwd | Explicit guard in `LoadAgentMemoryFiles`: skip the home leg entirely when `os.Getenv("HOME") == ""`. |

## File touch list

- `internal/agent/context.go` — add `LoadAgentMemoryFiles`, `FormatDateLine`, `MaxAgentMemoryBytes` constant; extend `BuildStaticSystemPrompt` with `memoryFiles` parameter; extend `buildDynamicSystemPromptSuffix` with `dateLine` parameter.
- `internal/agent/context_test.go` — new tests for both helpers; extensions for the existing `BuildStaticSystemPrompt*` and `BuildDynamicSystemPromptSuffix*` test families.
- `internal/agent/builder.go` — call `LoadAgentMemoryFiles(a.Workspace)` and pass through to `BuildStaticSystemPrompt`.
- `internal/agent/builder_test.go` — new test asserting memory files land in `Runtime.StaticSystemPrompt`.
- `internal/agent/runtime.go` — compute `dateLine := FormatDateLine(time.Now())` once at the top of `Run()`; pass into `buildDynamicSystemPromptSuffix` on every turn.
- `internal/agent/cache_stability_test.go` — new tests for end-to-end memory-file inclusion, date inclusion, and date stability across turns within a Run.
