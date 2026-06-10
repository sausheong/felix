# Cortex + Embeddings Default to Local Ollama Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Default Cortex (LLM + embedder) and Memory embeddings to bundled Ollama with first-run background pull of `gemma4:latest` + `nomic-embed-text`, removing the qwen/gemma onboarding prompt.

**Architecture:** Config defaults flip Memory and the default agent to `local`/well-known model names. `Cortex.Provider`/`LLMModel` stay empty as a sentinel meaning "mirror the default agent's model" — `cortex.Init` parses the agent's `Model` to pick the cortex provider/model. A new `internal/local/bootstrap.go` checks a `~/.felix/.first-run-done` sentinel at startup and, on first launch, pulls the two defaults sequentially via the existing `local.Installer`. Cortex/Memory tolerate missing models with `slog.Warn` plus one-time tray nudges.

**Tech Stack:** Go 1.x, `log/slog`, `github.com/sashabaranov/go-openai` (via cortex's `cortexoai` wrapper), existing `internal/local.Installer` for Ollama HTTP API.

**Spec:** `docs/superpowers/specs/2026-04-22-cortex-embeddings-local-default-design.md`

**Spec deviation flagged upfront:** The spec describes routing bootstrap progress through `AgentEvent`. Bootstrap runs at startup before any chat turn exists, so no `AgentEvent` channel is open yet. This plan delivers `BootstrapEvent` directly via a callback to the call site (CLI prints to stdout, gateway logs via `slog`). No new `AgentEvent` types are added. The `BootstrapEvent` struct still exists in `internal/local/bootstrap.go` for testing the goroutine.

---

## File Plan

**Create:**
- `internal/local/embed_dims.go` — embedding model → dimensions lookup
- `internal/local/embed_dims_test.go`
- `internal/local/bootstrap.go` — `EnsureFirstRunModels`, `BootstrapEvent`, `Puller` interface
- `internal/local/bootstrap_test.go`
- `internal/memory/embedder_test.go` — verify `nomic-embed-text` passes through

**Modify:**
- `internal/config/config.go` — defaults; remove `Cortex.Provider` backfill
- `internal/config/config_test.go` — assert new defaults
- `internal/cortex/cortex.go` — `Init` signature, auto-mirror, local branch
- `internal/cortex/cortex_test.go` — auto-mirror + local-branch tests
- `cmd/felix/main.go` — drop qwen/gemma prompt; cortex.Init caller; bootstrap call
- `internal/startup/startup.go` — cortex.Init caller; bootstrap call; memory probe

---

## Task 1: Embedding-dimension lookup

**Files:**
- Create: `internal/local/embed_dims.go`
- Test: `internal/local/embed_dims_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/local/embed_dims_test.go`:

```go
package local

import "testing"

func TestEmbeddingDimsKnownModels(t *testing.T) {
	cases := map[string]int{
		"nomic-embed-text":       768,
		"mxbai-embed-large":      1024,
		"all-minilm":             384,
		"text-embedding-3-small": 1536,
		"text-embedding-3-large": 3072,
		"text-embedding-ada-002": 1536,
	}
	for model, want := range cases {
		gotModel, gotDims := EmbeddingDims(model)
		if gotModel != model {
			t.Errorf("EmbeddingDims(%q): model = %q, want %q", model, gotModel, model)
		}
		if gotDims != want {
			t.Errorf("EmbeddingDims(%q): dims = %d, want %d", model, gotDims, want)
		}
	}
}

func TestEmbeddingDimsUnknownFallsBackTo1536(t *testing.T) {
	gotModel, gotDims := EmbeddingDims("some-unknown-model")
	if gotModel != "some-unknown-model" {
		t.Errorf("model passthrough failed: got %q", gotModel)
	}
	if gotDims != 1536 {
		t.Errorf("unknown dims = %d, want 1536", gotDims)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/local -run TestEmbeddingDims -v`
Expected: FAIL with `undefined: EmbeddingDims`.

- [ ] **Step 3: Write the implementation**

Create `internal/local/embed_dims.go`:

```go
package local

// embedDims maps known embedding model names to their vector dimensions.
// Used by cortex.Init to wire WithEmbeddingModel correctly.
var embedDims = map[string]int{
	"nomic-embed-text":       768,
	"mxbai-embed-large":      1024,
	"all-minilm":             384,
	"text-embedding-3-small": 1536,
	"text-embedding-3-large": 3072,
	"text-embedding-ada-002": 1536,
}

// EmbeddingDims returns (model, dimensions). Unknown models fall back to 1536
// (OpenAI's standard) so cortex doesn't refuse to start on an unrecognised name.
func EmbeddingDims(model string) (string, int) {
	if d, ok := embedDims[model]; ok {
		return model, d
	}
	return model, 1536
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/local -run TestEmbeddingDims -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/local/embed_dims.go internal/local/embed_dims_test.go
git commit -m "feat(local): embedding model dimensions lookup"
```

---

## Task 2: Bootstrap goroutine

**Files:**
- Create: `internal/local/bootstrap.go`
- Test: `internal/local/bootstrap_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/local/bootstrap_test.go`:

```go
package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakePuller struct {
	mu       sync.Mutex
	have     map[string]bool // models already on disk
	pulled   []string        // pull calls in order
	failOn   string          // model name that should fail; "" = never
	failWith error
}

func (f *fakePuller) List(ctx context.Context) ([]Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Model
	for name := range f.have {
		out = append(out, Model{Name: name})
	}
	return out, nil
}

func (f *fakePuller) Pull(ctx context.Context, name string, onEvent func(ProgressEvent)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, name)
	if name == f.failOn {
		return f.failWith
	}
	if f.have == nil {
		f.have = map[string]bool{}
	}
	f.have[name] = true
	return nil
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestEnsureFirstRunModelsPullsBothInOrder(t *testing.T) {
	tmp := t.TempDir()
	puller := &fakePuller{}
	done := make(chan struct{})
	EnsureFirstRunModels(context.Background(), tmp, puller, func(ev BootstrapEvent) {
		if ev.Type == BootstrapDone || ev.Type == BootstrapFailed {
			close(done)
		}
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("bootstrap did not complete in time")
	}
	puller.mu.Lock()
	defer puller.mu.Unlock()
	if len(puller.pulled) != 2 {
		t.Fatalf("expected 2 pulls, got %d (%v)", len(puller.pulled), puller.pulled)
	}
	if puller.pulled[0] != "nomic-embed-text" {
		t.Errorf("first pull should be nomic-embed-text, got %q", puller.pulled[0])
	}
	if puller.pulled[1] != "gemma4:latest" {
		t.Errorf("second pull should be gemma4:latest, got %q", puller.pulled[1])
	}
	if _, err := os.Stat(filepath.Join(tmp, ".first-run-done")); err != nil {
		t.Errorf("sentinel not written: %v", err)
	}
}

func TestEnsureFirstRunModelsSkipsWhenSentinelExists(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".first-run-done"), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	puller := &fakePuller{}
	called := false
	EnsureFirstRunModels(context.Background(), tmp, puller, func(ev BootstrapEvent) {
		called = true
	})
	// Give the goroutine a chance to fire if it were going to.
	time.Sleep(50 * time.Millisecond)
	puller.mu.Lock()
	defer puller.mu.Unlock()
	if len(puller.pulled) != 0 {
		t.Errorf("expected 0 pulls when sentinel present, got %d", len(puller.pulled))
	}
	if called {
		t.Errorf("expected no events when sentinel present")
	}
}

func TestEnsureFirstRunModelsSkipsAlreadyPulledModels(t *testing.T) {
	tmp := t.TempDir()
	puller := &fakePuller{have: map[string]bool{"nomic-embed-text": true}}
	done := make(chan struct{})
	EnsureFirstRunModels(context.Background(), tmp, puller, func(ev BootstrapEvent) {
		if ev.Type == BootstrapDone || ev.Type == BootstrapFailed {
			close(done)
		}
	})
	if !waitFor(func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 2*time.Second) {
		t.Fatal("bootstrap did not complete in time")
	}
	puller.mu.Lock()
	defer puller.mu.Unlock()
	if len(puller.pulled) != 1 {
		t.Fatalf("expected 1 pull (skipping pre-existing nomic), got %d (%v)", len(puller.pulled), puller.pulled)
	}
	if puller.pulled[0] != "gemma4:latest" {
		t.Errorf("only missing model should be pulled, got %q", puller.pulled[0])
	}
}

func TestEnsureFirstRunModelsLeavesSentinelAbsentOnFailure(t *testing.T) {
	tmp := t.TempDir()
	puller := &fakePuller{failOn: "gemma4:latest", failWith: errors.New("network down")}
	done := make(chan struct{})
	EnsureFirstRunModels(context.Background(), tmp, puller, func(ev BootstrapEvent) {
		if ev.Type == BootstrapDone || ev.Type == BootstrapFailed {
			close(done)
		}
	})
	if !waitFor(func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 2*time.Second) {
		t.Fatal("bootstrap did not complete in time")
	}
	if _, err := os.Stat(filepath.Join(tmp, ".first-run-done")); !os.IsNotExist(err) {
		t.Errorf("sentinel must NOT be written on failure (err=%v)", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/local -run TestEnsureFirstRun -v`
Expected: FAIL with `undefined: EnsureFirstRunModels`, `undefined: BootstrapEvent`, etc.

- [ ] **Step 3: Write the implementation**

Create `internal/local/bootstrap.go`:

```go
package local

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Puller is the subset of *Installer used by the bootstrap goroutine.
// Defined as an interface so tests can inject a fake.
type Puller interface {
	List(ctx context.Context) ([]Model, error)
	Pull(ctx context.Context, name string, onEvent func(ProgressEvent)) error
}

// BootstrapEventType identifies a bootstrap-progress event.
type BootstrapEventType int

const (
	BootstrapStart BootstrapEventType = iota
	BootstrapProgress
	BootstrapDone
	BootstrapFailed
)

// BootstrapEvent is delivered to the callback passed to EnsureFirstRunModels.
type BootstrapEvent struct {
	Type        BootstrapEventType
	Models      []string // populated for Start, Done
	Model       string   // populated for Progress, Failed
	Percent     float32  // populated for Progress (0-100)
	DurationSec int      // populated for Done
	Error       string   // populated for Failed
}

// firstRunModels are the two defaults pulled on the first ever Felix launch.
// Order matters: embedding model first (small, brings semantic search online
// quickly) then the LLM (slow).
var firstRunModels = []string{"nomic-embed-text", "gemma4:latest"}

// EnsureFirstRunModels kicks off background pulls of the default LLM and
// embedding model on the first ever Felix run. If `dataDir/.first-run-done`
// exists the function returns immediately. Otherwise it spawns a goroutine
// that pulls each missing default sequentially via puller. The sentinel is
// written only on full success — partial failures retry on the next launch.
//
// onEvent is called on the goroutine; pass nil to discard events.
func EnsureFirstRunModels(ctx context.Context, dataDir string, puller Puller, onEvent func(BootstrapEvent)) {
	sentinel := filepath.Join(dataDir, ".first-run-done")
	if _, err := os.Stat(sentinel); err == nil {
		return // already bootstrapped
	}

	emit := func(ev BootstrapEvent) {
		if onEvent != nil {
			onEvent(ev)
		}
	}

	go func() {
		emit(BootstrapEvent{Type: BootstrapStart, Models: firstRunModels})
		slog.Info("first-run bootstrap start", "models", firstRunModels)
		start := time.Now()

		// Find which models are already on disk so we don't re-pull.
		have := map[string]bool{}
		if list, err := puller.List(ctx); err == nil {
			for _, m := range list {
				have[m.Name] = true
			}
		}

		for _, m := range firstRunModels {
			if have[m] {
				slog.Info("first-run model already present", "model", m)
				continue
			}
			mStart := time.Now()
			err := puller.Pull(ctx, m, func(ev ProgressEvent) {
				if ev.Total > 0 {
					pct := float32(ev.Completed) / float32(ev.Total) * 100
					emit(BootstrapEvent{Type: BootstrapProgress, Model: m, Percent: pct})
				}
			})
			if err != nil {
				slog.Warn("first-run model pull failed", "model", m, "error", err)
				emit(BootstrapEvent{Type: BootstrapFailed, Model: m, Error: err.Error()})
				return // sentinel NOT written → retry on next launch
			}
			slog.Info("first-run model pulled", "model", m,
				"duration_ms", time.Since(mStart).Milliseconds())
		}

		dur := time.Since(start)
		slog.Info("first-run bootstrap complete", "duration_ms", dur.Milliseconds())
		emit(BootstrapEvent{Type: BootstrapDone, Models: firstRunModels, DurationSec: int(dur.Seconds())})

		if err := os.WriteFile(sentinel, []byte(time.Now().Format(time.RFC3339)), 0o644); err != nil {
			slog.Warn("first-run sentinel write failed", "error", err)
		}
	}()
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/local -run TestEnsureFirstRun -v`
Expected: PASS (all four tests).

- [ ] **Step 5: Commit**

```bash
git add internal/local/bootstrap.go internal/local/bootstrap_test.go
git commit -m "feat(local): first-run bootstrap for default models"
```

---

## Task 3: Config defaults flip

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go` (append at the end of the file):

```go
func TestDefaultConfigCortexEmbedDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if got := cfg.Memory.EmbeddingProvider; got != "local" {
		t.Errorf("Memory.EmbeddingProvider default = %q, want \"local\"", got)
	}
	if got := cfg.Memory.EmbeddingModel; got != "nomic-embed-text" {
		t.Errorf("Memory.EmbeddingModel default = %q, want \"nomic-embed-text\"", got)
	}
	if !cfg.Memory.Enabled {
		t.Errorf("Memory.Enabled default should be true")
	}
	if cfg.Cortex.Provider != "" {
		t.Errorf("Cortex.Provider default = %q, want \"\" (auto-mirror sentinel)", cfg.Cortex.Provider)
	}
	if cfg.Cortex.LLMModel != "" {
		t.Errorf("Cortex.LLMModel default = %q, want \"\" (auto-mirror sentinel)", cfg.Cortex.LLMModel)
	}
}

