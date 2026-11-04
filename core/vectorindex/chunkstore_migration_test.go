package vectorindex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	chromem "github.com/philippgille/chromem-go"
)

// The v1→v2 chunk-record migration completion's tests
// (chunkstore_migration.go): a promoted v1 corpus converts into the §3
// form with ZERO embeddings (each legacy document's vector MOVES to the
// chunk record keyed by the re-chunked text's C), the tables are
// replay-gated by their completion marker, source mismatches defer the
// whole snapshot, the project done marker opens the v1-leg reaping, and
// the §3 read path serves the converted corpus (with the lexical
// fallback to the legacy directory).

// extendedFacts wires the chunk-record completion over the harness's
// repository: sources read from the workspace tree, the fake chunker and
// hash the seeding pass used, and the chunker fingerprint the seeded
// corpus manifest carries.
func (h *migrationHarness) extendedFacts(t *testing.T) StoreMigrationFacts {
	t.Helper()
	f := h.facts()
	f.ReadSource = func(ctx context.Context, relPath string) ([]byte, error) {
		return readWorkspaceSource(ctx, h.root, relPath)
	}
	f.ChunkFn = fakeChunkFunc
	f.HashFn = fakeHashFunc
	if _, manifest, ok := readCurrentGeneration(h.storage); ok {
		_ = manifest
	}
	// The chunker fingerprint of the corpus the seeding pass produced: the
	// service's default fingerprint (ChunkerFingerprint over the resolved
	// defaults), identical for every corpus this harness seeds.
	f.ChunkerFP = h.seededChunkerFP(t)
	f.MaxChunkSize = 1600
	f.Overlap = 200
	return f
}

// seededChunkerFP resolves the fingerprint the seeded stores carry.
func (h *migrationHarness) seededChunkerFP(t *testing.T) string {
	t.Helper()
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:             fakeEmbeddingFunc(),
		EmbeddingCacheFingerprint: migrationTestFP,
		EmbeddingDimension:        8,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc.chunkerFingerprint
}

// runSweepExtended runs the unified migration with the chunk-record facts.
func (h *migrationHarness) runSweepExtended(ctx context.Context, t *testing.T) *StoreMigrationState {
	t.Helper()
	return h.reg.RunStoreMigration(ctx, h.storage, migrationTestFP, 8, h.extendedFacts(t))
}

