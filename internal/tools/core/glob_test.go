package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/agent/internal/tools"
)

// setupGlobTestDir creates a temp directory with nested structure for glob tests.
// Structure:
//
//	dir/sub/file.go
//	dir/sub/file.txt
//	dir/other/deep/file.go
func setupGlobTestDir(t *testing.T) string {
	t.Helper()

	base := t.TempDir()

	dirs := []string{
		filepath.Join(base, "sub"),
		filepath.Join(base, "other", "deep"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("failed to create dir %s: %v", d, err)
		}
	}

	files := []string{
		filepath.Join(base, "sub", "file.go"),
		filepath.Join(base, "sub", "file.txt"),
		filepath.Join(base, "other", "deep", "file.go"),
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("content"), 0o644); err != nil {
			t.Fatalf("failed to create file %s: %v", f, err)
		}
	}

	return base
}

func TestGlobTool_Name(t *testing.T) {
	tool := NewGlobTool()
	if tool.Name() != "glob" {
		t.Errorf("expected Name() = %q, got %q", "glob", tool.Name())
	}
}

func TestGlobTool_DefaultPolicy(t *testing.T) {
	tool := NewGlobTool()
	if tool.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected DefaultPolicy() = PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

func TestGlobTool_FindGoFiles(t *testing.T) {
	base := setupGlobTestDir(t)
	tool := NewGlobTool()

	input, _ := json.Marshal(globInput{
		Pattern: "**/*.go",
		Path:    base,
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 .go files, got %d: %v", len(lines), lines)
	}

	content := result.Content
	if !strings.Contains(content, "file.go") {
		t.Errorf("expected result to contain file.go, got: %s", content)
	}
}

func TestGlobTool_FindTxtFiles(t *testing.T) {
	base := setupGlobTestDir(t)
	tool := NewGlobTool()

	input, _ := json.Marshal(globInput{
		Pattern: "**/*.txt",
		Path:    base,
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 .txt file, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(result.Content, "file.txt") {
		t.Errorf("expected result to contain file.txt, got: %s", result.Content)
	}
}

func TestGlobTool_FindDirs(t *testing.T) {
	base := setupGlobTestDir(t)
	tool := NewGlobTool()

	input, _ := json.Marshal(globInput{
		Pattern: "**",
		Path:    base,
		Type:    "dirs",
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	content := result.Content
	if !strings.Contains(content, "sub") {
		t.Errorf("expected result to contain 'sub', got: %s", content)
	}
	if !strings.Contains(content, "other") {
		t.Errorf("expected result to contain 'other', got: %s", content)
	}
}

func TestGlobTool_MaxResults(t *testing.T) {
	base := setupGlobTestDir(t)
	tool := NewGlobTool()

	input, _ := json.Marshal(globInput{
		Pattern:    "**/*.go",
		Path:       base,
		MaxResults: 1,
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	if !strings.Contains(result.Content, "(results limited to 1)") {
		t.Errorf("expected truncation message, got: %s", result.Content)
	}

	// Count actual result lines (excluding the truncation message)
	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	// First line is the match, second is the truncation message
	matchCount := 0
	for _, l := range lines {
		if !strings.HasPrefix(l, "(") {
			matchCount++
		}
	}
	if matchCount != 1 {
		t.Errorf("expected 1 match line, got %d", matchCount)
	}
}

func TestGlobTool_NonExistentPath(t *testing.T) {
	tool := NewGlobTool()

	input, _ := json.Marshal(globInput{
		Pattern: "**/*.go",
		Path:    "/nonexistent/path/that/does/not/exist",
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for non-existent path, got content: %s", result.Content)
	}
}

func TestGlobTool_NoMatches(t *testing.T) {
	base := setupGlobTestDir(t)
	tool := NewGlobTool()

	input, _ := json.Marshal(globInput{
		Pattern: "**/*.xyz",
		Path:    base,
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}
	if result.Content != "no matching files found" {
		t.Errorf("expected 'no matching files found', got: %s", result.Content)
	}
}

func TestGlobTool_InvalidJSON(t *testing.T) {
	tool := NewGlobTool()

	result, err := tool.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for invalid JSON")
	}
}
