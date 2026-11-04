package vectorindex

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The §5 disk ledger and the §6 hygiene sweep's tests (store_gc.go):
// accounting truth (unique chunk bytes counted once), the orphan-chunk
// sweep's reference set and grace window, superseded-generation removal,
// snapshot eviction under the disk budget, the reachability keep set over
// the §3 tree, and the migrated-v1-leg reaping gated on the chunk-record
// migration's done marker.

// seedSnapshotTable publishes a minimal snapshot table referencing the
// given chunk identities.
func seedSnapshotTable(t *testing.T, genDir, snapKey string, cs ...string) {
	t.Helper()
	rows := []occurrenceRow{{
		Path:     "pkg/a.go",
		BlobHash: "deadbeef",
		Chunks:   make([]chunkOccurrence, 0, len(cs)),
	}}
	for i, c := range cs {
		rows[0].Chunks = append(rows[0].Chunks, chunkOccurrence{C: c, Start: i + 1, End: i + 9, Language: "go"})
	}
	if err := writeSnapshotTable(genDir, snapKey, rows, &snapshotTable{ChunkerFP: "k", IgnoreFP: "i", Files: 1, Chunks: len(cs), UniqueChunks: len(cs)}); err != nil {
		t.Fatalf("writeSnapshotTable %s: %v", snapKey, err)
	}
}

func chunkVec(c string) []float32 {
	v := make([]float32, 8)
	for i := range v {
		v[i] = float32(len(c)%7+i) / 8.0
	}
	return v
}

func TestStoreLedger_UniqueChunkBytesCountedOnce(t *testing.T) {
	h := newChunkStoreHarness(t)
	// Two chunk records; TWO snapshots share BOTH (the L1 dedup boundary:
	// N views of the same content share one set of records).
	ca, cb := chunkRecordKey("package main"), chunkRecordKey("package sub")
	for _, c := range []string{ca, cb} {
		if err := writeChunkRecord(h.gen, c, chunkVec(c)); err != nil {
			t.Fatalf("writeChunkRecord: %v", err)
		}
	}
	seedSnapshotTable(t, h.gen, "snap_1111111111111111", ca, cb)
	seedSnapshotTable(t, h.gen, "snap_2222222222222222", ca, cb)

	l := computeStoreLedger(h.storage)
	if l == nil {
		t.Fatal("no ledger for a §3 store")
	}
	if l.ChunkRecords != 2 || l.ChunkBytes != int64(2*8*4) {
		t.Errorf("unique chunks = %d records / %d bytes; want 2 / %d (counted ONCE across the two snapshots)",
			l.ChunkRecords, l.ChunkBytes, 2*8*4)
	}
	if len(l.SnapshotBytes) != 2 || l.Snapshots != 2 {
		t.Errorf("snapshot breakdown = %v (%d)", l.SnapshotBytes, l.Snapshots)
	}
	if l.TotalBytes != l.ChunkBytes+l.SnapshotBytes["snap_1111111111111111"]+l.SnapshotBytes["snap_2222222222222222"] {
		t.Errorf("total %d does not match the walked components", l.TotalBytes)
	}
}

func TestStoreLedger_SupersededGenerationsReported(t *testing.T) {
	h := newChunkStoreHarness(t)
	if err := writeChunkRecord(h.gen, chunkRecordKey("old"), chunkVec("old")); err != nil {
		t.Fatalf("writeChunkRecord: %v", err)
	}
	h.ensureGen("fp-era-b", 8) // era advance: generation 0 becomes superseded
	if err := writeChunkRecord(h.gen, chunkRecordKey("new"), chunkVec("new")); err != nil {
		t.Fatalf("writeChunkRecord: %v", err)
	}
	l := computeStoreLedger(h.storage)
	if l.Generation != 1 {
		t.Fatalf("current generation = %d, want 1", l.Generation)
	}
	if l.SupersededBytes <= 0 {
		t.Errorf("superseded bytes = %d, want > 0 (the whole old generation)", l.SupersededBytes)
	}
	if l.TotalBytes != l.ChunkBytes+l.SupersededBytes {
		t.Errorf("total %d must equal the current generation's %d plus the superseded %d",
			l.TotalBytes, l.ChunkBytes, l.SupersededBytes)
	}
}

