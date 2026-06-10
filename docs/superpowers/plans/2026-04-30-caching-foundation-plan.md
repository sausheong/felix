# Caching Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Anthropic prompt caching to Felix and pre-compute the static portion of the system prompt so it stops being rebuilt (with a per-turn `config.Load`) on every loop iteration.

**Architecture:** Two cache zones for Anthropic — one `cache_control` marker on the static system block, one on the last user message. Other providers consume a new `[]SystemPromptPart` field on `ChatRequest` by concatenating it back into a string (they cache automatically via byte-stable prefixes, no markers needed). Tool defs are sorted by name in the runtime hot loop. Static system prompt is built once at `BuildRuntimeForAgent` time and stored on `Runtime.StaticSystemPrompt`; the per-turn loop assembles only a small dynamic suffix (matched skills/memory/cortex hint) and ships both parts as the structured system prompt.

**Tech Stack:** Go, `github.com/anthropics/anthropic-sdk-go` v1.35.0, `testify`, `slog`.

**Spec:** `docs/superpowers/specs/2026-04-30-caching-foundation-design.md`.

---

## Task 1: Extend `llm.ChatRequest` with structured system prompt + extend `llm.Usage` with cache tokens

**Why first:** every later task depends on this contract. Pure additive; no behavioral change yet.

**Files:**
- Modify: `internal/llm/provider.go` (add type + extend two structs + new helper)
- Test: `internal/llm/provider_test.go` (helper unit test)

- [ ] **Step 1: Write the failing test for `concatSystemPromptParts`**

Append to `internal/llm/provider_test.go`:

```go
func TestConcatSystemPromptParts(t *testing.T) {
	cases := []struct {
		name string
		in   []SystemPromptPart
		want string
	}{
		{"nil", nil, ""},
		{"empty slice", []SystemPromptPart{}, ""},
		{"single", []SystemPromptPart{{Text: "A"}}, "A"},
		{"two", []SystemPromptPart{{Text: "A"}, {Text: "B"}}, "A\nB"},
		{"skips empty", []SystemPromptPart{{Text: "A"}, {Text: ""}, {Text: "B"}}, "A\nB"},
		{"cache flag ignored by concat", []SystemPromptPart{{Text: "A", Cache: true}, {Text: "B", Cache: false}}, "A\nB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := concatSystemPromptParts(tc.in)
			require.Equal(t, tc.want, got)
		})
	}
}
```

If the file doesn't import `testify/require`, add `"github.com/stretchr/testify/require"` to the import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/ -run TestConcatSystemPromptParts`
Expected: FAIL with "undefined: SystemPromptPart" or "undefined: concatSystemPromptParts".

- [ ] **Step 3: Implement the type, fields, and helper**

Edit `internal/llm/provider.go`. After the `Diagnostic` type (line 60) and before `ReasoningMode`, insert:

```go
// SystemPromptPart is one segment of the system prompt. Providers that
// support prompt caching attach a cache marker to parts where Cache=true;
// providers that don't simply concatenate Text fields together.
type SystemPromptPart struct {
	Text  string
	Cache bool // request that the prefix up to and including this part be
	           // cached. Anthropic-only; ignored elsewhere.
}
```

In `ChatRequest` (lines 95-103), add two new fields immediately after `SystemPrompt`:

```go
type ChatRequest struct {
	Model        string
	Messages     []Message
	Tools        []ToolDef
	MaxTokens    int
	Temperature  float64
	SystemPrompt string
	// SystemPromptParts, when non-empty, replaces SystemPrompt. Providers
	// that support caching emit one block per part, attaching cache markers
	// per Cache flag. Providers that don't support caching concatenate
	// Text fields with "\n" separators.
	SystemPromptParts []SystemPromptPart
	// CacheLastMessage requests that the final block of the final user
	// message also be cache-marked. Anthropic-only; ignored elsewhere.
	CacheLastMessage bool
	Reasoning        ReasoningMode // zero value = ReasoningOff; safe default
}
```

In `Usage` (lines 106-109), add cache fields:

```go
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}
```

After `ParseProviderModel` (line 147), append the helper:

```go
// concatSystemPromptParts joins parts back into a single string with "\n"
// separators. Used by every provider that doesn't implement caching.
// Empty Text fields are skipped. Returns "" for a nil/empty slice.
func concatSystemPromptParts(parts []SystemPromptPart) string {
	if len(parts) == 0 {
		return ""
	}
	var nonEmpty []string
	for _, p := range parts {
		if p.Text != "" {
			nonEmpty = append(nonEmpty, p.Text)
		}
	}
	return strings.Join(nonEmpty, "\n")
}
```

- [ ] **Step 4: Run the test**

Run: `go test ./internal/llm/ -run TestConcatSystemPromptParts -v`
Expected: PASS for all sub-cases.

- [ ] **Step 5: Verify nothing else broke**

Run: `go build ./... && go test ./internal/llm/ ./internal/agent/`
Expected: PASS. (Adding fields to `ChatRequest` and `Usage` is additive; no caller breaks.)

- [ ] **Step 6: Commit**

```bash
git add internal/llm/provider.go internal/llm/provider_test.go
git commit -m "feat(llm): add SystemPromptPart, structured ChatRequest, cache-token Usage fields"
```

---

## Task 2: OpenAI provider — derive `sysPrompt` from parts

**Files:**
- Modify: `internal/llm/openai.go` (~line 95-104)
- Test: `internal/llm/openai_test.go` or a new file (existing tests use this path)

- [ ] **Step 1: Write the failing test**

Append to `internal/llm/openai_test.go` (create file if absent; use the existing test file pattern):

```go
func TestOpenAIChatStreamUsesSystemPromptParts(t *testing.T) {
	captured := captureOpenAIRequest(t, ChatRequest{
		SystemPromptParts: []SystemPromptPart{
			{Text: "static"},
			{Text: "dynamic"},
		},
	})
	require.NotNil(t, captured)
	require.GreaterOrEqual(t, len(captured.Messages), 1)
	require.Equal(t, "system", string(captured.Messages[0].Role))
	require.Equal(t, "static\ndynamic", captured.Messages[0].Content)
}

func TestOpenAIChatStreamFallsBackToSystemPromptString(t *testing.T) {
	captured := captureOpenAIRequest(t, ChatRequest{
		SystemPrompt: "legacy",
	})
	require.NotNil(t, captured)
	require.GreaterOrEqual(t, len(captured.Messages), 1)
	require.Equal(t, "legacy", captured.Messages[0].Content)
}

func TestOpenAIChatStreamPartsBeatString(t *testing.T) {
	captured := captureOpenAIRequest(t, ChatRequest{
		SystemPrompt:      "legacy",
		SystemPromptParts: []SystemPromptPart{{Text: "new"}},
	})
	require.Equal(t, "new", captured.Messages[0].Content)
}
```

`captureOpenAIRequest` is a small helper that points an `OpenAIProvider` at an `httptest.Server`, captures the JSON body of the first request, and returns the parsed `openai.ChatCompletionRequest`. If a similar helper exists in `openai_test.go` already, reuse it; otherwise the helper looks like:

```go
func captureOpenAIRequest(t *testing.T, req ChatRequest) *openai.ChatCompletionRequest {
	t.Helper()
	var captured openai.ChatCompletionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		// Minimal valid SSE response so ChatStream returns cleanly.
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	p := NewOpenAIProviderWithKind("test-key", srv.URL+"/v1", "openai-compatible")
	stream, err := p.ChatStream(context.Background(), req)
	require.NoError(t, err)
	for range stream {
	}
	return &captured
}
```

