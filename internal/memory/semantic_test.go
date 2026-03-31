package memory

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// mockEmbedder is a deterministic embedder for testing.
type mockEmbedder struct {
	embeddings map[string][]float64
	dimensions int
}

func newMockEmbedder() *mockEmbedder {
	return &mockEmbedder{
		embeddings: make(map[string][]float64),
		dimensions: 4, // small dimension for testing
	}
}

func (m *mockEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	if emb, ok := m.embeddings[text]; ok {
		return emb, nil
	}
	// Generate a deterministic embedding based on text length and characters
	emb := make([]float64, m.dimensions)
	for i, c := range text {
		emb[i%m.dimensions] += float64(c) / 1000.0
	}
	// Normalize
	var mag float64
	for _, v := range emb {
		mag += v * v
	}
	mag = math.Sqrt(mag)
	if mag > 0 {
		for i := range emb {
			emb[i] /= mag
		}
	}
	return emb, nil
}

func (m *mockEmbedder) Dimensions() int {
	return m.dimensions
}

func (m *mockEmbedder) setEmbedding(text string, embedding []float64) {
	m.embeddings[text] = embedding
}

func TestSemanticMemory_StoreAndSearch(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()
	// Set up specific embeddings for predictable results
	embedder.setEmbedding("golang programming", []float64{1, 0, 0, 0})
	embedder.setEmbedding("go language", []float64{0.9, 0.1, 0, 0})         // similar to golang
	embedder.setEmbedding("python programming", []float64{0, 1, 0, 0})      // different
	embedder.setEmbedding("search for golang", []float64{0.95, 0.05, 0, 0}) // query

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	defer func() { _ = sm.Close() }()

	ctx := context.Background()

	// Store entries
	if err := sm.Store(ctx, "doc1", "golang programming", map[string]string{"type": "tutorial"}); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if err := sm.Store(ctx, "doc2", "go language", map[string]string{"type": "overview"}); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if err := sm.Store(ctx, "doc3", "python programming", map[string]string{"type": "tutorial"}); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	// Search
	results, err := sm.Search(ctx, "search for golang", 2)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Search() returned %d results, want 2", len(results))
	}

	// First result should be "golang programming" (most similar)
	if results[0].Key != "doc1" {
		t.Errorf("First result key = %q, want %q", results[0].Key, "doc1")
	}
	if results[0].Content != "golang programming" {
		t.Errorf("First result content = %q, want %q", results[0].Content, "golang programming")
	}
	if results[0].Metadata["type"] != "tutorial" {
		t.Errorf("First result metadata[type] = %q, want %q", results[0].Metadata["type"], "tutorial")
	}
	if results[0].Score <= 0 {
		t.Errorf("First result score = %f, want > 0", results[0].Score)
	}
}

