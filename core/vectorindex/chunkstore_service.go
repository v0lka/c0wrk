package vectorindex

// The §3 writer integration (ADR-104 §3): the service's working stores —
// the per-root overlay and a build lease holder's corpus store — land their
// chunk records and occurrence tables in the content-addressed generation
// tree as they commit. The chromem collection remains the runtime engine of
// the working store (the mutable delta leg rides chromem beneath the
// content-addressed layer, exactly the posture ADR-104 §7 keeps); the §3
// tree is the canonical durable form the corpus read path, the disk ledger
// and the chunk GC consume.
//
// Completeness rule (load-bearing for the GC's safety): an occurrence
// table is written only when its rows cover EVERY document of the working
// collection — sum(len(row.Chunks)) == collection.Count(). Poison-dropped
// chunks never commit, so they are absent from both sides; adopted corpus
// files create sidecar entries but no documents, so they never inflate the
// count. A store whose rows cannot reach the equality keeps its legacy
// legs serving and its §3 table deferred indefinitely: this happens for
// any overlay that still holds documents committed BEFORE its §3 adoption
// (the v1→v2 chunk-record migration completes SNAPSHOTS only, never
// overlays — its source reader is bound to the driving root's tree, while
// an overlay's rows must derive from its OWN tree). The chunk GC does NOT
// keep such a store's records forever: a deferred overlay's committed
// chunk records become sweepable orphans once the grace window elapses —
// they are re-derivable through the content-addressed embedding cache if
// a later re-index of the legacy paths ever completes the rows.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	chromem "github.com/philippgille/chromem-go"
)

// adoptStoreGenerationLocked binds the incoming working store's §3 state
// (ADR-104 §3): ensure the storage root's generation exists (creating or
// advancing it per the embedding era), load a pre-existing overlay table's
// rows so later flushes preserve them, and reset the accumulator for
// corpus builds. Fail-soft: a store that cannot adopt keeps running the
// legacy shape (storeGen stays "" and every §3 hook is a no-op — indexing
// is never blocked). The caller must hold s.mu (write).
func (s *Service) adoptStoreGenerationLocked(branch string) {
	ps := s.current
	ps.storeGen = ""
	ps.chunkRows = nil
	if s.corpusRegistry == nil || ps.projectPath == "" || branch == "" {
		return // v1 mode (registry-less) or an in-memory store: no §3 form
	}
	gen, err := ensureStoreGeneration(ps.projectPath, s.embeddingCacheFingerprint, s.embeddingDimension)
	if err != nil {
		// Refused (foreign format) or transient I/O: never block indexing.
		s.logger.Debug("vector index: §3 store generation not adopted",
			"storage", ps.projectPath, "error", err)
		return
	}
	ps.storeGen = gen
	if isOverlayIdentity(branch) {
		if rows, ok := readOverlayTable(gen, branch); ok {
			ps.chunkRows = make(map[string]occurrenceRow, len(rows))
			for _, row := range rows {
				ps.chunkRows[row.Path] = row
			}
		}
	}
}

// chunkRowsComplete reports whether the accumulated §3 rows cover every
// document of the working collection (the completeness rule above).
// Caller must hold the owning Service's mu.
func (ps *projectState) chunkRowsComplete() bool {
	if ps.storeGen == "" || ps.collection == nil {
		return false
	}
	total := 0
	for _, row := range ps.chunkRows {
		total += len(row.Chunks)
	}
	return total == ps.collection.Count()
}

// pendingOccRow accumulates one path's freshly landed chunk occurrences
// within a single commit batch, keyed by chunk index (a file's chunks may
// span several embedding-commit windows; the row merges across them).
type pendingOccRow struct {
	blob    string
	occs    map[int]chunkOccurrence
	hasZero bool // a chunk-0 occurrence: this batch RESTARTS the path's row
}