func TestChunkRecordMigration_MovesVectorsWithoutEmbedding(t *testing.T) {
	files := map[string]string{
		"a.go":     "package main\n\n// alpha marker\nfunc Alpha() {}\n",
		"sub/b.go": "package sub\n\n// beta marker\nfunc Beta() {}\n",
	}
	h := newMigrationHarness(t, files)
	h.repo.Git(t, "checkout", "-b", "feature")
	legacySvc, counter := h.seedLegacyStore(t, h.storage, h.root, "feature", migrationTestFP)
	if err := legacySvc.Close(); err != nil {
		t.Fatalf("closing legacy service: %v", err)
	}
	key := snapshotKey(h.oidOf(t, "feature"))

	// The plain layout sweep first (no chunk-record facts): the promotion
	// the extended sweep would also make, isolated so the v1 vectors can
	// be snapshotted from the promoted corpus BEFORE the conversion.
	if st := h.runSweep(context.Background()); st == nil || st.Status != storeMigrationStatusDone {
		t.Fatalf("layout sweep state = %+v, want done", st)
	}
	// The §3 generation the attaching working store adopts in production
	// (adoptStoreGenerationLocked); created directly here — the completion
	// converts corpora INTO an existing era-compatible generation.
	if _, err := ensureStoreGeneration(h.storage, migrationTestFP, 8); err != nil {
		t.Fatalf("ensureStoreGeneration: %v", err)
	}

	// Snapshot the v1 vectors before the conversion (the proof material).
	branchRoot := branchRootPath(h.storage, key)
	db, err := newPersistentDB(branchRoot, false)
	if err != nil {
		t.Fatalf("opening v1 corpus pre-conversion: %v", err)
	}
	manifest := readCorpusManifest(branchRoot)
	if manifest == nil {
		t.Fatal("no published corpus manifest after the layout sweep fixture")
	}
	col := db.GetCollection(manifest.Collection, nil)
	if col == nil {
		t.Fatalf("v1 collection %q missing", manifest.Collection)
	}
	v1Vectors := map[string][]float32{}
	for path := range manifest.Files {
		for i := 0; ; i++ {
			doc, docErr := col.GetByID(context.Background(), DocumentID(path, i))
			if docErr != nil {
				break
			}
			v1Vectors[doc.ID] = doc.Embedding
		}
	}
	if len(v1Vectors) == 0 {
		t.Fatal("fixture produced no v1 documents")
	}

	st := h.runSweepExtended(context.Background(), t)
	if st == nil || st.Status != storeMigrationStatusDone {
		t.Fatalf("sweep state = %+v, want done", st)
	}

	// The §3 table is published and every referenced chunk record's vector
	// EQUALS the v1 document's vector — moved, not re-embedded.
	genDir, genManifest, ok := readCurrentGeneration(h.storage)
	if !ok {
		t.Fatal("no §3 generation after the sweep")
	}
	rows, header, ok := readSnapshotTable(genDir, key)
	if !ok {
		t.Fatalf("no §3 table for %s after the conversion", key)
	}
	if header.Collection != manifest.Collection {
		t.Errorf("table collection %q, want the v1 provenance %q", header.Collection, manifest.Collection)
	}
	moved := 0
	for _, row := range rows {
		for _, occ := range row.Chunks {
			vec, err := readChunkRecord(genDir, occ.C, genManifest.Dims)
			if err != nil {
				t.Fatalf("chunk record %s of %s: %v", occ.C, row.Path, err)
			}
			moved++
			_ = vec // equality is asserted per-ID below via the collection
		}
	}
	if moved == 0 {
		t.Fatal("conversion produced no occurrences")
	}
	// Vector equality through the ID space: every v1 document's vector is
	// byte-identical to some chunk record the table references (the fake
	// embedder is deterministic per text, and C is keyed by that text —
	// the moved record IS the committed vector).
	seen := map[string]struct{}{}
	for _, row := range rows {
		for ci, occ := range row.Chunks {
			id := DocumentID(row.Path, ci)
			v1, has := v1Vectors[id]
			if !has {
				continue // poison-dropped at commit time: absent from both sides
			}
			vec, err := readChunkRecord(genDir, occ.C, genManifest.Dims)
			if err != nil {
				t.Fatalf("chunk record %s: %v", occ.C, err)
			}
			if len(vec) != len(v1) {
				t.Fatalf("vector arity drifted for %s: %d vs %d", id, len(vec), len(v1))
			}
			for i := range vec {
				if vec[i] != v1[i] {
					t.Fatalf("vector of %s drifted at %d: %v vs %v", id, i, vec[i], v1[i])
				}
			}
			seen[id] = struct{}{}
		}
	}
	if len(seen) != col.Count() {
		t.Fatalf("coverage %d of %d v1 documents", len(seen), col.Count())
	}

	// ZERO embeddings on the conversion path: the only embedder ever wired
	// is the seeding pass's counter, and the sweep owns none.
	if counter.count() == 0 {
		t.Fatal("fixture sanity: the seeding pass embedded nothing")
	}

	// The project marker landed and the v1 chromem leg was reaped (the
	// lexical leg stays — the §3 open's fallback).
	if !storeChunkRecordsDone(h.storage) {
		t.Error("chunk-record done marker missing after full conversion")
	}
	if pathExists(branchRoot) {
		t.Error("the migrated snapshot's v1 chromem leg survived the done marker")
	}
	if !pathExists(lexicalIndexPath(h.storage, key)) {
		t.Error("the migrated snapshot's lexical leg was removed (§3 opens fall back to it)")
	}

	// The §3 read path serves the corpus: open through the registry and
	// confirm the v2 form (no chromem decode — the tables are the corpus).
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:             fakeEmbeddingFunc(),
		EmbeddingCacheFingerprint: migrationTestFP,
		EmbeddingDimension:        8,
		Corpus:                    h.reg,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("p", h.storage); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	cs := h.reg.storeFor(h.storage)
	h2, err := cs.open(context.Background(), key, fakeEmbeddingFunc(), migrationTestFP, 8)
	if err != nil {
		t.Fatalf("opening the converted corpus: %v", err)
	}
	defer cs.release(h2)
	if h2.v2 == nil {
		t.Fatal("the converted corpus opened on the v1 path (the §3 table was not served)")
	}
	if h2.v2.count() != col.Count() {
		t.Fatalf("§3 corpus count %d, want the v1 corpus's %d", h2.v2.count(), col.Count())
	}
}

