package builtins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/user/agent/sdk/tools"
)

func TestWebFetchTool_Descriptor(t *testing.T) {
	tool := NewWebFetchTool()

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
	tool := NewWebFetchTool()

	// Verify it implements the Tool interface
	var _ tools.Tool = tool
}

func TestWebFetchTool_MissingURL(t *testing.T) {
	tool := NewWebFetchTool()
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
	tool := NewWebFetchTool()
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
	tool := NewWebFetchTool()
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

	tool := NewWebFetchTool()
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

	tool := NewWebFetchTool()
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
	// Create a test server that returns large content
	largeContent := strings.Repeat("x", 3*1024*1024) // 3MB
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(largeContent))
	}))
	defer server.Close()

	tool := NewWebFetchTool()
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}

	// Content should be truncated to ~2MB
	if len(result.Content) > 2300000 { // Allow some overhead for markdown conversion (~2.2MB)
		t.Errorf("expected content to be truncated to ~2MB, got %d bytes", len(result.Content))
	}
}

func TestWebFetchTool_FetchRealPage(t *testing.T) {
	// Skip if running in CI or no network
	t.Skip("Skipping integration test - requires network access")

	tool := NewWebFetchTool()
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
	tool := NewWebFetchTool()
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

	tool := NewWebFetchTool()

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

	tool := NewWebFetchTool()
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

	tool := NewWebFetchTool()
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