// recordChunkRecordsLocked lands the §3 side of one committed batch: every
// document's chunk record (content-addressed, deduplicated by C) is
// written BEFORE any table can reference it (ADR-104 §3 write ordering),
// and the occurrence rows accumulate per path. Documents the batch embedder
// pre-embedded contribute their vector directly; on the legacy
// per-document path the hook SHADOW-EMBEDDS the normalized text through
// the content-addressed cache — the chromem commit is untouched (its
// documents stay content-bearing and chromem-embedded, the v1 shape tests
// pin), and the chunk record holds the canonical cache-identity vector
// the batch path would have produced.
//
// Merge semantics: a batch carrying the path's chunk 0 RESTARTS the row (a
// re-index re-commits the whole file); a batch without chunk 0 (the tail
// of a window-spanning file) MERGES into the existing row at its indices.
// Every occurrence carries its TRUE chunk index explicitly (the
// occurrence grammar's index field): a mid-file poison drop (the batch
// embedder's per-text retry failure) leaves a permanent gap in the file's
// 0..n-1 sequence, and the row preserves that gap — occurrences are never
// re-positioned by slice compaction, so openV2Corpus's
// DocumentID(path, i) mapping stays exact. A failed embed or chunk write
// poisons the path FOR THE WHOLE BATCH (sticky) AND drops the path's
// accumulated row: the pending occurrences are dropped, every later
// document of that path in the same batch is skipped, and the previous
// version's row (adopted from the last flushed table) is discarded too —
// otherwise a later CONTINUATION batch would merge its tail into the
// PREVIOUS version's row under the new blob hash, baking a two-version
// chimera row that the count rule cannot detect; without a base the
// continuation starts a fresh tail-only row that fails the count rule and
// defers the flush. The count rule then keeps the table unwritten
// rather than referencing a missing chunk. Caller must hold s.mu (write).
func (s *Service) recordChunkRecordsLocked(ctx context.Context, docs []chromem.Document) {
	ps := s.current
	if ps.storeGen == "" || len(docs) == 0 {
		return
	}
	if ps.chunkRows == nil {
		ps.chunkRows = make(map[string]occurrenceRow)
	}
	pending := make(map[string]*pendingOccRow, len(docs))
	poisoned := make(map[string]bool)
	for i := range docs {
		d := &docs[i]
		rel := d.Metadata[relPathMetaKey]
		blob := d.Metadata["content_hash"]
		idx, ok := chunkIndexFromID(d.ID)
		if rel == "" || blob == "" || !ok {
			continue
		}
		if poisoned[rel] {
			continue // sticky batch poison: the path's row defers wholesale
		}
		vec := d.Embedding
		if len(vec) == 0 {
			// Legacy per-document path: shadow-embed through the cache.
			text := normalizeEmbeddingChunk(d.Content)
			if cached, hit := func() ([]float32, bool) {
				if c := ps.embeddingCache; c != nil {
					return c.get(text)
				}
				return nil, false
			}(); hit {
				vec = cached
			} else if v, err := s.embeddingFunc(ctx, text); err == nil {
				vec = v
				if c := ps.embeddingCache; c != nil {
					c.put(text, v)
				}
			} else {
				s.logger.Warn("vector index: shadow embedding failed; table flush deferred",
					"path", rel, "error", err)
				poisoned[rel] = true
				delete(pending, rel)
				// The accumulated row goes too (it may hold the PREVIOUS
				// version's occurrences adopted from the last flushed
				// table): a later continuation batch must not merge its
				// tail into it and flush a two-version chimera the count
				// rule accepts — it starts a fresh tail-only row that
				// fails the count rule instead.
				delete(ps.chunkRows, rel)
				continue
			}
		}
		c := chunkRecordKey(d.Content)
		if err := writeChunkRecord(ps.storeGen, c, vec); err != nil {
			s.logger.Warn("vector index: chunk record write failed; table flush deferred",
				"path", rel, "error", err)
			poisoned[rel] = true
			delete(pending, rel)
			// Same cross-batch scope as the shadow-embed poison above: the
			// previous version's accumulated row is dropped so a later
			// continuation batch cannot merge into it.
			delete(ps.chunkRows, rel)
			continue
		}
		row := pending[rel]
		if row == nil {
			row = &pendingOccRow{blob: blob, occs: make(map[int]chunkOccurrence)}
			pending[rel] = row
		}
		start, _ := strconv.Atoi(d.Metadata["start_line"])
		end, _ := strconv.Atoi(d.Metadata["end_line"])
		row.occs[idx] = chunkOccurrence{C: c, Index: idx, Start: start, End: end, Language: d.Metadata["language"]}
		if idx == 0 {
			row.hasZero = true
		}
	}
	for rel, prow := range pending {
		if !prow.hasZero {
			// A continuation batch: merge into whatever the path already
			// holds (an absent base starts fresh — the row below).
			if _, ok := ps.chunkRows[rel]; !ok {
				ps.chunkRows[rel] = occurrenceRow{Path: rel, BlobHash: prow.blob}
			}
		} else {
			// The batch restarts the path's row (a re-index re-commits
			// every chunk from 0).
			ps.chunkRows[rel] = occurrenceRow{Path: rel, BlobHash: prow.blob}
		}
		row := ps.chunkRows[rel]
		row.BlobHash = prow.blob
		for _, occ := range prow.occs {
			row.Chunks = upsertOccurrence(row.Chunks, occ)
		}
		if len(row.Chunks) == 0 {
			delete(ps.chunkRows, rel)
			continue
		}
		ps.chunkRows[rel] = row
	}
}

