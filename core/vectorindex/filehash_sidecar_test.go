package vectorindex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	chromem "github.com/philippgille/chromem-go"
)

// TestGetCollectionFileHashes_NoEmbeddingAfterPopulation verifies that after
// AddDocuments has populated the file-hash sidecar, reading the hashes does NOT
// invoke the embedding function — fixing the wasted "running inference" on every
// no-op ValidateCollection pass (the root cause of the nightly reindex churn).
func TestGetCollectionFileHashes_NoEmbeddingAfterPopulation(t *testing.T) {
	var embedCalls atomic.Int32
	embed := func(_ context.Context, _ string) ([]float32, error) {
		embedCalls.Add(1)
		return []float32{0.1, 0.2}, nil
	}

	svc, err := NewService(ServiceConfig{EmbeddingFunc: embed})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Create the temp dir BEFORE registering svc.Close so the LIFO cleanup
	// order runs Close first and TempDir RemoveAll last. On Windows the bleve
	// .bolt handles must be released before the temp dir is deleted.
	dir := t.TempDir()
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	// Add documents (this embeds + populates the sidecar).
	docs := []chromem.Document{
		{ID: "d1", Content: "hello", Metadata: map[string]string{"file_path": filepath.Join(dir, "a.go"), "content_hash": "hashA"}},
		{ID: "d2", Content: "world", Metadata: map[string]string{"file_path": filepath.Join(dir, "b.go"), "content_hash": "hashB"}},
	}
	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments: %v", err)
	}
	svc.ReleaseWriteLock()

	embedCalls.Store(0)

	// Reading hashes must NOT embed now that the sidecar is populated.
	svc.mu.RLock()
	hashes, err := svc.getCollectionFileHashes()
	svc.mu.RUnlock()
	if err != nil {
		t.Fatalf("getCollectionFileHashes: %v", err)
	}
	if got := embedCalls.Load(); got != 0 {
		t.Errorf("expected 0 embedding calls after sidecar population, got %d", got)
	}
	if hashes[filepath.Join(dir, "a.go")] != "hashA" || hashes[filepath.Join(dir, "b.go")] != "hashB" {
		t.Errorf("unexpected hashes: %v", hashes)
	}
}

// TestSwitchBranch_FileHashMigrationIsAsync verifies that SwitchBranch does NOT
// perform the sidecar backfill (which embeds the query vector) synchronously
// when a non-empty collection exists but no sidecar does (the upgrade / new-
// machine scenario). Instead the backfill is deferred to a background goroutine
// and settles before ValidateCollection runs via WaitFileHashMigration.
func TestSwitchBranch_FileHashMigrationIsAsync(t *testing.T) {
	dir := t.TempDir()

	var (
		embedCalls atomic.Int32
		gateOpen   atomic.Bool
		block      = make(chan struct{})
		release    sync.Once
	)
	embed := func(_ context.Context, _ string) ([]float32, error) {
		embedCalls.Add(1)
		if !gateOpen.Load() {
			<-block // hold the migration's query embedding until released
		}
		return []float32{0.1, 0.2}, nil
	}
	unblock := func() {
		gateOpen.Store(true)
		release.Do(func() { close(block) })
	}

	// Build a non-empty, persisted collection via service A (gate open so A can
	// embed freely).
	gateOpen.Store(true)
	a, err := NewService(ServiceConfig{EmbeddingFunc: embed})
	if err != nil {
		t.Fatalf("NewService A: %v", err)
	}
	if err := a.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject A: %v", err)
	}
	if err := a.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch A: %v", err)
	}
	docs := []chromem.Document{
		{ID: "d1", Content: "alpha", Metadata: map[string]string{"file_path": filepath.Join(dir, "a.go"), "content_hash": "hA"}},
		{ID: "d2", Content: "beta", Metadata: map[string]string{"file_path": filepath.Join(dir, "b.go"), "content_hash": "hB"}},
	}
	// Seed via raw chromem, NOT Service.AddDocuments: the service commit
	// path now strips per-file metadata (see strippedForCommit), while the
	// migration under test exists precisely for PRE-upgrade collections
	// whose documents still carry content_hash in their stored metadata.
	// Embeddings come from the (gate-open) chromem-side embedding func.
	if err := a.current.collection.AddDocuments(context.Background(), docs, 1); err != nil {
		t.Fatalf("chromem AddDocuments A: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close A: %v", err)
	}

	// Simulate the upgrade scenario: collection present, sidecar absent.
	sidecar := filepath.Join(dir, "file_hashes_"+collectionName("main")+".json")
	if err := os.Remove(sidecar); err != nil {
		t.Fatalf("remove sidecar: %v", err)
	}

	// Reopen with the gate CLOSED. If SwitchBranch ran the backfill
	// synchronously it would block here on the embedding gate (test would hang
	// until the go-test timeout); the fix defers it to a goroutine.
	gateOpen.Store(false)
	b, err := NewService(ServiceConfig{EmbeddingFunc: embed})
	if err != nil {
		t.Fatalf("NewService B: %v", err)
	}
	t.Cleanup(func() { unblock(); _ = b.Close() })

	if err := b.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject B: %v", err)
	}
	if err := b.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch B: %v", err)
	}
	// SwitchBranch returned without completing the backfill: it is pending in
	// the background (blocked on the gate), proving the migration is async.
	if !b.current.fileHashMigrationPending.Load() {
		t.Fatal("expected file-hash migration to be pending (deferred to background)")
	}

	// Release the gate and wait for the background backfill to settle.
	unblock()
	if err := b.WaitFileHashMigration(context.Background()); err != nil {
		t.Fatalf("WaitFileHashMigration: %v", err)
	}
	if b.current.fileHashMigrationPending.Load() {
		t.Fatal("expected file-hash migration to be settled after waiting")
	}

	// The migrated map must reflect the collection built by A.
	b.mu.RLock()
	hashes, err := b.getCollectionFileHashes()
	b.mu.RUnlock()
	if err != nil {
		t.Fatalf("getCollectionFileHashes: %v", err)
	}
	if hashes[filepath.Join(dir, "a.go")] != "hA" || hashes[filepath.Join(dir, "b.go")] != "hB" {
		t.Errorf("unexpected migrated hashes: %v", hashes)
	}
}

