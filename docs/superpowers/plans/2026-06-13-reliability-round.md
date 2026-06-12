# Reliability Round Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix six reliability bugs that trigger in normal operation — cron shutdown hang, cron-job duplication, hot-reload stripping tool grants, stale providers after reload, a run-log race under supersede, and stream-delivered provider errors bypassing recovery.

**Architecture:** Three groups. Group 1 (cron lifecycle) makes `Scheduler.Start` idempotent and tags jobs static/dynamic. Group 2 (hot-reload) extracts an auto-add helper called from both startup and reload, and routes providers through an atomic holder. Group 3 (concurrency) tightens run-log lock discipline and adds pre-first-token stream-error recovery in the harness runtime.

**Tech Stack:** Go 1.25, `stretchr/testify`, `log/slog`, `sync/atomic`. Two repos: `felix` (`/Users/sausheong/projects/felix`) and `harness` (`/Users/sausheong/projects/harness`, wired via `go.mod replace`).

**Spec:** `docs/superpowers/specs/2026-06-13-reliability-round-design.md`

**Conventions:**
- Tests use `testify` (`require`/`assert`), matching the existing suites.
- Commit messages omit any Co-Authored-By trailer.
- After any harness change, run `cd /Users/sausheong/projects/felix && go build ./...` to confirm the replace-wired build still compiles.

---

## File Structure

**Group 1 — Cron lifecycle (felix)**
- Modify: `internal/cron/cron.go` — idempotent `Start`; `Job.Source`; dup-name rejection in `Add`
- Modify: `internal/cron/cron_test.go` — R4 + R6 tests
- Modify: `internal/startup/startup.go` — set `Source` on static + dynamic adds; `persist()` dynamic-only filter

**Group 2 — Hot-reload (felix)**
- Modify: `internal/startup/startup.go` — `applyAutoAddedAllowlists` helper + both call sites; `providerHolder` + migrate reads/store
- Create: `internal/startup/providerholder_test.go` — holder concurrency test

**Group 3 — Concurrency / resilience**
- Modify: `internal/gateway/runs/registry.go` (felix) — `Run.closed`; lock discipline in `Finish` + `Append`
- Create: `internal/gateway/runs/registry_race_test.go` (felix) — N3 supersede race test
- Modify: `runtime/runtime.go` (harness) — pre-first-token `EventError` recovery
- Create: `runtime/streamrecover_test.go` (harness) — R1 tests

---

## Group 1 — Cron lifecycle

### Task 1: Idempotent `Scheduler.Start` (R4)

**Files:**
- Modify: `internal/cron/cron.go:68-84` (`Start`)
- Modify: `internal/cron/cron_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/cron/cron_test.go`:

```go
func TestSchedulerStartIdempotentNoHang(t *testing.T) {
	s := NewScheduler()
	require.NoError(t, s.Add(Job{
		Name: "j1", Schedule: "1h",
		AgentFn: func(ctx context.Context, p string) (string, error) { return "ok", nil },
	}))

	ctx := context.Background()
	s.Start(ctx) // first generation

	// Add a second job and Start again — the old code would derive a new
	// root context here, orphaning j1's canceller.
	require.NoError(t, s.Add(Job{
		Name: "j2", Schedule: "1h",
		AgentFn: func(ctx context.Context, p string) (string, error) { return "ok", nil },
	}))
	s.Start(ctx) // second generation

	// Both jobs must be running.
	require.Len(t, s.Jobs(), 2)

	// Stop() must return promptly — if Start orphaned a context, wg.Wait()
	// would block forever.
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() hung — Start is not idempotent (orphaned job context)")
	}
}
```

