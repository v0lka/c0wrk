package vectorindex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/backend/vectorindex/lexical"
)

// fakeEmbeddingFunc returns a deterministic embedding for testing.
// It creates a simple hash-based vector of fixed dimension.
func fakeEmbeddingFunc() chromem.EmbeddingFunc {
	return func(_ context.Context, text string) ([]float32, error) {
		vec := make([]float32, 8)
		for i, c := range text {
			vec[i%8] += float32(c) / 1000.0
		}
		return vec, nil
	}
}

func TestNewService(t *testing.T) {
	t.Run("nil embedding func returns error", func(t *testing.T) {
		_, err := NewService(ServiceConfig{})
		if err == nil {
			t.Fatal("expected error for nil EmbeddingFunc")
		}
	})

	t.Run("valid config succeeds", func(t *testing.T) {
		svc, err := NewService(ServiceConfig{
			EmbeddingFunc: fakeEmbeddingFunc(),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if svc == nil {
			t.Fatal("expected non-nil service")
		}
	})
}

func TestSetProject(t *testing.T) {
	t.Run("in-memory mode", func(t *testing.T) {
		svc, err := NewService(ServiceConfig{
			EmbeddingFunc: fakeEmbeddingFunc(),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := svc.SetProject("test-project"); err != nil {
			t.Fatalf("SetProject failed: %v", err)
		}
		if svc.db == nil {
			t.Fatal("expected db to be initialized")
		}
	})

	t.Run("persistent mode creates directory", func(t *testing.T) {
		dir := t.TempDir()
		svc, err := NewService(ServiceConfig{
			PersistPath:   dir,
			EmbeddingFunc: fakeEmbeddingFunc(),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := svc.SetProject("my-project"); err != nil {
			t.Fatalf("SetProject failed: %v", err)
		}

		projectDir := filepath.Join(dir, "my-project")
		info, statErr := os.Stat(projectDir)
		if statErr != nil {
			t.Fatalf("project directory not created: %v", statErr)
		}
		if !info.IsDir() {
			t.Fatal("expected project path to be a directory")
		}
	})
}

func TestSwitchBranch(t *testing.T) {
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("fails without project", func(t *testing.T) {
		err := svc.SwitchBranch(context.Background(), "main")
		if err == nil {
			t.Fatal("expected error without project set")
		}
	})

	if err := svc.SetProject("test"); err != nil {
		t.Fatalf("SetProject failed: %v", err)
	}

	t.Run("switches to main", func(t *testing.T) {
		if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
			t.Fatalf("SwitchBranch failed: %v", err)
		}
		if svc.currentBranch != "main" {
			t.Fatalf("expected branch 'main', got %q", svc.currentBranch)
		}
		if svc.collection == nil {
			t.Fatal("expected collection to be set")
		}
	})

	t.Run("switches to feature branch", func(t *testing.T) {
		if err := svc.SwitchBranch(context.Background(), "feature/my-feature"); err != nil {
			t.Fatalf("SwitchBranch failed: %v", err)
		}
		if svc.currentBranch != "feature/my-feature" {
			t.Fatalf("expected branch 'feature/my-feature', got %q", svc.currentBranch)
		}
	})

	t.Run("no-op for same branch", func(t *testing.T) {
		prevCol := svc.collection
		if err := svc.SwitchBranch(context.Background(), "feature/my-feature"); err != nil {
			t.Fatalf("SwitchBranch failed: %v", err)
		}
		if svc.collection != prevCol {
			t.Fatal("expected same collection for same branch")
		}
	})
}

func TestCollectionName(t *testing.T) {
	tests := []struct {
		branch string
		want   string
	}{
		{"main", "branch_main"},
		{"feature/my-feature", "branch_feature_my-feature"},
		{"release/v1.0.0", "branch_release_v100"},
		{"", "branch_default"},
	}
	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			got := collectionName(tt.branch)
			if got != tt.want {
				t.Errorf("collectionName(%q) = %q, want %q", tt.branch, got, tt.want)
			}
		})
	}
}

func TestReadiness(t *testing.T) {
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("initially not ready", func(t *testing.T) {
		if svc.IsReady() {
			t.Fatal("expected service to not be ready initially")
		}
	})

	t.Run("WaitReady blocks then unblocks", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)

		var waitErr error
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			waitErr = svc.WaitReady(ctx)
		}()

		// Give goroutine time to start waiting.
		time.Sleep(50 * time.Millisecond)

		// Set ready.
		svc.SetReady(true)
		wg.Wait()

		if waitErr != nil {
			t.Fatalf("WaitReady returned error: %v", waitErr)
		}
		if !svc.IsReady() {
			t.Fatal("expected service to be ready")
		}
	})

	t.Run("WaitReady returns immediately when ready", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := svc.WaitReady(ctx); err != nil {
			t.Fatalf("WaitReady returned error: %v", err)
		}
	})

	t.Run("WaitReady respects context cancellation", func(t *testing.T) {
		svc.SetReady(false)

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := svc.WaitReady(ctx)
		if err == nil {
			t.Fatal("expected error from cancelled context")
		}
	})
}