func TestValidateDoesNotBackfillCortexProvider(t *testing.T) {
	cfg := DefaultConfig()
	// Default has at least one agent already; Validate should leave
	// Cortex.Provider empty (the new auto-mirror sentinel).
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if cfg.Cortex.Provider != "" {
		t.Errorf("Validate must NOT backfill Cortex.Provider; got %q", cfg.Cortex.Provider)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config -run "TestDefaultConfigCortexEmbedDefaults|TestValidateDoesNotBackfillCortexProvider" -v`
Expected: FAIL on `Memory.EmbeddingProvider default = "" want "local"` and `Validate must NOT backfill Cortex.Provider; got "openai"`.

- [ ] **Step 3: Update `DefaultConfig()`**

In `internal/config/config.go`, find the existing `DefaultConfig` function. The current `MemoryConfig` literal (if any) is implicit zero-valued; the current `CortexConfig` literal sets `Enabled: true, LLMModel: "gpt-5-mini"`. Replace those two stanzas:

Locate:
```go
		Cortex: CortexConfig{
			Enabled:  true,
			LLMModel: "gpt-5-mini",
		},
```

Replace with:
```go
		Memory: MemoryConfig{
			Enabled:           true,
			EmbeddingProvider: "local",
			EmbeddingModel:    "nomic-embed-text",
		},
		Cortex: CortexConfig{
			Enabled: true,
			// Provider and LLMModel intentionally empty: cortex.Init mirrors
			// the default agent's model when both are unset.
		},
```

- [ ] **Step 4: Remove the Cortex.Provider backfill in `Validate()`**

In `internal/config/config.go`, locate:
```go
	if c.Cortex.Enabled && c.Cortex.Provider == "" {
		c.Cortex.Provider = "openai"
	}
```

Delete those three lines entirely. The auto-mirror sentinel (empty Provider) is now meaningful and must survive `Validate()`.

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/config -v`
Expected: PASS, including the two new tests. Some pre-existing tests may also need updating if they assert on `Cortex.Provider == "openai"` — patch them to expect empty string.

If `TestDefaultConfig` (or similar) fails because it expects `Cortex.Provider == "openai"` or `LLMModel == "gpt-5-mini"`, update those assertions to expect empty strings. Do not skip or delete the test — change the expected values.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): default memory + cortex to local provider"
```

---

## Task 4: Cortex auto-mirror + local-provider branch

**Files:**
- Modify: `internal/cortex/cortex.go`
- Test: `internal/cortex/cortex_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/cortex/cortex_test.go`:

```go
import (
	"testing"

	"github.com/sausheong/felix/internal/config"
)

func TestResolveCortexModelMirrorsAgentWhenEmpty(t *testing.T) {
	cfg := config.CortexConfig{Enabled: true} // Provider and LLMModel both empty
	provider, model := resolveCortexModel(cfg, "local/gemma4:latest")
	if provider != "local" {
		t.Errorf("auto-mirror provider = %q, want \"local\"", provider)
	}
	if model != "gemma4:latest" {
		t.Errorf("auto-mirror model = %q, want \"gemma4:latest\"", model)
	}
}

func TestResolveCortexModelPreservesExplicitConfig(t *testing.T) {
	cfg := config.CortexConfig{Enabled: true, Provider: "openai", LLMModel: "gpt-4o"}
	provider, model := resolveCortexModel(cfg, "local/gemma4:latest")
	if provider != "openai" {
		t.Errorf("explicit provider should be preserved; got %q", provider)
	}
	if model != "gpt-4o" {
		t.Errorf("explicit model should be preserved; got %q", model)
	}
}

func TestResolveCortexModelDoesNotHalfMirror(t *testing.T) {
	// If only one of Provider/LLMModel is set, the explicit values win
	// verbatim and we don't fill the missing field from the agent.
	cfg := config.CortexConfig{Enabled: true, Provider: "anthropic", LLMModel: ""}
	provider, model := resolveCortexModel(cfg, "local/gemma4:latest")
	if provider != "anthropic" || model != "" {
		t.Errorf("partial config should not auto-mirror; got (%q, %q)", provider, model)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cortex -run TestResolveCortexModel -v`
Expected: FAIL with `undefined: resolveCortexModel`.

- [ ] **Step 3: Replace the entire `Init` function and its imports**

In `internal/cortex/cortex.go`:

(a) Update the import block. The current imports are:

```go
import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/connector/conversation"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	"github.com/sausheong/cortex/extractor/llmext"
	cortexanthropic "github.com/sausheong/cortex/llm/anthropic"
	cortexoai "github.com/sausheong/cortex/llm/openai"
	"github.com/sausheong/felix/internal/config"
)
```

Replace with:

```go
import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/connector/conversation"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	"github.com/sausheong/cortex/extractor/llmext"
	cortexanthropic "github.com/sausheong/cortex/llm/anthropic"
	cortexoai "github.com/sausheong/cortex/llm/openai"
	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/llm"
	localpkg "github.com/sausheong/felix/internal/local"
)
```

(b) Replace the entire existing `Init` function (the doc comment + the function body, lines 43-96 in the current file) with this complete version:

```go
// resolveCortexModel returns the (provider, model) cortex.Init should use.
// When both cfg.Provider and cfg.LLMModel are empty, it mirrors the default
// agent's model (e.g. "local/gemma4:latest" → "local", "gemma4:latest").
// Otherwise it returns the explicit values verbatim — no half-mirroring.
func resolveCortexModel(cfg config.CortexConfig, defaultAgentModel string) (provider, model string) {
	if cfg.Provider == "" && cfg.LLMModel == "" {
		return llm.ParseProviderModel(defaultAgentModel)
	}
	return cfg.Provider, cfg.LLMModel
}

// Init opens (or creates) a Cortex knowledge graph using the provided config.
// When cfg.Provider and cfg.LLMModel are both empty, the function mirrors
// defaultAgentModel: e.g. "local/gemma4:latest" wires cortex through bundled
// Ollama with the same model the default agent uses. getProvider is used to
// look up the resolved provider's API key + base URL.
func Init(cfg config.CortexConfig, memCfg config.MemoryConfig, defaultAgentModel string, getProvider func(name string) config.ProviderConfig) (*cortex.Cortex, error) {
	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(config.DefaultDataDir(), "brain.db")
	}

	provider, model := resolveCortexModel(cfg, defaultAgentModel)
	pcfg := getProvider(provider)
	apiKey := pcfg.APIKey
	baseURL := pcfg.BaseURL
	slog.Info("cortex auto-mirror",
		"agent_model", defaultAgentModel,
		"resolved_provider", provider,
		"resolved_model", model)

	var opts []cortex.Option

	// "local" needs no API key (bundled Ollama). Other providers require one
	// to enable LLM-backed extraction; without a key cortex runs deterministic-
	// only via cortex.Open's default extractor.
	if provider == "local" || apiKey != "" {
		detExt := deterministic.New()

		switch provider {
		case "local":
			if model == "" {
				model = "gemma4:latest"
			}
			llmClient := cortexoai.NewLLM("",
				cortexoai.WithBaseURL(baseURL),
				cortexoai.WithModel(model))

			embModel, embDims := localpkg.EmbeddingDims(memCfg.EmbeddingModel)
			embedder := cortexoai.NewEmbedder("",
				cortexoai.WithEmbedderBaseURL(baseURL),
				cortexoai.WithEmbeddingModel(goopenai.EmbeddingModel(embModel), embDims))

			extractor := hybrid.New(detExt, llmext.New(llmClient))
			opts = append(opts,
				cortex.WithLLM(llmClient),
				cortex.WithEmbedder(embedder),
				cortex.WithExtractor(extractor),
			)

		case "anthropic":
			if model == "" {
				model = "claude-sonnet-4-5-20250929"
			}
			llmClient := cortexanthropic.NewLLM(apiKey, cortexanthropic.WithModel(model))
			extractor := hybrid.New(detExt, llmext.New(llmClient))
			opts = append(opts,
				cortex.WithLLM(llmClient),
				cortex.WithExtractor(extractor),
			)

		default: // "openai" and any unknown provider
			if model == "" {
				model = "gpt-5.4-mini"
			}
			llmClient := cortexoai.NewLLM(apiKey, cortexoai.WithModel(model))
			embedder := cortexoai.NewEmbedder(apiKey)
			extractor := hybrid.New(detExt, llmext.New(llmClient))
			opts = append(opts,
				cortex.WithLLM(llmClient),
				cortex.WithEmbedder(embedder),
				cortex.WithExtractor(extractor),
			)
		}
	}

	cx, err := cortex.Open(dbPath, opts...)
	if err != nil {
		return nil, fmt.Errorf("cortex init: %w", err)
	}

	slog.Info("cortex knowledge graph initialized", "db", dbPath)
	return cx, nil
}
```

Notes on the rewrite:
- The local `llm` variable in each case branch is renamed to `llmClient` to avoid shadowing the package-level `llm` import added in (a).
- The `case "local":` branch reuses `cortexoai.NewLLM` (cortex's openai client) with `WithBaseURL` — Ollama exposes an OpenAI-compatible `/v1` endpoint, so no new client type is needed.
- `cortexoai.NewEmbedder("")` with empty `apiKey` is safe when `WithEmbedderBaseURL` is set (Ollama doesn't authenticate).
- An ultimate `model == ""` fallback to `"gemma4:latest"` is added for the local branch in case the default agent's `Model` field is empty for any reason.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/cortex -v`
Expected: PASS, including the three new `TestInit*` tests. Existing tests that called `Init(cfg, apiKey)` will fail to compile — they will be updated in Task 5.