// upsertOccurrence inserts or replaces one occurrence in an Index-sorted
// slice (kept sorted so the durable encode is deterministic and a
// re-committed chunk index overwrites its earlier entry instead of
// duplicating it — the count rule sums slice lengths and must never count
// one index twice).
func upsertOccurrence(chunks []chunkOccurrence, occ chunkOccurrence) []chunkOccurrence {
	i := sort.Search(len(chunks), func(j int) bool { return chunks[j].Index >= occ.Index })
	if i < len(chunks) && chunks[i].Index == occ.Index {
		chunks[i] = occ
		return chunks
	}
	chunks = append(chunks, chunkOccurrence{})
	copy(chunks[i+1:], chunks[i:])
	chunks[i] = occ
	return chunks
}

// dropChunkRowsLocked removes the §3 rows of the given occurrence paths —
// files whose documents the pipeline removed; the rows follow the
// documents, exactly like the sidecar entries removed beside them. Caller
// must hold s.mu (write).
func (s *Service) dropChunkRowsLocked(paths []string) {
	ps := s.current
	if ps.storeGen == "" || len(ps.chunkRows) == 0 || len(paths) == 0 {
		return
	}
	for _, p := range paths {
		delete(ps.chunkRows, s.fileHashKeyLocked(p))
	}
}

// sidecarSettledExtras rides the sidecar settle boundary (saveFileHashes
// — every lifecycle flush, current AND parked states): retire a superseded
// legacy overlay sidecar and persist the overlay's §3 table when the rows
// are complete. Fail-soft; the chunk GC keeps a tableless store
// conservative, so a deferred flush never costs correctness.
func (s *Service) sidecarSettledExtras(ps *projectState) {
	if ps == nil || ps.storeGen == "" {
		return
	}
	s.retireLegacyOverlaySidecar(ps)
	if !isOverlayIdentity(ps.currentBranch) || !ps.chunkRowsComplete() {
		if ps.storeGen != "" && isOverlayIdentity(ps.currentBranch) {
			// The count is computed defensively: this boundary is reached
			// with collection == nil after a failed incoming open left the
			// residents released while the overlay identity and storeGen
			// survived (SwitchBranch's error path) — the log must report,
			// never panic (this runs on the RPC/shutdown path).
			docs := -1
			if ps.collection != nil {
				docs = ps.collection.Count()
			}
			s.logger.Debug("vector index: overlay table flush deferred (rows incomplete)",
				"store", ps.currentBranch,
				"rows", len(ps.chunkRows), "documents", docs)
		}
		return
	}
	rows := make([]occurrenceRow, 0, len(ps.chunkRows))
	for _, row := range ps.chunkRows {
		rows = append(rows, row)
	}
	if err := writeOverlayTable(ps.storeGen, ps.currentBranch, rows); err != nil {
		s.logger.Warn("vector index: overlay table flush failed",
			"store", ps.currentBranch, "error", err)
	}
}

// retireLegacyOverlaySidecar removes a legacy overlay sidecar after its §3
// successor landed (the adoption's cleanup half — both files must never
// coexist as competing truths). Best-effort.
func (s *Service) retireLegacyOverlaySidecar(ps *projectState) {
	if ps.storeGen == "" || !isOverlayIdentity(ps.currentBranch) || ps.projectPath == "" {
		return
	}
	legacy := fileHashesPathFor(ps.projectPath, ps.currentBranch)
	if legacy == "" {
		return
	}
	if pathExists(overlaySidecarPath(ps.storeGen, ps.currentBranch)) && pathExists(legacy) {
		if err := os.Remove(legacy); err != nil {
			s.logger.Debug("vector index: legacy overlay sidecar removal deferred",
				"path", legacy, "error", err)
		}
	}
}

