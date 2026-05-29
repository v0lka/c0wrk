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
	"github.com/v0lka/c0wrk/sdk/tools"
)

const toolWebfetchDescription = `Fetch a web page by URL and convert its HTML content to markdown for easy reading. Only HTTP and HTTPS URLs are supported. Markdown output is limited to 2MB, requests time out after 30 seconds, and up to 10 redirects are followed. Supports optional start_line/end_line parameters for paginated reading of large pages.`

// rawHTMLSafetyCapMultiplier is applied to MaxBodySize when reading raw HTML
// as a safety net against absurdly large pages. The real limit is enforced
// after Markdown conversion.
const rawHTMLSafetyCapMultiplier = 10

// WebFetchTool fetches web pages and converts HTML to markdown.
type WebFetchTool struct {
	*tools.BaseTool
	client *http.Client
	limits WebFetchLimits
}

// NewWebFetchTool creates a new WebFetchTool with specified limits.
func NewWebFetchTool(limits WebFetchLimits) *WebFetchTool {
	return NewWebFetchToolWithClient(limits, nil)
}

// NewWebFetchToolWithClient creates a new WebFetchTool with specified limits
// and an optional HTTP client. If client is nil, a default client is created.
func NewWebFetchToolWithClient(limits WebFetchLimits, client *http.Client) *WebFetchTool {
	schema := `{
		"type": "object",
		"properties": {
			"url": {
				"type": "string",
				"description": "The URL to fetch. Must be an HTTP or HTTPS URL."
			},
			"start_line": {
				"type": "integer",
				"description": "1-based line number to start reading from. If omitted, content is returned from the beginning."
			},
			"end_line": {
				"type": "integer",
				"description": "1-based line number to stop reading at (inclusive). If omitted, content is returned until the end (subject to size limits). Values beyond the content length are clamped automatically."
			}
		},
		"required": ["url"]
	}`

	if client == nil {
		client = &http.Client{Timeout: limits.Timeout}
	}
	// Always enforce redirect limit
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects (max 10)")
		}
		return nil
	}

	return &WebFetchTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "web_fetch",
			ToolDescription: toolWebfetchDescription,
			Schema:          json.RawMessage(schema),
			Policy:          tools.PolicyAlwaysAllow,
		},
		client: client,
		limits: limits,
	}
}

// webFetchInput represents the input parameters for web fetch.
type webFetchInput struct {
	URL       string `json:"url"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// Judge checks whether the target URL resolves to a private/reserved IP.
// Private addresses require user confirmation to prevent SSRF.
func (t *WebFetchTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	var params webFetchInput
	if err := json.Unmarshal(input, &params); err != nil || params.URL == "" {
		// Cannot determine URL; let Execute() handle validation.
		return true, "web fetch"
	}

	addr, private := resolveHostIsPrivate(ctx, params.URL)
	if private {
		return false, "URL resolves to private/reserved address " + addr
	}

	return true, "web fetch to public address"
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

	if params.StartLine < 0 {
		return tools.ToolResult{Content: fmt.Sprintf("validation error: start_line must be >= 1, got %d", params.StartLine), IsError: true}, nil
	}
	if params.EndLine < 0 {
		return tools.ToolResult{Content: fmt.Sprintf("validation error: end_line must be >= 1, got %d", params.EndLine), IsError: true}, nil
	}
	if params.StartLine > 0 && params.EndLine > 0 && params.StartLine > params.EndLine {
		return tools.ToolResult{Content: fmt.Sprintf("validation error: start_line (%d) must not exceed end_line (%d)", params.StartLine, params.EndLine), IsError: true}, nil
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

	// Split markdown into lines for line-range support and enhanced truncation messages
	allLines := strings.Split(markdown, "\n")
	totalLines := len(allLines)

	// Determine if line range was requested
	if params.StartLine > 0 || params.EndLine > 0 {
		startLine := params.StartLine
		endLine := params.EndLine

		if startLine <= 0 {
			startLine = 1
		}
		if endLine <= 0 {
			endLine = totalLines
		}
		if startLine > totalLines {
			startLine = totalLines
		}
		if endLine > totalLines {
			endLine = totalLines
		}
		if startLine < 1 {
			startLine = 1
		}

		selectedLines := allLines[startLine-1 : endLine]
		content := strings.Join(selectedLines, "\n")

		// Check byte limit
		if t.limits.MaxBodySize > 0 && len(content) > t.limits.MaxBodySize {
			content = content[:t.limits.MaxBodySize] + "\n\n...(content truncated to configured limit)"
		}

		// Build header
		header := fmt.Sprintf("[Lines %d-%d of %d | %d bytes]\n", startLine, endLine, totalLines, len(content))

		// Add continuation hint if more lines remain
		if endLine < totalLines {
			content = header + content + fmt.Sprintf("\n[Use start_line=%d to continue reading]", endLine+1)
		} else {
			content = header + content
		}

		return tools.ToolResult{Content: content, IsError: false}, nil
	}

	// No line range — default behavior with enhanced truncation message
	if t.limits.MaxBodySize > 0 && len(markdown) > t.limits.MaxBodySize {
		markdown = markdown[:t.limits.MaxBodySize] + fmt.Sprintf("\n\n...(content truncated to %d bytes; original content has %d lines — use start_line/end_line to read specific sections)", t.limits.MaxBodySize, totalLines)
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

	// Safety cap on raw HTML to protect memory; the real limit is enforced
	// on the Markdown output after conversion.
	safetyCap := int64(t.limits.MaxBodySize) * rawHTMLSafetyCapMultiplier
	body, err := io.ReadAll(io.LimitReader(resp.Body, safetyCap))
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