func TestSearchWithDocuments(t *testing.T) {
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := svc.SetProject("search-test"); err != nil {
		t.Fatalf("SetProject failed: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch failed: %v", err)
	}

	// Add test documents.
	docs := []chromem.Document{
		{
			ID:      "file1:0",
			Content: "func main() { fmt.Println(\"hello world\") }",
			Metadata: map[string]string{
				"file_path":    "/project/main.go",
				"file_name":    "main.go",
				"content_hash": "abc123",
				"start_line":   "1",
				"end_line":     "5",
				"language":     "go",
			},
		},
		{
			ID:      "file2:0",
			Content: "package utils\nfunc Add(a, b int) int { return a + b }",
			Metadata: map[string]string{
				"file_path":    "/project/utils.go",
				"file_name":    "utils.go",
				"content_hash": "def456",
				"start_line":   "1",
				"end_line":     "3",
				"language":     "go",
			},
		},
	}

	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments failed: %v", err)
	}
	svc.ReleaseWriteLock()
	svc.SetReady(true)

	t.Run("basic search", func(t *testing.T) {
		results, searchErr := svc.Search(context.Background(), "hello", 2)
		if searchErr != nil {
			t.Fatalf("Search failed: %v", searchErr)
		}
		if len(results) == 0 {
			t.Fatal("expected at least one result")
		}
		// Verify result structure.
		for _, r := range results {
			if r.FilePath == "" {
				t.Error("expected non-empty FilePath")
			}
			if r.Language == "" {
				t.Error("expected non-empty Language")
			}
		}
	})

	t.Run("search with file filter", func(t *testing.T) {
		results, searchErr := svc.SearchWithFilter(context.Background(), "func", 2, "**/main.go")
		if searchErr != nil {
			t.Fatalf("SearchWithFilter failed: %v", searchErr)
		}
		for _, r := range results {
			if r.FileName != "main.go" {
				t.Errorf("expected only main.go results, got %s", r.FileName)
			}
		}
	})

	t.Run("browse with file filter", func(t *testing.T) {
		results, browseErr := svc.BrowseWithFilter(context.Background(), 10, "**/utils.go")
		if browseErr != nil {
			t.Fatalf("BrowseWithFilter failed: %v", browseErr)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].FileName != "utils.go" {
			t.Errorf("expected utils.go, got %s", results[0].FileName)
		}
	})

	t.Run("browse with no filter returns all", func(t *testing.T) {
		results, browseErr := svc.BrowseWithFilter(context.Background(), 10, "")
		if browseErr != nil {
			t.Fatalf("BrowseWithFilter failed: %v", browseErr)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 results, got %d", len(results))
		}
	})

	t.Run("browse with non-matching filter returns empty", func(t *testing.T) {
		results, browseErr := svc.BrowseWithFilter(context.Background(), 10, "**/nonexistent.*")
		if browseErr != nil {
			t.Fatalf("BrowseWithFilter failed: %v", browseErr)
		}
		if len(results) != 0 {
			t.Fatalf("expected 0 results, got %d", len(results))
		}
	})
}

