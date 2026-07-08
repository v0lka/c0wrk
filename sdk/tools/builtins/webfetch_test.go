package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/tools"
)

// passthroughRoundTripper wraps an http.RoundTripper without being an
// *http.Transport, so NewWebFetchToolWithClient leaves it as-is (no SSRF-safe
// dialing). Used in tests to allow connections to loopback httptest servers.
type passthroughRoundTripper struct {
	rt http.RoundTripper
}

func (t *passthroughRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.rt.RoundTrip(req)
}

// newTestWebFetchTool creates a WebFetchTool with a client whose Transport is
// a passthroughRoundTripper (not *http.Transport), so connections to loopback
// addresses used by httptest.NewServer are not blocked by the SSRF-safe dialer.
// The redirect protection (CheckRedirect) is still applied. Use NewWebFetchTool
// directly when testing SSRF dial-time enforcement.
func newTestWebFetchTool(limits WebFetchLimits) *WebFetchTool {
	client := &http.Client{
		Timeout:   limits.Timeout,
		Transport: &passthroughRoundTripper{rt: http.DefaultTransport},
	}
	return NewWebFetchToolWithClient(limits, client)
}

func TestWebFetchTool_Descriptor(t *testing.T) {
	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})

	// Verify name
	if name := tool.Name(); name != "web_fetch" {
		t.Errorf("expected name 'web_fetch', got '%s'", name)
	}

	// Verify description is not empty
	if desc := tool.Description(); desc == "" {
		t.Error("expected non-empty description")
	}

	// Verify schema is valid JSON
	schema := tool.InputSchema()
	var schemaMap map[string]any
	if err := json.Unmarshal(schema, &schemaMap); err != nil {
		t.Errorf("expected valid JSON schema, got error: %v", err)
	}

	// Verify schema has required structure
	if schemaMap["type"] != "object" {
		t.Error("expected schema type to be 'object'")
	}

	props, ok := schemaMap["properties"].(map[string]any)
	if !ok {
		t.Error("expected schema to have properties")
	}

	if _, ok := props["url"]; !ok {
		t.Error("expected schema to have 'url' property")
	}

	required, ok := schemaMap["required"].([]any)
	if !ok {
		t.Error("expected schema to have required array")
	}

	hasURL := false
	for _, r := range required {
		if r == "url" {
			hasURL = true
			break
		}
	}
	if !hasURL {
		t.Error("expected 'url' to be in required fields")
	}
}

func TestWebFetchTool_ImplementsToolInterface(t *testing.T) {
	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})

	// Verify it implements the Tool interface
	var _ tools.Tool = tool
}

func TestWebFetchTool_MissingURL(t *testing.T) {
	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	ctx := context.Background()

	// Test with empty input
	input := json.RawMessage(`{}`)
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing URL")
	}
	if !strings.Contains(result.Content, "url") {
		t.Errorf("expected error message to mention 'url', got: %s", result.Content)
	}

	// Test with empty URL
	input = json.RawMessage(`{"url": ""}`)
	result, err = tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for empty URL")
	}
}

func TestWebFetchTool_InvalidURL(t *testing.T) {
	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	ctx := context.Background()

	testCases := []struct {
		name  string
		url   string
		check string
	}{
		{"ftp scheme", "ftp://example.com/file", "http"},
		{"file scheme", "file:///etc/passwd", "http"},
		{"javascript scheme", "javascript:alert(1)", "http"},
		{"no scheme", "example.com", "http"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			input, _ := json.Marshal(map[string]string{"url": tc.url})
			result, err := tool.Execute(ctx, input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.IsError {
				t.Error("expected IsError=true for invalid URL")
			}
			if !strings.Contains(result.Content, tc.check) {
				t.Errorf("expected error message to mention '%s', got: %s", tc.check, result.Content)
			}
		})
	}
}

