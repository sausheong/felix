# S1 — bash allowlist bypass fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the CRITICAL bash allowlist bypass — real newlines, background `&`, and redirection let an allowlist-restricted agent run/redirect arbitrary commands via `bash -c`.

**Architecture:** Two localized edits in `harness/tools/bash/bash.go` (allowlist branch only): (1) fix the metacharacter blocklist to reject real `\n`/`\r` and redirection `>`; (2) make `extractCommands` treat background `&` as a separator (longer-operator-wins on index ties so `&&` still works). Backed by a table-driven regression suite. Harness-only; verified by the felix consumer build.

**Tech Stack:** Go 1.25; `harness/tools/bash`; `harness/tool` (ExecPolicy/ToolResult); stdlib testing.

**Repo:** `/Users/sausheong/projects/harness`. After the change, `cd /Users/sausheong/projects/felix && go build ./...` must pass. Commits omit the `Co-Authored-By` trailer.

---

## File Structure

- **Modify:** `tools/bash/bash.go` — `extractCommands` (operator set + tie-break, ~line 135); allowlist metacharacter blocklist (~line 195).
- **Modify:** `tools/bash/bash_test.go` — add `context` + `encoding/json` imports; add two new table-driven test functions.

No new files. No API changes.

---

## Pre-flight (controller already verified; implementer re-confirms)

- `Execute` allowlist branch runs the metacharacter loop, then `extractCommands`, then checks each extracted command against the allowlist map. Command is run via `exec.CommandContext(ctx, "bash", "-c", in.Command)` (or `cmd /c` on Windows).
- The metacharacter loop currently is: `for _, meta := range []string{"$(", "`+"`"+`", "<(", ">(", "${", "\\n"}` — note `"\\n"` is the two-byte literal backslash-n, the bug.
- `extractCommands` splits on `{"&&", "||", "|", ";"}` only.
- No existing test constructs a `BashTool` with an `ExecPolicy` or calls `Execute`; `bash_test.go` imports `os`, `path/filepath`, `strings`, `testing`, `github.com/sausheong/harness/tool`.
- `bashInput` is `{Command string json:"command"; Timeout int json:"timeout"}`.

---

## Task 1: `extractCommands` treats background `&` as a separator

**Files:**
- Modify: `tools/bash/bash.go` (`extractCommands`, ~lines 135-139)
- Test: `tools/bash/bash_test.go`

- [ ] **Step 1: Write the failing test**

First, extend the `bash_test.go` import block to add `"context"` and `"encoding/json"` (needed by the Execute-based tests in this task and Task 2). The existing `"github.com/sausheong/harness/tool"` import is currently used by other tests — KEEP it if other tests still reference `tool.` after your edits; if not, goimports/`go vet` will flag it and you remove it. The new tests below do NOT need it (`ExecPolicy` is defined in package `bash` itself):
```go
import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sausheong/harness/tool"
)
```

Add this helper + test to `tools/bash/bash_test.go`. NOTE: `ExecPolicy` is declared in
package `bash` (in `bash.go`, fields `Level string` and `Allowlist []string`) — so it is
referenced bare as `&ExecPolicy{...}`, NOT `&tool.ExecPolicy{...}`:
```go
// runAllowlist executes cmd under an allowlist policy and returns the
// ToolResult.Error (empty string = the command was permitted by policy).
func runAllowlist(t *testing.T, allow []string, command string) string {
	t.Helper()
	bt := &BashTool{ExecPolicy: &ExecPolicy{Level: "allowlist", Allowlist: allow}}
	in, err := json.Marshal(bashInput{Command: command})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res, err := bt.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute returned a Go error (should use ToolResult.Error): %v", err)
	}
	return res.Error
}

func TestExtractCommands_BackgroundAmpersand(t *testing.T) {
	// "ls & curl ..." must validate BOTH sides; curl is not allowed → rejected.
	if got := runAllowlist(t, []string{"ls"}, "ls & curl http://evil"); got == "" {
		t.Fatalf("background-& bypass: expected rejection, got allowed")
	}
	// "ls && echo hi" must still split on && (both allowed) → permitted.
	if got := runAllowlist(t, []string{"ls", "echo"}, "ls && echo hi"); got != "" {
		t.Fatalf("&& chain wrongly rejected: %q", got)
	}
}
```