func TestDocumentID(t *testing.T) {
	id1 := DocumentID("/path/to/file.go", 0)
	id2 := DocumentID("/path/to/file.go", 1)
	id3 := DocumentID("/path/to/other.go", 0)

	if id1 == id2 {
		t.Error("different chunk indices should produce different IDs")
	}
	if id1 == id3 {
		t.Error("different file paths should produce different IDs")
	}
	// Same inputs should be deterministic.
	if id1 != DocumentID("/path/to/file.go", 0) {
		t.Error("DocumentID should be deterministic")
	}
}

func TestResultToSearchResult(t *testing.T) {
	r := chromem.Result{
		ID: "test:0",
		Metadata: map[string]string{
			"file_path":  "/project/main.go",
			"file_name":  "main.go",
			"start_line": "10",
			"end_line":   "20",
			"language":   "go",
		},
		Content:    "func main() {}",
		Similarity: 0.95,
	}

	sr := resultToSearchResult(r)
	if sr.FilePath != "/project/main.go" {
		t.Errorf("FilePath = %q, want %q", sr.FilePath, "/project/main.go")
	}
	if sr.StartLine != 10 {
		t.Errorf("StartLine = %d, want 10", sr.StartLine)
	}
	if sr.EndLine != 20 {
		t.Errorf("EndLine = %d, want 20", sr.EndLine)
	}
	if sr.Score != 0.95 {
		t.Errorf("Score = %f, want 0.95", sr.Score)
	}
}

func TestCurrentBranch_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	branch, err := CurrentBranch(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != DefaultBranch {
		t.Errorf("expected %q, got %q", DefaultBranch, branch)
	}
}

