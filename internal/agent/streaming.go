package agent

import "os"

// streamingToolsEnabled reports whether streaming tool kickoff is on for
// this process. Reads FELIX_STREAMING_TOOLS; only the literal "1" enables
// the feature. Anything else (unset, "0", "true", "garbage", " 1 ") is off.
//
// Strict "1" rather than truthy parsing keeps the env contract simple and
// matches Claude Code's binary-feature-gate posture.
func streamingToolsEnabled() bool {
	return os.Getenv("FELIX_STREAMING_TOOLS") == "1"
}

// kickoffResult is the channel payload sent by a streaming-kickoff goroutine
// once dispatchTool returns. The goroutine has already paired the session
// entries (via dispatchTool) and emitted the EventToolResult (via
// r.emitToolResult), so the post-stream await loop only needs to know
// whether dispatch was aborted (so it can break out and emit EventAborted).
type kickoffResult struct {
	aborted bool
}

// drainKickoffs blocks until every kickoff channel has received a value, then
// returns. Used on early-return paths (LLM error, abort) so kickoff goroutines
// fully settle before Run() returns and r.events closes — preventing leaks
// and ensuring all paired session entries land before the run is "done".
func drainKickoffs(kickoffs map[string]chan kickoffResult) {
	for _, ch := range kickoffs {
		<-ch
	}
}

// drainKickoffsExcept is drainKickoffs but skips the channel keyed by skipID.
// Used in the abort path where the caller has already received from one
// channel (the first aborted result) and needs to drain the rest.
func drainKickoffsExcept(kickoffs map[string]chan kickoffResult, skipID string) {
	for id, ch := range kickoffs {
		if id == skipID {
			continue
		}
		<-ch
	}
}
