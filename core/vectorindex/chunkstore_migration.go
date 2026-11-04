package vectorindex

// The v1 → v2 chunk-record migration completion (ADR-104 §3): every
// published v1 snapshot corpus — a chromem collection whose documents are
// keyed DocumentID(path, i) — becomes the §3 durable form (chunk records
// keyed by C + the snapshot's occurrence table), WITHOUT a single ONNX
// inference: each legacy document's vector MOVES from its document ID to
// the chunk record keyed by the re-chunked text's C.
//
// Provenance per file (ADR-104 §3, one-time migration): the corpus
// manifest's per-file blob hash gates the source read (the migration runs
// from a root whose working tree holds the bytes); the RAW bytes are
// hashed and compared — a missing or hash-mismatched source defers the
// WHOLE snapshot (all-or-nothing: a §3 table that covers fewer files than
// the v1 corpus would silently regress search, so the snapshot stays on
// the v1 read path until its sources match; the ADR's per-file drop is
// this deferral's permanent form — the snapshot is re-indexable on
// demand). The sanitized bytes are re-chunked with the service's chunker
// (the table's chunker fingerprint must equal the live one — a mismatch
// defers to the rebuild path, whose re-derivation the fingerprints own);
// each chunk's C is the content identity, and the legacy document at
// DocumentID(path, i) supplies the vector and its occurrence metadata.
// A chunk index with no legacy document is a poison-dropped chunk —
// absent from both sides, the row legitimately omits it.
//
// Replay-safety (the ADR's marker-gated pattern): the snapshot's
// table.json — written LAST by writeSnapshotTable, after every chunk
// record landed — IS the per-branch completion marker. A crash or
// cancellation before it leaves the snapshot unconverted; the next sweep
// replays it whole (chunk writes are idempotent by content addressing).
// The project-level marker (store_chunk_records.done, store_gc.go) is
// written only after EVERY published corpus of the store has its §3
// tables; its presence opens the v1 chromem leg removal — the rollback
// window's close (the lexical leg stays: §3 opens fall back to it until
// a rebuild lands the lexical index inside the snapshot directory).
//
// Lock discipline: the pass rides the store's sweepMu (single-flighted
// with the layout sweep and the hygiene pass), holds no service or
// registry lock, and takes each converted snapshot's build lease — the
// serialization point against corpus builds — only for the table publish
// window.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/v0lka/sp4rk/safeio"
)

// chunkRecordMigrationReport is one run's outcome (advisory telemetry).
type chunkRecordMigrationReport struct {
	Converted int // snapshots whose §3 tables landed in this run
	Deferred  int // snapshots left on the v1 path with a recorded reason
	Done      int // already-converted snapshots observed (marker hygiene)
	Marked    bool
}

// migrateCorpusChunkRecords converts one published v1 corpus into its §3
// form. Returns converted=true when the snapshot's table landed; ok=false
// with a reason defers the snapshot (v1 keeps serving). An error is a
// transient I/O condition — the whole sweep replays the snapshot next run.
func migrateCorpusChunkRecords(
	ctx context.Context,
	logger *slog.Logger,
	genDir string,
	storageRoot, snapKey string,
	facts StoreMigrationFacts,
	dims int,
) (converted bool, reason string, err error) {
	branchRoot := branchRootPath(storageRoot, snapKey)
	manifest := readCorpusManifest(branchRoot)
	if manifest == nil {
		return false, "corpus manifest absent", nil
	}
	if manifest.ChunkerFP != "" && manifest.ChunkerFP != facts.ChunkerFP {
		return false, "chunker fingerprint mismatch (rebuild path owns re-derivation)", nil
	}
	if facts.ReadSource == nil || facts.ChunkFn == nil {
		return false, "source reader not wired", nil
	}
	db, err := newPersistentDB(branchRoot, false)
	if err != nil {
		return false, "", fmt.Errorf("opening v1 corpus %s: %w", snapKey, err)
	}
	name := manifest.Collection
	if name == "" {
		name = collectionName(snapKey)
	}
	col := db.GetCollection(name, nil)
	if col == nil {
		return false, "", fmt.Errorf("v1 corpus collection %q missing for %s", name, snapKey)
	}
	total := col.Count()
	if total == 0 {
		return false, "v1 corpus is empty (nothing to move)", nil
	}

	paths := make([]string, 0, len(manifest.Files))
	for path := range manifest.Files {
		paths = append(paths, path)
	}
	slices.Sort(paths) // deterministic tables — the replay-safety proof

	rows := make([]occurrenceRow, 0, len(paths))
	occTotal := 0
	unique := map[string]struct{}{}
	for _, path := range paths {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		blobHash := manifest.Files[path]
		src, srcErr := facts.ReadSource(ctx, path)
		if srcErr != nil {
			return false, fmt.Sprintf("source of %s unavailable: %v", path, srcErr), nil
		}
		if facts.HashFn != nil && facts.HashFn(src) != blobHash {
			return false, fmt.Sprintf("source of %s hash-mismatched (deferred; re-indexable on demand)", path), nil
		}
		sanitized, _ := sanitizeChunkContent(src)
		chunks, chunkErr := facts.ChunkFn(path, sanitized, facts.MaxChunkSize, facts.Overlap)
		if chunkErr != nil {
			return false, "", fmt.Errorf("re-chunking %s for %s: %w", path, snapKey, chunkErr)
		}
		row := occurrenceRow{Path: path, BlobHash: blobHash}
		for i, chunk := range chunks {
			doc, docErr := col.GetByID(ctx, DocumentID(path, i))
			if docErr != nil {
				// Poison-dropped at commit time: absent from both sides.
				continue
			}
			if len(doc.Embedding) == 0 {
				return false, fmt.Sprintf("document %s carries no vector", doc.ID), nil
			}
			c := chunkRecordKey(chunk.Content)
			if !chunkRecordExists(genDir, c) {
				if wErr := writeChunkRecord(genDir, c, doc.Embedding); wErr != nil {
					return false, "", fmt.Errorf("landing chunk record %s: %w", c, wErr)
				}
			}
			row.Chunks = append(row.Chunks, chunkOccurrence{
				C:        c,
				Start:    chunk.StartLine,
				End:      chunk.EndLine,
				Language: chunk.Language,
			})
			unique[c] = struct{}{}
		}
		if len(row.Chunks) == 0 {
			continue
		}
		rows = append(rows, row)
		occTotal += len(row.Chunks)
	}
	if occTotal != total {
		return false, fmt.Sprintf("coverage %d occurrences over %d documents incomplete (deferred)", occTotal, total), nil
	}
	chunkBytes := int64(len(unique)) * int64(4*dims)
	if err := writeSnapshotTable(genDir, snapKey, rows, &snapshotTable{
		ChunkerFP:    manifest.ChunkerFP,
		IgnoreFP:     manifest.IgnoreFP,
		Files:        len(rows),
		Chunks:       occTotal,
		UniqueChunks: len(unique),
		ChunkBytes:   chunkBytes,
		Collection:   manifest.Collection,
	}); err != nil {
		return false, "", fmt.Errorf("publishing §3 table for %s: %w", snapKey, err)
	}
	logger.Debug("vector store: chunk-record migration converted a corpus (zero embeddings)",
		"snapshot", snapKey, "files", len(rows), "occurrences", occTotal)
	return true, "", nil
}

