package vectorindex

import "context"

// VectorSearcher is the narrow interface for search-only operations.
// External consumers that don't need full Service access should use this (S-24).
type VectorSearcher interface {
	HybridSearch(ctx context.Context, opts SearchOptions) ([]SearchResult, error)
	BrowseWithFilter(ctx context.Context, topK int, fileFilter string) ([]SearchResult, error)
	WaitReady(ctx context.Context) error
}

// SearchResult represents a single result from a vector similarity search.
//
// For hybrid (RRF-fused) results the Score field carries the fused RRF
// score; VectorScore/LexicalScore and VectorRank/LexicalRank carry the
// per-side attribution. For pure vector results Score is the cosine
// similarity and VectorScore/VectorRank are populated (with the lexical
// fields left zero). For pure lexical results Score is the BM25 score
// and LexicalScore/LexicalRank are populated.
type SearchResult struct {
	FilePath  string  // absolute path to source file
	FileName  string  // basename of the file
	Content   string  // chunk content
	Score     float32 // primary score (fused RRF for hybrid; cosine for vector; BM25 for lexical)
	StartLine int     // 1-based start line in original file
	EndLine   int     // 1-based end line in original file
	Language  string  // detected language (e.g., "go", "typescript")

	// Per-side attribution, populated when the retriever contributed a
	// hit for this document. Zero values indicate "not returned by that
	// side". Rank is 1-based.
	VectorScore  float32
	LexicalScore float32
	VectorRank   int
	LexicalRank  int
}
