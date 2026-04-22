package compaction

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsContextOverflow(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"anthropic request_too_large", errors.New("anthropic: request_too_large: prompt is too long"), true},
		{"anthropic context length exceeded", errors.New("context length exceeded for model claude"), true},
		{"anthropic input is too long", errors.New("input is too long for the model"), true},
		{"openai context_length_exceeded", errors.New("openai: error code 400 — context_length_exceeded"), true},
		{"openai maximum context length", errors.New("This model's maximum context length is 8192 tokens"), true},
		{"gemini input token count exceeds", errors.New("gemini: input token count exceeds the maximum"), true},
		{"gemini request payload size exceeds", errors.New("request payload size exceeds the limit"), true},
		{"ollama context length exceeded", errors.New("ollama error: context length exceeded"), true},
		{"unrelated network error", errors.New("connection refused"), false},
		{"unrelated 401", errors.New("401 unauthorized"), false},
		{"nil error", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsContextOverflow(tc.err))
		})
	}
}

func TestIsContextOverflowCaseInsensitive(t *testing.T) {
	assert.True(t, IsContextOverflow(errors.New("CONTEXT LENGTH EXCEEDED")))
	assert.True(t, IsContextOverflow(errors.New("Request_Too_Large")))
}