If existing tests block this commit, comment out or skip the failing `Init` callsites in tests temporarily with a `// TODO restored in Task 5` note. Better: extract the test bodies that exercise `resolveCortexModel` separately from those that call `Init`, since `resolveCortexModel` can be tested without rewiring `Init` callers.

- [ ] **Step 5: Commit**

```bash
git add internal/cortex/cortex.go internal/cortex/cortex_test.go
git commit -m "feat(cortex): auto-mirror agent model + local provider branch"
```

---

## Task 5: Update cortex.Init callers

**Files:**
- Modify: `cmd/felix/main.go` (around line 289)
- Modify: `internal/startup/startup.go` (around line 268)

- [ ] **Step 1: Update `cmd/felix/main.go::289`**

Locate:
```go
		cx, cxErr = cortexadapter.Init(cfg.Cortex, cfg.GetProvider(cfg.Cortex.Provider).APIKey)
```

Replace with:
```go
		defaultAgentModel := ""
		if len(cfg.Agents.List) > 0 {
			defaultAgentModel = cfg.Agents.List[0].Model
		}
		cx, cxErr = cortexadapter.Init(cfg.Cortex, cfg.Memory, defaultAgentModel, cfg.GetProvider)
```

- [ ] **Step 2: Update `internal/startup/startup.go::268`**

