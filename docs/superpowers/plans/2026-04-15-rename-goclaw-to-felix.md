# Rename goclaw → felix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the project from "goclaw" / "GoClaw" to "felix" / "Felix" across all source files, configuration, build tooling, and documentation.

**Architecture:** This is a mechanical rename touching the Go module path, cmd directory names, in-code string literals (config paths, binary names, metric names, log filenames), build config (Makefile + goreleaser), the macOS .app bundle, and all documentation. No logic changes — purely renaming.

**Tech Stack:** Go (module rename via `go mod edit`), shell (sed/find for bulk rename), Makefile, goreleaser YAML, plist XML.

---

## Rename map

| Old | New |
|-----|-----|
| `github.com/sausheong/goclaw` | `github.com/sausheong/felix` |
| `cmd/goclaw/` | `cmd/felix/` |
| `cmd/goclaw-app/` | `cmd/felix-app/` |
| binary `goclaw` | `felix` |
| binary `goclaw-app` | `felix-app` |
| `GoClaw.app` | `Felix.app` |
| `~/.goclaw/` | `~/.felix/` |
| `goclaw.json5` | `felix.json5` |
| `goclaw_*` (metrics) | `felix_*` |
| `goclaw-app.log` | `felix-app.log` |
| `com.goclaw.app` | `com.felix.app` |
| `GoClaw` (brand) | `Felix` |

---

## Files Touched

**Modified:**
- `go.mod` — module path
- `internal/config/config.go` — data dir name, config filename
- `internal/config/config_test.go` — config filename references
- `internal/config/watcher_test.go` — config filename references
- `internal/gateway/metrics.go` — Prometheus metric names
- `internal/gateway/ui.go` — any GoClaw brand strings
- `internal/channel/whatsapp.go` — "goclaw start" user-facing string
- `internal/channel/telegram.go` — "goclaw start" comment
- `internal/startup/startup.go` — "cmd/goclaw" comment
- `internal/skill/skill.go` — "~/.goclaw/" comment
- `internal/tools/policy.go`, `browser.go`, `tool.go`, `readfile.go` — import paths
- `internal/agent/runtime.go`, `context.go`, `agent_test.go` — import paths
- `internal/cortex/cortex.go` — import path
- All other `.go` files with `github.com/sausheong/goclaw` imports
- `cmd/felix/main.go` (was `cmd/goclaw/main.go`) — binary name strings, screenshot tmp file name
- `cmd/felix-app/main.go` (was `cmd/goclaw-app/main.go`) — log file name
- `cmd/felix-app/Info.plist` — bundle ID, CFBundleName, CFBundleDisplayName, CFBundleExecutable
- `Makefile` — BINARY, CMD, app binary names, GoClaw.app references
- `.goreleaser.yaml` — project_name, build main path, binary name, release repo name
- `README.md` — all brand/binary/path mentions
- `howtouse.md` — all brand/binary/path mentions
- `CLAUDE.md` — project name and binary references

**Renamed (directories/files):**
- `cmd/goclaw/` → `cmd/felix/`
- `cmd/goclaw-app/` → `cmd/felix-app/`
- `GoClaw.app/` → `Felix.app/` (the checked-in bundle skeleton)

---

### Task 1: Update the Go module path

This renames the module declaration and all import paths in one go. No manual file editing needed.

**Files:** `go.mod`, all `*.go` files

- [ ] **Step 1: Update go.mod module declaration**

```bash
go mod edit -module github.com/sausheong/felix
```

Expected: `go.mod` now reads `module github.com/sausheong/felix`

- [ ] **Step 2: Update all import paths in Go source files**

```bash
find . -name "*.go" | xargs sed -i '' 's|github.com/sausheong/goclaw|github.com/sausheong/felix|g'
```

- [ ] **Step 3: Verify imports are clean**

```bash
grep -r "github.com/sausheong/goclaw" --include="*.go" .
```

Expected: no output (zero matches)

- [ ] **Step 4: Confirm the build still compiles**

```bash
go build ./...
```

Expected: exits 0, no errors

- [ ] **Step 5: Commit**

```bash
git add go.mod
git add $(git diff --name-only)
git commit -m "rename: update Go module path to github.com/sausheong/felix"
```

---

### Task 2: Rename cmd directories

**Files:** `cmd/goclaw/` → `cmd/felix/`, `cmd/goclaw-app/` → `cmd/felix-app/`

- [ ] **Step 1: Rename the CLI cmd directory**

```bash
git mv cmd/goclaw cmd/felix
```

- [ ] **Step 2: Rename the tray app cmd directory**

```bash
git mv cmd/goclaw-app cmd/felix-app
```

- [ ] **Step 3: Rebuild to confirm paths work**

