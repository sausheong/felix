package llm

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper: extract tool_use IDs (in order) from an assistant MessageParam.
func toolUseIDs(t *testing.T, m anthropic.MessageParam) []string {
	t.Helper()
	require.Equal(t, anthropic.MessageParamRoleAssistant, m.Role)
	var ids []string
	for _, b := range m.Content {
		if b.OfToolUse != nil {
			ids = append(ids, b.OfToolUse.ID)
		}
	}
	return ids
}

// helper: extract tool_result IDs (in order) from a user MessageParam.
func toolResultIDs(t *testing.T, m anthropic.MessageParam) []string {
	t.Helper()
	require.Equal(t, anthropic.MessageParamRoleUser, m.Role)
	var ids []string
	for _, b := range m.Content {
		if b.OfToolResult != nil {
			ids = append(ids, b.OfToolResult.ToolUseID)
		}
	}
	return ids
}

// TestAnthropic_ConsecutiveToolResultsCoalesce verifies that when the
// LLM emits N tool_use blocks in a single assistant turn (e.g. parallel
// MCP calls), the N resulting tool_result user messages produced by the
// agent are coalesced into ONE user message with N tool_result content
// blocks. The Anthropic Messages API requires this — it rejects each
// tool_result needing the immediately preceding message to contain the
// matching tool_use.
func TestAnthropic_ConsecutiveToolResultsCoalesce(t *testing.T) {
	in := []Message{
		{Role: "user", Content: "do three things in parallel"},
		{
			Role:    "assistant",
			Content: "ok",
			ToolCalls: []ToolCall{
				{ID: "A", Name: "search", Input: []byte(`{"q":"a"}`)},
				{ID: "B", Name: "search", Input: []byte(`{"q":"b"}`)},
				{ID: "C", Name: "search", Input: []byte(`{"q":"c"}`)},
			},
		},
		// Results may arrive in any order — agent dispatches in parallel.
		{Role: "user", ToolCallID: "B", Content: "result B"},
		{Role: "user", ToolCallID: "A", Content: "result A"},
		{Role: "user", ToolCallID: "C", Content: "result C"},
		{Role: "assistant", Content: "done"},
	}

	got := buildAnthropicMessages(in, false)

	// Expected shape: user(text), assistant(3 tool_uses), user(3 tool_results), assistant
	require.Len(t, got, 4, "expected 4 messages, got %d", len(got))

	assert.Equal(t, anthropic.MessageParamRoleUser, got[0].Role)
	assert.Equal(t, []string{"A", "B", "C"}, toolUseIDs(t, got[1]))
	assert.Equal(t, []string{"B", "A", "C"}, toolResultIDs(t, got[2]),
		"three tool_results must coalesce into one user message preserving order")
	assert.Equal(t, anthropic.MessageParamRoleAssistant, got[3].Role)
}

// TestAnthropic_SingleToolResultStillSeparate pins the existing
// behavior: a single tool_result still produces a single user message
// containing one tool_result block.
func TestAnthropic_SingleToolResultStillSeparate(t *testing.T) {
	in := []Message{
		{Role: "user", Content: "search"},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "X", Name: "search", Input: []byte(`{"q":"x"}`)},
			},
		},
		{Role: "user", ToolCallID: "X", Content: "result X"},
		{Role: "assistant", Content: "done"},
	}

	got := buildAnthropicMessages(in, false)

	require.Len(t, got, 4)
	assert.Equal(t, []string{"X"}, toolUseIDs(t, got[1]))
	assert.Equal(t, []string{"X"}, toolResultIDs(t, got[2]),
		"single tool_result still produces one user message with one block")
}

// TestAnthropic_ToolResultRunInterspersedWithText verifies that
// coalescing only spans a contiguous run of tool_result messages and
// stops at non-tool-result boundaries (e.g. a plain user text turn).
func TestAnthropic_ToolResultRunInterspersedWithText(t *testing.T) {
	in := []Message{
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "T1", Name: "f", Input: []byte(`{}`)},
			},
		},
		{Role: "user", ToolCallID: "T1", Content: "r1"},
		{Role: "user", Content: "interjection"},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "T2", Name: "f", Input: []byte(`{}`)},
				{ID: "T3", Name: "f", Input: []byte(`{}`)},
			},
		},
		{Role: "user", ToolCallID: "T2", Content: "r2"},
		{Role: "user", ToolCallID: "T3", Content: "r3"},
		{Role: "user", Content: "trailing text"},
	}

	got := buildAnthropicMessages(in, false)

	// Expected: assistant, user(1 tr), user(text), assistant, user(2 tr), user(text)
	require.Len(t, got, 6)
	assert.Equal(t, []string{"T1"}, toolUseIDs(t, got[0]))
	assert.Equal(t, []string{"T1"}, toolResultIDs(t, got[1]))

	// got[2] is the plain text interjection — user role, no tool_result.
	assert.Equal(t, anthropic.MessageParamRoleUser, got[2].Role)
	require.Len(t, got[2].Content, 1)
	require.NotNil(t, got[2].Content[0].OfText)
	assert.Equal(t, "interjection", got[2].Content[0].OfText.Text)

	assert.Equal(t, []string{"T2", "T3"}, toolUseIDs(t, got[3]))
	assert.Equal(t, []string{"T2", "T3"}, toolResultIDs(t, got[4]),
		"the 2-element tool_result run must coalesce")

	// got[5] is the trailing plain text.
	assert.Equal(t, anthropic.MessageParamRoleUser, got[5].Role)
	require.Len(t, got[5].Content, 1)
	require.NotNil(t, got[5].Content[0].OfText)
	assert.Equal(t, "trailing text", got[5].Content[0].OfText.Text)
}