> The test file already imports `context`, `time`, `testing`, and testify. Confirm with a quick read; add any missing import.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/cron/ -run TestSchedulerStartIdempotentNoHang -v`
Expected: FAIL (Stop hangs → test times out / fatals) — this reproduces the R4 hang. (If by luck it passes because both jobs happen to share the latest ctx, it still documents the contract; proceed.)

- [ ] **Step 3: Make `Start` idempotent**

In `internal/cron/cron.go`, replace the `Start` method (lines 68-84):

```go
// Start begins running all scheduled jobs. It is idempotent: the
// scheduler-lifetime root context is created exactly once, on the first
// call. Subsequent calls start any jobs added since the last call against
// that same root, so every job goroutine is a child of one cancellable
// context and Stop() reliably cancels all of them.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel == nil {
		s.ctx, s.cancel = context.WithCancel(ctx)
	}

	for _, job := range s.jobs {
		if _, exists := s.running[job.Name]; exists {
			continue // already running
		}
		s.startJobLocked(s.ctx, job)
	}

	slog.Info("cron scheduler started", "jobs", len(s.running))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/cron/ -run TestSchedulerStartIdempotentNoHang -v`
Expected: PASS.

- [ ] **Step 5: Run full cron package + race**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/cron/ && go test -race ./internal/cron/`
Expected: PASS (all existing cron tests still green).

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/cron/cron.go internal/cron/cron_test.go
git commit -m "fix(cron): idempotent Start so Stop() cancels all jobs (no shutdown hang)"
```

---

### Task 2: `Job.Source` tag + dup-name rejection in `Add` (R6, cron side)

**Files:**
- Modify: `internal/cron/cron.go:18-28` (`Job`), `:54-66` (`Add`)
- Modify: `internal/cron/cron_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/cron/cron_test.go`:

```go
func TestSchedulerAddRejectsDuplicateName(t *testing.T) {
	s := NewScheduler()
	require.NoError(t, s.Add(Job{
		Name: "dup", Schedule: "1h", Source: "static",
		AgentFn: func(ctx context.Context, p string) (string, error) { return "ok", nil },
	}))
	err := s.Add(Job{
		Name: "dup", Schedule: "1h", Source: "dynamic",
		AgentFn: func(ctx context.Context, p string) (string, error) { return "ok", nil },
	})
	require.Error(t, err, "second Add with same name must be rejected")
	require.Len(t, s.Jobs(), 1)
}

