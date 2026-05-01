package channel

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewCLIChannelInitialState — fresh channel must report disconnected
// (no goroutine running yet) so a caller that defensively checks Status
// before Connect doesn't see a stale "connected" from a recycled value.
func TestNewCLIChannelInitialState(t *testing.T) {
	c := NewCLIChannel()
	assert.Equal(t, StatusDisconnected, c.Status())
	assert.Equal(t, "cli", c.Name())
	require.NotNil(t, c.Receive(), "Receive must return a non-nil channel even before Connect")
}

// TestCLIChannelConnectFlipsStatus — Connect must transition to
// Connected synchronously (the read goroutine launches in the
// background, but Status reflects the new state immediately so a
// caller can rely on the post-Connect invariant).
func TestCLIChannelConnectFlipsStatus(t *testing.T) {
	c := NewCLIChannel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, c.Connect(ctx))
	assert.Equal(t, StatusConnected, c.Status())

	require.NoError(t, c.Disconnect())
	assert.Equal(t, StatusDisconnected, c.Status())
}

// TestCLIChannelDisconnectWithoutConnect — call sites that bail out
// before Connect (e.g., config error in main()) still call Disconnect
// from a defer. Must not panic on the nil cancel func.
func TestCLIChannelDisconnectWithoutConnect(t *testing.T) {
	c := NewCLIChannel()
	require.NoError(t, c.Disconnect())
	assert.Equal(t, StatusDisconnected, c.Status())
}

// TestCLIChannelDoubleDisconnect — guards against the "deferred
// Disconnect at outer scope plus an explicit Disconnect on shutdown
// path" pattern. Second call should be a no-op, not a panic on a
// closed channel or already-cancelled context.
func TestCLIChannelDoubleDisconnect(t *testing.T) {
	c := NewCLIChannel()
	require.NoError(t, c.Connect(context.Background()))
	require.NoError(t, c.Disconnect())
	require.NoError(t, c.Disconnect())
}

// TestCLIChannelSendWritesToStdout — the only side effect of Send
// is writing msg.Text + newline to stdout. We swap os.Stdout for a
// pipe so the assertion doesn't depend on terminal capture.
func TestCLIChannelSendWritesToStdout(t *testing.T) {
	c := NewCLIChannel()

	r, w, err := os.Pipe()
	require.NoError(t, err)
	origStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	go func() {
		_ = c.Send(context.Background(), OutboundMessage{Text: "hello world"})
		w.Close()
	}()

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "hello world\n", string(out))
}

// TestCLIChannelReceiveChannelStable — the channel returned by
// Receive must be stable across calls (callers cache it for select
// loops). A new channel each call would silently break delivery.
// We send on the underlying inbound channel and assert that the
// public Receive() handle observes it on both calls.
func TestCLIChannelReceiveChannelStable(t *testing.T) {
	c := NewCLIChannel()
	a := c.Receive()
	b := c.Receive()
	c.inbound <- InboundMessage{Text: "ping"}
	select {
	case msg := <-a:
		assert.Equal(t, "ping", msg.Text)
	case <-time.After(time.Second):
		t.Fatal("first Receive() handle did not observe inbound write")
	}
	c.inbound <- InboundMessage{Text: "pong"}
	select {
	case msg := <-b:
		assert.Equal(t, "pong", msg.Text)
	case <-time.After(time.Second):
		t.Fatal("second Receive() handle did not observe inbound write — Receive returned a fresh channel")
	}
}

// TestCLIChannelInterfaceCompliance — guards against the Channel
// interface getting a new method added without an impl on
// CLIChannel. Compile-time check via assignment.
func TestCLIChannelInterfaceCompliance(t *testing.T) {
	var _ Channel = (*CLIChannel)(nil)
}