Locate the equivalent call (search for `cortexadapter.Init`):
```go
		cx, initErr = cortexadapter.Init(cfg.Cortex, cfg.GetProvider(cfg.Cortex.Provider).APIKey)
```

Replace with:
```go
		defaultAgentModel := ""
		if len(cfg.Agents.List) > 0 {
			defaultAgentModel = cfg.Agents.List[0].Model
		}
		cx, initErr = cortexadapter.Init(cfg.Cortex, cfg.Memory, defaultAgentModel, cfg.GetProvider)
```

- [ ] **Step 3: Build everything to catch any other callers**

Run: `go build ./...`
Expected: success. If any other call site was missed, the build will surface it. Patch those sites with the same pattern.

- [ ] **Step 4: Run the full test suite**

Run: `go test ./...`
Expected: pre-existing failures only (the three known ones noted in CLAUDE.md or recent commits — `TestAssembleSystemPromptDefault`, `TestDefaultConfig` if not already updated, `TestChannelManagerFallbackChatID`). No NEW failures.

If `TestDefaultConfig` fails on the OLD assertion (`Cortex.Provider == "openai"` or `Cortex.LLMModel == "gpt-5-mini"`), update it to assert the new defaults (empty strings).

- [ ] **Step 5: Commit**

```bash
git add cmd/felix/main.go internal/startup/startup.go
git commit -m "refactor: thread cortex.Init new signature through callers"
```