// newFormatEntry returns the current-format sidecar value
// ("hash|size|mtimeUnixNano|chunkerFP") for the given file content and stat,
// mirroring what processFile's metadata + upsertFileHashes compose in
// production for a default-configured Service (no explicit
// ServiceConfig.ChunkerFingerprint → package-default chunker config).
func newFormatEntry(t *testing.T, content []byte, info os.FileInfo) string {
	t.Helper()
	return computeHash(content) + fileHashEntrySep +
		strconv.FormatInt(info.Size(), 10) + fileHashEntrySep +
		strconv.FormatInt(info.ModTime().UnixNano(), 10) + fileHashEntrySep +
		ChunkerFingerprint(DefaultMaxChunkSize, DefaultChunkOverlap, DefaultContentFilterConfig().Fingerprint())
}

// newFormatMetadata returns document metadata carrying the size/mtime fields
// processFile records, so AddDocuments→upsertFileHashes composes a new-format
// sidecar entry.
func newFormatMetadata(filePath string, content []byte, info os.FileInfo) map[string]string {
	return map[string]string{
		"file_path":            filePath,
		"file_name":            filepath.Base(filePath),
		"content_hash":         computeHash(content),
		"file_size":            strconv.FormatInt(info.Size(), 10),
		"file_mtime_unix_nano": strconv.FormatInt(info.ModTime().UnixNano(), 10),
	}
}

// swapReadFileFn replaces the package readFileFn seam with a counting wrapper
// and restores it on cleanup. Tests using it must not run in parallel (none in
// this package do).
func swapReadFileFn(t *testing.T) *atomic.Int32 {
	t.Helper()
	var reads atomic.Int32
	orig := readFileFn
	readFileFn = func(name string) ([]byte, error) {
		reads.Add(1)
		return orig(name)
	}
	t.Cleanup(func() { readFileFn = orig })
	return &reads
}

// TestParseFileHashEntry covers the sidecar entry parser: new-format values
// parse into their components, legacy bare hashes and malformed values are
// rejected (ok=false) so callers take the full read+hash fallback.
func TestParseFileHashEntry(t *testing.T) {
	cases := []struct {
		name      string
		entry     string
		wantHash  string
		wantSize  int64
		wantMtime int64
		wantOK    bool
	}{
		{name: "new format", entry: "abc123|42|1700000000000000000", wantHash: "abc123", wantSize: 42, wantMtime: 1700000000000000000, wantOK: true},
		{name: "legacy bare hash", entry: "abc123", wantHash: "abc123", wantOK: false},
		{name: "two components", entry: "abc123|42", wantHash: "abc123|42", wantOK: false},
		{name: "four components with chunker fp", entry: "abc123|42|1700000000000000000|d42c1bb0ded8", wantHash: "abc123", wantSize: 42, wantMtime: 1700000000000000000, wantOK: true},
		{name: "five components", entry: "abc123|42|1700000000000000000|d42c1bb0ded8|extra", wantHash: "", wantOK: false},
		{name: "non-numeric size", entry: "abc123|x|1700000000000000000", wantHash: "abc123", wantOK: false},
		{name: "non-numeric mtime", entry: "abc123|42|yesterday", wantHash: "abc123", wantOK: false},
		{name: "empty entry", entry: "", wantHash: "", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hash, size, mtime, ok := parseFileHashEntry(tc.entry)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if hash != tc.wantHash || size != tc.wantSize || mtime != tc.wantMtime {
				t.Fatalf("parsed (%q,%d,%d), want (%q,%d,%d)", hash, size, mtime, tc.wantHash, tc.wantSize, tc.wantMtime)
			}
		})
	}

	// fileHashEntryHash returns the hash component for all formats.
	if got := fileHashEntryHash("abc123|42|1700000000000000000"); got != "abc123" {
		t.Errorf("fileHashEntryHash(new format) = %q, want abc123", got)
	}
	if got := fileHashEntryHash("abc123|42|1700000000000000000|d42c1bb0ded8"); got != "abc123" {
		t.Errorf("fileHashEntryHash(current format) = %q, want abc123", got)
	}
	if got := fileHashEntryHash("abc123"); got != "abc123" {
		t.Errorf("fileHashEntryHash(legacy) = %q, want abc123", got)
	}

	// fileHashEntryChunkerFP returns the fingerprint only for current-format
	// entries; every older shape (including the intermediate 3-field format)
	// reports "" = "configuration unknown, exempt from fp staleness".
	if got := fileHashEntryChunkerFP("abc123|42|1700000000000000000|d42c1bb0ded8"); got != "d42c1bb0ded8" {
		t.Errorf("fileHashEntryChunkerFP(current format) = %q, want d42c1bb0ded8", got)
	}
	for _, entry := range []string{"abc123", "abc123|42", "abc123|42|1700000000000000000", ""} {
		if got := fileHashEntryChunkerFP(entry); got != "" {
			t.Errorf("fileHashEntryChunkerFP(%q) = %q, want empty", entry, got)
		}
	}
}

// legacyParseFileHashEntry is a FROZEN COPY of parseFileHashEntry exactly as
// it was before the 5th (chunk-set) field existed. It emulates the parser
// inside an older binary reading a sidecar written by a newer one; see
// TestFileHashEntry_OldParserTreatsFiveFieldAsLegacy.
func legacyParseFileHashEntry(entry string) (hash string, size, mtimeUnixNano int64, ok bool) {
	parts := strings.Split(entry, fileHashEntrySep)
	if len(parts) != 3 && len(parts) != 4 {
		return entry, 0, 0, false // legacy bare hash
	}
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return parts[0], 0, 0, false
	}
	mtime, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return parts[0], 0, 0, false
	}
	return parts[0], size, mtime, true
}

