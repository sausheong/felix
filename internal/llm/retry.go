package llm

import (
	"errors"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/sashabaranov/go-openai"
)

// IsRetryableModelError reports whether err is a transient capacity
// failure from a hosted LLM provider — the kind of error a fallback
// model can plausibly succeed at. Recognised:
//
//   - Anthropic 429 (rate limit) and 529 (overloaded)
//   - OpenAI 429 (rate limit) and 5xx (server / overloaded)
//
// Anything else (including 4xx auth/validation errors) returns false:
// retrying with a different model wouldn't fix the underlying problem.
//
// Errors from local providers (Ollama, etc.) and Gemini are NOT
// classified as retryable here; their failure modes are different
// and a model swap rarely helps. Add cases as new providers
// accumulate retry experience.
func IsRetryableModelError(err error) bool {
	if err == nil {
		return false
	}

	// Typed-error path: when ANY known SDK error type matches, return
	// its decision authoritatively. Falling through to substring
	// matching after a typed "not retryable" verdict would mask
	// 4xx auth/validation failures whose .Error() text happens to
	// contain numbers like "429" in unrelated contexts (e.g., a
	// quota path or a request id).
	var anthErr *anthropic.Error
	if errors.As(err, &anthErr) {
		return anthErr.StatusCode == 429 || anthErr.StatusCode == 529
	}
	var openaiAPIErr *openai.APIError
	if errors.As(err, &openaiAPIErr) {
		return openaiAPIErr.HTTPStatusCode == 429 ||
			(openaiAPIErr.HTTPStatusCode >= 500 && openaiAPIErr.HTTPStatusCode < 600)
	}
	var openaiReqErr *openai.RequestError
	if errors.As(err, &openaiReqErr) {
		return openaiReqErr.HTTPStatusCode == 429 ||
			(openaiReqErr.HTTPStatusCode >= 500 && openaiReqErr.HTTPStatusCode < 600)
	}

	// Last-ditch: substring match on common SDK error wrappers that
	// don't unwrap cleanly to typed errors. Cheap belt-and-braces;
	// only reached when no typed match found above.
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "429") || strings.Contains(msg, "529") ||
		strings.Contains(msg, "rate limit") || strings.Contains(msg, "overloaded") {
		return true
	}
	return false
}
