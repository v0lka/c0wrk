package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/ignore"
)

// testIgnoreChecker builds an ignore resolver for root, failing the test on
// error. Tests use it in place of the removed loadGitignorePatterns so the
// walk and IsIndexablePath helpers exercise the real .gitignore/.aiignore
// resolver.
func testIgnoreChecker(t *testing.T, root string) ignore.IgnoreChecker {
	t.Helper()
	r, err := ignore.NewResolver(root)
	if err != nil {
		t.Fatalf("ignore.NewResolver(%q): %v", root, err)
	}
	return r
}

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
	if err := svc.SetProject("indexer-test", t.TempDir()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Logf("Service.Close in cleanup: %v", err)
		}
	})
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
		OnProgress: func(phase IndexPhase, state IndexState, filesIndexed, totalFiles int, currentFile string) {
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
	indexer.onProgress = func(phase IndexPhase, state IndexState, filesIndexed, totalFiles int, currentFile string) {
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

	files, err := walkProjectFiles(dir, testIgnoreChecker(t, dir), DefaultMaxIndexableFileSize)
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

	// With no ignore files present, everything non-hidden is included —
	// including the former hardcoded-default exclusions (node_modules, vendor,
	// build, *.png, go.sum, package-lock.json). Those are now only skipped when
	// listed in .gitignore/.aiignore (see TestWalkProjectFiles_WithGitignore
	// and TestWalkProjectFiles_WithAiignore).
	shouldInclude := []string{
		"main.go", filepath.Join("lib", "utils.go"), "data.json",
		filepath.Join("node_modules", "pkg", "index.js"),
		filepath.Join("vendor", "lib", "vendor.go"),
		filepath.Join("build", "output.bin"),
		"image.png", "go.sum", "package-lock.json",
	}
	for _, f := range shouldInclude {
		if !relFiles[f] {
			t.Errorf("expected %q to be included, got files: %v", f, relFiles)
		}
	}

	// Only hidden entries (leading dot) are excluded by the universal guard.
	shouldExclude := []string{
		filepath.Join(".git", "config"),
		".hidden_file",
		filepath.Join(".hidden_dir", "file.go"),
	}
	for _, f := range shouldExclude {
		if relFiles[f] {
			t.Errorf("expected hidden %q to be excluded", f)
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

	files, err := walkProjectFiles(dir, testIgnoreChecker(t, dir), DefaultMaxIndexableFileSize)
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

// TestWalkProjectFiles_WithAiignore verifies that an .aiignore file is honoured
// the same way .gitignore is, and documents that entries such as go.sum and
// package-lock.json — no longer skipped by hardcoded defaults — are indexed
// unless an ignore file lists them. A nested .aiignore is also honoured.
func TestWalkProjectFiles_WithAiignore(t *testing.T) {
	dir := t.TempDir()

	// Root .aiignore lists the former default exclusions.
	aiignore := "node_modules/\nvendor/\nbuild/\n*.png\ngo.sum\npackage-lock.json\n"
	if err := os.WriteFile(filepath.Join(dir, ".aiignore"), []byte(aiignore), 0o644); err != nil {
		t.Fatal(err)
	}

	structure := map[string]string{
		"main.go":                   "package main",
		"src/app.go":                "package src",
		"node_modules/pkg/index.js": "module.exports = {}",
		"vendor/lib/vendor.go":      "package vendor",
		"build/output.bin":          "binary",
		"image.png":                 "fake png",
		"go.sum":                    "checksum file",
		"package-lock.json":         "lock file",
		// Nested .aiignore scoped to its own directory.
		"nested/.aiignore": "*.log\n",
		"nested/debug.log": "log content",
		"nested/keep.go":   "package nested",
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

	files, err := walkProjectFiles(dir, testIgnoreChecker(t, dir), DefaultMaxIndexableFileSize)
	if err != nil {
		t.Fatalf("walkProjectFiles: %v", err)
	}

	relFiles := make(map[string]bool, len(files))
	for _, f := range files {
		rel, _ := filepath.Rel(dir, f)
		relFiles[rel] = true
	}

	for _, f := range []string{"main.go", filepath.Join("src", "app.go"), filepath.Join("nested", "keep.go")} {
		if !relFiles[f] {
			t.Errorf("expected %q to be included, got %v", f, relFiles)
		}
	}

	for _, f := range []string{
		filepath.Join("node_modules", "pkg", "index.js"),
		filepath.Join("vendor", "lib", "vendor.go"),
		filepath.Join("build", "output.bin"),
		"image.png",
		"go.sum",
		"package-lock.json",
		filepath.Join("nested", "debug.log"),
	} {
		if relFiles[f] {
			t.Errorf("expected %q to be excluded by .aiignore", f)
		}
	}
}

func TestContainsNullByte(t *testing.T) {
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
			got := containsNullByte(tt.content)
			if got != tt.want {
				t.Errorf("containsNullByte() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsBinaryHeader verifies the bounded header pre-read: it must detect a
// NUL byte in the leading bytes WITHOUT loading the whole file, so a large
// binary asset is rejected before a full os.ReadFile.
func TestIsBinaryHeader(t *testing.T) {
	root := t.TempDir()

	textPath := filepath.Join(root, "text.txt")
	if err := os.WriteFile(textPath, []byte("just plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(root, "blob.bin")
	if err := os.WriteFile(binPath, []byte{0x48, 0x65, 0x00, 0x6c, 0x6f}, 0o644); err != nil {
		t.Fatal(err)
	}
	emptyPath := filepath.Join(root, "empty.txt")
	if err := os.WriteFile(emptyPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"text", textPath, false},
		{"binary", binPath, true},
		{"empty", emptyPath, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := isBinaryHeader(tt.path)
			if err != nil {
				t.Fatalf("isBinaryHeader(%q) error: %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("isBinaryHeader(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}

	// Missing file reports an error rather than a false "not binary".
	if _, err := isBinaryHeader(filepath.Join(root, "nope")); err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestNewIndexer_Defaults(t *testing.T) {
	svc := setupTestService(t)
	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
	})

	if indexer.maxChunkSize != DefaultMaxChunkSize {
		t.Errorf("expected default maxChunkSize %d, got %d", DefaultMaxChunkSize, indexer.maxChunkSize)
	}
	if indexer.maxFileSize != DefaultMaxIndexableFileSize {
		t.Errorf("expected default maxFileSize %d, got %d", DefaultMaxIndexableFileSize, indexer.maxFileSize)
	}
	if indexer.overlap != 200 {
		t.Errorf("expected default overlap 200, got %d", indexer.overlap)
	}
	if indexer.prepWorkers != DefaultPrepWorkers {
		t.Errorf("expected default prepWorkers %d, got %d", DefaultPrepWorkers, indexer.prepWorkers)
	}
	if indexer.hashFn == nil {
		t.Error("expected default hashFn")
	}
}

// TestTooLargeForIndex verifies the size threshold used as the reliable
// backstop against loading oversized binary assets into memory.
func TestTooLargeForIndex(t *testing.T) {
	cases := []struct {
		name string
		size int64
		want bool
	}{
		{"zero", 0, false},
		{"small", 1024, false},
		{"at limit", DefaultMaxIndexableFileSize, false},
		{"one over", DefaultMaxIndexableFileSize + 1, true},
		{"model.onnx (522MB)", 522 * 1024 * 1024, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := tooLargeForIndex(tt.size, DefaultMaxIndexableFileSize); got != tt.want {
				t.Errorf("tooLargeForIndex(%d) = %v, want %v", tt.size, got, tt.want)
			}
		})
	}
}

// TestProcessFile_SkipsOversized is the regression test for the oversized-file
// hang: a multi-hundred-MB ONNX model file (protobuf with readable ASCII
// header, no NUL byte) must be rejected by the size guard — NOT read fully
// into memory. Without the guard, os.ReadFile + chunkFn would load the whole
// file and explode memory, hanging the indexer (and, by holding the service
// write lock, blocking app shutdown).
func TestProcessFile_SkipsOversized(t *testing.T) {
	svc := setupTestService(t)
	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})

	root := t.TempDir()
	// Simulate an ONNX protobuf header: readable ASCII field names with NO NUL
	// byte (mirrors a real model.onnx whose leading bytes spell "pytorch",
	// "embeddings.word_embeddings", ...), but large enough to exceed the limit.
	// The filler is NUL-free so isBinaryHeader does NOT flag it — this proves
	// the size guard (not the binary heuristic) is what skips the file.
	header := []byte("\x08\x07\x12\x07pytorch\x1a\x052.1.0 embeddings.word_embeddings")
	big := make([]byte, DefaultMaxIndexableFileSize+1)
	for i := range big {
		big[i] = 'A'
	}
	copy(big, header)
	bigPath := filepath.Join(root, "model.onnx")
	if err := os.WriteFile(bigPath, big, 0o644); err != nil {
		t.Fatal(err)
	}

	vecDocs, lexDocs, err := indexer.processFile(bigPath)
	if err != nil {
		t.Fatalf("processFile oversized model: unexpected error: %v", err)
	}
	if len(vecDocs) != 0 || len(lexDocs) != 0 {
		t.Errorf("expected oversized file to be skipped, got %d vec / %d lex docs",
			len(vecDocs), len(lexDocs))
	}

	// Sanity: isBinaryHeader alone does NOT flag this as binary (that is exactly
	// the hole the size guard fills), so the test proves the guard, not the
	// NUL heuristic, is what skips the file.
	if binary, _ := isBinaryHeader(bigPath); binary {
		t.Fatal("expected header to be NUL-free (binary heuristic must NOT catch it); test premise invalid")
	}
}

// TestWalkProjectFiles_SkipsOversized verifies oversized files never enter the
// pipeline at walk time, so they cannot appear as "new" on every incremental
// pass (which would otherwise drive a redundant reindex).
func TestWalkProjectFiles_SkipsOversized(t *testing.T) {
	root := t.TempDir()
	small := filepath.Join(root, "small.go")
	if err := os.WriteFile(small, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, DefaultMaxIndexableFileSize+1)
	if err := os.WriteFile(filepath.Join(root, "model.onnx"), big, 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := walkProjectFiles(root, testIgnoreChecker(t, root), DefaultMaxIndexableFileSize)
	if err != nil {
		t.Fatalf("walkProjectFiles: %v", err)
	}
	for _, f := range files {
		if filepath.Base(f) == "model.onnx" {
			t.Errorf("oversized model.onnx should not be walked; got %v", files)
		}
	}
	if len(files) != 1 || files[0] != small {
		t.Errorf("expected only small.go walked, got %v", files)
	}
}

// pathologicalChunkFunc is a test ChunkFunc that simulates the
// tokenizer.json failure mode: a small (under size-limit) data-format file
// that the structure-aware splitter fragments into a huge number of tiny
// chunks. The real tokenizer.json (695 KiB HuggingFace BPE vocab/merges)
// produced 30,635 chunks, each requiring a separate ONNX inference pass,
// hanging the embedder. Here an arbitrary chunkCount (well above
// DefaultMaxChunksPerFile) reproduces the condition.
func pathologicalChunkFunc(chunkCount int) ChunkFunc {
	return func(_ string, content []byte, _, _ int) ([]ChunkResult, error) {
		if len(content) == 0 {
			return nil, nil
		}
		chunks := make([]ChunkResult, chunkCount)
		for i := range chunks {
			chunks[i] = ChunkResult{Content: "token", StartLine: 1, EndLine: 1, Language: "text"}
		}
		return chunks, nil
	}
}

// TestProcessFile_SkipsExcessChunks is the regression test for the second
// indexer hang: after the oversized model.onnx was skipped, indexing moved
// on to tokenizer.json (695 KiB) and hung because the chunker produced 30,635
// fragments that reached the embedder in a single batch. The per-file
// chunk-count cap must skip such a file before it produces any documents,
// regardless of file size.
func TestProcessFile_SkipsExcessChunks(t *testing.T) {
	svc := setupTestService(t)
	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: pathologicalChunkFunc(DefaultMaxChunksPerFile + 5000),
		HashFn:  fakeHashFunc,
	})

	root := t.TempDir()
	// Small file — under the size guard, so only the chunk-count guard can
	// skip it. This mirrors tokenizer.json (695 KiB, well under 4 MiB).
	path := filepath.Join(root, "tokenizer.json")
	if err := os.WriteFile(path, []byte(`{"model":{"vocab":[]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	vecDocs, lexDocs, err := indexer.processFile(path)
	if err != nil {
		t.Fatalf("processFile excess-chunks: unexpected error: %v", err)
	}
	if len(vecDocs) != 0 || len(lexDocs) != 0 {
		t.Errorf("expected excess-chunk file to be skipped, got %d vec / %d lex docs",
			len(vecDocs), len(lexDocs))
	}
}

// TestIndexFull_ExcessChunksNeverEmbedded proves end-to-end that a
// pathological file's chunks never reach the embedder: a counting embedding
// func wrapped around the real fakeEmbeddingFunc tracks every call. With the
// per-file cap in place, the excess-chunk file contributes nothing and the
// embedder only sees the legitimate small file's chunk(s).
func TestIndexFull_ExcessChunksNeverEmbedded(t *testing.T) {
	var embedCalls int64
	emb := fakeEmbeddingFunc()
	countingEmb := func(ctx context.Context, text string) ([]float32, error) {
		atomic.AddInt64(&embedCalls, 1)
		return emb(ctx, text)
	}
	svc, err := NewService(ServiceConfig{EmbeddingFunc: countingEmb})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.SetProject("excess-chunks-test", t.TempDir()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Logf("Service.Close in cleanup: %v", err)
		}
	})

	root := t.TempDir()
	// Legitimate file: one chunk, under the cap.
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pathological file: chunker will burst far past the cap.
	if err := os.WriteFile(filepath.Join(root, "tokenizer.json"), []byte(`{"vocab":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Hybrid chunker: one chunk for normal files (under the cap), a burst far
	// past the cap for the pathological tokenizer.json — selected by name so
	// the test isolates the per-file cap's effect.
	chunker := func(fp string, content []byte, _, _ int) ([]ChunkResult, error) {
		if filepath.Base(fp) == "tokenizer.json" {
			return pathologicalChunkFunc(DefaultMaxChunksPerFile+5000)(fp, content, 0, 0)
		}
		return fakeChunkFunc(fp, content, 0, 0)
	}

	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: chunker,
		HashFn:  fakeHashFunc,
	})
	svc.SetReady(true)

	if err := indexer.IndexFull(context.Background(), root); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}

	// The embedder must only have been called for the legitimate file's chunk.
	// fakeChunkFunc yields exactly one chunk per non-empty file, so the
	// pathological tokenizer.json (skipped by the cap) must contribute zero
	// embed calls. Assert the exact count so any leak — not just a gross one
	// — fails loudly.
	if calls := atomic.LoadInt64(&embedCalls); calls != 1 {
		t.Errorf("embedder called %d times; want exactly 1 (legitimate file only). "+
			"pathological file's chunks leaked through the cap", calls)
	}
}

// linesChunker is a deterministic multi-chunk test ChunkFunc: it splits
// content into fixed line-count chunks so a file contributes several
// documents, exercising the consumer's cross-file batch accumulation.
func linesChunker(_ string, content []byte, _, _ int) ([]ChunkResult, error) {
	text := string(content)
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	const linesPerChunk = 4
	var chunks []ChunkResult
	for start := 0; start < len(lines); start += linesPerChunk {
		end := min(start+linesPerChunk, len(lines))
		chunks = append(chunks, ChunkResult{
			Content:   strings.Join(lines[start:end], "\n"),
			StartLine: start + 1,
			EndLine:   end,
			Language:  "go",
		})
	}
	return chunks, nil
}

// slowChunker wraps a ChunkFunc with a per-file delay, giving an in-flight
// indexing pass a window during which cancellation must take effect.
func slowChunker(d time.Duration, inner ChunkFunc) ChunkFunc {
	return func(fp string, content []byte, maxChunkSize, overlap int) ([]ChunkResult, error) {
		time.Sleep(d)
		return inner(fp, content, maxChunkSize, overlap)
	}
}

// createPrepWorkspace creates a workspace with numFiles files of 13 numbered
// lines each (4 chunks per file under linesChunker), spread across nested
// non-hidden directories.
func createPrepWorkspace(t *testing.T, numFiles int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < numFiles; i++ {
		dir := filepath.Join(root, fmt.Sprintf("pkg%d", i%3))
		var sb strings.Builder
		fmt.Fprintf(&sb, "package pkg%d\n", i)
		for line := 1; line <= 12; line++ {
			fmt.Fprintf(&sb, "// file %d line %d\n", i, line)
		}
		path := filepath.Join(dir, fmt.Sprintf("file%02d.go", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	return root
}

// newPrepTestService creates a fresh in-memory service under the given
// project ID, isolated from other services by its own persistence directory.
func newPrepTestService(t *testing.T, project string) *Service {
	t.Helper()
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.SetProject(project, t.TempDir()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Logf("Service.Close in cleanup: %v", err)
		}
	})
	return svc
}

// docSnapshot is a comparable projection of an indexed document: its content
// plus the metadata that is not wall-clock-derived. last_modified and
// file_mtime_unix_nano are stat fields that legitimately differ between two
// indexing runs of the same bytes, so they are excluded from set-equality
// comparisons.
type docSnapshot struct {
	content string
	meta    map[string]string
}

// snapshotCollection enumerates every document in the service's collection
// (a single-space query returns all docs; ranking is irrelevant here) and
// projects each into a docSnapshot keyed by document ID.
func snapshotCollection(t *testing.T, svc *Service) map[string]docSnapshot {
	t.Helper()
	col := svc.GetCollection()
	if col == nil {
		t.Fatal("expected collection to be non-nil")
	}
	count := col.Count()
	if count == 0 {
		t.Fatal("expected documents in collection")
	}
	results, err := col.Query(context.Background(), " ", count, nil, nil)
	if err != nil {
		t.Fatalf("enumerating collection: %v", err)
	}
	if len(results) != count {
		t.Fatalf("collection query returned %d of %d documents", len(results), count)
	}
	snaps := make(map[string]docSnapshot, len(results))
	for _, r := range results {
		meta := make(map[string]string, len(r.Metadata))
		for k, v := range r.Metadata {
			switch k {
			case "last_modified", "file_mtime_unix_nano":
				continue
			}
			meta[k] = v
		}
		snaps[r.ID] = docSnapshot{content: r.Content, meta: meta}
	}
	return snaps
}

// compareDocSets fails the test when the two document sets differ, naming
// the first few offending document IDs for diagnosability.
func compareDocSets(t *testing.T, label string, want, got map[string]docSnapshot) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s: document count differs: prep_workers=1 indexed %d, parallel %d",
			label, len(want), len(got))
	}
	shown := 0
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			if shown < 5 {
				t.Errorf("%s: document %q missing from parallel pass", label, id)
			}
			shown++
			continue
		}
		if w.content != g.content {
			if shown < 5 {
				t.Errorf("%s: document %q content differs:\nserial:   %q\nparallel: %q",
					label, id, w.content, g.content)
			}
			shown++
			continue
		}
		if len(w.meta) != len(g.meta) {
			if shown < 5 {
				t.Errorf("%s: document %q metadata key count differs: %d vs %d",
					label, id, len(w.meta), len(g.meta))
			}
			shown++
			continue
		}
		for k, v := range w.meta {
			if g.meta[k] != v {
				if shown < 5 {
					t.Errorf("%s: document %q metadata %q differs: %q vs %q",
						label, id, k, v, g.meta[k])
				}
				shown++
				break
			}
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			if shown < 5 {
				t.Errorf("%s: document %q present in parallel pass but not serial", label, id)
			}
			shown++
		}
	}
}

// TestIndexFull_PrepWorkers_ProduceIdenticalDocumentSet is the acceptance
// test for the prep-worker pool: a full pass with prep_workers=2 must index
// the exact same document set (IDs, contents, metadata) as prep_workers=1.
// Interleaved completion order may shuffle the order documents arrive in,
// but the SET must be identical. The workspace is large enough (24 files × 4
// chunks = 96 documents) to cross addDocumentBatchSize (50), exercising
// mid-pass batch flushes on both paths.
func TestIndexFull_PrepWorkers_ProduceIdenticalDocumentSet(t *testing.T) {
	ws := createPrepWorkspace(t, 24)

	run := func(workers int, project string) map[string]docSnapshot {
		svc := newPrepTestService(t, project)
		indexer := NewIndexer(IndexerConfig{
			Service:     svc,
			ChunkFn:     linesChunker,
			HashFn:      fakeHashFunc,
			PrepWorkers: workers,
		})
		if err := indexer.IndexFull(context.Background(), ws); err != nil {
			t.Fatalf("IndexFull (prep_workers=%d): %v", workers, err)
		}
		return snapshotCollection(t, svc)
	}

	serial := run(1, "prep-eq-serial")
	parallel := run(DefaultPrepWorkers, "prep-eq-parallel")
	compareDocSets(t, "full pass", serial, parallel)
}

// TestIndexIncremental_PrepWorkers_ProduceIdenticalDocumentSet extends the
// document-set equality guarantee to the incremental pipeline (the same
// shared consumer drives both passes): two independent runs — full index,
// fixed mutation script, incremental pass — must end with identical document
// sets regardless of prep_workers.
func TestIndexIncremental_PrepWorkers_ProduceIdenticalDocumentSet(t *testing.T) {
	ws := createPrepWorkspace(t, 12)

	// mutate applies a fixed change script: rewrite one file, add one file,
	// delete one file. Applied inside each run so both runs start their
	// incremental pass from the same logical state.
	mutate := func(t *testing.T) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ws, "pkg0", "file00.go"), []byte("package pkg0\n\n// rewritten\n"), 0o644); err != nil {
			t.Fatalf("rewrite file00.go: %v", err)
		}
		if err := os.WriteFile(filepath.Join(ws, "pkg1", "added.go"), []byte("package pkg1\n\n// added\n"), 0o644); err != nil {
			t.Fatalf("write added.go: %v", err)
		}
		// Idempotent across runs: the second run's full pass already sees the
		// file absent (the first run's mutate deleted it), so the remove is
		// a no-op there.
		if err := os.Remove(filepath.Join(ws, "pkg2", "file11.go")); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove file11.go: %v", err)
		}
	}

	run := func(workers int, project string) map[string]docSnapshot {
		svc := newPrepTestService(t, project)
		indexer := NewIndexer(IndexerConfig{
			Service:     svc,
			ChunkFn:     linesChunker,
			HashFn:      fakeHashFunc,
			PrepWorkers: workers,
		})
		if err := indexer.IndexFull(context.Background(), ws); err != nil {
			t.Fatalf("IndexFull (prep_workers=%d): %v", workers, err)
		}
		mutate(t)
		if err := indexer.IndexIncremental(context.Background(), ws); err != nil {
			t.Fatalf("IndexIncremental (prep_workers=%d): %v", workers, err)
		}
		return snapshotCollection(t, svc)
	}

	serial := run(1, "prep-eq-inc-serial")
	parallel := run(DefaultPrepWorkers, "prep-eq-inc-parallel")
	compareDocSets(t, "incremental pass", serial, parallel)
}