// TestFileHashEntry_OldParserTreatsFiveFieldAsLegacy pins the downgrade
// story for mixed-version sidecars: a 5-field entry (new format, with the
// committed chunk-index set) is rejected by the OLD grammar — which accepts
// only 3 or 4 fields — so an older binary treats it as a legacy bare hash.
// That is slower but correct: ValidateCollection falls back to the full
// read+hash comparison, the whole-entry string never equals the computed
// content hash, the file is re-indexed once, and the entry is rewritten in
// the old binary's format (self-healing, no data loss).
func TestFileHashEntry_OldParserTreatsFiveFieldAsLegacy(t *testing.T) {
	fiveField := "abc123|42|1700000000000000000|d42c1bb0ded8|L:0,2"
	contiguous := "abc123|42|1700000000000000000|d42c1bb0ded8|3"
	for _, entry := range []string{fiveField, contiguous} {
		_, _, _, ok := legacyParseFileHashEntry(entry)
		if ok {
			t.Errorf("old parser accepted 5-field entry %q; want legacy rejection", entry)
		}
	}
	// The new parser accepts both shapes the old one rejects.
	for _, entry := range []string{fiveField, contiguous} {
		if _, _, _, ok := parseFileHashEntry(entry); !ok {
			t.Errorf("new parser rejected 5-field entry %q", entry)
		}
	}
	// 4-field entries remain readable by BOTH parsers (upgrade-safe).
	entry := "abc123|42|1700000000000000000|d42c1bb0ded8"
	if _, _, _, ok := legacyParseFileHashEntry(entry); !ok {
		t.Error("old parser rejected 4-field entry")
	}
	if _, _, _, ok := parseFileHashEntry(entry); !ok {
		t.Error("new parser rejected 4-field entry")
	}

	// Tie the frozen copy to production so it cannot drift into a tautology:
	// on every 3-/4-field input the two parsers must AGREE, and on every
	// 5-field input they must DISAGREE (legacy rejects, current accepts). A
	// change to either grammar that broke the downgrade story fails here.
	for _, in := range []string{
		"abc123",
		"abc123|42|1700000000000000000",
		"abc123|42|1700000000000000000|d42c1bb0ded8",
	} {
		lh, ls, lm, lok := legacyParseFileHashEntry(in)
		nh, ns, nm, nok := parseFileHashEntry(in)
		if lok != nok || lh != nh || ls != ns || lm != nm {
			t.Errorf("parsers disagree on %q: legacy=(%q,%d,%d,%v) current=(%q,%d,%d,%v)",
				in, lh, ls, lm, lok, nh, ns, nm, nok)
		}
	}
	for _, in := range []string{fiveField, contiguous} {
		if _, _, _, lok := legacyParseFileHashEntry(in); lok {
			t.Errorf("legacy parser accepted 5-field %q; want rejection", in)
		}
		if _, _, _, nok := parseFileHashEntry(in); !nok {
			t.Errorf("current parser rejected 5-field %q; want acceptance", in)
		}
	}
}

// TestChunkSetCodec covers the 5th sidecar field's codec: contiguous sets
// encode as the bare chunk count, gapped sets (poison-dropped chunks) as
// "L:<indices>", the empty set as "0", and parsing inverts every encoding.
// Malformed values are rejected so the entry downgrades to legacy.
func TestChunkSetCodec(t *testing.T) {
	encodeCases := []struct {
		name    string
		indices []int
		want    string
		wantSet []int // canonical set the encoding must parse back to
	}{
		{name: "empty set", indices: nil, want: "0", wantSet: []int{}},
		{name: "single chunk", indices: []int{0}, want: "1", wantSet: []int{0}},
		{name: "contiguous", indices: []int{0, 1, 2, 3}, want: "4", wantSet: []int{0, 1, 2, 3}},
		{name: "gapped (poison drop)", indices: []int{0, 2, 3}, want: "L:0,2,3", wantSet: []int{0, 2, 3}},
		{name: "single gapped survivor", indices: []int{5}, want: "L:5", wantSet: []int{5}},
		{name: "tail dropped", indices: []int{0, 1}, want: "2", wantSet: []int{0, 1}},
		{name: "unsorted input normalizes", indices: []int{3, 0, 2, 0}, want: "L:0,2,3", wantSet: []int{0, 2, 3}},
	}
	for _, tc := range encodeCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := encodeChunkIndices(tc.indices); got != tc.want {
				t.Fatalf("encodeChunkIndices(%v) = %q, want %q", tc.indices, got, tc.want)
			}
			// Round-trip: parsing the encoding yields an equivalent set.
			parsed, ok := parseChunkIndices(tc.want)
			if !ok {
				t.Fatalf("parseChunkIndices(%q) rejected its own encoding", tc.want)
			}
			if !slices.Equal(parsed, tc.wantSet) {
				t.Fatalf("round-trip of %q = %v, want %v", tc.want, parsed, tc.wantSet)
			}
		})
	}

	parseCases := []struct {
		field string
		want  []int
		ok    bool
	}{
		{field: "3", want: []int{0, 1, 2}, ok: true},
		{field: "0", want: []int{}, ok: true},
		{field: "L:0,1,3", want: []int{0, 1, 3}, ok: true},
		{field: "L:7", want: []int{7}, ok: true},
		{field: "L:0,1,2,3", want: []int{0, 1, 2, 3}, ok: true},
		{field: "", ok: false},
		{field: "x", ok: false},
		{field: "-1", ok: false},
		{field: "1.5", ok: false},
		{field: "L:", ok: false},
		{field: "L:0,,2", ok: false},
		{field: "L:-1", ok: false},
		{field: "L:0,x", ok: false},
		{field: "9999999999", ok: false}, // over maxChunkSetEntries: no allocation bomb
	}
	for _, tc := range parseCases {
		t.Run("parse "+tc.field, func(t *testing.T) {
			got, ok := parseChunkIndices(tc.field)
			if ok != tc.ok {
				t.Fatalf("parseChunkIndices(%q) ok = %v, want %v", tc.field, ok, tc.ok)
			}
			if !ok {
				return
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("parseChunkIndices(%q) = %v, want %v", tc.field, got, tc.want)
			}
		})
	}
}

// TestFileHashEntryChunkSet covers the entry-level accessor for the 5th
// field: 5-field entries expose their set, every older shape reports
// "unknown", and a malformed 5th field downgrades the whole entry to legacy
// in parseFileHashEntry (strict grammar).
func TestFileHashEntryChunkSet(t *testing.T) {
	if got, ok := fileHashEntryChunkSet("abc123|42|1700000000000000000|d42c1bb0ded8|L:0,2"); !ok || !slices.Equal(got, []int{0, 2}) {
		t.Errorf("fileHashEntryChunkSet(gapped) = %v (ok=%v), want [0 2]", got, ok)
	}
	if got, ok := fileHashEntryChunkSet("abc123|42|1700000000000000000|d42c1bb0ded8|3"); !ok || !slices.Equal(got, []int{0, 1, 2}) {
		t.Errorf("fileHashEntryChunkSet(contiguous) = %v (ok=%v), want [0 1 2]", got, ok)
	}
	if got, ok := fileHashEntryChunkSet("abc123|42|1700000000000000000|d42c1bb0ded8|0"); !ok || len(got) != 0 {
		t.Errorf("fileHashEntryChunkSet(empty) = %v (ok=%v), want empty set", got, ok)
	}
	for _, entry := range []string{
		"abc123",
		"abc123|42",
		"abc123|42|1700000000000000000",
		"abc123|42|1700000000000000000|d42c1bb0ded8",
		"",
	} {
		if got, ok := fileHashEntryChunkSet(entry); ok {
			t.Errorf("fileHashEntryChunkSet(%q) = %v, want unknown", entry, got)
		}
	}

	// Strict grammar: a malformed 5th field ("extra" is neither N nor L:…)
	// downgrades the entry to legacy in the structural parser, exactly like
	// an old binary would.
	bad := "abc123|42|1700000000000000000|d42c1bb0ded8|extra"
	if _, _, _, ok := parseFileHashEntry(bad); ok {
		t.Error("parseFileHashEntry accepted a malformed 5th field; want legacy downgrade")
	}
	if got, ok := fileHashEntryChunkSet(bad); ok {
		t.Errorf("fileHashEntryChunkSet(malformed) = %v, want unknown", got)
	}

	// The chunker fingerprint of a 5-field entry is still readable.
	if got := fileHashEntryChunkerFP("abc123|42|1700000000000000000|d42c1bb0ded8|3"); got != "d42c1bb0ded8" {
		t.Errorf("fileHashEntryChunkerFP(5-field) = %q, want d42c1bb0ded8", got)
	}
}

