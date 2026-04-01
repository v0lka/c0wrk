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

func TestFileOpsTool_Name(t *testing.T) {
	tool := NewFileOpsTool()
	if tool.Name() != "file_ops" {
		t.Errorf("expected name 'file_ops', got '%s'", tool.Name())
	}
}

func TestFileOpsTool_Description(t *testing.T) {
	tool := NewFileOpsTool()
	if tool.Description() != "File system operations: read, write, edit, list, search" {
		t.Errorf("unexpected description: %s", tool.Description())
	}
}

func TestFileOpsTool_InputSchema(t *testing.T) {
	tool := NewFileOpsTool()
	schema := tool.InputSchema()
	if len(schema) == 0 {
		t.Error("expected non-empty schema")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Errorf("schema is not valid JSON: %v", err)
	}
}

func TestFileOpsTool_ReadFile(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := "Hello, World!"
	if err := os.WriteFile(testFile, []byte(testContent), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Read the file
	input, _ := json.Marshal(map[string]string{
		"action": "read_file",
		"path":   testFile,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	if result.Content != testContent {
		t.Errorf("expected content '%s', got '%s'", testContent, result.Content)
	}
}

func TestFileOpsTool_ReadFile_NonExistent(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	input, _ := json.Marshal(map[string]string{
		"action": "read_file",
		"path":   filepath.Join(tmpDir, "nonexistent.txt"),
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for non-existent file")
	}
	if !strings.Contains(result.Content, "failed to read file") {
		t.Errorf("expected error message about failed read, got: %s", result.Content)
	}
}

func TestFileOpsTool_WriteFile(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "output.txt")
	testContent := "Test content to write"

	input, _ := json.Marshal(map[string]string{
		"action":  "write_file",
		"path":    testFile,
		"content": testContent,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify the file was written
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(data) != testContent {
		t.Errorf("expected content '%s', got '%s'", testContent, string(data))
	}
}

func TestFileOpsTool_WriteFile_NestedPath(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Nested path that doesn't exist yet
	testFile := filepath.Join(tmpDir, "deep", "nested", "dir", "file.txt")
	testContent := "Nested content"

	input, _ := json.Marshal(map[string]string{
		"action":  "write_file",
		"path":    testFile,
		"content": testContent,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify the file was written
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(data) != testContent {
		t.Errorf("expected content '%s', got '%s'", testContent, string(data))
	}
}

func TestFileOpsTool_EditFile_UniqueMatch(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "edit.txt")
	originalContent := "Hello, World! This is a test."
	if err := os.WriteFile(testFile, []byte(originalContent), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
		"action":     "edit_file",
		"path":       testFile,
		"old_string": "World",
		"new_string": "Universe",
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify the replacement
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read edited file: %v", err)
	}
	expected := "Hello, Universe! This is a test."
	if string(data) != expected {
		t.Errorf("expected content '%s', got '%s'", expected, string(data))
	}
}

func TestFileOpsTool_EditFile_NonUniqueMatch(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "edit.txt")
	// Content with "test" appearing twice
	originalContent := "This is a test. Another test here."
	if err := os.WriteFile(testFile, []byte(originalContent), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
		"action":     "edit_file",
		"path":       testFile,
		"old_string": "test",
		"new_string": "example",
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for non-unique match")
	}
	if !strings.Contains(result.Content, "not unique") {
		t.Errorf("expected 'not unique' error message, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "2 occurrences") {
		t.Errorf("expected '2 occurrences' in error message, got: %s", result.Content)
	}
}

func TestFileOpsTool_EditFile_NotFound(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "edit.txt")
	originalContent := "Hello, World!"
	if err := os.WriteFile(testFile, []byte(originalContent), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
		"action":     "edit_file",
		"path":       testFile,
		"old_string": "nonexistent",
		"new_string": "replacement",
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for string not found")
	}
	if !strings.Contains(result.Content, "not found") {
		t.Errorf("expected 'not found' error message, got: %s", result.Content)
	}
}

func TestFileOpsTool_ListDirectory(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create test files and directories
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("content1"), 0o644); err != nil {
		t.Fatalf("failed to create file1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("content2"), 0o644); err != nil {
		t.Fatalf("failed to create file2: %v", err)
	}
	if err := os.Mkdir(filepath.Join(tmpDir, "subdir"), 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
		"action": "list_directory",
		"path":   tmpDir,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify entries are present
	if !strings.Contains(result.Content, "file1.txt") {
		t.Errorf("expected 'file1.txt' in listing, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "file2.txt") {
		t.Errorf("expected 'file2.txt' in listing, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "subdir") {
		t.Errorf("expected 'subdir' in listing, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "dir") {
		t.Errorf("expected 'dir' type in listing, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "file") {
		t.Errorf("expected 'file' type in listing, got: %s", result.Content)
	}
}

func TestFileOpsTool_SearchFiles(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create test files
	if err := os.WriteFile(filepath.Join(tmpDir, "test1.txt"), []byte("content1"), 0o644); err != nil {
		t.Fatalf("failed to create test1.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "test2.txt"), []byte("content2"), 0o644); err != nil {
		t.Fatalf("failed to create test2.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "other.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("failed to create other.log: %v", err)
	}

	// Create nested file
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatalf("failed to create nested.txt: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
		"action":  "search_files",
		"path":    tmpDir,
		"pattern": "*.txt",
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify .txt files are found
	if !strings.Contains(result.Content, "test1.txt") {
		t.Errorf("expected 'test1.txt' in results, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "test2.txt") {
		t.Errorf("expected 'test2.txt' in results, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "nested.txt") {
		t.Errorf("expected 'nested.txt' in results, got: %s", result.Content)
	}
	// Verify .log file is NOT found
	if strings.Contains(result.Content, "other.log") {
		t.Errorf("did not expect 'other.log' in results, got: %s", result.Content)
	}
}

func TestFileOpsTool_SearchContent(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create test files with known content
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("Hello World\nGoodbye World\n"), 0o644); err != nil {
		t.Fatalf("failed to create file1.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("No match here\n"), 0o644); err != nil {
		t.Fatalf("failed to create file2.txt: %v", err)
	}

	// Create nested file with match
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("Another World line\n"), 0o644); err != nil {
		t.Fatalf("failed to create nested.txt: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
		"action": "search_content",
		"path":   tmpDir,
		"regex":  "World",
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify matches are found
	if !strings.Contains(result.Content, "Hello World") {
		t.Errorf("expected 'Hello World' in results, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Goodbye World") {
		t.Errorf("expected 'Goodbye World' in results, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Another World") {
		t.Errorf("expected 'Another World' in results, got: %s", result.Content)
	}
	// Verify line numbers are present
	if !strings.Contains(result.Content, ":1:") && !strings.Contains(result.Content, ":2:") {
		t.Errorf("expected line numbers in results, got: %s", result.Content)
	}
}

func TestFileOpsTool_SearchContent_InvalidRegex(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	input, _ := json.Marshal(map[string]string{
		"action": "search_content",
		"path":   tmpDir,
		"regex":  "[invalid",
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid regex")
	}
	if !strings.Contains(result.Content, "invalid regex") {
		t.Errorf("expected 'invalid regex' error message, got: %s", result.Content)
	}
}

func TestFileOpsTool_UnknownAction(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{
		"action": "unknown_action",
		"path":   "/tmp",
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for unknown action")
	}
	if !strings.Contains(result.Content, "unknown action") {
		t.Errorf("expected 'unknown action' error message, got: %s", result.Content)
	}
}

func TestFileOpsTool_DefaultPolicy(t *testing.T) {
	tool := NewFileOpsTool()
	if tool.DefaultPolicy() != tools.PolicyAuto {
		t.Errorf("expected DefaultPolicy() to return PolicyAuto, got %v", tool.DefaultPolicy())
	}
}

func TestFileOpsTool_Judge_ReadOnlyAction(t *testing.T) {
	tool := NewFileOpsTool()

	input, _ := json.Marshal(map[string]string{
		"action": "read_file",
		"path":   "/some/path.txt",
	})

	allow, reasoning := tool.Judge(context.Background(), input)
	if !allow {
		t.Error("expected Judge to return allow=true for read-only action")
	}
	if !strings.Contains(reasoning, "read-only") {
		t.Errorf("expected reasoning to mention 'read-only', got: %s", reasoning)
	}
}

func TestFileOpsTool_Judge_WriteActionInsideWorkspace(t *testing.T) {
	tool := NewFileOpsTool()

	// Create a temp directory to use as workspace
	tmpDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)

	testFile := filepath.Join(tmpDir, "test.txt")
	input, _ := json.Marshal(map[string]string{
		"action": "write_file",
		"path":   testFile,
	})

	allow, reasoning := tool.Judge(ctx, input)
	if !allow {
		t.Error("expected Judge to return allow=true for write action inside workspace")
	}
	if !strings.Contains(reasoning, "workspace") {
		t.Errorf("expected reasoning to mention 'workspace', got: %s", reasoning)
	}
}

func TestFileOpsTool_Judge_WriteActionOutsideWorkspace(t *testing.T) {
	tool := NewFileOpsTool()

	// Create two temp directories
	tmpDir := t.TempDir()
	otherDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)

	// Try to write to a file outside the workspace
	testFile := filepath.Join(otherDir, "test.txt")
	input, _ := json.Marshal(map[string]string{
		"action": "write_file",
		"path":   testFile,
	})

	allow, reasoning := tool.Judge(ctx, input)
	if allow {
		t.Error("expected Judge to return allow=false for write action outside workspace")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning when outside workspace, got: %s", reasoning)
	}
}

func TestFileOpsTool_Judge_WriteActionNoWorkspace(t *testing.T) {
	tool := NewFileOpsTool()

	// No workspace in context
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{
		"action": "write_file",
		"path":   "/some/path.txt",
	})

	allow, reasoning := tool.Judge(ctx, input)
	if allow {
		t.Error("expected Judge to return allow=false when no workspace in context")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning when no workspace, got: %s", reasoning)
	}
}

func TestFileOpsTool_Judge_InvalidJSON(t *testing.T) {
	tool := NewFileOpsTool()

	allow, reasoning := tool.Judge(context.Background(), json.RawMessage(`{invalid`))
	if allow {
		t.Error("expected Judge to return allow=false for invalid JSON")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning for invalid JSON, got: %s", reasoning)
	}
}

func TestFileOpsTool_InvalidJSON(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()

	result, err := tool.Execute(ctx, json.RawMessage(`{invalid json}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid JSON")
	}
	if !strings.Contains(result.Content, "invalid input") {
		t.Errorf("expected 'invalid input' error message, got: %s", result.Content)
	}
}