func TestWebFetchTool_InvalidJSON(t *testing.T) {
	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	ctx := context.Background()

	input := json.RawMessage(`{invalid json`)
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid JSON")
	}
	if !strings.Contains(result.Content, "parse") {
		t.Errorf("expected error message to mention 'parse', got: %s", result.Content)
	}
}

func TestWebFetchTool_HTTPServer(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<h1>Hello World</h1>
<p>This is a test paragraph.</p>
<a href="https://example.com">Example Link</a>
</body>
</html>`))
	}))
	defer server.Close()

	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}

	// Verify markdown conversion
	if !strings.Contains(result.Content, "Hello World") {
		t.Errorf("expected markdown to contain 'Hello World', got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "test paragraph") {
		t.Errorf("expected markdown to contain 'test paragraph', got: %s", result.Content)
	}
}

func TestWebFetchTool_HTTPError(t *testing.T) {
	// Create a test server that returns 404
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for HTTP 404")
	}
	if !strings.Contains(result.Content, "404") {
		t.Errorf("expected error to contain '404', got: %s", result.Content)
	}
}

func TestWebFetchTool_BodySizeLimit(t *testing.T) {
	// Create a test server that returns large HTML content.
	// The raw HTML is 3 MB of repeated paragraphs; after Markdown conversion
	// the output will still exceed MaxBodySize so truncation must kick in.
	paragraph := "<p>" + strings.Repeat("x", 1000) + "</p>\n"
	largeHTML := "<html><body>" + strings.Repeat(paragraph, 3000) + "</body></html>"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(largeHTML))
	}))
	defer server.Close()

	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}

	// No per-tool byte truncation; central layer handles it. Full content should be returned.
	if result.Content == "" {
		t.Error("expected non-empty content since no per-tool truncation applies")
	}
	if strings.Contains(result.Content, "...(content truncated to") {
		t.Error("did not expect truncation notice in output")
	}
}

func TestWebFetchTool_TruncationAppliesToMarkdownNotHTML(t *testing.T) {
	// Verify that the size limit is applied to the Markdown output, not the raw HTML.
	// We craft HTML that is large in raw form but produces small Markdown.
	// If truncation were applied to the raw HTML, the Markdown would be broken/incomplete.

	// Build HTML with lots of attributes but little visible text.
	// The raw HTML is large (~200 KB) but its Markdown is small (~10 KB).
	var b strings.Builder
	b.WriteString("<html><body><article>")
	for i := range 500 {
		// Each element adds ~400 bytes of HTML but only a short text line in Markdown.
		b.WriteString(`<p data-x="` + strings.Repeat("a", 350) + `">`)
		fmt.Fprintf(&b, "Line %d", i)
		b.WriteString("</p>\n")
	}
	b.WriteString("<p>FINAL-SENTINEL</p>")
	b.WriteString("</article></body></html>")
	bigHTML := b.String()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(bigHTML))
	}))
	defer server.Close()

	limits := WebFetchLimits{Timeout: 30 * time.Second}
	tool := newTestWebFetchTool(limits)
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}

	// The sentinel at the end of the HTML must survive because Markdown is small
	// enough to fit within MaxBodySize. If truncation were on raw HTML, the
	// sentinel would be lost.
	if !strings.Contains(result.Content, "FINAL-SENTINEL") {
		t.Error("expected FINAL-SENTINEL in output — truncation was applied to raw HTML instead of Markdown")
	}

	// The Markdown output should be well under the limit, so no truncation notice.
	if strings.Contains(result.Content, "...(content truncated to") {
		t.Error("did not expect truncation notice — Markdown output should fit within limit")
	}
}

func TestWebFetchTool_FetchRealPage(t *testing.T) {
	// Skip if running in CI or no network
	t.Skip("Skipping integration test - requires network access")

	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	ctx := context.Background()

	// Use a stable URL
	input, _ := json.Marshal(map[string]string{"url": "https://example.com"})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Example Domain") {
		t.Errorf("expected content from example.com, got: %s", result.Content)
	}
}

func TestWebFetchTool_DefaultPolicy(t *testing.T) {
	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	if tool.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected DefaultPolicy() to return PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

func TestWebFetchTool_ContextCancellation(t *testing.T) {
	// Create a test server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if context was cancelled
		select {
		case <-r.Context().Done():
			return
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html><body>OK</body></html>`))
		}
	}))
	defer server.Close()

	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	input, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return error due to cancelled context
	if !result.IsError {
		t.Error("expected error for cancelled context")
	}
}