// TestUpsertFileHashesFiltered_DerivesChunkSetFromIDs pins how the committed
// chunk-index set is derived on the write side: from the documents' ID
// suffixes, with dropped (poisoned) chunks excluded, and with the entry left
// set-less when any ID falls outside the DocumentID grammar. A file whose
// every chunk was dropped still records an entry (empty set) so it is not
// re-reported as new forever.
func TestUpsertFileHashesFiltered_DerivesChunkSetFromIDs(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	md := func(path, hash string) map[string]string {
		return map[string]string{
			"file_path":            path,
			"content_hash":         hash,
			"file_size":            "10",
			"file_mtime_unix_nano": "1700000000000000000",
		}
	}
	doc := func(path, hash, id string) chromem.Document {
		return chromem.Document{ID: id, Metadata: md(path, hash)}
	}

	gappedPath, droppedPath, legacyPath := "/ws/gapped.go", "/ws/dropped.go", "/ws/legacy.go"
	docs := []chromem.Document{
		doc(gappedPath, "hG", DocumentID(gappedPath, 0)),
		doc(gappedPath, "hG", DocumentID(gappedPath, 2)),
		doc(gappedPath, "hG", DocumentID(gappedPath, 3)),
		doc(droppedPath, "hD", DocumentID(droppedPath, 0)),
		doc(droppedPath, "hD", DocumentID(droppedPath, 1)), // poisoned → dropped
		doc(legacyPath, "hL", "doc-0001"),                  // non-DocumentID grammar
	}
	dropped := map[string]struct{}{DocumentID(droppedPath, 1): {}}

	svc.AcquireWriteLock()
	svc.upsertFileHashesFiltered(docs, dropped)
	svc.ReleaseWriteLock()

	svc.mu.RLock()
	entries := map[string]string{}
	for k, v := range svc.current.fileHashes {
		entries[k] = v
	}
	svc.mu.RUnlock()

	setOf := func(path string) []int {
		t.Helper()
		entry, exists := entries[path]
		if !exists {
			t.Fatalf("no sidecar entry for %s", path)
		}
		if _, _, _, ok := parseFileHashEntry(entry); !ok {
			t.Fatalf("entry for %s does not parse: %q", path, entry)
		}
		set, ok := fileHashEntryChunkSet(entry)
		if !ok {
			t.Fatalf("entry for %s carries no chunk set: %q", path, entry)
		}
		return set
	}

	if got := setOf(gappedPath); !slices.Equal(got, []int{0, 2, 3}) {
		t.Errorf("gapped file set = %v, want [0 2 3]", got)
	}
	if got := setOf(droppedPath); !slices.Equal(got, []int{0}) {
		t.Errorf("poison-dropped file set = %v, want [0]", got)
	}
	// Non-DocumentID IDs leave the set unknown: the entry parses (4-field)
	// but carries no 5th field, so collectDocumentIDs falls back to Query.
	entry := entries[legacyPath]
	if _, _, _, ok := parseFileHashEntry(entry); !ok {
		t.Fatalf("legacy-grammar entry does not parse: %q", entry)
	}
	if _, ok := fileHashEntryChunkSet(entry); ok {
		t.Errorf("non-DocumentID IDs must leave the set unknown: %q", entry)
	}

	// A file whose every chunk was dropped still records an entry with the
	// empty set "0" — otherwise it would be re-reported as new on every pass.
	allDropped := "/ws/all-dropped.go"
	allDroppedDocs := []chromem.Document{
		doc(allDropped, "hA", DocumentID(allDropped, 0)),
		doc(allDropped, "hA", DocumentID(allDropped, 1)),
	}
	allDroppedSet := map[string]struct{}{
		DocumentID(allDropped, 0): {},
		DocumentID(allDropped, 1): {},
	}
	svc.AcquireWriteLock()
	svc.upsertFileHashesFiltered(allDroppedDocs, allDroppedSet)
	svc.ReleaseWriteLock()
	svc.mu.RLock()
	entry = svc.current.fileHashes[allDropped]
	svc.mu.RUnlock()
	if entry == "" {
		t.Fatal("fully poison-dropped file lost its sidecar entry")
	}
	if got, ok := fileHashEntryChunkSet(entry); !ok || len(got) != 0 {
		t.Errorf("fully poison-dropped file set = %v (ok=%v), want empty known set", got, ok)
	}
}

// TestValidateCollection_FastPathSkipsReads verifies the core optimization:
// when the sidecar entry is new-format and the file's size AND mtime match the
// recorded values, ValidateCollection classifies the file as unchanged WITHOUT
// reading its content (no ReadFile via the readFileFn seam — and no hash).
func TestValidateCollection_FastPathSkipsReads(t *testing.T) {
	ws := t.TempDir()
	proj := t.TempDir() // vector storage; kept separate so the walk never sees it
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", proj); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	content := []byte("package main\n")
	file := filepath.Join(ws, "a.go")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}

	// Index the file: AddDocuments composes a new-format sidecar entry from
	// the size/mtime metadata (as processFile records it).
	docs := []chromem.Document{{
		ID:       "a:0",
		Content:  string(content),
		Metadata: newFormatMetadata(file, content, info),
	}}
	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments: %v", err)
	}
	svc.ReleaseWriteLock()

	reads := swapReadFileFn(t)
	stale, newFiles, deleted, valErr := svc.ValidateCollection(context.Background(), ws, testIgnoreChecker(t, ws))
	if valErr != nil {
		t.Fatalf("ValidateCollection: %v", valErr)
	}
	if got := reads.Load(); got != 0 {
		t.Errorf("fast path must not read file content; got %d reads", got)
	}
	if len(stale) != 0 || len(newFiles) != 0 || len(deleted) != 0 {
		t.Errorf("unchanged file misclassified: stale=%v new=%v deleted=%v", stale, newFiles, deleted)
	}
}