func TestSchedulerJobsSurfacesSource(t *testing.T) {
	s := NewScheduler()
	require.NoError(t, s.Add(Job{
		Name: "s1", Schedule: "1h", Source: "static",
		AgentFn: func(ctx context.Context, p string) (string, error) { return "ok", nil },
	}))
	jobs := s.Jobs()
	require.Len(t, jobs, 1)
	require.Equal(t, "static", jobs[0].Source)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/cron/ -run 'TestSchedulerAddRejectsDuplicateName|TestSchedulerJobsSurfacesSource' -v`
Expected: FAIL to compile (`Job` has no field `Source`).

- [ ] **Step 3: Add `Source` to `Job` and dup-rejection to `Add`**

In `internal/cron/cron.go`, add the field to the `Job` struct (after `Paused`, line 24):

```go
	Paused   bool          // if true, the job is paused and not running
	Source   string        // "static" (from config) or "dynamic" (tool-created); blank treated as dynamic
	AgentFn  AgentFunc
```

Replace `Add` (lines 54-66):

```go
// Add registers a new job with the scheduler. Returns an error if the
// schedule is unparseable or a job with the same name already exists
// (names are unique; static config jobs are added before dynamic restores,
// so a colliding dynamic job is rejected — static config wins).
func (s *Scheduler) Add(job Job) error {
	d, err := time.ParseDuration(job.Schedule)
	if err != nil {
		return err
	}
	job.interval = d

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.jobs {
		if s.jobs[i].Name == job.Name {
			return fmt.Errorf("cron job %q already exists", job.Name)
		}
	}
	s.jobs = append(s.jobs, job)
	return nil
}
```

> `Jobs()` already does `copy(result, s.jobs)` (a struct copy), so `Source` flows through to callers automatically — no change needed there. Confirm by reading `Jobs()` at `cron.go:280`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/cron/ -run 'TestSchedulerAddRejectsDuplicateName|TestSchedulerJobsSurfacesSource' -v`
Expected: PASS.

- [ ] **Step 5: Run full cron package**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/cron/`
Expected: PASS. (If any existing test added two jobs with the same name, fix that test to use distinct names — report it.)

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/cron/cron.go internal/cron/cron_test.go
git commit -m "feat(cron): add Job.Source tag and reject duplicate job names"
```

---

### Task 3: Tag static/dynamic adds + persist dynamic-only (R6, startup side)

**Files:**
- Modify: `internal/startup/startup.go:141-148` (`addJobInternal`'s `Add`), `:863-869` (static add), `:212-241` (`persist`)

- [ ] **Step 1: Read the three sites**

Run: `cd /Users/sausheong/projects/felix && sed -n '141,149p;212,241p;863,870p' internal/startup/startup.go`
Confirm: `addJobInternal` builds a `cron.Job{...}` (no `Source`); the static loop builds a `cron.Job{...}` (no `Source`); `persist()` ranges over `a.Scheduler.Jobs()` and appends every job to `out`.

- [ ] **Step 2: Set `Source: "dynamic"` in `addJobInternal`**

In the `cron.Job{...}` literal inside `addJobInternal` (around `:141`), add:

```go
	err := a.Scheduler.Add(cron.Job{
		Name:     name,
		AgentID:  agentID,
		Schedule: schedule,
		Prompt:   prompt,
		Source:   "dynamic",
		AgentFn:  agentFn,
		OutputFn: a.OutputFn,
	})
```

- [ ] **Step 3: Set `Source: "static"` in the startup static loop**

In the static `cronScheduler.Add(cron.Job{...})` literal (around `:863`), add:

```go
			cronScheduler.Add(cron.Job{
				Name:     cronJob.Name,
				AgentID:  agentCfg.ID,
				Schedule: cronJob.Schedule,
				Prompt:   cronJob.Prompt,
				Source:   "static",
				AgentFn:  buildCronAgentFn(agentCfg, cronJob.Name),
			})
```

> Note: with Task 2's dup-name rejection, `cronScheduler.Add` now returns an error worth not ignoring silently. If the current code ignores the return, add minimal handling: `if err := cronScheduler.Add(...); err != nil { slog.Warn("skip duplicate static cron job", "name", cronJob.Name, "error", err) }`. Match the surrounding style; keep it non-fatal.

- [ ] **Step 4: Filter `persist()` to dynamic-only**

In `persist()` (around `:217-226`), change the range loop to skip static jobs:

```go
	jobs := a.Scheduler.Jobs()
	out := make([]persistedJob, 0, len(jobs))
	for _, j := range jobs {
		if j.Source == "static" {
			continue // static jobs live in felix.json5; don't persist them
		}
		out = append(out, persistedJob{
			Name:     j.Name,
			AgentID:  j.AgentID,
			Schedule: j.Schedule,
			Prompt:   j.Prompt,
			Paused:   j.Paused,
		})
	}
```

> `Restore`'s `addJobInternal(..., persist:false)` already tags restored jobs `"dynamic"` (via Task 3 Step 2), so the round-trip stays dynamic. No `persistedJob` field change needed.

- [ ] **Step 5: Build + verify**

Run: `cd /Users/sausheong/projects/felix && go build ./... && go vet ./internal/startup/ && go test ./internal/startup/`
Expected: clean build, vet clean, startup tests pass.

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/startup/startup.go
git commit -m "feat(startup): tag cron jobs static/dynamic; persist only dynamic jobs (no duplicate accrual)"
```

---

## Group 2 — Hot-reload correctness

### Task 4: `applyAutoAddedAllowlists` helper + both call sites (R2)

**Files:**
- Modify: `internal/startup/startup.go:512-528` (startup auto-adds), `:675-697` (watcher callback)

- [ ] **Step 1: Read both sites**

Run: `cd /Users/sausheong/projects/felix && sed -n '512,535p;675,700p' internal/startup/startup.go`
Confirm: startup calls `cfg.ApplyMCPToolNamesToAllowlists(mcpNames)`, `cfg.ApplyTaskToolToAllowlists()`, and (if `cfg.Cortex.Enabled`) `cfg.ApplyCortexToolNamesToAllowlists([]string{"recall","remember","find_entities","get_relationships"})`. The watcher callback calls `wsHandler.SetPermission(newCfg.BuildPermissionChecker())` WITHOUT those three.

- [ ] **Step 2: Add the helper + a shared cortex-names var**

Add near the top of the file's other helpers (package scope), the canonical cortex tool list and the helper:

```go
// cortexAutoAddToolNames are the cortex tool names auto-added to every
// agent's allowlist when cortex is enabled. Shared by the startup and
// hot-reload paths so they can't drift.
var cortexAutoAddToolNames = []string{"recall", "remember", "find_entities", "get_relationships"}

// applyAutoAddedAllowlists augments every agent's Tools.Allow with the tool
// names added at runtime: MCP-discovered tools, the task tool, and (when
// cortex is enabled) the cortex tools. This MUST run before
// BuildPermissionChecker on BOTH the startup path and the hot-reload path —
// otherwise a curated Tools.Allow silently loses these grants after the
// first config edit.
func applyAutoAddedAllowlists(cfg *config.Config, mcpNames []string) {
	cfg.ApplyMCPToolNamesToAllowlists(mcpNames)
	cfg.ApplyTaskToolToAllowlists()
	if cfg.Cortex.Enabled {
		cfg.ApplyCortexToolNamesToAllowlists(cortexAutoAddToolNames)
	}
}
```

- [ ] **Step 3: Replace the startup auto-add block with a call**

At `:519-528`, replace the three inline calls with:

```go
	applyAutoAddedAllowlists(cfg, mcpNames)
```

(Delete the now-inlined `ApplyMCPToolNamesToAllowlists` / `ApplyTaskToolToAllowlists` / cortex `if` block — they're inside the helper now. Keep `mcpNames` defined above.)

- [ ] **Step 4: Call the helper in the watcher callback**

In the watcher callback (before `wsHandler.SetPermission(newCfg.BuildPermissionChecker())` at `:681`), add:

```go
			applyAutoAddedAllowlists(newCfg, mcpNames)
			wsHandler.SetPermission(newCfg.BuildPermissionChecker())
```

> `mcpNames` is in scope at startup where the watcher closure is defined; confirm the closure can capture it (it's a `[]string` local from `mcp.RegisterTools`). If the watcher is set up before `mcpNames` exists, move the `NewWatcher` registration after `mcpNames` is assigned, or capture it explicitly. Verify by build.

- [ ] **Step 5: Build + verify**

Run: `cd /Users/sausheong/projects/felix && go build ./... && go vet ./internal/startup/ && go test ./internal/startup/`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/startup/startup.go
git commit -m "fix(startup): re-apply MCP/task/cortex auto-added allowlists on hot reload (R2)"
```

---

### Task 5: `providerHolder` + migrate provider reads (R5)

**Files:**
- Modify: `internal/startup/startup.go:450` (construct), `:741` (subagent read), `:807` (cron read), `:857` (static-add guard), `:683` (reload store)
- Create: `internal/startup/providerholder_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/startup/providerholder_test.go`:

```go
package startup

import (
	"sync"
	"testing"

	"github.com/sausheong/felix/internal/llm"
	"github.com/stretchr/testify/require"
)

func TestProviderHolderStoreLoad(t *testing.T) {
	h := newProviderHolder(map[string]llm.LLMProvider{})
	_, ok := h.get("openai")
	require.False(t, ok)

	// Swap in a map that has the provider.
	h.store(map[string]llm.LLMProvider{"openai": nil}) // nil value is fine; we test key presence
	_, ok = h.get("openai")
	require.True(t, ok, "reader must see the post-store map")
}

func TestProviderHolderConcurrent(t *testing.T) {
	h := newProviderHolder(map[string]llm.LLMProvider{"a": nil})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_, _ = h.get("a")
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				h.store(map[string]llm.LLMProvider{"a": nil})
			}
		}()
	}
	wg.Wait()
}
```

> Confirm the felix-internal LLM provider interface import path is `github.com/sausheong/felix/internal/llm` and the type is `llm.LLMProvider` (grep an existing startup file). Adjust if different.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/startup/ -run TestProviderHolder -v`
Expected: FAIL (`undefined: newProviderHolder`).

- [ ] **Step 3: Add the holder type**

Add to `internal/startup/startup.go` (package scope; ensure `sync/atomic` is imported):

```go
// providerHolder is an atomically-swappable provider map shared by the
// WebSocket handler, the cron agent factory, and the subagent factory, so a
// hot reload that rebuilds providers is visible to all of them at once
// (rotated/revoked keys take effect everywhere, not just on the WS path).
type providerHolder struct {
	p atomic.Pointer[map[string]llm.LLMProvider]
}

func newProviderHolder(m map[string]llm.LLMProvider) *providerHolder {
	h := &providerHolder{}
	h.p.Store(&m)
	return h
}

func (h *providerHolder) get(name string) (llm.LLMProvider, bool) {
	m := *h.p.Load()
	p, ok := m[name]
	return p, ok
}

func (h *providerHolder) store(m map[string]llm.LLMProvider) { h.p.Store(&m) }
```

- [ ] **Step 4: Construct the holder and migrate reads**

At `:450`, after `providers := InitProviders(cfg)`, add:

```go
	providers := InitProviders(cfg)
	providerHldr := newProviderHolder(providers)
```

Migrate the three read sites:
- `buildSubagentInputs` (`:741`): `p, ok := providers[pName]` → `p, ok := providerHldr.get(pName)`.
- `buildCronAgentFn` (`:807`): `p, ok := providers[pName]` → `p, ok := providerHldr.get(pName)`.
- static-add guard (`:857`): `if _, ok := providers[providerName]; !ok` → `if _, ok := providerHldr.get(providerName); !ok`.

> Leave the original `providers` map in place for any code that still needs it directly (e.g. the initial static-cron loop reads it before the holder is used, and `wsHandler` init). The holder reads the same underlying map until the first `store`.

- [ ] **Step 5: Store rebuilt providers in the reload callback**

In the watcher callback where `newProviders := InitProviders(newCfg)` is computed (~`:677`), add a `providerHldr.store(newProviders)` alongside the existing `wsHandler.UpdateProviders(newProviders)`:

```go
			newProviders := InitProviders(newCfg)
			providerHldr.store(newProviders)
			...
			wsHandler.UpdateProviders(newProviders)
```

> `providerHldr` is captured by the watcher closure (defined at startup scope, same as `wsHandler`). Confirm scope by build.

- [ ] **Step 6: Run test + build + race**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/startup/ -run TestProviderHolder -v && go build ./... && go test -race ./internal/startup/`
Expected: PASS / clean / no race.

- [ ] **Step 7: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/startup/startup.go internal/startup/providerholder_test.go
git commit -m "fix(startup): route cron/subagent providers through atomic holder so reload is visible (R5)"
```

---

## Group 3 — Concurrency / resilience

### Task 6: Run-log lock discipline under supersede (N3)

**Files:**
- Modify: `internal/gateway/runs/registry.go:36-49` (`Run` struct), `:247-265` (`Append`), `:269-310` (`Finish`)
- Create: `internal/gateway/runs/registry_race_test.go`

- [ ] **Step 1: Read `Append` and `Finish` in full**

Run: `cd /Users/sausheong/projects/felix && sed -n '247,310p' internal/gateway/runs/registry.go`
Confirm the exact current bodies before editing (the index-write + `r.fanout(e)` + `closeAllSubscribers()` ordering in `Finish`, and the early `r.Completed.Load()` check in `Append`).

- [ ] **Step 2: Write the failing race test**

Create `internal/gateway/runs/registry_race_test.go`:

```go
package runs

import (
	"sync"
	"testing"
)

// TestAppendFinishRaceUnderSupersede drives the supersede interleaving: one
// goroutine hammers Append (the old run's drain) while another calls Finish
// (the new turn superseding it). With -race this must report no data race and
// must not panic on a write to the closed log.
func TestAppendFinishRaceUnderSupersede(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)
	// Create signature is Create(scope, runID, cancel); pass an explicit
	// runID and a nil cancel (verified against registry.go:75 and the
	// existing integration_test.go usage).
	run, err := reg.Create(SessionScope{AgentID: "a", SessionKey: "k"}, "r1", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			// Append may legitimately fail once the run is finished; that's
			// fine. The point is it must never race or write post-close.
			_, _ = run.Append(EventTypeTextDelta, []byte(`{"text":"x"}`))
		}
	}()

	// Finish concurrently — mirrors the supersede path
	// (Finish(StatusCancelled, ReasonSuperseded, ...) as in integration_test.go:110).
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = run.Finish(StatusCancelled, ReasonSuperseded, "r2")
	}()

	wg.Wait()
}
```

> Symbols verified against the package: `NewRegistry(dir)`, `Create(scope, runID, cancel)`
> (registry.go:75), `SessionScope{AgentID, SessionKey}`, `EventTypeTextDelta`,
> `StatusCancelled`, `ReasonSuperseded` (all used in `integration_test.go`). The payload must be
> valid JSON (`json.RawMessage`-shaped) — existing tests pass `[]byte(`{"text":"x"}`)`. The
> load-bearing part is concurrent `Append` + `Finish`.

- [ ] **Step 3: Run test under -race to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test -race ./internal/gateway/runs/ -run TestAppendFinishRaceUnderSupersede -v`
Expected: FAIL — `-race` reports a data race on the log writer / `bufio.Writer`, or a panic writing to a closed file. (If it's flaky, run a few times; the window is real but timing-dependent.)

- [ ] **Step 4: Add `closed` flag and fix the lock discipline**

In `internal/gateway/runs/registry.go`, add to the `Run` struct (in the mutex-guarded section, after `log`):

```go
	mu          sync.Mutex
	log         *logWriter
	closed      bool // true after Finish closes log; guarded by mu
	subscribers map[*websocket.Conn]*subscriber
```

Rewrite `Append` so the completion/closed check happens **after** acquiring `r.mu`:

```go
func (r *Run) Append(t EventType, payload []byte) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Completed.Load() || r.closed {
		return 0, fmt.Errorf("run %s already completed", r.ID)
	}
	seq := r.LastSeq.Add(1)
	e := Event{
		Seq:     seq,
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Type:    t,
		Payload: payload,
	}
	if err := r.log.Append(e); err != nil {
		return seq, err
	}
	r.fanoutLocked(e)
	return seq, nil
}
```

In `Finish`, hold `r.mu` ONLY around the terminal log append + close + `closed = true`, then release it before the index write and fanout (preserving the existing "persist index before fanout" ordering and keeping disk I/O off the lock). The terminal-event build and `LastSeq.Add` move inside the lock:

```go
func (r *Run) Finish(status Status, reason CancelReason, supersededBy string) error {
	if !r.Completed.CompareAndSwap(false, true) {
		return nil
	}
	close(r.done)

	r.mu.Lock()
	seq := r.LastSeq.Add(1)
	e := Event{
		Seq:          seq,
		Ts:           time.Now().UTC().Format(time.RFC3339Nano),
		Type:         EventTypeDone,
		Status:       status,
		Reason:       reason,
		SupersededBy: supersededBy,
	}
	logErr := r.log.Append(e)
	_ = r.log.Close()
	r.closed = true
	r.mu.Unlock()

	// Persist terminal index BEFORE notifying subscribers (unchanged ordering).
	idx, _ := loadIndex(r.indexPath)
	idx.Upsert(RunSummary{
		ID:           r.ID,
		StartedAt:    r.StartedAt.Format(time.RFC3339Nano),
		EndedAt:      e.Ts,
		Status:       status,
		LastSeq:      seq,
		SupersededBy: supersededBy,
	})
	saveErr := saveIndex(r.indexPath, idx)

	r.fanout(e) // re-acquires r.mu internally; unchanged
	r.closeAllSubscribers()

	if saveErr != nil {
		return fmt.Errorf("save terminal index: %w", saveErr)
	}
	return logErr
}
```

> Reproduce the exact field names of `RunSummary` and the surrounding comments from the file you read in Step 1 — the snippet above mirrors the current structure but verify `EventTypeDone`, `RunSummary` fields, `loadIndex`/`saveIndex`, `r.fanout`, `r.closeAllSubscribers` names. The ONLY behavioral changes are: (a) `LastSeq.Add`+event-build+`log.Append`+`log.Close` now under `r.mu`; (b) new `r.closed = true` under the lock; (c) `Append` re-checks under the lock.

- [ ] **Step 5: Run the race test to verify it passes**

Run: `cd /Users/sausheong/projects/felix && go test -race ./internal/gateway/runs/ -run TestAppendFinishRaceUnderSupersede -v`
Expected: PASS, no race.

- [ ] **Step 6: Run full runs package + race**

Run: `cd /Users/sausheong/projects/felix && go test -race ./internal/gateway/runs/`
Expected: PASS (all existing runs tests still green under race).

- [ ] **Step 7: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/gateway/runs/registry.go internal/gateway/runs/registry_race_test.go
git commit -m "fix(runs): close run-log under mutex; Append re-checks closed (N3 supersede race)"
```

---

### Task 7: Pre-first-token stream-error recovery (R1, harness)

**Files:**
- Modify: `/Users/sausheong/projects/harness/runtime/runtime.go` (sync recovery `:456-487`, `EventError` branch `:558-584`)
- Create: `/Users/sausheong/projects/harness/runtime/streamrecover_test.go`

- [ ] **Step 1: Read the recovery + stream-loop region**

Run: `cd /Users/sausheong/projects/harness && sed -n '448,590p' runtime/runtime.go`
Confirm: the sync path (`stream, err := r.LLM.ChatStream`) handles `IsContextOverflow` (compact + reassemble `msgs`/`req` + re-`ChatStream`) and `IsRetryableModelError` (switch `req.Model` to `r.FallbackModel` + re-`ChatStream`). The `EventError` branch only does the non-streaming retry when `gotFirstToken`. Note the locals the recovery mutates: `stream`/`streamSource`, `msgs`, `req`, `history`.

- [ ] **Step 2: Write the failing tests**

Create `/Users/sausheong/projects/harness/runtime/streamrecover_test.go`:

```go
package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/sausheong/harness/llm"
	"github.com/sausheong/harness/llm/llmtest"
	"github.com/sausheong/harness/session"
	"github.com/sausheong/harness/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamErrThenOKProvider emits EventError (no token) on the first stream
// call, then clean text on the second. Used to verify pre-first-token
// recovery (fallback / compaction) retries the turn instead of aborting.
type streamErrThenOKProvider struct {
	llmtest.Base
	calls     atomic.Int64
	streamErr error
	finalText string
}

func (p *streamErrThenOKProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	n := p.calls.Add(1)
	ch := make(chan llm.ChatEvent, 4)
	if n == 1 {
		ch <- llm.ChatEvent{Type: llm.EventError, Error: p.streamErr}
	} else {
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.finalText}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}
	close(ch)
	return ch, nil
}

