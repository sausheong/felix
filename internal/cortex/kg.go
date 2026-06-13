package cortex

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sausheong/cortex"
	hrt "github.com/sausheong/harness/runtime"
)

// cortexKG adapts *cortex.Cortex to the harness KnowledgeGraph interface,
// enabling the runtime's bounded (800ms) auto-recall + deferred-async ingest.
// Recall relies on cortex's keyword+memory fallback (the adapter never enables
// cortex.WithLLM) so it stays within the recall budget.
type cortexKG struct {
	cx *cortex.Cortex
}

// NewKnowledgeGraph wraps cx; returns nil when cx is nil so the result can be
// passed straight to RuntimeDeps.KGFn (nil disables the whole KG pathway).
func NewKnowledgeGraph(cx *cortex.Cortex) hrt.KnowledgeGraph {
	if cx == nil {
		return nil
	}
	return &cortexKG{cx: cx}
}

const minRecallQueryLen = 8

func (k *cortexKG) ShouldRecall(query string) bool {
	return len(strings.TrimSpace(query)) >= minRecallQueryLen
}

func (k *cortexKG) Recall(ctx context.Context, query string) string {
	results, err := k.cx.Recall(ctx, query, cortex.WithLimit(5))
	if err != nil || len(results) == 0 {
		return ""
	}
	return formatRecall(results)
}

func (k *cortexKG) Ingest(ctx context.Context, thread []hrt.Message) {
	if len(thread) == 0 {
		return
	}
	var b strings.Builder
	for _, m := range thread {
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}
	_ = k.cx.Remember(ctx, strings.TrimSpace(b.String()))
}

// formatRecall renders recall results into a compact prompt hint. Field access
// mirrors internal/tools/cortextools/format.go (cortex.Result.Type/.Content).
func formatRecall(results []cortex.Result) string {
	var b strings.Builder
	b.WriteString("Relevant context from memory:\n")
	for _, r := range results {
		content := r.Content
		if utf8.RuneCountInString(content) > 300 {
			content = string([]rune(content)[:300]) + "..."
		}
		fmt.Fprintf(&b, "- [%s] %s\n", r.Type, content)
	}
	return b.String()
}