// TestValidateCollection_LegacySidecarFullReadAndUpgrade verifies that a
// legacy bare-hash sidecar (written before the format upgrade, loaded from
// disk) still validates correctly via the full read+hash comparison, and that
// the entry is upgraded to the new format by the next upsert.
func TestValidateCollection_LegacySidecarFullReadAndUpgrade(t *testing.T) {
	ws := t.TempDir()
	proj := t.TempDir()
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", proj); err != nil {
		t.Fatalf("SetProject: %v", err)
	}

	content := []byte("package utils\n")
	file := filepath.Join(ws, "b.go")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}
	hash := computeHash(content)

	// Seed a LEGACY sidecar on disk (bare hash values) before the first
	// SwitchBranch so loadFileHashes reads it from the fast path.
	legacy := map[string]string{file: hash}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(proj, "file_hashes_"+collectionName("main")+".json")
	if err := os.WriteFile(sidecar, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	// Legacy entries cannot use the stat fast path: the file MUST be read.
	reads := swapReadFileFn(t)
	stale, newFiles, deleted, valErr := svc.ValidateCollection(context.Background(), ws, testIgnoreChecker(t, ws))
	if valErr != nil {
		t.Fatalf("ValidateCollection: %v", valErr)
	}
	if got := reads.Load(); got != 1 {
		t.Errorf("legacy entry must take exactly one full read; got %d", got)
	}
	if containsPath(stale, file) || containsPath(newFiles, file) || containsPath(deleted, file) {
		t.Errorf("legacy entry with matching hash misclassified: stale=%v new=%v deleted=%v", stale, newFiles, deleted)
	}

	// Re-index the file (AddDocuments with size/mtime metadata): the entry is
	// upgraded to the new format in the in-memory sidecar.
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	docs := []chromem.Document{{
		ID:       "b:0",
		Content:  string(content),
		Metadata: newFormatMetadata(file, content, info),
	}}
	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments: %v", err)
	}
	svc.ReleaseWriteLock()

	svc.mu.RLock()
	entry := svc.current.fileHashes[file]
	svc.mu.RUnlock()
	gotHash, gotSize, gotMtime, ok := parseFileHashEntry(entry)
	if !ok {
		t.Fatalf("entry not upgraded to new format: %q", entry)
	}
	if gotHash != hash || gotSize != info.Size() || gotMtime != info.ModTime().UnixNano() {
		t.Errorf("upgraded entry = (%q,%d,%d), want (%q,%d,%d)",
			gotHash, gotSize, gotMtime, hash, info.Size(), info.ModTime().UnixNano())
	}
}

