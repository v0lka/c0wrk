package vectorindex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	chromem "github.com/philippgille/chromem-go"
)

// seedLegacyDoc is one pre-upgrade chromem document: full per-chunk metadata
// (including the per-file bookkeeping that no longer commits — see
// strippedForCommit), chunk text, and a pre-populated unit embedding so the
// seeding path never invokes an embedder. chunkIdx is only documentation
// (callers derive the ID via DocumentID themselves).
func seedLegacyDoc(id, filePath, content string, _ int) chromem.Document {
	return chromem.Document{
		ID:        id,
		Content:   content,
		Embedding: []float32{1, 0, 0, 0},
		Metadata: map[string]string{
			"file_path":            filePath,
			"file_name":            filepath.Base(filePath),
			"last_modified":        "2024-01-01T00:00:00Z",
			"content_hash":         "hash-" + filepath.Base(filePath),
			"file_size":            "1000",
			"file_mtime_unix_nano": "1700000000000000000",
			"start_line":           "1",
			"end_line":             "10",
			"language":             "go",
		},
	}
}

// writeSidecarFile persists a hand-built sidecar map as the branch's JSON
// sidecar, mirroring what an older binary would have left on disk.
func writeSidecarFile(t *testing.T, projectDir, branch string, entries map[string]string) {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal sidecar: %v", err)
	}
	path := filepath.Join(projectDir, "file_hashes_"+collectionName(branch)+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
}

// countingEmbed4 returns a 4-dimensional embedding func that counts calls.
// Every content-less path (migration probing, sidecar-driven lexical
// rebuild) must leave the counter at zero.
func countingEmbed4(calls *atomic.Int32) chromem.EmbeddingFunc {
	return func(_ context.Context, _ string) ([]float32, error) {
		calls.Add(1)
		return []float32{0.1, 0.2, 0.3, 0.4}, nil
	}
}

// assertStripped verifies one document is in the content-less commit shape:
// no chunk text, exactly the four commitMetadataKeys, and the original ID
// and embedding preserved.
func assertStripped(t *testing.T, doc chromem.Document, wantID string) {
	t.Helper()
	if doc.ID != wantID {
		t.Errorf("ID = %q, want %q", doc.ID, wantID)
	}
	if doc.Content != "" {
		t.Errorf("doc %s: Content = %q, want empty", doc.ID, doc.Content)
	}
	if len(doc.Embedding) != 4 || doc.Embedding[0] != 1 {
		t.Errorf("doc %s: embedding not preserved: %v", doc.ID, doc.Embedding)
	}
	if len(doc.Metadata) != len(commitMetadataKeys) {
		t.Fatalf("doc %s: metadata has %d keys (%v), want exactly %d", doc.ID, len(doc.Metadata), doc.Metadata, len(commitMetadataKeys))
	}
	for _, k := range commitMetadataKeys {
		if _, ok := doc.Metadata[k]; !ok {
			t.Errorf("doc %s: metadata missing commit key %q", doc.ID, k)
		}
	}
	if doc.Metadata["file_path"] == "" || doc.Metadata["language"] == "" {
		t.Errorf("doc %s: commit metadata values not preserved: %v", doc.ID, doc.Metadata)
	}
}

