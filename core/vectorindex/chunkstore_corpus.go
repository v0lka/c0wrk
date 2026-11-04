package vectorindex

// The §3 corpus read path (ADR-104 §3/§4): a published snapshot's tables
// ARE the corpus. Opening a v2 corpus reads the generation manifest, the
// snapshot's occurrence table and its lexical directory — NO chromem
// decode, no per-branch collection, O(one table + its chunk records). The
// handle's loaded chunk vectors are the shared chunk pool of §5: one
// content-addressed set per corpus, charged once, refcounted by the
// handle's readers exactly like the stage-4 residency contract.
//
// Vectors are reconstructed as pseudo chromem documents on demand (the
// occurrence row + the chunk record re-derive every metadata field the
// search paths consume), so the visibility arbiter, the pattern filters
// and the hybrid fusion see the same shapes the v1 corpus path produced —
// the read path's contract is unchanged, only its storage is honest.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
)

// v2ChunkReadWorkers bounds the §3 open's parallel chunk-record reads.
// The records are immutable small files; the bound keeps a huge corpus's
// open from saturating the process's file descriptors while still
// overlapping the filesystem's latency (the sequential read of ~27k
// records — the LARGE-corpus warm-attach shape — is a full second of
// serialized syscalls that parallelism collapses).
const v2ChunkReadWorkers = 8

// v2Corpus is one §3-backed corpus view. Everything is immutable after
// open (the snapshot's tables never change; a new snapshot is a new
// directory), so concurrent readers share it without locks.
type v2Corpus struct {
	gen     string
	snapKey string
	header  *snapshotTable
	lexical lexical.Index

	// rows are the occurrence table's rows, sorted by path (encode order).
	rows []occurrenceRow
	// byPath indexes rows by occurrence path.
	byPath map[string]int
	// vectors is the shared chunk pool: C → vector, loaded once per handle
	// (one read per chunk record; identical chunks across snapshots share
	// the on-disk record, so the pool is per-handle but the STORAGE is
	// per-content — the ledger counts the bytes once).
	vectors map[string][]float32
	// byID maps DocumentID(path, i) → (row index, chunk index) for the
	// lexical leg's enrichment.
	byID map[string]v2DocRef
}

// v2DocRef locates one occurrence's pseudo-document.
type v2DocRef struct {
	row   int
	chunk int
}

// corpusManifest synthesizes the compatibility manifest the existing view
// machinery consumes (visibility arbitration reads Files; the era gates
// read EmbeddingFP/Dims): every field is derived from the generation
// manifest (era) and the snapshot's table (fingerprints, per-file hashes).
func (v *v2Corpus) corpusManifest(gen *storeGenerationManifest) *CorpusManifest {
	files := make(map[string]string, len(v.rows))
	for _, row := range v.rows {
		files[row.Path] = row.BlobHash
	}
	return &CorpusManifest{
		Format:      storeFormatV2,
		Coords:      coordsRelativeV1,
		EmbeddingFP: gen.EmbeddingFP,
		Dims:        gen.Dims,
		ChunkerFP:   v.header.ChunkerFP,
		IgnoreFP:    v.header.IgnoreFP,
		Collection:  v.header.Collection,
		Files:       files,
	}
}

// count is the corpus's document count (one per chunk occurrence).
func (v *v2Corpus) count() int { return len(v.byID) }

// documentAt re-derives one pseudo chromem document: the occurrence's
// identity (ID — derived from the occurrence's STORED chunk index, never
// its slice position: a poison-dropped chunk leaves gaps in a file's
// 0..n-1 sequence, and the lexical leg's IDs are the true positional
// DocumentID(path, i)), the metadata the search paths and the content
// resolver consume, and its vector from the chunk pool.
func (v *v2Corpus) documentAt(ref v2DocRef) (chromem.Document, bool) {
	if ref.row < 0 || ref.row >= len(v.rows) {
		return chromem.Document{}, false
	}
	row := v.rows[ref.row]
	if ref.chunk < 0 || ref.chunk >= len(row.Chunks) {
		return chromem.Document{}, false
	}
	occ := row.Chunks[ref.chunk]
	vec, ok := v.vectors[occ.C]
	if !ok {
		return chromem.Document{}, false
	}
	return chromem.Document{
		ID:        DocumentID(row.Path, occ.Index),
		Embedding: vec,
		Metadata: map[string]string{
			relPathMetaKey: row.Path,
			"file_path":    row.Path,
			"file_name":    filepath.Base(row.Path),
			"content_hash": row.BlobHash,
			"start_line":   strconv.Itoa(occ.Start),
			"end_line":     strconv.Itoa(occ.End),
			"language":     occ.Language,
		},
	}, true
}

// docByID enriches one lexical hit's ID into its pseudo document.
func (v *v2Corpus) docByID(id string) (chromem.Document, bool) {
	ref, ok := v.byID[id]
	if !ok {
		return chromem.Document{}, false
	}
	return v.documentAt(ref)
}

