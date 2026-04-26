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
// trimmed, formatted summary text. additionalInstructions is appended to
// the prompt when non-empty (used by manual /compact <focus...>).
//
// The call wraps three fallback stages:
//  1. Full transcript — preferred; preserves all detail.
//  2. Small-only — drops oversized messages, keeps a note that they were
//     elided. Triggered when stage 1 returns a context-overflow error
//     or a stream error.
//  3. Placeholder — a static stub indicating compaction failed; never
//     returns an error to the caller so the agent loop can continue.
//
// Each stage applies FormatCompactSummary to strip the analysis scratchpad
// and unwrap the summary block.
func (s *Summarizer) Summarize(ctx context.Context, entries []session.SessionEntry, additionalInstructions string) (string, error) {
	return s.summarizeWithFallback(ctx, entries, additionalInstructions)
}

func (s *Summarizer) summarizeWithFallback(ctx context.Context, entries []session.SessionEntry, additionalInstructions string) (string, error) {
	// Stage 1: full transcript.
	out, err := s.callOnce(ctx, BuildTranscript(entries), additionalInstructions)
	if err == nil && out != "" {
		return out, nil
	}
	stage1Err := err

	// Context cancellation/deadline propagates up — the caller asked us to
	// stop, so don't burn an extra provider call on stage 2 or paper over
	// the cancel with a placeholder. This preserves the Manager's ability
	// to classify cancellation/timeout distinctly from "compaction is
	// genuinely broken; degrade gracefully".
	//
	// We also propagate a per-call deadline (Summarizer.Timeout firing while
	// the parent ctx is still alive) — otherwise a per-call timeout would
	// flow into stage 2/3, masking timeout as a placeholder. Manager
	// classifies the wrapped DeadlineExceeded as Skipped: "timeout".
	if ctxErr := ctx.Err(); ctxErr != nil || errors.Is(stage1Err, context.DeadlineExceeded) {
		return "", stage1Err
	}

	// Stage 2: drop oversized messages and retry. Only meaningful when
	// buildSmallOnlyTranscript actually elides something — otherwise we'd
	// be re-sending the same transcript with the same prompt against the
	// same provider (predictable failure, wasted call). Fall through to
	// stage 3 in that case.
	if isOverflowError(stage1Err) || isStreamError(stage1Err) {
		small, droppedCount := buildSmallOnlyTranscript(entries)
		if droppedCount > 0 {
			small += fmt.Sprintf("\n[oversized message(s) elided: %d]\n", droppedCount)
			out, err = s.callOnce(ctx, small, additionalInstructions)
			if err == nil && out != "" {
				return out, nil
			}
		}
	}

	// Stage 3: placeholder. Never returns an error — the agent loop must
	// be able to continue even if compaction is wholly broken.
	return placeholderSummary(len(entries)), nil
}

// callOnce performs a single summarizer invocation against a pre-built
// transcript. Returns the formatted summary text or an error.
func (s *Summarizer) callOnce(ctx context.Context, transcript, additionalInstructions string) (string, error) {
	prompt := BuildPrompt(transcript, additionalInstructions)
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req := llm.ChatRequest{
		Model:     s.Model,
		Messages:  []llm.Message{{Role: "user", Content: prompt}},
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

// maxSmallEntryLen is the per-entry size threshold for stage 2's
// drop-the-oversized-stuff transcript. Entries larger than this are
// elided. The threshold tracks maxTranscriptToolResultLen so a single
// hot-path tool result doesn't get re-elided here — only entries that
// blow far past the per-result cap are dropped.
const maxSmallEntryLen = maxTranscriptToolResultLen

// buildSmallOnlyTranscript renders entries while skipping any single-entry
// payload larger than maxSmallEntryLen. Returns the transcript and the
// count of dropped entries so the caller can append a note.
func buildSmallOnlyTranscript(entries []session.SessionEntry) (string, int) {
	var dropped int
	kept := make([]session.SessionEntry, 0, len(entries))
	for _, e := range entries {
		if len(e.Data) > maxSmallEntryLen {
			dropped++
			continue
		}
		kept = append(kept, e)
	}
	return BuildTranscript(kept), dropped
}

// placeholderSummary is the stage-3 fallback. It must be a valid summary
// the model can pick up from — minimally describing what was elided so
// the next turn doesn't act as if the conversation was empty. The
// "compaction failed and the summary could not be generated" phrase is
// stable: Task 5's circuit breaker detects placeholders by string match
// on this fragment to count them as failures for breaker accounting.
func placeholderSummary(entryCount int) string {
	return fmt.Sprintf(
		"Summary:\nConversation history (%d entries) — compaction failed and the summary could not be generated. "+
			"The conversation continues; refer to the recent preserved turns and ask the user for any context you need.",
		entryCount,
	)
}

// isOverflowError reports whether err looks like a "your prompt is too big"
// signal from any provider. Re-uses the overflow signature list rather
// than depending on package-level state.
func isOverflowError(err error) bool {
	return err != nil && IsContextOverflow(err)
}

// isStreamError reports whether err originated from the streaming layer
// (vs being a wrapper of context.DeadlineExceeded etc.). Used to decide
// whether the second stage is worth attempting.
func isStreamError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "stream error")
}
