package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/go-shiori/go-readability"
	"github.com/user/agent/sdk/tools"
)

const toolWebfetchDescription = `Fetch a web page by URL and convert its HTML content to markdown for easy reading. Only HTTP and HTTPS URLs are supported. Response bodies are limited to 2MB, requests time out after 30 seconds, and up to 10 redirects are followed.`

// WebFetchTool fetches web pages and converts HTML to markdown.
type WebFetchTool struct {
	*tools.BaseTool
	client *http.Client
	limits WebFetchLimits
}

// NewWebFetchTool creates a new WebFetchTool with default limits.
func NewWebFetchTool() *WebFetchTool {
	return NewWebFetchToolWithLimits(DefaultWebFetchLimits())
}

// NewWebFetchToolWithLimits creates a new WebFetchTool with specified limits.
func NewWebFetchToolWithLimits(limits WebFetchLimits) *WebFetchTool {
	schema := `{
		"type": "object",
		"properties": {
			"url": {
				"type": "string",
				"description": "The URL to fetch. Must be an HTTP or HTTPS URL."
			}
		},
		"required": ["url"]
	}`
	return &WebFetchTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "web_fetch",
			ToolDescription: toolWebfetchDescription,
			Schema:          json.RawMessage(schema),
			Policy:          tools.PolicyAlwaysAllow,
		},
		client: &http.Client{
			Timeout: limits.Timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("too many redirects (max 10)")
				}
				return nil
			},
		},
		limits: limits,
	}
}

// webFetchInput represents the input parameters for web fetch.
type webFetchInput struct {
	URL string `json:"url"`
}

// Execute fetches the URL and returns markdown content.
func (t *WebFetchTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params webFetchInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	// Validate URL
	if params.URL == "" {
		return tools.ToolResult{Content: "url parameter is required", IsError: true}, nil
	}

	parsedURL, err := url.Parse(params.URL)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("invalid URL: %v", err), IsError: true}, nil
	}

	// Only allow HTTP and HTTPS
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return tools.ToolResult{Content: "only http and https URLs are supported", IsError: true}, nil
	}

	// Fetch the page
	content, err := t.fetchPage(ctx, params.URL)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to fetch URL: %v", err), IsError: true}, nil
	}

	// Convert HTML to Markdown
	markdown, err := t.htmlToMarkdown(content, params.URL)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to convert HTML to markdown: %v", err), IsError: true}, nil
	}

	return tools.ToolResult{Content: markdown, IsError: false}, nil
}

// fetchPage performs HTTP GET and returns the response body.
func (t *WebFetchTool) fetchPage(ctx context.Context, targetURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set reasonable User-Agent
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	// Limit response body to configured max size
	limitedReader := io.LimitReader(resp.Body, int64(t.limits.MaxBodySize))

	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	return string(body), nil
}

// htmlToMarkdown converts HTML content to Markdown.
// It first attempts to extract the main article content using readability,
// then converts the extracted HTML to markdown.
func (t *WebFetchTool) htmlToMarkdown(htmlContent, pageURL string) (string, error) {
	// Parse the URL for readability
	parsedURL, err := url.Parse(pageURL)
	if err != nil {
		// If URL parsing fails, fall back to converting full HTML
		return t.convertHTMLToMarkdown(htmlContent)
	}

	// Try to extract article content using readability
	article, err := readability.FromReader(strings.NewReader(htmlContent), parsedURL)
	if err == nil && len(article.Content) > 100 {
		// Readability succeeded and produced meaningful content
		// article.Content contains the extracted HTML
		return t.convertHTMLToMarkdown(article.Content)
	}

	// Fall back to converting the full HTML
	return t.convertHTMLToMarkdown(htmlContent)
}

// convertHTMLToMarkdown performs the actual HTML to Markdown conversion.
func (t *WebFetchTool) convertHTMLToMarkdown(html string) (string, error) {
	converter := md.NewConverter("", true, nil)

	markdown, err := converter.ConvertString(html)
	if err != nil {
		return "", fmt.Errorf("conversion failed: %w", err)
	}

	// Trim excessive whitespace
	markdown = strings.TrimSpace(markdown)

	return markdown, nil
}
