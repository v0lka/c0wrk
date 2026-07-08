package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// --- Tavily Search Provider ---

// TavilyProvider implements SearchProvider using the Tavily API.
type TavilyProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewTavilyProviderWithClient creates a new TavilyProvider with the given API key, timeout,
// and optional HTTP client. If client is nil, a default client with the specified timeout is used.
func NewTavilyProviderWithClient(apiKey string, timeout time.Duration, client *http.Client) *TavilyProvider {
	if client == nil {
		// The Tavily API key travels in the POST body. Refuse to follow
		// redirects so the body (and key) is never re-sent to another host.
		// A caller-provided client is used as-is (never mutated).
		client = &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &TavilyProvider{
		apiKey:  apiKey,
		baseURL: "https://api.tavily.com/search",
		client:  client,
	}
}

// Name returns the provider name.
func (p *TavilyProvider) Name() string { return "tavily" }

// SetBaseURL allows setting a custom base URL (useful for testing).
func (p *TavilyProvider) SetBaseURL(url string) { p.baseURL = url }

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

// Search performs a web search using the Tavily API.
func (p *TavilyProvider) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	if p.apiKey == "" {
		return nil, errors.New("API key is not configured")
	}

	// Build request body
	reqBody := tavilyRequest{
		APIKey:      p.apiKey,
		Query:       query,
		MaxResults:  maxResults,
		SearchDepth: "basic",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Execute request
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Check status code
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(limitBody(resp.Body))
		if readErr != nil {
			return nil, fmt.Errorf("HTTP %d (could not read body: %w)", resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var tavilyResp tavilyResponse
	if err := json.NewDecoder(limitBody(resp.Body)).Decode(&tavilyResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Convert to generic SearchResult
	results := make([]SearchResult, len(tavilyResp.Results))
	for i, r := range tavilyResp.Results {
		results[i] = SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
		}
	}
	return results, nil
}
