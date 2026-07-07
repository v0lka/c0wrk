package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/tools"
)

// setupGlobTestDir creates a temp directory with nested structure for glob tests.
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

	input, _ := json.Marshal(GlobInput{
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

	input, _ := json.Marshal(GlobInput{
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

	input, _ := json.Marshal(GlobInput{
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

	input, _ := json.Marshal(GlobInput{
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

	// No more per-tool truncation; central layer handles it.
	// All matching files should be returned regardless of max_results.
	if strings.Contains(result.Content, "(results limited to") {
		t.Errorf("did not expect truncation message, got: %s", result.Content)
	}

	// Should have more than 1 match since truncation was removed.
	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	if len(lines) <= 1 {
		t.Errorf("expected more than 1 match since truncation is removed, got %d lines: %s", len(lines), result.Content)
	}
}

func TestGlobTool_NonExistentPath(t *testing.T) {
	tool := NewGlobTool()

	input, _ := json.Marshal(GlobInput{
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

	input, _ := json.Marshal(GlobInput{
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

func TestGlobTool_DefaultMaxResults(t *testing.T) {
	base := t.TempDir()
	tool := NewGlobTool()

	// Create many files to potentially exceed the default limit
	for i := 0; i < 250; i++ {
		f := filepath.Join(base, fmt.Sprintf("file%d.txt", i))
		if err := os.WriteFile(f, []byte("content"), 0o644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}
	}

	input, _ := json.Marshal(GlobInput{
		Pattern: "*.txt",
		Path:    base,
		// MaxResults not specified, should use default of 200
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	// No per-tool truncation; all 250 files should be returned.
	if strings.Contains(result.Content, "(results limited to") {
		t.Errorf("did not expect truncation message, got: %s", result.Content)
	}
}

func TestGlobTool_WorkspaceFallback(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := tools.WithWorkspacePath(context.Background(), workspace)
	tool := NewGlobTool()

	// Call with no path - should use workspace
	input, _ := json.Marshal(GlobInput{Pattern: "*.txt"})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "hello.txt") {
		t.Errorf("expected hello.txt in results, got: %s", result.Content)
	}
}

func TestGlobTool_NoPathNoWorkspace(t *testing.T) {
	ctx := context.Background() // no workspace
	tool := NewGlobTool()

	input, _ := json.Marshal(GlobInput{Pattern: "*.txt"})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error when no path and no workspace")
	}
}
