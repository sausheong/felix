package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/session"
)

// ErrEmptySummary is returned when the LLM emits no usable summary text.
var ErrEmptySummary = errors.New("compaction: empty summary returned")

// Summarizer wraps an llm.LLMProvider with the prompt and call shape used
// for compaction. The provider is expected to be the bundled Ollama in
// production but any LLMProvider works (used for tests).
type Summarizer struct {
	Provider llm.LLMProvider
	Model    string        // bare model id, e.g. "qwen2.5:3b-instruct"
	Timeout  time.Duration // per-call deadline; 0 → 60s
}

// Summarize sends entries through the configured provider and returns the
// trimmed summary text. additionalInstructions is appended to the prompt
// when non-empty (used by manual /compact <focus...>).
func (s *Summarizer) Summarize(ctx context.Context, entries []session.SessionEntry, additionalInstructions string) (string, error) {
	transcript := BuildTranscript(entries)
	prompt := BuildPrompt(transcript, additionalInstructions)

	timeout := s.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req := llm.ChatRequest{
		Model: s.Model,
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
		MaxTokens: 4096,
	}

	stream, err := s.Provider.ChatStream(callCtx, req)
	if err != nil {
		return "", fmt.Errorf("compaction: chat stream: %w", err)
	}

	var sb strings.Builder
	for ev := range stream {
		switch ev.Type {
		case llm.EventTextDelta:
			sb.WriteString(ev.Text)
		case llm.EventError:
			return "", fmt.Errorf("compaction: stream error: %w", ev.Error)
		}
	}

	out := strings.TrimSpace(sb.String())
	if out == "" {
		return "", ErrEmptySummary
	}
	out = FormatCompactSummary(out)
	if out == "" {
		return "", ErrEmptySummary
	}
	return out, nil
}
