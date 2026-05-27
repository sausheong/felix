package runs

import (
	"encoding/json"
	"testing"
)

func TestEvent_RoundTripJSON(t *testing.T) {
	in := Event{
		Seq:     7,
		Ts:      "2026-05-23T10:00:00Z",
		Type:    EventTypeTextDelta,
		Payload: json.RawMessage(`{"text":"hi"}`),
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Event
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Seq != 7 || out.Type != EventTypeTextDelta {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestSessionScope_AsMapKey(t *testing.T) {
	m := map[SessionScope]int{}
	m[SessionScope{AgentID: "a", SessionKey: "k"}] = 1
	if m[SessionScope{AgentID: "a", SessionKey: "k"}] != 1 {
		t.Fatal("SessionScope must be usable as map key")
	}
}
