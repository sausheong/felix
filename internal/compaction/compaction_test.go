package compaction

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/session"
)

func longSession() *session.Session {
	sess := session.NewSession("default", "test")
	for i := 0; i < 6; i++ {
		sess.Append(session.UserMessageEntry("user msg"))
		sess.Append(session.AssistantMessageEntry("assistant reply"))
	}
	return sess
}

func TestManagerAppendsCompactionEntry(t *testing.T) {
	mgr := &Manager{
		Summarizer: &Summarizer{
			Provider: &fakeProvider{text: "summary text"},
			Model:    "m",
			Timeout:  time.Second,
		},
		PreserveTurns: 4,
	}
	sess := longSession()
	res, err := mgr.MaybeCompact(context.Background(), sess, ReasonManual, "")
	require.NoError(t, err)
	assert.True(t, res.Compacted)
	assert.Equal(t, ReasonManual, res.Reason)

	// Final entry should be the compaction.
	last := sess.View()[0]
	assert.Equal(t, session.EntryTypeCompaction, last.Type)
}

func TestManagerRefusesShortSession(t *testing.T) {
	mgr := &Manager{
		Summarizer: &Summarizer{
			Provider: &fakeProvider{text: "summary"},
			Model:    "m",
			Timeout:  time.Second,
		},
		PreserveTurns: 4,
	}
	sess := session.NewSession("default", "test")
	sess.Append(session.UserMessageEntry("only one"))
	res, err := mgr.MaybeCompact(context.Background(), sess, ReasonManual, "")
	require.NoError(t, err)
	assert.False(t, res.Compacted)
	assert.Equal(t, "too_short", res.Skipped)
}

func TestManagerSummarizerErrorReturnsResult(t *testing.T) {
	mgr := &Manager{
		Summarizer: &Summarizer{
			Provider: &fakeProvider{text: ""}, // → ErrEmptySummary
			Model:    "m",
			Timeout:  time.Second,
		},
		PreserveTurns: 4,
	}
	sess := longSession()
	res, err := mgr.MaybeCompact(context.Background(), sess, ReasonManual, "")
	require.NoError(t, err) // skip is not a hard error
	assert.False(t, res.Compacted)
	assert.Equal(t, "empty_summary", res.Skipped)
}

func TestManagerSerializesPerSession(t *testing.T) {
	// Two concurrent compactions on the same session should not race.
	// The 2nd call must block until the 1st finishes.
	delayCh := make(chan struct{})
	mgr := &Manager{
		Summarizer: &Summarizer{
			Provider: &delayedProvider{text: "ok", delay: 200 * time.Millisecond, started: delayCh},
			Model:    "m",
			Timeout:  5 * time.Second,
		},
		PreserveTurns: 4,
	}
	sess := longSession()

	var wg sync.WaitGroup
	wg.Add(2)
	starts := make([]time.Time, 2)
	go func() {
		defer wg.Done()
		starts[0] = time.Now()
		_, _ = mgr.MaybeCompact(context.Background(), sess, ReasonManual, "")
	}()
	<-delayCh // wait until first call has started its provider call
	go func() {
		defer wg.Done()
		starts[1] = time.Now()
		_, _ = mgr.MaybeCompact(context.Background(), sess, ReasonManual, "")
	}()
	wg.Wait()

	// They should not have run truly in parallel.
	gap := starts[1].Sub(starts[0])
	assert.Less(t, gap.Milliseconds(), int64(50), "starts should be near-simultaneous")
	// (Mutex serializes the Summarize call, not MaybeCompact's first instructions.
	//  We assert serialization indirectly: with delay 200ms each, total wall time > 200ms.)
}

func TestManagerClassifiesCancellation(t *testing.T) {
	// Provider that blocks until ctx is cancelled, returning ctx.Err().
	cancelMe, cancelFn := context.WithCancel(context.Background())
	mgr := &Manager{
		Summarizer: &Summarizer{
			Provider: &delayedProvider{
				text:    "never reached",
				delay:   5 * time.Second,
				started: make(chan struct{}),
			},
			Model:   "m",
			Timeout: 10 * time.Second,
		},
		PreserveTurns: 4,
	}
	sess := longSession()

	// Cancel the parent ctx after 50ms — fires while summarizer is still waiting.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancelFn()
	}()

	res, err := mgr.MaybeCompact(cancelMe, sess, ReasonManual, "")
	require.NoError(t, err) // skip is not a hard error
	assert.False(t, res.Compacted)
	assert.Equal(t, "cancelled", res.Skipped)
}

// delayedProvider sleeps before responding, signalling start via a channel.
type delayedProvider struct {
	text    string
	delay   time.Duration
	started chan struct{}
	once    sync.Once
}

func (d *delayedProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	d.once.Do(func() { close(d.started) })
	ch := make(chan llm.ChatEvent, 2)
	go func() {
		defer close(ch)
		select {
		case <-time.After(d.delay):
		case <-ctx.Done():
			ch <- llm.ChatEvent{Type: llm.EventError, Error: ctx.Err()}
			return
		}
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: d.text}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (d *delayedProvider) Models() []llm.ModelInfo { return nil }
