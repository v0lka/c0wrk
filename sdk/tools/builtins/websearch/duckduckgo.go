package websearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// --- DuckDuckGo Search Provider ---

// DuckDuckGoProvider implements SearchProvider using DuckDuckGo HTML search.
// Note: DuckDuckGo does not have an official API, so we scrape the HTML version.
type DuckDuckGoProvider struct {
	baseURL string
	client  *http.Client
}

// NewDuckDuckGoProviderWithClient creates a new DuckDuckGoProvider with the given timeout
// and optional HTTP client. If client is nil, a default client with the specified timeout is used.
func NewDuckDuckGoProviderWithClient(timeout time.Duration, client *http.Client) *DuckDuckGoProvider {
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &DuckDuckGoProvider{
		baseURL: "https://html.duckduckgo.com/html/",
		client:  client,
	}
}

// Name returns the provider name.
func (p *DuckDuckGoProvider) Name() string { return "duckduckgo" }

// SetBaseURL allows setting a custom base URL (useful for testing).
func (p *DuckDuckGoProvider) SetBaseURL(rawURL string) { p.baseURL = rawURL }

// Search performs a web search using DuckDuckGo HTML search.
func (p *DuckDuckGoProvider) Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	// Build request URL with query parameters
	params := url.Values{}
	params.Set("q", query)
	reqURL := p.baseURL + "?" + params.Encode()

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set a realistic User-Agent to avoid being blocked
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

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

	// Parse HTML response
	results, err := parseDuckDuckGoHTML(limitBody(resp.Body), maxResults)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return results, nil
}

// parseDuckDuckGoHTML parses the DuckDuckGo HTML response and extracts search results.
func parseDuckDuckGoHTML(r io.Reader, maxResults int) ([]SearchResult, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	var results []SearchResult

	// Find all result divs
	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" {
			if hasClass(n, "result") {
				// Extract result from this div
				result := extractResultFromDiv(n)
				if result.URL != "" && result.Title != "" {
					results = append(results, result)
					if len(results) >= maxResults {
						return
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if len(results) >= maxResults {
				return
			}
			traverse(c)
		}
	}
	traverse(doc)

	return results, nil
}

// extractResultFromDiv extracts a single search result from a result div.
func extractResultFromDiv(n *html.Node) SearchResult {
	var result SearchResult

	// Find the anchor with class "result__a" for title and URL
	var findAnchor func(*html.Node)
	findAnchor = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			if hasClass(node, "result__a") {
				// Get URL from href attribute
				for _, attr := range node.Attr {
					if attr.Key == "href" {
						result.URL = cleanDuckDuckGoURL(attr.Val)
						break
					}
				}
				// Get title from text content
				result.Title = getTextContent(node)
				return
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			findAnchor(c)
		}
	}
	findAnchor(n)

	// Find snippet in anchor with class "result__snippet"
	var findSnippet func(*html.Node)
	findSnippet = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			if hasClass(node, "result__snippet") {
				result.Snippet = getTextContent(node)
				return
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			findSnippet(c)
		}
	}
	findSnippet(n)

	return result
}

// hasClass checks if a node has a specific CSS class.
func hasClass(n *html.Node, class string) bool {
	for _, attr := range n.Attr {
		if attr.Key == "class" {
			classes := strings.Fields(attr.Val)
			for _, c := range classes {
				if c == class {
					return true
				}
			}
		}
	}
	return false
}

// getTextContent extracts the text content from a node and its children.
func getTextContent(n *html.Node) string {
	var sb strings.Builder
	var extract func(*html.Node)
	extract = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			extract(c)
		}
	}
	extract(n)
	return strings.TrimSpace(sb.String())
}

// cleanDuckDuckGoURL extracts the actual URL from DuckDuckGo's redirect URL.
// DuckDuckGo uses URLs like: /l/?uddg=https%3A%2F%2Fexample.com&rut=...
func cleanDuckDuckGoURL(ddgURL string) string {
	// Check if it's a DuckDuckGo redirect URL
	if strings.Contains(ddgURL, "/l/?uddg=") {
		// Parse the URL to extract the uddg parameter
		if u, err := url.Parse(ddgURL); err == nil {
			if uddg := u.Query().Get("uddg"); uddg != "" {
				return uddg
			}
			// Try parsing as a relative URL
			if strings.HasPrefix(ddgURL, "//") {
				ddgURL = "https:" + ddgURL
			}
			if u, err := url.Parse(ddgURL); err == nil {
				if uddg := u.Query().Get("uddg"); uddg != "" {
					return uddg
				}
			}
		}
	}

	// If it's already a direct URL, return as-is
	if strings.HasPrefix(ddgURL, "http://") || strings.HasPrefix(ddgURL, "https://") {
		return ddgURL
	}

	// Handle protocol-relative URLs
	if strings.HasPrefix(ddgURL, "//") {
		return "https:" + ddgURL
	}

	return ddgURL
}
