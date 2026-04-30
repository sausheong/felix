package agent

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	conversation "github.com/sausheong/cortex/connector/conversation"
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
		// One INFO log per tool so operators can see whether parallelSafe
		// is taking effect at runtime. Cheap (one log per tool call) and
		// matches the existing trace-mark cadence.
		slog.Info("partition: classified tool call",
			"tool", tc.Name, "concurrencySafe", safe)
		// Append to the previous safe batch if both are safe; otherwise start
		// a new batch. Unsafe calls always start their own batch (single-call).
		if safe && len(out) > 0 && out[len(out)-1].concurrencySafe {
			out[len(out)-1].calls = append(out[len(out)-1].calls, tc)
			continue
		}
		out = append(out, batch{concurrencySafe: safe, calls: []llm.ToolCall{tc}})
	}
	// One INFO log per partition pass summarising the resulting batch shape.
	if len(tcs) > 1 {
		shape := make([]int, 0, len(out))
		for _, b := range out {
			shape = append(shape, len(b.calls))
		}
		slog.Info("partition: built batches", "calls", len(tcs), "batches", len(out), "shape", shape)
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
// safe batch. Precedence:
//  1. Runtime.AgentLoop.MaxToolConcurrency (>0) — config wins.
//  2. FELIX_MAX_TOOL_CONCURRENCY env var (>0) — env fallback.
//  3. Default 10.
func (r *Runtime) maxToolConcurrency() int {
	if r.AgentLoop.MaxToolConcurrency > 0 {
		return r.AgentLoop.MaxToolConcurrency
	}
	if v := os.Getenv("FELIX_MAX_TOOL_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 10
}

// runBatch dispatches the calls in a batch. Concurrency-safe batches with len
// > 1 fan out via sync.WaitGroup + semaphore (capped by maxToolConcurrency()).
// Single-call batches (any safety) call dispatchTool directly. Returns true
// if any call was aborted, in which case Run should break the outer loop.
//
// Sibling cancellation: parallel goroutines see the parent ctx. They are NOT
// cancelled by sibling errors or sibling aborts — only by user abort (parent
// ctx cancel). Reads are independent; a failed web_fetch should not invalidate
// a parallel read_file.
//
// Event emission: each goroutine emits EventToolResult via r.emit as it
// completes (completion order, not input order). When this function returns,
// every call in the batch has had its event emitted. Subagent runtimes
// forward each emission up to their parent's events channel.
func (r *Runtime) runBatch(
	ctx context.Context,
	b batch,
	cortexThread *[]conversation.Message,
	turn int,
	tr *Trace,
) (aborted bool) {
	// Single-call batch (safe or unsafe): direct synchronous dispatch.
	if len(b.calls) == 1 {
		tc := b.calls[0]
		result, abortedOne := r.dispatchTool(ctx, tc, cortexThread)
		r.emitToolResult(tr, turn, tc, result, abortedOne)
		return abortedOne
	}

	// Parallel batch: WaitGroup + semaphore.
	var (
		wg         sync.WaitGroup
		anyAborted atomic.Bool
		sem        = make(chan struct{}, r.maxToolConcurrency())
	)
	wg.Add(len(b.calls))
	for i := range b.calls {
		tc := b.calls[i]
		sem <- struct{}{} // block when at cap
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			result, abortedOne := r.dispatchTool(ctx, tc, cortexThread)
			r.emitToolResult(tr, turn, tc, result, abortedOne)
			if abortedOne {
				anyAborted.Store(true)
			}
		}()
	}
	wg.Wait()
	return anyAborted.Load()
}

// emitToolResult writes the per-call trace mark, log line, and EventToolResult.
// Centralised so the parallel runBatch and the single-call path produce the
// same event/log shape. Routes through r.emit so subagent runtimes forward
// the event to their parent's channel as well.
func (r *Runtime) emitToolResult(tr *Trace, turn int, tc llm.ToolCall, result tools.ToolResult, aborted bool) {
	tr.Mark("tool.exec",
		"turn", turn,
		"tool", tc.Name,
		"err", result.Error != "",
		"output_chars", len(result.Output),
		"aborted", aborted)

	if result.Error != "" {
		slog.Warn("tool error", "tool", tc.Name, "id", tc.ID, "error", result.Error)
	} else {
		outPreview := result.Output
		if len(outPreview) > 500 {
			outPreview = outPreview[:500] + "...(truncated)"
		}
		slog.Debug("tool result", "tool", tc.Name, "id", tc.ID, "output_len", len(result.Output), "output", outPreview)
	}

	r.emit(AgentEvent{
		Type:     EventToolResult,
		ToolCall: &tc,
		Result:   &result,
	})
}
