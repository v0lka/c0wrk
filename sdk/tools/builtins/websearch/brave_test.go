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

func TestBraveProvider_Name(t *testing.T) {
	p := NewBraveProviderWithClient("test-key", 30*time.Second, nil)
	if p.Name() != "brave" {
		t.Errorf("Name() = %q, want %q", p.Name(), "brave")
	}
}

func TestBraveProvider_Search(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if got := r.Header.Get("X-Subscription-Token"); got != "test-key" {
			t.Errorf("X-Subscription-Token = %q, want %q", got, "test-key")
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want %q", got, "application/json")
		}
		if got := r.URL.Query().Get("q"); got != "golang testing" {
			t.Errorf("query param q = %q, want %q", got, "golang testing")
		}
		if got := r.URL.Query().Get("count"); got != "5" {
			t.Errorf("query param count = %q, want %q", got, "5")
		}

		resp := braveResponse{
			Web: braveWebResults{
				Results: []braveResult{
					{Title: "Go Testing", URL: "https://go.dev/doc/testing", Description: "Go has built-in support for testing."},
					{Title: "Testing Best Practices", URL: "https://example.com/testing", Description: "Learn about testing in Go."},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewBraveProviderWithClient("test-key", 30*time.Second, nil)
	p.SetBaseURL(srv.URL)

	results, err := p.Search(context.Background(), "golang testing", 5)
	if err != nil {
		t.Fatalf("Search() returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results[0].Title != "Go Testing" {
		t.Errorf("results[0].Title = %q, want %q", results[0].Title, "Go Testing")
	}
	if results[0].URL != "https://go.dev/doc/testing" {
		t.Errorf("results[0].URL = %q, want %q", results[0].URL, "https://go.dev/doc/testing")
	}
	if results[0].Snippet != "Go has built-in support for testing." {
		t.Errorf("results[0].Snippet = %q, want %q", results[0].Snippet, "Go has built-in support for testing.")
	}

	if results[1].Title != "Testing Best Practices" {
		t.Errorf("results[1].Title = %q, want %q", results[1].Title, "Testing Best Practices")
	}
	if results[1].URL != "https://example.com/testing" {
		t.Errorf("results[1].URL = %q, want %q", results[1].URL, "https://example.com/testing")
	}
	if results[1].Snippet != "Learn about testing in Go." {
		t.Errorf("results[1].Snippet = %q, want %q", results[1].Snippet, "Learn about testing in Go.")
	}
}

func TestBraveProvider_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("Rate limit exceeded"))
	}))
	defer srv.Close()

	p := NewBraveProviderWithClient("test-key", 30*time.Second, nil)
	p.SetBaseURL(srv.URL)

	_, err := p.Search(context.Background(), "test", 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 429") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "HTTP 429")
	}
}

func TestBraveProvider_EmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer srv.Close()

	p := NewBraveProviderWithClient("test-key", 30*time.Second, nil)
	p.SetBaseURL(srv.URL)

	results, err := p.Search(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("Search() returned error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestBraveProvider_MissingAPIKey(t *testing.T) {
	p := NewBraveProviderWithClient("", 30*time.Second, nil)

	_, err := p.Search(context.Background(), "test", 5)
	if err == nil {
		t.Fatal("expected error for missing API key, got nil")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "API key")
	}
}

func TestBraveProvider_RealSearch(t *testing.T) {
	apiKey := os.Getenv("BRAVE_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: BRAVE_API_KEY environment variable not set")
	}
	provider := NewBraveProviderWithClient(apiKey, 30*time.Second, nil)
	results, err := provider.Search(context.Background(), "golang programming language", 3)
	if err != nil {
		t.Fatalf("Search() returned error: %v", err)
	}
	if len(results) == 0 {
		t.Error("Expected results for 'golang programming language'")
	}
}
