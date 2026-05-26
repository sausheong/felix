package runs

import "encoding/json"

// Status is the lifecycle state of a run as recorded in index.json.
type Status string

const (
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusCancelled   Status = "cancelled"
	StatusInterrupted Status = "interrupted"
	StatusFailed      Status = "failed"
)

// EventType discriminates entries in the per-run JSONL log.
type EventType string

const (
	EventTypeTextDelta         EventType = "text_delta"
	EventTypeToolCallStart     EventType = "tool_call_start"
	EventTypeToolCallEnd       EventType = "tool_call_end"
	EventTypeToolResult        EventType = "tool_result"
	EventTypeTrace             EventType = "trace"
	EventTypeError             EventType = "error"
	EventTypeAborted           EventType = "aborted"
	EventTypeCompactionStart   EventType = "compaction.start"
	EventTypeCompactionDone    EventType = "compaction.done"
	EventTypeCompactionSkipped EventType = "compaction.skipped"
	// EventTypeAgentDone wraps agent.EventDone so the run-level
	// EventTypeDone stays reserved for the terminal event written by
	// Run.Finish (and scanned for by recovery.go).
	EventTypeAgentDone EventType = "agent_done"
	EventTypeDone      EventType = "done"
)

// CancelReason annotates Done events with status=cancelled.
type CancelReason string

const (
	ReasonUserAbort  CancelReason = "user_abort"
	ReasonSuperseded CancelReason = "superseded"
)

// Event is a single line in <runID>.jsonl. Payload holds the
// event-type-specific JSON object; the runs package does not interpret it.
type Event struct {
	Seq     int64           `json:"seq"`
	Ts      string          `json:"ts"` // RFC3339Nano
	Type    EventType       `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`

	// Done-event fields (only set when Type == EventTypeDone).
	Status       Status       `json:"status,omitempty"`
	Reason       CancelReason `json:"reason,omitempty"`
	SupersededBy string       `json:"superseded_by,omitempty"`
	Error        string       `json:"error,omitempty"`
}

// SessionScope is the (agent, session) tuple used as registry key.
type SessionScope struct {
	AgentID    string
	SessionKey string
}

// RunSummary is what Snapshot returns and what index.json holds per run.
type RunSummary struct {
	ID           string `json:"id"`
	StartedAt    string `json:"started_at"`
	EndedAt      string `json:"ended_at,omitempty"`
	Status       Status `json:"status"`
	LastSeq      int64  `json:"last_seq"`
	SupersededBy string `json:"superseded_by,omitempty"`
}
