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

func TestFileOpsTool_Name(t *testing.T) {
	tool := NewFileOpsTool()
	if tool.Name() != "file_ops" {
		t.Errorf("expected name 'file_ops', got '%s'", tool.Name())
	}
}

func TestFileOpsTool_Description(t *testing.T) {
	tool := NewFileOpsTool()
	if tool.Description() != "File system operations: read, write, edit, list, search, create/delete directories, delete files" {
		t.Errorf("unexpected description: %s", tool.Description())
	}
}

func TestFileOpsTool_InputSchema(t *testing.T) {
	tool := NewFileOpsTool()
	schema := tool.InputSchema()
	if len(schema) == 0 {
		t.Error("expected non-empty schema")
	}

	var parsed map[string]any
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

	if err := os.WriteFile(filepath.Join(tmpDir, "test1.txt"), []byte("content1"), 0o644); err != nil {
		t.Fatalf("failed to create test1.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "test2.txt"), []byte("content2"), 0o644); err != nil {
		t.Fatalf("failed to create test2.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "other.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("failed to create other.log: %v", err)
	}

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

	if !strings.Contains(result.Content, "test1.txt") {
		t.Errorf("expected 'test1.txt' in results, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "test2.txt") {
		t.Errorf("expected 'test2.txt' in results, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "nested.txt") {
		t.Errorf("expected 'nested.txt' in results, got: %s", result.Content)
	}
	if strings.Contains(result.Content, "other.log") {
		t.Errorf("did not expect 'other.log' in results, got: %s", result.Content)
	}
}

func TestFileOpsTool_SearchContent(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("Hello World\nGoodbye World\n"), 0o644); err != nil {
		t.Fatalf("failed to create file1.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("No match here\n"), 0o644); err != nil {
		t.Fatalf("failed to create file2.txt: %v", err)
	}

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

	if !strings.Contains(result.Content, "Hello World") {
		t.Errorf("expected 'Hello World' in results, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Goodbye World") {
		t.Errorf("expected 'Goodbye World' in results, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Another World") {
		t.Errorf("expected 'Another World' in results, got: %s", result.Content)
	}
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

	tmpDir := t.TempDir()
	otherDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)

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
	if !strings.Contains(result.Content, "failed to parse input") {
		t.Errorf("expected 'failed to parse input' error message, got: %s", result.Content)
	}
}

func TestFileOpsTool_CreateDirectory(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	newDir := filepath.Join(tmpDir, "newdir")
	input, _ := json.Marshal(map[string]string{
		"action": "create_directory",
		"path":   newDir,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "successfully created directory") {
		t.Errorf("expected success message, got: %s", result.Content)
	}

	info, err := os.Stat(newDir)
	if err != nil {
		t.Fatalf("directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected path to be a directory")
	}
}

func TestFileOpsTool_CreateDirectory_Nested(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	nestedDir := filepath.Join(tmpDir, "a", "b", "c", "d")
	input, _ := json.Marshal(map[string]string{
		"action": "create_directory",
		"path":   nestedDir,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify all intermediate directories exist
	for _, dir := range []string{
		filepath.Join(tmpDir, "a"),
		filepath.Join(tmpDir, "a", "b"),
		filepath.Join(tmpDir, "a", "b", "c"),
		nestedDir,
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("intermediate directory %s was not created: %v", dir, err)
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory", dir)
		}
	}
}

func TestFileOpsTool_DeleteDirectory_Empty(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	emptyDir := filepath.Join(tmpDir, "empty")
	if err := os.Mkdir(emptyDir, 0o755); err != nil {
		t.Fatalf("failed to create empty dir: %v", err)
	}

	input, _ := json.Marshal(map[string]any{
		"action":    "delete_directory",
		"path":      emptyDir,
		"recursive": false,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "successfully deleted directory") {
		t.Errorf("expected success message, got: %s", result.Content)
	}

	if _, err := os.Stat(emptyDir); !os.IsNotExist(err) {
		t.Error("expected directory to be deleted")
	}
}

func TestFileOpsTool_DeleteDirectory_NonEmpty_NonRecursive(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	nonEmptyDir := filepath.Join(tmpDir, "notempty")
	if err := os.Mkdir(nonEmptyDir, 0o755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmptyDir, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	input, _ := json.Marshal(map[string]any{
		"action":    "delete_directory",
		"path":      nonEmptyDir,
		"recursive": false,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for non-empty directory with recursive=false")
	}
	if !strings.Contains(result.Content, "failed to delete directory") {
		t.Errorf("expected error message about failed delete, got: %s", result.Content)
	}
}

func TestFileOpsTool_DeleteDirectory_Recursive(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	dirToDelete := filepath.Join(tmpDir, "todelete")
	subDir := filepath.Join(dirToDelete, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create dirs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirToDelete, "file1.txt"), []byte("data1"), 0o644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file2.txt"), []byte("data2"), 0o644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	input, _ := json.Marshal(map[string]any{
		"action":    "delete_directory",
		"path":      dirToDelete,
		"recursive": true,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "successfully deleted directory") {
		t.Errorf("expected success message, got: %s", result.Content)
	}

	if _, err := os.Stat(dirToDelete); !os.IsNotExist(err) {
		t.Error("expected directory to be deleted")
	}
}

func TestFileOpsTool_DeleteDirectory_NonExistent(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	input, _ := json.Marshal(map[string]any{
		"action":    "delete_directory",
		"path":      filepath.Join(tmpDir, "nonexistent"),
		"recursive": false,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for non-existent directory")
	}
	if !strings.Contains(result.Content, "failed to stat path") {
		t.Errorf("expected stat error message, got: %s", result.Content)
	}
}

func TestFileOpsTool_DeleteFile(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "todelete.txt")
	if err := os.WriteFile(testFile, []byte("delete me"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
		"action": "delete_file",
		"path":   testFile,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "successfully deleted file") {
		t.Errorf("expected success message, got: %s", result.Content)
	}

	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

func TestFileOpsTool_DeleteFile_NonExistent(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	input, _ := json.Marshal(map[string]string{
		"action": "delete_file",
		"path":   filepath.Join(tmpDir, "nonexistent.txt"),
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for non-existent file")
	}
	if !strings.Contains(result.Content, "failed to stat path") {
		t.Errorf("expected stat error message, got: %s", result.Content)
	}
}

func TestFileOpsTool_DeleteFile_IsDirectory(t *testing.T) {
	tool := NewFileOpsTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	dirPath := filepath.Join(tmpDir, "adir")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
		"action": "delete_file",
		"path":   dirPath,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true when trying to delete a directory with delete_file")
	}
	if !strings.Contains(result.Content, "path is a directory, use delete_directory instead") {
		t.Errorf("expected directory error message, got: %s", result.Content)
	}
}

func TestFileOpsTool_Judge_CreateDirectoryInsideWorkspace(t *testing.T) {
	tool := NewFileOpsTool()

	tmpDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)

	newDir := filepath.Join(tmpDir, "newdir")
	input, _ := json.Marshal(map[string]string{
		"action": "create_directory",
		"path":   newDir,
	})

	allow, reasoning := tool.Judge(ctx, input)
	if !allow {
		t.Error("expected Judge to return allow=true for create_directory inside workspace")
	}
	if !strings.Contains(reasoning, "workspace") {
		t.Errorf("expected reasoning to mention 'workspace', got: %s", reasoning)
	}
}

func TestFileOpsTool_Judge_DeleteDirectoryOutsideWorkspace(t *testing.T) {
	tool := NewFileOpsTool()

	tmpDir := t.TempDir()
	otherDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)

	input, _ := json.Marshal(map[string]any{
		"action":    "delete_directory",
		"path":      filepath.Join(otherDir, "somedir"),
		"recursive": true,
	})

	allow, reasoning := tool.Judge(ctx, input)
	if allow {
		t.Error("expected Judge to return allow=false for delete_directory outside workspace")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning when outside workspace, got: %s", reasoning)
	}
}

func TestFileOpsTool_Judge_DeleteFileInsideWorkspace(t *testing.T) {
	tool := NewFileOpsTool()

	tmpDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)

	testFile := filepath.Join(tmpDir, "file.txt")
	input, _ := json.Marshal(map[string]string{
		"action": "delete_file",
		"path":   testFile,
	})

	allow, reasoning := tool.Judge(ctx, input)
	if !allow {
		t.Error("expected Judge to return allow=true for delete_file inside workspace")
	}
	if !strings.Contains(reasoning, "workspace") {
		t.Errorf("expected reasoning to mention 'workspace', got: %s", reasoning)
	}
}