```bash
go build -o /tmp/felix-test ./cmd/felix && echo "CLI OK"
go build -o /tmp/felix-app-test ./cmd/felix-app && echo "App OK"
rm -f /tmp/felix-test /tmp/felix-app-test
```

Expected: both print OK

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "rename: move cmd/goclaw → cmd/felix and cmd/goclaw-app → cmd/felix-app"
```

---

### Task 3: Update in-code string literals

All user-visible and config-path strings that reference "goclaw" in source files.

**Files:**
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/config/watcher_test.go`
- `internal/gateway/metrics.go`
- `cmd/felix/main.go`
- `cmd/felix-app/main.go`
- `internal/channel/whatsapp.go`
- `internal/channel/telegram.go`
- `internal/startup/startup.go`
- `internal/skill/skill.go`
- `internal/agent/context.go`

- [ ] **Step 1: Update config.go — data directory and config file name**

In `internal/config/config.go`, find and replace:

```go
// Old:
return ".goclaw"
// becomes:
return ".felix"

// Old:
return filepath.Join(home, ".goclaw")
// becomes:
return filepath.Join(home, ".felix")

// Old:
return filepath.Join(DefaultDataDir(), "goclaw.json5")
// becomes:
return filepath.Join(DefaultDataDir(), "felix.json5")
```

Run:
```bash
sed -i '' 's/\.goclaw/\.felix/g; s/goclaw\.json5/felix.json5/g' internal/config/config.go
```

- [ ] **Step 2: Update config test files**

```bash
sed -i '' 's/goclaw\.json5/felix.json5/g' internal/config/config_test.go internal/config/watcher_test.go
```

- [ ] **Step 3: Update Prometheus metric names in metrics.go**

```bash
sed -i '' 's/goclaw_/felix_/g' internal/gateway/metrics.go
```

- [ ] **Step 4: Update cmd/felix/main.go — binary name strings and temp file prefix**

```bash
sed -i '' \
  's/"goclaw"/"felix"/g; \
   s/goclaw-screenshot/felix-screenshot/g; \
   s/goclaw start/felix start/g; \
   s/goclaw chat/felix chat/g' \
  cmd/felix/main.go
```

Then manually verify that `Use: "felix"` and all help text now says `felix` — open the file and scan:

```bash
grep -n "goclaw\|GoClaw" cmd/felix/main.go
```

Expected: no output. Fix any remaining occurrences manually.

- [ ] **Step 5: Update cmd/felix-app/main.go — log file name**

```bash
sed -i '' 's/goclaw-app\.log/felix-app.log/g' cmd/felix-app/main.go
```

- [ ] **Step 6: Update channel and other internal strings**

```bash
sed -i '' "s/'goclaw start'/'felix start'/g; s/goclaw start/felix start/g" \
  internal/channel/whatsapp.go \
  internal/channel/telegram.go

sed -i '' 's|cmd/goclaw|cmd/felix|g' internal/startup/startup.go
sed -i '' 's|~/\.goclaw|~/\.felix|g' internal/skill/skill.go
```

- [ ] **Step 7: Scan for any remaining "goclaw" in Go files**

```bash
grep -rn "goclaw\|GoClaw" --include="*.go" .
```

Fix any remaining occurrences not already handled.

- [ ] **Step 8: Run tests**

```bash
go test ./...
```

Expected: all pass (or same failures as before this rename)

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "rename: update goclaw string literals to felix in source files"
```

---

### Task 4: Update macOS .app bundle files

**Files:** `cmd/felix-app/Info.plist`, `GoClaw.app/` directory

- [ ] **Step 1: Update Info.plist**

Edit `cmd/felix-app/Info.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key>
	<string>felix-app</string>
	<key>CFBundleIdentifier</key>
	<string>com.felix.app</string>
	<key>CFBundleName</key>
	<string>Felix</string>
	<key>CFBundleDisplayName</key>
	<string>Felix</string>
	<key>CFBundleVersion</key>
	<string>1.0.0</string>
	<key>CFBundleShortVersionString</key>
	<string>1.0.0</string>
	<key>CFBundleIconFile</key>
	<string>icon</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>LSUIElement</key>
	<true/>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
</dict>
</plist>
```

- [ ] **Step 2: Rename the checked-in GoClaw.app bundle directory**

```bash
git mv GoClaw.app Felix.app
```

- [ ] **Step 3: Update the binary inside the bundle**

The binary inside the app bundle is named `goclaw-app`. Rename it:

```bash
git mv Felix.app/Contents/MacOS/goclaw-app Felix.app/Contents/MacOS/felix-app
```

Update the `Info.plist` inside the bundle too (it is a copy — update it to match):

```bash
sed -i '' \
  's/goclaw-app/felix-app/g; \
   s/com\.goclaw\.app/com.felix.app/g; \
   s/GoClaw/Felix/g' \
  Felix.app/Contents/Info.plist
