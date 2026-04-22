package backend

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- resolveWorkspacePath tests ---

func TestResolveWorkspacePath_NoActiveProject(t *testing.T) {
	f := &FrontendAPI{}
	_, _, err := f.resolveWorkspacePath("/some/path")
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestResolveWorkspacePath_PathOutsideWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	_, _, err := f.resolveWorkspacePath("/etc/passwd")
	if err == nil {
		t.Fatal("expected error for path outside workspace")
	}
}

func TestResolveWorkspacePath_ValidPath(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	filePath := filepath.Join(tmpDir, "test.txt")
	absPath, absRoot, err := f.resolveWorkspacePath(filePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if absRoot != tmpDir {
		t.Errorf("expected root %q, got %q", tmpDir, absRoot)
	}
	if absPath != filePath {
		t.Errorf("expected path %q, got %q", filePath, absPath)
	}
}

func TestResolveWorkspacePath_WorkspaceRootItself(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	absPath, absRoot, err := f.resolveWorkspacePath(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if absRoot != tmpDir {
		t.Errorf("expected root %q, got %q", tmpDir, absRoot)
	}
	if absPath != tmpDir {
		t.Errorf("expected path %q, got %q", tmpDir, absPath)
	}
}

// --- ReadFile tests ---

func TestReadFile_NoActiveProject(t *testing.T) {
	f := &FrontendAPI{}
	_, err := f.ReadFile("/some/path")
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestReadFile_PathOutsideWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	_, err := f.ReadFile("/etc/passwd")
	if err == nil {
		t.Fatal("expected error for path outside workspace")
	}
}

func TestReadFile_Success(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	// Create a test file
	testContent := "hello world\nline 2\n"
	filePath := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(filePath, []byte(testContent), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	content, err := f.ReadFile(filePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != testContent {
		t.Errorf("expected content %q, got %q", testContent, content)
	}
}

func TestReadFile_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	filePath := filepath.Join(tmpDir, "nonexistent.txt")
	_, err := f.ReadFile(filePath)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestReadFile_NestedPath(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	// Create nested directory and file
	nestedDir := filepath.Join(tmpDir, "sub", "dir")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("failed to create nested dir: %v", err)
	}
	testContent := "nested content"
	filePath := filepath.Join(nestedDir, "nested.txt")
	if err := os.WriteFile(filePath, []byte(testContent), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	content, err := f.ReadFile(filePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != testContent {
		t.Errorf("expected content %q, got %q", testContent, content)
	}
}

// --- GetFileDiff tests ---

func TestGetFileDiff_NoActiveProject(t *testing.T) {
	f := &FrontendAPI{}
	_, err := f.GetFileDiff("/some/path")
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestGetFileDiff_PathOutsideWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	_, err := f.GetFileDiff("/etc/passwd")
	if err == nil {
		t.Fatal("expected error for path outside workspace")
	}
}

func TestGetFileDiff_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	// Create a test file (no git repo)
	filePath := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// With --no-index fallback, even a non-git directory produces a diff
	// showing the file as entirely added.
	diff, err := f.GetFileDiff(filePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff == "" {
		t.Error("expected non-empty diff for untracked file via --no-index")
	}
	if !strings.Contains(diff, "+++ b/test.txt") {
		t.Errorf("expected diff to contain +++ b/test.txt, got %q", diff)
	}
}

func TestGetFileDiff_UntrackedFileInGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	// Initialize git repo
	gitInit(t, tmpDir)

	// Create an untracked file (not git add-ed)
	filePath := filepath.Join(tmpDir, "newfile.txt")
	if err := os.WriteFile(filePath, []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	diff, err := f.GetFileDiff(filePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff == "" {
		t.Error("expected non-empty diff for untracked file")
	}
	if !strings.Contains(diff, "+++ b/newfile.txt") {
		t.Errorf("expected diff to contain +++ b/newfile.txt, got %q", diff)
	}
}

func TestGetFileDiff_TrackedFileNoChanges(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	// Initialize git repo
	gitInit(t, tmpDir)

	// Create and commit a file so it is tracked
	filePath := filepath.Join(tmpDir, "tracked.txt")
	if err := os.WriteFile(filePath, []byte("tracked content\n"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cmd := exec.CommandContext(context.Background(), "git", "add", "tracked.txt")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add failed: %v", err)
	}
	cmd = exec.CommandContext(context.Background(), "git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// No modifications — diff should be empty
	diff, err := f.GetFileDiff(filePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff for tracked file with no changes, got %q", diff)
	}
}

// --- helpers ---

func gitInit(t *testing.T, dir string) {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Skipf("git init failed: %v", err)
	}

	// Configure user for commits
	cmd = exec.CommandContext(context.Background(), "git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.CommandContext(context.Background(), "git", "config", "user.name", "Test")
	cmd.Dir = dir
	_ = cmd.Run()
}