// TestValidateCollection_StatMismatchTakesFullRead covers the fast-path bail
// conditions: a size-only mismatch and an mtime-only touch (content identical)
// must fall back to the full read, and a real content change must still be
// classified stale. This pins the stale/new/deleted classification while the
// stat shortcut is in place.
func TestValidateCollection_StatMismatchTakesFullRead(t *testing.T) {
	ws := t.TempDir()
	proj := t.TempDir()
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", proj); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	checker := testIgnoreChecker(t, ws)

	content := []byte("package main\nfunc A() {}\n")
	file := filepath.Join(ws, "c.go")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	mtime := strconv.FormatInt(info.ModTime().UnixNano(), 10)
	hash := computeHash(content)

	// setSidecarEntry installs a single crafted entry in the in-memory sidecar.
	setSidecarEntry := func(entry string) {
		svc.AcquireWriteLock()
		svc.current.fileHashes = map[string]string{file: entry}
		svc.ReleaseWriteLock()
	}

	// Size-only mismatch (mtime correct, content unchanged): full read, hash
	// comparison decides — NOT stale despite the size mismatch.
	setSidecarEntry(hash + fileHashEntrySep + strconv.FormatInt(info.Size()-1, 10) + fileHashEntrySep + mtime)
	reads := swapReadFileFn(t)
	stale, newFiles, deleted, valErr := svc.ValidateCollection(context.Background(), ws, checker)
	if valErr != nil {
		t.Fatalf("ValidateCollection (size mismatch): %v", valErr)
	}
	if got := reads.Load(); got != 1 {
		t.Errorf("size mismatch must take exactly one full read; got %d", got)
	}
	if containsPath(stale, file) || containsPath(newFiles, file) || containsPath(deleted, file) {
		t.Errorf("size-only mismatch with identical content misclassified: stale=%v new=%v deleted=%v", stale, newFiles, deleted)
	}

	// Mtime-only touch (size and content unchanged): full read, NOT stale.
	touched := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(file, touched, touched); err != nil {
		t.Fatal(err)
	}
	setSidecarEntry(newFormatEntry(t, content, info))
	reads.Store(0)
	stale, newFiles, deleted, valErr = svc.ValidateCollection(context.Background(), ws, checker)
	if valErr != nil {
		t.Fatalf("ValidateCollection (mtime touch): %v", valErr)
	}
	if got := reads.Load(); got != 1 {
		t.Errorf("mtime touch must take exactly one full read; got %d", got)
	}
	if containsPath(stale, file) || containsPath(newFiles, file) || containsPath(deleted, file) {
		t.Errorf("mtime-only touch with identical content misclassified: stale=%v new=%v deleted=%v", stale, newFiles, deleted)
	}

	// Real content change (size AND mtime move): full read, classified stale.
	changed := []byte("package main\nfunc AB() {}\n")
	if err := os.WriteFile(file, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	reads.Store(0)
	stale, _, _, valErr = svc.ValidateCollection(context.Background(), ws, checker)
	if valErr != nil {
		t.Fatalf("ValidateCollection (content change): %v", valErr)
	}
	if got := reads.Load(); got != 1 {
		t.Errorf("changed file must take exactly one full read; got %d", got)
	}
	if !containsPath(stale, file) {
		t.Errorf("changed file must be classified stale; got stale=%v", stale)
	}
}

// TestValidateCollection_OversizedAndBinaryStillSkippedWithFastPath pins the
// reindex-loop protections with the stat fast path active: oversized and
// binary files are never added to the collection, so they must NOT be
// reported as "new" on every pass (that would re-trigger incremental
// indexing forever). The size guard and binary header check run BEFORE the
// fast path touches the sidecar, and none of the three files may reach the
// readFileFn seam.
func TestValidateCollection_OversizedAndBinaryStillSkippedWithFastPath(t *testing.T) {
	ws := t.TempDir()
	proj := t.TempDir()
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
		MaxFileSize:   16,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", proj); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	// Regular indexable file (drives the fast path).
	content := []byte("package main\n")
	file := filepath.Join(ws, "ok.go")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	docs := []chromem.Document{{
		ID:       "ok:0",
		Content:  string(content),
		Metadata: newFormatMetadata(file, content, info),
	}}
	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments: %v", err)
	}
	svc.ReleaseWriteLock()

	// Oversized text file (exceeds the 16-byte limit).
	if err := os.WriteFile(filepath.Join(ws, "big.go"), []byte("package big\n// padding padding padding\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Small binary file (NUL byte within the header, under the limit).
	if err := os.WriteFile(filepath.Join(ws, "blob.bin"), []byte("a\x00b"), 0o644); err != nil {
		t.Fatal(err)
	}

	reads := swapReadFileFn(t)
	stale, newFiles, deleted, valErr := svc.ValidateCollection(context.Background(), ws, testIgnoreChecker(t, ws))
	if valErr != nil {
		t.Fatalf("ValidateCollection: %v", valErr)
	}
	for _, nf := range newFiles {
		switch filepath.Base(nf) {
		case "big.go", "blob.bin":
			t.Errorf("skippable file must not be reported new (reindex loop): %s", nf)
		}
	}
	if len(stale) != 0 || len(newFiles) != 0 || len(deleted) != 0 {
		t.Errorf("unexpected classification: stale=%v new=%v deleted=%v", stale, newFiles, deleted)
	}
	if got := reads.Load(); got != 0 {
		t.Errorf("no file should reach the content read (fast path + guards); got %d reads", got)
	}
}

// TestSwitchBranch_RoundTripsNewSidecarFormat verifies that the new-format
// entries survive the branch-switch save/load cycle both in memory and as the
// on-disk JSON sidecar, and that a reloaded entry still drives the stat fast
// path (zero content reads).
func TestSwitchBranch_RoundTripsNewSidecarFormat(t *testing.T) {
	ws := t.TempDir()
	proj := t.TempDir()
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", proj); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	content := []byte("package roundtrip\n")
	file := filepath.Join(ws, "d.go")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	docs := []chromem.Document{{
		ID:       "d:0",
		Content:  string(content),
		Metadata: newFormatMetadata(file, content, info),
	}}
	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments: %v", err)
	}
	svc.ReleaseWriteLock()

	// Switch away (persists main's sidecar) and back (loads it).
	if err := svc.SwitchBranch(context.Background(), "feature"); err != nil {
		t.Fatalf("SwitchBranch(feature): %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch(main): %v", err)
	}

	// The entry now carries the 5th field: the committed chunk-index set
	// derived from the document ID ("d:0" → one chunk, index 0 → "1").
	wantEntry := newFormatEntry(t, content, info) + fileHashEntrySep + "1"

	// In-memory round-trip: the entry parses with identical components.
	svc.mu.RLock()
	gotEntry := svc.current.fileHashes[file]
	svc.mu.RUnlock()
	if gotEntry != wantEntry {
		t.Fatalf("entry after switch round-trip = %q, want %q", gotEntry, wantEntry)
	}
	gotHash, gotSize, gotMtime, ok := parseFileHashEntry(gotEntry)
	if !ok || gotHash != computeHash(content) || gotSize != info.Size() || gotMtime != info.ModTime().UnixNano() {
		t.Fatalf("round-tripped entry does not parse to the original components: %q", gotEntry)
	}
	if gotSet, setOK := fileHashEntryChunkSet(gotEntry); !setOK || len(gotSet) != 1 || gotSet[0] != 0 {
		t.Fatalf("round-tripped entry chunk set = %v (ok=%v), want [0]", gotSet, setOK)
	}

	// On-disk round-trip: the persisted sidecar JSON stores the new format.
	sidecarData, err := os.ReadFile(filepath.Join(proj, "file_hashes_"+collectionName("main")+".json"))
	if err != nil {
		t.Fatalf("reading sidecar: %v", err)
	}
	var onDisk map[string]string
	if err := json.Unmarshal(sidecarData, &onDisk); err != nil {
		t.Fatalf("unmarshaling sidecar: %v", err)
	}
	if onDisk[file] != wantEntry {
		t.Errorf("on-disk sidecar entry = %q, want %q", onDisk[file], wantEntry)
	}

	// The reloaded entry must still drive the stat fast path: no reads.
	reads := swapReadFileFn(t)
	stale, newFiles, deleted, valErr := svc.ValidateCollection(context.Background(), ws, testIgnoreChecker(t, ws))
	if valErr != nil {
		t.Fatalf("ValidateCollection: %v", valErr)
	}
	if got := reads.Load(); got != 0 {
		t.Errorf("reloaded new-format entry must skip content reads; got %d", got)
	}
	if len(stale) != 0 || len(newFiles) != 0 || len(deleted) != 0 {
		t.Errorf("unchanged file misclassified after round-trip: stale=%v new=%v deleted=%v", stale, newFiles, deleted)
	}
}

// TestValidateCollection_ChunkerFingerprintChangeStalesFiles pins the
// chunker-config fingerprint (vector_index.chunk_overlap / max_chunk_size
// changes): a file whose content is unchanged but whose sidecar entry was
// recorded under a different fingerprint is reported stale so the next pass
// re-chunks it — no manual Reindex needed. Fingerprint-less
// (intermediate-format) entries are exempt: their chunker configuration is
// unknown, and staling every legacy file in one pass would cost a full
// re-embed for no user action.
func TestValidateCollection_ChunkerFingerprintChangeStalesFiles(t *testing.T) {
	ws := t.TempDir()
	proj := t.TempDir()
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:      fakeEmbeddingFunc(),
		ChunkerFingerprint: "cfgAAAAAAAAAAAA",
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", proj); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	content := []byte("package fp\n")
	file := filepath.Join(ws, "f.go")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	docs := []chromem.Document{{
		ID:       "f:0",
		Content:  string(content),
		Metadata: newFormatMetadata(file, content, info),
	}}
	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments: %v", err)
	}
	svc.ReleaseWriteLock()

	// The entry records the fingerprint of the configuration it was
	// chunked under (the Service's active fingerprint).
	svc.mu.RLock()
	entry := svc.current.fileHashes[file]
	svc.mu.RUnlock()
	if got := fileHashEntryChunkerFP(entry); got != "cfgAAAAAAAAAAAA" {
		t.Fatalf("upserted entry carries fingerprint %q, want cfgAAAAAAAAAAAA (entry %q)", got, entry)
	}

	// Same configuration: the file is unchanged (fast path holds, no stale).
	stale, newFiles, deleted, valErr := svc.ValidateCollection(context.Background(), ws, testIgnoreChecker(t, ws))
	if valErr != nil {
		t.Fatalf("ValidateCollection: %v", valErr)
	}
	if len(stale) != 0 || len(newFiles) != 0 || len(deleted) != 0 {
		t.Fatalf("same-configuration pass must report no changes: stale=%v new=%v deleted=%v", stale, newFiles, deleted)
	}

	// Configuration change: identical content, but the recorded fingerprint
	// no longer matches — the file must be reported stale for re-chunking.
	svc.mu.Lock()
	svc.chunkerFingerprint = "cfgBBBBBBBBBBBB"
	svc.mu.Unlock()
	stale, newFiles, deleted, valErr = svc.ValidateCollection(context.Background(), ws, testIgnoreChecker(t, ws))
	if valErr != nil {
		t.Fatalf("ValidateCollection after config change: %v", valErr)
	}
	if len(stale) != 1 || stale[0] != file {
		t.Fatalf("config change must stale the file %q; got stale=%v new=%v deleted=%v", file, stale, newFiles, deleted)
	}

	// Fingerprint-less entry (intermediate 3-field format): hash matches,
	// configuration unknown → exempt, not stale.
	svc.mu.Lock()
	hash, size, mtime, parseOK := parseFileHashEntry(svc.current.fileHashes[file])
	if !parseOK {
		svc.mu.Unlock()
		t.Fatalf("entry does not parse: %q", svc.current.fileHashes[file])
	}
	svc.current.fileHashes[file] = hash + fileHashEntrySep +
		strconv.FormatInt(size, 10) + fileHashEntrySep +
		strconv.FormatInt(mtime, 10)
	svc.mu.Unlock()
	stale, newFiles, deleted, valErr = svc.ValidateCollection(context.Background(), ws, testIgnoreChecker(t, ws))
	if valErr != nil {
		t.Fatalf("ValidateCollection with fingerprint-less entry: %v", valErr)
	}
	if len(stale) != 0 || len(newFiles) != 0 || len(deleted) != 0 {
		t.Fatalf("fingerprint-less entries must be exempt from fp staleness: stale=%v new=%v deleted=%v", stale, newFiles, deleted)
	}
}

