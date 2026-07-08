package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// maxResponseBodyBytes caps how much of a provider HTTP response body is read,
// protecting against unbounded memory use from a hostile or broken endpoint.
const maxResponseBodyBytes = 4 << 20 // 4 MB

// limitBody wraps a response body reader with a hard size cap.
func limitBody(r io.Reader) io.Reader {
	return io.LimitReader(r, maxResponseBodyBytes)
}

const toolWebsearchDescription = `Search the web and return a list of results with titles, URLs, and text snippets. Use this to find current information, external documentation, recent events, or any knowledge that may be beyond training data. Returns up to max_results entries (default 5), each with a title, URL, and a brief snippet summarizing the page content.`

// SearchResult represents a single provider-agnostic search result.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// SearchProvider defines the interface for web search providers.
// Built-in implementations include BraveProvider, DuckDuckGoProvider,
// ExaProvider, and TavilyProvider. To add a custom provider, implement this
// interface and pass it to NewTool.
type SearchProvider interface {
	Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error)
	Name() string
}

// Limits is an alias for builtins.WebSearchLimits.
type Limits = builtins.WebSearchLimits

// --- Tool ---

// Tool searches the web using a pluggable SearchProvider.
type Tool struct {
	*tools.BaseTool
	provider SearchProvider
	limits   Limits
}

// NewTool creates a new Tool with the given SearchProvider and specified limits.
func NewTool(provider SearchProvider, limits Limits) *Tool {
	schema := `{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "The search query string. Be specific and use keywords for best results."
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of results to return. Default: 5."
			}
		},
		"required": ["query"]
	}`
	return &Tool{
		BaseTool: &tools.BaseTool{
			ToolName:        "web_search",
			ToolDescription: toolWebsearchDescription,
			Schema:          json.RawMessage(schema),
			Policy:          tools.PolicyAlwaysAllow,
			Untrusted:       true,
		},
		provider: provider,
		limits:   limits,
	}
}

// webSearchInput represents the input parameters for web search.
type webSearchInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

// Execute performs a web search and returns the results.
func (t *Tool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params webSearchInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	// Fallback extraction for common model-generated parameter variations.
	if params.Query == "" {
		params.Query = extractQueryFallback(input)
	}

	// Validate query parameter
	if params.Query == "" {
		return tools.ToolResult{Content: "query parameter is required", IsError: true}, nil
	}

	// Set default max_results if not provided
	maxResults := params.MaxResults
	if maxResults <= 0 {
		maxResults = t.limits.MaxResults
	}

	// Perform the search
	results, err := t.provider.Search(ctx, params.Query, maxResults)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("search failed: %v", err), IsError: true}, nil
	}

	// Check for empty results
	if len(results) == 0 {
		return tools.ToolResult{Content: "No results found", IsError: false}, nil
	}

	// Format results
	output := formatResults(results)
	return tools.ToolResult{Content: output, IsError: false}, nil
}

// formatResults formats the search results as a readable string.
func formatResults(results []SearchResult) string {
	var output strings.Builder
	for i, result := range results {
		if i > 0 {
			output.WriteString("\n\n")
		}
		fmt.Fprintf(&output, "%d. **%s**\n   URL: %s\n   Snippet: %s", i+1, result.Title, result.URL, result.Snippet)
	}
	return output.String()
}

// extractQueryFallback attempts to extract a query string from common
// parameter variations that models may produce (e.g. "queries", "search_query").
func extractQueryFallback(input json.RawMessage) string {
	var raw map[string]any
	if err := json.Unmarshal(input, &raw); err != nil {
		return ""
	}

	for _, key := range []string{"queries", "search_query"} {
		val, ok := raw[key]
		if !ok {
			continue
		}
		switch v := val.(type) {
		case string:
			if v != "" {
				return v
			}
		case []any:
			for _, elem := range v {
				if s, ok := elem.(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return ""
}