// A retryable (429) pre-first-token stream error must engage the fallback
// model and retry, recovering with the fallback's output.
func TestStreamErrorRetryableEngagesFallback(t *testing.T) {
	prov := &streamErrThenOKProvider{
		streamErr: errors.New("429 rate limit exceeded"),
		finalText: "RECOVERED_VIA_FALLBACK",
	}
	rt := &Runtime{
		LLM:           prov,
		Tools:         tool.NewRegistry(),
		Session:       session.NewSession("a", "k"),
		AgentID:       "a",
		Model:         "claude-opus-4-8",
		FallbackModel: "claude-sonnet-4-5",
		Provider:      "anthropic",
		MaxTurns:      2,
		Workspace:     t.TempDir(),
	}
	out, err := rt.RunSync(context.Background(), "do it", nil)
	require.NoError(t, err)
	assert.Contains(t, out, "RECOVERED_VIA_FALLBACK")
	assert.EqualValues(t, 2, prov.calls.Load(), "must retry once after the pre-first-token 429")
}

// A non-retryable, non-overflow pre-first-token error must still abort
// (preserve existing behaviour — don't mask real 4xx bugs).
func TestStreamErrorNonRetryableStillAborts(t *testing.T) {
	prov := &streamErrThenOKProvider{
		streamErr: errors.New("400 invalid input"),
		finalText: "SHOULD_NOT_BE_REACHED",
	}
	rt := &Runtime{
		LLM:       prov,
		Tools:     tool.NewRegistry(),
		Session:   session.NewSession("a", "k"),
		AgentID:   "a",
		Model:     "claude-opus-4-8",
		// no FallbackModel
		Provider:  "anthropic",
		MaxTurns:  2,
		Workspace: t.TempDir(),
	}
	_, err := rt.RunSync(context.Background(), "do it", nil)
	require.Error(t, err)
	assert.EqualValues(t, 1, prov.calls.Load(), "non-retryable pre-first-token error must abort, not retry")
}
```

> `out` is `RunSync`'s returned text. Confirm `RunSync`'s signature returns `(string, error)` (the streamfallback_test.go uses it as `_, err := rt.RunSync(...)`). If it returns the text, the `Contains` assert holds; if not, assert on the session's stored assistant message as `streamfallback_test.go` does (lines 87-97). Adjust to match the real signature.

- [ ] **Step 3: Run tests to verify the retryable one fails**

Run: `cd /Users/sausheong/projects/harness && go test ./runtime/ -run 'TestStreamErrorRetryableEngagesFallback|TestStreamErrorNonRetryableStillAborts' -v`
Expected: `TestStreamErrorNonRetryableStillAborts` PASSES already (current code aborts pre-first-token). `TestStreamErrorRetryableEngagesFallback` FAILS (current code aborts instead of engaging the fallback) — this is the R1 bug.

- [ ] **Step 4: Factor the sync recovery into a helper and call it from `EventError`**

This is the careful part. The sync path at `runtime.go:456-487` does compaction-retry and fallback-retry by mutating `stream`, `err`, `msgs`, `req`. Extract a method that, given a classified error, performs the same recovery and returns a fresh stream (or nil if unrecoverable). Add to `runtime.go`:

```go
// recoverFromPreTokenError attempts the same recovery the synchronous
// ChatStream-error path performs, for an error delivered via EventError
// before any token arrived. It returns a new stream to resume from, or nil
// if the error is not recoverable (caller should abort). It may mutate req
// (model swap) and the session (compaction); on compaction it rebuilds and
// reassigns *msgs via the provided closure inputs.
//
// Mirrors the inline logic at the sync call site so the two cannot drift.
func (r *Runtime) recoverFromPreTokenError(
	ctx context.Context,
	err error,
	req *llm.ChatRequest,
	msgs *[]llm.Message,
	spillCfg spillConfig,
) (<-chan llm.ChatEvent, bool) {
	recovered := false
	var stream <-chan llm.ChatEvent

	if compaction.IsContextOverflow(err) && r.Compaction != nil {
		r.emit(AgentEvent{Type: EventCompactionStart})
		res, _ := r.Compaction.MaybeCompact(ctx, r.Session, compaction.ReasonReactive, "")
		if res.Compacted {
			r.emit(AgentEvent{Type: EventCompactionDone, Compaction: &res})
			history := r.Session.View()
			newMsgs := assembleMessages(history)
			pruneToolResults(newMsgs, r.maxToolResultLen(), spillCfg)
			newMsgs = prependPostCompactRestore(newMsgs, r.snapshotTouchedFiles())
			*msgs = newMsgs
			req.Messages = newMsgs
			s, e := r.LLM.ChatStream(ctx, *req)
			if e == nil {
				stream, recovered = s, true
			}
		} else {
			r.emit(AgentEvent{Type: EventCompactionSkipped, Compaction: &res})
		}
	}

	if !recovered && r.FallbackModel != "" && r.FallbackModel != req.Model && llm.IsRetryableModelError(err) {
		slog.Info("llm fallback model engaged (stream error)",
			"agent", r.AgentID, "primary", req.Model, "fallback", r.FallbackModel, "err", err.Error())
		req.Model = r.FallbackModel
		s, e := r.LLM.ChatStream(ctx, *req)
		if e == nil {
			stream, recovered = s, true
		}
	}

	return stream, recovered
}
```

Then in the `EventError` branch (`:558-584`), before the final abort, add the pre-first-token recovery. Insert at the start of the `case llm.EventError:` handling, BEFORE the existing `gotFirstToken && !retriedNonStreaming` non-streaming-retry block:

```go
		case llm.EventError:
			if !gotFirstToken {
				if newStream, ok := r.recoverFromPreTokenError(ctx, event.Error, &req, &msgs, spillCfg); ok {
					drainKickoffs(kickoffs)
					kickoffs = map[string]chan kickoffResult{}
					textContent.Reset()
					toolCalls = nil
					kickoffStopped = false
					streamSource = newStream
					continue streamLoop
				}
			}
			if gotFirstToken && !retriedNonStreaming {
				// ... existing non-streaming retry, unchanged ...
			}
			drainKickoffs(kickoffs)
			stopReason = "error"
			r.emit(AgentEvent{Type: EventError, Error: event.Error})
			return
