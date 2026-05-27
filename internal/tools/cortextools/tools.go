// Package cortextools exposes the cortex knowledge graph as four native
// Felix tools: recall, remember, find_entities, get_relationships. Each
// tool wraps a single *cortex.Cortex instance captured at construction
// time. Used in place of the older automatic-per-turn recall + ingest
// pathway that ran via the harness KnowledgeGraph interface.
package cortextools

import (
	"github.com/sausheong/cortex"
	"github.com/sausheong/harness/tool"
)

// BuildTools returns the four cortex-backed tools wired against cx.
// Returns nil when cx is nil so callers can pass through without a
// per-call nil check.
func BuildTools(cx *cortex.Cortex) []tool.Tool {
	if cx == nil {
		return nil
	}
	return []tool.Tool{
		&RecallTool{cx: cx},
		&RememberTool{cx: cx},
		&FindEntitiesTool{cx: cx},
		&GetRelationshipsTool{cx: cx},
	}
}