---

## Task 6: Drop the qwen/gemma onboarding prompt

**Files:**
- Modify: `cmd/felix/main.go` (around lines 1227-1254)

- [ ] **Step 1: Locate the existing block**

Open `cmd/felix/main.go`. Find the block:
```go
	if !hasCloudKey {
		fmt.Println("No cloud API key found in your environment.")
		fmt.Println("Pick a local model to download (you can change this later):")
		fmt.Println()
		localChoice := choose("", []string{
			"Qwen 3.5 9B                ~5.0 GB   (recommended — good general agent)",
			"Gemma 4 (multimodal)       ~9.6 GB   (vision-capable)",
			"Skip — I'll configure a cloud key later",
		}, 0)
		if localChoice != 2 {
			models := []string{
				"qwen3.5:9b",
				"gemma4:latest",
			}
			modelTag := models[localChoice]
			if err := pullLocalModel(modelTag); err != nil {
				fmt.Printf("Pull failed: %v\n", err)
				fmt.Println("Falling back to cloud setup.")
			} else {
				cfg.Agents.List[0].Model = "local/" + modelTag
				cfg.Providers["local"] = config.ProviderConfig{
					Kind:    "local",
					BaseURL: "http://127.0.0.1:18790/v1",
				}
				return finishOnboard(cfg)
			}
		}
	}
```

