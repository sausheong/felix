# Per-Turn Dynamic Context Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add today's date and FELIX.md/AGENTS.md walk-up content to Felix's prompt without invalidating the cached static prefix.

**Architecture:** FELIX.md and AGENTS.md from the workspace and `$HOME` are read once at Runtime construction (`BuildRuntimeForAgent` time) and appended into the cached `StaticSystemPrompt`. The current date is computed once at the start of each `Run()` and prepended into the per-turn dynamic suffix (uncached). Both insertion points reuse the just-built sub-project #1 architecture without changes to the LLM contract.

**Tech Stack:** Go, `testify`, stdlib (`os`, `path/filepath`, `time`, `fmt`, `strings`).

**Spec:** `docs/superpowers/specs/2026-05-01-per-turn-dynamic-context-design.md`.

---

## Task 1: Add `LoadAgentMemoryFiles` + `MaxAgentMemoryBytes` constant

**Why first:** pure function with comprehensive unit tests, no signature changes elsewhere. Foundation for Task 5's wiring.

**Files:**
- Modify: `internal/agent/context.go` (append helper + constant)
- Test: `internal/agent/context_test.go` (extend existing file)

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/context_test.go`. The tests use `t.TempDir()` and `t.Setenv("HOME", ...)` to isolate filesystem state.

```go
import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgentMemoryFilesEmpty(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	got := LoadAgentMemoryFiles(workspace)
	require.Equal(t, "", got)
}

func TestLoadAgentMemoryFilesWorkspaceOnly(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "FELIX.md"),
		[]byte("PROJECT_INSTRUCTIONS_SENTINEL"),
		0o644,
	))
	got := LoadAgentMemoryFiles(workspace)
	require.Contains(t, got, "## Project memory: ")
	require.Contains(t, got, "FELIX.md")
	require.Contains(t, got, "PROJECT_INSTRUCTIONS_SENTINEL")
	require.True(t, strings.HasPrefix(got, "\n\n"),
		"output must start with \\n\\n so it composes after skillsIndex")
}

func TestLoadAgentMemoryFilesBothLocations(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"),
		[]byte("WORKSPACE_FELIX_CONTENT"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, "AGENTS.md"),
		[]byte("HOME_AGENTS_CONTENT"), 0o644))
	got := LoadAgentMemoryFiles(workspace)
	require.Contains(t, got, "## Project memory: ")
	require.Contains(t, got, "WORKSPACE_FELIX_CONTENT")
	require.Contains(t, got, "## User memory: ")
	require.Contains(t, got, "HOME_AGENTS_CONTENT")
	wsIdx := strings.Index(got, "WORKSPACE_FELIX_CONTENT")
	homeIdx := strings.Index(got, "HOME_AGENTS_CONTENT")
	require.Less(t, wsIdx, homeIdx, "workspace must appear before home")
}

func TestLoadAgentMemoryFilesAllFour(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"),
		[]byte("WS_FELIX"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"),
		[]byte("WS_AGENTS"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, "FELIX.md"),
		[]byte("HOME_FELIX"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, "AGENTS.md"),
		[]byte("HOME_AGENTS"), 0o644))

	got := LoadAgentMemoryFiles(workspace)

	// Order assertion: workspace FELIX → workspace AGENTS → home FELIX → home AGENTS.
	wsFelix := strings.Index(got, "WS_FELIX")
	wsAgents := strings.Index(got, "WS_AGENTS")
	homeFelix := strings.Index(got, "HOME_FELIX")
	homeAgents := strings.Index(got, "HOME_AGENTS")
	require.True(t, wsFelix >= 0 && wsAgents > wsFelix &&
		homeFelix > wsAgents && homeAgents > homeFelix,
		"order must be ws-felix < ws-agents < home-felix < home-agents; got %d %d %d %d",
		wsFelix, wsAgents, homeFelix, homeAgents)
}

func TestLoadAgentMemoryFilesSkipsEmptyFile(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"), []byte{}, 0o644))
	got := LoadAgentMemoryFiles(workspace)
	require.Equal(t, "", got, "empty file produces no section header")
}

func TestLoadAgentMemoryFilesSkipsWhitespaceOnly(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"),
		[]byte("\n  \t\n"), 0o644))
	got := LoadAgentMemoryFiles(workspace)
	require.Equal(t, "", got, "whitespace-only file produces no section header")
}

