package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/user/agent/internal/tools"
)

func TestWebSearchTool_Descriptor(t *testing.T) {
	tool := NewWebSearchTool("test-api-key")

	if tool.Name() != "web_search" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "web_search")
	}

	if tool.Description() != "Search the web using Tavily API" {
		t.Errorf("Description() = %q, want %q", tool.Description(), "Search the web using Tavily API")
	}

	// Verify schema is valid JSON
	schema := tool.InputSchema()
	var schemaMap map[string]any
	if err := json.Unmarshal(schema, &schemaMap); err != nil {
		t.Fatalf("InputSchema() is not valid JSON: %v", err)
	}

	// Check that query is required
	props, ok := schemaMap["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema missing properties")
	}
	if _, ok := props["query"]; !ok {
		t.Error("schema missing query property")
	}
	if _, ok := props["max_results"]; !ok {
		t.Error("schema missing max_results property")
	}

	required, ok := schemaMap["required"].([]any)
	if !ok {
		t.Fatal("schema missing required array")
	}
	found := false
	for _, r := range required {
		if r == "query" {
			found = true
			break
		}
	}
	if !found {
		t.Error("query not in required fields")
	}
}

func TestWebSearchTool_MissingQuery(t *testing.T) {
	tool := NewWebSearchTool("test-api-key")

	// Test with empty query
	input := json.RawMessage(`{"query": ""}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return IsError=true for missing query")
	}
	if !strings.Contains(result.Content, "query parameter is required") {
		t.Errorf("Expected error message about missing query, got: %s", result.Content)
	}

	// Test with no query field at all
	input2 := json.RawMessage(`{}`)
	result2, err := tool.Execute(context.Background(), input2)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if !result2.IsError {
		t.Error("Execute() should return IsError=true for missing query field")
	}
}

func TestWebSearchTool_MissingAPIKey(t *testing.T) {
	tool := NewWebSearchTool("") // Empty API key

	input := json.RawMessage(`{"query": "test search"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return IsError=true for missing API key")
	}
	if !strings.Contains(result.Content, "API key is not configured") {
		t.Errorf("Expected error message about missing API key, got: %s", result.Content)
	}
}

func TestWebSearchTool_MockServer(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST request, got %s", r.Method)
		}

		// Verify content type
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}

		// Parse request body
		var reqBody tavilyRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("Failed to decode request body: %v", err)
		}

		// Verify request fields
		if reqBody.APIKey != "test-api-key" {
			t.Errorf("Expected api_key 'test-api-key', got %s", reqBody.APIKey)
		}
		if reqBody.Query != "golang testing" {
			t.Errorf("Expected query 'golang testing', got %s", reqBody.Query)
		}
		if reqBody.SearchDepth != "basic" {
			t.Errorf("Expected search_depth 'basic', got %s", reqBody.SearchDepth)
		}

		// Return mock response
		response := tavilyResponse{
			Results: []tavilyResult{
				{
					Title:   "Go Testing",
					URL:     "https://go.dev/doc/testing",
					Content: "Go has built-in support for testing.",
				},
				{
					Title:   "Testing Best Practices",
					URL:     "https://example.com/testing",
					Content: "Learn about testing in Go.",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create tool with mock server URL
	tool := NewWebSearchTool("test-api-key")
	tool.SetBaseURL(server.URL)

	input := json.RawMessage(`{"query": "golang testing", "max_results": 5}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if result.IsError {
		t.Errorf("Execute() returned IsError=true: %s", result.Content)
	}

	// Verify output contains both results
	if !strings.Contains(result.Content, "Go Testing") {
		t.Error("Result missing first title")
	}
	if !strings.Contains(result.Content, "https://go.dev/doc/testing") {
		t.Error("Result missing first URL")
	}
	if !strings.Contains(result.Content, "Testing Best Practices") {
		t.Error("Result missing second title")
	}
}

func TestWebSearchTool_HTTPError(t *testing.T) {
	// Create mock server that returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	tool := NewWebSearchTool("test-api-key")
	tool.SetBaseURL(server.URL)

	input := json.RawMessage(`{"query": "test search"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return IsError=true for HTTP 500")
	}
	if !strings.Contains(result.Content, "HTTP 500") {
		t.Errorf("Expected HTTP 500 error, got: %s", result.Content)
	}
}

func TestWebSearchTool_EmptyResults(t *testing.T) {
	// Create mock server that returns empty results
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := tavilyResponse{
			Results: []tavilyResult{},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	tool := NewWebSearchTool("test-api-key")
	tool.SetBaseURL(server.URL)

	input := json.RawMessage(`{"query": "obscure nonexistent query"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if result.IsError {
		t.Errorf("Execute() should not return IsError=true for empty results")
	}
	if result.Content != "No results found" {
		t.Errorf("Expected 'No results found', got: %s", result.Content)
	}
}

func TestWebSearchTool_FormatResults(t *testing.T) {
	tool := NewWebSearchTool("test-api-key")

	results := []tavilyResult{
		{
			Title:   "First Result",
			URL:     "https://example.com/first",
			Content: "This is the first snippet.",
		},
		{
			Title:   "Second Result",
			URL:     "https://example.com/second",
			Content: "This is the second snippet.",
		},
	}

	output := tool.formatResults(results)

	// Verify format
	if !strings.Contains(output, "1. **First Result**") {
		t.Error("Missing formatted first result title")
	}
	if !strings.Contains(output, "URL: https://example.com/first") {
		t.Error("Missing first result URL")
	}
	if !strings.Contains(output, "Snippet: This is the first snippet.") {
		t.Error("Missing first result snippet")
	}
	if !strings.Contains(output, "2. **Second Result**") {
		t.Error("Missing formatted second result title")
	}
	if !strings.Contains(output, "URL: https://example.com/second") {
		t.Error("Missing second result URL")
	}
}

func TestWebSearchTool_DefaultMaxResults(t *testing.T) {
	var capturedMaxResults int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody tavilyRequest
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		capturedMaxResults = reqBody.MaxResults

		response := tavilyResponse{Results: []tavilyResult{}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	tool := NewWebSearchTool("test-api-key")
	tool.SetBaseURL(server.URL)

	// Execute without specifying max_results
	input := json.RawMessage(`{"query": "test"}`)
	_, _ = tool.Execute(context.Background(), input)

	if capturedMaxResults != 5 {
		t.Errorf("Expected default max_results=5, got %d", capturedMaxResults)
	}
}

func TestWebSearchTool_InvalidJSON(t *testing.T) {
	tool := NewWebSearchTool("test-api-key")

	input := json.RawMessage(`{invalid json}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return IsError=true for invalid JSON")
	}
	if !strings.Contains(result.Content, "failed to parse input") {
		t.Errorf("Expected parse error message, got: %s", result.Content)
	}
}

func TestWebSearchTool_DefaultPolicy(t *testing.T) {
	tool := NewWebSearchTool("test-api-key")
	if tool.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected DefaultPolicy() to return PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

func TestWebSearchTool_RealSearch(t *testing.T) {
	apiKey := os.Getenv("TAVILY_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: TAVILY_API_KEY environment variable not set")
	}

	tool := NewWebSearchTool(apiKey)

	input := json.RawMessage(`{"query": "golang programming language", "max_results": 3}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if result.IsError {
		t.Errorf("Execute() returned IsError=true: %s", result.Content)
	}

	// Verify we got some results
	if result.Content == "No results found" {
		t.Error("Expected results for 'golang programming language'")
	}

	// Verify output format
	if !strings.Contains(result.Content, "1. **") {
		t.Error("Result doesn't match expected format")
	}
}