// openV2Corpus loads a published §3 snapshot into a shareable corpus view:
// occurrence table, every referenced chunk record (a missing record fails
// the open — the chunks-before-tables invariant makes that store damage,
// and the caller falls back to the v1 corpus or fails closed), and the
// read-only lexical index from the snapshot directory. Returns
// (nil, nil) when the snapshot has no §3 publication (the v1 path owns
// the open).
func openV2Corpus(genDir, snapKey string, gen *storeGenerationManifest) (*v2Corpus, error) {
	rows, header, ok := readSnapshotTable(genDir, snapKey)
	if !ok {
		return nil, nil // no §3 publication for this snapshot
	}
	v := &v2Corpus{
		gen:     genDir,
		snapKey: snapKey,
		header:  header,
		rows:    rows,
		byPath:  make(map[string]int, len(rows)),
		byID:    make(map[string]v2DocRef),
		vectors: make(map[string][]float32),
	}
	// Collect the DISTINCT chunk identities (content addressing collapses
	// duplicates — identical chunks at many occurrences read once), then
	// land the records with bounded parallelism: thousands of small random
	// reads serialize poorly on any filesystem, and the records are
	// immutable (no reader can observe a partial write), so concurrent
	// reads are safe by construction. The first error fails the open
	// (chunks-before-tables makes a missing record store damage, never a
	// skip); every worker drains its queue regardless — a fail-closed open
	// costs one walk, not a leaked goroutine. byID keys each occurrence
	// by its STORED chunk index (the true DocumentID suffix), never its
	// slice position — a poison-dropped chunk leaves a positional gap the
	// row legitimately omits.
	var distinct []string
	for ri, row := range rows {
		v.byPath[row.Path] = ri
		for ci, occ := range row.Chunks {
			v.byID[DocumentID(row.Path, occ.Index)] = v2DocRef{row: ri, chunk: ci}
			if _, loaded := v.vectors[occ.C]; !loaded {
				v.vectors[occ.C] = nil
				distinct = append(distinct, occ.C)
			}
		}
	}
	var readErr error
	var mu sync.Mutex
	next := atomic.Int64{}
	workers := v2ChunkReadWorkers
	if workers > len(distinct) {
		workers = len(distinct)
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(distinct) {
					return
				}
				c := distinct[i]
				vec, err := readChunkRecord(genDir, c, gen.Dims)
				mu.Lock()
				if err != nil && readErr == nil {
					readErr = err
				}
				if err == nil {
					v.vectors[c] = vec
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if readErr != nil {
		// Classify before destroying: only PROVEN damage — a record absent
		// (fs.ErrNotExist) or torn (short/over-long/non-regular, wrapped
		// errChunkRecordTorn; a truncated read is io.ErrUnexpectedEOF) — is
		// the power-loss window the unpublish exists to heal. A TRANSIENT
		// fault (EACCES, EMFILE/ENFILE, EIO, an AV/indexer sharing
		// violation on Windows) fails the open WITHOUT touching the
		// publication: the table is intact, the next attempt retries
		// against it, and a full rebuild pass (re-walk, re-hash,
		// re-commit) is not triggered by a retryable OS error.
		if !chunkRecordDamaged(readErr) {
			return nil, fmt.Errorf("chunk record of snapshot %s (transient, publication kept): %w", snapKey, readErr)
		}
		unpublishSnapshotTable(genDir, snapKey)
		return nil, fmt.Errorf("chunk record of snapshot %s: %w", snapKey, readErr)
	}
	lex, err := lexical.OpenReadOnly(filepath.Join(snapshotDir(genDir, snapKey), "lexical"))
	if err != nil {
		// The migrated-snapshot fallback: the v1→v2 chunk-record
		// migration publishes the tables but leaves the snapshot's bleve
		// state at the legacy per-key path (open read-only handles must
		// never have their directory moved under them); a corpus REBUILD
		// lands the lexical index inside the snapshot directory natively
		// (workingLexicalDirLocked). Fail-soft on both: a snapshot without
		// any lexical leg errors the open (the caller falls back to v1).
		lex, err = lexical.OpenReadOnly(lexicalIndexPath(storageRootOfGeneration(genDir), snapKey))
		if err != nil {
			return nil, fmt.Errorf("opening §3 corpus lexical index for %s: %w", snapKey, err)
		}
	}
	v.lexical = lex
	return v, nil
}

// chunkRecordDamaged reports whether a chunk-record read error indicates
// PROVEN durable damage — the power-loss window the publication model
// self-heals by unpublishing (a record absent or torn) — rather than a
// transient fault (permissions, fd exhaustion, a disk blip, an AV/indexer
// lock) that must leave the publication intact for the next attempt.
func chunkRecordDamaged(err error) bool {
	return errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, errChunkRecordTorn)
}

// cosineSimilarity is the plain cosine the vector leg ranks by — the same
// geometric meaning chromem's Query produces, computed over the pool's
// loaded vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// v2Scored is one scored occurrence of the §3 vector leg's working top-K.
type v2Scored struct {
	ref v2DocRef
	sim float64
}

// v2VectorCandidates is the §3 corpus's vector leg: the query embeds once,
// every VISIBILITY-ELIGIBLE occurrence's chunk vector scores by cosine,
// and the top-fanout survivors return as chromem results (the same shape
// the v1 leg produced — pre-fusion filtering is the stage-2 contract, now
// exact: the §3 table lets visibility run BEFORE scoring instead of
// trimming after it). The pseudo-document (its metadata map) is built ONLY
// for the fanout survivors: filtering needs the occurrence path and line
// range alone (passesV2Filters — allocation-free), so a full-corpus pass
// scores without materializing a map per occurrence.
func (s *Service) v2VectorCandidates(ctx context.Context, v *v2Corpus, query string, fanout int, filePattern string, mustMatch []string, resolver *contentResolver) ([]chromem.Result, error) {
	if v == nil || fanout <= 0 || v.count() == 0 {
		return nil, nil
	}
	qvec, err := s.embeddingFunc(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding §3 corpus query: %w", err)
	}
	top := make([]v2Scored, 0, fanout)
	for ri, row := range v.rows {
		if !s.corpusRowVisible(row) {
			continue
		}
		for ci, occ := range row.Chunks {
			vec, ok := v.vectors[occ.C]
			if !ok {
				continue // defensive: open verified every referenced chunk
			}
			if !passesV2Filters(row.Path, occ, filePattern, mustMatch, s.current.workspaceRoot, resolver) {
				continue
			}
			sim := cosineSimilarity(qvec, vec)
			if len(top) < fanout {
				top = append(top, v2Scored{ref: v2DocRef{row: ri, chunk: ci}, sim: sim})
				if len(top) == fanout {
					// The slice first reached the working-set size over an
					// arbitrary table-order permutation — a REAL sort is
					// required here (and only here): the admission
					// threshold below reads top[len(top)-1] as the set's
					// minimum, which it is only once the slice is ordered.
					sortV2ScoredDesc(top)
				}
				continue
			}
			if sim <= top[len(top)-1].sim {
				continue
			}
			top[len(top)-1] = v2Scored{ref: v2DocRef{row: ri, chunk: ci}, sim: sim}
			sortV2ScoredDesc(top)
		}
	}
	sortV2ScoredDesc(top) // also orders a slice that never reached fanout
	out := make([]chromem.Result, 0, len(top))
	for _, sc := range top {
		doc, ok := v.documentAt(sc.ref)
		if !ok {
			continue
		}
		out = append(out, chromem.Result{ID: doc.ID, Metadata: doc.Metadata, Similarity: float32(sc.sim)})
	}
	return out, nil
}

// passesV2Filters is the §3 twin of passesFilters (hybrid.go): the same
// predicate over the same inputs, minus the metadata map — the occurrence
// row carries the path and line range the filters consume, so the scoring
// pass never materializes documents.
func passesV2Filters(path string, occ chunkOccurrence, filePattern string, mustMatch []string, root string, resolver *contentResolver) bool {
	if filePattern != "" {
		if !matchFilePathPattern(filePattern, path) {
			return false
		}
	}
	for _, tok := range mustMatch {
		if tok == "" {
			continue
		}
		src := path
		if root != "" && !filepath.IsAbs(src) {
			src = filepath.Join(root, filepath.FromSlash(src))
		}
		content, ok := resolver.chunkContentOK(src, occ.Start, occ.End)
		if !ok {
			return false
		}
		if !strings.Contains(content, tok) {
			return false
		}
	}
	return true
}

// sortV2ScoredDesc orders the working top-K by descending similarity — a
// full sort of a fanout-sized slice (cheap and exact). The fill-time call
// (when the slice first reaches fanout over an arbitrary table-order
// permutation) and the pre-output call (a slice that never filled) both
// NEED the full sort; the replacement-time call re-establishes the order
// after the threshold swap. The admission rule (sim <= top[len-1].sim ⇒
// skip; replace top[len-1]) is correct only while this invariant holds.
func sortV2ScoredDesc(top []v2Scored) {
	sort.Slice(top, func(i, j int) bool { return top[i].sim > top[j].sim })
}

// corpusRowVisible is the §3 form of the per-root visibility arbiter: the
// row's occurrence is visible exactly while the root's own sidecar records
// the row's blob hash for the path (the same predicate corpusVisible
// applies to stored metadata).
func (s *Service) corpusRowVisible(row occurrenceRow) bool {
	entry, ok := s.current.fileHashes[row.Path]
	return ok && fileHashEntryHash(entry) == row.BlobHash
}