// TestValidateCollection_PeriodicFullHashRevalidation pins the backstop for
// the stat fast-path's size+mtime heuristic: every fullHashRevalidationEvery-th
// validation pass skips the fast-path and re-reads + re-hashes every file.
// Observable via the readFileFn seam: passes 1..N-1 read nothing, pass N
// reads the file, still classifies matching content as unchanged, and the
// counter resets (the next pass is a fast-path pass again).
func TestValidateCollection_PeriodicFullHashRevalidation(t *testing.T) {
	ws := t.TempDir()
	proj := t.TempDir()
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", proj); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	content := []byte("package revalidate\n")
	file := filepath.Join(ws, "r.go")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	docs := []chromem.Document{{
		ID:       "r:0",
		Content:  string(content),
		Metadata: newFormatMetadata(file, content, info),
	}}
	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments: %v", err)
	}
	svc.ReleaseWriteLock()

	reads := swapReadFileFn(t)
	for i := 1; i < fullHashRevalidationEvery; i++ {
		stale, newFiles, deleted, valErr := svc.ValidateCollection(context.Background(), ws, testIgnoreChecker(t, ws))
		if valErr != nil {
			t.Fatalf("pass %d: %v", i, valErr)
		}
		if len(stale)+len(newFiles)+len(deleted) != 0 {
			t.Fatalf("pass %d: unchanged file misclassified: stale=%v new=%v deleted=%v", i, stale, newFiles, deleted)
		}
		if got := reads.Load(); got != 0 {
			t.Fatalf("pass %d: fast path must skip content reads; got %d", i, got)
		}
	}

	// Pass N: the forced full-hash revalidation reads and re-hashes the
	// file; matching content stays non-stale.
	stale, newFiles, deleted, valErr := svc.ValidateCollection(context.Background(), ws, testIgnoreChecker(t, ws))
	if valErr != nil {
		t.Fatalf("forced pass: %v", valErr)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("forced pass must re-read the file exactly once; got %d reads", got)
	}
	if len(stale)+len(newFiles)+len(deleted) != 0 {
		t.Fatalf("forced pass must keep matching content unchanged: stale=%v new=%v deleted=%v", stale, newFiles, deleted)
	}

	// Counter reset: the next pass is a fast-path pass again.
	reads.Store(0)
	if _, _, _, valErr := svc.ValidateCollection(context.Background(), ws, testIgnoreChecker(t, ws)); valErr != nil {
		t.Fatalf("post-reset pass: %v", valErr)
	}
	if got := reads.Load(); got != 0 {
		t.Fatalf("post-reset pass must skip content reads; got %d", got)
	}
}

