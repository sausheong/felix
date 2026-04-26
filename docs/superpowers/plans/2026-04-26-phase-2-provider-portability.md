# Phase 2 — Provider Portability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate cross-provider tool-schema breakage and unlock extended-reasoning across all four providers, behind a unified `LLMProvider.NormalizeToolSchema` method and a 4-value `ChatRequest.Reasoning` enum.

**Architecture:** `LLMProvider` interface gains one method (`NormalizeToolSchema`) called pre-flight by the agent runtime. `ChatRequest` gains one field (`Reasoning ReasoningMode`). Each provider implements both. A new `internal/llm/llmtest` package collapses 10 ad-hoc test stubs into a shared `Base` (embeddable) + `Stub` (configurable) so future interface widenings don't churn 10 files.

**Tech Stack:** Go 1.22+, existing provider SDKs (`anthropic-sdk-go`, `sashabaranov/go-openai`, `google.golang.org/genai`, custom OpenAI-compatible HTTP for Qwen), `log/slog`, `stretchr/testify`.

---

## File Structure

**New files:**
- `internal/llm/llmtest/llmtest.go` — `Base` + `Stub` for tests
- `internal/llm/llmtest/llmtest_test.go` — tests for the stub itself
- `internal/llm/normalize.go` — shared schema-walker helpers used by per-provider normalizers
- `internal/llm/normalize_test.go` — tests for the helpers

**Modified files:**
- `internal/llm/provider.go` — add `Diagnostic`, `ReasoningMode`, widen interface, add `Reasoning` field to `ChatRequest`
- `internal/llm/anthropic.go` — add `NormalizeToolSchema`, reasoning mapping inside `ChatStream`
- `internal/llm/openai.go` — same; plus Kind-based suppression for openai-compatible/local
- `internal/llm/gemini.go` — same
- `internal/llm/qwen.go` — same; plus boolean clamping diagnostic
- `internal/agent/runtime.go` — call `NormalizeToolSchema` pre-flight, log diagnostics, set `Reasoning`
- `internal/agent/cache_stability_test.go` — extend with normalize-determinism + reasoning-prefix tests
- `internal/config/config.go` — add `Reasoning string` to `AgentConfig`, validation
- `internal/startup/startup.go` — pass `Reasoning` from config to Runtime
- 10 existing `_test.go` files in `internal/agent/`, `internal/compaction/` — embed `llmtest.Base`

**Stubs to migrate (10 total):**
1. `delayedProvider` — `internal/compaction/compaction_test.go:265`
2. `alwaysFailingProvider` — `internal/compaction/compaction_test.go:292`
3. `fakeProvider` — `internal/compaction/summarizer_test.go:17`
4. `flakyProvider` — `internal/compaction/summarizer_test.go:~95`
5. `recordingProvider` — `internal/agent/cache_stability_test.go:20`
6. `mockLLMProvider` — `internal/agent/agent_test.go:~20`
7. `statefulMockLLMProvider` — `internal/agent/agent_test.go:~400`
8. `fakeLLM` — `internal/agent/agent_test.go:481`
9. `alwaysSummary` — `internal/agent/agent_test.go:~518`
10. `cannedSummarizer` — `internal/agent/agent_test.go:~698`

---

## Pre-flight: branch off main

- [ ] **Step 0: Create the Phase 2 branch**

```bash
git checkout main
git pull --ff-only || true   # local-only OK if origin not pushed
git checkout -b phase-2-provider-portability
go test ./... -race           # baseline: must be green
```

---

## Task 1: Create `internal/llm/llmtest` package

**Files:**
- Create: `internal/llm/llmtest/llmtest.go`
- Create: `internal/llm/llmtest/llmtest_test.go`

**Why:** every existing `LLMProvider` test stub will need the new `NormalizeToolSchema` method when Task 3 widens the interface. Rather than touch 10 files now and again at every future widening, provide a shared `Base` to embed and a configurable `Stub` for the common case.

- [ ] **Step 1: Write the failing test** for `Base` and `Stub` defaults

Create `internal/llm/llmtest/llmtest_test.go`:

```go
package llmtest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/llm/llmtest"
)

func TestBaseDefaults(t *testing.T) {
	type stub struct{ llmtest.Base }
	var p llm.LLMProvider = &stub{}
	// Models must be non-nil-empty (callers may range over it).
	assert.NotNil(t, p.Models())
	// Default normalizer is identity, no diagnostics.
	tools := []llm.ToolDef{{Name: "t", Description: "d", Parameters: []byte(`{}`)}}
	out, diags := p.NormalizeToolSchema(tools)
	assert.Equal(t, tools, out)
	assert.Nil(t, diags)
}

func TestStubCannedText(t *testing.T) {
	s := &llmtest.Stub{Text: "hello"}
	ch, err := s.ChatStream(context.Background(), llm.ChatRequest{})
	require.NoError(t, err)
	var got string
	for ev := range ch {
		if ev.Type == llm.EventTextDelta {
			got += ev.Text
		}
	}
	assert.Equal(t, "hello", got)
}

func TestStubChatHookObservesRequests(t *testing.T) {
	var seen []llm.ChatRequest
	s := &llmtest.Stub{
		Text:     "ok",
		ChatHook: func(req llm.ChatRequest) { seen = append(seen, req) },
	}
	_, _ = s.ChatStream(context.Background(), llm.ChatRequest{Model: "m1"})
	_, _ = s.ChatStream(context.Background(), llm.ChatRequest{Model: "m2"})
	require.Len(t, seen, 2)
	assert.Equal(t, "m1", seen[0].Model)
	assert.Equal(t, "m2", seen[1].Model)
}

func TestStubChatErrShortCircuits(t *testing.T) {
	s := &llmtest.Stub{ChatErr: assert.AnError}
	_, err := s.ChatStream(context.Background(), llm.ChatRequest{})
	assert.Equal(t, assert.AnError, err)
}
```

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/llm/llmtest/...
```
Expected: FAIL with `package github.com/sausheong/felix/internal/llm/llmtest is not in std`.

- [ ] **Step 3: Implement the package**

Create `internal/llm/llmtest/llmtest.go`:

```go
// Package llmtest provides shared test helpers for LLMProvider stubs.
//
// Two pieces:
//   - Base: an embeddable struct that supplies default no-op
//     implementations of every LLMProvider method except ChatStream.
//     Test stubs that need custom ChatStream behavior should embed Base
//     so the LLMProvider interface can grow without churning every stub.
//   - Stub: a fully-configurable LLMProvider for the common case
//     (canned text response, optional delay, observable requests).
package llmtest

import (
	"context"
	"sync"
	"time"

	"github.com/sausheong/felix/internal/llm"
)

// Base provides default implementations of every LLMProvider method
// except ChatStream. Embed this in test stubs to avoid having to update
// every stub when the interface widens.
type Base struct{}

// Models returns an empty slice (non-nil so callers can range safely).
func (Base) Models() []llm.ModelInfo { return []llm.ModelInfo{} }

// NormalizeToolSchema is identity by default — no fields stripped, no
// diagnostics emitted.
func (Base) NormalizeToolSchema(tools []llm.ToolDef) ([]llm.ToolDef, []llm.Diagnostic) {
	return tools, nil
}

// Stub is a configurable LLMProvider for tests.
type Stub struct {
	Base

	// Text is the canned text response emitted on every ChatStream call.
	Text string
	// Delay sleeps before emitting the response. Zero means immediate.
	Delay time.Duration
	// Started is closed (once) just before the response is delayed/emitted.
	// Useful for synchronizing concurrent tests.
	Started chan struct{}
	// ChatErr, if non-nil, is returned synchronously from ChatStream.
	ChatErr error
	// ChatHook, if non-nil, observes every ChatStream request before
	// emission. Hook executes synchronously inside ChatStream.
	ChatHook func(req llm.ChatRequest)

	once sync.Once
}

func (s *Stub) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if s.ChatHook != nil {
		s.ChatHook(req)
	}
	if s.ChatErr != nil {
		return nil, s.ChatErr
	}
	if s.Started != nil {
		s.once.Do(func() { close(s.Started) })
	}
	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		if s.Delay > 0 {
			select {
			case <-time.After(s.Delay):
			case <-ctx.Done():
				ch <- llm.ChatEvent{Type: llm.EventError, Error: ctx.Err()}
				return
			}
		}
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: s.Text}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}
```

- [ ] **Step 4: Verify the test passes**

```bash
go test ./internal/llm/llmtest/... -race
```
Expected: PASS, all 4 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/llmtest/
git commit -m "feat(llm): add llmtest package with Base + Stub"
```

---

## Task 2: Migrate the 10 existing test stubs to embed `llmtest.Base`

**Files (modify):**
- `internal/compaction/compaction_test.go`
- `internal/compaction/summarizer_test.go`
- `internal/agent/agent_test.go`
- `internal/agent/cache_stability_test.go`

