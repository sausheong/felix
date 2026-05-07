// Package llm is a thin re-export shim over github.com/sausheong/harness/llm
// (interfaces + types + normalize + retry) and the per-provider subpackages
// in github.com/sausheong/harness/providers (anthropic, openai, gemini, qwen).
//
// Felix consumers continue to import "internal/llm" for the canonical
// LLMProvider/Message/ChatRequest types and call llm.NewProvider; the
// underlying implementation lives in the harness module so behavior is
// shared with any other consumer of harness.
//
// llm.NewProvider stays Felix-side because it dispatches on Felix's
// "provider name" string convention and threads ProviderOptions
// (APIKey/BaseURL/Kind) into each provider's native constructor — that
// dispatch is opinionated and Felix-shaped, so harness deliberately
// doesn't ship a factory.
package llm

import (
	"context"
	"fmt"

	harness "github.com/sausheong/harness/llm"
	"github.com/sausheong/harness/llm/llmtest"
	"github.com/sausheong/harness/providers/anthropic"
	"github.com/sausheong/harness/providers/gemini"
	"github.com/sausheong/harness/providers/openai"
	"github.com/sausheong/harness/providers/qwen"
)

// Type aliases — drop-in for pre-extraction concrete types.
type (
	EventType            = harness.EventType
	ImageContent         = harness.ImageContent
	Message              = harness.Message
	ToolCall             = harness.ToolCall
	ToolDef              = harness.ToolDef
	Diagnostic           = harness.Diagnostic
	SystemPromptPart     = harness.SystemPromptPart
	ReasoningMode        = harness.ReasoningMode
	ChatRequest          = harness.ChatRequest
	Usage                = harness.Usage
	ChatEvent            = harness.ChatEvent
	ModelInfo            = harness.ModelInfo
	LLMProvider          = harness.LLMProvider
	NonStreamingProvider = harness.NonStreamingProvider
	ProviderOptions      = harness.ProviderOptions
)

// Event constants.
const (
	EventTextDelta     = harness.EventTextDelta
	EventToolCallStart = harness.EventToolCallStart
	EventToolCallDelta = harness.EventToolCallDelta
	EventToolCallDone  = harness.EventToolCallDone
	EventDone          = harness.EventDone
	EventError         = harness.EventError
)

// Reasoning mode constants.
const (
	ReasoningOff    = harness.ReasoningOff
	ReasoningLow    = harness.ReasoningLow
	ReasoningMedium = harness.ReasoningMedium
	ReasoningHigh   = harness.ReasoningHigh
)

// Re-exported helpers.
var (
	ParseProviderModel    = harness.ParseProviderModel
	ParseReasoningMode    = harness.ParseReasoningMode
	JoinSystemPromptParts = harness.JoinSystemPromptParts
	IsRetryableModelError = harness.IsRetryableModelError
	StripFields           = harness.StripFields
	ResetStripCache       = harness.ResetStripCache
)

// NewAnthropicProvider, NewOpenAIProvider, etc. — Felix-side aliases so
// older call sites in cmd/felix and gateway settings can continue to
// import them from "internal/llm" without churn.
var (
	NewAnthropicProvider       = anthropic.NewAnthropicProvider
	NewOpenAIProvider          = openai.NewOpenAIProvider
	NewOpenAIProviderWithKind  = openai.NewOpenAIProviderWithKind
	NewQwenProvider            = qwen.NewQwenProvider
)

// NewGeminiProvider takes a context (the Gemini SDK requires one at
// construction). Wrapper here for symmetry with the other Felix-side
// constructors.
func NewGeminiProvider(ctx context.Context, apiKey string) (LLMProvider, error) {
	return gemini.NewGeminiProvider(ctx, apiKey)
}

// NewProvider is the dispatch factory used by Felix's startup wiring,
// settings UI, and compaction Provider builder. Mirrors the pre-extraction
// switch by Kind / providerName / BaseURL heuristic, but routes through
// the harness/providers/* constructors.
func NewProvider(providerName string, opts ProviderOptions) (LLMProvider, error) {
	kind := opts.Kind
	if kind == "" {
		if opts.BaseURL != "" {
			// Custom base URL with no Kind override → most proxies (LiteLLM, etc.)
			// expose an OpenAI-compatible API.
			kind = "openai-compatible"
		} else {
			kind = providerName
		}
	}

	switch kind {
	case "anthropic":
		return anthropic.NewAnthropicProvider(opts.APIKey, opts.BaseURL), nil
	case "openai":
		return openai.NewOpenAIProviderWithKind(opts.APIKey, opts.BaseURL, "openai"), nil
	case "openai-compatible":
		return openai.NewOpenAIProviderWithKind(opts.APIKey, opts.BaseURL, "openai-compatible"), nil
	case "local":
		return openai.NewOpenAIProviderWithKind("", opts.BaseURL, "local"), nil
	case "gemini":
		return gemini.NewGeminiProvider(context.Background(), opts.APIKey)
	case "qwen":
		return qwen.NewQwenProvider(opts.APIKey, opts.BaseURL), nil
	default:
		return nil, fmt.Errorf("unknown LLM provider kind: %q", kind)
	}
}

// llmtest is re-exported as a sibling subpackage so Felix tests that
// previously imported "internal/llm/llmtest" can keep working.
var _ = llmtest.Base{}