Make sure imports include `net/http`, `net/http/httptest`, `io`, `encoding/json`, `context`, plus `openai "github.com/sashabaranov/go-openai"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/ -run TestOpenAIChatStreamUsesSystemPromptParts -v`
Expected: FAIL — captured `Messages[0].Content` is `""` because the provider only honors `req.SystemPrompt`.

- [ ] **Step 3: Implement**

Edit `internal/llm/openai.go`. Replace lines 99-104:

```go
sysPrompt := req.SystemPrompt
if len(req.SystemPromptParts) > 0 {
	sysPrompt = concatSystemPromptParts(req.SystemPromptParts)
}
if sysPrompt != "" {
	msgs = append(msgs, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: sysPrompt,
	})
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/llm/ -run TestOpenAIChatStream -v`
Expected: PASS for all three new tests; existing OpenAI tests still pass.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/openai.go internal/llm/openai_test.go
git commit -m "feat(llm/openai): derive sysPrompt from SystemPromptParts when set"
```

---

## Task 3: Gemini provider — derive `sysPrompt` from parts

**Files:**
- Modify: `internal/llm/gemini.go` (~line 145)
- Test: `internal/llm/gemini_test.go` (extend or create)

- [ ] **Step 1: Write the failing test**

Append to `internal/llm/gemini_test.go`:

```go
func TestGeminiChatStreamConcatenatesParts(t *testing.T) {
	got := geminiResolveSystemPrompt(ChatRequest{
		SystemPromptParts: []SystemPromptPart{
			{Text: "alpha"}, {Text: "beta"},
		},
	})
	require.Equal(t, "alpha\nbeta", got)
}

func TestGeminiChatStreamFallsBackToSystemPromptString(t *testing.T) {
	got := geminiResolveSystemPrompt(ChatRequest{SystemPrompt: "only-string"})
	require.Equal(t, "only-string", got)
}
```

`geminiResolveSystemPrompt` will be a small unexported helper introduced in Step 3; we test the helper directly because constructing a `genai.Client` against a fake endpoint is significantly more work than a per-provider integration test deserves.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/ -run TestGeminiChatStream -v`
Expected: FAIL with "undefined: geminiResolveSystemPrompt".

- [ ] **Step 3: Implement**

In `internal/llm/gemini.go`, add (anywhere — bottom of file is fine):

```go
// geminiResolveSystemPrompt returns the effective system prompt string,
// preferring SystemPromptParts when present.
func geminiResolveSystemPrompt(req ChatRequest) string {
	if len(req.SystemPromptParts) > 0 {
		return concatSystemPromptParts(req.SystemPromptParts)
	}
	return req.SystemPrompt
}
```

Replace the existing block at line 145-146:

```go
if sysPrompt := geminiResolveSystemPrompt(req); sysPrompt != "" {
	config.SystemInstruction = genai.NewContentFromText(sysPrompt, "user")
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/llm/ -run TestGeminiChatStream -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/gemini.go internal/llm/gemini_test.go
git commit -m "feat(llm/gemini): derive sysPrompt from SystemPromptParts when set"
```

---

## Task 4: Qwen provider — derive `sysPrompt` from parts

**Files:**
- Modify: `internal/llm/qwen.go` (~line 51)
- Test: `internal/llm/qwen_test.go` (extend or create)

- [ ] **Step 1: Write the failing test**

Append to `internal/llm/qwen_test.go`:

```go
func TestQwenResolveSystemPromptPrefersParts(t *testing.T) {
	got := qwenResolveSystemPrompt(ChatRequest{
		SystemPrompt:      "legacy",
		SystemPromptParts: []SystemPromptPart{{Text: "new-a"}, {Text: "new-b"}},
	})
	require.Equal(t, "new-a\nnew-b", got)
}

func TestQwenResolveSystemPromptFallback(t *testing.T) {
	got := qwenResolveSystemPrompt(ChatRequest{SystemPrompt: "legacy"})
	require.Equal(t, "legacy", got)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/ -run TestQwenResolveSystemPrompt -v`
Expected: FAIL — undefined helper.

- [ ] **Step 3: Implement**

In `internal/llm/qwen.go`, add:

```go
func qwenResolveSystemPrompt(req ChatRequest) string {
	if len(req.SystemPromptParts) > 0 {
		return concatSystemPromptParts(req.SystemPromptParts)
	}
	return req.SystemPrompt
}
```

Replace lines 51-55 (the `if req.SystemPrompt != "" { ... }` block) with:

```go
if sysPrompt := qwenResolveSystemPrompt(req); sysPrompt != "" {
	msgs = append(msgs, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: sysPrompt,
	})
}
```