func TestCurrentBranch_GitRepo(t *testing.T) {
	dir := t.TempDir()

	// Initialize a git repo using git CLI for simplicity.
	cmd := exec.CommandContext(context.Background(), "git", "init", "-b", "test-branch", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Create an initial commit so HEAD points to a branch.
	cmd = exec.CommandContext(context.Background(), "git", "-C", dir, "commit", "--allow-empty", "-m", "init")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	branch, err := CurrentBranch(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "test-branch" {
		t.Errorf("expected %q, got %q", "test-branch", branch)
	}
}

func TestValidateCollection(t *testing.T) {
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := svc.SetProject("validate-test"); err != nil {
		t.Fatalf("SetProject failed: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch failed: %v", err)
	}

	// Create a workspace with files.
	wsDir := t.TempDir()
	file1 := filepath.Join(wsDir, "file1.go")
	file2 := filepath.Join(wsDir, "file2.go")
	if err := os.WriteFile(file1, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte("package utils"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create files that should be ignored by the filtering logic.
	// Ignored extension (.png is in defaultIgnoreExtensions).
	ignoredByExt := filepath.Join(wsDir, "image.png")
	if err := os.WriteFile(ignoredByExt, []byte("fake png data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ignored directory ("build" is in defaultIgnoreDirs).
	buildDir := filepath.Join(wsDir, "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "output.txt"), []byte("build output"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ignored file name ("go.sum" is in defaultIgnoreFileNames).
	if err := os.WriteFile(filepath.Join(wsDir, "go.sum"), []byte("checksum data"), 0o644); err != nil {
		t.Fatal(err)
	}

	hash1 := computeHash([]byte("package main"))
	hash2 := computeHash([]byte("package utils"))

	// Add documents that represent file1 (correct hash) and file2 (stale hash).
	docs := []chromem.Document{
		{
			ID:      DocumentID(file1, 0),
			Content: "package main",
			Metadata: map[string]string{
				"file_path":    file1,
				"file_name":    "file1.go",
				"content_hash": hash1, // matches current
				"start_line":   "1",
				"end_line":     "1",
				"language":     "go",
			},
		},
		{
			ID:      DocumentID(file2, 0),
			Content: "package utils",
			Metadata: map[string]string{
				"file_path":    file2,
				"file_name":    "file2.go",
				"content_hash": "stale_hash", // does NOT match current
				"start_line":   "1",
				"end_line":     "1",
				"language":     "go",
			},
		},
		{
			ID:      DocumentID("/deleted/file.go", 0),
			Content: "deleted content",
			Metadata: map[string]string{
				"file_path":    "/deleted/file.go",
				"file_name":    "file.go",
				"content_hash": "whatever",
				"start_line":   "1",
				"end_line":     "1",
				"language":     "go",
			},
		},
	}

	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments failed: %v", err)
	}
	svc.ReleaseWriteLock()

	stale, newFiles, deleted, valErr := svc.ValidateCollection(context.Background(), wsDir)
	if valErr != nil {
		t.Fatalf("ValidateCollection failed: %v", valErr)
	}

	// file2 should be stale (hash mismatch).
	if !containsPath(stale, file2) {
		t.Errorf("expected file2 in stale files, got %v", stale)
	}

	// file1 should not be stale.
	if containsPath(stale, file1) {
		t.Errorf("file1 should not be stale")
	}

	// /deleted/file.go should be in deleted.
	if !containsPath(deleted, "/deleted/file.go") {
		t.Errorf("expected /deleted/file.go in deleted files, got %v", deleted)
	}

	// Verify we have no false "new" for files already in collection.
	if containsPath(newFiles, file1) || containsPath(newFiles, file2) {
		t.Errorf("existing files should not be in newFiles, got %v", newFiles)
	}

	_ = hash2 // used for documentation clarity

	// Verify ignored files are not reported as new.
	for _, nf := range newFiles {
		base := filepath.Base(nf)
		if base == "image.png" || base == "output.txt" || base == "go.sum" {
			t.Errorf("ignored file should not be in newFiles: %s", nf)
		}
	}
}

func TestRebuildCollection(t *testing.T) {
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := svc.SetProject("rebuild-test"); err != nil {
		t.Fatalf("SetProject failed: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch failed: %v", err)
	}

	// Add a document.
	doc := chromem.Document{
		ID:      "test:0",
		Content: "test content",
		Metadata: map[string]string{
			"file_path": "/test/file.go",
			"file_name": "file.go",
		},
	}
	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), []chromem.Document{doc}, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments failed: %v", err)
	}

	// Rebuild should create a fresh empty collection.
	if err := svc.RebuildCollection(context.Background()); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("RebuildCollection failed: %v", err)
	}
	svc.ReleaseWriteLock()

	if svc.collection == nil {
		t.Fatal("expected collection after rebuild")
	}
	if svc.collection.Count() != 0 {
		t.Errorf("expected empty collection after rebuild, got %d documents", svc.collection.Count())
	}
}

func TestServiceClose(t *testing.T) {
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := svc.SetProject("close-test"); err != nil {
		t.Fatalf("SetProject failed: %v", err)
	}

	if err := svc.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if svc.db != nil {
		t.Error("expected db to be nil after close")
	}
	if svc.collection != nil {
		t.Error("expected collection to be nil after close")
	}
}

func TestGitMonitor_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	called := make(chan string, 1)
	monitor, err := NewGitMonitor(dir, func(branch string) {
		called <- branch
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Start should succeed but not watch anything.
	if err := monitor.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if monitor.CurrentBranchName() != DefaultBranch {
		t.Errorf("expected %q, got %q", DefaultBranch, monitor.CurrentBranchName())
	}

	if err := monitor.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestGetCollectionFiles(t *testing.T) {
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := svc.SetProject("files-test"); err != nil {
		t.Fatalf("SetProject failed: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch failed: %v", err)
	}

	// Empty collection.
	files, err := svc.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles failed: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(files))
	}

	// Add documents from two files.
	docs := []chromem.Document{
		{
			ID:      "f1:0",
			Content: "chunk1 of file1",
			Metadata: map[string]string{
				"file_path":    "/src/file1.go",
				"content_hash": "hash1",
			},
		},
		{
			ID:      "f1:1",
			Content: "chunk2 of file1",
			Metadata: map[string]string{
				"file_path":    "/src/file1.go",
				"content_hash": "hash1",
			},
		},
		{
			ID:      "f2:0",
			Content: "chunk1 of file2",
			Metadata: map[string]string{
				"file_path":    "/src/file2.go",
				"content_hash": "hash2",
			},
		},
	}

	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments failed: %v", err)
	}
	svc.ReleaseWriteLock()

	files, err = svc.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles failed: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files["/src/file1.go"] != "hash1" {
		t.Errorf("expected hash1 for file1.go, got %q", files["/src/file1.go"])
	}
	if files["/src/file2.go"] != "hash2" {
		t.Errorf("expected hash2 for file2.go, got %q", files["/src/file2.go"])
	}
}

func TestDeleteDocumentsByIDs(t *testing.T) {
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := svc.SetProject("delete-test"); err != nil {
		t.Fatalf("SetProject failed: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch failed: %v", err)
	}

	docs := []chromem.Document{
		{
			ID:      "doc1",
			Content: "first document content for testing",
			Metadata: map[string]string{
				"file_path": "/test/a.go",
			},
		},
		{
			ID:      "doc2",
			Content: "second document content for testing",
			Metadata: map[string]string{
				"file_path": "/test/b.go",
			},
		},
	}

	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments failed: %v", err)
	}

	if svc.collection.Count() != 2 {
		svc.ReleaseWriteLock()
		t.Fatalf("expected 2 documents, got %d", svc.collection.Count())
	}

	if err := svc.DeleteDocumentsByIDs(context.Background(), []string{"doc1"}); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("DeleteDocumentsByIDs failed: %v", err)
	}
	svc.ReleaseWriteLock()

	if svc.collection.Count() != 1 {
		t.Fatalf("expected 1 document after deletion, got %d", svc.collection.Count())
	}
}

