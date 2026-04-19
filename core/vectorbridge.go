package core

import (
	"context"
	"log/slog"

	"github.com/user/agent/sdk/embedding"
)

// EmbedFunc produces a vector embedding for a single text input.
// The signature matches chromem.EmbeddingFunc so the value can be passed
// directly without conversion.
type EmbedFunc func(ctx context.Context, text string) ([]float32, error)

// EmbedderConfig configures the ONNX-based embedder.
type EmbedderConfig struct {
	ModelPath     string
	TokenizerPath string
	LibraryPath   string
	MaxSeqLength  int
	HiddenDim     int
	Logger        *slog.Logger
}

// Embedder produces vector embeddings for text content.
type Embedder interface {
	EmbeddingFunc() EmbedFunc
	Close() error
}

// embedderWrapper adapts *embedding.Embedder to the Embedder interface.
type embedderWrapper struct {
	inner *embedding.Embedder
}

func (w *embedderWrapper) EmbeddingFunc() EmbedFunc {
	return EmbedFunc(w.inner.EmbeddingFunc())
}

func (w *embedderWrapper) Close() error {
	return w.inner.Close()
}

// NewEmbedder creates an ONNX-based embedder via sdk/embedding.
func NewEmbedder(cfg EmbedderConfig) (Embedder, error) {
	inner, err := embedding.NewEmbedder(embedding.EmbedderConfig{
		ModelPath:     cfg.ModelPath,
		TokenizerPath: cfg.TokenizerPath,
		LibraryPath:   cfg.LibraryPath,
		MaxSeqLength:  cfg.MaxSeqLength,
		HiddenDim:     cfg.HiddenDim,
		Logger:        cfg.Logger,
	})
	if err != nil {
		return nil, err
	}
	return &embedderWrapper{inner: inner}, nil
}

// ChunkInfo represents a chunk of a source file.
type ChunkInfo struct {
	Content   string
	StartLine int
	EndLine   int
	Language  string
}

// ChunkFile splits a source file into semantic chunks using sdk/embedding.
func ChunkFile(filePath string, content []byte, maxChunkSize, overlap int) ([]ChunkInfo, error) {
	chunks, err := embedding.ChunkFile(filePath, content, embedding.ChunkerConfig{
		MaxChunkSize: maxChunkSize,
		Overlap:      overlap,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ChunkInfo, len(chunks))
	for i, c := range chunks {
		out[i] = ChunkInfo{
			Content:   c.Content,
			StartLine: c.StartLine,
			EndLine:   c.EndLine,
			Language:  c.Language,
		}
	}
	return out, nil
}

// ComputeFileHash returns the SHA-256 hex digest of content.
func ComputeFileHash(content []byte) string {
	return embedding.ComputeFileHash(content)
}
