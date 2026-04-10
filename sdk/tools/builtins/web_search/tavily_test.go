package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestTavilyProvider_Search(t *testing.T) {
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
	provider := NewTavilyProvider("test-api-key")
	provider.SetBaseURL(server.URL)
	tool := NewWebSearchTool(provider)

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

func TestTavilyProvider_HTTPError(t *testing.T) {
	// Create mock server that returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	provider := NewTavilyProvider("test-api-key")
	provider.SetBaseURL(server.URL)
	tool := NewWebSearchTool(provider)

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

func TestTavilyProvider_EmptyResults(t *testing.T) {
	// Create mock server that returns empty results
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := tavilyResponse{
			Results: []tavilyResult{},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewTavilyProvider("test-api-key")
	provider.SetBaseURL(server.URL)
	tool := NewWebSearchTool(provider)

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

func TestTavilyProvider_DefaultMaxResults(t *testing.T) {
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

	provider := NewTavilyProvider("test-api-key")
	provider.SetBaseURL(server.URL)
	tool := NewWebSearchTool(provider)

	// Execute without specifying max_results
	input := json.RawMessage(`{"query": "test"}`)
	_, _ = tool.Execute(context.Background(), input)

	if capturedMaxResults != 5 {
		t.Errorf("Expected default max_results=5, got %d", capturedMaxResults)
	}
}

func TestWebSearchTool_QueryFallback(t *testing.T) {
	// Create mock server that echoes the query back.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody tavilyRequest
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		response := tavilyResponse{
			Results: []tavilyResult{
				{Title: "Result", URL: "https://example.com", Content: reqBody.Query},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	tests := []struct {
		name      string
		input     string
		wantQuery string
		wantError bool
	}{
		{
			name:      "queries array",
			input:     `{"queries": ["test query"]}`,
			wantQuery: "test query",
		},
		{
			name:      "queries string",
			input:     `{"queries": "test query"}`,
			wantQuery: "test query",
		},
		{
			name:      "search_query string",
			input:     `{"search_query": "test query"}`,
			wantQuery: "test query",
		},
		{
			name:      "empty object still errors",
			input:     `{}`,
			wantError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewTavilyProvider("test-api-key")
			provider.SetBaseURL(server.URL)
			tool := NewWebSearchTool(provider)

			result, err := tool.Execute(context.Background(), json.RawMessage(tc.input))
			if err != nil {
				t.Fatalf("Execute() returned error: %v", err)
			}

			if tc.wantError {
				if !result.IsError {
					t.Error("Expected IsError=true")
				}
				if !strings.Contains(result.Content, "query parameter is required") {
					t.Errorf("Expected 'query parameter is required', got: %s", result.Content)
				}
				return
			}

			if result.IsError {
				t.Fatalf("Execute() returned IsError=true: %s", result.Content)
			}
			if !strings.Contains(result.Content, tc.wantQuery) {
				t.Errorf("Expected result to contain %q, got: %s", tc.wantQuery, result.Content)
			}
		})
	}
}

func TestTavilyProvider_RealSearch(t *testing.T) {
	apiKey := os.Getenv("TAVILY_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: TAVILY_API_KEY environment variable not set")
	}

	tool := NewWebSearchTool(NewTavilyProvider(apiKey))

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

func TestTavilyProvider_MissingAPIKey(t *testing.T) {
	tool := NewWebSearchTool(NewTavilyProvider("")) // Empty API key

	input := json.RawMessage(`{"query": "test search"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return IsError=true for missing API key")
	}
	if !strings.Contains(result.Content, "search failed:") {
		t.Errorf("Expected 'search failed:' wrapper, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "API key is not configured") {
		t.Errorf("Expected error message about missing API key, got: %s", result.Content)
	}
}
