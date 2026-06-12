# Reliability Round Hardening — Design

**Date:** 2026-06-13
**Status:** Design (pending implementation plan)
**Scope:** Execution-order step 3 from `optimisation.md` — "reliability that can wedge the
process." Six findings: R4, R6, R2, R5 (Felix), N3 (Felix), R1 (harness).
**Repos touched:** `felix` and `harness` (R1 only; wired via `go.mod replace`).

---

## 1. Goal & motivation

These are bugs that trigger in **normal operation**, not under attack:

- Edit `felix.json5` while running → every agent with a curated `Tools.Allow` list silently
  loses its MCP / `task` / cortex tools until restart (**R2**), and cron jobs / subagents keep
  using the pre-edit provider map, so a rotated/revoked API key keeps being used (**R5**).
- Run with static cron jobs + any dynamic job, then restart → `Stop()` hangs forever, blocking
  graceful shutdown and OTel flush; felix-app SIGKILLs after 15s (**R4**). Separately, static
  config jobs leak into `cron-jobs.json` and duplicate across restarts (**R6**).
- Hit send twice quickly (supersede) → the superseded run's drain goroutine can write to a
  closed log file / race the `bufio.Writer` (**N3**).
- The primary provider (Anthropic) returns a 429 / context-overflow *after* the stream opens →
  reactive compaction and fallback-model retry never fire, because they only run on a
  synchronous `ChatStream` error (**R1**).

All six were re-verified against `main` on 2026-06-13 (post round-1). R3 (the watcher's
inode-watch + debounce-stop halves) is already fixed; this round does **not** revisit it.

---

## 2. Architecture: three groups, one spec

| Group | Findings | Files |
|-------|----------|-------|
| 1. Cron lifecycle | R4, R6 | `internal/cron/cron.go`, `internal/startup/startup.go` |
| 2. Hot-reload correctness | R2, R5 | `internal/startup/startup.go` |
| 3. Concurrency / resilience | N3, R1 | `internal/gateway/runs/registry.go` (Felix); `runtime/runtime.go` (harness) |

The findings within a group share files (R4/R6 both in cron; R2/R5 both in the watcher
callback + the closures that read providers), so grouping avoids touching the same code twice.
N3 and R1 are independent of each other and of Groups 1–2.

---

## 3. Group 1 — Cron lifecycle (R4, R6)

### 3.1 R4 — idempotent `Scheduler.Start`

**Where:** `internal/cron/cron.go:68-84` (`Start`), `:38-45` (`Scheduler` struct),
`internal/startup/startup.go:127-157` (`addJobInternal` calls `a.Scheduler.Start(a.Ctx)` after
**every** `Add`), `:895` (startup `cronScheduler.Start(ctx)`).

**Problem:** `Start` unconditionally does `ctx, cancel := context.WithCancel(ctx); s.ctx = ctx;
s.cancel = cancel` every call. Each call derives a *fresh* root context and overwrites the
canceller. Jobs started under an earlier generation are children of an earlier root whose
cancel is now unreachable. `Stop()` cancels only the latest `s.cancel`, then `wg.Wait()` blocks
forever on the older-generation goroutines.

**Fix:** make the root context init happen exactly once:

```go
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel == nil { // establish the scheduler-lifetime root once
		s.ctx, s.cancel = context.WithCancel(ctx)
	}
	for _, job := range s.jobs {
		if _, exists := s.running[job.Name]; exists {
			continue
		}
		s.startJobLocked(s.ctx, job)
	}
	slog.Info("cron scheduler started", "jobs", len(s.running))
}
```

Every job is now a child of the single root `s.ctx`, so `Stop()`'s one `s.cancel()` reaches all
of them and `wg.Wait()` returns. `addJobInternal` and startup can keep calling `Start` freely
(it becomes "start any not-yet-running jobs").

### 3.2 R6 — tag jobs static/dynamic; persist only dynamic; reject dup names

**Where:** `internal/cron/cron.go:18-28` (`Job`), `:54-66` (`Add`),
`internal/startup/startup.go:117-125` (`persistedJob`), `:212-241` (`persist`), `:863`
(static `cronScheduler.Add`), `:127-158` (`addJobInternal`).

**Problem:** `persist()` serializes **all** `Scheduler.Jobs()` including static felix.json5
jobs. After any dynamic mutation triggers a persist, static jobs land in `cron-jobs.json`; next
startup adds them from config *and* `Restore` re-adds them → duplicates grow by one per static
job per restart-with-mutation. `Add` never rejects duplicate names, so a tool-created job can
shadow a static one.

