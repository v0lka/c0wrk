package vectorindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
	"github.com/v0lka/sp4rk/safeio"
)

func largeIndexRows() []occurrenceRow {
	const count = 18000
	rows := make([]occurrenceRow, count)
	prefix := strings.Repeat("nested/", 80)
	c := chunkRecordKey("shared indexed content")
	for i := range rows {
		rows[i] = occurrenceRow{
			Path:     fmt.Sprintf("%sf%05d.go", prefix, i),
			BlobHash: strings.Repeat("a", 64),
			Chunks:   []chunkOccurrence{{C: c, Index: 0, Start: 1, End: 2, Language: "go"}},
		}
	}
	return rows
}

func requireLargeMetadata(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= safeio.DefaultMaxFileSize {
		t.Fatalf("fixture is %d bytes; must exceed SDK default %d", info.Size(), safeio.DefaultMaxFileSize)
	}
}

func TestLargeOccurrenceTable_ReopenAndSearch(t *testing.T) {
	h := newChunkStoreHarness(t)
	const key = "snap_0123456789abcdef"
	const rootKey = "root_0123456789abcdef"
	rows := largeIndexRows()
	c := rows[0].Chunks[0].C
	if err := writeChunkRecord(h.gen, c, chunkVec(c)); err != nil {
		t.Fatal(err)
	}
	header := &snapshotTable{Files: len(rows), Chunks: len(rows), UniqueChunks: 1}
	if err := writeSnapshotTable(h.gen, key, 8, rows, header, nil); err != nil {
		t.Fatal(err)
	}
	requireLargeMetadata(t, filepath.Join(snapshotDir(h.gen, key), occurrencesTabName))
	if _, err := os.Stat(lexicalIndexPath(h.storage, key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture must have no legacy lexical leg: %v", err)
	}
	lex, err := lexical.Open(filepath.Join(snapshotDir(h.gen, key), "lexical"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lex.Close() })
	docs := make([]lexical.Doc, len(rows))
	for i, row := range rows {
		docs[i] = lexical.Doc{ID: DocumentID(row.Path, 0), FilePath: row.Path, Language: "go", Content: "shared content"}
	}
	docs[0].Content = "needleunique"
	if err := lex.Upsert(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	if err := lex.Close(); err != nil {
		t.Fatal(err)
	}

	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	svc.current.fileHashes = make(map[string]string, len(rows))
	for _, row := range rows {
		svc.current.fileHashes[row.Path] = row.BlobHash
	}
	for pass := range 2 {
		reg := NewCorpusRegistry(nil)
		t.Cleanup(reg.Close)
		handle, err := reg.Open(context.Background(), h.storage, key, fakeEmbeddingFunc(), "fp-era-a", 8)
		if err != nil {
			t.Fatalf("open %d: %v", pass, err)
		}
		if handle.v2 == nil || handle.collection != nil || handle.CorpusCount() != len(rows) {
			t.Fatalf("open %d did not serve the complete native corpus", pass)
		}
		hits, err := handle.lexical.Query(context.Background(), "needleunique", 3)
		if err != nil || len(hits) != 1 || hits[0].ID != docs[0].ID {
			t.Fatalf("lexical search %d: hits=%v err=%v", pass, hits, err)
		}
		vectors, err := svc.v2VectorCandidates(context.Background(), handle.v2, "query", 3, "", nil, nil)
		if err != nil || len(vectors) != 3 {
			t.Fatalf("vector search %d: hits=%d err=%v", pass, len(vectors), err)
		}
		handle.Release()
		reg.Close()
	}
	if err := writeOverlayTable(h.gen, rootKey, rows); err != nil {
		t.Fatal(err)
	}
	if overlay, ok := readOverlayTable(h.gen, rootKey); !ok || len(overlay) != len(rows) {
		t.Fatalf("large overlay: ok=%v rows=%d, want %d", ok, len(overlay), len(rows))
	}
	refs, ok := chunkReferenceCounts(h.gen)
	if !ok || refs[c] != 2 {
		t.Fatalf("large tables' references = %v, ok=%v; want two references to shared chunk", refs, ok)
	}
}

func TestSnapshotReadLimit_PreservesCauseAndPublication(t *testing.T) {
	h := newChunkStoreHarness(t)
	const key = "snap_0123456789abcdef"
	dir := snapshotDir(h.gen, key)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	headerPath := filepath.Join(dir, snapshotTableName)
	if err := os.WriteFile(headerPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(headerPath, safeio.DefaultMaxFileSize+1); err != nil {
		t.Fatal(err)
	}
	reg := NewCorpusRegistry(nil)
	t.Cleanup(reg.Close)
	handle, err := reg.Open(context.Background(), h.storage, key, fakeEmbeddingFunc(), "fp-era-a", 8)
	if handle != nil || !errors.Is(err, safeio.ErrTooLarge) {
		t.Fatalf("open = %v, %v; want original read-limit refusal, without v1 fallback", handle, err)
	}
	var tooLarge *safeio.TooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Path != headerPath || tooLarge.Limit != safeio.DefaultMaxFileSize {
		t.Fatalf("read-limit detail = %+v", tooLarge)
	}
	svc, serviceErr := NewService(ServiceConfig{
		EmbeddingFunc:             fakeEmbeddingFunc(),
		EmbeddingCacheFingerprint: "fp-era-a",
		EmbeddingDimension:        8,
		Corpus:                    reg,
	})
	if serviceErr != nil {
		t.Fatal(serviceErr)
	}
	t.Cleanup(func() { _ = svc.Close() })
	svc.current.projectPath = h.storage
	attachErr := svc.AttachView(context.Background(), key, "root_0123456789abcdef")
	if !errors.Is(attachErr, safeio.ErrTooLarge) || !errors.Is(attachErr, ErrCorpusUnopenable) {
		t.Fatalf("attach lost the size refusal or stage: %v", attachErr)
	}
	for _, stage := range []error{nil, ErrCorpusUnopenable, ErrOverlayUnopenable} {
		wrapped := attachErr
		if stage != nil {
			wrapped = fmt.Errorf("%w: %w", stage, err)
		}
		if Repairable(wrapped) || QuarantineScopeOf(wrapped) != QuarantineNoStore || Transient(wrapped) {
			t.Errorf("size refusal classified incorrectly under stage %v", stage)
		}
	}
	if !snapshotTablePublished(h.gen, key) {
		t.Fatal("read refusal revoked publication")
	}
	if refs, ok := chunkReferenceCounts(h.gen); ok || refs != nil {
		t.Fatalf("unreadable publication must leave references unknowable: %v, %v", refs, ok)
	}
	info, statErr := os.Stat(headerPath)
	if statErr != nil || info.Size() != safeio.DefaultMaxFileSize+1 {
		t.Fatalf("refused header was changed: info=%v err=%v", info, statErr)
	}
}

func TestLargeProjectMetadata_ManifestAndSidecar(t *testing.T) {
	storage := t.TempDir()
	const branch = "main"
	rows := largeIndexRows()
	files := make(map[string]string, len(rows))
	entries := make(map[string]string, len(rows))
	for _, row := range rows {
		files[row.Path] = row.BlobHash
		entries[row.Path] = row.BlobHash + "|1|1|chunker-fp|1"
	}
	root := branchRootPath(storage, branch)
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeCorpusManifest(root, &CorpusManifest{Files: files}); err != nil {
		t.Fatal(err)
	}
	requireLargeMetadata(t, filepath.Join(root, corpusManifestName))
	if got := readCorpusManifest(root); got == nil || len(got.Files) != len(files) {
		t.Fatal("large corpus manifest was treated as absent")
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := fileHashesPathFor(storage, branch)
	if err := os.WriteFile(sidecar, data, 0o644); err != nil {
		t.Fatal(err)
	}
	requireLargeMetadata(t, sidecar)
	if fp := legacySidecarChunkerFP(storage, branch); fp != "chunker-fp" {
		t.Fatalf("large sidecar chunker fingerprint = %q", fp)
	}
	if hashes, ok := legacySidecarHashMap(storage, branch); !ok || len(hashes) != len(files) {
		t.Fatalf("large sidecar hashes: ok=%v entries=%d", ok, len(hashes))
	}
	if sets := corpusSidecarChunkSets(storage, branch); len(sets) != len(files) || !sets[rows[0].Path][0] {
		t.Fatal("large sidecar lost its committed chunk sets")
	}
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	svc.mu.Lock()
	svc.current.projectPath = storage
	svc.current.currentBranch = branch
	svc.loadFileHashes()
	loaded := len(svc.current.fileHashes)
	svc.mu.Unlock()
	if loaded != len(files) {
		t.Fatalf("working-store sidecar load: %d entries, want %d", loaded, len(files))
	}
}
