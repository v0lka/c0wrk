package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/user/agent/internal/tools"
)

const toolWebsearchDescription = "Search the web using Tavily API"

// WebSearchTool searches the web using Tavily API.
type WebSearchTool struct {
	*tools.BaseTool
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewWebSearchTool creates a new WebSearchTool with the given API key.
func NewWebSearchTool(apiKey string) *WebSearchTool {
	schema := `{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Search query"
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum results to return (default: 5)"
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
		apiKey:  apiKey,
		baseURL: "https://api.tavily.com/search",
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// webSearchInput represents the input parameters for web search.
type webSearchInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

// tavilyRequest represents the request body for Tavily API.
type tavilyRequest struct {
	APIKey      string `json:"api_key"`
	Query       string `json:"query"`
	MaxResults  int    `json:"max_results"`
	SearchDepth string `json:"search_depth"`
}

// tavilyResponse represents the response from Tavily API.
type tavilyResponse struct {
	Results []tavilyResult `json:"results"`
}

// tavilyResult represents a single search result from Tavily.
type tavilyResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// Execute performs a web search and returns the results.
func (t *WebSearchTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params webSearchInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	// Validate query parameter
	if params.Query == "" {
		return tools.ToolResult{Content: "query parameter is required", IsError: true}, nil
	}

	// Check API key
	if t.apiKey == "" {
		return tools.ToolResult{Content: "API key is not configured", IsError: true}, nil
	}

	// Set default max_results if not provided
	maxResults := params.MaxResults
	if maxResults <= 0 {
		maxResults = 5
	}

	// Perform the search
	results, err := t.search(ctx, params.Query, maxResults)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("search failed: %v", err), IsError: true}, nil
	}

	// Check for empty results
	if len(results) == 0 {
		return tools.ToolResult{Content: "No results found", IsError: false}, nil
	}

	// Format results
	output := t.formatResults(results)
	return tools.ToolResult{Content: output, IsError: false}, nil
}

// search performs the actual API call to Tavily.
func (t *WebSearchTool) search(ctx context.Context, query string, maxResults int) ([]tavilyResult, error) {
	// Build request body
	reqBody := tavilyRequest{
		APIKey:      t.apiKey,
		Query:       query,
		MaxResults:  maxResults,
		SearchDepth: "basic",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Execute request
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Check status code
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var tavilyResp tavilyResponse
	if err := json.NewDecoder(resp.Body).Decode(&tavilyResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return tavilyResp.Results, nil
}

// formatResults formats the search results as a readable string.
func (t *WebSearchTool) formatResults(results []tavilyResult) string {
	var output strings.Builder
	for i, result := range results {
		if i > 0 {
			output.WriteString("\n\n")
		}
		fmt.Fprintf(&output, "%d. **%s**\n   URL: %s\n   Snippet: %s", i+1, result.Title, result.URL, result.Content)
	}
	return output.String()
}

// SetBaseURL allows setting a custom base URL (useful for testing).
func (t *WebSearchTool) SetBaseURL(url string) {
	t.baseURL = url
}