- [ ] **Step 2: Replace with the simplified flow**

Replace the entire block above with:

```go
	if !hasCloudKey {
		fmt.Println("No cloud API key found in your environment.")
		fmt.Println("Felix will use the bundled local model.")
		fmt.Println("`gemma4:latest` (~9.6 GB) and `nomic-embed-text` (~270 MB) will")
		fmt.Println("download in the background on first launch.")
		fmt.Println()
		cfg.Agents.List[0].Model = "local/gemma4:latest"
		cfg.Providers["local"] = config.ProviderConfig{
			Kind:    "local",
			BaseURL: "http://127.0.0.1:18790/v1",
		}
		return finishOnboard(cfg)
	}
```

This removes the foreground pull entirely — Task 7 wires the background pull on every Felix start.

- [ ] **Step 3: Verify build**

Run: `go build ./cmd/felix`
Expected: success.

- [ ] **Step 4: Run any onboarding tests**

Run: `go test ./cmd/felix -v 2>&1 | head -50`
Expected: pass (or only pre-existing failures). If any test asserts on the qwen/gemma prompt text, update it to assert the new "Felix will use the bundled local model." text.

- [ ] **Step 5: Commit**

```bash
git add cmd/felix/main.go
git commit -m "feat(onboarding): drop qwen/gemma prompt; gemma4 is the local default"
```

---

## Task 7: Wire bootstrap into startup paths

**Files:**
- Modify: `cmd/felix/main.go` (chat-startup section)
- Modify: `internal/startup/startup.go` (after `local.InjectLocalProvider`)

- [ ] **Step 1: Find the bundled-Ollama startup site in `cmd/felix/main.go`**

Search for `InjectLocalProvider` in `cmd/felix/main.go`:

```bash
grep -n "InjectLocalProvider\|supervisor.Start\|local.NewSupervisor" /Users/sausheong/projects/felix/cmd/felix/main.go
```

After the line that confirms the local provider has been injected (and `cfg` reflects the bound port), add the bootstrap call. A good anchor is right before the chat REPL begins or right after `finishOnboard` succeeds.

- [ ] **Step 2: Add the bootstrap call**

At the chosen site in `cmd/felix/main.go`, insert:

