package backend

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/workspace"
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

// --- isHidden tests ---

func TestIsHidden(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect bool
	}{
		{"normal file", "main.go", false},
		{"dotfile", ".gitignore", true},
		{"dot dir", ".vscode", true},
		{"hidden with space", ". hidden", true},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workspace.IsHidden(tt.input); got != tt.expect {
				t.Errorf("workspace.IsHidden(%q) = %v, want %v", tt.input, got, tt.expect)
			}
		})
	}
}

// --- listDirectoryFlat tests ---

func TestListDirectoryFlat_HiddenAndGitIgnored(t *testing.T) {
	tmpDir := t.TempDir()

	// Create files and dirs
	_ = os.Mkdir(filepath.Join(tmpDir, "visible_dir"), 0o755)
	_ = os.Mkdir(filepath.Join(tmpDir, ".hidden_dir"), 0o755)
	_ = os.WriteFile(filepath.Join(tmpDir, "visible.txt"), []byte("v"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, ".hidden.txt"), []byte("h"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "ignored.txt"), []byte("i"), 0o644)

	// Init git and ignore ignored.txt
	gitInit(t, tmpDir)
	_ = os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("ignored.txt\n"), 0o644)

	ignoredPaths, err := workspace.GitIgnoredPaths(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("GitIgnoredPaths: %v", err)
	}
	nodes, err := workspace.ListDirFlat(tmpDir, ignoredPaths)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byName := make(map[string]FileNode)
	for _, n := range nodes {
		byName[n.Name] = n
	}

	if d, ok := byName["visible_dir"]; !ok || d.Hidden || d.GitIgnored {
		t.Errorf("visible_dir: unexpected flags hidden=%v gitignored=%v", d.Hidden, d.GitIgnored)
	}
	if d, ok := byName[".hidden_dir"]; !ok || !d.Hidden || d.GitIgnored {
		t.Errorf(".hidden_dir: expected hidden=true gitignored=false, got hidden=%v gitignored=%v", d.Hidden, d.GitIgnored)
	}
	if d, ok := byName["visible.txt"]; !ok || d.Hidden || d.GitIgnored {
		t.Errorf("visible.txt: unexpected flags hidden=%v gitignored=%v", d.Hidden, d.GitIgnored)
	}
	if d, ok := byName[".hidden.txt"]; !ok || !d.Hidden || d.GitIgnored {
		t.Errorf(".hidden.txt: expected hidden=true gitignored=false, got hidden=%v gitignored=%v", d.Hidden, d.GitIgnored)
	}
	if d, ok := byName["ignored.txt"]; !ok || d.Hidden || !d.GitIgnored {
		t.Errorf("ignored.txt: expected hidden=false gitignored=true, got hidden=%v gitignored=%v", d.Hidden, d.GitIgnored)
	}
}

func TestListDirectoryWalk_HiddenAndGitIgnored(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested structure
	_ = os.MkdirAll(filepath.Join(tmpDir, "sub", ".hidden_sub"), 0o755)
	_ = os.WriteFile(filepath.Join(tmpDir, "sub", "visible.txt"), []byte("v"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "sub", ".hidden.txt"), []byte("h"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "sub", "ignored.txt"), []byte("i"), 0o644)

	// Init git and ignore ignored.txt at root level
	gitInit(t, tmpDir)
	_ = os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("sub/ignored.txt\nsub/.hidden.txt\n"), 0o644)

	ignoredPaths, err := workspace.GitIgnoredPaths(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("GitIgnoredPaths: %v", err)
	}
	nodes, err := workspace.ListDirRecursive(tmpDir, ignoredPaths)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byPath := make(map[string]FileNode)
	for _, n := range nodes {
		byPath[n.Path] = n
	}

	visibleDir := filepath.Join(tmpDir, "sub")
	if n, ok := byPath[visibleDir]; !ok || n.Hidden || n.GitIgnored {
		t.Errorf("sub: unexpected flags hidden=%v gitignored=%v", n.Hidden, n.GitIgnored)
	}
	hiddenDir := filepath.Join(tmpDir, "sub", ".hidden_sub")
	if n, ok := byPath[hiddenDir]; !ok || !n.Hidden || n.GitIgnored {
		t.Errorf("sub/.hidden_sub: expected hidden=true gitignored=false, got hidden=%v gitignored=%v", n.Hidden, n.GitIgnored)
	}
	visibleFile := filepath.Join(tmpDir, "sub", "visible.txt")
	if n, ok := byPath[visibleFile]; !ok || n.Hidden || n.GitIgnored {
		t.Errorf("sub/visible.txt: unexpected flags hidden=%v gitignored=%v", n.Hidden, n.GitIgnored)
	}
	hiddenFile := filepath.Join(tmpDir, "sub", ".hidden.txt")
	if n, ok := byPath[hiddenFile]; !ok || !n.Hidden || !n.GitIgnored {
		t.Errorf("sub/.hidden.txt: expected hidden=true gitignored=true, got hidden=%v gitignored=%v", n.Hidden, n.GitIgnored)
	}
	ignoredFile := filepath.Join(tmpDir, "sub", "ignored.txt")
	if n, ok := byPath[ignoredFile]; !ok || n.Hidden || !n.GitIgnored {
		t.Errorf("sub/ignored.txt: expected hidden=false gitignored=true, got hidden=%v gitignored=%v", n.Hidden, n.GitIgnored)
	}
}