```

> IMPORTANT constraints:
> - The recovery must respect `MaxTurns` / not loop forever. Because `recoverFromPreTokenError` only retries the *stream* once per `EventError` and a re-failed stream re-enters the branch, add a guard so a persistently-failing stream can't spin: track an int `preTokenRetries` initialized to 0 above `streamLoop`, increment it before calling recovery, and skip recovery once it exceeds 1 (mirrors the single-shot `retriedNonStreaming` flag). Adjust the snippet to gate on `!gotFirstToken && preTokenRetries < 1`.
> - Verify the exact local names (`req` is a value `llm.ChatRequest`, so pass `&req`; `msgs` is `[]llm.Message`, pass `&msgs`; `spillCfg` is in scope). Match the real types from Step 1.
> - Keep `TestStreamErrorNonRetryableStillAborts` green: a `400 invalid input` with no fallback is neither overflow nor retryable, so `recoverFromPreTokenError` returns `false` and the code falls through to abort.

- [ ] **Step 5: Run the R1 tests to verify they pass**

Run: `cd /Users/sausheong/projects/harness && go test ./runtime/ -run 'TestStreamErrorRetryableEngagesFallback|TestStreamErrorNonRetryableStillAborts' -v`
Expected: both PASS.

- [ ] **Step 6: Run the full runtime package (incl. existing stream-fallback tests)**

Run: `cd /Users/sausheong/projects/harness && go test ./runtime/ && go test -race ./runtime/`
Expected: PASS — especially `TestRuntimeStreamFallbackSkippedWhenNoTokenReceived` (pre-flight 400 still aborts) and `TestRuntimeStreamFallbackDiscardsPartialAndRetries` (mid-stream non-streaming retry) must remain green.

- [ ] **Step 7: Verify Felix builds against the modified harness**

Run: `cd /Users/sausheong/projects/felix && go build ./...`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
cd /Users/sausheong/projects/harness
git add runtime/runtime.go runtime/streamrecover_test.go
git commit -m "fix(runtime): recover from pre-first-token stream errors (compaction/fallback) — R1"
```

