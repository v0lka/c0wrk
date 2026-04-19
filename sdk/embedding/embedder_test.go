package embedding

import (
	"context"
	"math"
	"os"
	"testing"
)

// testTokenizerPath returns the path to a test tokenizer.json if available.
// Tests that require a real tokenizer file are skipped if it's not present.
func testTokenizerPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("EMBEDDING_TEST_TOKENIZER_PATH")
	if path == "" {
		t.Skip("EMBEDDING_TEST_TOKENIZER_PATH not set; skipping tokenizer-dependent test")
	}
	return path
}

// testModelPath returns the path to a test ONNX model if available.
func testModelPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("EMBEDDING_TEST_MODEL_PATH")
	if path == "" {
		t.Skip("EMBEDDING_TEST_MODEL_PATH not set; skipping model-dependent test")
	}
	return path
}

// testLibraryPath returns the path to the ONNX Runtime shared library if available.
func testLibraryPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("EMBEDDING_TEST_LIBRARY_PATH")
	if path == "" {
		t.Skip("EMBEDDING_TEST_LIBRARY_PATH not set; skipping ONNX-dependent test")
	}
	return path
}

func TestNewTokenizer(t *testing.T) {
	tokPath := testTokenizerPath(t)

	tok, err := NewTokenizer(tokPath)
	if err != nil {
		t.Fatalf("NewTokenizer() error = %v", err)
	}
	if tok == nil {
		t.Fatal("NewTokenizer() returned nil")
	}
}

func TestTokenizer_Encode(t *testing.T) {
	tokPath := testTokenizerPath(t)

	tok, err := NewTokenizer(tokPath)
	if err != nil {
		t.Fatalf("NewTokenizer() error = %v", err)
	}

	ids, mask, typeIDs := tok.Encode("hello world", 16)

	// Should have maxLen elements.
	if len(ids) != 16 {
		t.Errorf("inputIDs length = %d, want 16", len(ids))
	}
	if len(mask) != 16 {
		t.Errorf("attentionMask length = %d, want 16", len(mask))
	}
	if len(typeIDs) != 16 {
		t.Errorf("tokenTypeIDs length = %d, want 16", len(typeIDs))
	}

	// First token should be [CLS] (101) for BERT-style tokenizers.
	if ids[0] != 101 {
		t.Errorf("first token = %d, want 101 ([CLS])", ids[0])
	}

	// Attention mask should be 1 for real tokens, 0 for padding.
	if mask[0] != 1 {
		t.Errorf("first attention mask = %d, want 1", mask[0])
	}

	// Last elements should be padding (0).
	if ids[15] != 0 {
		t.Errorf("last token = %d, want 0 (padding)", ids[15])
	}
	if mask[15] != 0 {
		t.Errorf("last attention mask = %d, want 0 (padding)", mask[15])
	}
}

func TestTokenizer_EncodeBatch(t *testing.T) {
	tokPath := testTokenizerPath(t)

	tok, err := NewTokenizer(tokPath)
	if err != nil {
		t.Fatalf("NewTokenizer() error = %v", err)
	}

	texts := []string{"hello", "world"}
	ids, mask, typeIDs := tok.EncodeBatch(texts, 8)

	// Should be flattened: 2 * 8 = 16 elements.
	if len(ids) != 16 {
		t.Errorf("batched inputIDs length = %d, want 16", len(ids))
	}
	if len(mask) != 16 {
		t.Errorf("batched attentionMask length = %d, want 16", len(mask))
	}
	if len(typeIDs) != 16 {
		t.Errorf("batched tokenTypeIDs length = %d, want 16", len(typeIDs))
	}
}

func TestMeanPoolAndNormalize(t *testing.T) {
	// Test with a simple 1-sample, 3-token, 2-dim example.
	batchSize := 1
	seqLen := 3
	hiddenDim := 2

	// Hidden states: [[1,2], [3,4], [5,6]]
	hiddenStates := []float32{1, 2, 3, 4, 5, 6}
	// Attention mask: [1, 1, 0] (only first 2 tokens are real)
	attentionMask := []int64{1, 1, 0}

	result := meanPoolAndNormalize(hiddenStates, attentionMask, batchSize, seqLen, hiddenDim)

	if len(result) != 1 {
		t.Fatalf("result length = %d, want 1", len(result))
	}
	if len(result[0]) != 2 {
		t.Fatalf("embedding dim = %d, want 2", len(result[0]))
	}

	// Mean of [1,2] and [3,4] = [2, 3].
	// L2 norm = sqrt(4+9) = sqrt(13).
	// Normalized: [2/sqrt(13), 3/sqrt(13)].
	expectedNorm := math.Sqrt(13)
	expected0 := float32(2.0 / expectedNorm)
	expected1 := float32(3.0 / expectedNorm)

	const tolerance = 1e-6
	if diff := math.Abs(float64(result[0][0] - expected0)); diff > tolerance {
		t.Errorf("result[0][0] = %f, want %f (diff=%e)", result[0][0], expected0, diff)
	}
	if diff := math.Abs(float64(result[0][1] - expected1)); diff > tolerance {
		t.Errorf("result[0][1] = %f, want %f (diff=%e)", result[0][1], expected1, diff)
	}

	// Verify unit norm.
	norm := math.Sqrt(float64(result[0][0])*float64(result[0][0]) + float64(result[0][1])*float64(result[0][1]))
	if diff := math.Abs(norm - 1.0); diff > tolerance {
		t.Errorf("embedding norm = %f, want 1.0", norm)
	}
}