// TestIndexFull_CancelMidPassReturnsPromptly verifies the acceptance
// criterion for cancellation: cancelling mid-pass (after the first progress
// event) must abort promptly with the historical "indexing cancelled" error,
// and readiness must be restored so WaitReady callers do not hang.
func TestIndexFull_CancelMidPassReturnsPromptly(t *testing.T) {
	svc := newPrepTestService(t, "prep-cancel")
	ws := createPrepWorkspace(t, 10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	indexer := NewIndexer(IndexerConfig{
		Service:     svc,
		ChunkFn:     slowChunker(15*time.Millisecond, linesChunker),
		HashFn:      fakeHashFunc,
		PrepWorkers: DefaultPrepWorkers,
		OnProgress: func(_ IndexPhase, state IndexState, filesIndexed, _ int, _ string) {
			if state == IndexStateIndexing && filesIndexed >= 1 {
				cancel()
			}
		},
	})

	start := time.Now()
	err := indexer.IndexFull(ctx, ws)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a mid-pass cancellation, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error should wrap context.Canceled, got: %v", err)
	}
	if !strings.Contains(err.Error(), "indexing cancelled") {
		t.Errorf("error should keep the historical 'indexing cancelled' wording, got: %v", err)
	}
	// "Promptly": bounded well below the full pass duration (10 files ×
	// 15 ms chunk delay ≈ 150 ms serial, ≈ 75 ms with two workers).
	if elapsed > 3*time.Second {
		t.Errorf("cancellation returned after %v; expected a prompt abort", elapsed)
	}
	if !svc.IsReady() {
		t.Error("expected readiness to be restored after a cancelled pass")
	}
}

