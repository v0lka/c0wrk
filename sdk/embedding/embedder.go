package embedding

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	chromem "github.com/philippgille/chromem-go"
)

const (
	// DefaultMaxSeqLength is the default maximum sequence length for tokenization.
	// jina-v2-small supports up to 8192, but 512 is practical for most use cases.
	DefaultMaxSeqLength = 512

	// DefaultHiddenDim is the embedding dimension for jina-embeddings-v2-small-en.
	DefaultHiddenDim = 512
)

// EmbedderConfig holds configuration for creating an Embedder.
type EmbedderConfig struct {
	// ModelPath is the path to the ONNX model file (.onnx).
	ModelPath string

	// TokenizerPath is the path to the HuggingFace tokenizer.json file.
	TokenizerPath string

	// LibraryPath is the path to the ONNX Runtime shared library
	// (e.g., libonnxruntime.dylib, libonnxruntime.so, onnxruntime.dll).
	LibraryPath string

	// MaxSeqLength is the maximum token sequence length. Defaults to 512.
	MaxSeqLength int

	// HiddenDim is the embedding dimension of the model. Defaults to 512 for jina-v2-small.
	HiddenDim int

	// Logger for structured logging. If nil, a no-op logger is used.
	Logger *slog.Logger
}

// Embedder provides ONNX-based text embedding using jina-embeddings-v2-small-en.
// It is safe for concurrent use.
type Embedder struct {
	tokenizer *Tokenizer
	modelPath string
	maxSeqLen int
	hiddenDim int
	logger    *slog.Logger
	mu        sync.Mutex
	sess      *onnxSession // persistent session for batchSize=1 (fast path)
}

// NewEmbedder creates a new Embedder by loading the tokenizer and initializing
// the ONNX Runtime environment.
//
// DESIGN NOTE: The ONNX Runtime is a process-global singleton — only one Embedder
// can exist at a time and it lives for the process lifetime. This is now ENFORCED
// by sync.Once in initONNXRuntime: the first successful initialization is final
// and cannot be repeated in the same process, even after Close/destroy. There is
// no reference counting; desktop.App is the single owner responsible for calling
// Close() at shutdown. This is a known limitation for library-reuse scenarios but
// sufficient for the single-process desktop app architecture.
func NewEmbedder(cfg EmbedderConfig) (*Embedder, error) {
	if cfg.ModelPath == "" {
		return nil, errors.New("ModelPath is required")
	}
	if cfg.TokenizerPath == "" {
		return nil, errors.New("TokenizerPath is required")
	}
	if cfg.LibraryPath == "" {
		return nil, errors.New("LibraryPath is required")
	}

	maxSeqLen := cfg.MaxSeqLength
	if maxSeqLen <= 0 {
		maxSeqLen = DefaultMaxSeqLength
	}

	hiddenDim := cfg.HiddenDim
	if hiddenDim <= 0 {
		hiddenDim = DefaultHiddenDim
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	logger.Info("initializing ONNX Runtime", "library", cfg.LibraryPath)
	if err := initONNXRuntime(cfg.LibraryPath); err != nil {
		return nil, fmt.Errorf("initializing ONNX Runtime: %w", err)
	}

	logger.Info("loading tokenizer", "path", cfg.TokenizerPath)
	tok, err := NewTokenizer(cfg.TokenizerPath)
	if err != nil {
		// Clean up ONNX env on failure.
		_ = destroyONNXRuntime()
		return nil, fmt.Errorf("loading tokenizer: %w", err)
	}

	logger.Info("creating persistent ONNX session", "model", cfg.ModelPath)
	sess, err := newONNXSession(cfg.ModelPath, maxSeqLen, hiddenDim)
	if err != nil {
		_ = destroyONNXRuntime()
		return nil, fmt.Errorf("creating persistent ONNX session: %w", err)
	}

	logger.Info("embedder initialized",
		"model", cfg.ModelPath,
		"maxSeqLen", maxSeqLen,
		"hiddenDim", hiddenDim,
	)

	return &Embedder{
		tokenizer: tok,
		modelPath: cfg.ModelPath,
		maxSeqLen: maxSeqLen,
		hiddenDim: hiddenDim,
		logger:    logger,
		sess:      sess,
	}, nil
}

// EmbedDocuments embeds a batch of text documents and returns their embedding vectors.
func (e *Embedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Re-check context after acquiring the lock, since ONNX inference blocks.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Guard against use-after-close.
	if e.tokenizer == nil {
		return nil, errors.New("embedder is closed")
	}

	batchSize := len(texts)
	inputIDs, attentionMask, tokenTypeIDs := e.tokenizer.EncodeBatch(texts, e.maxSeqLen)

	// Fast path: use the persistent session for single-text embedding.
	// This is the common case when chromem-go calls EmbeddingFunc one text at a time.
	if batchSize == 1 && e.sess != nil {
		e.logger.Debug("running inference (persistent session)", "seqLen", e.maxSeqLen)
		vec, err := e.sess.run(inputIDs, attentionMask, tokenTypeIDs)
		if err != nil {
			return nil, fmt.Errorf("embedding document: %w", err)
		}
		return [][]float32{vec}, nil
	}

	// Batch path: create a temporary session for larger batches.
	e.logger.Debug("running inference (batch session)", "batchSize", batchSize, "seqLen", e.maxSeqLen)
	embeddings, err := runInferenceBatch(e.modelPath, batchSize, e.maxSeqLen, e.hiddenDim, inputIDs, attentionMask, tokenTypeIDs)
	if err != nil {
		return nil, fmt.Errorf("embedding batch of %d documents: %w", batchSize, err)
	}

	return embeddings, nil
}

// EmbedQuery embeds a single text query and returns its embedding vector.
func (e *Embedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	results, err := e.EmbedDocuments(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, errors.New("no embedding returned")
	}
	return results[0], nil
}

// EmbeddingFunc returns a chromem-go compatible embedding function that can be
// passed to chromem.NewCollection as the embedding function parameter.
func (e *Embedder) EmbeddingFunc() chromem.EmbeddingFunc {
	return func(ctx context.Context, text string) ([]float32, error) {
		return e.EmbedQuery(ctx, text)
	}
}

// Close releases the ONNX Runtime environment and associated resources.
func (e *Embedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.logger.Info("closing embedder, destroying ONNX Runtime environment")
	if e.sess != nil {
		e.sess.destroy()
		e.sess = nil
	}
	// Mark the embedder closed so EmbedDocuments/EmbedQuery return an error
	// instead of touching the destroyed ONNX environment.
	e.tokenizer = nil
	if err := destroyONNXRuntime(); err != nil {
		return fmt.Errorf("destroying ONNX Runtime environment: %w", err)
	}
	return nil
}