// --- ListDirectory icon attachment test ---

func TestListDirectory_IconsAttached(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	// Create test files with known extensions
	goFile := filepath.Join(tmpDir, "main.go")
	pngFile := filepath.Join(tmpDir, "image.png")
	dirPath := filepath.Join(tmpDir, "subdir")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write go file: %v", err)
	}
	if err := os.WriteFile(pngFile, []byte("\x89PNG\x0d\x0a\x1a\x0a"), 0o644); err != nil {
		t.Fatalf("write png file: %v", err)
	}
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	nodes, err := f.ListDirectory(tmpDir, false)
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}

	byName := make(map[string]FileNode)
	for _, n := range nodes {
		byName[n.Name] = n
	}

	// Directories should have empty icon.
	if d, ok := byName["subdir"]; ok {
		if d.Icon != "" {
			t.Errorf("subdir: expected empty icon, got %q", d.Icon)
		}
	} else {
		t.Error("subdir missing from listing")
	}

	// Files should have non-empty icon and icon color.
	if f, ok := byName["main.go"]; ok {
		if f.Icon == "" {
			t.Error("main.go: expected non-empty icon")
		}
		if f.IconColor == "" {
			t.Error("main.go: expected non-empty icon color")
		}
	} else {
		t.Error("main.go missing from listing")
	}

	if f, ok := byName["image.png"]; ok {
		if f.Icon == "" {
			t.Error("image.png: expected non-empty icon")
		}
		if f.IconColor == "" {
			t.Error("image.png: expected non-empty icon color")
		}
	} else {
		t.Error("image.png missing from listing")
	}
}

// --- helpers ---

func TestGetGitStatus(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}
	gitInit(t, tmpDir)

	// Commit an initial file so we have a HEAD.
	initial := filepath.Join(tmpDir, "initial.txt")
	if err := os.WriteFile(initial, []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write initial: %v", err)
	}
	runGit(t, tmpDir, "add", "initial.txt")
	runGit(t, tmpDir, "commit", "-m", "initial")

	// Staged modification (M in x) -> status M, staged=true
	// Set this up before adding other staged files so the commit only picks up staged_mod.
	stagedMod := filepath.Join(tmpDir, "staged_mod.txt")
	if err := os.WriteFile(stagedMod, []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write staged_mod: %v", err)
	}
	runGit(t, tmpDir, "add", "staged_mod.txt")
	runGit(t, tmpDir, "commit", "-m", "add staged_mod")
	if err := os.WriteFile(stagedMod, []byte("b\n"), 0o644); err != nil {
		t.Fatalf("write staged_mod v2: %v", err)
	}
	runGit(t, tmpDir, "add", "staged_mod.txt")

	// Untracked (??) -> status A, staged=false
	untracked := filepath.Join(tmpDir, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	// Staged new file (A in index) -> status A, staged=true
	addedFile := filepath.Join(tmpDir, "added.txt")
	if err := os.WriteFile(addedFile, []byte("added\n"), 0o644); err != nil {
		t.Fatalf("write added: %v", err)
	}
	runGit(t, tmpDir, "add", "added.txt")

	// Modified in work tree (M in y) -> status M, staged=false
	modifiedWT := filepath.Join(tmpDir, "initial.txt")
	if err := os.WriteFile(modifiedWT, []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("write modified: %v", err)
	}

	status, err := f.GetGitStatus(tmpDir)
	if err != nil {
		t.Fatalf("GetGitStatus: %v", err)
	}

	// Untracked -> A, staged=false
	if e, ok := status[untracked]; !ok {
		t.Error("untracked.txt missing from status")
	} else if e.Status != "A" || e.Staged {
		t.Errorf("untracked.txt: got %q staged=%v, want A staged=false", e.Status, e.Staged)
	}

	// Staged new -> A, staged=true
	if e, ok := status[addedFile]; !ok {
		t.Error("added.txt missing from status")
	} else if e.Status != "A" || !e.Staged {
		t.Errorf("added.txt: got %q staged=%v, want A staged=true", e.Status, e.Staged)
	}

	// Modified in work tree -> M, staged=false
	if e, ok := status[modifiedWT]; !ok {
		t.Error("initial.txt (modified) missing from status")
	} else if e.Status != "M" {
		t.Errorf("initial.txt: got %q, want M", e.Status)
	}

	// Staged modification -> M, staged=true
	if e, ok := status[stagedMod]; !ok {
		t.Error("staged_mod.txt missing from status")
	} else if e.Status != "M" || !e.Staged {
		t.Errorf("staged_mod.txt: got %q staged=%v, want M staged=true", e.Status, e.Staged)
	}
}

