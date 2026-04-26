package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// QwenProvider implements LLMProvider using the Qwen Chat Completions API.
// Qwen uses an OpenAI-compatible API, so we reuse the go-openai client.
type QwenProvider struct {
	client *openai.Client
}

// NewQwenProvider creates a new Qwen LLM provider.
// The Qwen API is OpenAI-compatible, so we use the go-openai client
// with Qwen's base URL.
func NewQwenProvider(apiKey, baseURL string) *QwenProvider {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	} else {
		// Default to Qwen's official API endpoint
		cfg.BaseURL = "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"
	}
	client := openai.NewClientWithConfig(cfg)
	return &QwenProvider{client: client}
}

func (p *QwenProvider) Models() []ModelInfo {
	return []ModelInfo{
		{ID: "qwen-plus", Name: "Qwen Plus", Provider: "qwen"},
		{ID: "qwen-turbo", Name: "Qwen Turbo", Provider: "qwen"},
		{ID: "qwen-max", Name: "Qwen Max", Provider: "qwen"},
		{ID: "qwen-coder-plus", Name: "Qwen Coder Plus", Provider: "qwen"},
		{ID: "qwen-vl-plus", Name: "Qwen VL Plus", Provider: "qwen"},
	}
}

func (p *QwenProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	// Build messages
	msgs := make([]openai.ChatCompletionMessage, 0, len(req.Messages)+1)

	if req.SystemPrompt != "" {
		msgs = append(msgs, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: req.SystemPrompt,
		})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			if m.ToolCallID != "" {
				if len(m.Images) > 0 {
					// Tool result with images: use multi-content parts
					var parts []openai.ChatMessagePart
					for _, img := range m.Images {
						encoded := base64.StdEncoding.EncodeToString(img.Data)
						dataURI := fmt.Sprintf("data:%s;base64,%s", img.MimeType, encoded)
						parts = append(parts, openai.ChatMessagePart{
							Type: openai.ChatMessagePartTypeImageURL,
							ImageURL: &openai.ChatMessageImageURL{
								URL:    dataURI,
								Detail: openai.ImageURLDetailAuto,
							},
						})
					}
					if m.Content != "" {
						parts = append(parts, openai.ChatMessagePart{
							Type: openai.ChatMessagePartTypeText,
							Text: m.Content,
						})
					}
					msgs = append(msgs, openai.ChatCompletionMessage{
						Role:         openai.ChatMessageRoleTool,
						MultiContent: parts,
						ToolCallID:   m.ToolCallID,
					})
				} else {
					msgs = append(msgs, openai.ChatCompletionMessage{
						Role:       openai.ChatMessageRoleTool,
						Content:    m.Content,
						ToolCallID: m.ToolCallID,
					})
				}
			} else if len(m.Images) > 0 {
				var parts []openai.ChatMessagePart
				for _, img := range m.Images {
					encoded := base64.StdEncoding.EncodeToString(img.Data)
					dataURI := fmt.Sprintf("data:%s;base64,%s", img.MimeType, encoded)
					parts = append(parts, openai.ChatMessagePart{
						Type: openai.ChatMessagePartTypeImageURL,
						ImageURL: &openai.ChatMessageImageURL{
							URL:    dataURI,
							Detail: openai.ImageURLDetailAuto,
						},
					})
				}
				if m.Content != "" {
					parts = append(parts, openai.ChatMessagePart{
						Type: openai.ChatMessagePartTypeText,
						Text: m.Content,
					})
				}
				msgs = append(msgs, openai.ChatCompletionMessage{
					Role:         openai.ChatMessageRoleUser,
					MultiContent: parts,
				})
			} else {
				msgs = append(msgs, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleUser,
					Content: m.Content,
				})
			}
		case "assistant":
			msg := openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: m.Content,
			}
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{
					ID:   tc.ID,
					Type: openai.ToolTypeFunction,
					Function: openai.FunctionCall{
						Name:      tc.Name,
						Arguments: string(tc.Input),
					},
				})
			}
			msgs = append(msgs, msg)
		}
	}

	// Build tools
	var tools []openai.Tool
	for _, t := range req.Tools {
		var params any
		if err := json.Unmarshal(t.Parameters, &params); err != nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}

		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}

	model := req.Model
	if model == "" {
		model = "qwen-plus"
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	openaiReq := openai.ChatCompletionRequest{
		Model:     model,
		Messages:  msgs,
		MaxTokens: maxTokens,
		Stream:    true,
	}

	if len(tools) > 0 {
		openaiReq.Tools = tools
	}

	if req.Temperature > 0 {
		openaiReq.Temperature = float32(req.Temperature)
	}

	if enabled, diag, ok := p.BuildEnableThinking(model, req.Reasoning); ok {
		// TODO: Qwen DashScope expects a custom top-level "enable_thinking"
		// JSON field that go-openai v1.41 does not expose via any
		// ExtraBody/RawJSON mechanism. Wiring it requires either upgrading
		// to a go-openai version with custom-field support or adding a
		// custom HTTP roundtripper. Tracked as a Phase 2 follow-up.
		_ = enabled
		_ = diag
		slog.Info("qwen thinking requested",
			"model", model,
			"requested", string(req.Reasoning),
			"clamped_to_bool", true,
			"reason", diag.Reason,
			"sdk_limitation", "enable_thinking not yet wired to wire format")
	} else if req.Reasoning != ReasoningOff {
		slog.Info("reasoning ignored",
			"provider", "qwen",
			"model", model,
			"requested", string(req.Reasoning),
			"reason", "model does not support thinking")
	}

	stream, err := p.client.CreateChatCompletionStream(ctx, openaiReq)
	if err != nil {
		return nil, err
	}

	events := make(chan ChatEvent, 100)

	go func() {
		defer close(events)
		defer stream.Close()

		// Track tool calls being built up across deltas
		type pendingTC struct {
			id       string
			name     string
			argsJSON string
		}
		toolCalls := make(map[int]*pendingTC)

		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				events <- ChatEvent{Type: EventError, Error: err}
				return
			}

			for _, choice := range resp.Choices {
				delta := choice.Delta

				// Text content
				if delta.Content != "" {
					events <- ChatEvent{
						Type: EventTextDelta,
						Text: delta.Content,
					}
				}

				// Tool calls
				for _, tc := range delta.ToolCalls {
					idx := 0
					if tc.Index != nil {
						idx = *tc.Index
					}
					pending, exists := toolCalls[idx]
					if !exists {
						pending = &pendingTC{}
						toolCalls[idx] = pending
					}

					if tc.ID != "" {
						pending.id = tc.ID
					}
					if tc.Function.Name != "" {
						pending.name = tc.Function.Name
						events <- ChatEvent{
							Type: EventToolCallStart,
							ToolCall: &ToolCall{
								ID:   pending.id,
								Name: pending.name,
							},
						}
					}
					if tc.Function.Arguments != "" {
						pending.argsJSON += tc.Function.Arguments
					}
				}

				// Finish reason
				if choice.FinishReason == openai.FinishReasonToolCalls || choice.FinishReason == openai.FinishReasonStop {
					// Emit completed tool calls
					for _, tc := range toolCalls {
						if tc.name != "" {
							events <- ChatEvent{
								Type: EventToolCallDone,
								ToolCall: &ToolCall{
									ID:    tc.id,
									Name:  tc.name,
									Input: json.RawMessage(tc.argsJSON),
								},
							}
						}
					}
				}
			}
		}

		events <- ChatEvent{Type: EventDone}
	}()

	return events, nil
}

// NormalizeToolSchema strips $ref and definitions from each tool's
// JSON Schema. Qwen DashScope tracks the OpenAI function-calling
// shape, so the same restricted JSON Schema subset applies (and the
// same openaiUnsupportedFields list is reused).
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

// BuildEnableThinking maps a ReasoningMode to Qwen's enable_thinking
// boolean. Returns (false, empty diag, false) when off or the model
// doesn't support thinking. For any non-off mode on a supported model,
// returns (true, clamped diag, true) — the boolean toggle loses the
// requested low/medium/high granularity, so the diag always fires.
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

// qwenSupportsThinking returns true for Qwen models that support the
// enable_thinking parameter. Conservative — unknown IDs default to
// false (only QwQ and Qwen3 family models accept this field).
func qwenSupportsThinking(model string) bool {
	prefixes := []string{"qwen-qwq", "qwen3"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}
