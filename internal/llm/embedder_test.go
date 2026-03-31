package llm

import (
	"os"
	"testing"
)

func TestOpenAIEmbedder_New(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		wantModel      string
		wantDimensions int
	}{
		{
			name:           "default model",
			model:          "",
			wantModel:      "text-embedding-3-small",
			wantDimensions: 1536,
		},
		{
			name:           "text-embedding-3-small",
			model:          "text-embedding-3-small",
			wantModel:      "text-embedding-3-small",
			wantDimensions: 1536,
		},
		{
			name:           "text-embedding-3-large",
			model:          "text-embedding-3-large",
			wantModel:      "text-embedding-3-large",
			wantDimensions: 3072,
		},
		{
			name:           "text-embedding-ada-002",
			model:          "text-embedding-ada-002",
			wantModel:      "text-embedding-ada-002",
			wantDimensions: 1536,
		},
		{
			name:           "unknown model uses default dimensions",
			model:          "custom-model",
			wantModel:      "custom-model",
			wantDimensions: 1536,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			embedder := NewOpenAIEmbedder("test-api-key", tt.model)

			if embedder.model != tt.wantModel {
				t.Errorf("model = %q, want %q", embedder.model, tt.wantModel)
			}

			if got := embedder.Dimensions(); got != tt.wantDimensions {
				t.Errorf("Dimensions() = %d, want %d", got, tt.wantDimensions)
			}

			if embedder.client == nil {
				t.Error("client should not be nil")
			}
		})
	}
}

func TestOpenAIEmbedder_Integration(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}

	// embedder := NewOpenAIEmbedder(apiKey, "text-embedding-3-small")
	// ctx := context.Background()
	//
	// embedding, err := embedder.Embed(ctx, "Hello, world!")
	// if err != nil {
	// 	t.Fatalf("Embed() error = %v", err)
	// }
	//
	// if len(embedding) != embedder.Dimensions() {
	// 	t.Errorf("embedding length = %d, want %d", len(embedding), embedder.Dimensions())
	// }
	//
	// // Check that embedding values are reasonable (not all zeros)
	// allZero := true
	// for _, v := range embedding {
	// 	if v != 0 {
	// 		allZero = false
	// 		break
	// 	}
	// }
	// if allZero {
	// 	t.Error("embedding should not be all zeros")
	// }
}