func TestNewEmbedder_ValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  EmbedderConfig
	}{
		{"missing model path", EmbedderConfig{TokenizerPath: "t.json", LibraryPath: "lib.so"}},
		{"missing tokenizer path", EmbedderConfig{ModelPath: "m.onnx", LibraryPath: "lib.so"}},
		{"missing library path", EmbedderConfig{ModelPath: "m.onnx", TokenizerPath: "t.json"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewEmbedder(tt.cfg)
			if err == nil {
				t.Error("NewEmbedder() expected error, got nil")
			}
		})
	}
}

func TestEmbedder_EmbedDocuments_EmptyInput(t *testing.T) {
	// EmbedDocuments with empty input should return nil without error,
	// even without a real embedder (we test the early-return path).
	// We can't fully construct an Embedder without real files, so test the logic directly.
	e := &Embedder{
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
	}

	result, err := e.EmbedDocuments(context.Background(), nil)
	if err != nil {
		t.Errorf("EmbedDocuments(nil) error = %v", err)
	}
	if result != nil {
		t.Errorf("EmbedDocuments(nil) = %v, want nil", result)
	}

	result, err = e.EmbedDocuments(context.Background(), []string{})
	if err != nil {
		t.Errorf("EmbedDocuments([]) error = %v", err)
	}
	if result != nil {
		t.Errorf("EmbedDocuments([]) = %v, want nil", result)
	}
}

func TestEmbedder_EmbedDocuments_CancelledContext(t *testing.T) {
	e := &Embedder{
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := e.EmbedDocuments(ctx, []string{"hello"})
	if err == nil {
		t.Error("EmbedDocuments with cancelled context expected error")
	}
}

func TestEmbedder_EmbeddingFunc(t *testing.T) {
	libPath := testLibraryPath(t)
	tokPath := testTokenizerPath(t)
	modelPath := testModelPath(t)

	emb, err := NewEmbedder(EmbedderConfig{
		ModelPath:     modelPath,
		TokenizerPath: tokPath,
		LibraryPath:   libPath,
	})
	if err != nil {
		t.Fatalf("NewEmbedder() error = %v", err)
	}
	defer func() { _ = emb.Close() }()

	fn := emb.EmbeddingFunc()
	if fn == nil {
		t.Fatal("EmbeddingFunc() returned nil")
	}

	vec, err := fn(context.Background(), "test embedding")
	if err != nil {
		t.Fatalf("EmbeddingFunc()() error = %v", err)
	}
	if len(vec) != DefaultHiddenDim {
		t.Errorf("embedding dim = %d, want %d", len(vec), DefaultHiddenDim)
	}
}

func TestEmbedder_EndToEnd(t *testing.T) {
	libPath := testLibraryPath(t)
	tokPath := testTokenizerPath(t)
	modelPath := testModelPath(t)

	emb, err := NewEmbedder(EmbedderConfig{
		ModelPath:     modelPath,
		TokenizerPath: tokPath,
		LibraryPath:   libPath,
		MaxSeqLength:  128,
	})
	if err != nil {
		t.Fatalf("NewEmbedder() error = %v", err)
	}
	defer func() { _ = emb.Close() }()

	ctx := context.Background()

	// Single query embedding.
	vec, err := emb.EmbedQuery(ctx, "The quick brown fox jumps over the lazy dog")
	if err != nil {
		t.Fatalf("EmbedQuery() error = %v", err)
	}
	if len(vec) != DefaultHiddenDim {
		t.Errorf("EmbedQuery() dim = %d, want %d", len(vec), DefaultHiddenDim)
	}

	// Verify unit norm.
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if diff := math.Abs(norm - 1.0); diff > 1e-5 {
		t.Errorf("embedding norm = %f, want 1.0", norm)
	}

	// Batch embedding.
	vecs, err := emb.EmbedDocuments(ctx, []string{"hello", "world"})
	if err != nil {
		t.Fatalf("EmbedDocuments() error = %v", err)
	}
	if len(vecs) != 2 {
		t.Errorf("EmbedDocuments() count = %d, want 2", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != DefaultHiddenDim {
			t.Errorf("vecs[%d] dim = %d, want %d", i, len(v), DefaultHiddenDim)
		}
	}
}
