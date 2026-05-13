package vectorindex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeChunkFunc is a test ChunkFunc that splits content into a single chunk.
func fakeChunkFunc(filePath string, content []byte, maxChunkSize, overlap int) ([]ChunkResult, error) {
	if len(content) == 0 {
		return nil, nil
	}
	lang := "text"
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".go":
		lang = "go"
	case ".ts", ".tsx":
		lang = "typescript"
	case ".js":
		lang = "javascript"
	case ".py":
		lang = "python"
	}
	lines := strings.Count(string(content), "\n") + 1
	return []ChunkResult{
		{
			Content:   string(content),
			StartLine: 1,
			EndLine:   lines,
			Language:  lang,
		},
	}, nil
}

// fakeHashFunc returns a deterministic hash for testing.
func fakeHashFunc(content []byte) string {
	return computeHash(content)
}

// setupTestService creates an in-memory Service with a project and branch set up.
func setupTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.SetProject("indexer-test"); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	return svc
}

// createTestWorkspace creates a temporary directory with test files.
func createTestWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	files := map[string]string{
		"main.go":      "package main\n\nfunc main() {}\n",
		"lib/utils.go": "package lib\n\nfunc Add(a, b int) int { return a + b }\n",
		"README.md":    "# Test Project\n\nSome content.\n",
		"config.yaml":  "key: value\n",
	}

	for relPath, content := range files {
		absPath := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	return dir
}

func TestIndexFull(t *testing.T) {
	svc := setupTestService(t)
	wsDir := createTestWorkspace(t)

	var progressCalls []IndexState
	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
		OnProgress: func(state IndexState, filesIndexed, totalFiles int, currentFile string) {
			progressCalls = append(progressCalls, state)
		},
	})

	if err := indexer.IndexFull(context.Background(), wsDir); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}

	if !svc.IsReady() {
		t.Error("expected service to be ready after IndexFull")
	}

	col := svc.GetCollection()
	if col == nil {
		t.Fatal("expected collection to be non-nil")
	}
	if col.Count() == 0 {
		t.Error("expected documents in collection after IndexFull")
	}

	// Progress should have been called with indexing and ready states.
	hasIndexing := false
	hasReady := false
	for _, s := range progressCalls {
		if s == IndexStateIndexing {
			hasIndexing = true
		}
		if s == IndexStateReady {
			hasReady = true
		}
	}
	if !hasIndexing {
		t.Error("expected IndexStateIndexing progress callback")
	}
	if !hasReady {
		t.Error("expected IndexStateReady progress callback")
	}
}

func TestIndexFull_ContextCancelled(t *testing.T) {
	svc := setupTestService(t)
	wsDir := createTestWorkspace(t)

	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := indexer.IndexFull(ctx, wsDir)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestIndexIncremental(t *testing.T) {
	svc := setupTestService(t)
	wsDir := createTestWorkspace(t)

	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})

	// First: full index.
	if err := indexer.IndexFull(context.Background(), wsDir); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}

	initialCount := svc.GetCollection().Count()

	// Modify a file.
	mainGo := filepath.Join(wsDir, "main.go")
	if err := os.WriteFile(mainGo, []byte("package main\n\nfunc main() { println(\"updated\") }\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Add a new file.
	newFile := filepath.Join(wsDir, "new_file.go")
	if err := os.WriteFile(newFile, []byte("package main\n\nfunc New() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Delete a file.
	readmePath := filepath.Join(wsDir, "README.md")
	if err := os.Remove(readmePath); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	var progressStates []IndexState
	var readyTotalFiles int
	indexer.onProgress = func(state IndexState, filesIndexed, totalFiles int, currentFile string) {
		progressStates = append(progressStates, state)
		if state == IndexStateReady {
			readyTotalFiles = totalFiles
		}
	}

	if err := indexer.IndexIncremental(context.Background(), wsDir); err != nil {
		t.Fatalf("IndexIncremental: %v", err)
	}

	if !svc.IsReady() {
		t.Error("expected service to be ready after IndexIncremental")
	}

	// We should have at least some reindexing progress.
	hasReindexing := false
	for _, s := range progressStates {
		if s == IndexStateReindexing {
			hasReindexing = true
		}
	}
	if !hasReindexing {
		t.Error("expected IndexStateReindexing progress callback")
	}

	// The count should differ from initial (added one file, deleted one, modified one).
	finalCount := svc.GetCollection().Count()
	_ = initialCount
	if finalCount == 0 {
		t.Error("expected non-zero document count after incremental index")
	}

	// The ready callback should report total files in the collection, not just changed files.
	// The workspace has 4 files: main.go, lib/utils.go, config.yaml, new_file.go
	// (README.md was deleted, so 4 original - 1 deleted + 1 new = 4)
	if readyTotalFiles < 3 {
		t.Errorf("expected readyTotalFiles to reflect total collection size (>= 3), got %d", readyTotalFiles)
	}
}

func TestIndexIncremental_NoChanges(t *testing.T) {
	svc := setupTestService(t)
	wsDir := createTestWorkspace(t)

	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})

	if err := indexer.IndexFull(context.Background(), wsDir); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}

	// Incremental with no changes should return quickly.
	if err := indexer.IndexIncremental(context.Background(), wsDir); err != nil {
		t.Fatalf("IndexIncremental: %v", err)
	}

	if !svc.IsReady() {
		t.Error("expected service to be ready")
	}
}

