package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// EventType identifies the kind of streaming event from the LLM.
type EventType int

const (
	EventTextDelta EventType = iota
	EventToolCallStart
	EventToolCallDelta
	EventToolCallDone
	EventDone
	EventError
)

// ImageContent holds image data for multimodal messages.
type ImageContent struct {
	MimeType string // "image/jpeg", "image/png", etc.
	Data     []byte // raw image bytes
}

// Message represents a conversation message.
type Message struct {
	Role       string         `json:"role"` // "user", "assistant", "system"
	Content    string         `json:"content,omitempty"`
	Images     []ImageContent `json:"-"`                      // image attachments (not serialized)
	ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"` // for tool results
	IsError    bool           `json:"is_error,omitempty"`     // for tool results
}

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolDef defines a tool for the LLM.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// Diagnostic describes a single normalization or clamping event emitted
// by NormalizeToolSchema or per-provider reasoning mapping. The runtime
// logs these via slog; they are advisory, not fatal.
type Diagnostic struct {
	ToolName string // empty for non-tool diagnostics (e.g., reasoning)
	Field    string // dotted JSON path: "properties.url.format" — empty for whole-tool actions
	Action   string // "stripped" | "rewritten" | "rejected" | "clamped" | "ignored"
	Reason   string // human-readable; safe to log
}

// SystemPromptPart is one segment of the system prompt. Providers that
// support prompt caching attach a cache marker to parts where Cache=true;
// providers that don't simply concatenate Text fields together.
type SystemPromptPart struct {
	Text  string
	Cache bool // request that the prefix up to and including this part be
	// cached. Anthropic-only; ignored elsewhere.
}

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
// Accepts "" or "off" for ReasoningOff; "low", "medium", "high" for
// the named levels. Case-sensitive. Returns an error for unknown values.
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

// ChatRequest is the input to a streaming chat call.
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

// Usage tracks token usage.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// ChatEvent is a single streaming event from the LLM.
type ChatEvent struct {
	Type     EventType
	Text     string
	ToolCall *ToolCall
	Usage    *Usage
	Error    error
}

// ModelInfo describes an available model.
type ModelInfo struct {
	ID       string
	Name     string
	Provider string
}

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

// ParseProviderModel splits "provider/model" into (provider, model).
// If no slash is present, returns ("", name) and the caller should use a default.
func ParseProviderModel(name string) (provider, model string) {
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", name
}

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

// ProviderOptions holds connection details for creating an LLM provider.
type ProviderOptions struct {
	APIKey  string
	BaseURL string
	Kind    string // override: "openai-compatible" uses OpenAI client with custom URL
}

// NewProvider creates an LLMProvider for the given provider name.
func NewProvider(providerName string, opts ProviderOptions) (LLMProvider, error) {
	// If Kind is set, use it to override the provider type.
	// This lets e.g. "anthropic" route through an OpenAI-compatible proxy like LiteLLM.
	kind := opts.Kind
	if kind == "" {
		// If a custom base URL is set, default to openai-compatible
		// since most proxies (LiteLLM, etc.) expose an OpenAI-compatible API.
		if opts.BaseURL != "" {
			kind = "openai-compatible"
		} else {
			kind = providerName
		}
	}

	switch kind {
	case "anthropic":
		return NewAnthropicProvider(opts.APIKey, opts.BaseURL), nil
	case "openai":
		return NewOpenAIProviderWithKind(opts.APIKey, opts.BaseURL, "openai"), nil
	case "openai-compatible":
		return NewOpenAIProviderWithKind(opts.APIKey, opts.BaseURL, "openai-compatible"), nil
	case "local":
		return NewOpenAIProviderWithKind("", opts.BaseURL, "local"), nil
	case "gemini":
		return NewGeminiProvider(context.Background(), opts.APIKey)
	case "qwen":
		return NewQwenProvider(opts.APIKey, opts.BaseURL), nil
	default:
		return nil, fmt.Errorf("unknown LLM provider kind: %q", kind)
	}
}
