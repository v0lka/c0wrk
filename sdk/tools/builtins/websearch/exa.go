package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// --- Exa Search Provider ---

// ExaProvider implements SearchProvider using the Exa AI API.
type ExaProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewExaProviderWithClient creates a new ExaProvider with the given API key, timeout,
// and optional HTTP client. If client is nil, a default client with the specified timeout is used.
func NewExaProviderWithClient(apiKey string, timeout time.Duration, client *http.Client) *ExaProvider {
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &ExaProvider{
		apiKey:  apiKey,
		baseURL: "https://api.exa.ai/search",
		client:  client,
	}
}

// Name returns the provider name.
func (p *ExaProvider) Name() string { return "exa" }

// SetBaseURL allows setting a custom base URL (useful for testing).
func (p *ExaProvider) SetBaseURL(url string) { p.baseURL = url }

// exaRequest represents the request body for Exa API.
type exaRequest struct {
	Query      string      `json:"query"`
	NumResults int         `json:"numResults"`
	Type       string      `json:"type"`
	Contents   exaContents `json:"contents"`
}

// exaContents specifies content extraction options.
type exaContents struct {
	Highlights exaHighlights `json:"highlights"`
}

// exaHighlights specifies highlight extraction options.
type exaHighlights struct {
	MaxCharacters int `json:"maxCharacters"`
}

// exaResponse represents the response from Exa API.
type exaResponse struct {
	Results []exaResult `json:"results"`
}

// exaResult represents a single search result from Exa.
type exaResult struct {
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	Highlights []string `json:"highlights"`
	Text       string   `json:"text"`
}

// Search performs a web search using the Exa API.
func (p *ExaProvider) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	if p.apiKey == "" {
		return nil, errors.New("API key is not configured")
	}

	// Build request body
	reqBody := exaRequest{
		Query:      query,
		NumResults: maxResults,
		Type:       "auto",
		Contents: exaContents{
			Highlights: exaHighlights{
				MaxCharacters: 4000,
			},
		},
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
	req.Header.Set("x-api-key", p.apiKey)

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
	var exaResp exaResponse
	if err := json.NewDecoder(limitBody(resp.Body)).Decode(&exaResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Convert to generic SearchResult
	results := make([]SearchResult, len(exaResp.Results))
	for i, r := range exaResp.Results {
		snippet := r.Text
		if snippet == "" && len(r.Highlights) > 0 {
			snippet = strings.Join(r.Highlights, "\n")
		}
		results[i] = SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: snippet,
		}
	}
	return results, nil
}
