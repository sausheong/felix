package cortex

import (
	"testing"

	hrt "github.com/sausheong/harness/runtime"
	"github.com/stretchr/testify/require"
)

func TestNewKnowledgeGraph_NilCortex(t *testing.T) {
	require.Nil(t, NewKnowledgeGraph(nil), "nil cortex must yield nil KG (disables recall)")
}

func TestCortexKG_ShouldRecall(t *testing.T) {
	kg := &cortexKG{}
	require.False(t, kg.ShouldRecall(""))
	require.False(t, kg.ShouldRecall("hi"))
	require.True(t, kg.ShouldRecall("what did we decide about the database schema"))
}

var _ hrt.KnowledgeGraph = (*cortexKG)(nil)
