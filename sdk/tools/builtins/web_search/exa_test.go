package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExaProvider_Search(t *testing.T) {
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

		// Verify API key header
		if r.Header.Get("x-api-key") != "test-api-key" {
			t.Errorf("Expected x-api-key header 'test-api-key', got %s", r.Header.Get("x-api-key"))
		}

		// Parse request body
		var reqBody exaRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("Failed to decode request body: %v", err)
		}

		// Verify request fields
		if reqBody.Query != "golang testing" {
			t.Errorf("Expected query 'golang testing', got %s", reqBody.Query)
		}
		if reqBody.NumResults != 5 {
			t.Errorf("Expected numResults 5, got %d", reqBody.NumResults)
		}

		// Return mock response
		response := exaResponse{
			Results: []exaResult{
				{
					Title:      "Go Testing",
					URL:        "https://go.dev/doc/testing",
					Highlights: []string{"Go has built-in support for testing."},
				},
				{
					Title:      "Testing Best Practices",
					URL:        "https://example.com/testing",
					Highlights: []string{"Learn about testing in Go."},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create provider with mock server URL
	provider := NewExaProviderWithClient("test-api-key", 30*time.Second, nil)
	provider.SetBaseURL(server.URL)

	results, err := provider.Search(context.Background(), "golang testing", 5)
	if err != nil {
		t.Fatalf("Search() returned error: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}

	// Verify first result
	if results[0].Title != "Go Testing" {
		t.Errorf("Expected title 'Go Testing', got %s", results[0].Title)
	}
	if results[0].URL != "https://go.dev/doc/testing" {
		t.Errorf("Expected URL 'https://go.dev/doc/testing', got %s", results[0].URL)
	}
	if !strings.Contains(results[0].Snippet, "built-in support") {
		t.Errorf("Expected snippet to contain 'built-in support', got %s", results[0].Snippet)
	}
}

func TestExaProvider_TextFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := exaResponse{
			Results: []exaResult{
				{
					Title: "Test Result",
					URL:   "https://example.com",
					Text:  "Full text content here",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewExaProviderWithClient("test-api-key", 30*time.Second, nil)
	provider.SetBaseURL(server.URL)

	results, err := provider.Search(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("Search() returned error: %v", err)
	}

	if results[0].Snippet != "Full text content here" {
		t.Errorf("Expected snippet from text field, got %s", results[0].Snippet)
	}
}

func TestExaProvider_EmptyResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := exaResponse{Results: []exaResult{}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewExaProviderWithClient("test-api-key", 30*time.Second, nil)
	provider.SetBaseURL(server.URL)

	results, err := provider.Search(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("Search() returned error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Expected 0 results, got %d", len(results))
	}
}

func TestExaProvider_MissingAPIKey(t *testing.T) {
	provider := NewExaProviderWithClient("", 30*time.Second, nil)

	_, err := provider.Search(context.Background(), "test", 5)
	if err == nil {
		t.Fatal("expected error for missing API key, got nil")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "API key")
	}
}

func TestExaProvider_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	provider := NewExaProviderWithClient("test-api-key", 30*time.Second, nil)
	provider.SetBaseURL(server.URL)

	_, err := provider.Search(context.Background(), "test", 5)
	if err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "HTTP 500")
	}
}

func TestExaProvider_RealSearch(t *testing.T) {
	apiKey := os.Getenv("EXA_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: EXA_API_KEY environment variable not set")
	}

	provider := NewExaProviderWithClient(apiKey, 30*time.Second, nil)
	results, err := provider.Search(context.Background(), "golang programming language", 3)
	if err != nil {
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "INVALID_API_KEY") || strings.Contains(err.Error(), "403") {
			t.Skipf("Skipping integration test: API key invalid or expired: %v", err)
		}
		t.Fatalf("Search() returned error: %v", err)
	}
	if len(results) == 0 {
		t.Error("Expected results for 'golang programming language'")
	}
}