func TestSemanticMemory_CosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a    []float64
		b    []float64
		want float64
	}{
		{
			name: "identical vectors",
			a:    []float64{1, 0, 0},
			b:    []float64{1, 0, 0},
			want: 1.0,
		},
		{
			name: "opposite vectors",
			a:    []float64{1, 0, 0},
			b:    []float64{-1, 0, 0},
			want: -1.0,
		},
		{
			name: "orthogonal vectors",
			a:    []float64{1, 0, 0},
			b:    []float64{0, 1, 0},
			want: 0.0,
		},
		{
			name: "45 degree angle",
			a:    []float64{1, 0},
			b:    []float64{1, 1},
			want: 1 / math.Sqrt(2), // ~0.707
		},
		{
			name: "empty vectors",
			a:    []float64{},
			b:    []float64{},
			want: 0.0,
		},
		{
			name: "different lengths",
			a:    []float64{1, 0},
			b:    []float64{1, 0, 0},
			want: 0.0,
		},
		{
			name: "zero vector",
			a:    []float64{0, 0, 0},
			b:    []float64{1, 0, 0},
			want: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cosineSimilarity(tt.a, tt.b)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("cosineSimilarity() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSemanticMemory_TopK(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()
	// Create embeddings with decreasing similarity to query
	embedder.setEmbedding("query", []float64{1, 0, 0, 0})
	for i := 0; i < 10; i++ {
		similarity := 1.0 - float64(i)*0.1
		embedding := []float64{similarity, math.Sqrt(1 - similarity*similarity), 0, 0}
		embedder.setEmbedding("doc"+string(rune('0'+i)), embedding)
	}

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	defer func() { _ = sm.Close() }()

	ctx := context.Background()

	// Store 10 entries
	for i := 0; i < 10; i++ {
		key := "key" + string(rune('0'+i))
		content := "doc" + string(rune('0'+i))
		if err := sm.Store(ctx, key, content, nil); err != nil {
			t.Fatalf("Store() error = %v", err)
		}
	}

	// Request top 3
	results, err := sm.Search(ctx, "query", 3)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if len(results) != 3 {
		t.Errorf("Search() returned %d results, want 3", len(results))
	}

	// Verify results are sorted by score descending
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("Results not sorted: score[%d]=%f > score[%d]=%f",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
}

func TestSemanticMemory_SearchRelevance(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()
	// Related topics have similar embeddings, unrelated have orthogonal
	embedder.setEmbedding("machine learning basics", []float64{1, 0, 0, 0})
	embedder.setEmbedding("deep learning tutorial", []float64{0.9, 0.1, 0, 0})
	embedder.setEmbedding("neural networks guide", []float64{0.85, 0.15, 0, 0})
	embedder.setEmbedding("cooking recipes", []float64{0, 1, 0, 0})
	embedder.setEmbedding("gardening tips", []float64{0, 0, 1, 0})
	embedder.setEmbedding("AI and ML overview", []float64{0.95, 0.05, 0, 0}) // query

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	defer func() { _ = sm.Close() }()

	ctx := context.Background()

	// Store entries
	entries := []struct {
		key     string
		content string
	}{
		{"ml", "machine learning basics"},
		{"dl", "deep learning tutorial"},
		{"nn", "neural networks guide"},
		{"cook", "cooking recipes"},
		{"garden", "gardening tips"},
	}

	for _, e := range entries {
		if err := sm.Store(ctx, e.key, e.content, nil); err != nil {
			t.Fatalf("Store() error = %v", err)
		}
	}

	// Search for AI/ML related content
	results, err := sm.Search(ctx, "AI and ML overview", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	// Verify related topics rank higher than unrelated
	if len(results) < 5 {
		t.Fatalf("Expected 5 results, got %d", len(results))
	}

	// Top 3 should be ML-related (ml, dl, nn)
	mlRelatedKeys := map[string]bool{"ml": true, "dl": true, "nn": true}
	for i := 0; i < 3; i++ {
		if !mlRelatedKeys[results[i].Key] {
			t.Errorf("Top %d result should be ML-related, got key=%q", i+1, results[i].Key)
		}
	}

	// Bottom 2 should be unrelated (cook, garden)
	unrelatedKeys := map[string]bool{"cook": true, "garden": true}
	for i := 3; i < 5; i++ {
		if !unrelatedKeys[results[i].Key] {
			t.Errorf("Bottom result %d should be unrelated, got key=%q", i+1, results[i].Key)
		}
	}
}

func TestSemanticMemory_Metadata(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()
	embedder.setEmbedding("test content", []float64{1, 0, 0, 0})
	embedder.setEmbedding("search query", []float64{0.99, 0.01, 0, 0})

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	defer func() { _ = sm.Close() }()

	ctx := context.Background()

	// Store with metadata
	metadata := map[string]string{
		"author":  "test-user",
		"version": "1.0",
		"tags":    "important,reviewed",
	}

	if err := sm.Store(ctx, "doc1", "test content", metadata); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	// Search and verify metadata
	results, err := sm.Search(ctx, "search query", 1)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	for k, v := range metadata {
		if results[0].Metadata[k] != v {
			t.Errorf("Metadata[%q] = %q, want %q", k, results[0].Metadata[k], v)
		}
	}
}

func TestSemanticMemory_EmptyDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()
	embedder.setEmbedding("query", []float64{1, 0, 0, 0})

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	defer func() { _ = sm.Close() }()

	ctx := context.Background()

	// Search empty database
	results, err := sm.Search(ctx, "query", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if len(results) != 0 {
		t.Errorf("Search on empty DB returned %d results, want 0", len(results))
	}
}

func TestSemanticMemory_UpdateEntry(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()
	embedder.setEmbedding("original content", []float64{1, 0, 0, 0})
	embedder.setEmbedding("updated content", []float64{0, 1, 0, 0})
	embedder.setEmbedding("search updated", []float64{0, 0.99, 0, 0})

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	defer func() { _ = sm.Close() }()

	ctx := context.Background()

	// Store original
	if err := sm.Store(ctx, "doc1", "original content", map[string]string{"v": "1"}); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	// Update with same key
	if err := sm.Store(ctx, "doc1", "updated content", map[string]string{"v": "2"}); err != nil {
		t.Fatalf("Store() update error = %v", err)
	}

	// Search should find updated content
	results, err := sm.Search(ctx, "search updated", 1)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Content != "updated content" {
		t.Errorf("Content = %q, want %q", results[0].Content, "updated content")
	}

	if results[0].Metadata["v"] != "2" {
		t.Errorf("Metadata[v] = %q, want %q", results[0].Metadata["v"], "2")
	}
}

func TestSemanticMemory_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()
	embedder.setEmbedding("content to delete", []float64{1, 0, 0, 0})
	embedder.setEmbedding("content to keep", []float64{0, 1, 0, 0})
	embedder.setEmbedding("search query", []float64{0.5, 0.5, 0, 0})

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	defer func() { _ = sm.Close() }()

	ctx := context.Background()

	// Store entries
	if err := sm.Store(ctx, "doc1", "content to delete", nil); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if err := sm.Store(ctx, "doc2", "content to keep", nil); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	// Delete one entry
	if err := sm.Delete(ctx, "doc1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Search should only find remaining entry
	results, err := sm.Search(ctx, "search query", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if len(results) != 1 {
		t.Errorf("Expected 1 result after delete, got %d", len(results))
	}

	if results[0].Key != "doc2" {
		t.Errorf("Remaining result key = %q, want %q", results[0].Key, "doc2")
	}
}

func TestSemanticMemory_NilMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()
	embedder.setEmbedding("content", []float64{1, 0, 0, 0})
	embedder.setEmbedding("query", []float64{0.99, 0, 0, 0})

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	defer func() { _ = sm.Close() }()

	ctx := context.Background()

	// Store with nil metadata
	if err := sm.Store(ctx, "doc1", "content", nil); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	// Search and verify nil metadata doesn't cause issues
	results, err := sm.Search(ctx, "query", 1)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// Metadata should be nil or empty map
	if len(results[0].Metadata) > 0 {
		t.Errorf("Expected nil or empty metadata, got %v", results[0].Metadata)
	}
}

func TestSemanticMemory_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()
	embedder.setEmbedding("persistent content", []float64{1, 0, 0, 0})
	embedder.setEmbedding("search query", []float64{0.99, 0, 0, 0})

	// Create and store
	sm1, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}

	ctx := context.Background()
	if err := sm1.Store(ctx, "doc1", "persistent content", map[string]string{"key": "value"}); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	_ = sm1.Close()

	// Reopen and search
	sm2, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() reopen error = %v", err)
	}
	defer func() { _ = sm2.Close() }()

	results, err := sm2.Search(ctx, "search query", 1)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result after reopen, got %d", len(results))
	}

	if results[0].Content != "persistent content" {
		t.Errorf("Content = %q, want %q", results[0].Content, "persistent content")
	}

	if results[0].Metadata["key"] != "value" {
		t.Errorf("Metadata[key] = %q, want %q", results[0].Metadata["key"], "value")
	}
}

