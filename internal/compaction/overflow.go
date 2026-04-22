// Package compaction provides session compaction: detect long sessions,
// summarize the older portion, preserve recent turns verbatim.
package compaction

import "strings"

// overflowSignatures lists substrings that indicate a model returned a
// context-window-too-long error. New providers add their substrings here.
//
// All matches are case-insensitive.
var overflowSignatures = []string{
	// Anthropic
	"request_too_large",
	"context length exceeded",
	"input is too long",
	// OpenAI
	"context_length_exceeded",
	"maximum context length",
	// Gemini
	"input token count exceeds",
	"request payload size exceeds",
	// Ollama (also matches "context length exceeded" above)
	"ollama error: context length",
}

// IsContextOverflow reports whether err looks like a provider returning
// "your prompt is too big". Reactive compaction triggers on this.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range overflowSignatures {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}