func TestLoadAgentMemoryFilesTruncatesOverCap(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	// 50 KB of content with newlines so the truncator has somewhere to cut.
	huge := strings.Repeat("Lorem ipsum dolor sit amet.\n", 2000) // ~54 KB
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"),
		[]byte(huge), 0o644))
	got := LoadAgentMemoryFiles(workspace)
	require.LessOrEqual(t, len(got), MaxAgentMemoryBytes+200,
		"output must respect cap (allowing some slack for header + truncation marker)")
	require.Contains(t, got, "[truncated — over 40 KB total agent memory]")
}

func TestLoadAgentMemoryFilesSkipsAfterTruncation(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	near := strings.Repeat("x\n", 20000) // ~40 KB; will exhaust the budget
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"),
		[]byte(near), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"),
		[]byte("AGENTS_SHOULD_NOT_APPEAR"), 0o644))
	got := LoadAgentMemoryFiles(workspace)
	require.Contains(t, got, "[truncated")
	require.NotContains(t, got, "AGENTS_SHOULD_NOT_APPEAR",
		"files after truncation must be fully skipped")
}

func TestLoadAgentMemoryFilesDedupSameDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // workspace == home
	require.NoError(t, os.WriteFile(filepath.Join(dir, "FELIX.md"),
		[]byte("UNIQUE_CONTENT_DEDUP_TEST"), 0o644))
	got := LoadAgentMemoryFiles(dir)
	occurrences := strings.Count(got, "UNIQUE_CONTENT_DEDUP_TEST")
	require.Equal(t, 1, occurrences, "same file at same path must appear exactly once")
}

func TestLoadAgentMemoryFilesEmptyHome(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", "")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"),
		[]byte("WORKSPACE_STILL_LOADS"), 0o644))
	require.NotPanics(t, func() {
		got := LoadAgentMemoryFiles(workspace)
		require.Contains(t, got, "WORKSPACE_STILL_LOADS")
	})
}

func TestLoadAgentMemoryFilesEmptyWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, "FELIX.md"),
		[]byte("HOME_STILL_LOADS"), 0o644))
	require.NotPanics(t, func() {
		got := LoadAgentMemoryFiles("")
		require.Contains(t, got, "HOME_STILL_LOADS")
	})
}
```

The existing `context_test.go` already imports `os`, `path/filepath`, `testing`, `require`. Add `strings` if not present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestLoadAgentMemoryFiles -v`
Expected: FAIL — `MaxAgentMemoryBytes` and `LoadAgentMemoryFiles` undefined.

- [ ] **Step 3: Implement the constant and helper**

In `internal/agent/context.go`, near the top of the file (after the `defaultIdentityBase` const around line 36, or just before `BuildConfigSummary`), add:

```go
// MaxAgentMemoryBytes caps the total bytes of FELIX.md / AGENTS.md content
// injected into the static system prompt. Mirrors Claude Code's
// MAX_MEMORY_CHARACTER_COUNT (claudemd.ts) at 40 KB.
const MaxAgentMemoryBytes = 40 * 1024
```

After `buildDynamicSystemPromptSuffix` (or anywhere convenient at the bottom of the file), add:

```go
// LoadAgentMemoryFiles reads FELIX.md and AGENTS.md from workspace and
// from $HOME. Returns the concatenated content with a brief header per
// source, or "" if nothing is found.
//
// Discovery order (highest priority first):
//   1. <workspace>/FELIX.md  — labelled "Project memory"
//   2. <workspace>/AGENTS.md — labelled "Project memory"
//   3. $HOME/FELIX.md        — labelled "User memory"
//   4. $HOME/AGENTS.md       — labelled "User memory"
//
// Empty files, whitespace-only files, missing files, and unreadable files
// are silently skipped. Files at the same absolute path (workspace ==
// $HOME) are deduped — each unique file appears at most once.
//
// Hard cap: total returned content ≤ MaxAgentMemoryBytes. When adding a
// file would push past the cap, the file's content is truncated at the
// last newline before the byte limit and a "[truncated — over 40 KB
// total agent memory]" marker is appended. Subsequent files are skipped
// entirely.
//
// Pure (single I/O exception: reads up to 4 files from disk). The
// returned string starts with "\n\n" so it composes cleanly after
// skillsIndex in the static system prompt.
func LoadAgentMemoryFiles(workspace string) string {
	type candidate struct {
		path  string
		label string // "Project memory" or "User memory"
	}
	var candidates []candidate
	if workspace != "" {
		candidates = append(candidates,
			candidate{filepath.Join(workspace, "FELIX.md"), "Project memory"},
			candidate{filepath.Join(workspace, "AGENTS.md"), "Project memory"},
		)
	}
	if home := os.Getenv("HOME"); home != "" {
		candidates = append(candidates,
			candidate{filepath.Join(home, "FELIX.md"), "User memory"},
			candidate{filepath.Join(home, "AGENTS.md"), "User memory"},
		)
	}

	seen := map[string]bool{}
	var sb strings.Builder
	truncated := false

	for _, c := range candidates {
		if truncated {
			break
		}
		abs, err := filepath.Abs(c.path)
		if err != nil {
			continue // best-effort: if abs fails, skip dedup but still try the read
			// (kept simple — this branch only fires for malformed paths)
		}
		if seen[abs] {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		seen[abs] = true
		body := strings.TrimSpace(string(data))
		if body == "" {
			continue
		}

		header := fmt.Sprintf("\n\n## %s: %s\n\n", c.label, abs)
		section := header + body

		// Cap check. If this section pushes us past MaxAgentMemoryBytes,
		// truncate body at the last newline before the limit and append
		// the marker.
		if sb.Len()+len(section) > MaxAgentMemoryBytes {
			remaining := MaxAgentMemoryBytes - sb.Len() - len(header)
			if remaining > 0 && remaining < len(body) {
				cut := body[:remaining]
				if idx := strings.LastIndex(cut, "\n"); idx > remaining/2 {
					cut = cut[:idx]
				}
				sb.WriteString(header)
				sb.WriteString(cut)
				sb.WriteString("\n\n[truncated — over 40 KB total agent memory]")
			} else if remaining > 0 {
				// Whole body fits within remaining; this branch unreachable
				// given the outer condition, but defend anyway.
				sb.WriteString(section)
			} else {
				// No room even for the header. Append marker by itself.
				sb.WriteString("\n\n[truncated — over 40 KB total agent memory]")
			}
			truncated = true
			continue
		}
		sb.WriteString(section)
	}

	return sb.String()
}
```

Imports already present in `context.go`: `fmt`, `os`, `path/filepath`, `strings`. No new imports needed.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/ -run TestLoadAgentMemoryFiles -v -count=1`
Expected: all 10 sub-tests PASS.

- [ ] **Step 5: Verify broader build/test**

Run: `go build ./... && go test ./internal/agent/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/context.go internal/agent/context_test.go
git commit -m "feat(agent): add LoadAgentMemoryFiles for FELIX.md/AGENTS.md walk-up"
```

---

## Task 2: Add `FormatDateLine`

**Files:**
- Modify: `internal/agent/context.go` (append helper)
- Test: `internal/agent/context_test.go` (extend)

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/context_test.go`:

```go
import "time"

func TestFormatDateLine(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"may day", time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), "Today's date is 2026-05-01."},
		{"new year", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "Today's date is 2027-01-01."},
		{"single digit month", time.Date(2026, 3, 9, 23, 59, 59, 0, time.UTC), "Today's date is 2026-03-09."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatDateLine(tc.in)
			require.Equal(t, tc.want, got)
		})
	}
}
```

Add `"time"` to the test file imports if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestFormatDateLine -v`
Expected: FAIL — `FormatDateLine` undefined.

- [ ] **Step 3: Implement**

In `internal/agent/context.go`, after `LoadAgentMemoryFiles` (or anywhere convenient), add:

```go
// FormatDateLine returns the canonical date line injected into the
// dynamic system suffix every Run. Single-line, deterministic format.
//
//	"Today's date is YYYY-MM-DD."
//
// Uses the caller's process timezone (no UTC normalization) so "today"
// matches the user's local sense of the day.
func FormatDateLine(now time.Time) string {
	return fmt.Sprintf("Today's date is %s.", now.Format("2006-01-02"))
}
```

Add `"time"` to the import block in `context.go` (currently has `fmt`, `os`, `path/filepath`, `strings` — add `time`).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/ -run TestFormatDateLine -v -count=1`
Expected: all 3 sub-cases PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/context.go internal/agent/context_test.go
git commit -m "feat(agent): add FormatDateLine for per-Run date injection"
```

---

## Task 3: Extend `BuildStaticSystemPrompt` with `memoryFiles` parameter

**Files:**
- Modify: `internal/agent/context.go` — add new parameter to `BuildStaticSystemPrompt`
- Modify: `internal/agent/builder.go` — update the single call site to pass `""` (Task 5 wires the real value)
- Modify: `internal/agent/agent_test.go` — update 4 existing call sites to pass `""`
- Modify: `internal/agent/context_test.go` — update 4 existing call sites to pass `""`
- Test: `internal/agent/context_test.go` — add coverage for the new parameter

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/context_test.go`:

```go
func TestBuildStaticSystemPromptIncludesMemoryFiles(t *testing.T) {
	dir := t.TempDir()
	got := BuildStaticSystemPrompt(
		dir, "", "id", "Name",
		[]string{"read_file"},
		"",                  // configSummary
		"",                  // skillsIndex
		"\n\n## Project memory: /tmp/x\n\nUNIQUE_MEM_FILES_SENTINEL", // memoryFiles
	)
	require.Contains(t, got, "UNIQUE_MEM_FILES_SENTINEL")
	require.Contains(t, got, "## Project memory:")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestBuildStaticSystemPromptIncludesMemoryFiles -v`
Expected: FAIL — too many arguments to `BuildStaticSystemPrompt`.

- [ ] **Step 3: Update `BuildStaticSystemPrompt` signature and body**

In `internal/agent/context.go`, modify the signature (currently lines 128-133):

```go
func BuildStaticSystemPrompt(
	workspace, systemPrompt, agentID, agentName string,
	toolNames []string,
	configSummary string,
	skillsIndex string,
	memoryFiles string,
) string {
```

In the function body, after the `if skillsIndex != ""` block, add (the new content sits at the very end of the static prompt):

```go
	if memoryFiles != "" {
		base += memoryFiles
	}
```

(`memoryFiles` already starts with `\n\n` per Task 1's contract, so no separator is added by `BuildStaticSystemPrompt`.)

- [ ] **Step 4: Update the single production caller in `builder.go`**

In `internal/agent/builder.go`, find the `BuildStaticSystemPrompt(...)` call (around line 85-88) and add a `""` argument for `memoryFiles`. Task 5 will replace this with the real value.

```go
staticPrompt := BuildStaticSystemPrompt(
	a.Workspace, a.SystemPrompt, a.ID, a.Name,
	toolNames, configSummary, skillsIndex,
	"", // memoryFiles — wired in Task 5
)
```

- [ ] **Step 5: Update existing test callers**

Run: `grep -n 'BuildStaticSystemPrompt(' internal/agent/agent_test.go internal/agent/context_test.go`

For each call site, append a `""` argument for `memoryFiles`. Affected files (at minimum):

In `internal/agent/agent_test.go`:
```go
BuildStaticSystemPrompt(dir, "", "test", "Test Agent",
    []string{"read_file", "bash"}, "", "", "")
// (4 such call sites in the migrated tests from sub-project #1)
```

In `internal/agent/context_test.go`:
```go
BuildStaticSystemPrompt(dir, "", "alpha", "Alpha",
    []string{"read_file"},
    "Configured channels: cli",
    "\n\n## Skills Index\n\n- foo",
    "", // memoryFiles
)
// (and 3 other call sites in TestBuildStaticSystemPrompt* tests)
```

Add the trailing `""` to every existing call.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/agent/ -count=1`
Expected: all PASS, including the new `TestBuildStaticSystemPromptIncludesMemoryFiles`.

- [ ] **Step 7: Verify broader build**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add internal/agent/context.go internal/agent/builder.go \
        internal/agent/agent_test.go internal/agent/context_test.go
git commit -m "feat(agent): add memoryFiles parameter to BuildStaticSystemPrompt"
```

---

## Task 4: Extend `buildDynamicSystemPromptSuffix` with `dateLine` parameter

**Files:**
- Modify: `internal/agent/context.go` — add new parameter to `buildDynamicSystemPromptSuffix`
- Modify: `internal/agent/runtime.go` — update the single call site to pass `""` (Task 6 wires the real value)
- Modify: `internal/agent/context_test.go` — update 3 existing call sites to pass `""`
- Test: `internal/agent/context_test.go` — add coverage for the new parameter

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/context_test.go`:

```go
func TestBuildDynamicSystemPromptSuffixIncludesDate(t *testing.T) {
	got := buildDynamicSystemPromptSuffix(
		"Today's date is 2026-05-01.",
		nil, nil, "",
	)
	require.True(t, strings.HasPrefix(got, "Today's date is 2026-05-01."),
		"date line must appear at the top of the dynamic suffix")
}

func TestBuildDynamicSystemPromptSuffixDatePrependedBeforeOtherSources(t *testing.T) {
	skills := []skill.Skill{{Name: "foo", Body: "FOO_BODY"}}
	got := buildDynamicSystemPromptSuffix(
		"Today's date is 2026-05-01.",
		skills, nil, "\n\nCORTEX_HINT",
	)
	dateIdx := strings.Index(got, "Today's date is")
	skillIdx := strings.Index(got, "FOO_BODY")
	cortexIdx := strings.Index(got, "CORTEX_HINT")
	require.True(t, dateIdx >= 0 && skillIdx > dateIdx && cortexIdx > skillIdx,
		"order must be date < skills < cortex; got %d %d %d", dateIdx, skillIdx, cortexIdx)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestBuildDynamicSystemPromptSuffixIncludesDate -v`
Expected: FAIL — too many arguments to `buildDynamicSystemPromptSuffix`.

- [ ] **Step 3: Update `buildDynamicSystemPromptSuffix` signature and body**

In `internal/agent/context.go`, modify the signature (currently lines 168-172) and prepend the date:

```go
func buildDynamicSystemPromptSuffix(
	dateLine string,
	matchedSkills []skill.Skill,
	matchedMemory []memory.Entry,
	cortexContext string,
) string {
	var sb strings.Builder
	if dateLine != "" {
		sb.WriteString(dateLine)
	}
	if extra := skill.FormatForPrompt(matchedSkills); extra != "" {
		sb.WriteString(extra)
	}
	if extra := memory.FormatForPrompt(matchedMemory); extra != "" {
		sb.WriteString(extra)
	}
	if cortexContext != "" {
		sb.WriteString(cortexContext)
	}
	return sb.String()
}
```

(`skill.FormatForPrompt` returns content starting with `\n\n## Available Skills\n\n...`, so no separator between dateLine and skills is needed — the skills helper already brings its own leading newlines.)

- [ ] **Step 4: Update the production caller in `runtime.go`**

In `internal/agent/runtime.go`, find the call (around line 292):

```go
dynamicSuffix := buildDynamicSystemPromptSuffix(matchedSkills, matchedMemory, cortexContext)
```

Replace with:

```go
dynamicSuffix := buildDynamicSystemPromptSuffix("", matchedSkills, matchedMemory, cortexContext)
```

(Task 6 will replace `""` with `dateLine`.)

- [ ] **Step 5: Update existing test callers**

Run: `grep -n 'buildDynamicSystemPromptSuffix(' internal/agent/context_test.go`

There are 3 existing call sites in `TestBuildDynamicSystemPromptSuffix*` tests. Prepend a `""` argument to each:

```go
buildDynamicSystemPromptSuffix("", skills, mem, cortex) // was: (skills, mem, cortex)
buildDynamicSystemPromptSuffix("", nil, nil, "")         // was: (nil, nil, "")
buildDynamicSystemPromptSuffix("", nil, nil, "\n\nCORTEX") // was: (nil, nil, "\n\nCORTEX")
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/agent/ -count=1`
Expected: all PASS, including the 2 new dateLine tests.

- [ ] **Step 7: Verify broader build**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add internal/agent/context.go internal/agent/runtime.go internal/agent/context_test.go
git commit -m "feat(agent): add dateLine parameter to buildDynamicSystemPromptSuffix"
```

---

## Task 5: Wire `LoadAgentMemoryFiles` into `BuildRuntimeForAgent`

**Files:**
- Modify: `internal/agent/builder.go` — replace the `""` placeholder with `LoadAgentMemoryFiles(a.Workspace)`
- Test: `internal/agent/builder_test.go` — add end-to-end test

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/builder_test.go`:

```go
func TestBuildRuntimeForAgentLoadsMemoryFilesIntoStaticPrompt(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "FELIX.md"),
		[]byte("MEMFILE_END_TO_END_SENTINEL"),
		0o644,
	))

	a := &config.AgentConfig{
		ID:        "a",
		Name:      "A",
		Workspace: workspace,
		Model:     "anthropic/claude-sonnet-4-5",
	}
	rt, err := BuildRuntimeForAgent(RuntimeDeps{}, RuntimeInputs{}, a)
	require.NoError(t, err)
	require.Contains(t, rt.StaticSystemPrompt, "MEMFILE_END_TO_END_SENTINEL")
	require.Contains(t, rt.StaticSystemPrompt, "## Project memory:")
}
```

Add `"os"` and `"path/filepath"` to `builder_test.go` imports if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestBuildRuntimeForAgentLoadsMemoryFiles -v`
Expected: FAIL — sentinel string not in `rt.StaticSystemPrompt` because Task 3's wiring still passes `""`.

- [ ] **Step 3: Wire `LoadAgentMemoryFiles` into the builder**

In `internal/agent/builder.go`, locate the `BuildStaticSystemPrompt` call and replace the placeholder. Update from Task 3's:

```go
staticPrompt := BuildStaticSystemPrompt(
	a.Workspace, a.SystemPrompt, a.ID, a.Name,
	toolNames, configSummary, skillsIndex,
	"", // memoryFiles — wired in Task 5
)
```

To:

```go
memoryFiles := LoadAgentMemoryFiles(a.Workspace)
staticPrompt := BuildStaticSystemPrompt(
	a.Workspace, a.SystemPrompt, a.ID, a.Name,
	toolNames, configSummary, skillsIndex,
	memoryFiles,
)
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/ -run TestBuildRuntimeForAgentLoadsMemoryFiles -v -count=1`
Expected: PASS.

- [ ] **Step 5: Verify broader build/test**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/builder.go internal/agent/builder_test.go
git commit -m "feat(agent): wire LoadAgentMemoryFiles into BuildRuntimeForAgent"
```

---

## Task 6: Wire `FormatDateLine` into `Runtime.Run`

**Files:**
- Modify: `internal/agent/runtime.go` — compute `dateLine` once at the start of `Run()`; pass into the per-turn dynamic suffix
- Test: `internal/agent/cache_stability_test.go` — add 2 end-to-end tests

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/cache_stability_test.go`:

```go
func TestRuntimeDynamicSuffixIncludesDate(t *testing.T) {
	rec := &recordingProvider{reply: "ok"}
	sess := session.NewSession("test-agent", "test-key")
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "a", Name: "A", Model: "anthropic/claude-sonnet-4-5"},
	}
	rt, err := BuildRuntimeForAgent(
		RuntimeDeps{Config: cfg},
		RuntimeInputs{Provider: rec, Tools: tools.NewRegistry(), Session: sess},
		&cfg.Agents.List[0],
	)
	require.NoError(t, err)
	rt.MaxTurns = 5

	ev, err := rt.Run(context.Background(), "hi", nil)
	require.NoError(t, err)
	for range ev {
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.GreaterOrEqual(t, len(rec.requests), 1)
	require.GreaterOrEqual(t, len(rec.requests[0].SystemPromptParts), 2,
		"expected static + dynamic parts")
	require.Contains(t, rec.requests[0].SystemPromptParts[1].Text, "Today's date is ",
		"dynamic suffix must contain the date line")
}

// TestRuntimeDateLineByteStableAcrossTurnsWithinRun verifies the date is
// computed once at the start of Run and reused across all turns of that
// Run (not re-computed per loop iteration). This is what keeps the
// dynamic suffix's date portion deterministic across a multi-turn Run.
func TestRuntimeDateLineByteStableAcrossTurnsWithinRun(t *testing.T) {
	rec := &recordingProvider{reply: ""} // no text reply

	// Use a tool registry with one tool the model can call so we get
	// at least 2 turns within the Run.
	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	reg.Register(&mockTool{name: "ping", output: "pong"})

	// Configure the recording provider to emit a tool call on turn 0
	// and a plain reply on turn 1 so the loop terminates.
	rec.replies = []string{"", "done"}
	rec.toolCallsPerTurn = [][]llm.ToolCall{
		{{ID: "tc1", Name: "ping", Input: []byte(`{}`)}},
		nil,
	}

	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "a", Name: "A", Model: "anthropic/claude-sonnet-4-5"},
	}
	rt, err := BuildRuntimeForAgent(
		RuntimeDeps{Config: cfg},
		RuntimeInputs{Provider: rec, Tools: reg, Session: sess},
		&cfg.Agents.List[0],
	)
	require.NoError(t, err)
	rt.MaxTurns = 5

	ev, err := rt.Run(context.Background(), "hi", nil)
	require.NoError(t, err)
	for range ev {
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.GreaterOrEqual(t, len(rec.requests), 2, "expected at least 2 turns")

	// Extract the date-line prefix from each turn's dynamic suffix.
	// Date line format: "Today's date is YYYY-MM-DD." (29 chars).
	turn0Dynamic := rec.requests[0].SystemPromptParts[1].Text
	turn1Dynamic := rec.requests[1].SystemPromptParts[1].Text
	const datePrefix = "Today's date is "
	t0Idx := strings.Index(turn0Dynamic, datePrefix)
	t1Idx := strings.Index(turn1Dynamic, datePrefix)
	require.True(t, t0Idx >= 0 && t1Idx >= 0, "both turns must include date line")

	// The date line is the substring from datePrefix start through the next ".".
	t0Date := turn0Dynamic[t0Idx : t0Idx+len(datePrefix)+11] // "YYYY-MM-DD."
	t1Date := turn1Dynamic[t1Idx : t1Idx+len(datePrefix)+11]
	require.Equal(t, t0Date, t1Date,
		"date line must be byte-identical across turns within a Run")
}
```

NOTE on the second test: the existing `recordingProvider` may not yet support `replies []string` and `toolCallsPerTurn [][]llm.ToolCall`. Check by grepping `internal/agent/cache_stability_test.go` for the current `recordingProvider` definition. If those fields don't exist, either:
- (a) extend `recordingProvider` minimally to support a per-turn reply sequence, OR
- (b) replace the second test with a simpler one that uses a single-turn flow but verifies the date is *present* (already covered by `TestRuntimeDynamicSuffixIncludesDate`) and add a separate unit test on `buildDynamicSystemPromptSuffix` to prove the same date string passed in is returned in the same position both times (which is already trivially true for a pure function).

If (b) is chosen, the simpler stand-in test:

```go
func TestRuntimeDateLineComputedOncePerRun(t *testing.T) {
	// A pure-function check: calling buildDynamicSystemPromptSuffix
	// twice with the same dateLine produces identical output for that
	// portion. Combined with the runtime call site in runtime.go (Step 3
	// below) which computes dateLine once before the for-turn loop, this
	// proves the date is stable across turns of a single Run.
	dl := "Today's date is 2026-05-01."
	a := buildDynamicSystemPromptSuffix(dl, nil, nil, "")
	b := buildDynamicSystemPromptSuffix(dl, nil, nil, "")
	require.Equal(t, a, b)
}
```

The runtime-level test then becomes a single-turn verification of presence (already in `TestRuntimeDynamicSuffixIncludesDate` above). Pick (a) if `recordingProvider` is easily extensible (preferred for stronger end-to-end coverage); otherwise (b) is acceptable.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestRuntimeDynamicSuffixIncludesDate -v`
Expected: FAIL — date line not in dynamic suffix because Task 4's wiring still passes `""`.