**Why:** When Task 3 adds `NormalizeToolSchema` to the interface, every stub that doesn't embed `Base` will fail to compile. Doing the embed first as a pure refactor isolates the change.

This task has no new tests — the existing tests ARE the test (must continue to pass).

- [ ] **Step 1: Migrate `delayedProvider`** in `internal/compaction/compaction_test.go`

Replace the struct + Models method:

```go
// Before
type delayedProvider struct {
	text    string
	delay   time.Duration
	started chan struct{}
	once    sync.Once
}
// ... ChatStream ...
func (d *delayedProvider) Models() []llm.ModelInfo { return nil }

// After
type delayedProvider struct {
	llmtest.Base
	text    string
	delay   time.Duration
	started chan struct{}
	once    sync.Once
}
// ChatStream unchanged
// Models method removed (provided by Base)
```

Add the import:
```go
import "github.com/sausheong/felix/internal/llm/llmtest"
```

- [ ] **Step 2: Migrate `alwaysFailingProvider`** in the same file

```go
// Before
type alwaysFailingProvider struct{}
func (a *alwaysFailingProvider) ChatStream(...) {...}
func (a *alwaysFailingProvider) Models() []llm.ModelInfo { return nil }

// After
type alwaysFailingProvider struct{ llmtest.Base }
func (a *alwaysFailingProvider) ChatStream(...) {...}
// Models method removed
```

- [ ] **Step 3: Migrate `fakeProvider` and `flakyProvider`** in `internal/compaction/summarizer_test.go`

Both get `llmtest.Base` embedded; both `Models()` methods removed; import added.

- [ ] **Step 4: Migrate `recordingProvider`** in `internal/agent/cache_stability_test.go`

```go
type recordingProvider struct {
	llmtest.Base
	mu       sync.Mutex
	requests []llm.ChatRequest
	reply    string
}
// ChatStream unchanged
// Models() removed
```

Add import for `llmtest`.

- [ ] **Step 5: Migrate the 5 stubs in `internal/agent/agent_test.go`**

`mockLLMProvider`, `statefulMockLLMProvider`, `fakeLLM`, `alwaysSummary`, `cannedSummarizer` — each gets `llmtest.Base` embedded and `Models()` removed.

- [ ] **Step 6: Run the full suite**

```bash
go test ./... -race
```
Expected: PASS — pure refactor, no behavior change.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/ internal/compaction/
git commit -m "refactor(test): migrate 10 LLMProvider stubs to embed llmtest.Base"
```

---

## Task 3: Add `Diagnostic` type and `NormalizeToolSchema` to the interface

**Files:**
- Modify: `internal/llm/provider.go`
- Test: existing tests (interface change is verified by compilation + existing test stubs all returning identity via `Base`)

- [ ] **Step 1: Write the failing test** that proves the interface has the new method

Append to `internal/llm/provider_test.go`:

```go
func TestLLMProviderInterfaceHasNormalizeToolSchema(t *testing.T) {
	// Compile-time: any type implementing LLMProvider must have
	// NormalizeToolSchema. This test exists to fail loudly if the
	// interface is reverted.
	var _ llm.LLMProvider = &llmtest.Stub{}
	s := &llmtest.Stub{}
	tools := []llm.ToolDef{{Name: "x", Description: "y", Parameters: []byte(`{}`)}}
	out, diags := s.NormalizeToolSchema(tools)
	assert.Equal(t, tools, out)
	assert.Nil(t, diags)
}

func TestDiagnosticFields(t *testing.T) {
	d := llm.Diagnostic{
		ToolName: "read_file",
		Field:    "properties.url.format",
		Action:   "stripped",
		Reason:   "gemini does not support format",
	}
	assert.Equal(t, "read_file", d.ToolName)
	assert.Equal(t, "stripped", d.Action)
}
```

Add imports:
```go
"github.com/sausheong/felix/internal/llm/llmtest"
```

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/llm/... -run "TestLLMProviderInterfaceHasNormalizeToolSchema|TestDiagnosticFields"
```
Expected: FAIL — `Diagnostic` undefined, `NormalizeToolSchema` not in interface.

- [ ] **Step 3: Add the type and widen the interface** in `internal/llm/provider.go`

Insert after the `ToolDef` struct (around line 50):

```go
// Diagnostic describes a single normalization or clamping event emitted
// by NormalizeToolSchema or per-provider reasoning mapping. The runtime
// logs these via slog; they are advisory, not fatal.
type Diagnostic struct {
	ToolName string // empty for non-tool diagnostics (e.g., reasoning)
	Field    string // dotted JSON path: "properties.url.format" — empty for whole-tool actions
	Action   string // "stripped" | "rewritten" | "rejected" | "clamped" | "ignored"
	Reason   string // human-readable; safe to log
}
```

Update the interface (around line 85):

```go
// LLMProvider is the interface for all LLM backends.
type LLMProvider interface {
	ChatStream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
	Models() []ModelInfo
	// NormalizeToolSchema rewrites tool JSON Schemas to the subset the
	// provider accepts. Returns the normalized tool list (input order
	// preserved) and a diagnostic per stripped/rewritten/rejected field.
	// Implementations must be deterministic — same input → same output
	// in the same diagnostic order — to preserve prompt cache stability.
	NormalizeToolSchema(tools []ToolDef) ([]ToolDef, []Diagnostic)
}
```

- [ ] **Step 4: Verify the test passes** and the rest of the codebase compiles

```bash
go build ./...
go test ./internal/llm/... -race
```
Expected: PASS. If any existing stub doesn't embed `Base`, fix it (Task 2 should have caught all of them — re-grep if not).

- [ ] **Step 5: Run the full suite**

```bash
go test ./... -race
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/provider.go internal/llm/provider_test.go
git commit -m "feat(llm): add Diagnostic type and NormalizeToolSchema to interface"
```

---

## Task 4: Shared schema-walker helpers

**Files:**
- Create: `internal/llm/normalize.go`
- Create: `internal/llm/normalize_test.go`

**Why:** Three of the four providers (OpenAI, Qwen, Gemini) share the same recursive walk pattern (descend into `properties.*`, `items`, `additionalProperties`; strip listed top-level field names at every level). Anthropic's normalizer is identity. Pulling the walk into one helper avoids 3-way duplication and keeps the per-provider files focused on the strip set.

- [ ] **Step 1: Write the failing test**

Create `internal/llm/normalize_test.go`:

```go
package llm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripFieldsTopLevel(t *testing.T) {
	in := json.RawMessage(`{
		"type": "object",
		"properties": {"x": {"type": "string"}},
		"$ref": "#/defs/foo"
	}`)
	out, diags := StripFields("mytool", in, []string{"$ref"})

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(out, &parsed))
	_, hasRef := parsed["$ref"]
	assert.False(t, hasRef, "$ref must be stripped")
	require.Len(t, diags, 1)
	assert.Equal(t, "mytool", diags[0].ToolName)
	assert.Equal(t, "$ref", diags[0].Field)
	assert.Equal(t, "stripped", diags[0].Action)
}

func TestStripFieldsNested(t *testing.T) {
	in := json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "format": "uri"},
			"items": {
				"type": "array",
				"items": {"type": "string", "format": "email"}
			}
		}
	}`)
	out, diags := StripFields("read", in, []string{"format"})

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(out, &parsed))
	props := parsed["properties"].(map[string]any)
	url := props["url"].(map[string]any)
	_, hasFormat := url["format"]
	assert.False(t, hasFormat, "nested format under properties.url must be stripped")
	items := props["items"].(map[string]any)
	itemsItems := items["items"].(map[string]any)
	_, hasNestedFormat := itemsItems["format"]
	assert.False(t, hasNestedFormat, "format under properties.items.items must be stripped")
	assert.Len(t, diags, 2)
	assert.Equal(t, "properties.url.format", diags[0].Field)
	assert.Equal(t, "properties.items.items.format", diags[1].Field)
}

func TestStripFieldsAdditionalProperties(t *testing.T) {
	in := json.RawMessage(`{
		"type": "object",
		"additionalProperties": {"type": "string", "$ref": "#/x"}
	}`)
	out, diags := StripFields("t", in, []string{"$ref"})
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(out, &parsed))
	addl := parsed["additionalProperties"].(map[string]any)
	_, has := addl["$ref"]
	assert.False(t, has)
	require.Len(t, diags, 1)
	assert.Equal(t, "additionalProperties.$ref", diags[0].Field)
}

func TestStripFieldsNoOp(t *testing.T) {
	in := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
	out, diags := StripFields("t", in, []string{"$ref", "format"})
	assert.Equal(t, string(in), string(out), "no fields to strip → bytes unchanged (modulo whitespace)")
	assert.Empty(t, diags)
}