```go
	// First-run background pull of default local models.
	if cfg.Local.Enabled {
		if pcfg := cfg.GetProvider("local"); pcfg.BaseURL != "" {
			puller := local.NewInstaller(strings.TrimSuffix(pcfg.BaseURL, "/v1"))
			local.EnsureFirstRunModels(context.Background(), config.DefaultDataDir(), puller, func(ev local.BootstrapEvent) {
				switch ev.Type {
				case local.BootstrapStart:
					fmt.Printf("\033[90m📥 Downloading default models in background: %v\033[0m\n", ev.Models)
				case local.BootstrapDone:
					fmt.Printf("\033[90m📥 Default models ready (%ds)\033[0m\n", ev.DurationSec)
				case local.BootstrapFailed:
					fmt.Printf("\033[33m📥 Pull failed for %s: %s — will retry on next launch\033[0m\n", ev.Model, ev.Error)
				}
				// BootstrapProgress is intentionally silent — too noisy for the CLI.
			})
		}
	}
```

Imports needed at the top of `cmd/felix/main.go` (most likely already present): `"strings"`, `"context"`, `"github.com/sausheong/felix/internal/local"`.

- [ ] **Step 3: Find and update the equivalent site in `internal/startup/startup.go`**

```bash
grep -n "InjectLocalProvider" /Users/sausheong/projects/felix/internal/startup/startup.go
```

