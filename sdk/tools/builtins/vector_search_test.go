package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// semantic_search
// ---------------------------------------------------------------------------

func TestVectorSearchTool_Metadata(t *testing.T) {
	tool := NewVectorSearchTool(
		func(_ context.Context, _ VectorSearchOptions) ([]VectorSearchResult, error) {
			return nil, nil
		},
		nil,
	)

	if tool.Name() != "semantic_search" {
		t.Errorf("expected name 'semantic_search', got %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("description should not be empty")
	}

	// Validate schema is valid JSON with required fields.
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema missing 'properties'")
	}
	for _, key := range []string{"query", "top_k", "file_pattern", "must_match", "mode"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema missing %q property", key)
		}
	}
	// Verify mode enum values.
	if mode, ok := props["mode"].(map[string]any); ok {
		if enum, ok := mode["enum"].([]any); ok {
			got := make([]string, 0, len(enum))
			for _, v := range enum {
				if s, ok := v.(string); ok {
					got = append(got, s)
				}
			}
			want := []string{"hybrid", "vector", "lexical"}
			if len(got) != len(want) {
				t.Errorf("mode enum: want %v, got %v", want, got)
			}
		} else {
			t.Error("mode property missing enum")
		}
	}
}

func TestVectorSearchTool_Execute_Results(t *testing.T) {
	searchFunc := func(_ context.Context, _ VectorSearchOptions) ([]VectorSearchResult, error) {
		return []VectorSearchResult{
			{
				FilePath:   "pkg/auth/handler.go",
				FileName:   "handler.go",
				Content:    "func HandleLogin(w http.ResponseWriter, r *http.Request) {\n\t// authenticate user\n}",
				Score:      0.87,
				StartLine:  45,
				EndLine:    78,
				Language:   "go",
				VectorRank: 1,
			},
			{
				FilePath:    "pkg/auth/middleware.go",
				FileName:    "middleware.go",
				Content:     "func AuthMiddleware(next http.Handler) http.Handler {",
				Score:       0.82,
				StartLine:   12,
				EndLine:     30,
				Language:    "go",
				LexicalRank: 1,
			},
		}, nil
	}

	tool := NewVectorSearchTool(searchFunc, nil)
	input, _ := json.Marshal(VectorSearchInput{Query: "authentication handler"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}

	if !strings.Contains(result.Content, "Found 2 results") {
		t.Errorf("expected 'Found 2 results', got %q", result.Content)
	}
	if !strings.Contains(result.Content, "pkg/auth/handler.go") {
		t.Errorf("expected file path in result, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "lines 45-78") {
		t.Errorf("expected line range in result, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "0.87") {
		t.Errorf("expected score in result, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "language: go") {
		t.Errorf("expected language in result, got %q", result.Content)
	}
	// Per-side rank badges should be visible to the agent.
	if !strings.Contains(result.Content, "V#1") {
		t.Errorf("expected vector rank 'V#1' in result, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "L#1") {
		t.Errorf("expected lexical rank 'L#1' in result, got %q", result.Content)
	}
}

func TestVectorSearchTool_Execute_WaitFunc(t *testing.T) {
	waitCalled := false
	waitFunc := func(_ context.Context) error {
		waitCalled = true
		time.Sleep(10 * time.Millisecond) // simulate brief wait
		return nil
	}
	searchFunc := func(_ context.Context, _ VectorSearchOptions) ([]VectorSearchResult, error) {
		return []VectorSearchResult{
			{FilePath: "a.go", Content: "content", Score: 0.9, StartLine: 1, EndLine: 10, Language: "go"},
		}, nil
	}

	tool := NewVectorSearchTool(searchFunc, waitFunc)
	input, _ := json.Marshal(VectorSearchInput{Query: "test"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if !waitCalled {
		t.Error("waitFunc was not called")
	}
}

func TestVectorSearchTool_Execute_WaitFuncError(t *testing.T) {
	waitFunc := func(_ context.Context) error {
		return errors.New("index not available")
	}
	searchFunc := func(_ context.Context, _ VectorSearchOptions) ([]VectorSearchResult, error) {
		t.Fatal("searchFunc should not be called when waitFunc fails")
		return nil, nil
	}

	tool := NewVectorSearchTool(searchFunc, waitFunc)
	input, _ := json.Marshal(VectorSearchInput{Query: "test"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result when waitFunc fails")
	}
	if !strings.Contains(result.Content, "not ready") {
		t.Errorf("expected 'not ready' message, got %q", result.Content)
	}
}

func TestVectorSearchTool_Execute_EmptyResults(t *testing.T) {
	searchFunc := func(_ context.Context, _ VectorSearchOptions) ([]VectorSearchResult, error) {
		return nil, nil
	}

	tool := NewVectorSearchTool(searchFunc, nil)
	input, _ := json.Marshal(VectorSearchInput{Query: "nonexistent concept"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, "No results found") {
		t.Errorf("expected 'No results found', got %q", result.Content)
	}
}

func TestVectorSearchTool_Execute_FilePattern(t *testing.T) {
	var capturedOpts VectorSearchOptions
	searchFunc := func(_ context.Context, opts VectorSearchOptions) ([]VectorSearchResult, error) {
		capturedOpts = opts
		return []VectorSearchResult{
			{FilePath: "src/app.ts", Content: "const app = express()", Score: 0.8, StartLine: 1, EndLine: 5, Language: "typescript"},
		}, nil
	}

	tool := NewVectorSearchTool(searchFunc, nil)
	input, _ := json.Marshal(VectorSearchInput{
		Query:       "express app",
		FilePattern: "**/*.ts",
		MustMatch:   []string{"express"},
		Mode:        "hybrid",
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if capturedOpts.FilePattern != "**/*.ts" {
		t.Errorf("expected file_pattern '**/*.ts', got %q", capturedOpts.FilePattern)
	}
	if len(capturedOpts.MustMatch) != 1 || capturedOpts.MustMatch[0] != "express" {
		t.Errorf("expected must_match [express], got %v", capturedOpts.MustMatch)
	}
	if capturedOpts.Mode != "hybrid" {
		t.Errorf("expected mode 'hybrid', got %q", capturedOpts.Mode)
	}
}

func TestVectorSearchTool_Execute_TopKCapped(t *testing.T) {
	var capturedTopK int
	searchFunc := func(_ context.Context, opts VectorSearchOptions) ([]VectorSearchResult, error) {
		capturedTopK = opts.TopK
		return nil, nil
	}

	tool := NewVectorSearchTool(searchFunc, nil)
	input, _ := json.Marshal(VectorSearchInput{Query: "test", TopK: 100})

	_, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedTopK != 50 {
		t.Errorf("expected top_k capped at 50, got %d", capturedTopK)
	}
}

func TestVectorSearchTool_Execute_DefaultTopK(t *testing.T) {
	var capturedTopK int
	searchFunc := func(_ context.Context, opts VectorSearchOptions) ([]VectorSearchResult, error) {
		capturedTopK = opts.TopK
		return nil, nil
	}

	tool := NewVectorSearchTool(searchFunc, nil)
	input, _ := json.Marshal(VectorSearchInput{Query: "test"})

	_, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedTopK != 10 {
		t.Errorf("expected default top_k 10, got %d", capturedTopK)
	}
}

func TestVectorSearchTool_Execute_InvalidJSON(t *testing.T) {
	searchFunc := func(_ context.Context, _ VectorSearchOptions) ([]VectorSearchResult, error) {
		return nil, nil
	}
	tool := NewVectorSearchTool(searchFunc, nil)

	result, err := tool.Execute(context.Background(), []byte(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid JSON")
	}
}

func TestVectorSearchTool_Execute_EmptyQuery(t *testing.T) {
	searchFunc := func(_ context.Context, _ VectorSearchOptions) ([]VectorSearchResult, error) {
		t.Fatal("searchFunc should not be called for empty query")
		return nil, nil
	}
	tool := NewVectorSearchTool(searchFunc, nil)

	input, _ := json.Marshal(VectorSearchInput{Query: ""})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for empty query")
	}
}

func TestVectorSearchTool_Execute_SearchError(t *testing.T) {
	searchFunc := func(_ context.Context, _ VectorSearchOptions) ([]VectorSearchResult, error) {
		return nil, errors.New("connection timeout")
	}
	tool := NewVectorSearchTool(searchFunc, nil)

	input, _ := json.Marshal(VectorSearchInput{Query: "test"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for search error")
	}
	if !strings.Contains(result.Content, "search failed") {
		t.Errorf("expected 'search failed' message, got %q", result.Content)
	}
}

func TestVectorSearchTool_Execute_ContentTruncation(t *testing.T) {
	longContent := strings.Repeat("x", 600)
	searchFunc := func(_ context.Context, _ VectorSearchOptions) ([]VectorSearchResult, error) {
		return []VectorSearchResult{
			{FilePath: "big.go", Content: longContent, Score: 0.9, StartLine: 1, EndLine: 10, Language: "go"},
		}, nil
	}

	tool := NewVectorSearchTool(searchFunc, nil)
	input, _ := json.Marshal(VectorSearchInput{Query: "test"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, "...") {
		t.Error("expected truncation indicator '...' in content")
	}
	// Full 600-char content should NOT appear.
	if strings.Contains(result.Content, longContent) {
		t.Error("content should be truncated, not shown in full")
	}
}
