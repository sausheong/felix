package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSafeRawMessage verifies the WS layer's RawMessage guard.
// Empty or invalid input must become nil (marshals to null) instead
// of triggering "unexpected end of JSON input" at marshal time —
// which would abort the entire WebSocket write and leave the chat
// client's tool_call entry stuck in a pending state.
func TestSafeRawMessage(t *testing.T) {
	tests := []struct {
		name string
		in   json.RawMessage
		want any
	}{
		{"nil", nil, nil},
		{"empty", json.RawMessage{}, nil},
		{"whitespace_only_invalid", json.RawMessage(`   `), nil},
		{"truncated_object", json.RawMessage(`{"a":`), nil},
		{"plain_text_invalid", json.RawMessage(`hello world`), nil},
		{"valid_object", json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":1}`)},
		{"valid_null", json.RawMessage(`null`), json.RawMessage(`null`)},
		{"valid_array", json.RawMessage(`[1,2,3]`), json.RawMessage(`[1,2,3]`)},
		{"valid_string", json.RawMessage(`"hi"`), json.RawMessage(`"hi"`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safeRawMessage(tc.in)
			assert.Equal(t, tc.want, got)

			// And — load-bearing — the result must round-trip through
			// json.Marshal without error. That's the regression we're
			// guarding: a valid `null` is fine, valid JSON is fine,
			// invalid bytes become null, never an error.
			_, err := json.Marshal(map[string]any{"input": got})
			assert.NoError(t, err)
		})
	}
}

// TestWriteJSONIsGoroutineSafe is the regression guard for the
// "panic: concurrent write to websocket connection" crash. The
// gateway has multiple paths that write to the same WS conn from
// different goroutines (main agent-event drain loop, trace SetOnMark
// callbacks, mid-stream chat.compact responses). gorilla/websocket
// panics if two of those races into Conn.NextWriter at once. This
// test fans 200 writes across 50 goroutines through writeJSON and
// asserts no panic and that the receiving end can decode every frame.
func TestWriteJSONIsGoroutineSafe(t *testing.T) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		defer releaseConnMutex(conn)

		const goroutines = 50
		const writesPerG = 4
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for g := 0; g < goroutines; g++ {
			g := g
			go func() {
				defer wg.Done()
				for i := 0; i < writesPerG; i++ {
					writeJSON(conn, map[string]any{
						"goroutine": g,
						"index":     i,
					})
				}
			}()
		}
		wg.Wait()
		// One sentinel so the client knows when to stop reading.
		writeJSON(conn, map[string]any{"done": true})
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	got := 0
	for {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("read after %d messages: %v", got, err)
		}
		if done, _ := msg["done"].(bool); done {
			break
		}
		got++
	}
	const expected = 50 * 4
	assert.Equal(t, expected, got, "every write should arrive intact, none dropped")
}