(The exact assignment shape may differ — preserve whatever surrounds the existing `req.SystemPrompt` block in `qwen.go`; only replace the resolution of the prompt string.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/llm/ -run TestQwenResolveSystemPrompt -v && go build ./internal/llm/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/qwen.go internal/llm/qwen_test.go
git commit -m "feat(llm/qwen): derive sysPrompt from SystemPromptParts when set"
```

---

## Task 5: Anthropic provider — emit cache markers on system blocks

**Files:**
- Modify: `internal/llm/anthropic.go` (lines 90-94 — the system block construction)
- Test: `internal/llm/anthropic_test.go` (new test)

- [ ] **Step 1: Write the failing test**

Append to `internal/llm/anthropic_test.go`:

```go
func TestAnthropicSystemPromptPartsEmitCacheControl(t *testing.T) {
	got := buildAnthropicSystem(ChatRequest{
		SystemPromptParts: []SystemPromptPart{
			{Text: "static-cached", Cache: true},
			{Text: "dynamic", Cache: false},
		},
	})
	require.Len(t, got, 2)
	require.Equal(t, "static-cached", got[0].Text)
	require.Equal(t, "ephemeral", string(got[0].CacheControl.Type))
	require.Equal(t, "dynamic", got[1].Text)
	require.Empty(t, string(got[1].CacheControl.Type), "second block must not be cache-marked")
}

func TestAnthropicSystemPromptStringFallback(t *testing.T) {
	got := buildAnthropicSystem(ChatRequest{SystemPrompt: "legacy"})
	require.Len(t, got, 1)
	require.Equal(t, "legacy", got[0].Text)
	require.Empty(t, string(got[0].CacheControl.Type))
}

func TestAnthropicSystemEmptyWhenBothEmpty(t *testing.T) {
	got := buildAnthropicSystem(ChatRequest{})
	require.Empty(t, got)
}

func TestAnthropicSystemSkipsEmptyParts(t *testing.T) {
	got := buildAnthropicSystem(ChatRequest{
		SystemPromptParts: []SystemPromptPart{
			{Text: ""},
			{Text: "real", Cache: true},
		},
	})
	require.Len(t, got, 1)
	require.Equal(t, "real", got[0].Text)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/ -run TestAnthropicSystem -v`
Expected: FAIL — undefined `buildAnthropicSystem`.

- [ ] **Step 3: Implement the helper and refactor `ChatStream`**

In `internal/llm/anthropic.go`, after `buildToolResultBlock` (end of file), add:

```go
// buildAnthropicSystem builds the System param array from a ChatRequest.
// Prefers SystemPromptParts when set: each non-empty part becomes one
// TextBlockParam; parts with Cache=true get an ephemeral cache_control marker.
// Falls back to a single un-cached block built from SystemPrompt when parts
// are absent. Returns nil when both inputs are empty.
func buildAnthropicSystem(req ChatRequest) []anthropic.TextBlockParam {
	if len(req.SystemPromptParts) > 0 {
		blocks := make([]anthropic.TextBlockParam, 0, len(req.SystemPromptParts))
		for _, p := range req.SystemPromptParts {
			if p.Text == "" {
				continue
			}
			b := anthropic.TextBlockParam{Text: p.Text}
			if p.Cache {
				b.CacheControl = anthropic.NewCacheControlEphemeralParam()
			}
			blocks = append(blocks, b)
		}
		if len(blocks) > 0 {
			return blocks
		}
		return nil
	}
	if req.SystemPrompt != "" {
		return []anthropic.TextBlockParam{{Text: req.SystemPrompt}}
	}
	return nil
}
```

Replace lines 90-94 in `ChatStream` (the existing `if req.SystemPrompt != "" { params.System = ... }` block) with:

```go
if sys := buildAnthropicSystem(req); len(sys) > 0 {
	params.System = sys
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/llm/ -run TestAnthropicSystem -v && go test ./internal/llm/ -run TestAnthropic -v`
Expected: PASS — new tests + all pre-existing Anthropic tests still pass.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/anthropic.go internal/llm/anthropic_test.go
git commit -m "feat(llm/anthropic): emit cache_control on system blocks per SystemPromptPart"
```

---

## Task 6: Anthropic provider — cache marker on the last user message

**Files:**
- Modify: `internal/llm/anthropic.go` — `buildAnthropicMessages` signature + `ChatStream` call site
- Test: `internal/llm/anthropic_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/llm/anthropic_test.go`:

```go
func TestBuildAnthropicMessagesCacheLastTextBlock(t *testing.T) {
	in := []Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "second"},
	}
	got := buildAnthropicMessages(in, true)
	require.Len(t, got, 3)
	last := got[len(got)-1]
	// Last user message must end with a text block carrying cache_control.
	blocks := last.Content
	require.NotEmpty(t, blocks)
	cc := blocks[len(blocks)-1].GetCacheControl()
	require.NotNil(t, cc)
	require.Equal(t, "ephemeral", string(cc.Type))
}

func TestBuildAnthropicMessagesNoMarkerWhenCacheLastFalse(t *testing.T) {
	in := []Message{{Role: "user", Content: "hi"}}
	got := buildAnthropicMessages(in, false)
	require.Len(t, got, 1)
	blocks := got[0].Content
	require.NotEmpty(t, blocks)
	cc := blocks[len(blocks)-1].GetCacheControl()
	if cc != nil {
		require.Empty(t, string(cc.Type), "no cache_control should be emitted when CacheLastMessage=false")
	}
}

func TestBuildAnthropicMessagesCacheLastToolResult(t *testing.T) {
	in := []Message{
		{Role: "assistant", Content: "thinking"},
		{Role: "user", ToolCallID: "tc_1", Content: "tool output"},
	}
	got := buildAnthropicMessages(in, true)
	last := got[len(got)-1]
	require.NotEmpty(t, last.Content)
	cc := last.Content[len(last.Content)-1].GetCacheControl()
	require.NotNil(t, cc)
	require.Equal(t, "ephemeral", string(cc.Type))
}
```

`GetCacheControl()` is the SDK accessor on `ContentBlockParamUnion` (verified in `anthropic-sdk-go@v1.35.0/message.go:2174`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/ -run TestBuildAnthropicMessages -v`
Expected: FAIL — `buildAnthropicMessages` currently takes one argument.

- [ ] **Step 3: Update `buildAnthropicMessages` signature and add the cache-marker logic**

In `internal/llm/anthropic.go`, update the function signature at line ~292:

```go
func buildAnthropicMessages(in []Message, cacheLast bool) []anthropic.MessageParam {
```

At the end of the function, just before `return msgs`, add:

```go
// Cache marker on the last block of the last message. Anthropic accepts
// up to 4 cache_control markers per request; combined with the static
// system block this is at most 2.
if cacheLast && len(msgs) > 0 {
	tail := &msgs[len(msgs)-1]
	if len(tail.Content) > 0 {
		setCacheControlOnBlock(&tail.Content[len(tail.Content)-1])
	}
}
```

Add the helper after `buildAnthropicMessages`:

```go
// setCacheControlOnBlock attaches an ephemeral cache_control marker to the
// underlying param of any ContentBlockParamUnion variant that has a
// CacheControl field. The SDK union type does not expose a direct setter,
// so we mutate the variant struct directly.
func setCacheControlOnBlock(block *anthropic.ContentBlockParamUnion) {
	cc := anthropic.NewCacheControlEphemeralParam()
	switch {
	case block.OfText != nil:
		block.OfText.CacheControl = cc
	case block.OfToolResult != nil:
		block.OfToolResult.CacheControl = cc
	case block.OfImage != nil:
		block.OfImage.CacheControl = cc
	case block.OfToolUse != nil:
		block.OfToolUse.CacheControl = cc
	}
	// Other variants (citations etc.) are out of scope; the runtime never
	// produces them as the tail of a user message.
}
```

Update the single call site of `buildAnthropicMessages` (around line 40 of `anthropic.go`) from `msgs := buildAnthropicMessages(req.Messages)` to `msgs := buildAnthropicMessages(req.Messages, req.CacheLastMessage)`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/llm/ -run TestBuildAnthropicMessages -v && go test ./internal/llm/ -run TestAnthropic -v`
Expected: PASS for new tests; all pre-existing tests still pass.

- [ ] **Step 5: Verify no other callers exist**

Run: `grep -rn 'buildAnthropicMessages' internal/`
Expected: only the call site already updated and the test file. If anything else surfaces, update it to pass the second argument.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/anthropic.go internal/llm/anthropic_test.go
git commit -m "feat(llm/anthropic): cache_control on last message tail when CacheLastMessage=true"
```

---

## Task 7: Anthropic provider — surface cache-token telemetry

**Files:**
- Modify: `internal/llm/anthropic.go` — the streaming event loop (`message_start` and `message_delta` cases)
- Test: `internal/llm/anthropic_test.go`

- [ ] **Step 1: Read the streaming loop to understand current Usage emission**

Run: `sed -n '195,210p' internal/llm/anthropic.go`
Expected: see the `message_delta` case currently emits `EventDone` with `Usage{OutputTokens: ...}` but never propagates `CacheCreationInputTokens` / `CacheReadInputTokens`.

- [ ] **Step 2: Write the failing test**

Append to `internal/llm/anthropic_test.go`:

```go
// TestAnthropicStreamSurfacesCacheTokens points the SDK at an httptest
// server that serves a canned SSE response with cache_creation_input_tokens
// and cache_read_input_tokens populated, and asserts the emitted
// llm.Usage carries them through.
func TestAnthropicStreamSurfacesCacheTokens(t *testing.T) {
	const sseBody = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0,"cache_creation_input_tokens":42,"cache_read_input_tokens":17}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	t.Cleanup(srv.Close)

	p := NewAnthropicProvider("test-key", srv.URL)
	stream, err := p.ChatStream(context.Background(), ChatRequest{Model: "claude-test"})
	require.NoError(t, err)

	var done *ChatEvent
	for ev := range stream {
		ev := ev
		if ev.Type == EventDone {
			done = &ev
		}
	}
	require.NotNil(t, done, "expected EventDone")
	require.NotNil(t, done.Usage, "expected Usage on EventDone")
	require.Equal(t, 42, done.Usage.CacheCreationInputTokens)
	require.Equal(t, 17, done.Usage.CacheReadInputTokens)
	require.Equal(t, 5, done.Usage.OutputTokens)
}
```

Add imports as needed: `net/http`, `net/http/httptest`, `context`.

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/llm/ -run TestAnthropicStreamSurfacesCacheTokens -v`
Expected: FAIL — current code emits `Usage` only with `OutputTokens`, no cache fields.

- [ ] **Step 4: Implement**

In `internal/llm/anthropic.go`, locate the `for stream.Next()` loop. Add a tracking variable above the loop:

```go
var cacheCreationTokens, cacheReadTokens, inputTokens int64
```

In the `case "message_start":` branch (you may need to add this case if it doesn't exist; today the loop begins with `content_block_start`), capture initial usage:

```go
case "message_start":
	if u := event.Message.Usage; u.InputTokens > 0 || u.CacheCreationInputTokens > 0 || u.CacheReadInputTokens > 0 {
		inputTokens = u.InputTokens
		cacheCreationTokens = u.CacheCreationInputTokens
		cacheReadTokens = u.CacheReadInputTokens
	}
```

(If the SDK union event variant differs in field path, inspect `event.AsMessageStart()` accessors — the SDK exposes a union accessor pattern. The intent: read whichever fields the SDK exposes for `message_start` usage.)

In the `case "message_delta":` branch (lines 195-204), replace the body so it carries cache tokens through:

```go
case "message_delta":
	if event.Usage.OutputTokens > 0 || cacheCreationTokens > 0 || cacheReadTokens > 0 {
		events <- ChatEvent{
			Type: EventDone,
			Usage: &Usage{
				InputTokens:              int(inputTokens),
				OutputTokens:             int(event.Usage.OutputTokens),
				CacheCreationInputTokens: int(cacheCreationTokens),
				CacheReadInputTokens:     int(cacheReadTokens),
			},
		}
	}
```

Also add a `slog.Info` line right before the channel send so cache-token telemetry shows up in production logs:

```go
slog.Info("anthropic stream usage",
	"input_tokens", inputTokens,
	"output_tokens", event.Usage.OutputTokens,
	"cache_creation_input_tokens", cacheCreationTokens,
	"cache_read_input_tokens", cacheReadTokens,
)
```

- [ ] **Step 5: Run the test**

Run: `go test ./internal/llm/ -run TestAnthropicStreamSurfacesCacheTokens -v`
Expected: PASS. If the test fails because the SDK accessor names differ, run `grep -n "MessageStartEvent\|message_start" /Users/sausheong/go/pkg/mod/github.com/anthropics/anthropic-sdk-go@v1.35.0/messagestreamevent.go` to find the right accessor and adjust.

- [ ] **Step 6: Run all Anthropic tests**

Run: `go test ./internal/llm/ -run TestAnthropic -v`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/llm/anthropic.go internal/llm/anthropic_test.go
git commit -m "feat(llm/anthropic): surface cache_creation/read_input_tokens via Usage + slog"
```

---

## Task 8: Pure `BuildConfigSummary(cfg *config.Config) string`

**Files:**
- Modify: `internal/agent/context.go` — add the new function alongside the existing `configSummary()`
- Test: `internal/agent/context_test.go` (create if absent)

The current `configSummary()` calls `config.Load("")` from disk on every invocation. The pure version takes a pre-loaded `*config.Config`. We add it now and switch the runtime to it in Task 11.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/context_test.go` (or append to existing test file in this package):

```go
package agent

import (
	"strings"
	"testing"

	"github.com/sausheong/felix/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBuildConfigSummaryWithAgentsAndCLI(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "alpha", Name: "Alpha", Model: "anthropic/claude-sonnet-4-5",
			Tools: config.AgentToolsConfig{Allow: []string{"read_file", "bash"}}},
		{ID: "beta", Name: "Beta", Model: "openai/gpt-4o"},
	}
	cfg.Channels.CLI.Enabled = true

	got := BuildConfigSummary(cfg)
	require.Contains(t, got, "Configured agents:")
	require.Contains(t, got, "Alpha (id: alpha, model: anthropic/claude-sonnet-4-5, tools: read_file, bash)")
	require.Contains(t, got, "Beta (id: beta, model: openai/gpt-4o)")
	require.Contains(t, got, "Configured channels: cli")
}

func TestBuildConfigSummaryEmptyConfig(t *testing.T) {
	got := BuildConfigSummary(&config.Config{})
	require.Equal(t, "", strings.TrimSpace(got))
}

func TestBuildConfigSummaryNilSafe(t *testing.T) {
	got := BuildConfigSummary(nil)
	require.Equal(t, "", got)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestBuildConfigSummary -v`
Expected: FAIL — `BuildConfigSummary` undefined.

- [ ] **Step 3: Implement**

In `internal/agent/context.go`, after the existing `configSummary()` function, add:

```go
// BuildConfigSummary returns the brief summary of agents and channels
// that gets injected into the static portion of the system prompt. Pure:
// no I/O, accepts the already-loaded *config.Config. Replaces the
// per-turn configSummary() that read felix.json5 from disk.
//
// Returns "" for a nil config or one with no agents and no enabled channels.
func BuildConfigSummary(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}

	var sb strings.Builder

	if len(cfg.Agents.List) > 0 {
		sb.WriteString("Configured agents:")
		for _, a := range cfg.Agents.List {
			tools := ""
			if len(a.Tools.Allow) > 0 {
				tools = ", tools: " + strings.Join(a.Tools.Allow, ", ")
			}
			sb.WriteString(fmt.Sprintf("\n- %s (id: %s, model: %s%s)", a.Name, a.ID, a.Model, tools))
		}
	}

	if cfg.Channels.CLI.Enabled {
		sb.WriteString("\n\nConfigured channels: cli")
	}

	return sb.String()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/ -run TestBuildConfigSummary -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/context.go internal/agent/context_test.go
git commit -m "feat(agent): add pure BuildConfigSummary(*config.Config) helper"
```

---

## Task 9: Pure `BuildStaticSystemPrompt`

**Files:**
- Modify: `internal/agent/context.go` — extract the body of `assembleSystemPrompt` into a pure function whose inputs include the pre-resolved `configSummary` and `skillsIndex` strings
- Test: `internal/agent/context_test.go`

This new function is what `BuildRuntimeForAgent` will call once at construction time. The legacy `assembleSystemPrompt` is left in place for now and removed in Task 14.

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/context_test.go`:

```go
import (
	"os"
	"path/filepath"
)

func TestBuildStaticSystemPromptWithIdentityFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "IDENTITY.md"),
		[]byte("CUSTOM IDENTITY"),
		0o644,
	))

	got := BuildStaticSystemPrompt(
		dir, "", "alpha", "Alpha",
		[]string{"read_file"},
		"Configured channels: cli",
		"\n\n## Skills Index\n\n- foo",
	)
	require.Contains(t, got, "CUSTOM IDENTITY")
	require.Contains(t, got, `"Alpha" agent (id: alpha)`)
	require.Contains(t, got, "Configured channels: cli")
	require.Contains(t, got, "## Skills Index")
}

func TestBuildStaticSystemPromptConfigOverride(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("file"), 0o644))

	got := BuildStaticSystemPrompt(dir, "FROM CONFIG", "id", "Name", nil, "", "")
	require.Contains(t, got, "FROM CONFIG")
	require.NotContains(t, got, "file")
}