// publishCorpusTableLocked writes a corpus build's §3 snapshot table from
// the accumulated rows (the chunks are already on disk — the commit hooks
// wrote them before this point). The table lands BEFORE the corpus.json
// publication manifest: a crash between the two leaves a table the §3
// open path serves directly (the table IS a publication), while the v1
// gate's absence makes EnsureCorpus replay the build idempotently.
// Returns an error when the rows are incomplete (the count rule) —
// publishing a partial table would reference chunks that may not exist
// for every document. Caller must hold s.mu (write).
func (s *Service) publishCorpusTableLocked(ctx context.Context, snapKey string) error {
	ps := s.current
	if ps.storeGen == "" {
		return nil // legacy shape: the v1 publication alone is the record
	}
	if !ps.chunkRowsComplete() {
		return fmt.Errorf("publishing corpus %s: §3 rows incomplete (%d rows over %d documents)",
			snapKey, len(ps.chunkRows), ps.collection.Count())
	}
	rows := make([]occurrenceRow, 0, len(ps.chunkRows))
	chunks, unique := 0, map[string]struct{}{}
	chunkBytes := int64(0)
	for _, row := range ps.chunkRows {
		rows = append(rows, row)
		for _, occ := range row.Chunks {
			chunks++
			unique[occ.C] = struct{}{}
			chunkBytes += int64(4 * s.embeddingDimension)
		}
	}
	return writeSnapshotTable(ps.storeGen, snapKey, s.embeddingDimension, rows, &snapshotTable{
		ChunkerFP:    s.chunkerFingerprint,
		IgnoreFP:     s.ignoreFingerprint,
		Files:        len(rows),
		Chunks:       chunks,
		UniqueChunks: len(unique),
		ChunkBytes:   chunkBytes,
	}, s.corpusChunkRepairLocked(ctx))
}

// corpusChunkRepairLocked is the §3 publish repair source: one occurrence's
// vector is re-derived from the build store's own committed document (the
// pass wrote it through the ordinary pipeline), falling back to the embedding
// cache and the service's embedder for a legacy document that carries no
// vector. Caller must hold s.mu (read).
func (s *Service) corpusChunkRepairLocked(ctx context.Context) chunkRecordRepair {
	return func(path string, chunkIndex int) ([]float32, error) {
		col := s.current.collection
		if col == nil {
			return nil, fmt.Errorf("chunk %d of %s: the build store has no collection", chunkIndex, path)
		}
		doc, err := col.GetByID(ctx, DocumentID(path, chunkIndex))
		if err != nil {
			return nil, fmt.Errorf("chunk %d of %s is not in the build store: %w", chunkIndex, path, err)
		}
		if len(doc.Embedding) > 0 {
			return doc.Embedding, nil
		}
		text := normalizeEmbeddingChunk(doc.Content)
		if c := s.current.embeddingCache; c != nil {
			if cached, hit := c.get(text); hit {
				return cached, nil
			}
		}
		v, embErr := s.embeddingFunc(ctx, text)
		if embErr != nil {
			return nil, fmt.Errorf("embedding chunk %d of %s: %w", chunkIndex, path, embErr)
		}
		if c := s.current.embeddingCache; c != nil {
			c.put(text, v)
		}
		return v, nil
	}
}

// workingLexicalDirLocked resolves the working store's bleve directory:
// view-model stores (overlay, corpus build) under an adopted generation
// live in the §3 tree (roots/<fp>/overlay-lexical, snapshots/<oid>/lexical
// — ADR-104 §3's per-snapshot lexical state); everything else — legacy
// branch stores and pre-adoption overlays — keeps the historical
// lexical/<dir> path. The overlay resolver prefers the §3 directory only
// once it EXISTS (a pre-adoption overlay's index stays where bleve built
// it until the chunk-record migration adopts it), so a fresh overlay's
// first open creates the §3 directory and never the legacy one. Corpus
// builds ALWAYS target the snapshot directory: a build publishes the §3
// form directly. Caller must hold s.mu.
func (s *Service) workingLexicalDirLocked(branch string) string {
	if s.current.storeGen != "" && s.current.projectPath != "" {
		gen := s.current.storeGen
		switch {
		case isOverlayIdentity(branch):
			dir := overlayLexicalDir(gen, branch)
			if pathExists(dir) || !pathExists(lexicalIndexPath(s.current.projectPath, branch)) {
				return dir
			}
		case isCorpusIdentity(branch):
			return filepath.Join(snapshotDir(gen, branch), "lexical")
		}
	}
	return lexicalIndexPath(s.current.projectPath, branch)
}
