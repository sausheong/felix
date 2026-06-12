package startup

import (
	"sync"
	"testing"

	"github.com/sausheong/felix/internal/llm"
	"github.com/stretchr/testify/require"
)

func TestProviderHolderStoreLoad(t *testing.T) {
	h := newProviderHolder(map[string]llm.LLMProvider{})
	_, ok := h.get("openai")
	require.False(t, ok)

	h.store(map[string]llm.LLMProvider{"openai": nil})
	_, ok = h.get("openai")
	require.True(t, ok, "reader must see the post-store map")
}

func TestProviderHolderConcurrent(t *testing.T) {
	h := newProviderHolder(map[string]llm.LLMProvider{"a": nil})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_, _ = h.get("a")
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				h.store(map[string]llm.LLMProvider{"a": nil})
			}
		}()
	}
	wg.Wait()
}
