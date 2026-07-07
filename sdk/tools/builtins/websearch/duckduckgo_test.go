package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDuckDuckGoProvider_Search(t *testing.T) {
	// Create mock HTML response
	mockHTML := `<!DOCTYPE html>
<html>
<head><title>Test Search</title></head>
<body>
<div class="results">
	<div class="result">
		<a class="result__a" href="https://example.com/testing">Go Testing</a>
		<a class="result__snippet">Go has built-in support for testing.</a>
	</div>
	<div class="result">
		<a class="result__a" href="/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2Ftesting&amp;rut=abc123">Go Documentation</a>
		<a class="result__snippet">Learn about testing in Go.</a>
	</div>
</div>
</body>
</html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET request, got %s", r.Method)
		}

		// Verify query parameter
		q := r.URL.Query().Get("q")
		if q != "golang testing" {
			t.Errorf("Expected query 'golang testing', got %s", q)
		}

		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(mockHTML))
	}))
	defer server.Close()

	provider := NewDuckDuckGoProviderWithClient(30*time.Second, nil)
	provider.SetBaseURL(server.URL)

	results, err := provider.Search(context.Background(), "golang testing", 5)
	if err != nil {
		t.Fatalf("Search() returned error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	// Verify first result (direct URL)
	if results[0].Title != "Go Testing" {
		t.Errorf("Expected title 'Go Testing', got %s", results[0].Title)
	}
	if results[0].URL != "https://example.com/testing" {
		t.Errorf("Expected URL 'https://example.com/testing', got %s", results[0].URL)
	}

	// Verify second result (redirect URL should be decoded)
	if results[1].Title != "Go Documentation" {
		t.Errorf("Expected title 'Go Documentation', got %s", results[1].Title)
	}
	// The URL should be decoded from the redirect
	expectedURL, _ := url.QueryUnescape("https://go.dev/doc/testing")
	if results[1].URL != expectedURL {
		t.Errorf("Expected URL '%s', got %s", expectedURL, results[1].URL)
	}
}

func TestDuckDuckGoProvider_EmptyResults(t *testing.T) {
	mockHTML := `<!DOCTYPE html>
<html>
<head><title>Test Search</title></head>
<body>
<div class="results"></div>
</body>
</html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(mockHTML))
	}))
	defer server.Close()

	provider := NewDuckDuckGoProviderWithClient(30*time.Second, nil)
	provider.SetBaseURL(server.URL)

	results, err := provider.Search(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("Search() returned error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Expected 0 results, got %d", len(results))
	}
}

func TestDuckDuckGoProvider_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	provider := NewDuckDuckGoProviderWithClient(30*time.Second, nil)
	provider.SetBaseURL(server.URL)

	_, err := provider.Search(context.Background(), "test", 5)
	if err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "HTTP 500")
	}
}

func TestDuckDuckGoProvider_MaxResults(t *testing.T) {
	mockHTML := `<!DOCTYPE html>
<html>
<body>
<div class="result">
	<a class="result__a" href="https://example.com/1">Result 1</a>
	<a class="result__snippet">Snippet 1</a>
</div>
<div class="result">
	<a class="result__a" href="https://example.com/2">Result 2</a>
	<a class="result__snippet">Snippet 2</a>
</div>
<div class="result">
	<a class="result__a" href="https://example.com/3">Result 3</a>
	<a class="result__snippet">Snippet 3</a>
</div>
</body>
</html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(mockHTML))
	}))
	defer server.Close()

	provider := NewDuckDuckGoProviderWithClient(30*time.Second, nil)
	provider.SetBaseURL(server.URL)

	results, err := provider.Search(context.Background(), "test", 2)
	if err != nil {
		t.Fatalf("Search() returned error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Expected 2 results (maxResults limit), got %d", len(results))
	}
}

func TestCleanDuckDuckGoURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "direct https URL",
			input:    "https://example.com/page",
			expected: "https://example.com/page",
		},
		{
			name:     "direct http URL",
			input:    "http://example.com/page",
			expected: "http://example.com/page",
		},
		{
			name:     "redirect URL",
			input:    "/l/?uddg=https%3A%2F%2Fexample.com%2Fpage&rut=abc123",
			expected: "https://example.com/page",
		},
		{
			name:     "protocol-relative URL",
			input:    "//example.com/page",
			expected: "https://example.com/page",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := cleanDuckDuckGoURL(tc.input)
			if result != tc.expected {
				t.Errorf("cleanDuckDuckGoURL(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestDuckDuckGoProvider_RealSearch(t *testing.T) {
	provider := NewDuckDuckGoProviderWithClient(30*time.Second, nil)
	results, err := provider.Search(context.Background(), "golang programming language", 3)
	if err != nil {
		t.Fatalf("Search() returned error: %v", err)
	}
	if len(results) == 0 {
		t.Error("Expected results for 'golang programming language'")
	}
}
