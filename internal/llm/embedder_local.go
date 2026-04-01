package llm

import (
	"context"

	"github.com/clems4ever/all-minilm-l6-v2-go/all_minilm_l6_v2"
)

// LocalEmbedder implements Embedder using a local all-MiniLM-L6-v2 model.
// It provides 384-dimensional embeddings without requiring external API calls.
type LocalEmbedder struct {
	model *all_minilm_l6_v2.Model
}

// NewLocalEmbedder creates a new local embedder using the all-MiniLM-L6-v2 model.
// It requires the ONNXRUNTIME_LIB_PATH environment variable to be set to the ONNX Runtime library path.
func NewLocalEmbedder() (*LocalEmbedder, error) {
	model, err := all_minilm_l6_v2.NewModel()
	if err != nil {
		return nil, err
	}
	return &LocalEmbedder{model: model}, nil
}

// Embed generates an embedding vector for the given text.
// It converts the float32 result from the model to float64 for interface compatibility.
func (e *LocalEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	embedding, err := e.model.Compute(text, false)
	if err != nil {
		return nil, err
	}

	// Convert float32 to float64
	result := make([]float64, len(embedding))
	for i, v := range embedding {
		result[i] = float64(v)
	}

	return result, nil
}

// Dimensions returns the number of dimensions in the embedding vector (384 for all-MiniLM-L6-v2).
func (e *LocalEmbedder) Dimensions() int {
	return 384
}

// Close releases the model resources.
func (e *LocalEmbedder) Close() error {
	if e.model != nil {
		return e.model.Close()
	}
	return nil
}