- [ ] **Step 3: Wire `FormatDateLine` into `Run()`**

In `internal/agent/runtime.go`, find the `Run` method. The per-turn loop starts around line 243 (`for turn := 0; turn < maxTurns; turn++`).

Just BEFORE the for-loop opens (right after the `var matchedSkills` / `var matchedMemory` declarations, around line 240-241), add:

```go
dateLine := FormatDateLine(time.Now())
```

Then in the per-turn loop body (around line 292), update the call:

```go
dynamicSuffix := buildDynamicSystemPromptSuffix("", matchedSkills, matchedMemory, cortexContext)
```

To:

```go
dynamicSuffix := buildDynamicSystemPromptSuffix(dateLine, matchedSkills, matchedMemory, cortexContext)
```

`time` is likely already imported in `runtime.go` (used by trace marks and timeout selects). Verify and add if absent.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/ -run TestRuntimeDynamicSuffixIncludesDate -v -count=1`
Expected: PASS.

If you went with option (b) for Step 1, also run:
Run: `go test ./internal/agent/ -run TestRuntimeDateLineComputedOncePerRun -v -count=1`
Expected: PASS.

- [ ] **Step 5: Verify broader build/test**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/runtime.go internal/agent/cache_stability_test.go
git commit -m "feat(agent): inject FormatDateLine(time.Now()) into per-Run dynamic suffix"
```