// TestBrowseWithFilter_NoEmbeddingCall pins the embedding-free Browse path:
// when the embedding dimension is known, enumerating the collection must go
// through QueryEmbedding with a fixed unit vector — the embedding function
// (one ONNX inference per Browse, previously paid for the " " query text)
// must not run at all, and the requested topK documents are still returned.
// The dimension-0 sub-test pins the guard: an unknown dimension keeps the
// embedding-bearing space-query path.
func TestBrowseWithFilter_NoEmbeddingCall(t *testing.T) {
	newBrowsedService := func(t *testing.T) (*Service, *atomic.Int32) {
		t.Helper()
		var embedCalls atomic.Int32
		embed := func(_ context.Context, _ string) ([]float32, error) {
			embedCalls.Add(1)
			return []float32{0.1, 0.2, 0.3, 0.4}, nil
		}
		svc, err := NewService(ServiceConfig{EmbeddingFunc: embed})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close() })
		if err := svc.SetProject("proj", t.TempDir()); err != nil {
			t.Fatalf("SetProject: %v", err)
		}
		if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
			t.Fatalf("SwitchBranch: %v", err)
		}
		ws := t.TempDir()
		files := []string{"a.go", "b.go", "c.go"}
		docs := make([]chromem.Document, 0, 6)
		for _, name := range files {
			path := filepath.Join(ws, name)
			for chunk := range 2 {
				docs = append(docs, chromem.Document{
					ID:      DocumentID(path, chunk),
					Content: fmt.Sprintf("chunk %d of %s", chunk, name),
					Metadata: map[string]string{
						"file_path": path,
						"file_name": name,
						"language":  "go",
					},
				})
			}
		}
		svc.AcquireWriteLock()
		if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
			svc.ReleaseWriteLock()
			t.Fatalf("AddDocuments: %v", err)
		}
		svc.ReleaseWriteLock()
		svc.SetReady(true)
		return svc, &embedCalls
	}

	t.Run("known dimension: no embedding call, returns topK", func(t *testing.T) {
		svc, embedCalls := newBrowsedService(t)
		svc.embeddingDimension = 4
		embedCalls.Store(0)

		results, err := svc.BrowseWithFilter(context.Background(), 4, "")
		if err != nil {
			t.Fatalf("BrowseWithFilter: %v", err)
		}
		if got := embedCalls.Load(); got != 0 {
			t.Errorf("Browse invoked the embedding function %d times; want 0 (unit-vector enumeration)", got)
		}
		if len(results) != 4 {
			t.Fatalf("Browse returned %d results; want topK=4", len(results))
		}
		for _, r := range results {
			if r.FilePath == "" || r.Content == "" {
				t.Errorf("hollow browse result: %+v", r)
			}
		}

		// The non-blocking form must behave identically on a ready index.
		embedCalls.Store(0)
		results, err = svc.BrowseWithFilterNoWait(context.Background(), 6, "")
		if err != nil {
			t.Fatalf("BrowseWithFilterNoWait: %v", err)
		}
		if got := embedCalls.Load(); got != 0 {
			t.Errorf("BrowseWithFilterNoWait invoked the embedding function %d times; want 0", got)
		}
		if len(results) != 6 {
			t.Fatalf("BrowseWithFilterNoWait returned %d results; want 6", len(results))
		}
	})

	t.Run("known dimension: file filter still narrows without embedding", func(t *testing.T) {
		svc, embedCalls := newBrowsedService(t)
		svc.embeddingDimension = 4
		embedCalls.Store(0)

		results, err := svc.BrowseWithFilter(context.Background(), 10, "**/b.go")
		if err != nil {
			t.Fatalf("BrowseWithFilter: %v", err)
		}
		if got := embedCalls.Load(); got != 0 {
			t.Errorf("filtered Browse invoked the embedding function %d times; want 0", got)
		}
		if len(results) != 2 {
			t.Fatalf("filtered Browse returned %d results; want the 2 chunks of b.go", len(results))
		}
		for _, r := range results {
			if r.FileName != "b.go" {
				t.Errorf("filtered Browse leaked %s", r.FileName)
			}
		}
	})

	t.Run("unknown dimension: keeps the embedding-bearing query", func(t *testing.T) {
		svc, embedCalls := newBrowsedService(t)
		// embeddingDimension stays 0 (unset ServiceConfig) → legacy path.
		embedCalls.Store(0)

		results, err := svc.BrowseWithFilter(context.Background(), 3, "")
		if err != nil {
			t.Fatalf("BrowseWithFilter: %v", err)
		}
		if got := embedCalls.Load(); got != 1 {
			t.Errorf("legacy Browse invoked the embedding function %d times; want exactly 1 (the \" \" query)", got)
		}
		if len(results) != 3 {
			t.Fatalf("legacy Browse returned %d results; want 3", len(results))
		}
	})
}

// TestFileHashMigration_UsesUnitVectorQuery pins the other embedding-free
// enumeration path: the background sidecar migration
// (migrateFileHashes → queryCollectionFileHashes) must enumerate the
// collection via QueryEmbedding with the unit vector when the embedding
// dimension is known — no inference for the query itself.
func TestFileHashMigration_UsesUnitVectorQuery(t *testing.T) {
	dir := t.TempDir()

	var embedCalls atomic.Int32
	embed := func(_ context.Context, _ string) ([]float32, error) {
		embedCalls.Add(1)
		return []float32{0.1, 0.2, 0.3, 0.4}, nil
	}

	// Build a persisted non-empty collection (the AddDocuments calls embed
	// per document on the legacy path — irrelevant here).
	a, err := NewService(ServiceConfig{EmbeddingFunc: embed, EmbeddingDimension: 4})
	if err != nil {
		t.Fatalf("NewService A: %v", err)
	}
	if err := a.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject A: %v", err)
	}
	if err := a.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch A: %v", err)
	}
	docs := []chromem.Document{
		{ID: "m1", Content: "alpha", Metadata: map[string]string{"file_path": filepath.Join(dir, "a.go"), "content_hash": "hA"}},
		{ID: "m2", Content: "beta", Metadata: map[string]string{"file_path": filepath.Join(dir, "b.go"), "content_hash": "hB"}},
	}
	// Seed via raw chromem (full metadata persists) — the migration under
	// test targets pre-upgrade collections; the service commit path now
	// strips content_hash from stored documents (see strippedForCommit).
	if err := a.current.collection.AddDocuments(context.Background(), docs, 1); err != nil {
		t.Fatalf("chromem AddDocuments A: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close A: %v", err)
	}

	// Upgrade scenario: collection present, sidecar absent.
	if err := os.Remove(filepath.Join(dir, "file_hashes_"+collectionName("main")+".json")); err != nil {
		t.Fatalf("remove sidecar: %v", err)
	}

	b, err := NewService(ServiceConfig{EmbeddingFunc: embed, EmbeddingDimension: 4})
	if err != nil {
		t.Fatalf("NewService B: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if err := b.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject B: %v", err)
	}
	embedCalls.Store(0)
	if err := b.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch B: %v", err)
	}
	if err := b.WaitFileHashMigration(context.Background()); err != nil {
		t.Fatalf("WaitFileHashMigration: %v", err)
	}

	// The migration enumerated the collection with QueryEmbedding: no
	// inference at all.
	if got := embedCalls.Load(); got != 0 {
		t.Errorf("migration invoked the embedding function %d times; want 0 (unit-vector enumeration)", got)
	}
	b.mu.RLock()
	hashes, herr := b.getCollectionFileHashes()
	b.mu.RUnlock()
	if herr != nil {
		t.Fatalf("getCollectionFileHashes: %v", herr)
	}
	if hashes[filepath.Join(dir, "a.go")] != "hA" || hashes[filepath.Join(dir, "b.go")] != "hB" {
		t.Errorf("unexpected migrated hashes: %v", hashes)
	}
}
