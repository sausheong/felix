package local

import "testing"

func TestEmbeddingDimsKnownModels(t *testing.T) {
	cases := map[string]int{
		"nomic-embed-text":       768,
		"mxbai-embed-large":      1024,
		"all-minilm":             384,
		"text-embedding-3-small": 1536,
		"text-embedding-3-large": 3072,
		"text-embedding-ada-002": 1536,
	}
	for model, want := range cases {
		gotModel, gotDims := EmbeddingDims(model)
		if gotModel != model {
			t.Errorf("EmbeddingDims(%q): model = %q, want %q", model, gotModel, model)
		}
		if gotDims != want {
			t.Errorf("EmbeddingDims(%q): dims = %d, want %d", model, gotDims, want)
		}
	}
}

func TestEmbeddingDimsUnknownFallsBackTo1536(t *testing.T) {
	gotModel, gotDims := EmbeddingDims("some-unknown-model")
	if gotModel != "some-unknown-model" {
		t.Errorf("model passthrough failed: got %q", gotModel)
	}
	if gotDims != 1536 {
		t.Errorf("unknown dims = %d, want 1536", gotDims)
	}
}
