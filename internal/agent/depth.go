package agent

import (
	"os"
	"strconv"
)

// maxAgentDepth returns the maximum subagent recursion depth permitted by the
// current environment. Reads FELIX_MAX_AGENT_DEPTH; defaults to 3 when the
// env var is unset, empty, non-numeric, or non-positive (<= 0).
//
// The cap exists to prevent runaway delegation chains (a parent invokes a
// subagent that invokes another subagent ad infinitum). 3 was chosen as a
// safe default that allows two-hop delegation patterns (default -> researcher
// -> web_fetcher) while still terminating quickly.
func maxAgentDepth() int {
	if v := os.Getenv("FELIX_MAX_AGENT_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 3
}
