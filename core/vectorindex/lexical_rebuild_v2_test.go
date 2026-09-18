package vectorindex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
)

// recordingLex is a lexical.Index test double that records every upserted
// document (in arrival order) so tests can compare the exact ID+content set
// two rebuild strategies produce.
type recordingLex struct {
	docs []lexical.Doc
}

func (r *recordingLex) Upsert(_ context.Context, docs []lexical.Doc) error {
	r.docs = append(r.docs, docs...)
	return nil
}

func (r *recordingLex) Delete(_ context.Context, _ []string) error { return nil }

func (r *recordingLex) Query(_ context.Context, _ string, _ int) ([]lexical.Hit, error) {
	return nil, nil
}

func (r *recordingLex) Count() (uint64, error) { return uint64(len(r.docs)), nil }

func (r *recordingLex) Close() error { return nil }

// installRecordingLex swaps the service's live lexical index for a recorder
// and returns it. RebuildLexical reads the index through GetLexical, so the
// swap routes every upsert window into the recorder.
func installRecordingLex(t *testing.T, svc *Service) *recordingLex {
	t.Helper()
	rec := &recordingLex{}
	svc.AcquireWriteLock()
	prev := svc.current.lexical
	svc.current.lexical = rec
	svc.ReleaseWriteLock()
	t.Cleanup(func() {
		svc.AcquireWriteLock()
		svc.current.lexical = prev
		svc.ReleaseWriteLock()
	})
	return rec
}

// seedLegacyIndexedFile prepares one file exactly like an older dual-write
// binary would have: real bytes on disk, chunks committed to chromem with
// FULL legacy metadata (raw collection commit, bypassing strippedForCommit),
// and a current-format sidecar entry carrying the active chunker
// fingerprint and the committed chunk set. The lexical index stays EMPTY —
// the state RebuildLexical exists for. Returns the committed documents.
func seedLegacyIndexedFile(t *testing.T, idx *Indexer, svc *Service, absPath, body string) []chromem.Document {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(absPath), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(absPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	vecDocs, _, err := idx.processFile(absPath)
	if err != nil {
		t.Fatalf("processFile(%s): %v", absPath, err)
	}
	if len(vecDocs) == 0 {
		t.Fatalf("processFile(%s) produced no chunks", absPath)
	}
	for i := range vecDocs {
		// Pre-populated unit embedding: the raw commit below must never
		// invoke an embedder.
		vecDocs[i].Embedding = []float32{1, 0, 0, 0}
	}
	svc.AcquireWriteLock()
	err = svc.current.collection.AddDocuments(context.Background(), vecDocs, 1)
	if err == nil {
		info, ok := fileHashInfoFromMetadata(vecDocs[0].Metadata)
		if !ok {
			err = fmt.Errorf("no file-hash info for %s", absPath)
		} else {
			ids := make([]string, len(vecDocs))
			for i := range vecDocs {
				ids[i] = vecDocs[i].ID
			}
			svc.upsertFileHashEntryInfo(info, ids)
		}
	}
	svc.ReleaseWriteLock()
	if err != nil {
		t.Fatalf("seed %s: %v", absPath, err)
	}
	return vecDocs
}

// newLexicalRebuildFixture builds a service+indexer pair over a persistent
// temp project whose chunker configuration matches the service's sidecar
// fingerprint (maxChunkSize=120, overlap=10 — small enough that every file
// yields several chunks).
func newLexicalRebuildFixture(t *testing.T, embedCalls *atomic.Int32) (*Service, *Indexer, string) {
	t.Helper()
	const maxChunkSize, overlap = 120, 10
	fp := ChunkerFingerprint(maxChunkSize, overlap, resolveContentFilterConfig(nil).Fingerprint())
	// Register the index temp dir BEFORE the svc.Close cleanup: t.Cleanup is
	// LIFO, so svc.Close runs first and releases the bleve lexical store
	// (.bolt/.zap) handles before TempDir's RemoveAll. Windows refuses to
	// delete files that still have open handles (EBUSY).
	viDir := t.TempDir()
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:      countingEmbed4(embedCalls),
		EmbeddingDimension: 4,
		ChunkerFingerprint: fp,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", viDir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	idx := NewIndexer(IndexerConfig{
		Service:      svc,
		ChunkFn:      defaultChunkFn,
		MaxChunkSize: maxChunkSize,
		Overlap:      overlap,
	})
	return svc, idx, viDir
}

