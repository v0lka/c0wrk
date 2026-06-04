package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/sdk/tools"
)

// --- Name tests for individual tools ---

func TestReadFileTool_Name(t *testing.T) {
	tool := NewReadFileTool()
	if tool.Name() != "read_file" {
		t.Errorf("expected name 'read_file', got '%s'", tool.Name())
	}
}

func TestWriteFileTool_Name(t *testing.T) {
	tool := NewWriteFileTool()
	if tool.Name() != "write_file" {
		t.Errorf("expected name 'write_file', got '%s'", tool.Name())
	}
}

func TestEditFileTool_Name(t *testing.T) {
	tool := NewEditFileTool()
	if tool.Name() != "edit_file" {
		t.Errorf("expected name 'edit_file', got '%s'", tool.Name())
	}
}

func TestListDirectoryTool_Name(t *testing.T) {
	tool := NewListDirectoryTool()
	if tool.Name() != "list_directory" {
		t.Errorf("expected name 'list_directory', got '%s'", tool.Name())
	}
}

func TestCreateDirectoryTool_Name(t *testing.T) {
	tool := NewCreateDirectoryTool()
	if tool.Name() != "create_directory" {
		t.Errorf("expected name 'create_directory', got '%s'", tool.Name())
	}
}

func TestDeleteDirectoryTool_Name(t *testing.T) {
	tool := NewDeleteDirectoryTool()
	if tool.Name() != "delete_directory" {
		t.Errorf("expected name 'delete_directory', got '%s'", tool.Name())
	}
}

func TestDeleteFileTool_Name(t *testing.T) {
	tool := NewDeleteFileTool()
	if tool.Name() != "delete_file" {
		t.Errorf("expected name 'delete_file', got '%s'", tool.Name())
	}
}

// --- InputSchema test for a representative tool ---

func TestReadFileTool_InputSchema(t *testing.T) {
	tool := NewReadFileTool()
	schema := tool.InputSchema()
	if len(schema) == 0 {
		t.Error("expected non-empty schema")
	}

	var parsed map[string]any
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Errorf("schema is not valid JSON: %v", err)
	}
}

// --- Read tests ---

