package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
)

// Embedder interface for generating embeddings (avoids importing llm package).
type Embedder interface {
	// Embed generates an embedding vector for the given text.
	Embed(ctx context.Context, text string) ([]float64, error)
	// Dimensions returns the number of dimensions in the embedding vector.
	Dimensions() int
}

// SemanticResult represents a search result from SemanticMemory.
type SemanticResult struct {
	Key      string
	Content  string
	Metadata map[string]string
	Score    float64 // cosine similarity
}

// SemanticMemory provides embedding-based semantic search over stored content.
type SemanticMemory struct {
	db       *sql.DB
	embedder Embedder
}

// NewSemanticMemory creates a new SemanticMemory instance using the provided database connection.
// The database should be managed by the caller (e.g., MemorySystem).
func NewSemanticMemory(db *sql.DB, embedder Embedder) (*SemanticMemory, error) {
	// Create schema
	schema := `
		CREATE TABLE IF NOT EXISTS semantic_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key TEXT NOT NULL UNIQUE,
			content TEXT NOT NULL,
			metadata TEXT,
			embedding TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_semantic_entries_key ON semantic_entries(key);
	`

	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		return nil, fmt.Errorf("create schema: %w", err)
	}

	// Validate embedding dimensions - clear incompatible data if model changed
	if embedder != nil {
		var existingEmbeddingJSON string
		err := db.QueryRowContext(context.Background(), "SELECT embedding FROM semantic_entries LIMIT 1").Scan(&existingEmbeddingJSON)
		if err == nil && existingEmbeddingJSON != "" {
			var existingEmbedding []float64
			if parseErr := json.Unmarshal([]byte(existingEmbeddingJSON), &existingEmbedding); parseErr == nil {
				if len(existingEmbedding) != embedder.Dimensions() {
					slog.Warn("embedding dimension mismatch, clearing semantic memory",
						"expected", embedder.Dimensions(),
						"found", len(existingEmbedding))
					if _, delErr := db.ExecContext(context.Background(), "DELETE FROM semantic_entries"); delErr != nil {
						slog.Error("failed to clear incompatible semantic entries", "error", delErr)
					}
				}
			}
		}
	}

	return &SemanticMemory{
		db:       db,
		embedder: embedder,
	}, nil
}

// Store embeds content and stores it with key and metadata.
func (sm *SemanticMemory) Store(ctx context.Context, key, content string, metadata map[string]string) error {
	// Generate embedding
	embedding, err := sm.embedder.Embed(ctx, content)
	if err != nil {
		return fmt.Errorf("generate embedding: %w", err)
	}

	// Serialize embedding to JSON
	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("marshal embedding: %w", err)
	}

	// Serialize metadata to JSON
	var metadataJSON []byte
	if metadata != nil {
		metadataJSON, err = json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("marshal metadata: %w", err)
		}
	}

	// Insert or replace entry
	query := `
		INSERT INTO semantic_entries (key, content, metadata, embedding)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			content = excluded.content,
			metadata = excluded.metadata,
			embedding = excluded.embedding,
			created_at = CURRENT_TIMESTAMP
	`

	_, err = sm.db.ExecContext(ctx, query, key, content, string(metadataJSON), string(embeddingJSON))
	if err != nil {
		return fmt.Errorf("insert entry: %w", err)
	}

	return nil
}

// Search embeds the query and finds top-K similar entries by cosine similarity.
func (sm *SemanticMemory) Search(ctx context.Context, query string, topK int) ([]SemanticResult, error) {
	if topK <= 0 {
		return nil, nil
	}

	// Generate query embedding
	queryEmbedding, err := sm.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("generate query embedding: %w", err)
	}

	// Load all entries from database
	rows, err := sm.db.QueryContext(ctx, `
		SELECT key, content, metadata, embedding
		FROM semantic_entries
	`)
	if err != nil {
		return nil, fmt.Errorf("query entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type entry struct {
		key       string
		content   string
		metadata  map[string]string
		embedding []float64
	}

	var entries []entry
	for rows.Next() {
		var e entry
		var metadataJSON, embeddingJSON sql.NullString

		if err := rows.Scan(&e.key, &e.content, &metadataJSON, &embeddingJSON); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		// Parse metadata
		if metadataJSON.Valid && metadataJSON.String != "" {
			if err := json.Unmarshal([]byte(metadataJSON.String), &e.metadata); err != nil {
				return nil, fmt.Errorf("unmarshal metadata: %w", err)
			}
		}

		// Parse embedding
		if embeddingJSON.Valid && embeddingJSON.String != "" {
			if err := json.Unmarshal([]byte(embeddingJSON.String), &e.embedding); err != nil {
				return nil, fmt.Errorf("unmarshal embedding: %w", err)
			}
		}

		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	// Compute cosine similarity for each entry
	type scoredEntry struct {
		entry entry
		score float64
	}

	scored := make([]scoredEntry, 0, len(entries))
	for _, e := range entries {
		score := cosineSimilarity(queryEmbedding, e.embedding)
		scored = append(scored, scoredEntry{entry: e, score: score})
	}

	// Sort by score descending
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// Return top-K results
	if topK > len(scored) {
		topK = len(scored)
	}

	results := make([]SemanticResult, topK)
	for i := 0; i < topK; i++ {
		results[i] = SemanticResult{
			Key:      scored[i].entry.key,
			Content:  scored[i].entry.content,
			Metadata: scored[i].entry.metadata,
			Score:    scored[i].score,
		}
	}

	return results, nil
}

// Delete removes an entry by key.
func (sm *SemanticMemory) Delete(ctx context.Context, key string) error {
	_, err := sm.db.ExecContext(ctx, `DELETE FROM semantic_entries WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete entry: %w", err)
	}
	return nil
}

// Count returns the total number of stored semantic entries.
func (sm *SemanticMemory) Count(ctx context.Context) (int, error) {
	var count int
	err := sm.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM semantic_entries").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting semantic entries: %w", err)
	}
	return count, nil
}

// cosineSimilarity computes the cosine similarity between two vectors.
// Returns a value between -1 and 1, where 1 means identical direction.
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dotProduct, magnitudeA, magnitudeB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		magnitudeA += a[i] * a[i]
		magnitudeB += b[i] * b[i]
	}

	magnitudeA = math.Sqrt(magnitudeA)
	magnitudeB = math.Sqrt(magnitudeB)

	if magnitudeA == 0 || magnitudeB == 0 {
		return 0
	}

	return dotProduct / (magnitudeA * magnitudeB)
}