func containsPath(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}

// --- Hybrid search tests ---

// seedHybridService builds a persistent service with a lexical index,
// adds two documents (one matching query "main" via chromem, another
// matching the exact term "MatcherFactory" via bleve), and returns it.
func seedHybridService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	svc, err := NewService(ServiceConfig{
		PersistPath:   dir,
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.SetProject("hybrid-proj"); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	mkDoc := func(id, path, name, content string) (chromem.Document, lexical.Doc) {
		return chromem.Document{
				ID:      id,
				Content: content,
				Metadata: map[string]string{
					"file_path":    path,
					"file_name":    name,
					"content_hash": id + "-hash",
					"start_line":   "1",
					"end_line":     "5",
					"language":     "go",
				},
			},
			lexical.Doc{
				ID:       id,
				FilePath: path,
				Language: "go",
				Content:  content,
			}
	}

	vec1, lex1 := mkDoc("f1:0", "/proj/main.go", "main.go", "func main() { fmt.Println(\"hello\") }")
	vec2, lex2 := mkDoc("f2:0", "/proj/matcher.go", "matcher.go", "type MatcherFactory struct { rules []Rule }")

	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(),
		[]chromem.Document{vec1, vec2},
		[]lexical.Doc{lex1, lex2},
	); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments: %v", err)
	}
	svc.ReleaseWriteLock()
	svc.SetReady(true)
	return svc
}

