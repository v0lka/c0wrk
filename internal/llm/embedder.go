package llm

import (
	"context"

	"github.com/sashabaranov/go-openai"
)

// Embedder generates embedding vectors for text.
type Embedder interface {
	// Embed generates an embedding vector for the given text.
	Embed(ctx context.Context, text string) ([]float64, error)
	// Dimensions returns the number of dimensions in the embedding vector.
	Dimensions() int
}

// OpenAIEmbedder implements Embedder using OpenAI's embedding API.
type OpenAIEmbedder struct {
	client     *openai.Client
	model      string
	dimensions int
}

// NewOpenAIEmbedder creates a new OpenAI embedder.
// If model is empty, defaults to "text-embedding-3-small".
func NewOpenAIEmbedder(apiKey string, model string) *OpenAIEmbedder {
	if model == "" {
		model = "text-embedding-3-small"
	}

	// Determine dimensions based on model
	dimensions := 1536 // default for text-embedding-3-small
	switch model {
	case "text-embedding-3-small":
		dimensions = 1536
	case "text-embedding-3-large":
		dimensions = 3072
	case "text-embedding-ada-002":
		dimensions = 1536
	}

	return &OpenAIEmbedder{
		client:     openai.NewClient(apiKey),
		model:      model,
		dimensions: dimensions,
	}
}

// Embed generates an embedding vector for the given text.
func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	req := openai.EmbeddingRequest{
		Input: []string{text},
		Model: openai.EmbeddingModel(e.model),
	}

	resp, err := e.client.CreateEmbeddings(ctx, req)
	if err != nil {
		return nil, err
	}

	if len(resp.Data) == 0 {
		return nil, nil
	}

	// Convert float32 to float64
	embedding := resp.Data[0].Embedding
	result := make([]float64, len(embedding))
	for i, v := range embedding {
		result[i] = float64(v)
	}

	return result, nil
}

// Dimensions returns the number of dimensions in the embedding vector.
func (e *OpenAIEmbedder) Dimensions() int {
	return e.dimensions
}