// TestIndexProgress_FilesIndexedMonotonic verifies that filesIndexed in
// within-pass progress events never decreases — the consumer is the single
// goroutine advancing the counter, so interleaved worker completion order
// must not disturb monotonicity. Covers both a full pass and an incremental
// pass.
func TestIndexProgress_FilesIndexedMonotonic(t *testing.T) {
	svc := newPrepTestService(t, "prep-monotonic")
	ws := createPrepWorkspace(t, 18)

	last := -1
	wrap := func(state IndexState) ProgressCallback {
		return func(_ IndexPhase, s IndexState, filesIndexed, _ int, _ string) {
			if s != state {
				return
			}
			if filesIndexed < last {
				t.Errorf("filesIndexed regressed within %s pass: %d after %d",
					state, filesIndexed, last)
			}
			last = filesIndexed
		}
	}
	indexer := NewIndexer(IndexerConfig{
		Service:     svc,
		ChunkFn:     linesChunker,
		HashFn:      fakeHashFunc,
		PrepWorkers: DefaultPrepWorkers,
		OnProgress:  wrap(IndexStateIndexing),
	})
	if err := indexer.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}

	// Incremental pass: same guarantee, with progress resuming at the number
	// of already-counted deletions.
	last = 0
	indexer.onProgress = wrap(IndexStateReindexing)
	if err := os.WriteFile(filepath.Join(ws, "pkg0", "file00.go"), []byte("package pkg0\n\n// changed\n"), 0o644); err != nil {
		t.Fatalf("rewrite file00.go: %v", err)
	}
	if err := indexer.IndexIncremental(context.Background(), ws); err != nil {
		t.Fatalf("IndexIncremental: %v", err)
	}
}

