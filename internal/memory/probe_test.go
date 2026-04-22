package memory

import (
	"context"
	"errors"
	"testing"
)

type fakeEmbedder struct {
	err error
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{0.1, 0.2}
	}
	return out, nil
}

func TestAttachWithProbeAttachesOnSuccess(t *testing.T) {
	mgr := NewManager(t.TempDir())
	emb := &fakeEmbedder{}
	AttachWithProbe(mgr, emb, "test-model")
	if mgr.embedder == nil {
		t.Errorf("embedder should be attached on probe success")
	}
}

func TestAttachWithProbeSkipsOnFailure(t *testing.T) {
	mgr := NewManager(t.TempDir())
	emb := &fakeEmbedder{err: errors.New("connection refused")}
	AttachWithProbe(mgr, emb, "test-model")
	if mgr.embedder != nil {
		t.Errorf("embedder must NOT be attached on probe failure")
	}
}
