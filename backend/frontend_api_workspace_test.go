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

// --- stripLineAnchor tests ---

func TestStripLineAnchor(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"L range with L suffix", "/abs/path/x.go#L20-L36", "/abs/path/x.go"},
		{"L range bare end", "/abs/path/x.go#L5-10", "/abs/path/x.go"},
		{"L range both L", "/abs/path/x.go#L5-L10", "/abs/path/x.go"},
		{"single L anchor", "/abs/path/x.go#L42", "/abs/path/x.go"},
		{"bare single anchor", "/abs/path/x.go#42", "/abs/path/x.go"},
		{"no anchor left intact", "/abs/path/x.go", "/abs/path/x.go"},
		{"version fragment not stripped", "/abs/path/spec#v2.md", "/abs/path/spec#v2.md"},
		{"numeric-only fragment not stripped", "/abs/path/file#123.txt", "/abs/path/file#123.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripLineAnchor(tt.input); got != tt.want {
				t.Errorf("stripLineAnchor(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
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

// TestReadFile_OutsideWorkspaceReadable verifies the relaxed read path: the
// viewer may surface any file path the agent cites (e.g. SDK files, system
// files referenced in chat), so an out-of-workspace file must be readable.
func TestReadFile_OutsideWorkspaceReadable(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	// ReadFile is now path-agnostic: the viewer may surface any file path
	// surfaced by the agent (e.g. SDK files, system files referenced in
	// chat). Create a temp file OUTSIDE the workspace subdir and assert it
	// is readable.
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "external.txt")
	wantContent := "external content\n"
	if err := os.WriteFile(outsidePath, []byte(wantContent), 0o644); err != nil {
		t.Fatalf("failed to write external file: %v", err)
	}

	got, err := f.ReadFile(outsidePath)
	if err != nil {
		t.Fatalf("expected out-of-workspace path to be readable, got error: %v", err)
	}
	if got != wantContent {
		t.Errorf("expected content %q, got %q", wantContent, got)
	}
}

// TestReadFile_LineAnchorStripped verifies that a trailing "#L<n>-L<m>" line
// anchor fragment is stripped before os.ReadFile, so a viewer-provided path
// to an existing file plus an anchor does not surface "no such file".
func TestReadFile_LineAnchorStripped(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	filePath := filepath.Join(tmpDir, "x.go")
	wantContent := "package x\n"
	if err := os.WriteFile(filePath, []byte(wantContent), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	got, err := f.ReadFile(filePath + "#L20-L36")
	if err != nil {
		t.Fatalf("expected anchored path to read %q, got error: %v", filePath, err)
	}
	if got != wantContent {
		t.Errorf("expected content %q, got %q", wantContent, got)
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

	// No active project at all means projectPath is empty, so any path is
	// out-of-workspace → no git baseline. Consistent with the relaxed read
	// path, GetFileDiff returns ("", nil) rather than an error.
	diff, err := f.GetFileDiff("/some/path")
	if err != nil {
		t.Fatalf("expected no error with no active project, got: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff with no active project, got %q", diff)
	}
}

// TestGetFileDiff_OutsideWorkspaceNoBaseline verifies that out-of-workspace
// paths return ("", nil): there is no git baseline to diff against, so the
// frontend does not render a diff panel (and no error is surfaced).
func TestGetFileDiff_OutsideWorkspaceNoBaseline(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	// Out-of-workspace paths have no git baseline — GetFileDiff returns
	// ("", nil) instead of an error.
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "external.txt")
	if err := os.WriteFile(outsidePath, []byte("external\n"), 0o644); err != nil {
		t.Fatalf("failed to write external file: %v", err)
	}

	diff, err := f.GetFileDiff(outsidePath)
	if err != nil {
		t.Fatalf("expected no error for out-of-workspace path, got: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff for out-of-workspace path, got %q", diff)
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

	// Non-git paths have no git baseline to diff against, so GetFileDiff
	// returns an empty string — no synthetic --no-index diff, no hunk-staging
	// panel in the file viewer.
	diff, err := f.GetFileDiff(filePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff for non-git path, got %q", diff)
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

// --- ListDirectory .aiignore flagging tests ---

// TestListDirectory_AiignoreFlagsFiles verifies that files matched by a root
// .aiignore are flagged with GitIgnored=true, identically to .gitignore-ignored
// files, while non-matched files remain unflagged. The workspace is a git repo
// so the ignore resolver is built.
func TestListDirectory_AiignoreFlagsFiles(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	// Seed a few files: one matched by .aiignore, one matched by .gitignore,
	// and one visible.
	if err := os.WriteFile(filepath.Join(tmpDir, "visible.txt"), []byte("v"), 0o644); err != nil {
		t.Fatalf("write visible: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "aiignored.log"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write aiignored: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "gitignored.tmp"), []byte("g"), 0o644); err != nil {
		t.Fatalf("write gitignored: %v", err)
	}

	// .aiignore covers a pattern .gitignore does not.
	if err := os.WriteFile(filepath.Join(tmpDir, ".aiignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("write .aiignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("*.tmp\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	gitInit(t, tmpDir)

	nodes, err := f.ListDirectory(tmpDir, false)
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}

	byName := make(map[string]FileNode, len(nodes))
	for _, n := range nodes {
		byName[n.Name] = n
	}

	// .aiignore-matched file must be flagged exactly like a .gitignored file.
	if n, ok := byName["aiignored.log"]; !ok {
		t.Error("aiignored.log missing from listing")
	} else if !n.GitIgnored {
		t.Errorf("aiignored.log: expected GitIgnored=true (matched by .aiignore), got false")
	}

	// .gitignore behaviour is unchanged.
	if n, ok := byName["gitignored.tmp"]; !ok {
		t.Error("gitignored.tmp missing from listing")
	} else if !n.GitIgnored {
		t.Errorf("gitignored.tmp: expected GitIgnored=true, got false")
	}

	// Visible file stays unflagged.
	if n, ok := byName["visible.txt"]; !ok {
		t.Error("visible.txt missing from listing")
	} else if n.GitIgnored {
		t.Errorf("visible.txt: expected GitIgnored=false, got true")
	}
}

// TestListDirectory_AiignoreRecursiveAndDirs confirms the .aiignore flag is
// applied in recursive listings and honours directory patterns (a directory
// pattern flags both the directory and its contents).
func TestListDirectory_AiignoreRecursiveAndDirs(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	// build/ directory with contents, plus a loose file.
	if err := os.MkdirAll(filepath.Join(tmpDir, "build", "out"), 0o755); err != nil {
		t.Fatalf("mkdir build: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "build", "out", "artifact.bin"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "keep.txt"), []byte("k"), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}

	// Directory-only pattern: build/ is ignored by .aiignore.
	if err := os.WriteFile(filepath.Join(tmpDir, ".aiignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatalf("write .aiignore: %v", err)
	}

	gitInit(t, tmpDir)

	nodes, err := f.ListDirectory(tmpDir, true)
	if err != nil {
		t.Fatalf("ListDirectory recursive: %v", err)
	}

	byPath := make(map[string]FileNode, len(nodes))
	for _, n := range nodes {
		byPath[n.Path] = n
	}

	// The ignored directory and everything beneath it must be flagged.
	buildDir := filepath.Join(tmpDir, "build")
	if n, ok := byPath[buildDir]; !ok {
		t.Error("build dir missing from listing")
	} else if !n.GitIgnored {
		t.Errorf("build/: expected GitIgnored=true, got false")
	}
	artifact := filepath.Join(tmpDir, "build", "out", "artifact.bin")
	if n, ok := byPath[artifact]; !ok {
		t.Error("build/out/artifact.bin missing from listing")
	} else if !n.GitIgnored {
		t.Errorf("build/out/artifact.bin: expected GitIgnored=true (under ignored dir), got false")
	}

	// Unrelated file is untouched.
	if n, ok := byPath[filepath.Join(tmpDir, "keep.txt")]; !ok {
		t.Error("keep.txt missing from listing")
	} else if n.GitIgnored {
		t.Errorf("keep.txt: expected GitIgnored=false, got true")
	}
}

// TestListDirectory_AiignoreNonGitNoError verifies the No-Project / non-git
// path: when the workspace is not a git repository, ListDirectory returns no
// error and applies no spurious GitIgnored flags.
func TestListDirectory_AiignoreNonGitNoError(t *testing.T) {
	tmpDir := t.TempDir()
	// NOTE: no gitInit — this is a non-git workspace. No .aiignore is honoured
	// here either (resolver is only built for git repos), matching the task
	// requirement that the No-Project / non-git path is unchanged.
	f := &FrontendAPI{activeProjectPath: tmpDir}

	if err := os.WriteFile(filepath.Join(tmpDir, "plain.txt"), []byte("p"), 0o644); err != nil {
		t.Fatalf("write plain: %v", err)
	}

	nodes, err := f.ListDirectory(tmpDir, false)
	if err != nil {
		t.Fatalf("ListDirectory on non-git workspace: unexpected error: %v", err)
	}

	for _, n := range nodes {
		if n.GitIgnored {
			t.Errorf("non-git workspace: %s should not be flagged GitIgnored, got true", n.Name)
		}
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

// --- GetFileIcon tests ---

func TestGetFileIcon_OutsideWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	f := &FrontendAPI{activeProjectPath: tmpDir}

	// GetFileIcon is now path-agnostic: the viewer may request an icon for
	// any file path surfaced by the agent (e.g. SDK files).
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "external.go")

	resp, err := f.GetFileIcon(outsidePath)
	if err != nil {
		t.Fatalf("expected icon for out-of-workspace path, got error: %v", err)
	}
	if resp.Icon == "" {
		t.Error("expected non-empty icon for out-of-workspace .go path")
	}
}

// --- GetGitStatus tests ---

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