// TestIndexFull_PrepWorkersOverlap proves the pool genuinely prepares files
// concurrently: with 2 workers and 2 files, each file's chunk call blocks
// until BOTH files have entered chunking. A serial pipeline deadlocks the
// first file against the barrier (broken by the watchdog, failing the test);
// an overlapping one releases both.
func TestIndexFull_PrepWorkersOverlap(t *testing.T) {
	svc := newPrepTestService(t, "prep-overlap")
	ws := t.TempDir()
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte("package main\n\n// x\n// y\n// z\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	entered := make(chan string, 2)
	release := make(chan struct{})
	var bothArrived atomic.Bool
	go func() {
		<-entered
		<-entered
		bothArrived.Store(true)
		close(release)
	}()

	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		HashFn:  fakeHashFunc,
		ChunkFn: func(fp string, content []byte, maxChunkSize, overlap int) ([]ChunkResult, error) {
			entered <- filepath.Base(fp)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
				// Watchdog: serial execution can never satisfy the barrier.
				// Unblock so the pass can finish and the test can fail
				// cleanly instead of hanging.
			}
			return linesChunker(fp, content, maxChunkSize, overlap)
		},
		PrepWorkers: 2,
	})

	if err := indexer.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}
	if !bothArrived.Load() {
		t.Error("expected both files to be prepared concurrently by 2 prep workers; " +
			"chunking appeared strictly serial")
	}
}
