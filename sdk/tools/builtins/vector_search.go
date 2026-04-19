package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/user/agent/sdk/tools"
)

const toolVectorSearchDescription = `Recommended first choice for codebase investigation and discovery. Searches the entire project in a single call using semantic understanding — far more efficient than multiple grep or glob calls. Understands code intent, concepts, and relationships, not just text patterns. Excels at: finding implementations of a concept (e.g. "authentication middleware"), locating related functionality across files, discovering architecture patterns and data flows, and understanding how subsystems connect. Returns file paths, line ranges, relevance scores, and content previews. Prefer ripgrep/glob only for exact string literals, specific error messages, or known file-name patterns.`

// VectorSearchResult represents a single search result.
type VectorSearchResult struct {
	FilePath  string
	FileName  string
	Content   string
	Score     float32
	StartLine int
	EndLine   int
	Language  string
}

// VectorSearchFunc is the function signature for performing vector search.
// Provided by the backend layer at wiring time.
type VectorSearchFunc func(ctx context.Context, query string, topK int, fileFilter string) ([]VectorSearchResult, error)

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
				"description": "Natural language description of the code concept, functionality, or pattern you're looking for. Examples: 'authentication middleware', 'database connection pooling', 'error handling in HTTP handlers', 'WebSocket event dispatching logic'"
			},
			"top_k": {
				"type": "integer",
				"description": "Number of results to return. Use 10 for focused lookups, 20-30 for broad exploration of a feature area. Default: 10, max: 50",
				"default": 10
			},
			"file_pattern": {
				"type": "string",
				"description": "Optional glob pattern to narrow results to specific file types or directories. Examples: '**/*.go' (Go files only), 'src/**/*.ts' (TypeScript in src), 'backend/**' (backend directory). Omit for whole-codebase search."
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
	Query       string `json:"query"`
	TopK        int    `json:"top_k"`
	FilePattern string `json:"file_pattern"`
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

	results, err := t.searchFunc(ctx, params.Query, params.TopK, params.FilePattern)
	if err != nil {
		return tools.ErrorResult("search failed: %v", err), nil
	}

	if len(results) == 0 {
		return tools.ToolResult{Content: "No results found for query: " + params.Query}, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d results for %q:\n", len(results), params.Query)

	for i, r := range results {
		// Format header: index, path, lines, score, language.
		fmt.Fprintf(&sb, "\n%d. %s", i+1, r.FilePath)
		if r.StartLine > 0 && r.EndLine > 0 {
			fmt.Fprintf(&sb, " (lines %d-%d", r.StartLine, r.EndLine)
		} else if r.StartLine > 0 {
			fmt.Fprintf(&sb, " (line %d", r.StartLine)
		}
		if r.StartLine > 0 {
			fmt.Fprintf(&sb, ", score: %.2f", r.Score)
			if r.Language != "" {
				fmt.Fprintf(&sb, ", language: %s", r.Language)
			}
			sb.WriteString(")")
		} else {
			fmt.Fprintf(&sb, " (score: %.2f", r.Score)
			if r.Language != "" {
				fmt.Fprintf(&sb, ", language: %s", r.Language)
			}
			sb.WriteString(")")
		}
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