func TestWebFetchTool_ReadabilityExtraction(t *testing.T) {
	// Create a test server that returns realistic article HTML with nav, sidebar, and article content
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
	<title>Test Article Page</title>
</head>
<body>
	<nav>
		<a href="/">Home</a>
		<a href="/about">About</a>
		<a href="/contact">Contact</a>
	</nav>
	<div class="sidebar">
		<h3>Related Articles</h3>
		<ul>
			<li><a href="/article1">Article 1</a></li>
			<li><a href="/article2">Article 2</a></li>
			<li><a href="/article3">Article 3</a></li>
		</ul>
	</div>
	<article>
		<h1>Main Article Title</h1>
		<p>This is the main article content that should be extracted by readability. It contains important information about the topic.</p>
		<p>Here is another paragraph with more details about the subject matter.</p>
		<h2>Subsection</h2>
		<p>This subsection provides additional context and information.</p>
	</article>
	<footer>
		<p>Copyright 2024 Test Site</p>
		<p>Privacy Policy | Terms of Service</p>
	</footer>
</body>
</html>`))
	}))
	defer server.Close()

	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}

	// Verify that the main article content is present
	if !strings.Contains(result.Content, "Main Article Title") {
		t.Errorf("expected markdown to contain 'Main Article Title', got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "main article content") {
		t.Errorf("expected markdown to contain 'main article content', got: %s", result.Content)
	}

	// Verify that navigation/sidebar content is not present (extracted by readability)
	if strings.Contains(result.Content, "Related Articles") {
		t.Errorf("expected sidebar content to be filtered out, but found 'Related Articles' in: %s", result.Content)
	}
	if strings.Contains(result.Content, "Privacy Policy") {
		t.Errorf("expected footer content to be filtered out, but found 'Privacy Policy' in: %s", result.Content)
	}
}

// multiLineServer creates a test server returning HTML with known multi-line markdown output.
// Each <p> becomes a separate line in markdown.
func multiLineServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><body>
<p>Line 1</p>
<p>Line 2</p>
<p>Line 3</p>
<p>Line 4</p>
<p>Line 5</p>
</body></html>`))
	}))
}

func TestWebFetchTool_StartLineEndLine(t *testing.T) {
	server := multiLineServer()
	defer server.Close()

	limits := WebFetchLimits{Timeout: 30 * time.Second}
	tool := newTestWebFetchTool(limits)
	ctx := context.Background()

	input, _ := json.Marshal(map[string]any{"url": server.URL, "start_line": 2, "end_line": 4})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}

	// Should have a header with line range
	if !strings.Contains(result.Content, "[Lines 2-4 of") {
		t.Errorf("expected line range header, got: %s", result.Content)
	}
	// Should have continuation hint since we didn't read to end
	if !strings.Contains(result.Content, "[Use start_line=") {
		t.Errorf("expected continuation hint, got: %s", result.Content)
	}
}

func TestWebFetchTool_StartLineOnly(t *testing.T) {
	server := multiLineServer()
	defer server.Close()

	limits := WebFetchLimits{Timeout: 30 * time.Second}
	tool := newTestWebFetchTool(limits)
	ctx := context.Background()

	input, _ := json.Marshal(map[string]any{"url": server.URL, "start_line": 3})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}

	// Should start from line 3 to end
	if !strings.Contains(result.Content, "[Lines 3-") {
		t.Errorf("expected lines starting from 3, got: %s", result.Content)
	}
	// No continuation hint since we read to end
	if strings.Contains(result.Content, "[Use start_line=") {
		t.Errorf("expected no continuation hint when reading to end, got: %s", result.Content)
	}
}

