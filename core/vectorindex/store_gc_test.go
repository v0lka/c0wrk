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
		// writeSnapshotTable verifies every referenced chunk record before
		// publishing (the header is the completion marker), so the seed
		// writes the records first.
		if err := writeChunkRecord(genDir, c, chunkVec(c)); err != nil {
			t.Fatalf("writeChunkRecord %s: %v", c, err)
		}
		rows[0].Chunks = append(rows[0].Chunks, chunkOccurrence{C: c, Index: i, Start: i + 1, End: i + 9, Language: "go"})
	}
	if err := writeSnapshotTable(genDir, snapKey, 8, rows, &snapshotTable{ChunkerFP: "k", IgnoreFP: "i", Files: 1, Chunks: len(cs), UniqueChunks: len(cs)}, nil); err != nil {
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

// backdate moves a path's mtime into the past (beyond the sweeps' grace
// windows) so a test observes the post-grace behavior without sleeping.
func backdate(t *testing.T, path string, age time.Duration) {
	t.Helper()
	past := time.Now().Add(-age)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatalf("backdating %s: %v", path, err)
	}
}

func TestRemoveSupersededGenerations_KeepSetHonored(t *testing.T) {
	h := newChunkStoreHarness(t)
	gen0 := h.gen
	h.ensureGen("fp-era-b", 8) // advance: gen0 superseded
	logger := gcTestLogger()

	// The grace window applies to EVERY non-CURRENT generation: a freshly
	// superseded leg (a losing racer may still be writing §3 records there
	// until its next re-adopt) survives the sweep...
	if removed := removeSupersededGenerations(logger, h.storage, nil); removed != 0 {
		t.Fatalf("freshly superseded generation removed inside the grace window: %d", removed)
	}
	if !pathExists(gen0) {
		t.Fatal("freshly superseded generation was removed inside the grace window")
	}
	// ...and once the window has elapsed it goes.
	backdate(t, gen0, advanceDebrisGrace+time.Hour)
	if removed := removeSupersededGenerations(logger, h.storage, nil); removed != 1 || pathExists(gen0) {
		t.Fatalf("grace-elapsed superseded generation not removed (removed=%d, exists=%v)", removed, pathExists(gen0))
	}
	if !pathExists(h.gen) {
		t.Fatal("the CURRENT generation must never be removed")
	}
	// A generation an open handle was decoded from stays.
	h.ensureGen("fp-era-c", 8) // gen1 now superseded too
	backdate(t, generationPath(h.storage, 1), advanceDebrisGrace+time.Hour)
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
	evicted := evictSnapshotsUnderBudget(logger, h.storage, h.gen, keep, budget, ledger, true)
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
	evicted = evictSnapshotsUnderBudget(logger, h.storage, h.gen, keep, 1, ledger, true)
	if evicted != 0 {
		t.Fatalf("evicted %d with only pinned candidates, want 0", evicted)
	}
	if !pathExists(snapshotDir(h.gen, "snap_new000000000000d")) || !pathExists(snapshotDir(h.gen, "snap_old000000000000c")) {
		t.Error("eviction starved onto a pinned snapshot")
	}
}