**Fix:**
1. Add `Source string` to `cron.Job` (values `"static"` / `"dynamic"`).
2. Startup's static `cronScheduler.Add(cron.Job{...})` (`startup.go:863`) sets `Source:
   "static"`. `addJobInternal` (`startup.go:141`) sets `Source: "dynamic"`.
3. `Scheduler.Add` rejects a duplicate name:
   ```go
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
4. `Scheduler.Jobs()` must surface `Source` (copy it through in the result like `Paused`).
5. `persist()` (`startup.go:212-241`) filters to `j.Source == "dynamic"` before serializing.
6. `Job.Source` is propagated to `persistedJob` only implicitly — persisted jobs are dynamic by
   definition, so `Restore`'s `addJobInternal(..., persist:false)` already tags them
   `"dynamic"`. No new `persistedJob` field needed.

**Dup-name interaction:** with `Add` now rejecting duplicates, the startup ordering (static
added first at `:863`, then `Restore` at `:887`) means a persisted dynamic job whose name
collides with a static one is rejected on restore — logged and skipped, not fatal (Restore
already logs+skips per-entry errors). This is the correct precedence: static config wins.

---

## 4. Group 2 — Hot-reload correctness (R2, R5)

### 4.1 R2 — re-apply auto-added allowlists on reload

**Where:** `internal/startup/startup.go:519-528` (startup applies MCP/task/cortex auto-adds),
`:675-697` (watcher callback rebuilds the permission checker **without** re-applying them).

**Problem:** the auto-add augmentation is in-memory only. The reload callback calls
`newCfg.BuildPermissionChecker()` on a fresh config that has *not* had the auto-adds applied, so
every agent with a curated `Tools.Allow` list loses all MCP tools, `task`, and cortex tools
until restart.

**Fix:** extract the three calls into one helper and call it from both paths:

```go
// applyAutoAddedAllowlists augments every agent's Tools.Allow with the tool
// names that are added at runtime (MCP-discovered tools, the task tool, and
// cortex tools when enabled). Must run before BuildPermissionChecker on BOTH
// the startup path and the hot-reload path, or curated allowlists silently
// lose these grants after the first config edit.
func applyAutoAddedAllowlists(cfg *config.Config, mcpNames, cortexNames []string) {
	cfg.ApplyMCPToolNamesToAllowlists(mcpNames)
	cfg.ApplyTaskToolToAllowlists()
	if cfg.Cortex.Enabled {
		cfg.ApplyCortexToolNamesToAllowlists(cortexNames)
	}
}
```

- Startup (`:519-528`) calls `applyAutoAddedAllowlists(cfg, mcpNames, cortexToolNames)` where
  `cortexToolNames = []string{"recall", "remember", "find_entities", "get_relationships"}`
  (hoisted to a package var or local so both call sites share the exact list).