// TestAnthropic_ToolResultsWithImages verifies that a coalesced run
// containing both plain-text and image-bearing tool_results preserves
// each block's images. The image-bearing branch builds a structured
// content slice; the plain branch uses the SDK helper. Both must end up
// inside the same user message.
func TestAnthropic_ToolResultsWithImages(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4E, 0x47}
	in := []Message{
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "P1", Name: "snap", Input: []byte(`{}`)},
				{ID: "P2", Name: "snap", Input: []byte(`{}`)},
				{ID: "P3", Name: "snap", Input: []byte(`{}`)},
			},
		},
		// Plain text result.
		{Role: "user", ToolCallID: "P1", Content: "no image"},
		// Image-bearing result with caption.
		{
			Role:       "user",
			ToolCallID: "P2",
			Content:    "see attached",
			Images: []ImageContent{
				{MimeType: "image/png", Data: pngBytes},
			},
		},
		// Image-bearing result, error.
		{
			Role:       "user",
			ToolCallID: "P3",
			IsError:    true,
			Images: []ImageContent{
				{MimeType: "image/png", Data: pngBytes},
			},
		},
	}

	got := buildAnthropicMessages(in, false)

	// Expected: assistant(3 tool_uses), user(3 tool_results coalesced).
	require.Len(t, got, 2)
	assert.Equal(t, []string{"P1", "P2", "P3"}, toolUseIDs(t, got[0]))
	assert.Equal(t, []string{"P1", "P2", "P3"}, toolResultIDs(t, got[1]))

	// P1: no image — built via NewToolResultBlock helper, content is
	// either nil/empty or carries text — we don't assert on its shape
	// beyond ID. The key assertion is presence in the same user message.
	require.NotNil(t, got[1].Content[0].OfToolResult)

	// P2: image + text caption.
	p2 := got[1].Content[1].OfToolResult
	require.NotNil(t, p2)
	require.Len(t, p2.Content, 2, "image + text caption -> 2 content items")
	require.NotNil(t, p2.Content[0].OfImage)
	assert.Equal(t,
		base64.StdEncoding.EncodeToString(pngBytes),
		p2.Content[0].OfImage.Source.OfBase64.Data)
	require.NotNil(t, p2.Content[1].OfText)
	assert.Equal(t, "see attached", p2.Content[1].OfText.Text)

	// P3: image only, IsError set.
	p3 := got[1].Content[2].OfToolResult
	require.NotNil(t, p3)
	require.Len(t, p3.Content, 1, "image only -> 1 content item")
	require.NotNil(t, p3.Content[0].OfImage)
	assert.True(t, p3.IsError.Value)
}

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

func TestBuildAnthropicMessagesCacheLastTextBlock(t *testing.T) {
	in := []Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "second"},
	}
	got := buildAnthropicMessages(in, true)
	require.Len(t, got, 3)
	last := got[len(got)-1]
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

func TestBuildAnthropicMessagesCacheLastImageBlock(t *testing.T) {
	// Image-bearing user message: the SDK lays out blocks as [image..., text?].
	// With Content="" the trailing block is the image, so cache_control must
	// land on the OfImage variant.
	in := []Message{{
		Role: "user",
		Images: []ImageContent{
			{MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}},
		},
	}}
	got := buildAnthropicMessages(in, true)
	require.Len(t, got, 1)
	last := got[0]
	require.NotEmpty(t, last.Content)
	tail := last.Content[len(last.Content)-1]
	require.NotNil(t, tail.OfImage, "expected last block to be an image variant")
	cc := tail.GetCacheControl()
	require.NotNil(t, cc)
	require.Equal(t, "ephemeral", string(cc.Type))
}

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
	require.Equal(t, 10, done.Usage.InputTokens)
}
