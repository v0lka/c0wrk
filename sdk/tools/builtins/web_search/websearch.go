package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/user/agent/sdk/tools"
	"github.com/user/agent/sdk/tools/builtins"
)

const toolWebsearchDescription = `Search the web and return a list of results with titles, URLs, and text snippets. Use this to find current information, external documentation, recent events, or any knowledge that may be beyond training data. Returns up to max_results entries (default 5), each with a title, URL, and a brief snippet summarizing the page content.`

// SearchResult represents a single provider-agnostic search result.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// SearchProvider defines the interface for web search providers.
type SearchProvider interface {
	Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error)
	Name() string
}

// Limits is an alias for builtins.WebSearchLimits.
type Limits = builtins.WebSearchLimits

// DefaultLimits returns the default limits for web_search.
func DefaultLimits() Limits {
	return builtins.DefaultWebSearchLimits()
}

// --- WebSearchTool ---

// WebSearchTool searches the web using a pluggable SearchProvider.
type WebSearchTool struct {
	*tools.BaseTool
	provider SearchProvider
	limits   Limits
}

// NewWebSearchTool creates a new WebSearchTool with the given SearchProvider and default limits.
func NewWebSearchTool(provider SearchProvider) *WebSearchTool {
	return NewWebSearchToolWithLimits(provider, DefaultLimits())
}

// NewWebSearchToolWithLimits creates a new WebSearchTool with the given SearchProvider and specified limits.
func NewWebSearchToolWithLimits(provider SearchProvider, limits Limits) *WebSearchTool {
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
	return &WebSearchTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "web_search",
			ToolDescription: toolWebsearchDescription,
			Schema:          json.RawMessage(schema),
			Policy:          tools.PolicyAlwaysAllow,
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
func (t *WebSearchTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
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