- The watcher callback calls `applyAutoAddedAllowlists(newCfg, mcpNames, cortexToolNames)`
  **before** `wsHandler.SetPermission(newCfg.BuildPermissionChecker())`. `mcpNames` and
  `cortexToolNames` are captured by the closure (they don't change without a restart — MCP
  connections aren't hot-reloaded, per the existing design note).

### 4.2 R5 — shared provider holder

**Where:** `internal/startup/startup.go:450` (`providers := InitProviders(cfg)`), `:741`
(subagent factory reads `providers[pName]`), `:807` (cron `buildCronAgentFn` reads
`providers[pName]`), `:683` (reload pushes new providers only into `wsHandler` via
`UpdateProviders`).

**Problem:** on reload only `wsHandler` gets the rebuilt providers. The cron and subagent
closures captured the startup-time `providers` map by reference and keep using it — rotated /
revoked keys keep being used; a newly added provider is invisible until restart.

**Fix:** a small lock-free holder shared by all readers and the reload callback.

```go
// providerHolder is an atomically-swappable provider map shared by the
// WebSocket handler, the cron agent factory, and the subagent factory, so a
// hot reload that rebuilds providers is visible everywhere at once.
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

- Construct `holder := newProviderHolder(InitProviders(cfg))` at `:450`.
- `buildSubagentInputs` (`:741`) and `buildCronAgentFn` (`:807`) read via
  `holder.get(pName)` instead of `providers[pName]`.
- The static cron-add guard at `:857` (`if _, ok := providers[providerName]; !ok`) reads via
  `holder.get`.
- The watcher callback replaces `wsHandler.UpdateProviders(newProviders)` semantics with
  `holder.store(newProviders)` and still calls `wsHandler.UpdateProviders(newProviders)` if the
  WebSocket handler keeps its own reference — OR, preferably, `wsHandler` is given the holder so
  there is a single source. **Decision:** keep `wsHandler.UpdateProviders` as-is (its internal
  field) AND add `holder.store(newProviders)` for the closures; the two are updated together in
  the callback. (Folding `wsHandler` onto the holder is a larger change deferred to avoid
  touching the WS handler's provider plumbing this round.)

> **Scope note:** R5 fixes the *provider staleness* in the closures. The broader R3 "single
> canonical `*Config` identity / `UpdateFrom` map aliasing" refactor is **out of scope** — only
> the provider readers are migrated to the holder.

---

## 5. Group 3 — Concurrency / resilience (N3, R1)

### 5.1 N3 — run-log writer race under supersede

**Where:** `internal/gateway/runs/registry.go:36-49` (`Run` struct), `:247-265` (`Append`
checks `r.Completed.Load()` *outside* `r.mu`), `:269-310` (`Finish` does `LastSeq.Add` +
`log.Append` + `log.Close` with only the `Completed` CAS guarding it, **not** `r.mu`),
`internal/chatexec/chatexec.go` (supersede path calls `oldRun.Finish` from the new turn's
goroutine while the old turn's drain may still call `oldRun.Append`).

**Problem:** `Append` reads `r.Completed` outside the lock, then takes `r.mu` and calls
`r.log.Append`. `Finish` sets `Completed` via CAS, then calls `r.log.Append` + `r.log.Close()`
**without holding `r.mu`**. Interleavings allow `Append` to write to a closed `*os.File` /
race the `bufio.Writer` that `Finish`'s `Close→Flush` touches.

**Fix:**
1. Add `closed bool` to the `Run` struct (guarded by `r.mu`).
2. `Finish`: take `r.mu` ONLY around the terminal-event build + `log.Append` + `log.Close` +
   `closed = true` sequence, then **release it** before the index write and fanout. This is the
   critical correctness point found in self-review: `Finish` currently does the index write
   (disk I/O) and then `r.fanout(e)` (which re-acquires `r.mu` itself) *after* the log work, in
   that order, deliberately so subscribers read the terminal index only after it is persisted.
   The fix must NOT hold `r.mu` across the index disk I/O (that would inject disk latency into
   the lock and block `Append`), and must NOT reorder index-before-fanout. So:
   ```go
   func (r *Run) Finish(status Status, reason CancelReason, supersededBy string) error {
   	if !r.Completed.CompareAndSwap(false, true) {
   		return nil
   	}
   	close(r.done)

   	r.mu.Lock()
   	seq := r.LastSeq.Add(1)
   	e := Event{Seq: seq, Ts: ..., Type: EventTypeDone, Status: status, Reason: reason, SupersededBy: supersededBy}
   	logErr := r.log.Append(e)
   	_ = r.log.Close()
   	r.closed = true
   	r.mu.Unlock()

   	// index persistence (disk) — OUTSIDE r.mu, unchanged
   	// ... loadIndex / Upsert / saveIndex ...

   	r.fanout(e)            // unchanged: re-acquires r.mu internally
   	r.closeAllSubscribers()
   	// ... return logErr / saveErr, unchanged ...
   }
   ```
   The plan will reproduce the existing index-write + fanout + return code verbatim and only
   (a) wrap the log build/append/close in `r.mu` and (b) set `r.closed = true` inside that span.
   `r.LastSeq.Add` moves inside the lock so the terminal seq can't interleave with an `Append`
   seq.
3. `Append`: re-check **after** acquiring `r.mu`:
   ```go
   func (r *Run) Append(t EventType, payload []byte) (int64, error) {
   	r.mu.Lock()
   	defer r.mu.Unlock()
   	if r.Completed.Load() || r.closed {
   		return 0, fmt.Errorf("run %s already completed", r.ID)
   	}
   	seq := r.LastSeq.Add(1)
   	// ... build + r.log.Append + fanoutLocked, unchanged ...
   }
   ```
   The early `r.Completed.Load()` check outside the lock may stay as a cheap fast-path, but the
   authoritative check is now inside the lock before `r.log` is touched.

**Test:** a `-race` test that starts a run, spawns a goroutine hammering `Append` in a loop, and
concurrently calls `Finish`, asserting no race and no post-close write panic. Run with
`go test -race`.

### 5.2 R1 — stream-delivered errors bypass recovery (harness)

**Where:** `harness/runtime/runtime.go:455-487` (sync-error path: `IsContextOverflow` →
compact+retry at `:457`; `IsRetryableModelError` → fallback at `:472`), `:492-584` (the stream
loop; `gotFirstToken` at `:492`, the `EventError` handling). The Anthropic SDK delivers HTTP
errors via `stream.Err()` → an `EventError` on the channel, not synchronously, so the sync-path
recovery never sees them. Gemini has the same shape.

**Problem:** reactive compaction and fallback-model retry only fire on a **synchronous**
`ChatStream` error. A pre-first-token 429 / context-overflow delivered via `EventError` just
aborts the turn.

**Fix:** in the `EventError` branch, when `!gotFirstToken`, run the same classification as the
sync path before giving up:

```go
case EventError:
	if !gotFirstToken {
		// Same recovery the synchronous ChatStream-error path performs:
		// a stream-delivered pre-first-token error (Anthropic/Gemini) must
		// get the same compaction + fallback treatment.
		if compaction.IsContextOverflow(ev.Error) && r.Compaction != nil {
			// compact, then retry the turn (mirror the sync path's flow)
		}
		if r.FallbackModel != "" && r.FallbackModel != req.Model && llm.IsRetryableModelError(ev.Error) {
			// switch to fallback model, then retry the turn
		}
	}
	// existing post-first-token non-streaming retry behaviour unchanged
```

The plan will factor the sync path's compact-and-retry / fallback-and-retry blocks
(`runtime.go:455-487`) into a shared helper (or a labelled retry) so the `EventError` branch
reuses the **exact** same logic rather than duplicating it — avoiding drift between the two
recovery sites. The retry must respect the existing turn/iteration cap and not loop infinitely
on a persistently-failing stream.

**Test:** harness unit test with a fake provider that emits an `EventError` carrying a
context-overflow (then a retryable error) **before any token**, asserting that compaction runs
(resp. the fallback model is selected) and the turn retries rather than aborting.

---

## 6. Testing strategy (per finding)

- **R4:** `Start(ctx)` → `Add(job)` → `Start(ctx)` again → `Stop()` returns within a short
  timeout (no hang); a job added before the second `Start` actually executes.
- **R6:** `persist()` writes only `Source=="dynamic"` jobs; `Scheduler.Add` returns an error on
  duplicate name; add-static + add-dynamic + persist + fresh-restore yields exactly one of each
  (no duplicates).
- **R2:** build a config with an agent whose `Tools.Allow` is curated; run
  `applyAutoAddedAllowlists`; assert the agent's effective allow set includes the MCP / task /
  cortex names — and that running it again (simulating reload) is idempotent.
- **R5:** `providerHolder` Load/Store under `-race`; a reader sees the post-`store` map; the
  closures resolve a provider added only after `store`.
- **N3:** `-race` concurrent `Append`-vs-`Finish` test; no post-close write.
- **R1:** fake-provider `EventError` pre-first-token (overflow / retryable) → compaction fires /
  fallback selected / turn retries.

Run in both repos: `go build ./...`, `go test ./...`, `go vet ./...`, and `go test -race` on
touched packages (`internal/cron`, `internal/startup`, `internal/gateway/runs` in Felix;
`runtime` in harness).

---

## 7. Files touched

**felix:**
- `internal/cron/cron.go` — idempotent `Start`; `Job.Source`; dup-name rejection in `Add`;
  `Source` surfaced in `Jobs()`.
- `internal/startup/startup.go` — `Source` on static + dynamic adds; `persist()` dynamic-only
  filter; `applyAutoAddedAllowlists` helper + both call sites; `providerHolder` + migrate the
  subagent/cron/static-add provider reads + the reload `store`.
- `internal/gateway/runs/registry.go` — `Run.closed`; lock discipline in `Finish` + `Append`.

**harness:**
- `runtime/runtime.go` — `EventError` pre-first-token recovery (shared with the sync path).

**New tests:** `internal/cron/cron_test.go` (R4, R6 additions), a startup-level test for R2/R5
(or `internal/startup` test), `internal/gateway/runs/registry_race_test.go` (N3),
`runtime/*_test.go` (R1).

---

## 8. Deferred (explicitly not in this round)

| Item | Why |
|------|-----|
| R3 single-canonical-`Config` identity / `UpdateFrom` map-aliasing refactor | Larger architectural change; watcher + debounce halves of R3 already done |
| Folding `wsHandler`'s provider field onto the shared holder | Avoid touching WS provider plumbing this round; closures are the staleness source R5 targets |
| R7–R10, performance batch (P1–P8), remaining security (S2/S3/N4/…) | Later execution-order steps |
