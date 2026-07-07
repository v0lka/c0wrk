package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// mockSearchProvider implements SearchProvider for unit tests that don't need HTTP.
type mockSearchProvider struct {
	name    string
	results []SearchResult
	err     error
}

func (m *mockSearchProvider) Search(_ context.Context, _ string, _ int) ([]SearchResult, error) {
	return m.results, m.err
}

func (m *mockSearchProvider) Name() string { return m.name }

func TestTool_Descriptor(t *testing.T) {
	tool := NewTool(&mockSearchProvider{name: "mock"}, builtins.DefaultWebSearchLimits())

	if tool.Name() != "web_search" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "web_search")
	}

	if tool.Description() != toolWebsearchDescription {
		t.Errorf("Description() = %q, want %q", tool.Description(), toolWebsearchDescription)
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

func TestTool_MissingQuery(t *testing.T) {
	tool := NewTool(&mockSearchProvider{name: "mock"}, builtins.DefaultWebSearchLimits())

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

func TestTool_FormatResults(t *testing.T) {
	results := []SearchResult{
		{
			Title:   "First Result",
			URL:     "https://example.com/first",
			Snippet: "This is the first snippet.",
		},
		{
			Title:   "Second Result",
			URL:     "https://example.com/second",
			Snippet: "This is the second snippet.",
		},
	}

	output := formatResults(results)

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

func TestTool_InvalidJSON(t *testing.T) {
	tool := NewTool(&mockSearchProvider{name: "mock"}, builtins.DefaultWebSearchLimits())

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

func TestTool_DefaultPolicy(t *testing.T) {
	tool := NewTool(&mockSearchProvider{name: "mock"}, builtins.DefaultWebSearchLimits())
	if tool.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected DefaultPolicy() to return PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

func TestTool_ProviderError(t *testing.T) {
	provider := &mockSearchProvider{name: "mock", err: errors.New("provider failure")}
	tool := NewTool(provider, builtins.DefaultWebSearchLimits())
	input := json.RawMessage(`{"query": "test"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.IsError {
		t.Error("Expected IsError=true for provider error")
	}
	if !strings.Contains(result.Content, "search failed") {
		t.Errorf("Expected 'search failed' message, got: %s", result.Content)
	}
}
