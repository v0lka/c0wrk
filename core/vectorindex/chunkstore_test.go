package vectorindex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	chromem "github.com/philippgille/chromem-go"
)

// chunkStoreHarness is a temp-dir storage root with a ensured generation.
type chunkStoreHarness struct {
	t       *testing.T
	storage string
	gen     string
}

func newChunkStoreHarness(t *testing.T) *chunkStoreHarness {
	t.Helper()
	h := &chunkStoreHarness{t: t, storage: t.TempDir()}
	h.ensureGen("fp-era-a", 8)
	return h
}

func (h *chunkStoreHarness) ensureGen(fp string, dims int) string {
	h.t.Helper()
	dir, err := ensureStoreGeneration(h.storage, fp, dims)
	if err != nil {
		h.t.Fatalf("ensureStoreGeneration: %v", err)
	}
	h.gen = dir
	return dir
}

func TestStoreGeneration_CreatedWithCurrentPointer(t *testing.T) {
	h := newChunkStoreHarness(t)
	data, err := os.ReadFile(currentPointerPath(h.storage))
	if err != nil {
		t.Fatalf("CURRENT pointer: %v", err)
	}
	if string(data) != "0" {
		t.Fatalf("CURRENT = %q, want \"0\"", string(data))
	}
	dir, m, ok := readCurrentGeneration(h.storage)
	if !ok || dir != generationPath(h.storage, 0) {
		t.Fatalf("readCurrentGeneration = %q %v %v", dir, m, ok)
	}
	if m.EmbeddingFP != "fp-era-a" || m.Dims != 8 || m.Format != storeFormatV2Generation {
		t.Fatalf("manifest = %+v", m)
	}
	for _, sub := range []string{storeChunksDirName, storeSnapshotsDirName, storeRefsDirName, storeRootsDirName} {
		if _, err := os.Stat(filepath.Join(h.gen, sub)); err != nil {
			t.Fatalf("generation lacks %s: %v", sub, err)
		}
	}
}

func TestStoreGeneration_IdempotentSameEra(t *testing.T) {
	h := newChunkStoreHarness(t)
	again, err := ensureStoreGeneration(h.storage, "fp-era-a", 8)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if again != h.gen {
		t.Fatalf("ensure returned %q, want the same generation %q", again, h.gen)
	}
}

func TestStoreGeneration_EraAdvanceStartsFreshGeneration(t *testing.T) {
	h := newChunkStoreHarness(t)
	// Seed one published snapshot with a chunk on disk, plus a storage-root
	// ownership marker (refs live OUTSIDE the generations tree, so an era
	// advance never disturbs them).
	c := chunkRecordKey("package main")
	if err := writeChunkRecord(h.gen, c, []float32{1, 2, 3, 4, 5, 6, 7, 8}); err != nil {
		t.Fatalf("writeChunkRecord: %v", err)
	}
	rows := []occurrenceRow{{Path: "pkg/a.go", BlobHash: "deadbeef", Chunks: []chunkOccurrence{{C: c, Index: 0, Start: 1, End: 9, Language: "go"}}}}
	if err := writeSnapshotTable(h.gen, "snap_abcdef0123456789", rows, &snapshotTable{ChunkerFP: "k", IgnoreFP: "i", Files: 1, Chunks: 1, UniqueChunks: 1}); err != nil {
		t.Fatalf("writeSnapshotTable: %v", err)
	}
	if err := writeRootRef(h.storage, "root_key", "/tmp/tree", "snap_abcdef0123456789"); err != nil {
		t.Fatalf("writeRootRef: %v", err)
	}

	oldGen := h.gen
	next := h.ensureGen("fp-era-b", 16)
	if next == oldGen {
		t.Fatal("era advance must create a new generation directory")
	}
	data, _ := os.ReadFile(currentPointerPath(h.storage))
	if string(data) != "1" {
		t.Fatalf("CURRENT = %q after advance, want \"1\"", string(data))
	}
	// Nothing is carried across an era boundary: chunk vectors are
	// era-bound and the §3 reader fails closed on a missing chunk record,
	// so the successor generation must read ABSENT for the old snapshot
	// (the next attach's build pass re-publishes it in the new era).
	if gotRows, _, ok := readSnapshotTable(next, "snap_abcdef0123456789"); ok || len(gotRows) != 0 {
		t.Fatalf("snapshot table carried into the new era = %+v %v", gotRows, ok)
	}
	if !chunkRecordExists(oldGen, c) {
		t.Fatal("old generation must keep its chunks until the sweep collects them")
	}
	// Ownership markers are era-independent: the storage-root refs/ tree
	// survives the advance untouched.
	refs, ok := readRootRefs(h.storage)
	if !ok || refs["root_key"].Snapshot != "snap_abcdef0123456789" {
		t.Fatalf("storage-root refs after advance = %+v %v", refs["root_key"], ok)
	}
	// The new generation is the readable one under the new era.
	if _, m, ok := readCurrentGeneration(h.storage); !ok || m.EmbeddingFP != "fp-era-b" || m.Dims != 16 {
		t.Fatalf("post-advance CURRENT generation = %+v %v", m, ok)
	}
}

