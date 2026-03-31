package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/user/agent/internal/tools"
)

const toolWebfetchDescription = "Fetch a web page and convert HTML to markdown. Optionally extract specific content using a prompt."

// LLMSummarizer is a function type for optional LLM-based content extraction.
// It allows the tool to use LLM summarization without importing internal/core or internal/llm.
type LLMSummarizer func(ctx context.Context, content string, prompt string) (string, error)

// WebFetchTool fetches web pages and converts HTML to markdown.
type WebFetchTool struct {
	summarizer LLMSummarizer
	client     *http.Client
}

// NewWebFetchTool creates a new WebFetchTool with an optional summarizer.
func NewWebFetchTool(summarizer LLMSummarizer) *WebFetchTool {
	return &WebFetchTool{
		summarizer: summarizer,
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects (max 10)")
				}
				return nil
			},
		},
	}
}

// webFetchInput represents the input parameters for web fetch.
type webFetchInput struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

// Name returns the tool name.
func (t *WebFetchTool) Name() string {
	return "web_fetch"
}

// Description returns the tool description.
func (t *WebFetchTool) Description() string {
	return toolWebfetchDescription
}

// InputSchema returns the JSON schema for the tool input.
func (t *WebFetchTool) InputSchema() json.RawMessage {
	schema := `{
		"type": "object",
		"properties": {
			"url": {
				"type": "string",
				"description": "URL to fetch"
			},
			"prompt": {
				"type": "string",
				"description": "Optional extraction prompt to filter/summarize the content"
			}
		},
		"required": ["url"]
	}`
	return json.RawMessage(schema)
}

// DefaultPolicy returns PolicyAlwaysAllow because web fetch only reads external content.
func (t *WebFetchTool) DefaultPolicy() tools.ToolPolicy {
	return tools.PolicyAlwaysAllow
}

// Execute fetches the URL and returns markdown content.
func (t *WebFetchTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params webFetchInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to parse input: %v", err), IsError: true}, nil
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
	markdown, err := t.htmlToMarkdown(content)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to convert HTML to markdown: %v", err), IsError: true}, nil
	}

	// If prompt is provided and summarizer is available, use it for extraction
	if params.Prompt != "" && t.summarizer != nil {
		extracted, err := t.summarizer(ctx, markdown, params.Prompt)
		if err != nil {
			// Return the markdown content but note the extraction failure
			return tools.ToolResult{
				Content: fmt.Sprintf("Note: Extraction failed (%v). Full content:\n\n%s", err, markdown),
				IsError: false,
			}, nil
		}
		return tools.ToolResult{Content: extracted, IsError: false}, nil
	}

	return tools.ToolResult{Content: markdown, IsError: false}, nil
}

// fetchPage performs HTTP GET and returns the response body.
func (t *WebFetchTool) fetchPage(ctx context.Context, targetURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set reasonable User-Agent
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AgentBot/1.0)")
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

	// Limit response body to 100KB
	const maxBodySize = 100 * 1024
	limitedReader := io.LimitReader(resp.Body, maxBodySize)

	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	return string(body), nil
}

// htmlToMarkdown converts HTML content to Markdown.
func (t *WebFetchTool) htmlToMarkdown(html string) (string, error) {
	converter := md.NewConverter("", true, nil)

	markdown, err := converter.ConvertString(html)
	if err != nil {
		return "", fmt.Errorf("conversion failed: %w", err)
	}

	// Trim excessive whitespace
	markdown = strings.TrimSpace(markdown)

	return markdown, nil
}
