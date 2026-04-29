package agent

import (
	"log/slog"
	"os"
	"strconv"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/tools"
)

// batch is a contiguous group of tool calls that may be dispatched together.
// concurrencySafe=true means they can run in parallel; false means single-call
// (the partitioner emits one batch per unsafe call).
type batch struct {
	concurrencySafe bool
	calls           []llm.ToolCall
}

// partitionToolCalls groups consecutive concurrency-safe calls into one batch
// each, and emits a single-call batch for every unsafe call. Order is
// preserved both within and across batches. Tools not found in the executor
// are treated as unsafe (defensive). If a tool's IsConcurrencySafe panics,
// the recover treats it as unsafe and logs at WARN.
func partitionToolCalls(tcs []llm.ToolCall, ex tools.Executor) []batch {
	out := []batch{}
	for _, tc := range tcs {
		safe := isCallConcurrencySafe(tc, ex)
		// Append to the previous safe batch if both are safe; otherwise start
		// a new batch. Unsafe calls always start their own batch (single-call).
		if safe && len(out) > 0 && out[len(out)-1].concurrencySafe {
			out[len(out)-1].calls = append(out[len(out)-1].calls, tc)
			continue
		}
		out = append(out, batch{concurrencySafe: safe, calls: []llm.ToolCall{tc}})
	}
	return out
}

// isCallConcurrencySafe looks up the tool and asks it; recovers from any
// panic in the tool's IsConcurrencySafe and treats it as unsafe.
func isCallConcurrencySafe(tc llm.ToolCall, ex tools.Executor) (safe bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("tool IsConcurrencySafe panicked; treating as unsafe",
				"tool", tc.Name, "panic", r)
			safe = false
		}
	}()
	tool, ok := ex.Get(tc.Name)
	if !ok {
		return false // unknown tool → unsafe (dispatchTool will report the error)
	}
	return tool.IsConcurrencySafe(tc.Input)
}

// maxToolConcurrency returns the cap on concurrent tool dispatch within a
// safe batch. Reads FELIX_MAX_TOOL_CONCURRENCY (default 10).
func maxToolConcurrency() int {
	if v := os.Getenv("FELIX_MAX_TOOL_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 10
}