// recordedSet renders a recorder's documents as a sorted ID→doc map plus
// the sorted key list, the shape the equivalence assertions compare.
func recordedSet(rec *recordingLex) (byID map[string]lexical.Doc, keys []string) {
	byID = make(map[string]lexical.Doc, len(rec.docs))
	for _, d := range rec.docs {
		byID[d.ID] = d
	}
	keys = make([]string, 0, len(byID))
	for k := range byID {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return byID, keys
}

// TestRebuildLexical_SidecarPathMatchesCollectionPath is the equivalence
// pin: on an unchanged corpus, the sidecar-driven v2 rebuild (re-chunk from
// disk, deterministic IDs) must produce EXACTLY the same lexical set — IDs
// AND content — as the legacy collection-enumeration path, without a single
// embedding call.
func TestRebuildLexical_SidecarPathMatchesCollectionPath(t *testing.T) {
	var embedCalls atomic.Int32
	svc, idx, viDir := newLexicalRebuildFixture(t, &embedCalls)
	ws := filepath.Join(viDir, "ws")

	fileA := filepath.Join(ws, "notes.md")
	fileB := filepath.Join(ws, "other.md")
	fileC := filepath.Join(ws, "code.go")
	seedLegacyIndexedFile(t, idx, svc, fileA, repeatLines("alpha notes line with words", 40))
	seedLegacyIndexedFile(t, idx, svc, fileB, repeatLines("beta other markdown words", 40))
	seedLegacyIndexedFile(t, idx, svc, fileC, "package code\n\n"+repeatLines("func helper() { return 42 } // comment", 30))

	if col := svc.GetCollection(); col.Count() == 0 {
		t.Fatal("precondition: collection must be non-empty")
	}

	// Old path: collection enumeration into a recorder.
	oldRec := installRecordingLex(t, svc)
	col := svc.GetCollection()
	if err := idx.rebuildLexicalFromCollection(context.Background(), oldRec, col, col.Count()); err != nil {
		t.Fatalf("rebuildLexicalFromCollection: %v", err)
	}

	// v2 path: sidecar traversal into a fresh recorder.
	v2Rec := installRecordingLex(t, svc)
	if err := idx.RebuildLexical(context.Background()); err != nil {
		t.Fatalf("RebuildLexical (v2): %v", err)
	}

	oldDocs, oldKeys := recordedSet(oldRec)
	v2Docs, v2Keys := recordedSet(v2Rec)
	if len(oldDocs) == 0 {
		t.Fatal("old path produced no documents; fixture is broken")
	}
	if fmt.Sprint(oldKeys) != fmt.Sprint(v2Keys) {
		t.Fatalf("lexical ID sets differ:\nold(%d): %v\nv2(%d): %v", len(oldKeys), oldKeys, len(v2Keys), v2Keys)
	}
	for _, id := range oldKeys {
		o, v := oldDocs[id], v2Docs[id]
		if o.Content != v.Content {
			t.Errorf("doc %s content differs:\nold: %.80q\nv2:  %.80q", id, o.Content, v.Content)
		}
		if o.FilePath != v.FilePath || o.Language != v.Language {
			t.Errorf("doc %s file/language differ: old=%q/%q v2=%q/%q", id, o.FilePath, o.Language, v.FilePath, v.Language)
		}
	}
	if got := embedCalls.Load(); got != 0 {
		t.Errorf("rebuild invoked the embedding function %d times; want 0 on both paths", got)
	}
}

// TestRebuildLexical_V2SkipsChangedFile verifies the disk-verification leg
// of v2: a file whose bytes changed after indexing (sidecar hash stale) is
// skipped with its IDs absent from the rebuilt lexical set, while unchanged
// files are still processed.
func TestRebuildLexical_V2SkipsChangedFile(t *testing.T) {
	var embedCalls atomic.Int32
	svc, idx, viDir := newLexicalRebuildFixture(t, &embedCalls)
	ws := filepath.Join(viDir, "ws")

	fileA := filepath.Join(ws, "stable.md")
	fileB := filepath.Join(ws, "changed.md")
	seedLegacyIndexedFile(t, idx, svc, fileA, repeatLines("stable content words", 40))
	seedLegacyIndexedFile(t, idx, svc, fileB, repeatLines("original content words", 40))

	// Mutate fileB AFTER its sidecar entry was recorded: the entry's hash no
	// longer describes the bytes on disk.
	if err := os.WriteFile(fileB, []byte(repeatLines("rewritten completely different", 40)), 0o644); err != nil {
		t.Fatalf("rewrite changed.md: %v", err)
	}

	rec := installRecordingLex(t, svc)
	if err := idx.RebuildLexical(context.Background()); err != nil {
		t.Fatalf("RebuildLexical: %v", err)
	}
	docs, _ := recordedSet(rec)
	if len(docs) == 0 {
		t.Fatal("v2 produced no documents at all")
	}
	stableSeen := false
	for _, d := range docs {
		if d.FilePath == fileB {
			t.Errorf("changed file %s must be skipped, but its document %s was indexed", fileB, d.ID)
		}
		if d.FilePath == fileA {
			stableSeen = true
		}
	}
	if !stableSeen {
		t.Errorf("unchanged file %s must be processed", fileA)
	}
	if got := embedCalls.Load(); got != 0 {
		t.Errorf("rebuild invoked the embedding function %d times; want 0", got)
	}
}

// TestRebuildLexical_FallbackWhenSidecarIneligible pins the transitional
// nets: with an EMPTY sidecar the rebuild enumerates the collection, and
// with a sidecar whose entries cannot certify the active chunker
// configuration (legacy backfilled entries carry no fingerprint) it also
// falls back after the v2 pass found nothing — a pre-upgrade project must
// never end up with a permanently empty lexical index.
func TestRebuildLexical_FallbackWhenSidecarIneligible(t *testing.T) {
	var embedCalls atomic.Int32
	svc, idx, viDir := newLexicalRebuildFixture(t, &embedCalls)
	ws := filepath.Join(viDir, "ws")

	fileA := filepath.Join(ws, "a.md")
	fileB := filepath.Join(ws, "b.go")
	seedLegacyIndexedFile(t, idx, svc, fileA, repeatLines("alpha words for lexical", 40))
	seedLegacyIndexedFile(t, idx, svc, fileB, "package b\n\n"+repeatLines("var x = 1", 30))
	col := svc.GetCollection()
	totalDocs := col.Count()

	// Degraded sidecar: every entry loses its fingerprint and chunk set —
	// the exact shape the file-hash backfill produces for legacy documents
	// (queryCollectionFileHashes composes entries without either field).
	svc.AcquireWriteLock()
	degraded := make(map[string]string, len(svc.current.fileHashes))
	for fp, entry := range svc.current.fileHashes {
		hash := fileHashEntryHash(entry)
		var size, mtime int64 = 1000, 1700000000000000000
		if _, s, m, ok := parseFileHashEntry(entry); ok {
			size, mtime = s, m
		}
		degraded[fp] = fileHashInfo{filePath: fp, hash: hash,
			size: strconv.FormatInt(size, 10), mtime: strconv.FormatInt(mtime, 10)}.entry("", "")
	}
	svc.current.fileHashes = degraded
	svc.ReleaseWriteLock()

	rec := installRecordingLex(t, svc)
	if err := idx.RebuildLexical(context.Background()); err != nil {
		t.Fatalf("RebuildLexical: %v", err)
	}
	if got := len(rec.docs); got != totalDocs {
		t.Fatalf("fallback enumerated %d documents, want %d (the full collection)", got, totalDocs)
	}
	if got := embedCalls.Load(); got != 0 {
		t.Errorf("fallback invoked the embedding function %d times; want 0 (unit-vector enumeration)", got)
	}

	// Empty sidecar: the same fallback, verbatim.
	rec2 := installRecordingLex(t, svc)
	svc.AcquireWriteLock()
	svc.current.fileHashes = make(map[string]string)
	svc.ReleaseWriteLock()
	if err := idx.RebuildLexical(context.Background()); err != nil {
		t.Fatalf("RebuildLexical (empty sidecar): %v", err)
	}
	if got := len(rec2.docs); got != totalDocs {
		t.Fatalf("empty-sidecar fallback enumerated %d documents, want %d", got, totalDocs)
	}
}

// repeatLines renders n lines of the given text, each suffixed with its line
// number so chunks over the same words still differ byte-wise.
func repeatLines(text string, n int) string {
	out := make([]byte, 0, n*(len(text)+8))
	for i := 1; i <= n; i++ {
		out = append(out, []byte(fmt.Sprintf("%s %d\n", text, i))...)
	}
	return string(out)
}

// TestRebuildLexicalFromCollection_StaleLargerCountTolerated pins the fix for
// the nResults race: the enumeration's nResults must be sampled from the LIVE
// collection under the read lock, not forwarded from the caller's earlier
// sample. RebuildLexical reads its count before waiting for the file-hash
// migration and walking the sidecar; a concurrent incremental pass can delete
// documents in that window, and chromem hard-errors once nResults exceeds the
// live document count — which used to leave the BM25 backfill permanently
// failing. Passing an inflated count here simulates that shrink
// deterministically.
func TestRebuildLexicalFromCollection_StaleLargerCountTolerated(t *testing.T) {
	var embedCalls atomic.Int32
	svc, idx, viDir := newLexicalRebuildFixture(t, &embedCalls)
	ws := filepath.Join(viDir, "ws")

	seedLegacyIndexedFile(t, idx, svc, filepath.Join(ws, "a.md"), repeatLines("alpha notes line with words", 40))
	seedLegacyIndexedFile(t, idx, svc, filepath.Join(ws, "b.md"), repeatLines("beta other markdown words", 40))

	col := svc.GetCollection()
	live := col.Count()
	if live == 0 {
		t.Fatal("precondition: collection must be non-empty")
	}

	rec := installRecordingLex(t, svc)
	staleCount := live + 5 // documents deleted after the caller sampled
	if err := idx.rebuildLexicalFromCollection(context.Background(), rec, col, staleCount); err != nil {
		t.Fatalf("rebuildLexicalFromCollection with a stale larger count: %v", err)
	}
	if got := len(rec.docs); got != live {
		t.Errorf("enumerated %d lexical docs, want the live count %d (nResults must follow the live collection)", got, live)
	}
}

// TestRebuildLexicalFromCollection_EmptyLiveCollection covers the companion
// edge: the collection can empty ENTIRELY in the sampling window, and chromem
// rejects nResults <= 0 just as it rejects nResults > live — the rebuild must
// return nil without querying rather than error.
func TestRebuildLexicalFromCollection_EmptyLiveCollection(t *testing.T) {
	var embedCalls atomic.Int32
	svc, idx, _ := newLexicalRebuildFixture(t, &embedCalls)

	// A brand-new branch has an empty collection; the caller's stale count
	// still claims the pre-shrink sample.
	if err := svc.SwitchBranch(context.Background(), "emptied"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	col := svc.GetCollection()
	if col == nil || col.Count() != 0 {
		t.Fatal("precondition: the branch collection must be empty")
	}

	rec := installRecordingLex(t, svc)
	if err := idx.rebuildLexicalFromCollection(context.Background(), rec, col, 7); err != nil {
		t.Fatalf("rebuildLexicalFromCollection on an emptied collection: %v", err)
	}
	if got := len(rec.docs); got != 0 {
		t.Errorf("enumerated %d docs from an empty collection, want 0", got)
	}
}