```

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "rename: update Info.plist and GoClaw.app → Felix.app bundle"
```

---

### Task 5: Update Makefile

**File:** `Makefile`

- [ ] **Step 1: Apply all renames in Makefile**

```bash
sed -i '' \
  's|BINARY    := goclaw|BINARY    := felix|g; \
   s|CMD       := ./cmd/goclaw|CMD       := ./cmd/felix|g; \
   s|./cmd/goclaw-app|./cmd/felix-app|g; \
   s|goclaw-app\.exe|felix-app.exe|g; \
   s|goclaw-app\.log|felix-app.log|g; \
   s| goclaw-app | felix-app |g; \
   s|/goclaw-app|/felix-app|g; \
   s|GoClaw\.app|Felix.app|g; \
   s|GoClaw-|Felix-|g; \
   s|GoClaw\.app|Felix.app|g' \
  Makefile
```

- [ ] **Step 2: Verify Makefile has no remaining goclaw references**

```bash
grep -n "goclaw\|GoClaw" Makefile
```

Fix any remaining occurrences manually.

- [ ] **Step 3: Test the build target**

```bash
make build
ls -la felix
```

Expected: `felix` binary exists

- [ ] **Step 4: Commit**

```bash
git add Makefile
git commit -m "rename: update Makefile for felix binary and Felix.app"
```

---

### Task 6: Update .goreleaser.yaml

**File:** `.goreleaser.yaml`

- [ ] **Step 1: Apply renames**

```bash
sed -i '' \
  's|project_name: goclaw|project_name: felix|g; \
   s|main: ./cmd/goclaw|main: ./cmd/felix|g; \
   s|binary: goclaw|binary: felix|g; \
   s|name: goclaw|name: felix|g' \
  .goreleaser.yaml
```

- [ ] **Step 2: Verify**

```bash
grep -n "goclaw\|GoClaw" .goreleaser.yaml
```

Expected: no output.

- [ ] **Step 3: Commit**

```bash
git add .goreleaser.yaml
git commit -m "rename: update .goreleaser.yaml for felix project"
```

---

### Task 7: Update documentation

**Files:** `README.md`, `howtouse.md`, `CLAUDE.md`

- [ ] **Step 1: Update README.md**

```bash
sed -i '' \
  's/GoClaw/Felix/g; \
   s/goclaw/felix/g' \
  README.md
```

Verify the image reference updated correctly (the `.jpg` file is named `goclaw.jpg` — keep the filename the same or rename it):

```bash
grep -n "felix\|goclaw" README.md | head -10
```

Note: `goclaw.jpg` is an image file. Either rename it to `felix.jpg` and update the reference, or leave the filename and just update the alt text/title. For consistency, rename it:

```bash
git mv goclaw.jpg felix.jpg
sed -i '' 's/goclaw\.jpg/felix.jpg/g' README.md
```

- [ ] **Step 2: Update howtouse.md**

```bash
sed -i '' \
  's/GoClaw/Felix/g; \
   s/goclaw/felix/g' \
  howtouse.md
```

```bash
grep -n "goclaw\|GoClaw" howtouse.md
```

Expected: no output.

- [ ] **Step 3: Update CLAUDE.md**

```bash
sed -i '' \
  's/GoClaw/Felix/g; \
   s/goclaw/felix/g' \
  CLAUDE.md
```

```bash
grep -n "goclaw\|GoClaw" CLAUDE.md
```

Fix any remaining occurrences (e.g., module path `github.com/sausheong/felix` should already be correct from Task 1).

- [ ] **Step 4: Full scan for any remaining occurrences in the repo**

```bash
grep -rn "goclaw\|GoClaw" . \
  --exclude-dir=".git" \
  --exclude="*.sum" \
  --exclude="*.mod"
```

Fix any remaining occurrences found.

- [ ] **Step 5: Commit**

```bash
git add README.md howtouse.md CLAUDE.md felix.jpg
git commit -m "rename: update documentation from goclaw to felix"
```

---

### Task 8: Final verification

- [ ] **Step 1: Clean build**

```bash
make clean
make build
./felix version
```

Expected: prints version and commit info; binary is named `felix`

- [ ] **Step 2: Run all tests**

```bash
go test ./...
```

Expected: all pass

- [ ] **Step 3: Final scan — zero goclaw references**

```bash
grep -rn "goclaw\|GoClaw" . \
  --exclude-dir=".git" \
  --exclude="*.sum"
```

Expected: no output (or only `go.mod` replace directive `../cortex` which is unrelated). Fix anything found.

- [ ] **Step 4: Commit final state**

```bash
git add -A
git commit -m "rename: final cleanup — project is now felix"
```