---

## Final verification

### Task 8: Full test + race + vet across both repos

- [ ] **Step 1: harness**

Run: `cd /Users/sausheong/projects/harness && go build ./... && go test ./... && go vet ./... && go test -race ./runtime/`
Expected: clean build, all tests pass, vet clean, no races.

- [ ] **Step 2: felix**

Run: `cd /Users/sausheong/projects/felix && go build ./... && go test ./... && go vet ./...`
Expected: clean build, all tests pass, vet clean.

- [ ] **Step 3: felix race on touched packages**

Run: `cd /Users/sausheong/projects/felix && go test -race ./internal/cron/ ./internal/startup/ ./internal/gateway/runs/`
Expected: PASS, no races.

---

## Self-Review Notes (coverage map)

| Spec section | Finding | Task |
|--------------|---------|------|
| §3.1 | R4 idempotent Start | Task 1 |
| §3.2 | R6 job tagging + dup rejection | Task 2 (cron), Task 3 (startup) |
| §4.1 | R2 re-apply auto-adds | Task 4 |
| §4.2 | R5 provider holder | Task 5 |
| §5.1 | N3 run-log lock discipline | Task 6 |
| §5.2 | R1 stream-error recovery | Task 7 |
| §6 | testing | per-task + Task 8 |

All six findings map to tasks. Deferred items (R3 config-identity refactor, wsHandler-onto-holder, R7–R10, performance, remaining security) are intentionally absent per spec §8.