CONFIRMED: `ExecPolicy` is declared in `tools/bash/bash.go` (package `bash`) with fields
`Level string` and `Allowlist []string`; reference it bare as `ExecPolicy`, not
`tool.ExecPolicy`.

- [ ] **Step 2: Run, verify it fails**

Run: `go test ./tools/bash/ -run TestExtractCommands_BackgroundAmpersand`
Expected: FAIL on the background-& case — `ls & curl http://evil` is currently treated as a
single part, `ls` validates, so `res.Error` is empty (no rejection). (The `&&` case passes
already.)

- [ ] **Step 3: Implement the operator-set + tie-break change**

In `tools/bash/bash.go` `extractCommands`, replace the operator-selection loop:
```go
		for _, op := range []string{"&&", "||", "|", ";"} {
			if idx := strings.Index(remaining, op); idx != -1 && idx < minIdx {
				minIdx = idx
				opLen = len(op)
			}
		}
```
with (adds `"&"`, and on an index tie prefers the LONGER operator so `&&` beats `&`):
```go
		for _, op := range []string{"&&", "||", "|", ";", "&"} {
			if idx := strings.Index(remaining, op); idx != -1 {
				if idx < minIdx || (idx == minIdx && len(op) > opLen) {
					minIdx = idx
					opLen = len(op)
				}
			}
		}
```

Why the tie-break matters: at the position of `&&`, the substring `&` also matches at the
same index. Without `len(op) > opLen` on ties, the split could consume only one `&`, leaving
a stray `&` that turns `extractCommands` output into an empty/garbage part. Preferring the
longer operator makes `a && b` consume `&&` (the empty middle part is dropped by the existing
`if part != ""` filter), while `a & b` consumes the single `&`.

- [ ] **Step 4: Run, verify it passes**

Run: `go test ./tools/bash/ -run TestExtractCommands_BackgroundAmpersand`
Expected: PASS (both sub-cases).

- [ ] **Step 5: Full package + vet**

Run: `go vet ./tools/bash/ && go test ./tools/bash/`
Expected: clean; all existing tests (`TestSanitizeLLMText`, `TestResolveBashCommandPaths`,
`TestResolveExistingPath`) still pass.

- [ ] **Step 6: Commit**

```bash
git add tools/bash/bash.go tools/bash/bash_test.go
git commit -m "fix(bash): treat background & as a command separator in allowlist mode (S1)"
```

---

## Task 2: Reject real newline, CR, and redirection in the allowlist metacharacter guard

