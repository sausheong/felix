package agent

import (
	"os"
	"strconv"
)

// maxAgentDepth returns the maximum subagent recursion depth permitted for
// this Runtime. Precedence:
//  1. Runtime.AgentLoop.MaxAgentDepth (>0) — config wins.
//  2. FELIX_MAX_AGENT_DEPTH env var (>0) — env fallback.
//  3. Default 3.
//
// The cap exists to prevent runaway delegation chains (a parent invokes a
// subagent that invokes another subagent ad infinitum). 3 was chosen as a
// safe default that allows two-hop delegation patterns (default -> researcher
// -> web_fetcher) while still terminating quickly.
func (r *Runtime) maxAgentDepth() int {
	if r.AgentLoop.MaxAgentDepth > 0 {
		return r.AgentLoop.MaxAgentDepth
	}
	if v := os.Getenv("FELIX_MAX_AGENT_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 3
}