// runChunkRecordMigration drives one physical store's v1→v2 chunk-record
// completion: every published, era-compatible v1 corpus without a §3
// table converts; when none remains, the project marker lands (atomic,
// last) and the v1 chromem legs of converted snapshots are reaped by the
// hygiene pass (store_gc.go — the rollback window closes with the
// marker). The caller holds the store's sweepMu.
func (r *CorpusRegistry) runChunkRecordMigration(ctx context.Context, cs *corpusStore, storageRoot, embeddingFP string, dims int, facts StoreMigrationFacts) chunkRecordMigrationReport {
	logger := r.logger
	var report chunkRecordMigrationReport
	genDir, genManifest, ok := readCurrentGeneration(storageRoot)
	if !ok || !genManifest.compatible(embeddingFP, dims) {
		return report // no §3 tree (or a foreign one): nothing to complete
	}
	if storeChunkRecordsDone(storageRoot) {
		report.Marked = true
		return report
	}
	if facts.ReadSource == nil || facts.ChunkFn == nil {
		return report // not wired (in-memory tests): defer silently
	}
	keys := publishedV1CorpusKeys(storageRoot, embeddingFP, dims)
	remaining := 0
	for _, key := range keys {
		if ctx.Err() != nil {
			break
		}
		if snapshotTablePublished(genDir, key) {
			report.Done++
			continue
		}
		started, waitCh := cs.beginBuild(key)
		if !started {
			// A live corpus build owns the key: defer the unit to the next
			// sweep (the layout sweep's short-wait posture would only
			// duplicate the wait here — the build publishes its own §3
			// tables on completion).
			cs.waitBuildFor(waitCh, key, sweepLeaseWait)
			remaining++
			continue
		}
		converted, reason, err := migrateCorpusChunkRecords(ctx, logger, genDir, storageRoot, key, facts, dims)
		cs.endBuild(key, err)
		if err != nil {
			logger.Debug("vector store: chunk-record migration deferred a corpus (transient)",
				"snapshot", key, "error", err)
			remaining++
			continue
		}
		if converted {
			report.Converted++
			continue
		}
		logger.Debug("vector store: chunk-record migration deferred a corpus",
			"snapshot", key, "reason", reason)
		remaining++
	}
	if remaining == 0 && ctx.Err() == nil && !storeChunkRecordsDone(storageRoot) {
		// Every published corpus of the store is either converted or has
		// nothing to move: the project marker lands (the rollback window's
		// close). Only the store's own process may run this (ADR-075).
		if err := safeio.WriteFileAtomic(storeChunkRecordsDonePath(storageRoot), nil, 0o644); err != nil {
			logger.Debug("vector store: chunk-record migration marker deferred",
				"error", err)
			return report
		}
		report.Marked = true
		logger.Info("vector store: chunk-record migration complete (v1 legs now reclaimable)",
			"storage", storageRoot, "converted_total", report.Done+report.Converted)
	}
	return report
}

// publishedV1CorpusKeys lists the store's published, era-compatible
// snapshot corpora (v1 or §3 — the completion and the marker account for
// both) in store-key space, sorted for determinism.
func publishedV1CorpusKeys(storageRoot, embeddingFP string, dims int) []string {
	entries, err := os.ReadDir(filepath.Join(storageRoot, branchRootsDirName))
	if err != nil {
		return nil
	}
	var keys []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		identity, ok := identityFromCollectionName(e.Name())
		if !ok || !isCorpusIdentity(identity) {
			continue
		}
		m := readCorpusManifest(branchRootPath(storageRoot, identity))
		if m == nil || !m.compatible(embeddingFP, dims) {
			continue
		}
		keys = append(keys, identity)
	}
	slices.Sort(keys)
	return keys
}