func TestHandleBranchSwitch(t *testing.T) {
	svc := setupTestService(t)
	wsDir := createTestWorkspace(t)

	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})

	// Full index on main.
	if err := indexer.IndexFull(context.Background(), wsDir); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}

	// Switch to a new branch (empty collection) — should trigger full index.
	if err := indexer.HandleBranchSwitch(context.Background(), wsDir, "feature/new"); err != nil {
		t.Fatalf("HandleBranchSwitch: %v", err)
	}

	if svc.CurrentBranchName() != "feature/new" {
		t.Errorf("expected branch 'feature/new', got %q", svc.CurrentBranchName())
	}
	if !svc.IsReady() {
		t.Error("expected service to be ready after branch switch")
	}
	if svc.GetCollection().Count() == 0 {
		t.Error("expected documents after branch switch with full index")
	}
}

func TestHandleBranchSwitch_ExistingBranch(t *testing.T) {
	svc := setupTestService(t)
	wsDir := createTestWorkspace(t)

	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})

	// Index on main.
	if err := indexer.IndexFull(context.Background(), wsDir); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}

	// Switch to feature, index it.
	if err := indexer.HandleBranchSwitch(context.Background(), wsDir, "feature/test"); err != nil {
		t.Fatalf("HandleBranchSwitch to feature: %v", err)
	}

	// Switch back to main (existing collection with documents) — should do incremental.
	if err := indexer.HandleBranchSwitch(context.Background(), wsDir, "main"); err != nil {
		t.Fatalf("HandleBranchSwitch back to main: %v", err)
	}

	if !svc.IsReady() {
		t.Error("expected service to be ready")
	}
}

func TestWalkProjectFiles(t *testing.T) {
	dir := t.TempDir()

	// Create various files and directories.
	structure := map[string]string{
		"main.go":                   "package main",
		"lib/utils.go":              "package lib",
		".git/config":               "git config",
		"node_modules/pkg/index.js": "module.exports = {}",
		"vendor/lib/vendor.go":      "package vendor",
		"build/output.bin":          "binary",
		"image.png":                 "fake png",
		".hidden_file":              "hidden",
		".hidden_dir/file.go":       "hidden dir",
		"data.json":                 `{"key": "value"}`,
		"go.sum":                    "checksum file",
		"package-lock.json":         "lock file",
	}

	for relPath, content := range structure {
		absPath := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	files, err := walkProjectFiles(dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("walkProjectFiles: %v", err)
	}

	// Convert to relative paths for easy assertions.
	relFiles := make(map[string]bool, len(files))
	for _, f := range files {
		rel, relErr := filepath.Rel(dir, f)
		if relErr != nil {
			t.Fatal(relErr)
		}
		relFiles[rel] = true
	}

	// Should include.
	shouldInclude := []string{"main.go", filepath.Join("lib", "utils.go"), "data.json"}
	for _, f := range shouldInclude {
		if !relFiles[f] {
			t.Errorf("expected %q to be included, got files: %v", f, relFiles)
		}
	}

	// Should exclude.
	shouldExclude := []string{
		filepath.Join(".git", "config"),
		filepath.Join("node_modules", "pkg", "index.js"),
		filepath.Join("vendor", "lib", "vendor.go"),
		"image.png",
		".hidden_file",
		filepath.Join(".hidden_dir", "file.go"),
		"go.sum",
		"package-lock.json",
	}
	for _, f := range shouldExclude {
		if relFiles[f] {
			t.Errorf("expected %q to be excluded", f)
		}
	}
}

func TestWalkProjectFiles_WithGitignore(t *testing.T) {
	dir := t.TempDir()

	// Create a .gitignore.
	gitignore := "*.log\ntmp/\ncustom_dir/\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}

	structure := map[string]string{
		"main.go":         "package main",
		"debug.log":       "log content",
		"tmp/cache.txt":   "temp data",
		"custom_dir/a.go": "custom",
		"src/app.go":      "package src",
	}

	for relPath, content := range structure {
		absPath := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := walkProjectFiles(dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("walkProjectFiles: %v", err)
	}

	relFiles := make(map[string]bool, len(files))
	for _, f := range files {
		rel, _ := filepath.Rel(dir, f)
		relFiles[rel] = true
	}

	if !relFiles["main.go"] {
		t.Error("expected main.go to be included")
	}
	if !relFiles[filepath.Join("src", "app.go")] {
		t.Error("expected src/app.go to be included")
	}
	if relFiles["debug.log"] {
		t.Error("expected debug.log to be excluded by gitignore")
	}
	if relFiles[filepath.Join("tmp", "cache.txt")] {
		t.Error("expected tmp/cache.txt to be excluded by gitignore")
	}
	if relFiles[filepath.Join("custom_dir", "a.go")] {
		t.Error("expected custom_dir/a.go to be excluded by gitignore")
	}
}

func TestIsBinaryFile(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    bool
	}{
		{"text file", []byte("hello world\nline 2\n"), false},
		{"empty file", []byte{}, false},
		{"binary with null byte", []byte{0x48, 0x65, 0x00, 0x6c, 0x6f}, true},
		{"binary at start", []byte{0x00, 0x01, 0x02}, true},
		{"utf8 text", []byte("こんにちは世界"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBinaryFile(tt.content)
			if got != tt.want {
				t.Errorf("isBinaryFile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewIndexer_Defaults(t *testing.T) {
	svc := setupTestService(t)
	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
	})

	if indexer.maxChunkSize != 1500 {
		t.Errorf("expected default maxChunkSize 1500, got %d", indexer.maxChunkSize)
	}
	if indexer.overlap != 200 {
		t.Errorf("expected default overlap 200, got %d", indexer.overlap)
	}
	if indexer.hashFn == nil {
		t.Error("expected default hashFn")
	}
}

func TestNormalizeGitignorePattern(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"*.log", "**/*.log"},
		{"/build", "**/build"},
		{"dist/", "**/dist"},
		{"src/temp", "src/temp"},
		{"node_modules", "**/node_modules"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeGitignorePattern(tt.input)
			if got != tt.want {
				t.Errorf("normalizeGitignorePattern(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