func TestChunkRecordMigration_DefersOnSourceMismatch(t *testing.T) {
	files := map[string]string{"a.go": "package main\n\n// alpha marker\n"}
	h := newMigrationHarness(t, files)
	legacySvc, _ := h.seedLegacyStore(t, h.storage, h.root, "main", migrationTestFP)
	if err := legacySvc.Close(); err != nil {
		t.Fatalf("closing legacy service: %v", err)
	}
	key := snapshotKey(h.oidOf(t, "main"))
	if st := h.runSweep(context.Background()); st == nil || st.Status != storeMigrationStatusDone {
		t.Fatalf("layout sweep state = %+v, want done", st)
	}
	// The §3 generation the attaching working store adopts in production
	// (adoptStoreGenerationLocked); created directly here — the completion
	// converts corpora INTO an existing era-compatible generation.
	if _, err := ensureStoreGeneration(h.storage, migrationTestFP, 8); err != nil {
		t.Fatalf("ensureStoreGeneration: %v", err)
	}

	// Dirty the tree: the corpus's sources no longer re-hash to the
	// manifest's blob hashes — the conversion must defer the snapshot and
	// write NO marker (the v1 corpus keeps serving).
	dirty := filepath.Join(h.root, "a.go")
	if err := os.WriteFile(dirty, []byte("package main\n\n// DIRTY\n"), 0o644); err != nil {
		t.Fatalf("dirtying the tree: %v", err)
	}
	h.runSweepExtended(context.Background(), t)

	genDir, _, ok := readCurrentGeneration(h.storage)
	if !ok {
		t.Fatal("no §3 generation")
	}
	if snapshotTablePublished(genDir, key) {
		t.Fatal("a snapshot with mismatched sources was converted (all-or-nothing violated)")
	}
	if storeChunkRecordsDone(h.storage) {
		t.Fatal("the done marker landed while a snapshot was deferred")
	}
	if !pathExists(branchRootPath(h.storage, key)) {
		t.Fatal("the deferred snapshot's v1 corpus was disturbed")
	}

	// Restore the source: the next sweep converts (replay-safety — the
	// deferred snapshot needed no reset, only another run).
	if err := os.WriteFile(dirty, []byte(files["a.go"]), 0o644); err != nil {
		t.Fatalf("restoring the tree: %v", err)
	}
	h.runSweepExtended(context.Background(), t)
	if !snapshotTablePublished(genDir, key) {
		t.Fatal("the restored snapshot was not converted on replay")
	}
	if !storeChunkRecordsDone(h.storage) {
		t.Fatal("the done marker did not land after the replay conversion")
	}
}

func TestChunkRecordMigration_ReplayGate_TableIsTheMarker(t *testing.T) {
	files := map[string]string{"a.go": "package main\n\n// alpha marker\n"}
	h := newMigrationHarness(t, files)
	legacySvc, _ := h.seedLegacyStore(t, h.storage, h.root, "main", migrationTestFP)
	if err := legacySvc.Close(); err != nil {
		t.Fatalf("closing legacy service: %v", err)
	}
	key := snapshotKey(h.oidOf(t, "main"))
	if st := h.runSweep(context.Background()); st == nil || st.Status != storeMigrationStatusDone {
		t.Fatalf("layout sweep state = %+v, want done", st)
	}
	// The §3 generation the attaching working store adopts in production
	// (adoptStoreGenerationLocked); created directly here — the completion
	// converts corpora INTO an existing era-compatible generation.
	if _, err := ensureStoreGeneration(h.storage, migrationTestFP, 8); err != nil {
		t.Fatalf("ensureStoreGeneration: %v", err)
	}

	h.runSweepExtended(context.Background(), t)
	h.runSweepExtended(context.Background(), t) // replay: nothing to do, no churn

	genDir, _, ok := readCurrentGeneration(h.storage)
	if !ok {
		t.Fatal("no §3 generation")
	}
	rows, header, ok := readSnapshotTable(genDir, key)
	if !ok {
		t.Fatal("the converted table vanished on replay")
	}
	if header.Files != len(rows) {
		t.Fatalf("header files %d != rows %d on replay", header.Files, len(rows))
	}
	if !storeChunkRecordsDone(h.storage) {
		t.Fatal("marker lost on replay")
	}
}

var _ chromem.EmbeddingFunc = fakeEmbeddingFunc()
