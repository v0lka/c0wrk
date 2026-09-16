package vectorindex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	chromem "github.com/philippgille/chromem-go"
)

// This file pins the storage-shrink contract: committed chromem documents
// carry only ID, vector, and the four chunk-position metadata fields; chunk
// text and per-file bookkeeping live in the file-hash sidecar / the source
// files, and every search mode reconstructs full SearchResult.Content from
// the real files (with an explicit placeholder when a file is gone).

// stripFixtureFiles is the deterministic fixture corpus shared by the
// acceptance tests below.
var stripFixtureFiles = map[string]string{
	"main.go": "package main\n\nfunc main() {\n\tfmt.Println(\"hello vector\")\n}\n",
	"lib/util.go": "package lib\n\n" +
		"// Add sums two ints.\n" +
		"func Add(a, b int) int {\n" +
		"\treturn a + b\n" +
		"}\n",
	"notes.md": "# Notes\n\nThe MatcherFactory token lives here.\nUse it for must-match checks.\n",
}

// writeStripFixture writes the fixture corpus under a fresh temp dir and
// returns the workspace root.
func writeStripFixture(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	for rel, content := range stripFixtureFiles {
		abs := filepath.Join(ws, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", abs, err)
		}
	}
	return ws
}

// newStripTestService builds a persistent service (project + branch) over
// projectDir. A non-nil batch selects the embedding-batched commit path —
// the production path that strips stored content; nil keeps the legacy
// chromem-embedding path.
func newStripTestService(t *testing.T, projectDir string, batch BatchEmbedder) *Service {
	t.Helper()
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
		BatchEmbedder: batch,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() }) // before TempDir cleanup (LIFO)
	if err := svc.SetProject("strip-proj", projectDir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	return svc
}

// indexStripFixture runs a full index pass over ws with the deterministic
// single-chunk-per-file test chunker.
func indexStripFixture(t *testing.T, svc *Service, ws string) *Indexer {
	t.Helper()
	idx := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})
	if err := idx.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}
	return idx
}

// fixtureDocID returns the single (fakeChunkFunc) chunk ID for a fixture file.
func fixtureDocID(t *testing.T, svc *Service, ws, rel string) string {
	t.Helper()
	files, err := svc.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles: %v", err)
	}
	abs := filepath.Join(ws, rel)
	if _, ok := files[abs]; !ok {
		t.Fatalf("fixture file %s missing from sidecar %v", abs, files)
	}
	return DocumentID(abs, 0)
}

// TestIndexedDocuments_StoreNoContentOrPerFileMetadata pins acceptance (1):
// after a batch-path index pass, GetByID returns Content=="" and EXACTLY the
// four chunk-position metadata fields.
func TestIndexedDocuments_StoreNoContentOrPerFileMetadata(t *testing.T) {
	ws := writeStripFixture(t)
	svc := newStripTestService(t, t.TempDir(), &fakeBatchEmbedder{})
	indexStripFixture(t, svc, ws)

	wantKeys := map[string]bool{"file_path": true, "start_line": true, "end_line": true, "language": true}
	for rel := range stripFixtureFiles {
		id := fixtureDocID(t, svc, ws, rel)
		doc, err := svc.current.collection.GetByID(context.Background(), id)
		if err != nil {
			t.Fatalf("GetByID(%s): %v", id, err)
		}
		if doc.Content != "" {
			t.Errorf("doc %s stored Content %q; want empty (content stripped after embedding)", id, doc.Content)
		}
		if len(doc.Embedding) == 0 {
			t.Errorf("doc %s stored without an embedding", id)
		}
		if len(doc.Metadata) != len(wantKeys) {
			t.Errorf("doc %s stored %d metadata fields %v; want exactly %d", id, len(doc.Metadata), doc.Metadata, len(wantKeys))
		}
		for k := range doc.Metadata {
			if !wantKeys[k] {
				t.Errorf("doc %s stored non-positional metadata key %q", id, k)
			}
		}
		if got := doc.Metadata["file_path"]; got != filepath.Join(ws, rel) {
			t.Errorf("doc %s file_path = %q; want %q", id, got, filepath.Join(ws, rel))
		}
	}

	// The per-file bookkeeping moved to the sidecar: one entry per fixture
	// file, new-format (5-field) values with the committed chunk set.
	files, err := svc.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles: %v", err)
	}
	if len(files) != len(stripFixtureFiles) {
		t.Fatalf("sidecar has %d entries %v; want %d", len(files), files, len(stripFixtureFiles))
	}
	for fp, entry := range files {
		if _, ok := fileHashEntryChunkSet(entry); !ok {
			t.Errorf("sidecar entry for %s carries no chunk set: %q", fp, entry)
		}
	}
}

// sumGobBytes sums the sizes of the per-document gob files under dir.
func sumGobBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() && strings.HasSuffix(path, ".gob") {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return total
}

