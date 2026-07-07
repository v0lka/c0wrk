package builtins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/tools"
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
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	// No per-tool truncation; central layer handles it. All matches returned.
	if !strings.Contains(result.Content, "Found") && !strings.Contains(result.Content, "matches") {
		t.Errorf("expected match summary in output, got: %s", result.Content)
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

	// No per-line truncation; central layer handles it. The full long line should be present.
	if strings.Contains(result.Content, "...(line truncated)") {
		t.Errorf("did not expect '...(line truncated)' marker, got: %s", result.Content)
	}

	// Verify the file path and long content are present in output
	if !strings.Contains(result.Content, "longline.txt") {
		t.Errorf("expected output to contain file path 'longline.txt', got: %s", result.Content)
	}
}

func TestRipgrepTool_WorkspaceFallback(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "code.go"), []byte("func main() {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := tools.WithWorkspacePath(context.Background(), workspace)
	tool := NewRipgrepTool()

	input, _ := json.Marshal(RipgrepInput{Pattern: "func main"})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "func main") {
		t.Errorf("expected 'func main' in results, got: %s", result.Content)
	}
}

func TestRipgrepTool_NoPathNoWorkspace(t *testing.T) {
	ctx := context.Background()
	tool := NewRipgrepTool()

	input, _ := json.Marshal(RipgrepInput{Pattern: "test"})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error when no path and no workspace")
	}
}