func TestSemanticMemory_ZeroTopK(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	defer func() { _ = sm.Close() }()

	ctx := context.Background()

	results, err := sm.Search(ctx, "query", 0)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if results != nil {
		t.Errorf("Search(topK=0) should return nil, got %v", results)
	}
}

func TestSemanticMemory_NegativeTopK(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	defer func() { _ = sm.Close() }()

	ctx := context.Background()

	results, err := sm.Search(ctx, "query", -1)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if results != nil {
		t.Errorf("Search(topK=-1) should return nil, got %v", results)
	}
}

func TestNewSemanticMemory_InvalidPath(t *testing.T) {
	embedder := newMockEmbedder()

	// Try to create database in non-existent directory
	_, err := NewSemanticMemory("/nonexistent/path/test.db", embedder)
	if err == nil {
		t.Error("Expected error for invalid path, got nil")
	}
}

func TestSemanticMemory_TopKLargerThanEntries(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()
	embedder.setEmbedding("content1", []float64{1, 0, 0, 0})
	embedder.setEmbedding("content2", []float64{0, 1, 0, 0})
	embedder.setEmbedding("query", []float64{0.5, 0.5, 0, 0})

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	defer func() { _ = sm.Close() }()

	ctx := context.Background()

	// Store only 2 entries
	if err := sm.Store(ctx, "doc1", "content1", nil); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if err := sm.Store(ctx, "doc2", "content2", nil); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	// Request top 100
	results, err := sm.Search(ctx, "query", 100)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	// Should return only 2 results
	if len(results) != 2 {
		t.Errorf("Search(topK=100) with 2 entries returned %d results, want 2", len(results))
	}
}

func TestSemanticMemory_FileCreated(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	_ = sm.Close()

	// Verify file was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("Database file was not created at %s", dbPath)
	}
}

func TestSemanticMemory_Count(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	embedder := newMockEmbedder()
	embedder.setEmbedding("content1", []float64{1, 0, 0, 0})
	embedder.setEmbedding("content2", []float64{0, 1, 0, 0})
	embedder.setEmbedding("content3", []float64{0, 0, 1, 0})

	sm, err := NewSemanticMemory(dbPath, embedder)
	if err != nil {
		t.Fatalf("NewSemanticMemory() error = %v", err)
	}
	defer func() { _ = sm.Close() }()

	ctx := context.Background()

	// Test Count on empty DB
	count, err := sm.Count(ctx)
	if err != nil {
		t.Fatalf("Count() error on empty DB = %v", err)
	}
	if count != 0 {
		t.Errorf("Count() on empty DB = %d, want 0", count)
	}

	// Store a few entries
	entries := []struct {
		key     string
		content string
	}{
		{"doc1", "content1"},
		{"doc2", "content2"},
		{"doc3", "content3"},
	}

	for _, e := range entries {
		if err := sm.Store(ctx, e.key, e.content, nil); err != nil {
			t.Fatalf("Store() error = %v", err)
		}
	}

	// Test Count returns correct number
	count, err = sm.Count(ctx)
	if err != nil {
		t.Fatalf("Count() error after storing = %v", err)
	}
	if count != 3 {
		t.Errorf("Count() after storing = %d, want 3", count)
	}
}