After that call site (where the gateway has the bundled Ollama URL), add the same bootstrap call but with a slog-only event handler (the gateway server doesn't print to stdout):

```go
	if cfg.Local.Enabled {
		if pcfg := cfg.GetProvider("local"); pcfg.BaseURL != "" {
			puller := local.NewInstaller(strings.TrimSuffix(pcfg.BaseURL, "/v1"))
			local.EnsureFirstRunModels(context.Background(), config.DefaultDataDir(), puller, nil)
		}
	}
```

Imports needed at the top of `internal/startup/startup.go`: `"strings"`, `"context"`, `"github.com/sausheong/felix/internal/local"` (likely all already present).

- [ ] **Step 4: Build and run smoke**

Run:
```bash
go build ./...
go test ./cmd/felix ./internal/startup -v 2>&1 | tail -40
```
Expected: build success; tests pass (or only pre-existing failures).

- [ ] **Step 5: Commit**

```bash
git add cmd/felix/main.go internal/startup/startup.go
git commit -m "feat: wire first-run bootstrap into CLI and gateway startup"
```

---

## Task 8: Memory embedder probe with skip-and-nudge

**Files:**
- Modify: `cmd/felix/main.go` (around line 278)
- Modify: `internal/startup/startup.go` (around line 255)

- [ ] **Step 1: Add a probe helper at the top of `cmd/felix/main.go`**

Add this helper near the top of `cmd/felix/main.go` (after imports, before `main`):

```go
// probeAndAttachEmbedder wires an embedder onto memMgr only if a single probe
// embed call succeeds within 5s. Logs warn + leaves the embedder unset on
// failure (memory falls back to BM25-only — its existing nil-embedder path).
func probeAndAttachEmbedder(memMgr *memory.Manager, embedder memory.Embedder, modelName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := embedder.Embed(ctx, []string{"probe"}); err != nil {
		slog.Warn("memory: embedder unavailable, BM25-only", "model", modelName, "reason", err)
		// Tray nudge would fire here in a future task; for now slog is enough.
		return
	}
	memMgr.SetEmbedder(embedder)
}
```

Add `"time"` and `"log/slog"` to the import block if not already there.

- [ ] **Step 2: Replace the memory wiring at line 278**

Locate:
```go
		if pcfg := cfg.GetProvider(cfg.Memory.EmbeddingProvider); pcfg.BaseURL != "" || pcfg.APIKey != "" {
			embedder := memory.NewOpenAIEmbedder(pcfg.APIKey, pcfg.BaseURL, cfg.Memory.EmbeddingModel)
			memMgr.SetEmbedder(embedder)
		}
```

(or the equivalent `if cfg.Memory.Enabled { ... }` block; find by `memMgr.SetEmbedder`.)

Replace the `memMgr.SetEmbedder(embedder)` line with:

```go
			probeAndAttachEmbedder(memMgr, embedder, cfg.Memory.EmbeddingModel)
```

- [ ] **Step 3: Repeat for `internal/startup/startup.go`**

Find the matching `memMgr.SetEmbedder` call (around line 255). The `probeAndAttachEmbedder` helper lives in `cmd/felix/main.go`, so for `internal/startup/startup.go` either:

(a) inline the same probe logic at the call site (duplicated, but startup.go uses different package context), or
(b) move `probeAndAttachEmbedder` into `internal/memory/` as a public function `memory.AttachWithProbe(mgr, embedder, model)` and call it from both sites.

Option (b) is cleaner. Move the helper to `internal/memory/probe.go`:

Create `internal/memory/probe.go`:
```go
package memory

import (
	"context"
	"log/slog"
	"time"
)

// AttachWithProbe attaches embedder to mgr only if a single probe embed call
// succeeds within 5s. Logs warn + leaves the embedder unset on failure
// (memory falls back to BM25-only — its existing nil-embedder path).
func AttachWithProbe(mgr *Manager, embedder Embedder, modelName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := embedder.Embed(ctx, []string{"probe"}); err != nil {
		slog.Warn("memory: embedder unavailable, BM25-only", "model", modelName, "reason", err)
		return
	}
	mgr.SetEmbedder(embedder)
}
```

Then in BOTH `cmd/felix/main.go` (line 279) AND `internal/startup/startup.go` (line 256), replace:

```go
			memMgr.SetEmbedder(embedder)
```

with:

```go
			memory.AttachWithProbe(memMgr, embedder, cfg.Memory.EmbeddingModel)
```

(Remove the `probeAndAttachEmbedder` helper added in Step 1 of this task — it's now `memory.AttachWithProbe`.)

- [ ] **Step 4: Add a test for the probe helper**

Create or extend `internal/memory/probe_test.go`:

```go
package memory

import (
	"context"
	"errors"
	"testing"
)

type fakeEmbedder struct {
	err error
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{0.1, 0.2}
	}
	return out, nil
}

func TestAttachWithProbeAttachesOnSuccess(t *testing.T) {
	mgr := New("/tmp", 100) // adjust if New takes different args; check internal/memory/memory.go
	emb := &fakeEmbedder{}
	AttachWithProbe(mgr, emb, "test-model")
	if mgr.embedder == nil {
		t.Errorf("embedder should be attached on probe success")
	}
}

func TestAttachWithProbeSkipsOnFailure(t *testing.T) {
	mgr := New("/tmp", 100)
	emb := &fakeEmbedder{err: errors.New("connection refused")}
	AttachWithProbe(mgr, emb, "test-model")
	if mgr.embedder != nil {
		t.Errorf("embedder must NOT be attached on probe failure")
	}
}
```

Before running, verify `memory.New` signature by reading `internal/memory/memory.go` around line 30 — adjust the `New(...)` calls to match what's actually exported. If `embedder` is unexported and the test is in the same package, accessing `mgr.embedder` directly is fine.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/memory -run TestAttachWithProbe -v`
Expected: PASS (both tests).

Then build everything: `go build ./...` — expected success.

- [ ] **Step 6: Commit**

```bash
git add internal/memory/probe.go internal/memory/probe_test.go cmd/felix/main.go internal/startup/startup.go
git commit -m "feat(memory): probe embedder, fall back to BM25 if unreachable"
```

---

## Task 9: Memory embedder passthrough test

**Files:**
- Create: `internal/memory/embedder_test.go` (if not already created in Task 8)

- [ ] **Step 1: Write the test**

Create `internal/memory/embedder_test.go`:

```go
package memory

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestNewOpenAIEmbedderPassesNomicModelVerbatim(t *testing.T) {
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		capturedBody = string(buf)
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}],"model":"nomic-embed-text"}`))
	}))
	defer srv.Close()

	emb := NewOpenAIEmbedder("dummy-key", srv.URL, "nomic-embed-text")
	if emb == nil {
		t.Fatal("NewOpenAIEmbedder returned nil")
	}

	// Trigger an actual call so we can inspect the wire payload.
	_, _ = emb.Embed(context.Background(), []string{"hello"})

	if !strings.Contains(capturedBody, "nomic-embed-text") {
		t.Errorf("request payload should pass model verbatim; body=%q", capturedBody)
	}
	// Sanity: ensure we didn't silently downgrade to ada-002.
	if strings.Contains(capturedBody, "text-embedding-ada-002") {
		t.Errorf("model was silently rewritten to ada-002; body=%q", capturedBody)
	}
	_ = openai.AdaEmbeddingV2 // keep the import alive for editors
}
```

Add `"context"` to the import block.

- [ ] **Step 2: Run the test**

Run: `go test ./internal/memory -run TestNewOpenAIEmbedderPassesNomicModelVerbatim -v`
Expected: PASS.

If it fails because the `default` branch in `NewOpenAIEmbedder` does something other than passthrough, the spec is wrong about the existing behavior — update `internal/memory/embedder.go` to ensure unknown model names pass through untouched, then re-run.

- [ ] **Step 3: Commit**

```bash
git add internal/memory/embedder_test.go
git commit -m "test(memory): verify nomic-embed-text passes through verbatim"
```

---

## Final verification

After all 9 tasks complete:

- [ ] **Run full test suite:**

```bash
go test -race ./...
```

Expected: only the three pre-existing failures (`TestAssembleSystemPromptDefault`, `TestDefaultConfig` if not yet patched in Task 3, `TestChannelManagerFallbackChatID`). No new failures.

- [ ] **Manual smoke (optional, requires bundled Ollama running):**

```bash
rm -f ~/.felix/.first-run-done
./felix chat --help    # any short-lived command that triggers startup
```

Expected: `slog` logs show `first-run bootstrap start` followed by per-model pulled lines. `~/.felix/.first-run-done` exists after success.

- [ ] **Lint:**

```bash
golangci-lint run
```

Expected: clean.

- [ ] **Final commit (if any cleanup):**

```bash
git status
```

If anything uncommitted, finish with:
```bash
git add -p
git commit -m "chore: cleanup post-feature"
```