func TestStripFieldsDeterministic(t *testing.T) {
	// Same input must produce the same output and diagnostic order across
	// repeated calls. Required for cache stability.
	in := json.RawMessage(`{
		"properties": {
			"a": {"format": "uri"},
			"b": {"format": "email"},
			"c": {"format": "date"}
		}
	}`)
	out1, diags1 := StripFields("t", in, []string{"format"})
	out2, diags2 := StripFields("t", in, []string{"format"})
	assert.Equal(t, string(out1), string(out2))
	assert.Equal(t, diags1, diags2)
}
```

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/llm/ -run TestStripFields
```
Expected: FAIL — `StripFields undefined`.

- [ ] **Step 3: Implement `StripFields`**

Create `internal/llm/normalize.go`:

```go
package llm

import (
	"encoding/json"
	"sort"
)

// StripFields removes the given field names from a JSON Schema document
// recursively. It descends into "properties.*", "items", and
// "additionalProperties" (which is the standard JSON Schema set of
// schema-bearing positions). Returns the rewritten schema as JSON bytes
// and one Diagnostic per stripped occurrence (ToolName set, Field set
// to the dotted JSON path).
//
// Determinism: the walker visits map keys in sorted order so output and
// diagnostics are reproducible across calls. Required for prompt cache
// stability.
func StripFields(toolName string, schema json.RawMessage, fields []string) (json.RawMessage, []Diagnostic) {
	if len(fields) == 0 || len(schema) == 0 {
		return schema, nil
	}
	stripSet := make(map[string]bool, len(fields))
	for _, f := range fields {
		stripSet[f] = true
	}
	var doc any
	if err := json.Unmarshal(schema, &doc); err != nil {
		// Malformed schema — leave alone, no diagnostic. The provider
		// SDK will reject it with its own error.
		return schema, nil
	}
	var diags []Diagnostic
	stripped := walkStrip(doc, "", toolName, stripSet, &diags)
	out, err := json.Marshal(stripped)
	if err != nil {
		return schema, nil
	}
	return out, diags
}

// walkStrip is the recursive worker. It returns the (possibly rewritten)
// node and appends any diagnostics for stripped fields.
func walkStrip(node any, path string, toolName string, stripSet map[string]bool, diags *[]Diagnostic) any {
	m, ok := node.(map[string]any)
	if !ok {
		// Arrays of schemas don't appear at any of the positions we
		// descend into (items is a single schema, not a list, in the
		// flavor of JSON Schema we accept). Other scalar/array nodes
		// are leaves.
		return node
	}
	// Strip target fields at this level.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fieldPath := joinPath(path, k)
		if stripSet[k] {
			*diags = append(*diags, Diagnostic{
				ToolName: toolName,
				Field:    fieldPath,
				Action:   "stripped",
				Reason:   "field not supported by provider",
			})
			delete(m, k)
			continue
		}
		// Recurse into schema-bearing positions.
		switch k {
		case "properties":
			if props, ok := m[k].(map[string]any); ok {
				propKeys := make([]string, 0, len(props))
				for pk := range props {
					propKeys = append(propKeys, pk)
				}
				sort.Strings(propKeys)
				for _, pk := range propKeys {
					props[pk] = walkStrip(props[pk], joinPath(fieldPath, pk), toolName, stripSet, diags)
				}
			}
		case "items", "additionalProperties":
			m[k] = walkStrip(m[k], fieldPath, toolName, stripSet, diags)
		}
	}
	return m
}

func joinPath(base, leaf string) string {
	if base == "" {
		return leaf
	}
	return base + "." + leaf
}
```

- [ ] **Step 4: Verify the tests pass**

```bash
go test ./internal/llm/ -run TestStripFields -race -v
```
Expected: PASS, all 5 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/normalize.go internal/llm/normalize_test.go
git commit -m "feat(llm): add StripFields schema walker helper"
```

---

## Task 5: Anthropic `NormalizeToolSchema` (identity baseline)

**Files:**
- Modify: `internal/llm/anthropic.go`
- Modify: `internal/llm/provider_test.go` (add Anthropic-specific test)

- [ ] **Step 1: Write the failing test**

Append to `internal/llm/provider_test.go`:

```go
func TestAnthropicNormalizeToolSchemaIsIdentity(t *testing.T) {
	p := llm.NewAnthropicProvider("fake-key", "")
	tools := []llm.ToolDef{
		{
			Name: "complex",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"url": {"type": "string", "format": "uri"},
					"items": {"oneOf": [{"type": "string"}, {"type": "number"}]}
				},
				"$ref": "#/defs/x"
			}`),
		},
	}
	out, diags := p.NormalizeToolSchema(tools)
	assert.Equal(t, tools, out, "Anthropic accepts full draft-7; nothing stripped")
	assert.Empty(t, diags)
}
```

Add `"encoding/json"` import to `provider_test.go` if not already there.

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/llm/ -run TestAnthropicNormalize
```
Expected: FAIL — `(*AnthropicProvider).NormalizeToolSchema undefined`.

- [ ] **Step 3: Implement** in `internal/llm/anthropic.go`

Append to the file:

```go
// NormalizeToolSchema returns tools unchanged. Anthropic accepts the
// full JSON Schema draft-7 dialect including anyOf, oneOf, format,
// $ref, and definitions. No stripping needed.
func (p *AnthropicProvider) NormalizeToolSchema(tools []ToolDef) ([]ToolDef, []Diagnostic) {
	return tools, nil
}
```

- [ ] **Step 4: Verify the test passes**

```bash
go test ./internal/llm/ -run TestAnthropicNormalize -race
```
Expected: PASS.

- [ ] **Step 5: Run the full suite**

```bash
go test ./... -race
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/anthropic.go internal/llm/provider_test.go
git commit -m "feat(llm): Anthropic NormalizeToolSchema (identity)"
```

---

## Task 6: OpenAI `NormalizeToolSchema` (strip `$ref`, `definitions`)

**Files:**
- Modify: `internal/llm/openai.go`
- Modify: `internal/llm/provider_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/llm/provider_test.go`:

```go
func TestOpenAINormalizeToolSchemaStripsRef(t *testing.T) {
	p := llm.NewOpenAIProvider("fake-key", "")
	tools := []llm.ToolDef{
		{
			Name: "lookup",
			Parameters: json.RawMessage(`{
				"type": "object",
				"$ref": "#/defs/foo",
				"definitions": {"foo": {"type": "string"}},
				"properties": {"q": {"type": "string"}}
			}`),
		},
	}
	out, diags := p.NormalizeToolSchema(tools)
	require.Len(t, out, 1)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(out[0].Parameters, &schema))
	_, hasRef := schema["$ref"]
	_, hasDefs := schema["definitions"]
	assert.False(t, hasRef, "$ref must be stripped")
	assert.False(t, hasDefs, "definitions must be stripped")

	// anyOf/oneOf/format are kept (OpenAI accepts them).
	require.GreaterOrEqual(t, len(diags), 2)
	fields := []string{diags[0].Field, diags[1].Field}
	assert.Contains(t, fields, "$ref")
	assert.Contains(t, fields, "definitions")
}

func TestOpenAINormalizeKeepsAnyOf(t *testing.T) {
	p := llm.NewOpenAIProvider("fake-key", "")
	tools := []llm.ToolDef{{
		Name: "x",
		Parameters: json.RawMessage(`{"properties":{"v":{"anyOf":[{"type":"string"},{"type":"number"}]}}}`),
	}}
	out, diags := p.NormalizeToolSchema(tools)
	assert.Empty(t, diags)
	assert.Equal(t, tools, out)
}
```

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/llm/ -run TestOpenAINormalize
```
Expected: FAIL.

- [ ] **Step 3: Implement** in `internal/llm/openai.go`

Append:

```go
// openaiUnsupportedFields are JSON Schema fields the OpenAI function-
// calling schema rejects. anyOf/oneOf/format are accepted and kept.
var openaiUnsupportedFields = []string{"$ref", "definitions"}

// NormalizeToolSchema strips $ref and definitions from each tool's
// JSON Schema. Diagnostics list every stripped occurrence with a
// dotted path.
func (p *OpenAIProvider) NormalizeToolSchema(tools []ToolDef) ([]ToolDef, []Diagnostic) {
	out := make([]ToolDef, len(tools))
	var allDiags []Diagnostic
	for i, t := range tools {
		newParams, diags := StripFields(t.Name, t.Parameters, openaiUnsupportedFields)
		td := t
		td.Parameters = newParams
		out[i] = td
		allDiags = append(allDiags, diags...)
	}
	return out, allDiags
}
```

- [ ] **Step 4: Verify the tests pass**

```bash
go test ./internal/llm/ -run TestOpenAINormalize -race -v
```
Expected: PASS.

- [ ] **Step 5: Run the full suite**

