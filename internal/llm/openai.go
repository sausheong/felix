package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	openai "github.com/sashabaranov/go-openai"
)

// logOpenAIError unwraps the go-openai SDK error into a structured slog.WARN
// record so we can see exactly why LiteLLM/OpenAI rejected the request — the
// SDK's flat error string drops Param, Type, Code, and the raw response body
// that often pinpoints which JSON field is invalid (e.g. "max_tokens" vs
// "max_completion_tokens" on gpt-5 family).
func logOpenAIError(err error, model string, msgCount, toolCount, maxTokens int, temp float32, hasStream bool) {
	attrs := []any{
		"model", model,
		"msg_count", msgCount,
		"tool_count", toolCount,
		"max_completion_tokens", maxTokens,
		"temperature", temp,
		"stream", hasStream,
		"err", err.Error(),
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		attrs = append(attrs,
			"http_status", apiErr.HTTPStatusCode,
			"api_type", apiErr.Type,
			"api_code", fmt.Sprintf("%v", apiErr.Code),
			"api_message", apiErr.Message,
		)
		if apiErr.Param != nil {
			attrs = append(attrs, "api_param", *apiErr.Param)
		}
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		body := reqErr.Body
		if len(body) > 2000 {
			body = body[:2000]
		}
		attrs = append(attrs,
			"http_status", reqErr.HTTPStatusCode,
			"body", string(body),
		)
	}
	slog.Warn("openai chat error", attrs...)
}

// OpenAIProvider implements LLMProvider using the OpenAI Chat Completions API.
type OpenAIProvider struct {
	client *openai.Client
}

// NewOpenAIProvider creates a new OpenAI LLM provider.
// If baseURL is non-empty, the client points to that endpoint (e.g. LiteLLM).
func NewOpenAIProvider(apiKey, baseURL string) *OpenAIProvider {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	client := openai.NewClientWithConfig(cfg)
	return &OpenAIProvider{client: client}
}

func (p *OpenAIProvider) Models() []ModelInfo {
	return []ModelInfo{
		{ID: "gpt-4o", Name: "GPT-4o", Provider: "openai"},
		{ID: "gpt-4o-mini", Name: "GPT-4o Mini", Provider: "openai"},
		{ID: "gpt-4-turbo", Name: "GPT-4 Turbo", Provider: "openai"},
	}
}

func (p *OpenAIProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
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
				// Tool results: always send text-only as the tool message.
				// OpenAI vision (and ollama's vision-capable models) only
				// look at images on user-role messages — image content on a
				// tool message is silently ignored. So when the tool
				// returned images, we emit them as a follow-up user message.
				toolText := m.Content
				if toolText == "" && len(m.Images) > 0 {
					toolText = "(image attached in following message)"
				}
				msgs = append(msgs, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					Content:    toolText,
					ToolCallID: m.ToolCallID,
				})
				if len(m.Images) > 0 {
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
					parts = append(parts, openai.ChatMessagePart{
						Type: openai.ChatMessagePartTypeText,
						Text: "(Image returned by the previous tool call.)",
					})
					msgs = append(msgs, openai.ChatCompletionMessage{
						Role:         openai.ChatMessageRoleUser,
						MultiContent: parts,
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
		model = "gpt-4o"
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	openaiReq := openai.ChatCompletionRequest{
		Model:               model,
		Messages:            msgs,
		MaxCompletionTokens: maxTokens,
		Stream:              true,
		StreamOptions:       &openai.StreamOptions{IncludeUsage: true},
	}

	if len(tools) > 0 {
		openaiReq.Tools = tools
	}

	if req.Temperature > 0 {
		openaiReq.Temperature = float32(req.Temperature)
	}

	stream, err := p.client.CreateChatCompletionStream(ctx, openaiReq)
	if err != nil {
		logOpenAIError(err, model, len(msgs), len(tools), maxTokens, openaiReq.Temperature, true)
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

		var lastUsage *Usage

		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				logOpenAIError(err, model, len(msgs), len(tools), maxTokens, openaiReq.Temperature, true)
				events <- ChatEvent{Type: EventError, Error: err}
				return
			}

			// Capture usage when the provider finally sends it (typically on
			// the final chunk thanks to StreamOptions.IncludeUsage=true).
			if resp.Usage != nil && resp.Usage.TotalTokens > 0 {
				lastUsage = &Usage{
					InputTokens:  resp.Usage.PromptTokens,
					OutputTokens: resp.Usage.CompletionTokens,
				}
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

		events <- ChatEvent{Type: EventDone, Usage: lastUsage}
	}()

	return events, nil
}

// openaiUnsupportedFields are JSON Schema fields the OpenAI function-
// calling schema rejects. anyOf/oneOf/format are accepted and kept.
var openaiUnsupportedFields = []string{"$ref", "definitions"}

// NormalizeToolSchema strips $ref and definitions from each tool's
// JSON Schema. The OpenAI function-calling endpoint rejects schemas
// that contain these (it accepts a restricted JSON Schema subset).
// Diagnostics list every stripped occurrence with a dotted JSON path.
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
