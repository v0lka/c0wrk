package vectorindex

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
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

func TestStoreGeneration_EraAdvanceCarriesTablesAndRefs(t *testing.T) {
	h := newChunkStoreHarness(t)
	// Seed one published snapshot with a chunk on disk, plus a refs record.
	c := chunkRecordKey("package main")
	if err := writeChunkRecord(h.gen, c, []float32{1, 2, 3, 4, 5, 6, 7, 8}); err != nil {
		t.Fatalf("writeChunkRecord: %v", err)
	}
	rows := []occurrenceRow{{Path: "pkg/a.go", BlobHash: "deadbeef", Chunks: []chunkOccurrence{{C: c, Start: 1, End: 9, Language: "go"}}}}
	if err := writeSnapshotTable(h.gen, "snap_abcdef0123456789", rows, &snapshotTable{ChunkerFP: "k", IgnoreFP: "i", Files: 1, Chunks: 1, UniqueChunks: 1}); err != nil {
		t.Fatalf("writeSnapshotTable: %v", err)
	}
	if err := writeRefRecordIn(filepath.Join(h.gen, storeRefsDirName), "root_key", rootRef{Root: "/tmp/tree", Snapshot: "snap_abcdef0123456789"}); err != nil {
		t.Fatalf("writeRefRecordIn: %v", err)
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
	// Era-independent records carried: table readable in the new generation
	// (without its chunk vectors — they re-fill from cache misses).
	gotRows, header, ok := readSnapshotTable(next, "snap_abcdef0123456789")
	if !ok || len(gotRows) != 1 || gotRows[0].Path != "pkg/a.go" || header.ChunkerFP != "k" {
		t.Fatalf("carried snapshot table = %+v %+v %v", gotRows, header, ok)
	}
	if !chunkRecordExists(oldGen, c) {
		t.Fatal("old generation must keep its chunks until the sweep collects them")
	}
	if _, err := os.Stat(filepath.Join(next, storeRefsDirName, "root_key.ref")); err != nil {
		t.Fatalf("refs record not carried: %v", err)
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
			{C: c1, Start: 1, End: 20, Language: "go"},
			{C: c2, Start: 21, End: 40, Language: "weird;lang,name"},
		}},
		{Path: "a\ttab\tin path.md", BlobHash: "0123abcd", Chunks: []chunkOccurrence{
			{C: c2, Start: 1, End: 5, Language: ""},
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
	if got[1].Chunks[0].C != c1 || got[1].Chunks[0].Start != 1 || got[1].Chunks[0].End != 20 || got[1].Chunks[0].Language != "go" {
		t.Fatalf("chunk 0 = %+v", got[1].Chunks[0])
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
		"two fields":     "only\ttwo",
		"no occurrences": "path\thash\t",
		"bad hash len":   "path\thash\t0123,1,2,go",
		"bad start":      "path\thash\t" + chunkRecordKey("x") + ",notanint,2,go",
		"bad end":        "path\thash\t" + chunkRecordKey("x") + ",1,notanint,go",
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
	// Unpublished: nothing written.
	if snapshotTablePublished(h.gen, key) {
		t.Fatal("fresh snapshot must be unpublished")
	}
	if _, _, ok := readSnapshotTable(h.gen, key); ok {
		t.Fatal("readSnapshotTable must refuse an unpublished snapshot")
	}
	// Torn publish: occurrences.tab without table.json reads as absent.
	if err := writeSnapshotTable(h.gen, key, []occurrenceRow{{Path: "x.go", BlobHash: "h", Chunks: []chunkOccurrence{{C: c, Start: 1, End: 2, Language: "go"}}}}, &snapshotTable{Files: 1, Chunks: 1}); err != nil {
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
