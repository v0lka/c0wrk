package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/v0lka/sp4rk/tools"
)

const toolVectorSearchDescription = `Search the project codebase using hybrid (vector + BM25) similarity matching. Finds code by meaning and intent as well as by literal symbol/keyword match. Returns file paths, line ranges, fused relevance scores, per-side ranks, and content previews. Effective for: finding implementations of a concept (e.g. "authentication middleware"), locating related functionality across files, discovering architecture patterns and data flows, and pinpointing a specific identifier (e.g. +MatcherFactory). For exact string literals or known file-name patterns, use ripgrep or glob.`

// VectorSearchResult represents a single search result.
//
// Score is the primary score: the fused RRF score for hybrid queries,
// the raw cosine similarity for vector-only queries, or the BM25 score
// for lexical-only queries. VectorRank / LexicalRank are per-side 1-based
// ranks, 0 when the corresponding retriever did not return the hit.
type VectorSearchResult struct {
	FilePath    string
	FileName    string
	Content     string
	Score       float32
	StartLine   int
	EndLine     int
	Language    string
	VectorRank  int
	LexicalRank int
}

// VectorSearchOptions carries search parameters from the tool to the
// backend. The backend maps this onto vectorindex.SearchOptions.
type VectorSearchOptions struct {
	Query       string
	TopK        int
	FilePattern string
	MustMatch   []string
	// Mode is "hybrid" | "vector" | "lexical". Empty string defaults
	// to hybrid on the backend side, with auto-fallback to vector when
	// the lexical index is empty or unavailable.
	Mode string
}

// VectorSearchFunc is the function signature for performing vector search.
// Provided by the backend layer at wiring time.
type VectorSearchFunc func(ctx context.Context, opts VectorSearchOptions) ([]VectorSearchResult, error)

// VectorSearchWaitFunc blocks until the vector index is ready.
type VectorSearchWaitFunc func(ctx context.Context) error

// VectorSearchTool searches the project codebase using semantic similarity.
type VectorSearchTool struct {
	*tools.BaseTool
	searchFunc VectorSearchFunc
	waitFunc   VectorSearchWaitFunc
}

// maxVectorSearchTopK is the maximum number of results the tool will return.
const maxVectorSearchTopK = 50

// defaultVectorSearchTopK is the default number of results returned.
const defaultVectorSearchTopK = 10

// maxContentPreview is the maximum number of characters shown for each result's content.
const maxContentPreview = 500

// NewVectorSearchTool creates a new VectorSearchTool instance.
func NewVectorSearchTool(searchFunc VectorSearchFunc, waitFunc VectorSearchWaitFunc) *VectorSearchTool {
	return &VectorSearchTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "semantic_search",
			ToolDescription: toolVectorSearchDescription,
			Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Natural language description of the code concept, functionality, or pattern you're looking for. Examples: 'authentication middleware', 'database connection pooling', 'error handling in HTTP handlers', 'WebSocket event dispatching logic'. Tokens prefixed with '+' (e.g. '+MatcherFactory') are treated as must-match substrings on chunk content."
			},
			"top_k": {
				"type": "integer",
				"description": "Number of results to return. Use 10 for focused lookups, 20-30 for broad exploration of a feature area. Default: 10, max: 50",
				"default": 10
			},
			"file_pattern": {
				"type": "string",
				"description": "Optional glob pattern to narrow results to specific file types or directories. Examples: '**/*.go' (Go files only), 'src/**/*.ts' (TypeScript in src), 'backend/**' (backend directory). Omit for whole-codebase search."
			},
			"must_match": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Optional list of literal substrings that must ALL appear in a chunk's content. Useful for pinpointing a specific identifier or phrase while still using semantic ranking. Example: [\"MatcherFactory\"]."
			},
			"mode": {
				"type": "string",
				"enum": ["hybrid", "vector", "lexical"],
				"description": "Retrieval strategy. 'hybrid' (default) fuses vector and BM25 results via Reciprocal Rank Fusion. 'vector' uses embedding similarity only. 'lexical' uses BM25 only. Backend auto-falls-back to 'vector' when the lexical index is empty.",
				"default": "hybrid"
			}
		},
		"required": ["query"]
	}`),
			Policy: tools.PolicyAlwaysAllow,
		},
		searchFunc: searchFunc,
		waitFunc:   waitFunc,
	}
}

// VectorSearchInput represents the input parameters for semantic_search.
type VectorSearchInput struct {
	Query       string   `json:"query"`
	TopK        int      `json:"top_k"`
	FilePattern string   `json:"file_pattern"`
	MustMatch   []string `json:"must_match"`
	Mode        string   `json:"mode"`
}

// Execute performs the semantic search and returns formatted results.
func (t *VectorSearchTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params VectorSearchInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.Query == "" {
		return tools.ErrorResult("query is required"), nil
	}

	// Apply defaults and caps.
	if params.TopK <= 0 {
		params.TopK = defaultVectorSearchTopK
	}
	if params.TopK > maxVectorSearchTopK {
		params.TopK = maxVectorSearchTopK
	}

	// Wait for the vector index to be ready.
	if t.waitFunc != nil {
		if err := t.waitFunc(ctx); err != nil {
			return tools.ErrorResult("vector index not ready: %v", err), nil
		}
	}

	// VectorSearchInput has identical field layout to VectorSearchOptions; update both if adding fields.
	results, err := t.searchFunc(ctx, VectorSearchOptions(params))
	if err != nil {
		return tools.ErrorResult("search failed: %v", err), nil
	}

	if len(results) == 0 {
		return tools.ToolResult{Content: "No results found for query: " + params.Query}, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d results for %q:\n", len(results), params.Query)

	for i, r := range results {
		// Format header: index, path, lines, score, ranks, language.
		fmt.Fprintf(&sb, "\n%d. %s", i+1, r.FilePath)
		switch {
		case r.StartLine > 0 && r.EndLine > 0:
			fmt.Fprintf(&sb, " (lines %d-%d", r.StartLine, r.EndLine)
		case r.StartLine > 0:
			fmt.Fprintf(&sb, " (line %d", r.StartLine)
		default:
			sb.WriteString(" (")
		}
		fmt.Fprintf(&sb, ", score: %.2f", r.Score)
		if r.VectorRank > 0 {
			fmt.Fprintf(&sb, ", V#%d", r.VectorRank)
		}
		if r.LexicalRank > 0 {
			fmt.Fprintf(&sb, ", L#%d", r.LexicalRank)
		}
		if r.Language != "" {
			fmt.Fprintf(&sb, ", language: %s", r.Language)
		}
		sb.WriteString(")")
		sb.WriteString("\n")

		// Content preview.
		preview := r.Content
		if len(preview) > maxContentPreview {
			preview = preview[:maxContentPreview] + "..."
		}
		fmt.Fprintf(&sb, "   %s\n", strings.ReplaceAll(preview, "\n", "\n   "))
	}

	return tools.ToolResult{Content: sb.String()}, nil
}
