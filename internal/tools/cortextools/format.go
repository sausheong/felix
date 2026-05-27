package cortextools

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/sausheong/cortex"
)

// formatRecallResults renders cortex.Recall hits as a markdown bullet list
// suitable for direct return from the recall tool. Mirrors the shape used
// by internal/cortex.FormatResults but lives here to avoid pulling the
// internal/cortex package (and its config-side dependency chain) into the
// tools tree — that would create an import cycle through internal/mcp.
func formatRecallResults(results []cortex.Result) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range results {
		switch r.Type {
		case "entity":
			b.WriteString("- [entity] ")
		case "memory":
			b.WriteString("- [memory] ")
		case "chunk":
			b.WriteString("- [context] ")
		default:
			fmt.Fprintf(&b, "- [%s] ", r.Type)
		}
		content := r.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		b.WriteString(content)
		if r.Source != "" {
			b.WriteString(" (source: ")
			b.WriteString(r.Source)
			b.WriteString(")")
		}
		if r.Confidence > 0 {
			fmt.Fprintf(&b, " [conf: %d%%]", int(math.Round(r.Confidence*100)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// formatEntities renders a markdown bullet list of entities. The entity ID
// is included so the agent can pass it back to get_relationships.
func formatEntities(es []cortex.Entity) string {
	var b strings.Builder
	for _, e := range es {
		fmt.Fprintf(&b, "- **%s** (%s) — id: `%s`", e.Name, e.Type, e.ID)
		if e.Source != "" {
			fmt.Fprintf(&b, " [source: %s]", e.Source)
		}
		if e.Confidence > 0 {
			fmt.Fprintf(&b, " [conf: %d%%]", int(e.Confidence*100+0.5))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// formatRelationships renders a markdown bullet list of relationships.
// Resolves source/target IDs to entity names via cx.GetEntity for
// human-readable output, with the ID retained as a fallback when
// resolution fails.
func formatRelationships(ctx context.Context, cx *cortex.Cortex, rs []cortex.Relationship) string {
	var b strings.Builder
	for _, r := range rs {
		sourceName := r.SourceID
		if e, err := cx.GetEntity(ctx, r.SourceID); err == nil && e != nil && e.Name != "" {
			sourceName = e.Name
		}
		targetName := r.TargetID
		if e, err := cx.GetEntity(ctx, r.TargetID); err == nil && e != nil && e.Name != "" {
			targetName = e.Name
		}
		fmt.Fprintf(&b, "- %s → **%s** → %s", sourceName, r.Type, targetName)
		if r.Confidence > 0 {
			fmt.Fprintf(&b, " [conf: %d%%]", int(r.Confidence*100+0.5))
		}
		b.WriteString("\n")
	}
	return b.String()
}
