package cortex

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sausheong/cortex"
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

func TestFormatRecall_UTF8Safe(t *testing.T) {
	// One ASCII byte then 3-byte runes (U+597D): byte offset 300 = 1 + 299,
	// and 299 is not a multiple of 3, so a naive content[:300] byte-slice
	// lands mid-rune and produces invalid UTF-8. True regression guard.
	long := "x" + strings.Repeat("好", 400) // 1 + 1200 bytes, 401 runes
	results := []cortex.Result{{Type: "memory", Content: long}}
	out := formatRecall(results)
	require.True(t, utf8.ValidString(out), "formatRecall must not split a rune")
}