func TestBuildStaticSystemPromptDefaultIdentity(t *testing.T) {
	dir := t.TempDir() // no IDENTITY.md
	got := BuildStaticSystemPrompt(dir, "", "id", "Name", []string{"read_file", "bash"}, "", "")
	require.Contains(t, got, defaultIdentityBase)
	require.Contains(t, got, "read files")
	require.Contains(t, got, "bash commands")
}

func TestBuildStaticSystemPromptByteStableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	a := BuildStaticSystemPrompt(dir, "", "id", "Name", []string{"read_file"}, "summary", "index")
	b := BuildStaticSystemPrompt(dir, "", "id", "Name", []string{"read_file"}, "summary", "index")
	require.Equal(t, a, b, "BuildStaticSystemPrompt must be deterministic")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestBuildStaticSystemPrompt -v`
Expected: FAIL — undefined `BuildStaticSystemPrompt`.

- [ ] **Step 3: Implement**

In `internal/agent/context.go`, after `BuildConfigSummary`, add:

```go
// BuildStaticSystemPrompt assembles the portion of the system prompt that
// does not change across turns within a Run: identity (from systemPrompt
// arg, IDENTITY.md, or the built-in default tailored to toolNames), agent
// self-identity, the configuration/data dir paths, the pre-computed
// configSummary, and the pre-computed skillsIndex.
//
// Pure (with one allowed exception: it reads IDENTITY.md from workspace
// when systemPrompt is empty). Caller pre-resolves configSummary and
// skillsIndex so neither config.Load nor skill index assembly happens
// per-turn. Suitable to call once at Runtime construction.
func BuildStaticSystemPrompt(
	workspace, systemPrompt, agentID, agentName string,
	toolNames []string,
	configSummary string,
	skillsIndex string,
) string {
	var base string
	if systemPrompt != "" {
		base = systemPrompt
	} else {
		identityPath := filepath.Join(workspace, "IDENTITY.md")
		data, err := os.ReadFile(identityPath)
		if err != nil {
			base = buildDefaultIdentity(toolNames)
		} else {
			base = string(data)
		}
	}

	if agentID != "" {
		base += fmt.Sprintf("\n\nYou are the %q agent (id: %s).", agentName, agentID)
	}

	base += fmt.Sprintf("\n\nYour configuration file is at %s and your data directory is %s.",
		config.DefaultConfigPath(), config.DefaultDataDir())

	if configSummary != "" {
		base += "\n\n" + configSummary
	}
	if skillsIndex != "" {
		base += skillsIndex
	}

	return base
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/ -run TestBuildStaticSystemPrompt -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/context.go internal/agent/context_test.go
git commit -m "feat(agent): add pure BuildStaticSystemPrompt for one-time prefix construction"
```

---

## Task 10: Pure `buildDynamicSystemPromptSuffix`

**Files:**
- Modify: `internal/agent/context.go`
- Test: `internal/agent/context_test.go`

The dynamic suffix collects the per-turn-cached pieces (matched skills + memory + cortex hint) into a single string the runtime ships as the second `SystemPromptPart`.

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/context_test.go`:

```go
import (
	"github.com/sausheong/felix/internal/memory"
	"github.com/sausheong/felix/internal/skill"
)

func TestBuildDynamicSystemPromptSuffixAllSources(t *testing.T) {
	skills := []skill.Skill{{Name: "foo", Body: "FOO BODY"}}
	mem := []memory.Entry{{ID: "m1", Title: "Mem One", Content: "memory body"}}
	cortex := "\n\n## Cortex hint\n..."

	got := buildDynamicSystemPromptSuffix(skills, mem, cortex)
	require.Contains(t, got, "FOO BODY")
	require.Contains(t, got, "Mem One")
	require.Contains(t, got, "memory body")
	require.Contains(t, got, "Cortex hint")
}

func TestBuildDynamicSystemPromptSuffixEmpty(t *testing.T) {
	got := buildDynamicSystemPromptSuffix(nil, nil, "")
	require.Equal(t, "", got)
}

func TestBuildDynamicSystemPromptSuffixCortexOnly(t *testing.T) {
	got := buildDynamicSystemPromptSuffix(nil, nil, "\n\nCORTEX")
	require.Equal(t, "\n\nCORTEX", got)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestBuildDynamicSystemPromptSuffix -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

In `internal/agent/context.go`, after `BuildStaticSystemPrompt`, add:

```go
// buildDynamicSystemPromptSuffix concatenates the per-turn dynamic context
// — matched skill bodies, matched memory entries, and the cortex hint —
// into a single string the runtime sends as the second (un-cached)
// SystemPromptPart. Returns "" when all inputs are empty/nil.
func buildDynamicSystemPromptSuffix(
	matchedSkills []skill.Skill,
	matchedMemory []memory.Entry,
	cortexContext string,
) string {
	var sb strings.Builder
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

Add the imports if not already present: `"github.com/sausheong/felix/internal/memory"`, `"github.com/sausheong/felix/internal/skill"`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/ -run TestBuildDynamicSystemPromptSuffix -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/context.go internal/agent/context_test.go
git commit -m "feat(agent): add buildDynamicSystemPromptSuffix for per-turn dynamic context"
```

---

## Task 11: Add `Provider` + `StaticSystemPrompt` fields, wire `*config.Config` through `RuntimeDeps`, pre-compute prompt in builder

**Files:**
- Modify: `internal/agent/runtime.go` — add fields to `Runtime` struct
- Modify: `internal/agent/builder.go` — extend `RuntimeDeps`, set fields in `BuildRuntimeForAgent`
- Modify: `internal/startup/startup.go:562` — pass `Config: cfg`
- Modify: `internal/gateway/websocket.go:371` — pass `Config: cfg` (verify cfg variable is in scope; if not, propagate it)
- Modify: `cmd/felix/main.go:376` — pass `Config: cfg`
- Test: `internal/agent/builder_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

Create `internal/agent/builder_test.go`:

```go
package agent

import (
	"testing"

	"github.com/sausheong/felix/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBuildRuntimeForAgentSetsProviderAndStaticPrompt(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "a", Name: "A", Model: "anthropic/claude-sonnet-4-5"},
	}
	cfg.Channels.CLI.Enabled = true

	a := &cfg.Agents.List[0]
	deps := RuntimeDeps{Config: cfg}
	inputs := RuntimeInputs{}

	rt, err := BuildRuntimeForAgent(deps, inputs, a)
	require.NoError(t, err)
	require.Equal(t, "anthropic", rt.Provider)
	require.Equal(t, "claude-sonnet-4-5", rt.Model)
	require.NotEmpty(t, rt.StaticSystemPrompt)
	require.Contains(t, rt.StaticSystemPrompt, `"A" agent (id: a)`)
	require.Contains(t, rt.StaticSystemPrompt, "Configured channels: cli")
}

func TestBuildRuntimeForAgentLocalProvider(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "x", Name: "X", Model: "local/qwen2.5:3b"},
	}
	rt, err := BuildRuntimeForAgent(RuntimeDeps{Config: cfg}, RuntimeInputs{}, &cfg.Agents.List[0])
	require.NoError(t, err)
	require.Equal(t, "local", rt.Provider)
}

func TestBuildRuntimeForAgentNilConfigSafe(t *testing.T) {
	a := &config.AgentConfig{ID: "a", Name: "A", Model: "anthropic/claude-sonnet-4-5"}
	rt, err := BuildRuntimeForAgent(RuntimeDeps{}, RuntimeInputs{}, a)
	require.NoError(t, err)
	require.Equal(t, "anthropic", rt.Provider)
	require.NotEmpty(t, rt.StaticSystemPrompt) // identity + paths still present
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestBuildRuntimeForAgent -v`
Expected: FAIL — `RuntimeDeps.Config` and `Runtime.Provider`/`Runtime.StaticSystemPrompt` are undefined.

- [ ] **Step 3: Add fields to `Runtime` struct**

In `internal/agent/runtime.go`, inside the `Runtime` struct (after existing fields, e.g., right after `Compaction`), add:

```go
	// Provider is the LLM provider name parsed from the agent's "provider/model"
	// model string (e.g., "anthropic", "openai", "local"). Used by
	// providerSupportsCaching() to decide whether to set CacheLastMessage on
	// outgoing ChatRequests.
	Provider string

	// StaticSystemPrompt is the cacheable portion of the system prompt
	// (identity, agent metadata, configuration paths, configSummary,
	// skillsIndex). Built once at BuildRuntimeForAgent time; reused
	// verbatim on every turn so the Anthropic prompt cache hits.
	StaticSystemPrompt string
```

- [ ] **Step 4: Add `Config` field to `RuntimeDeps`**

In `internal/agent/builder.go`, inside `RuntimeDeps`, add:

```go
	// Config is the live *config.Config. Used during BuildRuntimeForAgent
	// to pre-compute the configuration summary that goes into the static
	// system prompt — replaces the per-turn config.Load("") that the old
	// configSummary() did.
	Config *config.Config
```

Add `"github.com/sausheong/felix/internal/config"` to the imports if it's not already present (it is; the existing struct uses `config.AgentLoopConfig`).

- [ ] **Step 5: Wire pre-computation into `BuildRuntimeForAgent`**

Replace the existing function body of `BuildRuntimeForAgent` (`internal/agent/builder.go:56-87`) with:

```go
func BuildRuntimeForAgent(deps RuntimeDeps, inputs RuntimeInputs, a *config.AgentConfig) (*Runtime, error) {
	provider, modelName := llm.ParseProviderModel(a.Model)
	reasoning, err := llm.ParseReasoningMode(a.Reasoning)
	if err != nil {
		slog.Error("invalid reasoning mode in agent config; defaulting to off",
			"agent", a.ID, "value", a.Reasoning, "err", err)
		reasoning = llm.ReasoningOff
	}
	var cx *cortex.Cortex
	if deps.CortexFn != nil {
		cx = deps.CortexFn(a.Model)
	}

	// Pre-compute the static portion of the system prompt so the per-turn
	// hot loop never reads config or rebuilds the skills index.
	configSummary := BuildConfigSummary(deps.Config)
	skillsIndex := ""
	if deps.Skills != nil {
		skillsIndex = deps.Skills.FormatIndex()
	}
	var toolNames []string
	if inputs.Tools != nil {
		toolNames = inputs.Tools.Names()
	}
	staticPrompt := BuildStaticSystemPrompt(
		a.Workspace, a.SystemPrompt, a.ID, a.Name,
		toolNames, configSummary, skillsIndex,
	)

	return &Runtime{
		LLM:                inputs.Provider,
		Tools:              inputs.Tools,
		Session:            inputs.Session,
		AgentID:            a.ID,
		AgentName:          a.Name,
		Model:              modelName,
		Provider:           provider,
		Reasoning:          reasoning,
		Workspace:          a.Workspace,
		MaxTurns:           a.MaxTurns,
		SystemPrompt:       a.SystemPrompt,
		Skills:             deps.Skills,
		Memory:             deps.Memory,
		Cortex:             cx,
		Permission:         deps.Permission,
		Compaction:         inputs.Compaction,
		IngestSource:       inputs.IngestSource,
		AgentLoop:          deps.AgentLoop,
		StaticSystemPrompt: staticPrompt,
	}, nil
}
```

- [ ] **Step 6: Update production call sites to pass `Config`**

For each of the three call sites identified by `grep -n "RuntimeDeps{" internal/startup/startup.go internal/gateway/websocket.go cmd/felix/main.go`, add `Config: cfg,` to the literal. The `cfg` variable name may differ — check the surrounding code:

Run: `sed -n '555,570p' internal/startup/startup.go`

Add `Config: cfg,` (or whatever the local config variable is named) to the `RuntimeDeps{...}` literal at line 562. Repeat for `internal/gateway/websocket.go:371` and `cmd/felix/main.go:376`. Verify the local variable name in each file before editing — in `websocket.go` it may be a struct field on the server; in `main.go` it may be `appCfg` or similar.

- [ ] **Step 7: Run tests**

Run: `go test ./internal/agent/ -run TestBuildRuntimeForAgent -v && go build ./...`
Expected: PASS for the new tests; the whole tree still compiles.

- [ ] **Step 8: Run the full agent test suite**

Run: `go test ./internal/agent/ -v -count=1`
Expected: every existing test still passes. Tests that construct `Runtime{...}` directly without `StaticSystemPrompt` will still work — the field is optional; the runtime falls back at runtime in Task 13.

- [ ] **Step 9: Commit**

```bash
git add internal/agent/runtime.go internal/agent/builder.go internal/agent/builder_test.go internal/startup/startup.go internal/gateway/websocket.go cmd/felix/main.go
git commit -m "feat(agent): add Provider/StaticSystemPrompt fields, pre-compute prompt in builder"
```

---

## Task 12: Sort tool defs alphabetically in the runtime hot loop

**Files:**
- Modify: `internal/agent/runtime.go` (around line 304-308 — the `toolDefs := r.Tools.ToolDefs()` block)
- Test: `internal/agent/cache_stability_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/cache_stability_test.go`:

```go
func TestToolDefsSortedByNameInRequest(t *testing.T) {
	rec := &recordingProvider{reply: "ok"}
	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	reg.Register(&mockTool{name: "zebra", output: "z"})
	reg.Register(&mockTool{name: "alpha", output: "a"})
	reg.Register(&mockTool{name: "mango", output: "m"})

	rt := &Runtime{
		LLM: rec, Tools: reg, Session: sess,
		Model: "rec-model", Workspace: t.TempDir(), MaxTurns: 5,
	}

	events, err := rt.Run(context.Background(), "hello", nil)
	require.NoError(t, err)
	for range events {
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.GreaterOrEqual(t, len(rec.requests), 1)
	names := make([]string, 0, len(rec.requests[0].Tools))
	for _, td := range rec.requests[0].Tools {
		names = append(names, td.Name)
	}
	require.Equal(t, []string{"alpha", "mango", "zebra"}, names)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestToolDefsSortedByName -v`
Expected: FAIL — tools come out in registration order (`zebra, alpha, mango`).

- [ ] **Step 3: Implement**

In `internal/agent/runtime.go`, around line 304 (just before `r.LLM.NormalizeToolSchema`), insert the sort. The current block:

```go
toolDefs := r.Tools.ToolDefs()
if r.Permission != nil {
	toolDefs = r.Permission.FilterToolDefs(toolDefs, r.AgentID)
}
toolDefs, diags := r.LLM.NormalizeToolSchema(toolDefs)
```

Becomes:

```go
toolDefs := r.Tools.ToolDefs()
if r.Permission != nil {
	toolDefs = r.Permission.FilterToolDefs(toolDefs, r.AgentID)
}
sort.SliceStable(toolDefs, func(i, j int) bool {
	return toolDefs[i].Name < toolDefs[j].Name
})
toolDefs, diags := r.LLM.NormalizeToolSchema(toolDefs)
```

Add `"sort"` to the import block at the top of `runtime.go`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/ -run TestToolDefs -v && go test ./internal/agent/ -run TestRequestPrefixIsByteStable -v`
Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/runtime.go internal/agent/cache_stability_test.go
git commit -m "feat(agent): sort tool defs by name before sending for cache prefix stability"
```

---

## Task 13: Switch the runtime hot loop to `SystemPromptParts` + dynamic suffix

**Files:**
- Modify: `internal/agent/runtime.go` — the section at lines 237-296 (system prompt assembly + cortex injection + ChatRequest build)
- Test: `internal/agent/cache_stability_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/cache_stability_test.go`:

```go
func TestRuntimeSendsStructuredSystemPromptParts(t *testing.T) {
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

	events, err := rt.Run(context.Background(), "hi", nil)
	require.NoError(t, err)
	for range events {
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.GreaterOrEqual(t, len(rec.requests), 1)
	req0 := rec.requests[0]
	require.NotEmpty(t, req0.SystemPromptParts, "expected SystemPromptParts to be populated")
	require.True(t, req0.SystemPromptParts[0].Cache, "first part must be cache-marked")
	require.Contains(t, req0.SystemPromptParts[0].Text, `"A" agent (id: a)`)
	require.True(t, req0.CacheLastMessage, "Anthropic provider must request CacheLastMessage")
}

func TestRuntimeStaticPromptByteStableAcrossTurns(t *testing.T) {
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

	for _, msg := range []string{"hello", "world"} {
		ev, err := rt.Run(context.Background(), msg, nil)
		require.NoError(t, err)
		for range ev {
		}
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.GreaterOrEqual(t, len(rec.requests), 2)
	require.Equal(t,
		rec.requests[0].SystemPromptParts[0].Text,
		rec.requests[1].SystemPromptParts[0].Text,
		"static system prompt must be byte-identical across turns",
	)
}

func TestRuntimeNonAnthropicHasCacheLastMessageFalse(t *testing.T) {
	rec := &recordingProvider{reply: "ok"}
	sess := session.NewSession("test-agent", "test-key")
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "a", Name: "A", Model: "openai/gpt-4o"},
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
	require.False(t, rec.requests[0].CacheLastMessage)
}
```

If `config` is not imported in this test file, add `"github.com/sausheong/felix/internal/config"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run "TestRuntimeSendsStructuredSystemPromptParts|TestRuntimeStaticPromptByteStable|TestRuntimeNonAnthropicHasCacheLastMessageFalse" -v`
Expected: FAIL — runtime currently sends `SystemPrompt` (string) and never sets `SystemPromptParts` or `CacheLastMessage`.

- [ ] **Step 3: Add `providerSupportsCaching` helper**

In `internal/agent/runtime.go`, anywhere convenient (e.g., right above `Run`):

```go
// providerSupportsCaching returns true when the runtime's provider implements
// Anthropic-style explicit prompt caching. Used to decide whether to set
// CacheLastMessage on outgoing ChatRequests.
func (r *Runtime) providerSupportsCaching() bool {
	return r.Provider == "anthropic"
}
```

- [ ] **Step 4: Refactor the per-turn system prompt assembly**

In `internal/agent/runtime.go`, locate the per-turn assembly block (currently lines 237-296). Replace it with:

```go
// Assemble dynamic suffix only — the static portion was pre-computed at
// Runtime construction time and lives on r.StaticSystemPrompt.
phaseStart := time.Now()

// Inject relevant skills. Match once per request (turn 0) since user
// message doesn't change across turns.
if r.Skills != nil && turn == 0 {
	skillStart := time.Now()
	matchedSkills = r.Skills.MatchSkills(userMsg, 1)
	tr.Mark("skills.match", "turn", turn, "matched", len(matchedSkills), "dur_ms_local", time.Since(skillStart).Milliseconds())
}

// Inject relevant memory (same per-request caching as skills).
if r.Memory != nil && turn == 0 {
	memStart := time.Now()
	matchedMemory = r.Memory.Search(userMsg, 3)
	tr.Mark("memory.search", "turn", turn, "hits", len(matchedMemory), "dur_ms_local", time.Since(memStart).Milliseconds())
}

// Cortex hint (same as before — unchanged from current implementation).
if cortexCh != nil && cortexContext == "" {
	select {
	case cortexContext = <-cortexCh:
	case <-time.After(800 * time.Millisecond):
		tr.Mark("cortex.recall.timeout", "turn", turn, "budget_ms", 800)
		if cortexCancel != nil {
			cortexCancel()
			cortexCancel = nil
		}
		cortexContext = cortexadapter.CortexHint
	}
	cortexCh = nil
}

dynamicSuffix := buildDynamicSystemPromptSuffix(matchedSkills, matchedMemory, cortexContext)

// Build the structured system prompt: static (cached) + dynamic (not cached).
parts := []llm.SystemPromptPart{
	{Text: r.StaticSystemPrompt, Cache: true},
}
if dynamicSuffix != "" {
	parts = append(parts, llm.SystemPromptPart{Text: dynamicSuffix, Cache: false})
}

// Fallback for tests that construct Runtime{...} directly without
// StaticSystemPrompt: build it on the fly using the legacy assembler.
if r.StaticSystemPrompt == "" {
	parts[0].Text = assembleSystemPrompt(r.Workspace, r.SystemPrompt, r.AgentID, r.AgentName, r.Tools.Names())
}
```

Then locate the `req := llm.ChatRequest{...}` construction (around line 364) and replace `SystemPrompt: systemPrompt,` with:

```go
SystemPromptParts: parts,
CacheLastMessage:  r.providerSupportsCaching(),
```

The variable `systemPrompt` is no longer needed; remove its declaration. Update any local logging that referenced `systemPrompt` (e.g., `prefillChars := len(systemPrompt)`) to use `parts[0].Text + dynamicSuffix` or refactor to compute the prefill estimate from `parts`:

```go
prefillChars := 0
for _, p := range parts {
	prefillChars += len(p.Text)
}
for _, m := range msgs {
	prefillChars += len(m.Content)
}
```

For `r.calibrator.Adjust(tokens.Estimate(msgs, systemPrompt, toolDefs))` calls in the preventive-compaction block (around line 340), pass the joined parts string:

```go
joinedSystemPrompt := concatSystemPromptPartsExternal(parts)
estimate := r.calibrator.Adjust(tokens.Estimate(msgs, joinedSystemPrompt, toolDefs))
```

`concatSystemPromptPartsExternal` is a tiny exported re-export of the package-private helper in `llm`. Add it to `internal/llm/provider.go`:

```go
// JoinSystemPromptParts is the exported variant of concatSystemPromptParts
// for callers outside the llm package (e.g., agent runtime token estimation).
func JoinSystemPromptParts(parts []SystemPromptPart) string {
	return concatSystemPromptParts(parts)
}
```

In runtime.go, call it as `llm.JoinSystemPromptParts(parts)`. (Don't shadow it with a local helper — just call directly.)

Same treatment for the `r.calibrator.Update(event.Usage.InputTokens, tokens.Estimate(msgs, systemPrompt, toolDefs))` call in the `EventDone` case.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/agent/ -run "TestRuntimeSendsStructured|TestRuntimeStaticPromptByteStable|TestRuntimeNonAnthropic" -v`
Expected: PASS.

- [ ] **Step 6: Run the full agent + llm test suite**

Run: `go test ./internal/agent/ ./internal/llm/ -v -count=1`
Expected: every test passes, including the legacy `assembleSystemPrompt` tests (which still hit the fallback path).

- [ ] **Step 7: Commit**

```bash
git add internal/agent/runtime.go internal/agent/cache_stability_test.go internal/llm/provider.go
git commit -m "feat(agent): runtime sends SystemPromptParts + CacheLastMessage per turn"
```

---

## Task 14: Delete legacy `assembleSystemPrompt` and `configSummary`; update affected tests

**Files:**
- Modify: `internal/agent/context.go` — delete `assembleSystemPrompt` and `configSummary`
- Modify: `internal/agent/runtime.go` — remove the fallback that uses `assembleSystemPrompt`
- Modify: `internal/agent/agent_test.go` — remove or replace tests that called `assembleSystemPrompt` directly

The legacy function was kept around in Task 13 as a fallback for direct-construction tests. Now that test fixtures use `BuildRuntimeForAgent` (or the helper introduced in Task 11), we delete the legacy path.

- [ ] **Step 1: Audit legacy callers**

Run: `grep -rn "assembleSystemPrompt\|configSummary" --include='*.go' internal/ cmd/`
Expected: matches in `internal/agent/context.go`, `internal/agent/runtime.go` (the fallback added in Task 13), and `internal/agent/agent_test.go` (4 tests starting at line 38).

- [ ] **Step 2: Convert the legacy `agent_test.go` tests to use `BuildStaticSystemPrompt`**

In `internal/agent/agent_test.go`, replace each `assembleSystemPrompt(...)` call with the equivalent `BuildStaticSystemPrompt(...)` invocation. The signatures map cleanly:

`assembleSystemPrompt(workspace, sp, id, name, toolNames)` → `BuildStaticSystemPrompt(workspace, sp, id, name, toolNames, "", "")`.

For `TestAssembleSystemPromptDefault` (the test that sets up a fake `~/.felix/felix.json5`), the test was working around the per-turn `config.Load`. Now the load happens at builder time and the test doesn't need to touch HOME. Simplify it to:

```go
func TestBuildStaticSystemPromptDefaultIdentityAndPaths(t *testing.T) {
	dir := t.TempDir() // workspace, no IDENTITY.md
	result := BuildStaticSystemPrompt(dir, "", "default", "Assistant",
		[]string{"read_file", "bash"}, "", "")
	require.Contains(t, result, defaultIdentityBase)
	require.Contains(t, result, "read files")
	require.Contains(t, result, "bash commands")
	require.NotContains(t, result, "web_fetch")
	require.Contains(t, result, "data directory")
}
```

Rename or delete the four legacy tests as appropriate; keep their assertions about default identity / config-override / self-identity intact, but anchored on `BuildStaticSystemPrompt`.

- [ ] **Step 3: Delete the runtime fallback**

In `internal/agent/runtime.go`, remove the block introduced in Task 13's Step 4:

```go
if r.StaticSystemPrompt == "" {
	parts[0].Text = assembleSystemPrompt(r.Workspace, r.SystemPrompt, r.AgentID, r.AgentName, r.Tools.Names())
}
```

- [ ] **Step 4: Delete the legacy functions**

In `internal/agent/context.go`, delete:
- The `assembleSystemPrompt` function (lines 73-108).
- The `configSummary` function (lines 110-138).

The doc comment block immediately above `assembleSystemPrompt` goes too.

- [ ] **Step 5: Verify no lingering references**

Run: `grep -rn "assembleSystemPrompt\|configSummary" --include='*.go' internal/ cmd/`
Expected: no matches.

- [ ] **Step 6: Run the full test suite**

Run: `go build ./... && go test ./... -count=1`
Expected: every test passes. If `TestAssembleSystemPromptDefault` (or its rename) is still in the file and uses `t.Setenv("HOME", ...)`, that's harmless leftover — it can stay or be removed; the test should pass either way because `BuildStaticSystemPrompt` doesn't read disk for config.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/context.go internal/agent/runtime.go internal/agent/agent_test.go
git commit -m "refactor(agent): delete legacy assembleSystemPrompt/configSummary; tests use BuildStaticSystemPrompt"
```

---

## Task 15: Document the manual verification recipe

**Files:**
- Create: `docs/superpowers/specs/2026-04-30-caching-foundation-design.md` (append a "Verified on" section) or a new note file. Pick the one consistent with prior phase patterns in `docs/superpowers/specs/`.

This task is documentation only — no code, no tests.

- [ ] **Step 1: After running Felix locally against an Anthropic model, capture log output**

Build and start Felix:
```bash
go build -o felix ./cmd/felix
./felix chat --agent default
```

Send 5 messages in the CLI chat. Then:
```bash
grep "anthropic stream usage" ~/.felix/logs/*.log | tail -10
```

- [ ] **Step 2: Verify the cache-token pattern**

Expected log shape:
- Turn 1: `cache_creation_input_tokens` ≈ size of static system prompt + tool defs in tokens; `cache_read_input_tokens=0`.
- Turns 2–5: `cache_read_input_tokens` ≈ static system + tools + prior messages; `cache_creation_input_tokens` ≈ what was added since last turn (small).

If turns 2–5 show `cache_read_input_tokens=0`, the prefix is being invalidated. Bisect by re-running the recording-provider test and inspecting `req.SystemPromptParts[0].Text` for non-determinism (timestamps, random IDs, map-iteration order).

- [ ] **Step 3: Append the verification result to the spec**

Edit `docs/superpowers/specs/2026-04-30-caching-foundation-design.md` and append a section:

```markdown
## Verified

- **Date:** 2026-04-30 (or whenever)
- **Model:** claude-sonnet-4-5-20250514
- **Result:** turn 1 created N cache tokens; turns 2–5 read M cache tokens (~93% hit rate).
- **Notes:** any quirks observed.
```

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/specs/2026-04-30-caching-foundation-design.md
git commit -m "docs: record cache-foundation manual verification results"
```

---

## Self-review checklist (filled out before handoff)

- **Spec coverage:**
  - §1 LLM contract → Task 1.
  - §2 Anthropic system markers → Task 5.
  - §2 Anthropic last-message marker → Task 6.
  - §2 Anthropic telemetry → Task 7.
  - §3 OpenAI/Gemini/Qwen concat → Tasks 2, 3, 4.
  - §4 BuildStaticSystemPrompt + BuildConfigSummary + buildDynamicSystemPromptSuffix → Tasks 8, 9, 10.
  - §5 Runtime field + RuntimeDeps.Config + builder rewiring → Task 11.
  - §6 Tool ordering → Task 12.
  - Switch runtime to parts → Task 13.
  - Cleanup → Task 14.
  - Manual verification recipe → Task 15.
- **No placeholders:** all code blocks contain runnable Go; all commands have expected output.
- **Type consistency:** `SystemPromptPart` (Text/Cache), `Runtime.Provider`/`StaticSystemPrompt`, `RuntimeDeps.Config`, `JoinSystemPromptParts` — all referenced consistently across tasks.

---

Plan complete and saved to `docs/superpowers/plans/2026-04-30-caching-foundation-plan.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
