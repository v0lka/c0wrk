package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/go-shiori/go-readability"
	"github.com/v0lka/sp4rk/tools"
)

const toolWebfetchDescription = `Fetch a web page by URL and convert its HTML content to markdown for easy reading. Only HTTP and HTTPS URLs are supported. requests time out after 30 seconds, and up to 10 redirects are followed. Supports optional start_line/end_line parameters for paginated reading of large pages.`

// maxWebFetchBodyBytes caps the response body buffered during fetch to bound
// memory consumption. The centralized truncation layer only limits what
// reaches the model; without this cap a malicious server could exhaust memory
// with an unbounded response before any output truncation runs. Fetches that
// exceed the cap fail closed rather than silently truncating (which would
// yield broken HTML/markdown).
const maxWebFetchBodyBytes = 10 * 1024 * 1024 // 10 MB

// WebFetchTool fetches web pages and converts HTML to markdown.
type WebFetchTool struct {
	*tools.BaseTool
	client *http.Client
	limits WebFetchLimits
}

// newSSRFSafeTransport clones base (or creates a new transport if base is nil)
// and configures its DialContext with a net.Dialer whose Control function
// rejects connections to private/reserved IP addresses at TCP connect time.
// This closes the DNS rebinding TOCTOU window between Judge's pre-flight
// resolution and the actual dial: a host could resolve to a public IP during
// Judge and a private IP during the dial.
func newSSRFSafeTransport(base *http.Transport) *http.Transport {
	var t *http.Transport
	if base != nil {
		t = base.Clone()
	} else {
		t = &http.Transport{}
	}

	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   ssrfSafeControl,
	}
	t.DialContext = dialer.DialContext

	return t
}

// NewWebFetchTool creates a new WebFetchTool with specified limits.
func NewWebFetchTool(limits WebFetchLimits) *WebFetchTool {
	return NewWebFetchToolWithClient(limits, nil)
}

// NewWebFetchToolWithClient creates a new WebFetchTool with specified limits
// and an optional HTTP client. If client is nil, a default client with an
// SSRF-safe transport is created. If client is provided and its Transport is
// an *http.Transport, the transport is cloned and wrapped with SSRF-safe
// dialing; otherwise the client is left as-is (e.g. custom RoundTripper or
// nil transport using http.DefaultTransport).
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
		client = &http.Client{
			Timeout:   limits.Timeout,
			Transport: newSSRFSafeTransport(nil),
		}
	} else {
		// Never mutate the caller's client: make a shallow copy and configure
		// the copy (Transport, CheckRedirect) for exclusive use by this tool.
		c := *client
		client = &c
		if transport, ok := client.Transport.(*http.Transport); ok {
			// Wrap the caller's transport with SSRF-safe dialing. Clone preserves
			// existing settings (proxy, TLS config, etc.) while adding the
			// dial-time private-IP check.
			client.Transport = newSSRFSafeTransport(transport)
		} else if client.Transport == nil {
			// nil Transport means http.DefaultTransport will be used. Wrap it
			// with SSRF-safe dialing so the protection applies even when the
			// caller didn't set an explicit transport.
			if defaultT, ok := http.DefaultTransport.(*http.Transport); ok {
				client.Transport = newSSRFSafeTransport(defaultT)
			}
		}
	}
	// If client is provided but its Transport is not an *http.Transport and
	// not nil (e.g. a custom RoundTripper), leave it as-is.
	// Always enforce redirect limit and SSRF protection on redirect targets.
	// The initial URL is validated by Judge, but an HTTP redirect could
	// otherwise bypass it (e.g. a public URL 302-ing to 169.254.169.254).
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects (max 10)")
		}
		addr, private, chkErr := resolveHostIsPrivate(req.Context(), req.URL.String())
		if chkErr != nil {
			return fmt.Errorf("SSRF check on redirect target failed: %w", chkErr)
		}
		if private {
			return fmt.Errorf("redirect to private/reserved address refused: %s", addr)
		}
		return nil
	}

	return &WebFetchTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "web_fetch",
			ToolDescription: toolWebfetchDescription,
			Schema:          json.RawMessage(schema),
			Policy:          tools.PolicyAlwaysAllow,
			Untrusted:       true,
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
		// Cannot determine URL — fail closed and escalate to confirmation.
		return false, "cannot determine target URL"
	}

	addr, private, initErr := resolveHostIsPrivate(ctx, params.URL)
	if initErr != nil {
		// CIDR list failed to initialize — SSRF protection is unavailable.
		// Fail-safe: require user confirmation for all web fetches.
		return false, fmt.Sprintf("SSRF protection degraded: %v", initErr)
	}
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

	// No line range — return full markdown; centralized caching+truncation layer handles output size
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

	// Cap the response body to bound memory use; the centralized
	// caching+truncation layer only limits what reaches the model, not the
	// bytes buffered during fetch. Fail closed when the cap is exceeded
	// rather than silently truncating (which would yield broken HTML).
	limited := io.LimitReader(resp.Body, maxWebFetchBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}
	if len(body) > maxWebFetchBodyBytes {
		return "", fmt.Errorf("response body exceeds %d byte limit", maxWebFetchBodyBytes)
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
