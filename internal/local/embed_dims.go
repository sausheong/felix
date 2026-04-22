package local

// embedDims maps known embedding model names to their vector dimensions.
// Used by cortex.Init to wire WithEmbeddingModel correctly.
var embedDims = map[string]int{
	"nomic-embed-text":       768,
	"mxbai-embed-large":      1024,
	"all-minilm":             384,
	"text-embedding-3-small": 1536,
	"text-embedding-3-large": 3072,
	"text-embedding-ada-002": 1536,
}

// EmbeddingDims returns (model, dimensions). Unknown models fall back to 1536
// (OpenAI's standard) so cortex doesn't refuse to start on an unrecognised name.
func EmbeddingDims(model string) (string, int) {
	if d, ok := embedDims[model]; ok {
		return model, d
	}
	return model, 1536
}