func TestHybridSearch_Modes(t *testing.T) {
	svc := seedHybridService(t)

	t.Run("hybrid fuses both sides", func(t *testing.T) {
		results, err := svc.HybridSearch(context.Background(), SearchOptions{
			Query: "MatcherFactory", TopK: 5, Mode: ModeHybrid,
		})
		if err != nil {
			t.Fatalf("HybridSearch: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected non-empty results")
		}
		// Top result should be matcher.go (lexical exact match)
		if results[0].FileName != "matcher.go" {
			t.Errorf("expected matcher.go as top hybrid result, got %q", results[0].FileName)
		}
		if results[0].LexicalRank == 0 {
			t.Error("expected LexicalRank > 0 on hybrid hit")
		}
	})

	t.Run("vector-only mode only populates VectorRank", func(t *testing.T) {
		results, err := svc.HybridSearch(context.Background(), SearchOptions{
			Query: "main", TopK: 5, Mode: ModeVector,
		})
		if err != nil {
			t.Fatalf("vector search: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected non-empty vector results")
		}
		for _, r := range results {
			if r.LexicalRank != 0 {
				t.Errorf("vector-only mode should not set LexicalRank, got %d", r.LexicalRank)
			}
		}
	})

	t.Run("lexical-only mode only populates LexicalRank", func(t *testing.T) {
		results, err := svc.HybridSearch(context.Background(), SearchOptions{
			Query: "MatcherFactory", TopK: 5, Mode: ModeLexical,
		})
		if err != nil {
			t.Fatalf("lexical search: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected non-empty lexical results")
		}
		if results[0].FileName != "matcher.go" {
			t.Errorf("expected matcher.go, got %q", results[0].FileName)
		}
		for _, r := range results {
			if r.VectorRank != 0 {
				t.Errorf("lexical-only mode should not set VectorRank, got %d", r.VectorRank)
			}
		}
	})
}

func TestHybridSearch_FilePatternAppliedBeforeFusion(t *testing.T) {
	svc := seedHybridService(t)
	results, err := svc.HybridSearch(context.Background(), SearchOptions{
		Query: "MatcherFactory", TopK: 5, Mode: ModeHybrid, FilePattern: "**/main.go",
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	for _, r := range results {
		if r.FileName != "main.go" {
			t.Errorf("FilePattern should filter out matcher.go, got %q", r.FileName)
		}
	}
}

func TestHybridSearch_MustMatchEnforced(t *testing.T) {
	svc := seedHybridService(t)
	results, err := svc.HybridSearch(context.Background(), SearchOptions{
		Query: "main MatcherFactory", TopK: 5, Mode: ModeHybrid,
		MustMatch: []string{"MatcherFactory"},
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	for _, r := range results {
		if !strings.Contains(r.Content, "MatcherFactory") {
			t.Errorf("MustMatch not enforced on %q content %q", r.FileName, r.Content)
		}
	}
}

func TestHybridSearch_PlusSugarMergesIntoMustMatch(t *testing.T) {
	svc := seedHybridService(t)
	results, err := svc.HybridSearch(context.Background(), SearchOptions{
		Query: "function +MatcherFactory", TopK: 5, Mode: ModeHybrid,
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	for _, r := range results {
		if !strings.Contains(r.Content, "MatcherFactory") {
			t.Errorf("+sugar must-match not enforced on %q", r.FileName)
		}
	}
}

func TestHybridSearch_AutoFallbackWhenLexicalEmpty(t *testing.T) {
	// In-memory service has no lexical index at all.
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.SetProject("fallback-proj"); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	docs := []chromem.Document{{
		ID:      "x:0",
		Content: "hello world",
		Metadata: map[string]string{
			"file_path":    "/p/a.go",
			"file_name":    "a.go",
			"content_hash": "h",
			"start_line":   "1",
			"end_line":     "2",
			"language":     "go",
		},
	}}
	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments: %v", err)
	}
	svc.ReleaseWriteLock()
	svc.SetReady(true)

	// Hybrid should auto-fall-back to vector (no error, results populated).
	results, err := svc.HybridSearch(context.Background(), SearchOptions{
		Query: "hello", TopK: 2, Mode: ModeHybrid,
	})
	if err != nil {
		t.Fatalf("hybrid with empty lexical should fall back, got err: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected vector fallback to return results")
	}

	// Explicit ModeLexical should error out because there is no lexical index at all.
	if _, err := svc.HybridSearch(context.Background(), SearchOptions{
		Query: "hello", TopK: 2, Mode: ModeLexical,
	}); err == nil {
		t.Error("ModeLexical with no lexical index should return error")
	}
}