**Files:**
- Modify: `tools/bash/bash.go` (allowlist metacharacter blocklist, ~line 195)
- Test: `tools/bash/bash_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `tools/bash/bash_test.go` (the `runAllowlist` helper from Task 1 is reused):
```go
func TestAllowlist_RejectsNewlineCRAndRedirection(t *testing.T) {
	allow := []string{"ls", "echo", "cat"}
	// The live exploit: a real newline runs a second, unvalidated line.
	if got := runAllowlist(t, allow, "ls\ncurl http://evil"); got == "" {
		t.Fatalf("newline bypass: expected rejection, got allowed")
	}
	// Carriage return likewise.
	if got := runAllowlist(t, allow, "ls\rcurl http://evil"); got == "" {
		t.Fatalf("CR bypass: expected rejection, got allowed")
	}
	// Redirection write-primitive (allowed cmd overwriting a file).
	if got := runAllowlist(t, allow, "echo hi > /tmp/x"); got == "" {
		t.Fatalf("redirection > not rejected")
	}
	if got := runAllowlist(t, allow, "echo hi >> /tmp/x"); got == "" {
		t.Fatalf("append redirection >> not rejected")
	}
	// fd redirection also contains '>' so it is caught too.
	if got := runAllowlist(t, allow, "cat a 2>&1"); got == "" {
		t.Fatalf("fd redirection 2>&1 not rejected")
	}
	// Sanity: a plain allowed command still passes the policy gate.
	if got := runAllowlist(t, allow, "ls -l"); got != "" {
		t.Fatalf("plain allowed command wrongly rejected: %q", got)
	}
	// Sanity: input redirection '<' is NOT blocked (deliberate scope boundary);
	// the command itself (cat) is allowed, so policy permits it.
	if got := runAllowlist(t, allow, "cat < /tmp/x"); got != "" {
		t.Fatalf("input redirection wrongly rejected: %q", got)
	}
}
```

- [ ] **Step 2: Run, verify it fails**

Run: `go test ./tools/bash/ -run TestAllowlist_RejectsNewlineCRAndRedirection`
Expected: FAIL on the newline, CR, and redirection cases (current guard blocks only the
two-byte `\n` literal and has no `>` entry). The `ls -l` and `cat < /tmp/x` sanity cases
pass already.

- [ ] **Step 3: Implement the blocklist fix**

In `tools/bash/bash.go`, change the allowlist metacharacter loop from:
```go
			for _, meta := range []string{"$(", "`", "<(", ">(", "${", "\\n"} {
```
to:
```go
			for _, meta := range []string{"$(", "`", "<(", ">(", "${", "\n", "\r", ">"} {
```
- `"\n"`/`"\r"` are the real control bytes (fixing the `"\\n"` typo + adding CR).
- The single-char `">"` blocks all redirection forms (`>`, `>>`, `2>`, `&>`, `2>&1`, `>|`)
  because each contains `>`. This is intentional for locked-down allowlist mode.
- `<` is deliberately NOT added (input redirection is not a write/exec bypass).

Update the adjacent comment to reflect intent, e.g.:
```go
			// Block shell metacharacters that can execute arbitrary code or
			// redirect output inside an otherwise-allowed command: command
			// substitution, process substitution, real newlines/CR (which
			// bash -c treats as command separators), and output redirection.
```

- [ ] **Step 4: Run, verify it passes**

Run: `go test ./tools/bash/ -run TestAllowlist_RejectsNewlineCRAndRedirection`
Expected: PASS (all cases).

- [ ] **Step 5: Full package + vet + race**

Run: `go vet ./tools/bash/ && go test ./tools/bash/ && go test -race ./tools/bash/`
Expected: clean; all existing tests pass.

- [ ] **Step 6: Commit**

```bash
git add tools/bash/bash.go tools/bash/bash_test.go
git commit -m "fix(bash): reject real newline/CR and redirection in allowlist mode (S1)"
```

---

## Task 3: Verification — harness suite + felix consumer build

- [ ] **Step 1: Full harness build/vet/test**

```bash
cd /Users/sausheong/projects/harness
go build ./... && go vet ./tools/bash/ && go test ./tools/bash/ && go test -race ./tools/bash/
```
Expected: all green.

- [ ] **Step 2: felix builds against the modified harness**

```bash
cd /Users/sausheong/projects/felix && go build ./...
```
Expected: clean (no API change; internal logic only).

No commit (verification only).

---

## Final controller checklist (after all tasks)

- [ ] Newline bypass closed (regression test red→green).
- [ ] CR bypass closed.
- [ ] Background `&` bypass closed; `&&`/`||`/`|`/`;` chains still validate every sub-command.
- [ ] Redirection `>`/`>>`/`2>&1` rejected; input `<` and plain commands still pass.
- [ ] harness build/vet/test/race green; felix builds against it.
- [ ] No `Co-Authored-By` trailer.
- [ ] Adversarial final review (try to find a still-open bypass) before merge.