---

## Task 7: End-to-end integration test for memoryFiles in static prompt

**Files:**
- Test: `internal/agent/cache_stability_test.go` — add a test that proves memoryFiles content lands in the LIVE request's `SystemPromptParts[0].Text`

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/cache_stability_test.go`:

```go
func TestRuntimeStaticPromptIncludesMemoryFilesContent(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "FELIX.md"),
		[]byte("RUNTIME_MEMFILE_INTEGRATION_SENTINEL"),
		0o644,
	))

	rec := &recordingProvider{reply: "ok"}
	sess := session.NewSession("test-agent", "test-key")
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "a",
			Name:      "A",
			Workspace: workspace,
			Model:     "anthropic/claude-sonnet-4-5",
		},
	}
	rt, err := BuildRuntimeForAgent(
		RuntimeDeps{Config: cfg},
		RuntimeInputs{Provider: rec, Tools: tools.NewRegistry(), Session: sess},
		&cfg.Agents.List[0],
	)
	require.NoError(t, err)
	rt.MaxTurns = 5

	ev, err := rt.Run(context.Background(), "hi", nil)
	require.NoError(t, err)
	for range ev {
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.GreaterOrEqual(t, len(rec.requests), 1)
	staticPart := rec.requests[0].SystemPromptParts[0].Text
	require.Contains(t, staticPart, "RUNTIME_MEMFILE_INTEGRATION_SENTINEL")
	require.Contains(t, staticPart, "## Project memory:")
	require.True(t, rec.requests[0].SystemPromptParts[0].Cache,
		"static part with memory files must still be cache-marked")
}
```

Add `"os"` and `"path/filepath"` to `cache_stability_test.go` imports if not present.

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestRuntimeStaticPromptIncludesMemoryFilesContent -v -count=1`
Expected: PASS (Tasks 1, 3, 5 already wired everything; this test just locks in the integration).