// TestContentlessMigration_StripsLegacyDocsAndWritesMarker covers the happy
// path: a legacy collection (full metadata + chunk text) is rewritten to the
// content-less shape document-by-document, preserving ID, vector and the
// four commit metadata fields; the marker is written once and a re-open
// never migrates again.
//
// Two probe modes are exercised:
//   - fileA's sidecar entry carries its committed chunk set (5th field), so
//     the migration probes exactly those indices arithmetically;
//   - fileB's entry is legacy 3-field (no set), so the migration scans
//     gap-tolerantly — and chunk 2 is genuinely missing from the collection
//     (the poisoned-drop shape), which the scan must bridge.
func TestContentlessMigration_StripsLegacyDocsAndWritesMarker(t *testing.T) {
	dir := t.TempDir()
	var embedCalls atomic.Int32

	// Build the on-disk legacy state exactly like an older binary would
	// leave it: raw chromem persistence, no marker, hand-written sidecar.
	legacyDB, err := chromem.NewPersistentDB(dir, false)
	if err != nil {
		t.Fatalf("NewPersistentDB: %v", err)
	}
	legacyCol, err := legacyDB.GetOrCreateCollection(collectionName("main"), nil, countingEmbed4(&embedCalls))
	if err != nil {
		t.Fatalf("GetOrCreateCollection: %v", err)
	}

	fileA := filepath.Join(dir, "ws", "a.go")
	fileB := filepath.Join(dir, "ws", "b.go")

	seededA := make([]chromem.Document, 0, 3)
	seededB := make([]chromem.Document, 0, 3)
	for i := range 3 {
		doc := seedLegacyDoc(DocumentID(fileA, i), fileA, "alpha chunk", i)
		seededA = append(seededA, doc)
	}
	// Chunk 2 of fileB is deliberately absent (poison-dropped shape).
	for _, i := range []int{0, 1, 3} {
		doc := seedLegacyDoc(DocumentID(fileB, i), fileB, "beta chunk", i)
		seededB = append(seededB, doc)
	}
	seed := append(slices.Clone(seededA), seededB...)
	if err := legacyCol.AddDocuments(context.Background(), seed, 1); err != nil {
		t.Fatalf("seed legacy docs: %v", err)
	}

	writeSidecarFile(t, dir, "main", map[string]string{
		// Current-format entry: committed set {0,1,2} → arithmetic probing.
		fileA: fileHashInfo{
			filePath: fileA, hash: "hash-a.go", size: "1000", mtime: "1700000000000000000",
		}.entry("fp-aaaaaaaaaaaa", encodeChunkIndices([]int{0, 1, 2})),
		// Legacy 3-field entry: no fingerprint, no set → gap-tolerant scan.
		fileB: fileHashInfo{
			filePath: fileB, hash: "hash-b.go", size: "1000", mtime: "1700000000000000000",
		}.entry("", ""),
	})

	// Open with the upgraded binary: SwitchBranch starts the migration in
	// the background; WaitContentlessMigration gates on its completion.
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:      countingEmbed4(&embedCalls),
		EmbeddingDimension: 4,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	if err := svc.WaitContentlessMigration(context.Background()); err != nil {
		t.Fatalf("WaitContentlessMigration: %v", err)
	}

	// Every seeded document is now content-less with ID/vector/commit
	// metadata preserved — including fileB's bridged gap (chunk 2 stays
	// absent, chunk 3 is stripped).
	col := svc.GetCollection()
	for _, doc := range seed {
		got, err := col.GetByID(context.Background(), doc.ID)
		if err != nil {
			t.Fatalf("GetByID(%s): %v", doc.ID, err)
		}
		assertStripped(t, got, doc.ID)
		if got.Metadata["file_path"] != doc.Metadata["file_path"] {
			t.Errorf("doc %s: file_path = %q, want %q", doc.ID, got.Metadata["file_path"], doc.Metadata["file_path"])
		}
	}

	marker := filepath.Join(dir, "contentless_"+collectionName("main")+".done")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("migration marker missing after migration: %v", err)
	}
	if got := embedCalls.Load(); got != 0 {
		t.Errorf("migration invoked the embedding function %d times; want 0 (GetByID/re-Add never embed)", got)
	}

	// Re-open: the marker short-circuits the trigger synchronously — no new
	// goroutine, settled channel immediately after SwitchBranch returns.
	_ = svc.Close()
	svc2, err := NewService(ServiceConfig{
		EmbeddingFunc:      countingEmbed4(&embedCalls),
		EmbeddingDimension: 4,
	})
	if err != nil {
		t.Fatalf("NewService (reopen): %v", err)
	}
	t.Cleanup(func() { _ = svc2.Close() })
	if err := svc2.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject (reopen): %v", err)
	}
	if err := svc2.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch (reopen): %v", err)
	}
	select {
	case <-svc2.current.contentlessCh:
	default:
		t.Fatal("contentless migration re-triggered on reopen despite the marker")
	}
	if svc2.current.contentlessPending.Load() {
		t.Fatal("contentlessPending must be false right after reopen with a marker")
	}
}

