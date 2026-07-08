package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// --- Brave Search Provider ---

// BraveProvider implements SearchProvider using the Brave Search API.
type BraveProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewBraveProviderWithClient creates a new BraveProvider with the given API key, timeout,
// and optional HTTP client. If client is nil, a default client with the specified timeout is used.
func NewBraveProviderWithClient(apiKey string, timeout time.Duration, client *http.Client) *BraveProvider {
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &BraveProvider{
		apiKey:  apiKey,
		baseURL: "https://api.search.brave.com/res/v1/web/search",
		client:  client,
	}
}

// Name returns the provider name.
func (p *BraveProvider) Name() string { return "brave" }

// SetBaseURL allows setting a custom base URL (useful for testing).
func (p *BraveProvider) SetBaseURL(rawURL string) { p.baseURL = rawURL }

// braveResponse represents the top-level response from Brave Search API.
type braveResponse struct {
	Web braveWebResults `json:"web"`
}

// braveWebResults contains the web search results.
type braveWebResults struct {
	Results []braveResult `json:"results"`
}

// braveResult represents a single search result from Brave.
type braveResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// Search performs a web search using the Brave Search API.
func (p *BraveProvider) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	if p.apiKey == "" {
		return nil, errors.New("API key is not configured")
	}

	// Build request URL with query parameters
	params := url.Values{}
	params.Set("q", query)
	params.Set("count", strconv.Itoa(maxResults))
	reqURL := p.baseURL + "?" + params.Encode()

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", p.apiKey)

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
	var braveResp braveResponse
	if err := json.NewDecoder(limitBody(resp.Body)).Decode(&braveResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Convert to generic SearchResult
	results := make([]SearchResult, len(braveResp.Web.Results))
	for i, r := range braveResp.Web.Results {
		results[i] = SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Description,
		}
	}
	return results, nil
}