func TestReadFileTool_ReadFile(t *testing.T) {
	tool := NewReadFileTool()
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
		"path": testFile,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify metadata header is present
	if !strings.HasPrefix(result.Content, "[File:") {
		t.Errorf("expected result to start with metadata header '[File:', got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Lines 1-1 of 1") {
		t.Errorf("expected metadata header to contain 'Lines 1-1 of 1', got: %s", result.Content)
	}

	// Verify original file content is preserved
	if !strings.Contains(result.Content, testContent) {
		t.Errorf("expected content to contain '%s', got: %s", testContent, result.Content)
	}
}

func TestReadFileTool_ReadFile_NonExistent(t *testing.T) {
	tool := NewReadFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	input, _ := json.Marshal(map[string]string{
		"path": filepath.Join(tmpDir, "nonexistent.txt"),
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

// --- Write tests ---

func TestWriteFileTool_WriteFile(t *testing.T) {
	tool := NewWriteFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "output.txt")
	testContent := "Test content to write"

	input, _ := json.Marshal(map[string]string{
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

func TestWriteFileTool_WriteFile_NestedPath(t *testing.T) {
	tool := NewWriteFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Nested path that doesn't exist yet
	testFile := filepath.Join(tmpDir, "deep", "nested", "dir", "file.txt")
	testContent := "Nested content"

	input, _ := json.Marshal(map[string]string{
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

// --- Edit tests ---

func TestEditFileTool_EditFile_UniqueMatch(t *testing.T) {
	tool := NewEditFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "edit.txt")
	originalContent := "Hello, World! This is a test."
	if err := os.WriteFile(testFile, []byte(originalContent), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
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

func TestEditFileTool_EditFile_NonUniqueMatch(t *testing.T) {
	tool := NewEditFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "edit.txt")
	originalContent := "This is a test. Another test here."
	if err := os.WriteFile(testFile, []byte(originalContent), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
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

func TestEditFileTool_EditFile_NotFound(t *testing.T) {
	tool := NewEditFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "edit.txt")
	originalContent := "Hello, World!"
	if err := os.WriteFile(testFile, []byte(originalContent), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
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

// --- List test ---

func TestListDirectoryTool_ListDirectory(t *testing.T) {
	tool := NewListDirectoryTool()
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
		"path": tmpDir,
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

// --- DefaultPolicy test ---

func TestReadFileTool_DefaultPolicy(t *testing.T) {
	tool := NewReadFileTool()
	if tool.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected DefaultPolicy() to return PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

// --- Judge tests ---

func TestReadFileTool_Judge_ReadOnlyAction(t *testing.T) {
	tool := NewReadFileTool()

	t.Run("outside workspace requires confirmation", func(t *testing.T) {
		input, _ := json.Marshal(map[string]string{
			"path": "/some/path.txt",
		})

		allow, reasoning := tool.Judge(context.Background(), input)
		if allow {
			t.Error("expected Judge to return allow=false for path outside workspace")
		}
		if reasoning == "" {
			t.Error("expected non-empty reasoning for path outside workspace")
		}
	})

	t.Run("inside workspace auto-allows", func(t *testing.T) {
		tmpDir := t.TempDir()
		ctx := tools.WithWorkspacePath(context.Background(), tmpDir)

		testFile := filepath.Join(tmpDir, "test.txt")
		input, _ := json.Marshal(map[string]string{
			"path": testFile,
		})

		allow, reasoning := tool.Judge(ctx, input)
		if !allow {
			t.Errorf("expected Judge to return allow=true for path inside workspace, got reasoning: %s", reasoning)
		}
	})

	t.Run("inside temp dir auto-allows", func(t *testing.T) {
		tmpDir := t.TempDir()
		ctx := tools.WithTempDir(context.Background(), tmpDir)

		testFile := filepath.Join(tmpDir, "output.txt")
		input, _ := json.Marshal(map[string]string{
			"path": testFile,
		})

		allow, reasoning := tool.Judge(ctx, input)
		if !allow {
			t.Errorf("expected Judge to return allow=true for path inside temp dir, got reasoning: %s", reasoning)
		}
	})
}

func TestWriteFileTool_Judge_WriteActionInsideWorkspace(t *testing.T) {
	tool := NewWriteFileTool()

	tmpDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)

	testFile := filepath.Join(tmpDir, "test.txt")
	input, _ := json.Marshal(map[string]string{
		"path": testFile,
	})

	allow, reasoning := tool.Judge(ctx, input)
	if !allow {
		t.Error("expected Judge to return allow=true for write action inside workspace")
	}
	if !strings.Contains(reasoning, "workspace") {
		t.Errorf("expected reasoning to mention 'workspace', got: %s", reasoning)
	}
}

func TestWriteFileTool_Judge_WriteActionOutsideWorkspace(t *testing.T) {
	tool := NewWriteFileTool()

	tmpDir := t.TempDir()
	otherDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)

	testFile := filepath.Join(otherDir, "test.txt")
	input, _ := json.Marshal(map[string]string{
		"path": testFile,
	})

	allow, reasoning := tool.Judge(ctx, input)
	if allow {
		t.Error("expected Judge to return allow=false for write action outside workspace")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning when outside workspace, got: %s", reasoning)
	}
}

func TestWriteFileTool_Judge_WriteActionNoWorkspace(t *testing.T) {
	tool := NewWriteFileTool()

	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{
		"path": "/some/path.txt",
	})

	allow, reasoning := tool.Judge(ctx, input)
	if allow {
		t.Error("expected Judge to return allow=false when no workspace in context")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning when no workspace, got: %s", reasoning)
	}
}

func TestWriteFileTool_Judge_InvalidJSON(t *testing.T) {
	tool := NewWriteFileTool()

	allow, reasoning := tool.Judge(context.Background(), json.RawMessage(`{invalid`))
	if allow {
		t.Error("expected Judge to return allow=false for invalid JSON")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning for invalid JSON, got: %s", reasoning)
	}
}

func TestWriteFileTool_InvalidJSON(t *testing.T) {
	tool := NewWriteFileTool()
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

// --- Directory management tests ---

func TestCreateDirectoryTool_CreateDirectory(t *testing.T) {
	tool := NewCreateDirectoryTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	newDir := filepath.Join(tmpDir, "newdir")
	input, _ := json.Marshal(map[string]string{
		"path": newDir,
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

func TestCreateDirectoryTool_CreateDirectory_Nested(t *testing.T) {
	tool := NewCreateDirectoryTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	nestedDir := filepath.Join(tmpDir, "a", "b", "c", "d")
	input, _ := json.Marshal(map[string]string{
		"path": nestedDir,
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

func TestDeleteDirectoryTool_DeleteDirectory_Empty(t *testing.T) {
	tool := NewDeleteDirectoryTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	emptyDir := filepath.Join(tmpDir, "empty")
	if err := os.Mkdir(emptyDir, 0o755); err != nil {
		t.Fatalf("failed to create empty dir: %v", err)
	}

	input, _ := json.Marshal(map[string]any{
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

func TestDeleteDirectoryTool_DeleteDirectory_NonEmpty_NonRecursive(t *testing.T) {
	tool := NewDeleteDirectoryTool()
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

func TestDeleteDirectoryTool_DeleteDirectory_Recursive(t *testing.T) {
	tool := NewDeleteDirectoryTool()
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

func TestDeleteDirectoryTool_DeleteDirectory_NonExistent(t *testing.T) {
	tool := NewDeleteDirectoryTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	input, _ := json.Marshal(map[string]any{
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

// --- File delete tests ---

func TestDeleteFileTool_DeleteFile(t *testing.T) {
	tool := NewDeleteFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "todelete.txt")
	if err := os.WriteFile(testFile, []byte("delete me"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
		"path": testFile,
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

func TestDeleteFileTool_DeleteFile_NonExistent(t *testing.T) {
	tool := NewDeleteFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	input, _ := json.Marshal(map[string]string{
		"path": filepath.Join(tmpDir, "nonexistent.txt"),
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

func TestDeleteFileTool_DeleteFile_IsDirectory(t *testing.T) {
	tool := NewDeleteFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	dirPath := filepath.Join(tmpDir, "adir")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
		"path": dirPath,
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

// --- More Judge tests ---

func TestCreateDirectoryTool_Judge_CreateDirectoryInsideWorkspace(t *testing.T) {
	tool := NewCreateDirectoryTool()

	tmpDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)

	newDir := filepath.Join(tmpDir, "newdir")
	input, _ := json.Marshal(map[string]string{
		"path": newDir,
	})

	allow, reasoning := tool.Judge(ctx, input)
	if !allow {
		t.Error("expected Judge to return allow=true for create_directory inside workspace")
	}
	if !strings.Contains(reasoning, "workspace") {
		t.Errorf("expected reasoning to mention 'workspace', got: %s", reasoning)
	}
}

func TestDeleteDirectoryTool_Judge_DeleteDirectoryOutsideWorkspace(t *testing.T) {
	tool := NewDeleteDirectoryTool()

	tmpDir := t.TempDir()
	otherDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)

	input, _ := json.Marshal(map[string]any{
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

func TestDeleteFileTool_Judge_DeleteFileInsideWorkspace(t *testing.T) {
	tool := NewDeleteFileTool()

	tmpDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)

	testFile := filepath.Join(tmpDir, "file.txt")
	input, _ := json.Marshal(map[string]string{
		"path": testFile,
	})

	allow, reasoning := tool.Judge(ctx, input)
	if !allow {
		t.Error("expected Judge to return allow=true for delete_file inside workspace")
	}
	if !strings.Contains(reasoning, "workspace") {
		t.Errorf("expected reasoning to mention 'workspace', got: %s", reasoning)
	}
}

// --- Pagination tests ---

func TestReadFileTool_ReadFile_DefaultPagination(t *testing.T) {
	tool := NewReadFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create a temp file with 3000 lines
	testFile := filepath.Join(tmpDir, "large.txt")
	lines := make([]string, 0, 3000)
	for i := 1; i <= 3000; i++ {
		lines = append(lines, fmt.Sprintf("Line %d", i))
	}
	if err := os.WriteFile(testFile, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Read with no start_line/end_line — returns full file; truncation is handled centrally
	input, _ := json.Marshal(map[string]string{
		"path": testFile,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify all lines are returned (no per-tool truncation)
	if !strings.Contains(result.Content, "Line 1") {
		t.Errorf("expected 'Line 1' in content")
	}
	if !strings.Contains(result.Content, "Line 3000") {
		t.Errorf("expected 'Line 3000' in content (full file should be returned)")
	}

	// Verify metadata header shows full file
	if !strings.Contains(result.Content, "[File: large.txt | Lines 1-3000 of 3000") {
		t.Errorf("expected metadata header with full line range, got: %s", result.Content)
	}
}

func TestReadFileTool_ReadFile_ExplicitRange(t *testing.T) {
	tool := NewReadFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create a temp file with 100 lines
	testFile := filepath.Join(tmpDir, "medium.txt")
	lines := make([]string, 0, 100)
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("Line %d", i))
	}
	if err := os.WriteFile(testFile, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Read with explicit range: start_line=10, end_line=20
	input, _ := json.Marshal(map[string]any{
		"path":       testFile,
		"start_line": 10,
		"end_line":   20,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify exactly lines 10-20 returned
	if strings.Contains(result.Content, "Line 9") {
		t.Errorf("did not expect 'Line 9' in content (should start at 10)")
	}
	if !strings.Contains(result.Content, "Line 10") {
		t.Errorf("expected 'Line 10' in content")
	}
	if !strings.Contains(result.Content, "Line 20") {
		t.Errorf("expected 'Line 20' in content")
	}
	if strings.Contains(result.Content, "Line 21") {
		t.Errorf("did not expect 'Line 21' in content (should end at 20)")
	}

	// Verify metadata header shows correct range
	if !strings.Contains(result.Content, "[File: medium.txt | Lines 10-20 of 100") {
		t.Errorf("expected metadata header with lines 10-20, got: %s", result.Content)
	}
}

func TestReadFileTool_ReadFile_LongLinesTruncated(t *testing.T) {
	tool := NewReadFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create a temp file with one 5000-char line
	testFile := filepath.Join(tmpDir, "longline.txt")
	longLine := strings.Repeat("a", 5000)
	if err := os.WriteFile(testFile, []byte(longLine), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
		"path": testFile,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify the full line is returned (no per-tool truncation; central layer handles it)
	if !strings.Contains(result.Content, longLine) {
		t.Errorf("expected full 5000-char line without truncation, got: ...%s", result.Content[len(result.Content)-100:])
	}

	// Verify no truncation notice
	if strings.Contains(result.Content, "...(line truncated") {
		t.Errorf("did not expect line truncation notice")
	}
}

func TestReadFileTool_ReadFile_SmallFile(t *testing.T) {
	tool := NewReadFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create a temp file with 50 lines
	testFile := filepath.Join(tmpDir, "small.txt")
	lines := make([]string, 0, 50)
	for i := 1; i <= 50; i++ {
		lines = append(lines, fmt.Sprintf("Line %d", i))
	}
	if err := os.WriteFile(testFile, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	input, _ := json.Marshal(map[string]string{
		"path": testFile,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify all lines returned
	if !strings.Contains(result.Content, "Line 1") {
		t.Errorf("expected 'Line 1' in content")
	}
	if !strings.Contains(result.Content, "Line 50") {
		t.Errorf("expected 'Line 50' in content")
	}

	// Verify NO continuation hint (all lines fit)
	if strings.Contains(result.Content, "[Use start_line=") {
		t.Errorf("did not expect continuation hint for small file, got: %s", result.Content)
	}

	// Verify NO truncation notice
	if strings.Contains(result.Content, "truncated") {
		t.Errorf("did not expect truncation notice, got: %s", result.Content)
	}

	// Verify metadata header shows all lines
	if !strings.Contains(result.Content, "[File: small.txt | Lines 1-50 of 50") {
		t.Errorf("expected metadata header showing all lines, got: %s", result.Content)
	}
}

func TestReadFileTool_ReadFile_OutOfRangeClamp(t *testing.T) {
	tool := NewReadFileTool()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create a temp file with 100 lines
	testFile := filepath.Join(tmpDir, "clamp.txt")
	lines := make([]string, 0, 100)
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("Line %d", i))
	}
	if err := os.WriteFile(testFile, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Read with end_line=500 (beyond file size)
	input, _ := json.Marshal(map[string]any{
		"path":       testFile,
		"start_line": 1,
		"end_line":   500,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}

	// Verify all lines returned (clamped to 100)
	if !strings.Contains(result.Content, "Line 1") {
		t.Errorf("expected 'Line 1' in content")
	}
	if !strings.Contains(result.Content, "Line 100") {
		t.Errorf("expected 'Line 100' in content")
	}

	// Verify metadata header shows clamped range
	if !strings.Contains(result.Content, "[File: clamp.txt | Lines 1-100 of 100") {
		t.Errorf("expected metadata header with clamped line range, got: %s", result.Content)
	}
}

// --- Relative path resolution with workspace context ---

func TestFileTools_RelativePathWithWorkspace(t *testing.T) {
	workspace := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), workspace)

	t.Run("read_file", func(t *testing.T) {
		testContent := "hello workspace"
		if err := os.WriteFile(filepath.Join(workspace, "test.txt"), []byte(testContent), 0o644); err != nil {
			t.Fatal(err)
		}
		tool := NewReadFileTool()
		input, _ := json.Marshal(ReadFileInput{Path: "test.txt"})
		result, err := tool.Execute(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("unexpected error: %s", result.Content)
		}
		if !strings.Contains(result.Content, testContent) {
			t.Errorf("expected content %q in result, got: %s", testContent, result.Content)
		}
	})

	t.Run("write_file", func(t *testing.T) {
		tool := NewWriteFileTool()
		input, _ := json.Marshal(WriteFileInput{Path: "written.txt", Content: "workspace write"})
		result, err := tool.Execute(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("unexpected error: %s", result.Content)
		}
		data, err := os.ReadFile(filepath.Join(workspace, "written.txt"))
		if err != nil {
			t.Fatalf("file not created at workspace path: %v", err)
		}
		if string(data) != "workspace write" {
			t.Errorf("expected 'workspace write', got %q", string(data))
		}
	})

	t.Run("edit_file", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(workspace, "edit.txt"), []byte("old content here"), 0o644); err != nil {
			t.Fatal(err)
		}
		tool := NewEditFileTool()
		input, _ := json.Marshal(EditFileInput{Path: "edit.txt", OldString: "old", NewString: "new"})
		result, err := tool.Execute(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("unexpected error: %s", result.Content)
		}
		data, err := os.ReadFile(filepath.Join(workspace, "edit.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "new content here" {
			t.Errorf("expected 'new content here', got %q", string(data))
		}
	})

	t.Run("list_directory", func(t *testing.T) {
		subDir := filepath.Join(workspace, "mysubdir")
		if err := os.Mkdir(subDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(subDir, "a.txt"), []byte("a"), 0o644); err != nil {
			t.Fatal(err)
		}
		tool := NewListDirectoryTool()
		input, _ := json.Marshal(ListDirectoryInput{Path: "mysubdir"})
		result, err := tool.Execute(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("unexpected error: %s", result.Content)
		}
		if !strings.Contains(result.Content, "a.txt") {
			t.Errorf("expected 'a.txt' in listing, got: %s", result.Content)
		}
	})

	t.Run("delete_file", func(t *testing.T) {
		delFile := filepath.Join(workspace, "todelete.txt")
		if err := os.WriteFile(delFile, []byte("bye"), 0o644); err != nil {
			t.Fatal(err)
		}
		tool := NewDeleteFileTool()
		input, _ := json.Marshal(DeleteFileInput{Path: "todelete.txt"})
		result, err := tool.Execute(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("unexpected error: %s", result.Content)
		}
		if _, err := os.Stat(delFile); !os.IsNotExist(err) {
			t.Error("expected file to be deleted")
		}
	})
}