If it fails, the wiring from Tasks 3 and 5 is incomplete — fix before continuing.

- [ ] **Step 3: Verify the static prefix is still byte-stable**

Run: `go test ./internal/agent/ -run TestRuntimeStaticPromptByteStableAcrossTurns -v -count=1`
Expected: PASS. (This was added by sub-project #1; should still pass since `LoadAgentMemoryFiles` is deterministic and read once at construction.)

- [ ] **Step 4: Run the full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: every package PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/cache_stability_test.go
git commit -m "test(agent): pin memoryFiles + cache-marker integration in runtime path"
```

---

## Self-review checklist (filled out before handoff)

**1. Spec coverage:**
- §1 `LoadAgentMemoryFiles` + `MaxAgentMemoryBytes` → Task 1.
- §2 `FormatDateLine` → Task 2.
- §3 `BuildStaticSystemPrompt` extension → Task 3.
- §4 `buildDynamicSystemPromptSuffix` extension → Task 4.
- §5 `BuildRuntimeForAgent` wiring → Task 5.
- §6 per-turn loop wiring → Task 6.
- Spec's unit test list (10 `LoadAgentMemoryFiles` cases) → Task 1 Step 1.
- Spec's `TestFormatDateLine` → Task 2.
- Spec's `TestBuildStaticSystemPromptIncludesMemoryFiles` → Task 3.
- Spec's `TestBuildDynamicSystemPromptSuffixIncludesDate` → Task 4.
- Spec's `TestBuildRuntimeForAgentLoadsMemoryFilesIntoStaticPrompt` → Task 5.
- Spec's `TestRuntimeStaticPromptIncludesMemoryFilesContent` → Task 7.
- Spec's `TestRuntimeDynamicSuffixIncludesDate` → Task 6.
- Spec's `TestRuntimeDateLineByteStableAcrossTurnsWithinRun` → Task 6 (with fallback option (b) if `recordingProvider` isn't extensible).

**2. Placeholder scan:** no TBD/TODO/"add error handling"/etc. in the plan body. The phrase "wired in Task 5" / "wired in Task 6" is a deliberate cross-reference, not a placeholder — Tasks 5 and 6 explicitly do that wiring.

**3. Type consistency:**
- `LoadAgentMemoryFiles(workspace string) string` consistent across Tasks 1, 5.
- `FormatDateLine(now time.Time) string` consistent across Tasks 2, 6.
- `BuildStaticSystemPrompt` final signature has 8 params: `(workspace, systemPrompt, agentID, agentName string, toolNames []string, configSummary, skillsIndex, memoryFiles string) string`. Used consistently in Tasks 3, 5.
- `buildDynamicSystemPromptSuffix(dateLine string, matchedSkills []skill.Skill, matchedMemory []memory.Entry, cortexContext string) string` consistent across Tasks 4, 6.
- `MaxAgentMemoryBytes = 40 * 1024` defined in Task 1 Step 3, used in Task 1 Step 1's `TestLoadAgentMemoryFilesTruncatesOverCap`.

---

Plan complete and saved to `docs/superpowers/plans/2026-05-01-per-turn-dynamic-context-plan.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