func TestOrphanChunkSweep_ReferenceSetAndGraceWindow(t *testing.T) {
	h := newChunkStoreHarness(t)
	logger := gcTestLogger()
	kept := chunkRecordKey("referenced chunk")
	freshOrphan := chunkRecordKey("fresh orphan")
	oldOrphan := chunkRecordKey("old orphan")
	for _, c := range []string{kept, freshOrphan, oldOrphan} {
		if err := writeChunkRecord(h.gen, c, chunkVec(c)); err != nil {
			t.Fatalf("writeChunkRecord: %v", err)
		}
	}
	seedSnapshotTable(t, h.gen, "snap_aaaaaaaaaaaaaaaa", kept)
	// Overlay rows reference chunks too (the working store's deferred
	// table is the grace window's raison d'être; a PUBLISHED overlay table
	// is a hard reference).
	overlayChunk := chunkRecordKey("overlay chunk")
	if err := writeChunkRecord(h.gen, overlayChunk, chunkVec(overlayChunk)); err != nil {
		t.Fatalf("writeChunkRecord: %v", err)
	}
	if err := writeOverlayTable(h.gen, "root_abcdef", []occurrenceRow{{
		Path: "dirty.go", BlobHash: "feedface",
		Chunks: []chunkOccurrence{{C: overlayChunk, Start: 1, End: 4, Language: "go"}},
	}}); err != nil {
		t.Fatalf("writeOverlayTable: %v", err)
	}
	// Push the old orphan past the grace window.
	old := time.Now().Add(-2 * orphanChunkGrace)
	if err := os.Chtimes(chunkRecordPath(h.gen, oldOrphan), old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	collected := sweepOrphanChunkRecords(logger, h.gen)
	if collected != 1 {
		t.Fatalf("collected %d orphans, want 1 (only the grace-elapsed one)", collected)
	}
	if pathExists(chunkRecordPath(h.gen, oldOrphan)) {
		t.Error("grace-elapsed orphan survived the sweep")
	}
	for _, c := range []string{kept, freshOrphan, overlayChunk} {
		if !pathExists(chunkRecordPath(h.gen, c)) {
			t.Errorf("chunk %s… must survive (referenced or inside the grace window)", c[:8])
		}
	}
}

func TestRemoveSupersededGenerations_KeepSetHonored(t *testing.T) {
	h := newChunkStoreHarness(t)
	gen0 := h.gen
	h.ensureGen("fp-era-b", 8) // advance: gen0 superseded
	logger := gcTestLogger()
	removed := removeSupersededGenerations(logger, h.storage, nil)
	if removed != 1 || pathExists(gen0) {
		t.Fatalf("superseded generation not removed (removed=%d, exists=%v)", removed, pathExists(gen0))
	}
	if !pathExists(h.gen) {
		t.Fatal("the CURRENT generation must never be removed")
	}
	// A generation an open handle was decoded from stays.
	h.ensureGen("fp-era-c", 8) // gen1 now superseded too
	if removed := removeSupersededGenerations(logger, h.storage, map[string]struct{}{generationPath(h.storage, 1): {}}); removed != 0 {
		t.Fatalf("held generation removed: %d", removed)
	}
	if !pathExists(generationPath(h.storage, 1)) {
		t.Fatal("held generation was removed despite the keep set")
	}
}

func TestHygiene_ReclaimsUnreachableSnapshot_LegsTogether(t *testing.T) {
	h := newChunkStoreHarness(t)
	logger := gcTestLogger()
	live := chunkRecordKey("live chunk")
	dead := chunkRecordKey("dead chunk")
	for _, c := range []string{live, dead} {
		if err := writeChunkRecord(h.gen, c, chunkVec(c)); err != nil {
			t.Fatalf("writeChunkRecord: %v", err)
		}
	}
	seedSnapshotTable(t, h.gen, "snap_live00000000000a", live)
	seedSnapshotTable(t, h.gen, "snap_dead00000000000b", dead)
	// v1 legs for both snapshots (the corpus.json shape the migration and
	// the fallback path consume).
	for _, key := range []string{"snap_live00000000000a", "snap_dead00000000000b"} {
		if err := os.MkdirAll(branchRootPath(h.storage, key), 0o750); err != nil {
			t.Fatalf("mkdir v1 corpus: %v", err)
		}
		if err := os.MkdirAll(lexicalIndexPath(h.storage, key), 0o750); err != nil {
			t.Fatalf("mkdir v1 lexical: %v", err)
		}
	}
	// A live root marker pins snap_live…; the dead snapshot's owner is gone.
	tree := t.TempDir()
	if err := writeRootRef(h.storage, "root_aaaa", tree, "snap_live00000000000a"); err != nil {
		t.Fatalf("writeRootRef: %v", err)
	}

	hygieneStoreFS(logger, h.storage, nil, 0)

	if !pathExists(snapshotDir(h.gen, "snap_live00000000000a")) {
		t.Error("the pinned snapshot's §3 table was removed")
	}
	if !pathExists(branchRootPath(h.storage, "snap_live00000000000a")) {
		t.Error("the pinned snapshot's v1 leg was removed")
	}
	if pathExists(snapshotDir(h.gen, "snap_dead00000000000b")) {
		t.Error("the unreachable snapshot's §3 table survived")
	}
	if pathExists(branchRootPath(h.storage, "snap_dead00000000000b")) {
		t.Error("the unreachable snapshot's v1 corpus leg survived")
	}
	if pathExists(lexicalIndexPath(h.storage, "snap_dead00000000000b")) {
		t.Error("the unreachable snapshot's lexical leg survived")
	}
	if !pathExists(chunkRecordPath(h.gen, live)) {
		t.Error("the live snapshot's chunk record was swept")
	}
	// The dead snapshot's table is gone, so its chunk is an orphan — but
	// inside the grace window (just written): the SAME pass must not
	// delete it (the next sweep collects it). Assert the conservative
	// behavior explicitly: fresh orphans survive one pass.
	if !pathExists(chunkRecordPath(h.gen, dead)) {
		t.Error("a fresh orphan was collected in the same pass (grace window violated)")
	}
}

func TestHygiene_NoRefsDirKeepsEverything(t *testing.T) {
	h := newChunkStoreHarness(t)
	logger := gcTestLogger()
	c := chunkRecordKey("anything")
	if err := writeChunkRecord(h.gen, c, chunkVec(c)); err != nil {
		t.Fatalf("writeChunkRecord: %v", err)
	}
	seedSnapshotTable(t, h.gen, "snap_9999999999999999", c)

	// Even under an impossible budget: no refs/ ⇒ no owner record ⇒ the
	// conservative posture governs the disk budget too — nothing evicts.
	hygieneStoreFS(logger, h.storage, nil, 1)

	if !pathExists(snapshotDir(h.gen, "snap_9999999999999999")) {
		t.Error("a refs-less store had a snapshot reclaimed (the conservative posture was violated)")
	}
}

func TestEvictSnapshotsUnderBudget_LRUPinnedKept(t *testing.T) {
	h := newChunkStoreHarness(t)
	logger := gcTestLogger()
	cOld, cNew := chunkRecordKey("old content"), chunkRecordKey("new content")
	for _, c := range []string{cOld, cNew} {
		if err := writeChunkRecord(h.gen, c, chunkVec(c)); err != nil {
			t.Fatalf("writeChunkRecord: %v", err)
		}
	}
	seedSnapshotTable(t, h.gen, "snap_old000000000000c", cOld)
	seedSnapshotTable(t, h.gen, "snap_new000000000000d", cNew)
	// Age the old snapshot beyond the new one (LRU order).
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(snapshotDir(h.gen, "snap_old000000000000c"), snapshotTableName), old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	ledger := computeStoreLedger(h.storage)
	snapBytes := ledger.SnapshotBytes["snap_old000000000000c"]
	if snapBytes <= 0 {
		t.Fatalf("snapshot bytes = %d, want > 0", snapBytes)
	}
	// A budget that fits everything but the OLD snapshot's bytes: the old
	// one is evicted, the pinned one would not be even if older.
	budget := ledger.TotalBytes - snapBytes
	keep := map[string]struct{}{"snap_new000000000000d": {}}
	evicted := evictSnapshotsUnderBudget(logger, h.storage, h.gen, keep, budget, ledger)
	if evicted != 1 {
		t.Fatalf("evicted %d, want 1", evicted)
	}
	if pathExists(snapshotDir(h.gen, "snap_old000000000000c")) {
		t.Error("the LRU snapshot survived eviction")
	}
	if !pathExists(snapshotDir(h.gen, "snap_new000000000000d")) {
		t.Error("the pinned snapshot was evicted")
	}
	// Both remaining candidates pinned: eviction starves without touching
	// either pin, however tight the budget.
	seedSnapshotTable(t, h.gen, "snap_old000000000000c", cOld) // resurrect
	keep = map[string]struct{}{"snap_new000000000000d": {}, "snap_old000000000000c": {}}
	ledger = computeStoreLedger(h.storage)
	evicted = evictSnapshotsUnderBudget(logger, h.storage, h.gen, keep, 1, ledger)
	if evicted != 0 {
		t.Fatalf("evicted %d with only pinned candidates, want 0", evicted)
	}
	if !pathExists(snapshotDir(h.gen, "snap_new000000000000d")) || !pathExists(snapshotDir(h.gen, "snap_old000000000000c")) {
		t.Error("eviction starved onto a pinned snapshot")
	}
}

func TestRemoveMigratedV1Legs_MarkerGated(t *testing.T) {
	h := newChunkStoreHarness(t)
	logger := gcTestLogger()
	c := chunkRecordKey("migrated")
	if err := writeChunkRecord(h.gen, c, chunkVec(c)); err != nil {
		t.Fatalf("writeChunkRecord: %v", err)
	}
	withTable := "snap_migrated00000e"
	tableless := "snap_tableless000f"
	seedSnapshotTable(t, h.gen, withTable, c)
	for _, key := range []string{withTable, tableless} {
		if err := os.MkdirAll(branchRootPath(h.storage, key), 0o750); err != nil {
			t.Fatalf("mkdir v1 corpus: %v", err)
		}
		if err := os.MkdirAll(lexicalIndexPath(h.storage, key), 0o750); err != nil {
			t.Fatalf("mkdir lexical: %v", err)
		}
	}

	// Without the done marker nothing is reaped (the rollback window).
	if removed := removeMigratedV1Legs(logger, h.storage, h.gen, nil); removed != 0 {
		t.Fatalf("v1 legs reaped before the done marker: %d", removed)
	}
	if !pathExists(branchRootPath(h.storage, withTable)) {
		t.Fatal("v1 leg removed before the marker")
	}

	if err := os.WriteFile(storeChunkRecordsDonePath(h.storage), nil, 0o644); err != nil {
		t.Fatalf("marker: %v", err)
	}
	if removed := removeMigratedV1Legs(logger, h.storage, h.gen, map[string]struct{}{tableless: {}}); removed != 1 {
		t.Fatalf("reaped %d, want 1 (the converted, unheld snapshot)", removed)
	}
	if pathExists(branchRootPath(h.storage, withTable)) {
		t.Error("the migrated snapshot's v1 chromem leg survived")
	}
	if !pathExists(lexicalIndexPath(h.storage, withTable)) {
		t.Error("the migrated snapshot's LEXICAL leg was removed (§3 opens fall back to it)")
	}
	if !pathExists(branchRootPath(h.storage, tableless)) {
		t.Error("a tableless snapshot's v1 leg was reaped (it is the only form)")
	}
}

// migrationTestLogger returns a discard logger when MIG_DEBUG is unset
// (the harness form returns nil there; this local form keeps call sites
// unconditionally typed).
func gcTestLogger() *slog.Logger {
	if l := migrationTestLogger(); l != nil {
		return l
	}
	return slog.New(slog.DiscardHandler)
}
