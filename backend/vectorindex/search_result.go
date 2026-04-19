package vectorindex

// SearchResult represents a single result from a vector similarity search.
type SearchResult struct {
	FilePath  string  // absolute path to source file
	FileName  string  // basename of the file
	Content   string  // chunk content
	Score     float32 // similarity score (higher = more similar)
	StartLine int     // 1-based start line in original file
	EndLine   int     // 1-based end line in original file
	Language  string  // detected language (e.g., "go", "typescript")
}
