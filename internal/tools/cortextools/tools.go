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
//
// A nil cx is valid: the returned tools still expose Name / Description
// / Parameters (so the shared toolReg can list them in the Settings UI
// tool picker), but every Execute returns errCortexNotConfigured.
// Real per-chat execution always goes through chatexec.ChatToolOverlay,
// which holds a non-nil cortex per agent model and intercepts these
// same names before the shared registry sees them.
func BuildTools(cx *cortex.Cortex) []tool.Tool {
	return []tool.Tool{
		&RecallTool{cx: cx},
		&RememberTool{cx: cx},
		&FindEntitiesTool{cx: cx},
		&GetRelationshipsTool{cx: cx},
	}
}

// errCortexNotConfigured is the fallback Execute message when a cortex
// tool is invoked without a wired cortex client. Only the shared
// toolReg's no-cx variant should ever trigger this; the chat overlay
// always supplies a real cx.
const errCortexNotConfigured = "error: cortex not configured for this chat"
