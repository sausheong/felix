package memory

import (
	"context"
	"fmt"

	openai "github.com/sashabaranov/go-openai"
)

// Embedder converts text into float vectors for semantic search.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// openAIEmbedder uses the OpenAI embeddings API (or any OpenAI-compatible
// endpoint such as LiteLLM) to produce text embeddings.
type openAIEmbedder struct {
	client *openai.Client
	model  openai.EmbeddingModel
}

// NewOpenAIEmbedder creates an Embedder backed by an OpenAI-compatible
// embeddings endpoint. If baseURL is empty the official OpenAI API is used.
// If model is empty or unrecognised it defaults to text-embedding-ada-002.
func NewOpenAIEmbedder(apiKey, baseURL, model string) Embedder {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	client := openai.NewClientWithConfig(cfg)

	var m openai.EmbeddingModel
	switch model {
	case "text-embedding-3-small":
		m = openai.SmallEmbedding3
	case "text-embedding-3-large":
		m = openai.LargeEmbedding3
	case "text-embedding-ada-002", "":
		m = openai.AdaEmbeddingV2
	default:
		m = openai.EmbeddingModel(model)
	}
	return &openAIEmbedder{client: client, model: m}
}

func (e *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	resp, err := e.client.CreateEmbeddings(ctx, openai.EmbeddingRequest{
		Input: texts,
		Model: e.model,
	})
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	result := make([][]float32, len(resp.Data))
	for i, d := range resp.Data {
		result[i] = d.Embedding
	}
	return result, nil
}