func TestWebFetchTool_EndLineOnly(t *testing.T) {
	server := multiLineServer()
	defer server.Close()

	limits := WebFetchLimits{Timeout: 30 * time.Second}
	tool := newTestWebFetchTool(limits)
	ctx := context.Background()

	input, _ := json.Marshal(map[string]any{"url": server.URL, "end_line": 3})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}

	// Should start from line 1 to line 3
	if !strings.Contains(result.Content, "[Lines 1-3 of") {
		t.Errorf("expected lines 1-3, got: %s", result.Content)
	}
	// Should have continuation hint
	if !strings.Contains(result.Content, "[Use start_line=4") {
		t.Errorf("expected continuation hint to line 4, got: %s", result.Content)
	}
}

func TestWebFetchTool_LineRangeOutOfBounds(t *testing.T) {
	server := multiLineServer()
	defer server.Close()

	limits := WebFetchLimits{Timeout: 30 * time.Second}
	tool := newTestWebFetchTool(limits)
	ctx := context.Background()

	// Request lines way beyond actual content
	input, _ := json.Marshal(map[string]any{"url": server.URL, "start_line": 100, "end_line": 200})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}

	// Should be clamped — header should show valid range
	if !strings.Contains(result.Content, "[Lines") {
		t.Errorf("expected line header, got: %s", result.Content)
	}
}

func TestWebFetchTool_TruncationShowsLineCount(t *testing.T) {
	// Create a server with content that exceeds a small MaxBodySize
	var b strings.Builder
	b.WriteString("<html><body>")
	for i := range 50 {
		fmt.Fprintf(&b, "<p>Paragraph number %d with some extra text to bulk it up</p>\n", i)
	}
	b.WriteString("</body></html>")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(b.String()))
	}))
	defer server.Close()

	limits := WebFetchLimits{Timeout: 30 * time.Second}
	tool := newTestWebFetchTool(limits)
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}

	// No per-tool truncation; central layer handles it. Content returned fully.
	if strings.Contains(result.Content, "content truncated") {
		t.Errorf("did not expect truncation notice, got: %s", result.Content)
	}
}

func TestWebFetchTool_LineRangeWithTruncation(t *testing.T) {
	// Create a server with long lines that exceed small MaxBodySize when a range is requested
	var b strings.Builder
	b.WriteString("<html><body>")
	for i := range 10 {
		fmt.Fprintf(&b, "<p>%s line %d</p>\n", strings.Repeat("abcdefghij", 5), i)
	}
	b.WriteString("</body></html>")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(b.String()))
	}))
	defer server.Close()

	limits := WebFetchLimits{Timeout: 30 * time.Second}
	tool := newTestWebFetchTool(limits)
	ctx := context.Background()

	input, _ := json.Marshal(map[string]any{"url": server.URL, "start_line": 1, "end_line": 5})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}

	// No per-tool truncation in line range; central layer handles it.
	if strings.Contains(result.Content, "content truncated") {
		t.Errorf("did not expect truncation notice in line range result, got: %s", result.Content)
	}
	// Should still have the header
	if !strings.Contains(result.Content, "[Lines") {
		t.Errorf("expected line header, got: %s", result.Content)
	}
}

