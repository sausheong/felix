package gateway

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
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
