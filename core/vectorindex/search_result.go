package vectorindex

import "context"

// VectorSearcher is the narrow interface for search-only operations, for
// external consumers that don't need full Service access.
type VectorSearcher interface {
	HybridSearch(ctx context.Context, opts SearchOptions) ([]SearchResult, error)
	BrowseWithFilter(ctx context.Context, topK int, fileFilter string) ([]SearchResult, error)
	WaitReady(ctx context.Context) error
}

// SearchResult is a single result from a similarity search. Score carries the
// primary metric for the mode — the fused RRF score for hybrid, the cosine
// similarity for vector, the BM25 score for lexical — with the per-side
// score/rank fields attributing each side's contribution.
type SearchResult struct {
	FilePath  string  // absolute path to source file
	FileName  string  // basename of the file
	Content   string  // chunk content
	Score     float32 // primary score (fused RRF for hybrid; cosine for vector; BM25 for lexical)
	StartLine int     // 1-based start line in original file
	EndLine   int     // 1-based end line in original file
	Language  string  // detected language (e.g., "go", "typescript")

	// Per-side attribution, populated when that retriever contributed a hit;
	// zero = not returned by that side. Rank is 1-based.
	VectorScore  float32
	LexicalScore float32
	VectorRank   int
	LexicalRank  int
}