func TestRemoveMigratedV1Legs_PerSnapshot(t *testing.T) {
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

	// PER-SNAPSHOT reaping, marker-ungated: a converted snapshot's v1
	// chromem leg is derived state the moment its own table.json published
	// (the rollback window closes per snapshot), while the store-wide
	// store_chunk_records.done marker is completion telemetry only — a
	// multi-branch store whose deferred corpora never block reaping the
	// snapshots that DID convert.
	if removed := removeMigratedV1Legs(logger, h.storage, h.gen, nil); removed != 1 {
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

	// A held key is protected (Windows close-ordering): the sweep leaves
	// its leg for the next pass.
	seedSnapshotTable(t, h.gen, withTable, c) // re-publish the table
	if err := os.MkdirAll(branchRootPath(h.storage, withTable), 0o750); err != nil {
		t.Fatalf("mkdir v1 corpus (re-seed): %v", err)
	}
	if removed := removeMigratedV1Legs(logger, h.storage, h.gen, map[string]struct{}{withTable: {}}); removed != 0 {
		t.Fatalf("reaped %d with the snapshot held, want 0", removed)
	}
	if !pathExists(branchRootPath(h.storage, withTable)) {
		t.Error("a held snapshot's v1 leg was reaped")
	}
	if removed := removeMigratedV1Legs(logger, h.storage, h.gen, nil); removed != 1 {
		t.Fatalf("reaped %d after release, want 1", removed)
	}
}

// TestHygiene_LiveBuildLeaseHoldsChunkReap pins the ADR-104 §6 live-lease
// gate: while a corpus build holds a lease in the store, the hygiene pass
// reaps NO chunk records at all — pass 3 is skipped outright and the budget
// pass runs without reclaiming count-zero records — and the ledger reports
// the skip honestly. Once the lease settles, the very next pass applies.
// The lease is taken and settled through the registry's own
// beginBuild/endBuild (the production mechanism), so the test needs no
// sleep: endBuild drops the lease before returning and closes the done
// channel, which the test asserts as its barrier.
func TestHygiene_LiveBuildLeaseHoldsChunkReap(t *testing.T) {
	h := newChunkStoreHarness(t)
	logger := gcTestLogger()
	registry := NewCorpusRegistry(logger)
	cs := registry.storeFor(h.storage)
	if cs == nil {
		t.Fatal("storeFor returned no coordinator")
	}

	// One pinned live corpus (a live root marker reaches it) so the pass
	// has a resolved keep set and passes 1/2 do real work alongside.
	liveKey := "snap_live0000000000001"
	liveChunk := chunkRecordKey("live chunk")
	seedSnapshotTable(t, h.gen, liveKey, liveChunk)
	if err := writeRootRef(h.storage, "root_aaaa", t.TempDir(), liveKey); err != nil {
		t.Fatalf("writeRootRef: %v", err)
	}

	// A count-zero record past the grace window: pass 3's ordinary victim.
	orphan := chunkRecordKey("stale orphan")
	if err := writeChunkRecord(h.gen, orphan, chunkVec(orphan)); err != nil {
		t.Fatalf("writeChunkRecord: %v", err)
	}
	backdate(t, chunkRecordPath(h.gen, orphan), orphanChunkGrace+time.Hour)

	// The lease is held for a DIFFERENT snapshot than the orphan's content
	// owner-less records have no per-key identity: chunk records are
	// content-addressed and shared across snapshots and overlays, so the
	// store-wide lease set is the only sound gate.
	building := "snap_building00000002"
	started, leaseDone := cs.beginBuild(building)
	if !started {
		t.Fatal("could not acquire the build lease")
	}

	// The exposed lease snapshot is a detached read-only copy (mutating it
	// must not touch the store's lease table).
	leases := cs.liveBuildLeases()
	if len(leases) != 1 {
		t.Fatalf("live build leases = %d, want 1", len(leases))
	}
	if _, ok := leases[building]; !ok {
		t.Fatalf("lease snapshot is missing %q", building)
	}
	delete(leases, building)
	if len(cs.liveBuildLeases()) != 1 {
		t.Fatal("liveBuildLeases returned an alias of the store's lease map")
	}

	// A budget far below the store size: without the gate the budget pass
	// would evict and reclaim count-zero chunk records.
	registry.SetDiskBudget(1)

	held := registry.RunHygiene(h.storage, nil)
	if held == nil {
		t.Fatal("hygiene returned no ledger")
	}
	if !held.ChunkReapSkipped {
		t.Error("ledger did not report the chunk-reap skip")
	}
	if held.ChunkOrphans != 0 {
		t.Errorf("ledger reports %d reaped orphans under a live lease, want 0", held.ChunkOrphans)
	}
	if !pathExists(chunkRecordPath(h.gen, orphan)) {
		t.Error("pass 3 reaped a count-zero chunk record while a build lease was live")
	}
	if !pathExists(snapshotDir(h.gen, liveKey)) || !pathExists(chunkRecordPath(h.gen, liveChunk)) {
		t.Error("pass 1/2 disturbed the pinned live corpus")
	}

	// Settle the lease. endBuild drops it under cs.mu before returning and
	// then closes done — the channel is the barrier, so no sleep or polling
	// is needed to know the gate is open again.
	cs.endBuild(building, nil)
	select {
	case <-leaseDone:
	default:
		t.Fatal("endBuild did not close the lease's done channel")
	}

	released := registry.RunHygiene(h.storage, nil)
	if released == nil {
		t.Fatal("post-release hygiene returned no ledger")
	}
	if released.ChunkReapSkipped {
		t.Error("ledger still reports a chunk-reap skip after the lease settled")
	}
	if released.ChunkOrphans != 1 {
		t.Errorf("post-release reaped orphans = %d, want 1", released.ChunkOrphans)
	}
	if pathExists(chunkRecordPath(h.gen, orphan)) {
		t.Error("the grace-elapsed orphan survived the post-release pass")
	}
	if !pathExists(chunkRecordPath(h.gen, liveChunk)) {
		t.Error("the pinned corpus's chunk record was reaped")
	}
}

// TestEvictSnapshotsUnderBudget_ChunkReapSuppressed pins the budget pass's
// half of the live-lease gate: with reapChunks false the owner-unpinned
// snapshot's legs still go, but its exclusive count-zero chunk record is
// NOT deleted and its bytes are NOT credited — the ledger keeps charging a
// record that is still on disk (walk-honest), and the next pass reclaims
// it. reapChunks true is the ordinary lease-free behavior.
func TestEvictSnapshotsUnderBudget_ChunkReapSuppressed(t *testing.T) {
	logger := gcTestLogger()
	const oldKey, newKey = "snap_old000000000000c", "snap_new000000000000d"

	seed := func() (h *chunkStoreHarness, cOld, cNew string) {
		h = newChunkStoreHarness(t)
		cOld, cNew = chunkRecordKey("suppressed old content"), chunkRecordKey("suppressed new content")
		for _, c := range []string{cOld, cNew} {
			if err := writeChunkRecord(h.gen, c, chunkVec(c)); err != nil {
				t.Fatalf("writeChunkRecord: %v", err)
			}
		}
		seedSnapshotTable(t, h.gen, oldKey, cOld)
		seedSnapshotTable(t, h.gen, newKey, cNew)
		return h, cOld, cNew
	}

	t.Run("suppressed", func(t *testing.T) {
		h, cOld, cNew := seed()
		ledger := computeStoreLedger(h.storage)
		chunkBytes := ledger.ChunkBytes
		budget := ledger.TotalBytes - ledger.SnapshotBytes[oldKey]
		keep := map[string]struct{}{newKey: {}}

		if evicted := evictSnapshotsUnderBudget(logger, h.storage, h.gen, keep, budget, ledger, false); evicted != 1 {
			t.Fatalf("evicted %d, want 1 (the snapshot legs still go under a live lease)", evicted)
		}
		if pathExists(snapshotDir(h.gen, oldKey)) {
			t.Error("the evicted snapshot's §3 legs survived")
		}
		if !pathExists(chunkRecordPath(h.gen, cOld)) {
			t.Error("a count-zero chunk record was reaped under a live build lease")
		}
		if ledger.ChunkBytes != chunkBytes {
			t.Errorf("suppressed pass credited %d chunk bytes it did not reclaim", chunkBytes-ledger.ChunkBytes)
		}
		if !pathExists(chunkRecordPath(h.gen, cNew)) {
			t.Error("the pinned snapshot's chunk record was reaped")
		}
		// Walk-honest: the suppressed record is still on disk, so the
		// reported total must equal a fresh walk of the tree.
		walk := computeStoreLedger(h.storage)
		if walk == nil {
			t.Fatal("no ledger walk after the suppressed pass")
		}
		if walk.TotalBytes != ledger.TotalBytes {
			t.Errorf("ledger total = %d, walk = %d (the suppressed bytes must stay charged)",
				ledger.TotalBytes, walk.TotalBytes)
		}
	})

	t.Run("reap", func(t *testing.T) {
		h, cOld, _ := seed()
		ledger := computeStoreLedger(h.storage)
		budget := ledger.TotalBytes - ledger.SnapshotBytes[oldKey]
		keep := map[string]struct{}{newKey: {}}

		if evicted := evictSnapshotsUnderBudget(logger, h.storage, h.gen, keep, budget, ledger, true); evicted != 1 {
			t.Fatalf("evicted %d, want 1", evicted)
		}
		if pathExists(chunkRecordPath(h.gen, cOld)) {
			t.Error("the evicted snapshot's exclusive chunk record survived the ordinary pass")
		}
	})
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