func TestStoreGeneration_UnknownFormatReadsAbsent(t *testing.T) {
	h := newChunkStoreHarness(t)
	if err := os.WriteFile(filepath.Join(h.gen, storeGenerationManifestName), []byte(`{"format":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readCurrentGeneration(h.storage); ok {
		t.Fatal("unknown manifest format must read as absent")
	}
	if _, err := ensureStoreGeneration(h.storage, "fp-era-a", 8); err == nil {
		t.Fatal("ensure must refuse to adopt a foreign-format tree")
	}
}

func TestChunkRecord_RoundTripAndDedup(t *testing.T) {
	h := newChunkStoreHarness(t)
	vec := []float32{0.5, -1.25, 3, 4, 0, 6, 7, 8}
	c := chunkRecordKey("some chunk text")
	if err := writeChunkRecord(h.gen, c, vec); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Dedup: a second write is a no-op that must not error or corrupt.
	if err := writeChunkRecord(h.gen, c, vec); err != nil {
		t.Fatalf("dedup write: %v", err)
	}
	got, err := readChunkRecord(h.gen, c, 8)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for i := range vec {
		if got[i] != vec[i] {
			t.Fatalf("vector[%d] = %v, want %v", i, got[i], vec[i])
		}
	}
	if _, err := readChunkRecord(h.gen, c, 16); err == nil {
		t.Fatal("wrong dims must fail decode")
	}
	if _, err := readChunkRecord(h.gen, chunkRecordKey("missing"), 8); err == nil {
		t.Fatal("missing record must fail")
	}
	if !chunkRecordExists(h.gen, c) || chunkRecordExists(h.gen, chunkRecordKey("missing")) {
		t.Fatal("chunkRecordExists disagrees with disk")
	}
}

func TestChunkRecordKey_NormalizationIsEmbeddingIdentity(t *testing.T) {
	// CRLF and CR normalize to LF: the same chunk written on different
	// operating systems shares one record (one embedding, one vector).
	if chunkRecordKey("line one\r\nline two\r\n") != chunkRecordKey("line one\nline two\n") {
		t.Fatal("CRLF normalization missing from chunk identity")
	}
	if chunkRecordKey("a\rb") != chunkRecordKey("a\nb") {
		t.Fatal("CR normalization missing from chunk identity")
	}
	if chunkRecordKey("alpha") == chunkRecordKey("beta") {
		t.Fatal("distinct texts must have distinct identities")
	}
}

func TestOccurrenceTab_RoundTripWithEscaping(t *testing.T) {
	c1 := chunkRecordKey("first chunk")
	c2 := chunkRecordKey("second, chunk; weird")
	rows := []occurrenceRow{
		{Path: "b/path with space.go", BlobHash: "cafebabe", Chunks: []chunkOccurrence{
			{C: c1, Index: 0, Start: 1, End: 20, Language: "go"},
			{C: c2, Index: 7, Start: 21, End: 40, Language: "weird;lang,name"},
		}},
		{Path: "a\ttab\tin path.md", BlobHash: "0123abcd", Chunks: []chunkOccurrence{
			{C: c2, Index: 0, Start: 1, End: 5, Language: ""},
		}},
	}
	got, err := decodeOccurrenceTab(encodeOccurrenceTab(rows))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	// Sorted by path on encode.
	if got[0].Path != "a\ttab\tin path.md" || got[1].Path != "b/path with space.go" {
		t.Fatalf("rows not sorted/round-tripped: %+v", got)
	}
	if got[1].BlobHash != "cafebabe" || len(got[1].Chunks) != 2 {
		t.Fatalf("row 1 = %+v", got[1])
	}
	if got[1].Chunks[0].C != c1 || got[1].Chunks[0].Index != 0 || got[1].Chunks[0].Start != 1 || got[1].Chunks[0].End != 20 || got[1].Chunks[0].Language != "go" {
		t.Fatalf("chunk 0 = %+v", got[1].Chunks[0])
	}
	if got[1].Chunks[1].Index != 7 {
		t.Fatalf("chunk index must round-trip (positional identity): %+v", got[1].Chunks[1])
	}
	if got[1].Chunks[1].Language != "weird;lang,name" {
		t.Fatalf("language escaping broken: %+v", got[1].Chunks[1])
	}
	if got[0].Chunks[0].Language != "" {
		t.Fatalf("empty language must round-trip empty: %+v", got[0].Chunks[0])
	}
	// Determinism: same rows, same bytes.
	b1, b2 := encodeOccurrenceTab(rows), encodeOccurrenceTab(rows)
	if !bytes.Equal(b1, b2) {
		t.Fatal("occurrence tab encoding is not deterministic")
	}
}

func TestOccurrenceTab_MalformedFails(t *testing.T) {
	for name, body := range map[string]string{
		"two fields":       "only\ttwo",
		"no occurrences":   "path\thash\t",
		"four-field chunk": "path\thash\t" + chunkRecordKey("x") + ",1,2,go",
		"bad hash len":     "path\thash\t0123,0,1,2,go",
		"bad index":        "path\thash\t" + chunkRecordKey("x") + ",zero,1,2,go",
		"bad start":        "path\thash\t" + chunkRecordKey("x") + ",0,notanint,2,go",
		"bad end":          "path\thash\t" + chunkRecordKey("x") + ",0,1,notanint,go",
	} {
		if _, err := decodeOccurrenceTab([]byte(body)); err == nil {
			t.Fatalf("%s: decode must fail", name)
		}
	}
	if rows, err := decodeOccurrenceTab(nil); err != nil || len(rows) != 0 {
		t.Fatalf("empty body must decode to zero rows: %+v %v", rows, err)
	}
}

func TestSnapshotTable_HeaderIsCompletionMarker(t *testing.T) {
	h := newChunkStoreHarness(t)
	key := "snap_1111222233334444"
	c := chunkRecordKey("payload")
	if err := writeChunkRecord(h.gen, c, make([]float32, 8)); err != nil {
		t.Fatalf("writeChunkRecord: %v", err)
	}
	// Unpublished: nothing written.
	if snapshotTablePublished(h.gen, key) {
		t.Fatal("fresh snapshot must be unpublished")
	}
	if _, _, ok := readSnapshotTable(h.gen, key); ok {
		t.Fatal("readSnapshotTable must refuse an unpublished snapshot")
	}
	// A publish over a MISSING chunk record defers (verify-before-publish):
	// the table is the completion marker, so it must never land over a
	// record the open path would fail closed on.
	ghost := chunkRecordKey("never written")
	if err := writeSnapshotTable(h.gen, key, []occurrenceRow{{Path: "g.go", BlobHash: "h", Chunks: []chunkOccurrence{{C: ghost, Index: 0, Start: 1, End: 2, Language: "go"}}}}, &snapshotTable{Files: 1, Chunks: 1}); err == nil {
		t.Fatal("writeSnapshotTable must defer a publish over a missing chunk record")
	}
	if snapshotTablePublished(h.gen, key) {
		t.Fatal("a deferred publish must not mark the snapshot published")
	}
	// Torn publish: occurrences.tab without table.json reads as absent.
	if err := writeSnapshotTable(h.gen, key, []occurrenceRow{{Path: "x.go", BlobHash: "h", Chunks: []chunkOccurrence{{C: c, Index: 0, Start: 1, End: 2, Language: "go"}}}}, &snapshotTable{Files: 1, Chunks: 1}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Remove(filepath.Join(snapshotDir(h.gen, key), snapshotTableName)); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readSnapshotTable(h.gen, key); ok {
		t.Fatal("occurrences without the header must read as absent (torn publish)")
	}
}

func TestOverlayTableAndViewState(t *testing.T) {
	h := newChunkStoreHarness(t)
	rootKey := "root_abcd1234abcd5678"
	c := chunkRecordKey("overlay chunk")
	rows := []occurrenceRow{{Path: "dirty.go", BlobHash: "beef", Chunks: []chunkOccurrence{{C: c, Start: 3, End: 8, Language: "go"}}}}
	if err := writeOverlayTable(h.gen, rootKey, rows); err != nil {
		t.Fatalf("writeOverlayTable: %v", err)
	}
	got, ok := readOverlayTable(h.gen, rootKey)
	if !ok || len(got) != 1 || got[0].Path != "dirty.go" {
		t.Fatalf("overlay table = %+v %v", got, ok)
	}
	if _, ok := readOverlayTable(h.gen, "root_0000000000000000"); ok {
		t.Fatal("absent overlay table must read !ok")
	}
	if err := writeRootViewState(h.gen, rootKey, "snap_aaaa"); err != nil {
		t.Fatalf("writeRootViewState: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootStateDir(h.gen, rootKey), rootViewStateName)); err != nil {
		t.Fatalf("view.json: %v", err)
	}
}

func TestChunkEnumeration_Shards(t *testing.T) {
	h := newChunkStoreHarness(t)
	cs := []string{chunkRecordKey("a"), chunkRecordKey("b"), chunkRecordKey("c")}
	for _, c := range cs {
		if err := writeChunkRecord(h.gen, c, make([]float32, 8)); err != nil {
			t.Fatalf("write %s: %v", c, err)
		}
	}
	dirs, err := chunkShardDirs(h.gen)
	if err != nil {
		t.Fatalf("chunkShardDirs: %v", err)
	}
	total := 0
	for _, d := range dirs {
		ids, err := chunkIDsFromShard(d)
		if err != nil {
			t.Fatalf("chunkIDsFromShard: %v", err)
		}
		total += len(ids)
		for _, id := range ids {
			if id != chunkRecordKey("a") && id != chunkRecordKey("b") && id != chunkRecordKey("c") {
				t.Fatalf("unexpected chunk id %q", id)
			}
		}
	}
	if total != len(cs) {
		t.Fatalf("enumerated %d chunks, want %d", total, len(cs))
	}
}

// TestV2VectorCandidates_TopFanoutExact pins the §3 vector leg's top-fanout
// admission: the fill-time working set must be a REAL top-K of the
// scored occurrences — a single ordering pass over a hostile (here:
// reverse-sorted) fill permutation parks the true best matches outside the
// working set, and the admission threshold then rejects them forever.
func TestV2VectorCandidates_TopFanoutExact(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer func() { _ = svc.Close() }()

	qvec, err := svc.embeddingFunc(context.Background(), "query")
	if err != nil {
		t.Fatalf("embedding the query: %v", err)
	}
	// Chunk k's vector rides qvec plus an orthogonal component growing
	// with k, so its cosine to the query is STRICTLY decreasing in k:
	// k=0 is the best possible match, k=9 the worst. The table lists the
	// chunks in exactly REVERSE quality order — the permutation a single
	// bubble pass cannot sort.
	v := &v2Corpus{
		vectors: make(map[string][]float32, 10),
		byID:    make(map[string]v2DocRef, 10),
	}
	bestPaths := make(map[string]bool, 3)
	for k := 0; k < 10; k++ {
		path := fmt.Sprintf("f%02d.go", 9-k) // ascending path = descending quality
		vec := make([]float32, len(qvec))
		copy(vec, qvec)
		vec[5] += float32(k+1) / 4.0
		c := "c" + strconv.Itoa(k)
		v.rows = append(v.rows, occurrenceRow{
			Path:     path,
			BlobHash: "blob_" + path,
			Chunks:   []chunkOccurrence{{C: c, Index: 0, Start: 1, End: 9, Language: "go"}},
		})
		v.vectors[c] = vec
		v.byID[DocumentID(path, 0)] = v2DocRef{row: len(v.rows) - 1, chunk: 0}
		if k < 3 {
			bestPaths[path] = true
		}
	}

	svc.mu.Lock()
	svc.current.fileHashes = make(map[string]string, len(v.rows))
	for _, row := range v.rows {
		svc.current.fileHashes[row.Path] = row.BlobHash
	}
	svc.mu.Unlock()

	svc.mu.RLock()
	got, err := svc.v2VectorCandidates(context.Background(), v, "query", 3, "", nil, nil)
	svc.mu.RUnlock()
	if err != nil {
		t.Fatalf("v2VectorCandidates: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want the fanout of 3", len(got))
	}
	for i, r := range got {
		rel := r.Metadata[relPathMetaKey]
		if !bestPaths[rel] {
			t.Fatalf("candidate %d = %s (sim %v): outside the true top-3 — the fill-time sort admitted a wrong survivor", i, rel, r.Similarity)
		}
		delete(bestPaths, rel)
		if i > 0 && got[i-1].Similarity < r.Similarity {
			t.Fatalf("candidates not similarity-ordered: %v then %v", got[i-1].Similarity, r.Similarity)
		}
	}
}

// TestRecordChunkRecords_PoisonDropsAccumulatedRow pins the sticky poison's
// CROSS-BATCH scope: a poisoned restart batch must also drop the path's
// accumulated row, so a later continuation batch cannot merge its tail into
// the PREVIOUS version's row (adopted from the last flushed table) and
// flush a two-version chimera that the count rule accepts.
func TestRecordChunkRecords_PoisonDropsAccumulatedRow(t *testing.T) {
	h := newChunkStoreHarness(t)
	svc, err := NewService(ServiceConfig{
		// The embedder always fails: a shadow-embed (empty document
		// embedding) is the poison trigger.
		EmbeddingFunc: func(_ context.Context, _ string) ([]float32, error) {
			return nil, errors.New("boom")
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer func() { _ = svc.Close() }()

	svc.mu.Lock()
	defer svc.mu.Unlock()
	ps := svc.current
	ps.projectPath = h.storage
	ps.storeGen = h.gen

	doc := func(idx int, blob, content string, embedded bool) chromem.Document {
		d := chromem.Document{
			ID:      DocumentID("a.go", idx),
			Content: content,
			Metadata: map[string]string{
				relPathMetaKey: "a.go",
				"content_hash": blob,
				"start_line":   strconv.Itoa(idx*5 + 1),
				"end_line":     strconv.Itoa(idx*5 + 5),
				"language":     "go",
			},
		}
		if embedded {
			d.Embedding = make([]float32, 8)
		}
		return d
	}

	// The previous version's row exactly as a completed flush would have
	// adopted it: old chunks 0..5 under blobOld.
	oldRow := occurrenceRow{Path: "a.go", BlobHash: "blobOld"}
	for i := 0; i < 6; i++ {
		c := chunkRecordKey(fmt.Sprintf("old chunk %d body", i))
		oldRow.Chunks = append(oldRow.Chunks, chunkOccurrence{C: c, Index: i, Start: i*5 + 1, End: i*5 + 5, Language: "go"})
	}
	ps.chunkRows = map[string]occurrenceRow{"a.go": oldRow}

	// Restart batch (chunk 0 present) under blobNew: chunk 0 lands, chunk 1
	// has no pre-computed embedding and the failing shadow-embed poisons the
	// path — dropping the pending row AND the accumulated old row.
	svc.recordChunkRecordsLocked(context.Background(), []chromem.Document{
		doc(0, "blobNew", "new chunk 0 body", true),
		doc(1, "blobNew", "new chunk 1 body", false), // poison trigger
	})
	if _, still := ps.chunkRows["a.go"]; still {
		t.Fatal("the poisoned batch left the previous version's accumulated row in place — a continuation batch would merge into it")
	}

	// Continuation batch (no chunk 0) under blobNew: without a base it must
	// start a FRESH tail-only row, never resurrect the old version's prefix.
	svc.recordChunkRecordsLocked(context.Background(), []chromem.Document{
		doc(4, "blobNew", "new chunk 4 body", true),
		doc(5, "blobNew", "new chunk 5 body", true),
	})
	row, ok := ps.chunkRows["a.go"]
	if !ok {
		t.Fatal("the continuation batch produced no row")
	}
	if row.BlobHash != "blobNew" {
		t.Errorf("row blob = %q; want blobNew", row.BlobHash)
	}
	if len(row.Chunks) != 2 || row.Chunks[0].Index != 4 || row.Chunks[1].Index != 5 {
		t.Fatalf("row = %+v; want a fresh tail-only row of indices [4 5] — an old-prefix merge is the two-version chimera", row.Chunks)
	}
	for _, occ := range row.Chunks {
		if occ.C != chunkRecordKey(fmt.Sprintf("new chunk %d body", occ.Index)) {
			t.Fatalf("occurrence %d carries %s; want the NEW version's chunk identity", occ.Index, occ.C)
		}
	}
}

// TestRecordChunkRecords_ContinuationKeepsTrueIndices pins the live §3
// writer's positional identity: a mid-file chunk dropped by the
// commit path (its per-text embedding failure drops exactly that document
// before the batch lands) must leave a GAP in the file's 0..n-1 index
// sequence — the continuation batch's chunks keep their TRUE indices and
// never shift into the dropped slot, so the durable row's DocumentID space
// stays aligned with the lexical leg's.
func TestRecordChunkRecords_ContinuationKeepsTrueIndices(t *testing.T) {
	h := newChunkStoreHarness(t)
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer func() { _ = svc.Close() }()

	svc.mu.Lock()
	defer svc.mu.Unlock()
	ps := svc.current
	ps.projectPath = h.storage
	ps.storeGen = h.gen

	doc := func(idx, start int) chromem.Document {
		return chromem.Document{
			ID:        DocumentID("a.go", idx),
			Content:   fmt.Sprintf("chunk %d body", idx),
			Embedding: make([]float32, 8), // non-empty: the batch skips the shadow-embed path
			Metadata: map[string]string{
				relPathMetaKey: "a.go",
				"content_hash": "blobA",
				"start_line":   strconv.Itoa(start),
				"end_line":     strconv.Itoa(start + 4),
				"language":     "go",
			},
		}
	}
	// Base batch: chunks 0..2 commit; chunk 3 never reaches the recorder.
	svc.recordChunkRecordsLocked(context.Background(), []chromem.Document{doc(0, 1), doc(1, 6), doc(2, 11)})
	// Continuation batch: chunks 4 and 5 land in a later commit window.
	svc.recordChunkRecordsLocked(context.Background(), []chromem.Document{doc(4, 21), doc(5, 26)})

	row, ok := ps.chunkRows["a.go"]
	if !ok {
		t.Fatal("the overlay's chunk row is missing")
	}
	wantIdx := []int{0, 1, 2, 4, 5}
	if len(row.Chunks) != len(wantIdx) {
		t.Fatalf("chunks = %+v; want indices %v (the drop leaves a gap, never a shift)", row.Chunks, wantIdx)
	}
	for i, occ := range row.Chunks {
		if occ.Index != wantIdx[i] {
			t.Fatalf("chunk %d index = %d, want %d — a positional shift misaligns every later DocumentID", i, occ.Index, wantIdx[i])
		}
		wantC := chunkRecordKey(fmt.Sprintf("chunk %d body", occ.Index))
		if occ.C != wantC {
			t.Fatalf("chunk %d content identity = %s, want %s", i, occ.C, wantC)
		}
		if !chunkRecordExists(h.gen, occ.C) {
			t.Fatalf("chunk %d record missing on disk", i)
		}
	}
}
