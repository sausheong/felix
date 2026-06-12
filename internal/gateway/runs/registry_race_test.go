package runs

import (
	"sync"
	"testing"
)

// TestAppendFinishRaceUnderSupersede drives the supersede interleaving: one
// goroutine hammers Append (the old run's drain) while another calls Finish
// (the new turn superseding it). Under -race this must report no data race and
// must not panic on a write to the closed log.
func TestAppendFinishRaceUnderSupersede(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)
	run, err := reg.Create(SessionScope{AgentID: "a", SessionKey: "k"}, "r1", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_, _ = run.Append(EventTypeTextDelta, []byte(`{"text":"x"}`))
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = run.Finish(StatusCancelled, ReasonSuperseded, "r2")
	}()

	wg.Wait()
}
