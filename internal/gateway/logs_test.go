package gateway

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSubscribe_CapsSubscribers(t *testing.T) {
	buf := NewLogBuffer(16, slog.NewTextHandler(io.Discard, nil))
	var chans []chan LogEntry
	for i := 0; i < maxSSESubscribers; i++ {
		ch := buf.Subscribe()
		require.NotNil(t, ch, "subscriber %d should be admitted", i)
		chans = append(chans, ch)
	}
	require.Nil(t, buf.Subscribe(), "subscriber beyond cap must be refused")
	for _, ch := range chans {
		buf.Unsubscribe(ch)
	}
}

func TestHandle_FanOutDoesNotBlockOnFullSubscriber(t *testing.T) {
	buf := NewLogBuffer(16, slog.NewTextHandler(io.Discard, nil))
	ch := buf.Subscribe()
	require.NotNil(t, ch)
	defer buf.Unsubscribe(ch)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			_ = buf.Handle(context.Background(), slog.Record{})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle blocked on a full subscriber channel")
	}
}

func TestHandle_ConcurrentUnsubscribeNoPanic(t *testing.T) {
	buf := NewLogBuffer(64, slog.NewTextHandler(io.Discard, nil))
	var wg sync.WaitGroup
	// Producer: log continuously.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = buf.Handle(context.Background(), slog.Record{})
		}
	}()
	// Churn: subscribe then unsubscribe repeatedly, concurrent with Handle.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			ch := buf.Subscribe()
			if ch != nil {
				buf.Unsubscribe(ch)
			}
		}
	}()
	wg.Wait()
	// If we get here without a panic ("send on closed channel"), the fix holds.
}