// TestContentlessMigration_CancelledByProjectSwitch_ReplaysOnReopen covers
// the acceptance scenario for cancellation: switching the project away
// mid-migration abandons the rewrite WITHOUT the marker, the collection is
// left consistent at every instant (each document is either fully legacy or
// fully stripped — the re-commit is an atomic ID-preserving replace), and
// the next open replays the migration to completion.
func TestContentlessMigration_CancelledByProjectSwitch_ReplaysOnReopen(t *testing.T) {
	dir := t.TempDir()
	otherDir := t.TempDir()
	var embedCalls atomic.Int32

	legacyDB, err := chromem.NewPersistentDB(dir, false)
	if err != nil {
		t.Fatalf("NewPersistentDB: %v", err)
	}
	legacyCol, err := legacyDB.GetOrCreateCollection(collectionName("main"), nil, countingEmbed4(&embedCalls))
	if err != nil {
		t.Fatalf("GetOrCreateCollection: %v", err)
	}

	// Two files × 250 chunks = 500 legacy documents: enough for several
	// commit windows (embeddingCommitWindowSize = 200), so the cancel can
	// land mid-flight or at entry — either way the assertions must hold.
	files := []string{filepath.Join(dir, "ws", "big1.go"), filepath.Join(dir, "ws", "big2.go")}
	entries := make(map[string]string, len(files))
	seed := make([]chromem.Document, 0, 250*len(files))
	for _, fp := range files {
		for i := range 250 {
			seed = append(seed, seedLegacyDoc(DocumentID(fp, i), fp, "payload chunk", i))
		}
		entries[fp] = fileHashInfo{
			filePath: fp, hash: "hash-" + filepath.Base(fp), size: "1000", mtime: "1700000000000000000",
		}.entry("", "") // legacy entries → gap-tolerant probing
	}
	if err := legacyCol.AddDocuments(context.Background(), seed, 1); err != nil {
		t.Fatalf("seed legacy docs: %v", err)
	}
	writeSidecarFile(t, dir, "main", entries)

	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:      countingEmbed4(&embedCalls),
		EmbeddingDimension: 4,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	// Cancel by switching the project away immediately: the outgoing state
	// is closed (parking disabled by default), which cancels the migration
	// context. Close() then waits for the goroutine to unwind.
	if err := svc.SetProject("other", otherDir); err != nil {
		t.Fatalf("SetProject away: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	marker := filepath.Join(dir, "contentless_"+collectionName("main")+".done")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker must not exist after a cancelled migration, stat err = %v", err)
	}

	// Reopen: verify the pre-replay stored state is consistent. SwitchBranch
	// re-triggers the migration immediately, so hold the service WRITE lock
	// during the probe: the migration's window flushes need the read lock
	// and block, while GetByID reads the (atomically swapped) stored
	// documents directly — the probe observes exactly the post-cancel state.
	svc2, err := NewService(ServiceConfig{
		EmbeddingFunc:      countingEmbed4(&embedCalls),
		EmbeddingDimension: 4,
	})
	if err != nil {
		t.Fatalf("NewService (reopen): %v", err)
	}
	t.Cleanup(func() { _ = svc2.Close() })
	if err := svc2.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject (reopen): %v", err)
	}
	if err := svc2.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch (reopen): %v", err)
	}
	col := svc2.GetCollection()
	svc2.AcquireWriteLock()
	for _, doc := range seed {
		got, err := col.GetByID(context.Background(), doc.ID)
		if err != nil {
			svc2.ReleaseWriteLock()
			t.Fatalf("GetByID(%s) after cancelled migration: %v", doc.ID, err)
		}
		if got.Content == "" {
			assertStripped(t, got, doc.ID)
		} else if got.Metadata["content_hash"] == "" {
			svc2.ReleaseWriteLock()
			t.Fatalf("doc %s: content present but per-file metadata lost — inconsistent mid-migration state", doc.ID)
		}
	}
	svc2.ReleaseWriteLock()

	// The replay finishes the job: everything stripped, marker written.
	if err := svc2.WaitContentlessMigration(context.Background()); err != nil {
		t.Fatalf("WaitContentlessMigration (replay): %v", err)
	}
	for _, doc := range seed {
		got, err := col.GetByID(context.Background(), doc.ID)
		if err != nil {
			t.Fatalf("GetByID(%s) after replay: %v", doc.ID, err)
		}
		assertStripped(t, got, doc.ID)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker missing after replay: %v", err)
	}
	if got := embedCalls.Load(); got != 0 {
		t.Errorf("migration replay invoked the embedding function %d times; want 0", got)
	}
}

// TestContentlessMigration_EmptyCollectionWritesMarkerSynchronously pins the
// trigger's empty-collection rule: a branch opened with no documents gets
// the marker synchronously (every future commit is content-less by
// construction), so a subsequent full index never pays a probe pass and
// documents committed after the open are left alone.
func TestContentlessMigration_EmptyCollectionWritesMarkerSynchronously(t *testing.T) {
	dir := t.TempDir()
	var embedCalls atomic.Int32

	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:      countingEmbed4(&embedCalls),
		EmbeddingDimension: 4,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	// SwitchBranch has already returned: the marker must exist and the
	// settle channel be closed without any background work.
	marker := filepath.Join(dir, "contentless_"+collectionName("main")+".done")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker not written synchronously for an empty collection: %v", err)
	}
	select {
	case <-svc.current.contentlessCh:
	default:
		t.Fatal("contentlessCh must be closed synchronously for an empty collection")
	}
	if svc.current.contentlessPending.Load() {
		t.Fatal("contentlessPending must be false for an empty collection")
	}

	// Documents committed after the open keep the (test-path) commit shape
	// they were given — the marker means the migration never runs for them.
	doc := seedLegacyDoc("plain-id", filepath.Join(dir, "x.go"), "late commit", 0)
	if err := svc.GetCollection().AddDocuments(context.Background(), []chromem.Document{doc}, 1); err != nil {
		t.Fatalf("AddDocuments: %v", err)
	}
	got, err := svc.GetCollection().GetByID(context.Background(), "plain-id")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Content != doc.Content {
		t.Errorf("post-marker commit was stripped by a migration that must not run: %q", got.Content)
	}
}