func TestWebFetchTool_ReadabilityFallback(t *testing.T) {
	// Create a test server that returns non-article HTML (e.g., a simple HTML page without article structure)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Simple Page</title></head>
<body>
	<h1>Welcome</h1>
	<p>This is a simple page without article markup.</p>
	<p>It should still be converted to markdown via fallback.</p>
</body>
</html>`))
	}))
	defer server.Close()

	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}

	// Verify that the content is still returned via fallback
	if !strings.Contains(result.Content, "Welcome") {
		t.Errorf("expected markdown to contain 'Welcome', got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "simple page") {
		t.Errorf("expected markdown to contain 'simple page', got: %s", result.Content)
	}
}

func TestWebFetchTool_Judge_PrivateIP(t *testing.T) {
	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})

	t.Run("loopback is blocked", func(t *testing.T) {
		input, _ := json.Marshal(map[string]string{"url": "http://127.0.0.1/secret"})
		allow, reasoning := tool.Judge(context.Background(), input)
		if allow {
			t.Error("expected Judge to deny request to loopback address")
		}
		if !strings.Contains(reasoning, "private") {
			t.Errorf("expected reasoning to mention private, got: %s", reasoning)
		}
	})

	t.Run("private RFC1918 is blocked", func(t *testing.T) {
		input, _ := json.Marshal(map[string]string{"url": "http://192.168.1.1/admin"})
		allow, reasoning := tool.Judge(context.Background(), input)
		if allow {
			t.Error("expected Judge to deny request to private address")
		}
		if reasoning == "" {
			t.Error("expected non-empty reasoning")
		}
	})

	t.Run("public address is allowed", func(t *testing.T) {
		input, _ := json.Marshal(map[string]string{"url": "https://example.com"})
		allow, _ := tool.Judge(context.Background(), input)
		if !allow {
			t.Error("expected Judge to allow request to public address")
		}
	})

	t.Run("invalid input escalates to confirmation", func(t *testing.T) {
		allow, reason := tool.Judge(context.Background(), json.RawMessage(`{invalid`))
		if allow {
			t.Error("expected Judge to escalate on invalid input (fail closed)")
		}
		if reason != "cannot determine target URL" {
			t.Errorf("unexpected reason: %q", reason)
		}
	})
}

// TestWebFetchTool_RedirectToPrivateRefused verifies that an HTTP redirect to a
// private/reserved address is refused by CheckRedirect before any connection is
// made to the private target. This closes the SSRF redirect bypass: Judge only
// inspects the initial URL, so without a redirect-target check a public URL
// could 302 to 169.254.169.254 (cloud metadata) or localhost.
func TestWebFetchTool_RedirectToPrivateRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer server.Close()

	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true for redirect to private address, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "private") && !strings.Contains(result.Content, "refused") {
		t.Errorf("expected error to mention private/refused, got: %s", result.Content)
	}
}

// TestWebFetchTool_BodySizeCapExceeded verifies that a response body larger
// than maxWebFetchBodyBytes is rejected (fail closed) instead of being buffered
// entirely into memory.
func TestWebFetchTool_BodySizeCapExceeded(t *testing.T) {
	oversized := strings.Repeat("x", maxWebFetchBodyBytes+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(oversized))
	}))
	defer server.Close()

	tool := newTestWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true for oversized body, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "exceeds") {
		t.Errorf("expected error to mention size limit, got: %s", result.Content)
	}
}

// TestWebFetchTool_DialTimeSSRFRefused verifies that a connection to a
// private/reserved IP is refused at TCP dial time by the SSRF-safe transport,
// closing the DNS rebinding TOCTOU window. Judge's pre-flight check can be
// bypassed if a hostname resolves to a public IP during Judge and a private IP
// during the actual dial; the dialer's Control function inspects the resolved
// address at connect time and rejects private ranges.
//
// This test uses NewWebFetchTool (the production constructor with the
// SSRF-safe transport) rather than newTestWebFetchTool (which bypasses the
// dial-time check for loopback test servers).
func TestWebFetchTool_DialTimeSSRFRefused(t *testing.T) {
	tool := NewWebFetchTool(WebFetchLimits{Timeout: 30 * time.Second})
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{"url": "http://127.0.0.1:1/"})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true for private IP at dial time, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "private/reserved address refused") {
		t.Errorf("expected error to mention 'private/reserved address refused', got: %s", result.Content)
	}
}