// TestIndexedGob_SmallerThanLegacy pins acceptance (1): the gob footprint of
// a batch-path index is strictly smaller than the same corpus indexed via
// the legacy (content-storing) path.
func TestIndexedGob_SmallerThanLegacy(t *testing.T) {
	ws := writeStripFixture(t)

	batchProj := t.TempDir()
	batchSvc := newStripTestService(t, batchProj, &fakeBatchEmbedder{})
	indexStripFixture(t, batchSvc, ws)

	legacyProj := t.TempDir()
	legacySvc := newStripTestService(t, legacyProj, nil)
	indexStripFixture(t, legacySvc, ws)

	batchBytes := sumGobBytes(t, batchProj)
	legacyBytes := sumGobBytes(t, legacyProj)
	t.Logf("gob bytes: batch=%d legacy=%d (fixture corpus %d bytes)",
		batchBytes, legacyBytes, len(stripFixtureFiles["main.go"])+len(stripFixtureFiles["lib/util.go"])+len(stripFixtureFiles["notes.md"]))
	if batchBytes >= legacyBytes {
		t.Errorf("batch-path gob footprint %d >= legacy %d; want strictly smaller (content + per-file metadata stripped)", batchBytes, legacyBytes)
	}
}

// TestSearchModes_ReconstructContentFromFiles pins acceptance (2): all
// search modes — vector, lexical, hybrid, browse — return full
// SearchResult.Content reconstructed from the real source files, and
// must-match filtering works against the reconstructed text.
func TestSearchModes_ReconstructContentFromFiles(t *testing.T) {
	ws := writeStripFixture(t)
	svc := newStripTestService(t, t.TempDir(), &fakeBatchEmbedder{})
	indexStripFixture(t, svc, ws)
	svc.SetReady(true)

	expectContent := func(t *testing.T, mode string, results []SearchResult) {
		t.Helper()
		if len(results) == 0 {
			t.Fatalf("%s mode returned no results", mode)
		}
		seen := map[string]bool{}
		for _, r := range results {
			if r.Content == "" {
				t.Errorf("%s mode: result for %s has empty Content", mode, r.FilePath)
			}
			if strings.Contains(r.Content, contentUnavailablePrefix) {
				t.Errorf("%s mode: result for %s got placeholder %q", mode, r.FilePath, r.Content)
			}
			rel, err := filepath.Rel(ws, r.FilePath)
			if err != nil {
				t.Fatalf("Rel(%s): %v", r.FilePath, err)
			}
			want, ok := stripFixtureFiles[rel]
			if !ok {
				t.Fatalf("%s mode returned unexpected file %s", mode, r.FilePath)
			}
			// fakeChunkFunc yields one chunk spanning the whole file, so the
			// reconstruction must reproduce the file byte-for-byte (the
			// trailing newline is preserved by the line join).
			if r.Content != want {
				t.Errorf("%s mode: content for %s = %q; want the file content %q", mode, rel, r.Content, want)
			}
			seen[rel] = true
		}
		if len(seen) == 0 {
			t.Fatalf("%s mode returned no results", mode)
		}
	}

	t.Run("vector mode", func(t *testing.T) {
		results, err := svc.HybridSearch(context.Background(), SearchOptions{Query: "Add", TopK: 5, Mode: ModeVector})
		if err != nil {
			t.Fatalf("vector search: %v", err)
		}
		expectContent(t, "vector", results)
	})

	t.Run("lexical mode", func(t *testing.T) {
		results, err := svc.HybridSearch(context.Background(), SearchOptions{Query: "MatcherFactory", TopK: 5, Mode: ModeLexical})
		if err != nil {
			t.Fatalf("lexical search: %v", err)
		}
		expectContent(t, "lexical", results)
	})

	t.Run("hybrid mode", func(t *testing.T) {
		results, err := svc.HybridSearch(context.Background(), SearchOptions{Query: "Notes", TopK: 5, Mode: ModeHybrid})
		if err != nil {
			t.Fatalf("hybrid search: %v", err)
		}
		expectContent(t, "hybrid", results)
	})

	t.Run("browse", func(t *testing.T) {
		results, err := svc.BrowseWithFilter(context.Background(), 10, "")
		if err != nil {
			t.Fatalf("BrowseWithFilter: %v", err)
		}
		expectContent(t, "browse", results)
	})

	t.Run("must-match filters on reconstructed content", func(t *testing.T) {
		// MatcherFactory appears only in notes.md; the other files must be
		// filtered out by the must-match check against reconstructed text.
		results, err := svc.HybridSearch(context.Background(), SearchOptions{
			Query:     "vector Notes",
			TopK:      5,
			Mode:      ModeHybrid,
			MustMatch: []string{"MatcherFactory"},
		})
		if err != nil {
			t.Fatalf("hybrid must-match search: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("must-match search returned no results")
		}
		for _, r := range results {
			if !strings.Contains(r.Content, "MatcherFactory") {
				t.Errorf("must-match token missing from reconstructed content of %s: %q", r.FilePath, r.Content)
			}
			if filepath.Base(r.FilePath) != "notes.md" {
				t.Errorf("must-match admitted %s; want only notes.md", r.FilePath)
			}
		}
	})
}

// TestSearch_MissingFileYieldsPlaceholder pins acceptance (3): a hit whose
// source file disappeared renders an explicit placeholder — never an error —
// while a must-match query against the missing file filters it out instead
// of matching the placeholder.
func TestSearch_MissingFileYieldsPlaceholder(t *testing.T) {
	ws := writeStripFixture(t)
	svc := newStripTestService(t, t.TempDir(), &fakeBatchEmbedder{})
	indexStripFixture(t, svc, ws)
	svc.SetReady(true)

	removed := filepath.Join(ws, "lib", "util.go")
	if err := os.Remove(removed); err != nil {
		t.Fatalf("Remove(%s): %v", removed, err)
	}

	t.Run("vector mode yields placeholder without error", func(t *testing.T) {
		results, err := svc.HybridSearch(context.Background(), SearchOptions{Query: "Add", TopK: 5, Mode: ModeVector})
		if err != nil {
			t.Fatalf("vector search over deleted file: %v", err)
		}
		var placeholderSeen bool
		for _, r := range results {
			if r.FilePath != removed {
				continue
			}
			placeholderSeen = true
			if !strings.Contains(r.Content, contentUnavailablePrefix) {
				t.Errorf("result for deleted file %s has Content %q; want placeholder containing %q", removed, r.Content, contentUnavailablePrefix)
			}
		}
		if !placeholderSeen {
			t.Fatalf("vector results did not include the deleted file %s: %+v", removed, results)
		}
	})

	t.Run("browse yields placeholder without error", func(t *testing.T) {
		results, err := svc.BrowseWithFilter(context.Background(), 10, "")
		if err != nil {
			t.Fatalf("browse over deleted file: %v", err)
		}
		var placeholderSeen bool
		for _, r := range results {
			if r.FilePath != removed {
				continue
			}
			placeholderSeen = true
			if !strings.Contains(r.Content, contentUnavailablePrefix) {
				t.Errorf("browse result for deleted file %s has Content %q; want placeholder", removed, r.Content)
			}
		}
		if !placeholderSeen {
			t.Fatalf("browse results did not include the deleted file %s", removed)
		}
	})

	t.Run("must-match excludes the deleted file", func(t *testing.T) {
		results, err := svc.HybridSearch(context.Background(), SearchOptions{
			Query:     "Add",
			TopK:      5,
			Mode:      ModeVector,
			MustMatch: []string{"return"},
		})
		if err != nil {
			t.Fatalf("must-match search over deleted file: %v", err)
		}
		for _, r := range results {
			if r.FilePath == removed {
				t.Errorf("must-match admitted the deleted file %s via its placeholder", removed)
			}
		}
	})
}

// mapsEqual is a tiny map[string]string equality helper for the strip tests.
func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestStrippedForCommit unit-pins the strip semantics: content drops only
// for pre-embedded documents, metadata narrows on both paths, and the input
// documents are never mutated.
func TestStrippedForCommit(t *testing.T) {
	docs := []chromem.Document{
		{
			ID:        "batch:0",
			Content:   "batched text",
			Embedding: []float32{1, 0},
			Metadata: map[string]string{
				"file_path":            "/ws/a.go",
				"file_name":            "a.go",
				"last_modified":        "2024-01-01T00:00:00Z",
				"content_hash":         "h",
				"file_size":            "42",
				"file_mtime_unix_nano": "1",
				"start_line":           "1",
				"end_line":             "2",
				"language":             "go",
			},
		},
		{
			ID:      "legacy:0",
			Content: "legacy text",
			Metadata: map[string]string{
				"file_path": "/ws/b.go",
				"file_name": "b.go",
				"language":  "go",
			},
		},
	}

	stripped := strippedForCommit(docs)

	// Batch doc: content dropped, embedding kept, metadata narrowed to the
	// positional keys it actually carries.
	if stripped[0].Content != "" {
		t.Errorf("batch doc kept Content %q; want stripped", stripped[0].Content)
	}
	if len(stripped[0].Embedding) != 2 {
		t.Errorf("batch doc embedding lost")
	}
	wantBatchMeta := map[string]string{"file_path": "/ws/a.go", "start_line": "1", "end_line": "2", "language": "go"}
	if !mapsEqual(stripped[0].Metadata, wantBatchMeta) {
		t.Errorf("batch doc metadata = %v; want %v", stripped[0].Metadata, wantBatchMeta)
	}

	// Legacy doc: content kept (chromem must embed it), metadata narrowed.
	if stripped[1].Content != "legacy text" {
		t.Errorf("legacy doc Content = %q; want retained (chromem embeds it)", stripped[1].Content)
	}
	wantLegacyMeta := map[string]string{"file_path": "/ws/b.go", "language": "go"}
	if !mapsEqual(stripped[1].Metadata, wantLegacyMeta) {
		t.Errorf("legacy doc metadata = %v; want %v", stripped[1].Metadata, wantLegacyMeta)
	}

	// Input untouched.
	if docs[0].Content != "batched text" || docs[0].Metadata["content_hash"] != "h" {
		t.Errorf("input batch doc was mutated: %+v", docs[0])
	}
	if docs[1].Metadata["file_name"] != "b.go" {
		t.Errorf("input legacy doc was mutated: %+v", docs[1])
	}
}