func TestGetGitStatus_RenamedAndCopied(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}
	gitInit(t, tmpDir)

	// Renamed file (R) -> status R, uses destination path
	renSrc := filepath.Join(tmpDir, "old_name.txt")
	if err := os.WriteFile(renSrc, []byte("rename\n"), 0o644); err != nil {
		t.Fatalf("write rename src: %v", err)
	}
	runGit(t, tmpDir, "add", "old_name.txt")
	runGit(t, tmpDir, "commit", "-m", "add old_name")
	runGit(t, tmpDir, "mv", "old_name.txt", "new_name.txt")

	status, err := f.GetGitStatus(tmpDir)
	if err != nil {
		t.Fatalf("GetGitStatus: %v", err)
	}

	renDst := filepath.Join(tmpDir, "new_name.txt")
	if e, ok := status[renDst]; !ok {
		t.Error("new_name.txt (rename dest) missing from status")
	} else if e.Status != "R" {
		t.Errorf("new_name.txt: got %q, want R", e.Status)
	}
	// Original path should NOT be present
	if _, ok := status[renSrc]; ok {
		t.Error("old_name.txt (rename src) should not be in status")
	}
}

func TestGetGitStatus_Unmerged(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}
	gitInit(t, tmpDir)

	// Create and commit a file on the default branch so both branches share a common ancestor.
	conflictFile := filepath.Join(tmpDir, "conflict.txt")
	if err := os.WriteFile(conflictFile, []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write conflict: %v", err)
	}
	runGit(t, tmpDir, "add", "conflict.txt")
	runGit(t, tmpDir, "commit", "-m", "base conflict")

	// Save default branch name before switching.
	defaultBranch := gitDefaultBranch(t, tmpDir)

	// On the conflict branch, modify the file.
	runGit(t, tmpDir, "checkout", "-b", "conflict")
	if err := os.WriteFile(conflictFile, []byte("branch\n"), 0o644); err != nil {
		t.Fatalf("write conflict: %v", err)
	}
	runGit(t, tmpDir, "add", "conflict.txt")
	runGit(t, tmpDir, "commit", "-m", "conflict branch")

	// On the default branch, also modify the file (differently) to cause a conflict.
	runGit(t, tmpDir, "checkout", defaultBranch)
	if err := os.WriteFile(conflictFile, []byte("master\n"), 0o644); err != nil {
		t.Fatalf("write conflict master: %v", err)
	}
	runGit(t, tmpDir, "add", "conflict.txt")
	runGit(t, tmpDir, "commit", "-m", "conflict master")

	// Merge will conflict — ignore the exit code.
	mergeCmd := exec.CommandContext(context.Background(), "git", "merge", "conflict")
	mergeCmd.Dir = tmpDir
	_ = mergeCmd.Run()

	status, err := f.GetGitStatus(tmpDir)
	if err != nil {
		t.Fatalf("GetGitStatus: %v", err)
	}

	// Conflict file should have status U (both-modified = UU in porcelain)
	if e, ok := status[conflictFile]; !ok {
		t.Error("conflict.txt missing from status")
	} else if e.Status != "U" {
		t.Errorf("conflict.txt: got %q, want U", e.Status)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Skipf("git %s failed: %v", strings.Join(args, " "), err)
	}
}

func gitDefaultBranch(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "master"
	}
	return strings.TrimSpace(string(out))
}

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
