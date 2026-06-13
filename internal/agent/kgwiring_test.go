package agent

import (
	"testing"

	"github.com/sausheong/cortex"
	hrt "github.com/sausheong/harness/runtime"
	"github.com/stretchr/testify/require"

	cortexadapter "github.com/sausheong/felix/internal/cortex"
)

// TestKGFn_NilWhenCortexDisabled documents the nil-when-disabled guarantee:
// when Felix has no CortexFn configured, the KGFn closure handed to the
// harness runtime must return a nil KnowledgeGraph so the runtime skips the
// entire auto-recall/ingest pathway. This mirrors the closure wired at the
// main hdeps in BuildRuntimeForAgent.
func TestKGFn_NilWhenCortexDisabled(t *testing.T) {
	var cortexFn func(string) *cortex.Cortex // nil — cortex disabled
	kgfn := func(model string) hrt.KnowledgeGraph {
		if cortexFn == nil {
			return nil
		}
		return cortexadapter.NewKnowledgeGraph(cortexFn(model))
	}
	require.Nil(t, kgfn("any"), "KGFn must yield nil KG when CortexFn is unset")
}
