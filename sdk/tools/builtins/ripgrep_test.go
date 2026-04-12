package builtins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/agent/sdk/tools"
)

// setupRipgrepTestDir creates a temp directory with test files containing known content.
func setupRipgrepTestDir(t *testing.T) string {
	t.Helper()

	base := t.TempDir()

	// Create subdirectory
	subDir := filepath.Join(base, "src")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	files := map[string]string{
		filepath.Join(base, "hello.go"):    "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello World\")\n}\n",
		filepath.Join(base, "readme.txt"):  "This is a README file.\nIt contains some information.\nHello from readme.\n",
		filepath.Join(subDir, "utils.go"):  "package src\n\n// Helper function\nfunc helper() string {\n\treturn \"hello\"\n}\n",
		filepath.Join(subDir, "data.json"): `{"key": "value", "hello": "world"}` + "\n",
	}

	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write file %s: %v", path, err)
		}
	}

	return base
}

func TestRipgrepTool_Name(t *testing.T) {
	tool := NewRipgrepTool()
	if tool.Name() != "ripgrep" {
		t.Errorf("expected Name() = %q, got %q", "ripgrep", tool.Name())
	}
}

func TestRipgrepTool_DefaultPolicy(t *testing.T) {
	tool := NewRipgrepTool()
	if tool.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected DefaultPolicy() = PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

func TestRipgrepTool_BasicSearch(t *testing.T) {
	base := setupRipgrepTestDir(t)
	tool := NewRipgrepTool()

	input, _ := json.Marshal(RipgrepInput{
		Pattern: "Hello",
		Path:    base,
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	if !strings.Contains(result.Content, "Hello") {
		t.Errorf("expected result to contain 'Hello', got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Found") {
		t.Errorf("expected result to contain stats line, got: %s", result.Content)
	}
}

func TestRipgrepTool_IgnoreCase(t *testing.T) {
	base := setupRipgrepTestDir(t)
	tool := NewRipgrepTool()

	input, _ := json.Marshal(RipgrepInput{
		Pattern:    "hello",
		Path:       base,
		IgnoreCase: true,
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	// Should find both "Hello" and "hello" occurrences
	if !strings.Contains(result.Content, "Found") {
		t.Errorf("expected matches with ignore_case, got: %s", result.Content)
	}
}

func TestRipgrepTool_FilePattern(t *testing.T) {
	base := setupRipgrepTestDir(t)
	tool := NewRipgrepTool()

	input, _ := json.Marshal(RipgrepInput{
		Pattern:     "Hello",
		Path:        base,
		FilePattern: "*.go",
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	// Should only find matches in .go files
	if strings.Contains(result.Content, "readme.txt") {
		t.Errorf("expected no matches in .txt files when filtering *.go, got: %s", result.Content)
	}
}

func TestRipgrepTool_MaxResults(t *testing.T) {
	base := setupRipgrepTestDir(t)
	tool := NewRipgrepTool()

	input, _ := json.Marshal(RipgrepInput{
		Pattern:    "hello",
		Path:       base,
		IgnoreCase: true,
		MaxResults: 1,
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	// Count match lines (lines containing file:line: pattern, exclude stats and context)
	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	matchLines := 0
	for _, l := range lines {
		if strings.Contains(l, ":") && !strings.HasPrefix(l, "Found") && !strings.HasPrefix(l, "\nFound") && !strings.HasPrefix(l, "  ") {
			matchLines++
		}
	}
	if matchLines > 1 {
		t.Errorf("expected at most 1 match line with max_results=1, got %d: %s", matchLines, result.Content)
	}
}

func TestRipgrepTool_NonExistentPath(t *testing.T) {
	tool := NewRipgrepTool()

	input, _ := json.Marshal(RipgrepInput{
		Pattern: "test",
		Path:    "/nonexistent/path/that/does/not/exist",
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Either IsError=true or "no matches found" is acceptable for non-existent path
	if !result.IsError && result.Content != "no matches found" {
		t.Errorf("expected error or 'no matches found' for non-existent path, got: %s", result.Content)
	}
}

func TestRipgrepTool_NoMatches(t *testing.T) {
	base := setupRipgrepTestDir(t)
	tool := NewRipgrepTool()

	input, _ := json.Marshal(RipgrepInput{
		Pattern: "zzzznonexistent_pattern_xyz",
		Path:    base,
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}
	if result.Content != "no matches found" {
		t.Errorf("expected 'no matches found', got: %s", result.Content)
	}
}

func TestRipgrepTool_InvalidJSON(t *testing.T) {
	tool := NewRipgrepTool()

	result, err := tool.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for invalid JSON")
	}
}

func TestRipgrepTool_LongLineTruncation(t *testing.T) {
	base := t.TempDir()
	tool := NewRipgrepTool()

	// Create a file with a very long line (5000 characters)
	longContent := strings.Repeat("a", 5000)
	content := "short line\n" + longContent + "\nanother short line\n"
	testFile := filepath.Join(base, "longline.txt")
	if err := os.WriteFile(testFile, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	input, _ := json.Marshal(RipgrepInput{
		Pattern: "aaaa",
		Path:    base,
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	// Verify the output contains the truncation marker
	if !strings.Contains(result.Content, "...(line truncated)") {
		t.Errorf("expected result to contain '...(line truncated)', got: %s", result.Content)
	}

	// Verify the line is actually truncated (should be around 2000 + len(truncation marker))
	lines := strings.Split(result.Content, "\n")
	for _, line := range lines {
		if strings.Contains(line, "...(line truncated)") {
			// The line should be truncated to DefaultRipgrepLimits().MaxLineLength + len("...(line truncated)")
			defaults := DefaultRipgrepLimits()
			if len(line) > defaults.MaxLineLength+50 {
				t.Errorf("line appears not to be truncated, length: %d", len(line))
			}
			// Verify the file path and line number are preserved
			if !strings.HasPrefix(line, base) && !strings.Contains(line, "longline.txt") {
				t.Errorf("expected line to contain file path, got: %s", line)
			}
		}
	}
}
