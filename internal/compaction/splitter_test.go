package compaction

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sausheong/felix/internal/session"
)

// makeHistory builds a slice of SessionEntry with the given roles, in order.
// "user" or "assistant" → message; "tc" → tool_call; "tr" → tool_result.
func makeHistory(roles ...string) []session.SessionEntry {
	var out []session.SessionEntry
	for _, r := range roles {
		switch r {
		case "user":
			out = append(out, session.UserMessageEntry("u"))
		case "assistant":
			out = append(out, session.AssistantMessageEntry("a"))
		case "tc":
			out = append(out, session.ToolCallEntry("id1", "bash", []byte(`{}`)))
		case "tr":
			out = append(out, session.ToolResultEntry("id1", "out", "", nil))
		}
	}
	return out
}

func TestSplitFiveUserMessagesK4(t *testing.T) {
	// 5 user msgs → cutoff after the 1st user msg. compact = [u1, a1].
	h := makeHistory("user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant")
	toCompact, toPreserve, ok := Split(h, 4)
	assert.True(t, ok)
	assert.Len(t, toCompact, 2, "first user+assistant")
	assert.Len(t, toPreserve, 8, "last 4 user msgs + their assistant replies")
}

func TestSplitExactlyKUserMessagesRefuses(t *testing.T) {
	h := makeHistory("user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant")
	_, _, ok := Split(h, 4)
	assert.False(t, ok, "exactly K user msgs → no cutoff exists")
}

func TestSplitFewerThanKUserMessagesRefuses(t *testing.T) {
	h := makeHistory("user", "assistant", "user", "assistant")
	_, _, ok := Split(h, 4)
	assert.False(t, ok)
}

func TestSplitZeroUserMessagesRefuses(t *testing.T) {
	h := makeHistory("assistant")
	_, _, ok := Split(h, 4)
	assert.False(t, ok)
}

func TestSplitPreservesToolPair(t *testing.T) {
	// 5 user msgs, with a tool pair attached to the last assistant turn.
	h := makeHistory("user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant", "tc", "tr")
	toCompact, toPreserve, ok := Split(h, 4)
	assert.True(t, ok)
	// Cutoff is after first user+assistant. Preserved range starts at the 2nd user msg.
	assert.Len(t, toCompact, 2)
	// Preserved tail must include the trailing tc/tr together.
	last := toPreserve[len(toPreserve)-1]
	prevToLast := toPreserve[len(toPreserve)-2]
	assert.Equal(t, session.EntryTypeToolResult, last.Type)
	assert.Equal(t, session.EntryTypeToolCall, prevToLast.Type)
}

func TestSplitCompactRangeNeverContainsLastUserMsg(t *testing.T) {
	h := makeHistory("user", "assistant", "user", "assistant", "user", "user", "user", "user", "user")
	toCompact, _, ok := Split(h, 4)
	assert.True(t, ok)
	for _, e := range toCompact {
		if e.Type == session.EntryTypeMessage && e.Role == "user" {
			// the last 4 user msgs must NOT be in toCompact
			// (we have 6 user msgs total → at most 2 in toCompact)
		}
	}
	// Preserved must contain the last 4 user msgs.
	userInPreserve := 0
	_, toPreserve, _ := Split(h, 4)
	for _, e := range toPreserve {
		if e.Type == session.EntryTypeMessage && e.Role == "user" {
			userInPreserve++
		}
	}
	assert.Equal(t, 4, userInPreserve)
}