```bash
go test ./... -race
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/openai.go internal/llm/provider_test.go
git commit -m "feat(llm): OpenAI NormalizeToolSchema strips \$ref + definitions"
```

---

## Task 7: Qwen `NormalizeToolSchema` (same set as OpenAI)

**Files:**
- Modify: `internal/llm/qwen.go`
- Modify: `internal/llm/provider_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestQwenNormalizeToolSchemaStripsRef(t *testing.T) {
	p := llm.NewQwenProvider("fake-key", "")
	tools := []llm.ToolDef{{
		Name: "lookup",
		Parameters: json.RawMessage(`{"$ref":"#/x","properties":{"q":{"type":"string"}}}`),
	}}
	out, diags := p.NormalizeToolSchema(tools)
	require.Len(t, out, 1)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(out[0].Parameters, &schema))
	_, has := schema["$ref"]
	assert.False(t, has)
	assert.NotEmpty(t, diags)
	assert.Equal(t, "$ref", diags[0].Field)
}
```

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/llm/ -run TestQwenNormalize
```
Expected: FAIL.

- [ ] **Step 3: Implement** in `internal/llm/qwen.go`

Append:

```go
// NormalizeToolSchema strips $ref and definitions from each tool's
// JSON Schema. Qwen DashScope tracks the OpenAI function-calling
// shape, so the same restricted set applies.
func (p *QwenProvider) NormalizeToolSchema(tools []ToolDef) ([]ToolDef, []Diagnostic) {
	out := make([]ToolDef, len(tools))
	var allDiags []Diagnostic
	for i, t := range tools {
		newParams, diags := StripFields(t.Name, t.Parameters, openaiUnsupportedFields)
		td := t
		td.Parameters = newParams
		out[i] = td
		allDiags = append(allDiags, diags...)
	}
	return out, allDiags
}
```

(Reuses `openaiUnsupportedFields` from `openai.go`.)

- [ ] **Step 4: Verify the test passes**

```bash
go test ./internal/llm/ -run TestQwenNormalize -race
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/qwen.go internal/llm/provider_test.go
git commit -m "feat(llm): Qwen NormalizeToolSchema strips \$ref + definitions"
```

---

## Task 8: Gemini `NormalizeToolSchema` (strip `anyOf`, `oneOf`, `not`, `$ref`, `format`)

**Files:**
- Modify: `internal/llm/gemini.go`
- Modify: `internal/llm/provider_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestGeminiNormalizeToolSchemaStripsAll(t *testing.T) {
	ctx := context.Background()
	p, err := llm.NewGeminiProvider(ctx, "fake-key")
	if err != nil {
		t.Skipf("Gemini provider construction failed (no API stub): %v", err)
	}
	tools := []llm.ToolDef{{
		Name: "fetch",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {"type": "string", "format": "uri"},
				"alt": {"anyOf": [{"type": "string"}, {"type": "null"}]}
			},
			"$ref": "#/defs/x"
		}`),
	}}
	out, diags := p.NormalizeToolSchema(tools)
	require.Len(t, out, 1)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(out[0].Parameters, &schema))

	_, hasRef := schema["$ref"]
	assert.False(t, hasRef)

	props := schema["properties"].(map[string]any)
	url := props["url"].(map[string]any)
	_, hasFormat := url["format"]
	assert.False(t, hasFormat, "format must be stripped under properties.url")

	alt := props["alt"].(map[string]any)
	_, hasAnyOf := alt["anyOf"]
	assert.False(t, hasAnyOf, "anyOf must be stripped under properties.alt")

	// Expect 3 diagnostics: $ref, properties.url.format, properties.alt.anyOf.
	require.Len(t, diags, 3)
}
```

Note: if `NewGeminiProvider` errors without a real API key, the test skips. Implementation should still allow construction with a fake key for unit tests — check current behavior before assuming.

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/llm/ -run TestGeminiNormalize
```
Expected: FAIL.

- [ ] **Step 3: Implement** in `internal/llm/gemini.go`

Append:

```go
// geminiUnsupportedFields are JSON Schema fields Gemini's "OpenAPI 3.0
// subset" rejects. This is the broadest strip set across the four
// providers — most of the cross-provider portability gap shows up here.
var geminiUnsupportedFields = []string{"anyOf", "oneOf", "not", "$ref", "format"}

// NormalizeToolSchema strips fields incompatible with Gemini's OpenAPI
// 3.0 subset.
func (p *GeminiProvider) NormalizeToolSchema(tools []ToolDef) ([]ToolDef, []Diagnostic) {
	out := make([]ToolDef, len(tools))
	var allDiags []Diagnostic
	for i, t := range tools {
		newParams, diags := StripFields(t.Name, t.Parameters, geminiUnsupportedFields)
		td := t
		td.Parameters = newParams
		out[i] = td
		allDiags = append(allDiags, diags...)
	}
	return out, allDiags
}
```

- [ ] **Step 4: Verify the test passes**

```bash
go test ./internal/llm/ -run TestGeminiNormalize -race
```
Expected: PASS (or SKIP if Gemini construction needs network — adjust the test accordingly using a build-tag or mock).

- [ ] **Step 5: Run full suite**

```bash
go test ./... -race
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/gemini.go internal/llm/provider_test.go
git commit -m "feat(llm): Gemini NormalizeToolSchema strips OpenAPI-incompatible fields"
```

---

## Task 9: Wire `NormalizeToolSchema` into the agent runtime

**Files:**
- Modify: `internal/agent/runtime.go` (around line 241, where `toolDefs := r.Tools.ToolDefs()` is called)
- Modify: `internal/agent/cache_stability_test.go` (add normalize-determinism test)

- [ ] **Step 1: Write the failing regression test** for normalize-determinism across turns

Append to `internal/agent/cache_stability_test.go`:

```go
// TestNormalizeToolSchemaIsDeterministicAcrossTurns guards against drift
// in the normalizer output between turns. The same agent + same tools
// must produce byte-identical normalized tool definitions on every
// turn — otherwise the prompt cache breaks.
func TestNormalizeToolSchemaIsDeterministicAcrossTurns(t *testing.T) {
	rec := &recordingProvider{reply: "ok"}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	// Register a tool whose schema has fields some providers strip,
	// to exercise the normalizer.
	reg.Register(&mockTool{
		name: "fetch",
		schema: []byte(`{
			"type":"object",
			"properties":{"url":{"type":"string","format":"uri"}}
		}`),
		output: "ok",
	})

	rt := &Runtime{
		LLM:       rec,
		Tools:     reg,
		Session:   sess,
		Model:     "rec-model",
		Workspace: t.TempDir(),
		MaxTurns:  3,
	}

	for i := 0; i < 3; i++ {
		events, err := rt.Run(context.Background(), "ping", nil)
		require.NoError(t, err)
		for range events {
		}
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.GreaterOrEqual(t, len(rec.requests), 3)

	// All three turns must have the same Tools slice contents.
	for i := 1; i < len(rec.requests); i++ {
		require.Equal(t, len(rec.requests[0].Tools), len(rec.requests[i].Tools))
		for j := range rec.requests[0].Tools {
			assert.Equal(t,
				string(rec.requests[0].Tools[j].Parameters),
				string(rec.requests[i].Tools[j].Parameters),
				"turn %d tool %d parameters differ from turn 0", i, j)
		}
	}
}
```

You may need to add a `schema []byte` field to `mockTool` if it doesn't exist; default to `[]byte('{"type":"object"}')` when unset. Check `internal/agent/agent_test.go` for the existing mockTool definition and extend if needed.

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/agent/ -run TestNormalizeToolSchemaIsDeterministicAcrossTurns
```
Expected: FAIL — without runtime wiring, `rec.requests[i].Tools` still has the un-normalized `format` field. (Or PASS trivially if the recordingProvider doesn't normalize — that's also OK as long as the test still meaningfully covers the runtime call. Adjust to use a real provider stub that strips, to make the test meaningful.)

Actually — `recordingProvider` embeds `llmtest.Base` which is identity. To make this test meaningful, we need a stub whose `NormalizeToolSchema` strips. Add this stub locally in the test file:

```go
type strippingRecordingProvider struct {
	llmtest.Base
	mu       sync.Mutex
	requests []llm.ChatRequest
	reply    string
}

func (p *strippingRecordingProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.reply}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (p *strippingRecordingProvider) NormalizeToolSchema(tools []llm.ToolDef) ([]llm.ToolDef, []llm.Diagnostic) {
	out := make([]llm.ToolDef, len(tools))
	var diags []llm.Diagnostic
	for i, t := range tools {
		newParams, d := llm.StripFields(t.Name, t.Parameters, []string{"format"})
		td := t
		td.Parameters = newParams
		out[i] = td
		diags = append(diags, d...)
	}
	return out, diags
}
```

Use `&strippingRecordingProvider{reply: "ok"}` in the new test instead of `recordingProvider`.

Now run:
```bash
go test ./internal/agent/ -run TestNormalizeToolSchemaIsDeterministicAcrossTurns
```
Expected: FAIL — runtime doesn't call `NormalizeToolSchema`, so the recorded `req.Tools` still has the original `format` field. The test asserts that the request bytes are deterministic AND that they reflect normalization. Without the wiring, the recorded tools are byte-identical (so determinism passes) but the test should additionally assert the format field is gone, which it isn't yet. Add:

```go
// Also verify normalization actually happened.
var schema map[string]any
require.NoError(t, json.Unmarshal(rec.requests[0].Tools[0].Parameters, &schema))
props := schema["properties"].(map[string]any)
url := props["url"].(map[string]any)
_, hasFormat := url["format"]
assert.False(t, hasFormat, "runtime must call NormalizeToolSchema; format should be stripped")
```

Add `"encoding/json"` import.

- [ ] **Step 3: Wire it in** `internal/agent/runtime.go`

Find the block near line 241 where `toolDefs := r.Tools.ToolDefs()` is set. Replace with:

```go
toolDefs := r.Tools.ToolDefs()
toolDefs, diags := r.LLM.NormalizeToolSchema(toolDefs)
for _, d := range diags {
	slog.Warn("tool schema normalized",
		"tool", d.ToolName,
		"field", d.Field,
		"action", d.Action,
		"reason", d.Reason)
}
tr.Mark("context.assemble", "turn", turn, "msgs", len(msgs), "tools", len(toolDefs), "sysprompt_chars", len(systemPrompt), "dur_ms_local", time.Since(phaseStart).Milliseconds())
```

(Slot the `NormalizeToolSchema` call between `ToolDefs()` and the existing `tr.Mark` line. The `tr.Mark` is already there; just keep it after the normalization.)

Add `"log/slog"` import to `runtime.go` if not already there (likely is).

- [ ] **Step 4: Verify the test passes**

```bash
go test ./internal/agent/ -run TestNormalizeToolSchemaIsDeterministicAcrossTurns -race
```
Expected: PASS.

- [ ] **Step 5: Run the full suite**

```bash
go test ./... -race
```
Expected: PASS. The existing `TestRequestPrefixIsByteStableAcrossTurns` should still pass (recordingProvider's identity normalize is a no-op).

- [ ] **Step 6: Commit**

```bash
git add internal/agent/runtime.go internal/agent/cache_stability_test.go
git commit -m "feat(agent): wire NormalizeToolSchema pre-flight + log diagnostics"
```

---

## Task 10: Add `ReasoningMode` type and `ChatRequest.Reasoning` field

**Files:**
- Modify: `internal/llm/provider.go`
- Modify: `internal/llm/provider_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/llm/provider_test.go`:

```go
func TestReasoningModeConstants(t *testing.T) {
	assert.Equal(t, llm.ReasoningMode(""), llm.ReasoningOff)
	assert.Equal(t, llm.ReasoningMode("low"), llm.ReasoningLow)
	assert.Equal(t, llm.ReasoningMode("medium"), llm.ReasoningMedium)
	assert.Equal(t, llm.ReasoningMode("high"), llm.ReasoningHigh)
}

func TestChatRequestReasoningZeroValue(t *testing.T) {
	// Existing call sites that don't set Reasoning must keep working —
	// zero value must equal ReasoningOff.
	var req llm.ChatRequest
	assert.Equal(t, llm.ReasoningOff, req.Reasoning)
}

func TestParseReasoningMode(t *testing.T) {
	cases := map[string]llm.ReasoningMode{
		"":       llm.ReasoningOff,
		"off":    llm.ReasoningOff,
		"low":    llm.ReasoningLow,
		"medium": llm.ReasoningMedium,
		"high":   llm.ReasoningHigh,
	}
	for in, want := range cases {
		got, err := llm.ParseReasoningMode(in)
		require.NoError(t, err, "input %q", in)
		assert.Equal(t, want, got, "input %q", in)
	}
	_, err := llm.ParseReasoningMode("ultra")
	assert.Error(t, err, "unknown level must error")
}
```

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/llm/ -run "TestReasoning|TestChatRequest|TestParseReasoning"
```
Expected: FAIL — `ReasoningMode`, constants, and `ParseReasoningMode` all undefined; `Reasoning` not on `ChatRequest`.

- [ ] **Step 3: Implement** in `internal/llm/provider.go`

After the `Diagnostic` type (added in Task 3), add:

```go
// ReasoningMode is the unified reasoning/thinking knob across providers.
// Zero value (ReasoningOff) means no extended reasoning — safe default
// for existing call sites that don't set the field. Each provider maps
// this to its native config (Anthropic thinking budget, OpenAI
// reasoning_effort, Gemini ThinkingConfig, Qwen enable_thinking).
type ReasoningMode string

const (
	ReasoningOff    ReasoningMode = ""
	ReasoningLow    ReasoningMode = "low"
	ReasoningMedium ReasoningMode = "medium"
	ReasoningHigh   ReasoningMode = "high"
)

// ParseReasoningMode parses a config string into a ReasoningMode.
// Accepts "", "off", "low", "medium", "high" (case-sensitive).
// Returns an error for unknown values.
func ParseReasoningMode(s string) (ReasoningMode, error) {
	switch s {
	case "", "off":
		return ReasoningOff, nil
	case "low":
		return ReasoningLow, nil
	case "medium":
		return ReasoningMedium, nil
	case "high":
		return ReasoningHigh, nil
	default:
		return ReasoningOff, fmt.Errorf("unknown reasoning mode %q (want off|low|medium|high)", s)
	}
}
```

Add `Reasoning ReasoningMode` to `ChatRequest`:

```go
type ChatRequest struct {
	Model        string
	Messages     []Message
	Tools        []ToolDef
	MaxTokens    int
	Temperature  float64
	SystemPrompt string
	Reasoning    ReasoningMode // NEW; zero value = ReasoningOff
}
```

- [ ] **Step 4: Verify the tests pass**

```bash
go test ./internal/llm/ -run "TestReasoning|TestChatRequest|TestParseReasoning" -race
```
Expected: PASS.

- [ ] **Step 5: Run full suite**

```bash
go test ./... -race
```
Expected: PASS — existing call sites that don't set `Reasoning` get the zero value, which is `ReasoningOff`.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/provider.go internal/llm/provider_test.go
git commit -m "feat(llm): add ReasoningMode type + ChatRequest.Reasoning field"
```

---

## Task 11: Anthropic reasoning mapping (thinking budget tokens)

**Files:**
- Modify: `internal/llm/anthropic.go`
- Modify: `internal/llm/provider_test.go`

**Notes:** Anthropic SDK exposes thinking via `MessageNewParams.Thinking`. Check the SDK version's exact field name (likely `anthropic.ThinkingConfigParam` or similar). If unavailable in current SDK pin, add a TODO comment in the code AND in the test to upgrade SDK first — but try the current SDK first. The mapping table:

| Mode    | Budget tokens |
|---------|---------------|
| off     | omit          |
| low     | 1024          |
| medium  | 4096          |
| high    | 16384         |

Models without thinking support (`claude-3-haiku`, `claude-3-5-haiku`, `claude-3-opus`, anything before claude-3-7) get reasoning ignored with an Info-level slog.

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestAnthropicReasoningOff(t *testing.T) {
	p := llm.NewAnthropicProvider("fake", "")
	// Off should produce no thinking config — verify by inspecting the
	// builder helper directly (not via ChatStream which needs network).
	cfg, ok := p.BuildThinkingConfig("claude-sonnet-4-5", llm.ReasoningOff)
	assert.False(t, ok, "off → no thinking config")
	assert.Nil(t, cfg)
}

func TestAnthropicReasoningLevels(t *testing.T) {
	p := llm.NewAnthropicProvider("fake", "")
	cases := map[llm.ReasoningMode]int64{
		llm.ReasoningLow:    1024,
		llm.ReasoningMedium: 4096,
		llm.ReasoningHigh:   16384,
	}
	for mode, wantBudget := range cases {
		cfg, ok := p.BuildThinkingConfig("claude-sonnet-4-5", mode)
		require.True(t, ok, "mode %s should produce config", mode)
		require.NotNil(t, cfg)
		assert.Equal(t, wantBudget, cfg.BudgetTokens, "mode %s budget", mode)
	}
}

func TestAnthropicReasoningUnsupportedModel(t *testing.T) {
	p := llm.NewAnthropicProvider("fake", "")
	cfg, ok := p.BuildThinkingConfig("claude-3-haiku-20240307", llm.ReasoningHigh)
	assert.False(t, ok, "haiku-3 doesn't support thinking; mode should be ignored")
	assert.Nil(t, cfg)
}
```

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/llm/ -run TestAnthropicReasoning
```
Expected: FAIL — `BuildThinkingConfig` undefined.

- [ ] **Step 3: Implement** in `internal/llm/anthropic.go`

Add a builder helper and call it from `ChatStream`. First check the Anthropic SDK for the exact thinking-config type — likely `anthropic.ThinkingConfigEnabledParam` with `BudgetTokens int64`. If the SDK pin doesn't have it, exporting a thin local wrapper struct is fine; the API call would skip Thinking but the helper return shape stays useful for future SDK upgrade.

```go
// AnthropicThinkingConfig is a provider-internal representation of
// Anthropic's thinking config. Exported for testability.
type AnthropicThinkingConfig struct {
	BudgetTokens int64
}

// BuildThinkingConfig maps a ReasoningMode to Anthropic's thinking
// budget. Returns (nil, false) when reasoning is off or the model
// doesn't support thinking. When (nil, false) is returned for a
// non-off mode on an unsupported model, the caller should emit an
// "ignored" diagnostic (handled inside ChatStream).
func (p *AnthropicProvider) BuildThinkingConfig(model string, mode ReasoningMode) (*AnthropicThinkingConfig, bool) {
	if mode == ReasoningOff {
		return nil, false
	}
	if !anthropicSupportsThinking(model) {
		return nil, false
	}
	switch mode {
	case ReasoningLow:
		return &AnthropicThinkingConfig{BudgetTokens: 1024}, true
	case ReasoningMedium:
		return &AnthropicThinkingConfig{BudgetTokens: 4096}, true
	case ReasoningHigh:
		return &AnthropicThinkingConfig{BudgetTokens: 16384}, true
	default:
		return nil, false
	}
}

// anthropicSupportsThinking returns true for Claude models that accept
// extended thinking. Conservative — unknown IDs default to true so we
// let the API decide rather than silently drop.
func anthropicSupportsThinking(model string) bool {
	// Models known NOT to support thinking.
	noThink := []string{
		"claude-3-haiku", "claude-3-5-haiku",
		"claude-3-sonnet", "claude-3-5-sonnet",
		"claude-3-opus",
	}
	for _, prefix := range noThink {
		if strings.HasPrefix(model, prefix) {
			return false
		}
	}
	return true
}
```

In `ChatStream`, after building `params` and before calling `NewStreaming`, add:

```go
if cfg, ok := p.BuildThinkingConfig(model, req.Reasoning); ok {
	// Wire to SDK. If the current pinned SDK version doesn't expose
	// Thinking on MessageNewParams, leave this commented and emit an
	// info-level diagnostic until the SDK is upgraded.
	// params.Thinking = anthropic.ThinkingConfigEnabledParam{BudgetTokens: cfg.BudgetTokens, Type: "enabled"}
	_ = cfg // until SDK wiring confirmed; remove once params.Thinking is set
	slog.Info("anthropic thinking requested",
		"model", model,
		"budget_tokens", cfg.BudgetTokens)
} else if req.Reasoning != ReasoningOff {
	slog.Info("reasoning ignored",
		"provider", "anthropic",
		"model", model,
		"requested", string(req.Reasoning),
		"reason", "model does not support thinking or sdk pin lacks support")
}
```

**SDK note:** the implementer should check the actual `anthropic` package version in `go.mod` and consult the SDK source for the right field name. If the SDK supports Thinking, uncomment and adjust. If not, the helper still works (and tests pass) — actually wiring to the API becomes a follow-up commit.

- [ ] **Step 4: Verify the tests pass**

```bash
go test ./internal/llm/ -run TestAnthropicReasoning -race
```
Expected: PASS.

- [ ] **Step 5: Run full suite**

```bash
go test ./... -race
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/anthropic.go internal/llm/provider_test.go
git commit -m "feat(llm): Anthropic reasoning mapping (thinking budget tokens)"
```

---

## Task 12: OpenAI reasoning mapping + Kind-based suppression for openai-compatible/local

**Files:**
- Modify: `internal/llm/openai.go`
- Modify: `internal/llm/provider.go` (add `Kind` accessor on the provider — see step 3)
- Modify: `internal/llm/provider_test.go`

**Mapping:**

| Mode    | OpenAI ReasoningEffort |
|---------|------------------------|
| off     | omit                   |
| low     | "low"                  |
| medium  | "medium"               |
| high    | "high"                 |

Reasoning-supporting models: `o1-*`, `o3-*`, `o4-*`, `gpt-5-*`. Others get reasoning ignored with an Info diag.

For `Kind == "openai-compatible"` or `"local"`, reasoning is suppressed regardless of model with an Info diag (`reason: "endpoint may not support reasoning_effort"`).

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestOpenAIReasoningOff(t *testing.T) {
	p := llm.NewOpenAIProvider("fake", "")
	effort, ok := p.BuildReasoningEffort("o3-mini", llm.ReasoningOff)
	assert.False(t, ok)
	assert.Empty(t, effort)
}

func TestOpenAIReasoningLevels(t *testing.T) {
	p := llm.NewOpenAIProvider("fake", "")
	cases := map[llm.ReasoningMode]string{
		llm.ReasoningLow:    "low",
		llm.ReasoningMedium: "medium",
		llm.ReasoningHigh:   "high",
	}
	for mode, want := range cases {
		effort, ok := p.BuildReasoningEffort("o3-mini", mode)
		require.True(t, ok, "mode %s", mode)
		assert.Equal(t, want, effort)
	}
}

func TestOpenAIReasoningUnsupportedModel(t *testing.T) {
	p := llm.NewOpenAIProvider("fake", "")
	effort, ok := p.BuildReasoningEffort("gpt-4o", llm.ReasoningHigh)
	assert.False(t, ok, "gpt-4o does not support reasoning_effort")
	assert.Empty(t, effort)
}

func TestOpenAICompatibleSuppressesReasoning(t *testing.T) {
	// When the provider is built with Kind=openai-compatible (e.g.,
	// pointing at Ollama), reasoning is suppressed regardless of model.
	p := llm.NewOpenAIProviderWithKind("", "http://localhost:11434/v1", "openai-compatible")
	_, ok := p.BuildReasoningEffort("gpt-5-thinking", llm.ReasoningHigh)
	assert.False(t, ok, "openai-compatible kind suppresses reasoning")
}
```

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/llm/ -run TestOpenAIReasoning
```
Expected: FAIL.

- [ ] **Step 3: Implement** in `internal/llm/openai.go`

The OpenAI provider needs to remember its `Kind`. Add a constructor variant or extend the struct. The least-invasive change: add a `kind string` field to `OpenAIProvider`, set via a new `NewOpenAIProviderWithKind` constructor, default to `"openai"` from the existing `NewOpenAIProvider`.

```go
// OpenAIProvider holds the OpenAI client + the resolved provider kind.
// Kind is "openai", "openai-compatible", or "local"; affects reasoning
// suppression (compat/local endpoints may not support reasoning_effort).
type OpenAIProvider struct {
	client *openai.Client
	kind   string
}

func NewOpenAIProvider(apiKey, baseURL string) *OpenAIProvider {
	return NewOpenAIProviderWithKind(apiKey, baseURL, "openai")
}

func NewOpenAIProviderWithKind(apiKey, baseURL, kind string) *OpenAIProvider {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	return &OpenAIProvider{
		client: openai.NewClientWithConfig(cfg),
		kind:   kind,
	}
}

// BuildReasoningEffort maps a ReasoningMode to OpenAI's reasoning_effort
// string. Returns ("", false) when reasoning is off, the model doesn't
// support it, or the provider Kind suppresses it (openai-compatible /
// local).
func (p *OpenAIProvider) BuildReasoningEffort(model string, mode ReasoningMode) (string, bool) {
	if mode == ReasoningOff {
		return "", false
	}
	if p.kind == "openai-compatible" || p.kind == "local" {
		return "", false
	}
	if !openaiSupportsReasoning(model) {
		return "", false
	}
	switch mode {
	case ReasoningLow:
		return "low", true
	case ReasoningMedium:
		return "medium", true
	case ReasoningHigh:
		return "high", true
	default:
		return "", false
	}
}

func openaiSupportsReasoning(model string) bool {
	prefixes := []string{"o1-", "o3-", "o4-", "gpt-5"}
	for _, p := range prefixes {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}
```

In `ChatStream`, just before the API call, add:

```go
if effort, ok := p.BuildReasoningEffort(model, req.Reasoning); ok {
	// SDK field name varies by version; check go-openai docs for the
	// exact field — likely openaiReq.ReasoningEffort or
	// openaiReq.Reasoning.Effort. If the pinned version lacks it,
	// log + skip (the request still goes through, just without
	// reasoning).
	// openaiReq.ReasoningEffort = effort
	_ = effort
	slog.Info("openai reasoning requested", "model", model, "effort", effort)
} else if req.Reasoning != ReasoningOff {
	reason := "model does not support reasoning_effort"
	if p.kind == "openai-compatible" || p.kind == "local" {
		reason = "endpoint may not support reasoning_effort"
	}
	slog.Info("reasoning ignored",
		"provider", "openai",
		"kind", p.kind,
		"model", model,
		"requested", string(req.Reasoning),
		"reason", reason)
}
```

Update `provider.go` `NewProvider`:

```go
case "openai":
	return NewOpenAIProviderWithKind(opts.APIKey, opts.BaseURL, "openai"), nil
case "openai-compatible":
	return NewOpenAIProviderWithKind(opts.APIKey, opts.BaseURL, "openai-compatible"), nil
case "local":
	return NewOpenAIProviderWithKind("", opts.BaseURL, "local"), nil
```

- [ ] **Step 4: Verify the tests pass**

```bash
go test ./internal/llm/ -run TestOpenAIReasoning -race
```
Expected: PASS.

- [ ] **Step 5: Run full suite**

```bash
go test ./... -race
```
Expected: PASS — `NewOpenAIProvider` keeps its old signature so no callers break.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/openai.go internal/llm/provider.go internal/llm/provider_test.go
git commit -m "feat(llm): OpenAI reasoning mapping + suppress for openai-compatible/local"
```

---

## Task 13: Gemini reasoning mapping (ThinkingConfig)

**Files:**
- Modify: `internal/llm/gemini.go`
- Modify: `internal/llm/provider_test.go`

**Mapping:**

| Mode    | Gemini ThinkingBudget |
|---------|------------------------|
| off     | 0                      |
| low     | 1024                   |
| medium  | 4096                   |
| high    | 16384                  |

Reasoning-supporting models: `gemini-2.0-flash-thinking-*`, `gemini-2.5-*`. Others get reasoning ignored with an Info diag.

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestGeminiReasoningOff(t *testing.T) {
	p, err := llm.NewGeminiProvider(context.Background(), "fake")
	if err != nil {
		t.Skipf("gemini construction failed: %v", err)
	}
	budget, ok := p.BuildThinkingBudget("gemini-2.5-pro", llm.ReasoningOff)
	assert.False(t, ok)
	assert.Equal(t, int32(0), budget)
}

func TestGeminiReasoningLevels(t *testing.T) {
	p, err := llm.NewGeminiProvider(context.Background(), "fake")
	if err != nil {
		t.Skipf("gemini construction failed: %v", err)
	}
	cases := map[llm.ReasoningMode]int32{
		llm.ReasoningLow:    1024,
		llm.ReasoningMedium: 4096,
		llm.ReasoningHigh:   16384,
	}
	for mode, want := range cases {
		budget, ok := p.BuildThinkingBudget("gemini-2.5-pro", mode)
		require.True(t, ok, "mode %s", mode)
		assert.Equal(t, want, budget)
	}
}

func TestGeminiReasoningUnsupportedModel(t *testing.T) {
	p, err := llm.NewGeminiProvider(context.Background(), "fake")
	if err != nil {
		t.Skipf("gemini construction failed: %v", err)
	}
	_, ok := p.BuildThinkingBudget("gemini-1.5-flash", llm.ReasoningHigh)
	assert.False(t, ok, "gemini-1.5-flash does not support thinking")
}
```

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/llm/ -run TestGeminiReasoning
```
Expected: FAIL.

- [ ] **Step 3: Implement** in `internal/llm/gemini.go`

```go
// BuildThinkingBudget maps a ReasoningMode to Gemini's thinking budget
// (in tokens). Returns (0, false) when reasoning is off or the model
// does not support thinking.
func (p *GeminiProvider) BuildThinkingBudget(model string, mode ReasoningMode) (int32, bool) {
	if mode == ReasoningOff {
		return 0, false
	}
	if !geminiSupportsThinking(model) {
		return 0, false
	}
	switch mode {
	case ReasoningLow:
		return 1024, true
	case ReasoningMedium:
		return 4096, true
	case ReasoningHigh:
		return 16384, true
	default:
		return 0, false
	}
}

func geminiSupportsThinking(model string) bool {
	prefixes := []string{"gemini-2.0-flash-thinking", "gemini-2.5"}
	for _, p := range prefixes {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}
```

In `ChatStream` before the API call:

```go
if budget, ok := p.BuildThinkingBudget(model, req.Reasoning); ok {
	// SDK field: genai.ThinkingConfig{ThinkingBudget: &budget}
	// Wire when SDK pin supports it.
	// config.ThinkingConfig = &genai.ThinkingConfig{ThinkingBudget: &budget}
	_ = budget
	slog.Info("gemini thinking requested", "model", model, "budget_tokens", budget)
} else if req.Reasoning != ReasoningOff {
	slog.Info("reasoning ignored",
		"provider", "gemini",
		"model", model,
		"requested", string(req.Reasoning),
		"reason", "model does not support thinking")
}
```

- [ ] **Step 4: Verify the tests pass**

```bash
go test ./internal/llm/ -run TestGeminiReasoning -race
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/gemini.go internal/llm/provider_test.go
git commit -m "feat(llm): Gemini reasoning mapping (ThinkingConfig)"
```

---

## Task 14: Qwen reasoning mapping (boolean enable_thinking with clamping diagnostic)

**Files:**
- Modify: `internal/llm/qwen.go`
- Modify: `internal/llm/provider_test.go`

**Mapping:**

| Mode    | enable_thinking | clamping diag |
|---------|-----------------|---------------|
| off     | false           | none          |
| low     | true            | yes           |
| medium  | true            | yes           |
| high    | true            | yes           |

Any non-off mode emits a diagnostic because the boolean toggle loses the granularity. Reasoning-supporting models: `qwen-qwq-*`, `qwen3-*`. Others get reasoning ignored.

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestQwenReasoningOff(t *testing.T) {
	p := llm.NewQwenProvider("fake", "")
	enabled, diag, ok := p.BuildEnableThinking("qwen3-32b", llm.ReasoningOff)
	assert.False(t, ok)
	assert.False(t, enabled)
	assert.Empty(t, diag.Action)
}

func TestQwenReasoningClamps(t *testing.T) {
	p := llm.NewQwenProvider("fake", "")
	for _, mode := range []llm.ReasoningMode{llm.ReasoningLow, llm.ReasoningMedium, llm.ReasoningHigh} {
		enabled, diag, ok := p.BuildEnableThinking("qwen3-32b", mode)
		require.True(t, ok, "mode %s should produce config", mode)
		assert.True(t, enabled, "mode %s → enable_thinking=true", mode)
		assert.Equal(t, "clamped", diag.Action, "any non-off mode emits clamped diagnostic")
		assert.Contains(t, diag.Reason, "boolean")
	}
}

func TestQwenReasoningUnsupportedModel(t *testing.T) {
	p := llm.NewQwenProvider("fake", "")
	_, _, ok := p.BuildEnableThinking("qwen-turbo", llm.ReasoningHigh)
	assert.False(t, ok, "qwen-turbo does not support thinking")
}
```

- [ ] **Step 2: Verify the test fails**

```bash
go test ./internal/llm/ -run TestQwenReasoning
```
Expected: FAIL.

- [ ] **Step 3: Implement** in `internal/llm/qwen.go`

```go
// BuildEnableThinking maps a ReasoningMode to Qwen's enable_thinking
// boolean. Returns (false, _, false) when off or the model doesn't
// support thinking. For any non-off level on a supported model, returns
// (true, diag, true) where diag is a "clamped" Diagnostic noting that
// the boolean toggle loses the requested granularity.
func (p *QwenProvider) BuildEnableThinking(model string, mode ReasoningMode) (bool, Diagnostic, bool) {
	if mode == ReasoningOff {
		return false, Diagnostic{}, false
	}
	if !qwenSupportsThinking(model) {
		return false, Diagnostic{}, false
	}
	return true, Diagnostic{
		Action: "clamped",
		Reason: "qwen reasoning is boolean; granularity ignored",
	}, true
}

func qwenSupportsThinking(model string) bool {
	prefixes := []string{"qwen-qwq", "qwen3"}
	for _, p := range prefixes {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}
```

In `ChatStream` before the API call:

```go
if enabled, diag, ok := p.BuildEnableThinking(model, req.Reasoning); ok {
	// SDK / HTTP field: top-level "enable_thinking": true on Qwen
	// DashScope. Wire when adding the field to the outgoing JSON.
	_ = enabled
	slog.Info("qwen thinking requested",
		"model", model,
		"requested", string(req.Reasoning),
		"clamped_to_bool", true,
		"reason", diag.Reason)
} else if req.Reasoning != ReasoningOff {
	slog.Info("reasoning ignored",
		"provider", "qwen",
		"model", model,
		"requested", string(req.Reasoning),
		"reason", "model does not support thinking")
}
```

- [ ] **Step 4: Verify the tests pass**

```bash
go test ./internal/llm/ -run TestQwenReasoning -race
```
Expected: PASS.

- [ ] **Step 5: Run full suite**

```bash
go test ./... -race
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/qwen.go internal/llm/provider_test.go
git commit -m "feat(llm): Qwen reasoning mapping (enable_thinking + clamping diag)"
```

---

## Task 15: Agent config plumbing — `AgentConfig.Reasoning`

**Files:**
- Modify: `internal/config/config.go` (add field, add validation)
- Modify: `internal/config/config_test.go` (validation test)
- Modify: `internal/agent/runtime.go` (read field, set on `ChatRequest`)
- Modify: `internal/startup/startup.go` (3 sites — line 546, 595, 642 — pass `Reasoning` from agentCfg into `agent.Runtime{...}`)
- Modify: `internal/agent/cache_stability_test.go` (extend test for reasoning-prefix stability)

- [ ] **Step 1: Write the failing test** for config validation

Append to `internal/config/config_test.go`:

```go
func TestAgentConfigReasoningValidation(t *testing.T) {
	cases := map[string]bool{
		"":       true, // empty = off
		"off":    true,
		"low":    true,
		"medium": true,
		"high":   true,
		"ultra":  false,
		"LOW":    false, // case-sensitive
	}
	for in, wantOK := range cases {
		err := config.ValidateReasoningMode(in)
		if wantOK {
			assert.NoError(t, err, "input %q", in)
		} else {
			assert.Error(t, err, "input %q", in)
		}
	}
}
```

- [ ] **Step 2: Write the failing test** for runtime + cache stability

Append to `internal/agent/cache_stability_test.go`:

```go
// TestReasoningIsInRequestPrefix asserts that the agent's Reasoning
// setting flows into ChatRequest and remains stable across turns.
// Required for prompt cache hits with thinking enabled.
func TestReasoningIsInRequestPrefix(t *testing.T) {
	rec := &recordingProvider{reply: "ok"}
	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()

	rt := &Runtime{
		LLM:       rec,
		Tools:     reg,
		Session:   sess,
		Model:     "rec-model",
		Workspace: t.TempDir(),
		MaxTurns:  3,
		Reasoning: llm.ReasoningHigh,
	}

	for i := 0; i < 2; i++ {
		events, err := rt.Run(context.Background(), "ping", nil)
		require.NoError(t, err)
		for range events {
		}
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.GreaterOrEqual(t, len(rec.requests), 2)
	assert.Equal(t, llm.ReasoningHigh, rec.requests[0].Reasoning)
	assert.Equal(t, llm.ReasoningHigh, rec.requests[1].Reasoning,
		"reasoning level must be stable across turns")
}
```

- [ ] **Step 3: Verify the tests fail**

```bash
go test ./internal/config/ -run TestAgentConfigReasoningValidation
go test ./internal/agent/ -run TestReasoningIsInRequestPrefix
```
Expected: BOTH FAIL — `ValidateReasoningMode` undefined; `Runtime.Reasoning` undefined.

- [ ] **Step 4: Add the field + validation** in `internal/config/config.go`

In the `AgentConfig` struct (around line 155), add:

```go
type AgentConfig struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Workspace    string       `json:"workspace"`
	Model        string       `json:"model"`
	Reasoning    string       `json:"reasoning,omitempty"` // off | low | medium | high; default off
	Fallbacks    []string     `json:"fallbacks"`
	Sandbox      string       `json:"sandbox"`
	MaxTurns     int          `json:"maxTurns,omitempty"`
	SystemPrompt string       `json:"system_prompt,omitempty"`
	Tools        ToolPolicy   `json:"tools"`
	Cron         []CronConfig `json:"cron,omitempty"`
}
```

Add the validator at the end of the file:

```go
// ValidateReasoningMode returns nil for "", "off", "low", "medium",
// "high"; an error otherwise. Case-sensitive.
func ValidateReasoningMode(s string) error {
	switch s {
	case "", "off", "low", "medium", "high":
		return nil
	default:
		return fmt.Errorf("reasoning %q invalid (want off|low|medium|high)", s)
	}
}
```

Add `"fmt"` import if not present.

In the `Load` (or equivalent) function that processes loaded config, call `ValidateReasoningMode(a.Reasoning)` for each agent and return an error if any fails. Find the existing per-agent validation loop (look for `for _, a := range cfg.Agents.List`) and add the check there.

- [ ] **Step 5: Add `Reasoning` to `Runtime`** in `internal/agent/runtime.go`

In the `Runtime` struct (line 49), add:

```go
type Runtime struct {
	LLM          llm.LLMProvider
	Tools        tools.Executor
	Session      *session.Session
	AgentID      string
	AgentName    string
	Model        string
	Reasoning    llm.ReasoningMode // NEW; zero value = ReasoningOff
	Workspace    string
	MaxTurns     int
	SystemPrompt string
	Skills       *skill.Loader
	Memory       *memory.Manager
	Cortex       *cortex.Cortex
	Compaction   *compaction.Manager
	IngestSource string
	calibrator   *tokens.Calibrator
}
```

In the `req := llm.ChatRequest{...}` block (around line 287), add:

```go
req := llm.ChatRequest{
	Model:        r.Model,
	Messages:     msgs,
	Tools:        toolDefs,
	MaxTokens:    8192,
	SystemPrompt: systemPrompt,
	Reasoning:    r.Reasoning,
}
```

- [ ] **Step 6: Wire startup** — `internal/startup/startup.go`

For each of the three `agent.Runtime{...}` construction sites (around lines 546, 595, 642), add the `Reasoning` field. Compute it once per agent before constructing:

```go
reasoning, err := llm.ParseReasoningMode(agentCfg.Reasoning)
if err != nil {
	slog.Error("invalid reasoning mode in agent config; defaulting to off",
		"agent", agentCfg.ID, "value", agentCfg.Reasoning, "err", err)
	reasoning = llm.ReasoningOff
}
```

Then pass `Reasoning: reasoning,` into each `agent.Runtime{...}` literal.

- [ ] **Step 7: Verify the tests pass**

```bash
go test ./internal/config/ -run TestAgentConfigReasoningValidation -race
go test ./internal/agent/ -run TestReasoningIsInRequestPrefix -race
```
Expected: BOTH PASS.

- [ ] **Step 8: Run full suite**

```bash
go test ./... -race
go build ./...
```
Expected: PASS, build succeeds.

- [ ] **Step 9: Commit**

```bash
git add internal/config/ internal/agent/ internal/startup/
git commit -m "feat(agent): wire AgentConfig.Reasoning through Runtime to ChatRequest"
```

---

## Final integration check

- [ ] **Step 1: Run full test suite with race detector**

```bash
go test ./... -race
```
Expected: ALL PASS.

- [ ] **Step 2: Verify no diagnostics leak from real provider construction**

```bash
go vet ./...
golangci-lint run 2>/dev/null || true
```
Expected: no new issues.

- [ ] **Step 3: Sanity check — list new commits on the branch**

```bash
git log --oneline main..HEAD
```
Expected: 14-15 commits (one per task plus pre-flight).

- [ ] **Step 4: Merge to main**

After spec-then-code-quality review of the final state passes:

```bash
git checkout main
git merge --no-ff phase-2-provider-portability -m "Merge branch 'phase-2-provider-portability' — Phase 2: provider portability"
go test ./... -race
```
Expected: clean merge, suite green.

---

## Notes for the implementer

**Anthropic / OpenAI / Gemini SDK wiring:** the tests (Tasks 11-13) verify the per-provider helper functions (`BuildThinkingConfig`, `BuildReasoningEffort`, `BuildThinkingBudget`). Wiring those values into the actual SDK call inside `ChatStream` is shown commented-out in the implementation steps, with `_ = cfg` placeholders. **Before completing each task, check the pinned SDK version's actual field names** and uncomment + adjust. If the SDK doesn't expose the field, the helper-only state is acceptable — the diagnostic still logs that reasoning was requested. Open a follow-up issue to upgrade the SDK and wire the field.

**No tool-result truncation needed for normalization.** `NormalizeToolSchema` walks tool *definitions*, not tool results. Tool results never reach the normalizer.

**Cache stability is non-negotiable.** If any task introduces non-deterministic output (random ordering, timestamps, anything keyed on call count), the cache-stability tests in Task 9 and Task 15 will catch it. Don't skip them.

**Anthropic SDK upgrade (potential follow-up):** if the pinned `anthropic-sdk-go` version doesn't expose `Thinking` on `MessageNewParams`, file a follow-up. Phase 2 ships the helper + diagnostic; the wiring lands when the SDK does.
