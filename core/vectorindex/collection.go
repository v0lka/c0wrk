package vectorindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
	"github.com/v0lka/sp4rk/ignore"

	"github.com/v0lka/sp4rk/safeio"
)

var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// embeddingCommitWindowSize is the chromem commit window; the ≤200-doc bound, the
// accumulator's inference tail, and the sidecar publish-after-commit rule are
// specified in specs/domains/workspace.md (Vector Index, memory program).
const embeddingCommitWindowSize = 200

// collectionName returns a deterministic, sanitized collection name for a branch.
func collectionName(branch string) string {
	sanitized := strings.ReplaceAll(branch, "/", "_")
	sanitized = sanitizeRe.ReplaceAllString(sanitized, "")
	if sanitized == "" {
		sanitized = "default"
	}
	return collectionNamePrefix + sanitized
}

// lexicalBranchDirName returns a sanitized directory name for a branch,
// used as the per-branch subdirectory under the project's lexical folder.
func lexicalBranchDirName(branch string) string {
	sanitized := strings.ReplaceAll(branch, "/", "_")
	sanitized = sanitizeRe.ReplaceAllString(sanitized, "")
	if sanitized == "" {
		sanitized = "default"
	}
	return sanitized
}

// SwitchBranch switches to (or creates) a collection for the given branch.
// The caller must NOT hold s.mu.
func (s *Service) SwitchBranch(ctx context.Context, branchName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publishIndexAvailabilityLocked()

	if branchName == s.current.currentBranch && s.current.collection != nil {
		return nil
	}

	// ADR-064: SetProject prepares the layout but opens no DB, so a real project is
	// a non-empty storage path (specs/decisions/064-branch-scoped-lazy-vector-index-open.md).
	if s.current.projectPath == "" {
		return errors.New("no project initialized; call SetProject first")
	}

	// Persist the outgoing branch's in-memory hashes before they are
	// overwritten by loadFileHashes for the new branch.
	if s.current.currentBranch != "" && s.current.collection != nil {
		if err := s.current.saveFileHashes(); err != nil {
			s.logger.Warn("failed to persist file-hash sidecar on branch switch",
				"branch", s.current.currentBranch, "error", err)
		}
	}

	// ADR-064 step (a): release the outgoing residents BEFORE the incoming open so a
	// switch peaks at one branch's working set (specs/decisions/064-branch-scoped-lazy-vector-index-open.md).
	s.releaseBranchResidentsLocked()

	// Branch-scoped open: NewPersistentDB decodes only this branch's collection
	// (ADR-064).
	db, err := s.openBranchDBLocked(branchName)
	if err != nil {
		return err
	}

	name := collectionName(branchName)
	col, err := db.GetOrCreateCollection(name, nil, s.embeddingFunc)
	if err != nil {
		// Deliberately do NOT publish db: a half-open state (db resident, collection nil)
		// parks at ~0 bytes — parkCurrentLocked gates on db != nil while estimateStateBytes
		// sizes from the COLLECTION — and takeParkedLocked would restore it collection-less.
		// Keep the state fully closed; GetOrCreateCollection genuinely fails on full volumes.
		return fmt.Errorf("getting or creating collection %q: %w", name, err)
	}
	s.current.db = db

	// Open this branch's lexical index (the previous branch's was closed by
	// releaseBranchResidentsLocked above); a state without a storage root or
	// project id (in-memory No Project) gets no lexical persistence.
	if s.current.projectPath != "" && s.current.projectID != "" {
		lexDir := filepath.Join(s.current.projectPath, "lexical", lexicalBranchDirName(branchName))
		// Real parent dir only (a planted symlink must not redirect the index);
		// bleve's New() creates the leaf directory itself.
		if mkErr := ensureRealDir(filepath.Dir(lexDir), 0o750); mkErr != nil {
			s.logger.Warn("failed to create lexical parent directory", "path", lexDir, "error", mkErr)
		} else {
			lex, lexErr := lexical.Open(lexDir)
			if lexErr != nil {
				s.logger.Warn("failed to open lexical index", "path", lexDir, "error", lexErr)
			} else {
				s.current.lexical = lex
			}
		}
	}

	s.current.collection = col
	s.current.currentBranch = branchName
	// Load (or migrate) the file-hash sidecar so ValidateCollection can compare
	// stored hashes against disk without an embedding-bearing collection Query.
	s.loadFileHashes()
	s.logger.Info("switched branch collection", "branch", branchName, "collection", name)
	return nil
}

// readFileFn is a safeio.ReadFile indirection — a test-only seam for asserting the
// stat fast path skips content reads; production must not reassign it. safeio's
// regular-file-only default cannot hang on a FIFO planted at a candidate path.
var readFileFn = safeio.ReadFile

// fileHashEntrySep separates the components of a file-hash sidecar value.
const fileHashEntrySep = "|"

// fullHashRevalidationEvery bounds how many consecutive ValidateCollection passes may
// ride the stat fast-path (size+mtime as a content proxy) before one pass re-reads and
// re-hashes every file — the backstop for rewrites preserving both size and mtime; 20
// keeps amortized revalidation at ~5% of a pass with a ≤20-pass detection window.
// See specs/domains/workspace.md (ValidateCollection stat fast-path backstop).
const fullHashRevalidationEvery = 20

// parseFileHashEntry parses a sidecar value in any format up to v5; malformed values
// and legacy bare hashes return ok=false (caller falls back to full read+hash).
// Grammar, strictness, and older-binary back-compat: specs/domains/workspace.md,
// sidecar entry format v5. Field accessors: fileHashEntryChunkerFP, fileHashEntryChunkSet.
func parseFileHashEntry(entry string) (hash string, size, mtimeUnixNano int64, ok bool) {
	parts := strings.Split(entry, fileHashEntrySep)
	if len(parts) < 3 || len(parts) > 5 {
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
	if len(parts) == 5 {
		// A 5th field that does not parse as a chunk set downgrades the entry to legacy
		// rather than carrying an unusable set; the allocation-free validity check keeps
		// this O(files) fast path from materializing throwaway sets (see validChunkIndicesField).
		if !validChunkIndicesField(parts[4]) {
			return parts[0], 0, 0, false
		}
	}
	return parts[0], size, mtime, true
}

// validChunkIndicesField checks the 5th sidecar field's grammar without materializing
// the set (parseChunkIndices's contiguous form allocates a []int of size N).
func validChunkIndicesField(field string) bool {
	if field == "" {
		return false
	}
	if rest, ok := strings.CutPrefix(field, chunkSetListPrefix); ok {
		if rest == "" {
			return false
		}
		parts := strings.Split(rest, ",")
		if len(parts) > maxChunkSetEntries {
			return false
		}
		for _, p := range parts {
			v, err := strconv.Atoi(p)
			if err != nil || v < 0 {
				return false
			}
		}
		return true
	}
	n, err := strconv.Atoi(field)
	return err == nil && n >= 0 && n <= maxChunkSetEntries
}

// fileHashEntryChunkerFP returns the entry's chunker fingerprint, or "" when absent
// (legacy/intermediate entries). "" means "configuration unknown — exempt from
// fingerprint-based staleness", never "default configuration".
func fileHashEntryChunkerFP(entry string) string {
	parts := strings.Split(entry, fileHashEntrySep)
	if len(parts) != 4 && len(parts) != 5 {
		return ""
	}
	return parts[3]
}

// chunkSetListPrefix marks the explicit-list form of the 5th sidecar field; a bare
// decimal N is the contiguous form (idxset codec, specs/decisions/049-vector-index-memory-reduction.md).
const chunkSetListPrefix = "L:"

// maxChunkSetEntries caps parseChunkIndices materialization: a corrupt or hand-edited
// set value must not become an allocation bomb. Oversized values downgrade the entry
// to legacy (specs/decisions/049-vector-index-memory-reduction.md).
const maxChunkSetEntries = 1 << 20

// encodeChunkIndices serializes a committed chunk-index set as the 5th sidecar field:
// contiguous 0..N-1 → "N", gapped (poison-dropped chunks) → "L:0,1,3" ascending — the
// idxset codec in specs/decisions/049-vector-index-memory-reduction.md. Input order is
// irrelevant (sorted and de-duplicated); parseChunkIndices inverts it.
func encodeChunkIndices(indices []int) string {
	sorted := make([]int, len(indices))
	copy(sorted, indices)
	slices.Sort(sorted)
	// De-duplicate in place (sorted, so equal values are adjacent).
	uniq := sorted[:0]
	for i, v := range sorted {
		if i == 0 || v != uniq[len(uniq)-1] {
			uniq = append(uniq, v)
		}
	}
	// Ascending distinct non-negative values are exactly {0..len-1} iff the
	// maximum equals len-1 (a gap would push the max above len-1).
	if len(uniq) == 0 || uniq[len(uniq)-1] == len(uniq)-1 {
		return strconv.Itoa(len(uniq))
	}
	var b strings.Builder
	b.WriteString(chunkSetListPrefix)
	for i, v := range uniq {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(v))
	}
	return b.String()
}

// parseChunkIndices decodes the 5th sidecar field ("N" contiguous, "L:0,1,3" gapped);
// ok is false for empty, malformed, or oversized values — callers must then treat the
// entry's chunk set as unknown.
func parseChunkIndices(field string) ([]int, bool) {
	if field == "" {
		return nil, false
	}
	if rest, ok := strings.CutPrefix(field, chunkSetListPrefix); ok {
		if rest == "" {
			return nil, false
		}
		parts := strings.Split(rest, ",")
		if len(parts) > maxChunkSetEntries {
			return nil, false
		}
		indices := make([]int, 0, len(parts))
		for _, p := range parts {
			v, err := strconv.Atoi(p)
			if err != nil || v < 0 {
				return nil, false
			}
			indices = append(indices, v)
		}
		return indices, true
	}
	n, err := strconv.Atoi(field)
	if err != nil || n < 0 || n > maxChunkSetEntries {
		return nil, false
	}
	indices := make([]int, n)
	for i := range indices {
		indices[i] = i
	}
	return indices, true
}

// chunkIndexFromID extracts the chunk index from a DocumentID ("<pathHash>:<idx>");
// ok is false for IDs outside that grammar (hand-built/foreign docs) — treat the
// file's committed chunk set as unknown.
func chunkIndexFromID(id string) (int, bool) {
	i := strings.LastIndexByte(id, ':')
	if i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// fileHashEntryChunkSet returns the entry's committed chunk-index set; ok is false for
// every older format and malformed values — callers fall back to enumerating the
// collection (specs/domains/workspace.md, enumeration without materialization).
func fileHashEntryChunkSet(entry string) ([]int, bool) {
	parts := strings.Split(entry, fileHashEntrySep)
	if len(parts) != 5 {
		return nil, false
	}
	return parseChunkIndices(parts[4])
}

// fileHashEntryHash returns the entry's content hash (any format).
func fileHashEntryHash(entry string) string {
	if hash, _, _, ok := parseFileHashEntry(entry); ok {
		return hash
	}
	return entry
}

// ChunkerFingerprint fingerprints the chunker configuration (max chunk size + overlap +
// content-filter policy) for sidecar field 4: a config change re-chunks files despite an
// unchanged content hash, a policy flip re-evaluates excluded files (reported deleted),
// identical re-chunks hit the embedding cache (specs/decisions/036-vector-index-embedding-optimization-policy.md).
func ChunkerFingerprint(maxChunkSize, overlap int, contentPolicy string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("v2|max=%d|overlap=%d|filter=%s", maxChunkSize, overlap, contentPolicy)))
	return hex.EncodeToString(sum[:6]) // 12 hex chars, plenty for a config fingerprint
}

// fileHashEntryFromMetadata composes a sidecar value from document metadata — the v5
// format when size/mtime metadata is present, the legacy bare hash otherwise (upgraded
// on the file's next index pass); "" chunkerFP/chunkSet mean unknown (no field written;
// exempt from fingerprint staleness), and the 5th field requires the 4th, keeping the
// grammar positional (specs/domains/workspace.md, sidecar entry format v5).
func fileHashEntryFromMetadata(md map[string]string, chunkerFP, chunkSet string) string {
	hash := md["content_hash"]
	size, mtime := md["file_size"], md["file_mtime_unix_nano"]
	if hash == "" || size == "" || mtime == "" {
		return hash
	}
	entry := hash + fileHashEntrySep + size + fileHashEntrySep + mtime
	if chunkerFP != "" {
		entry += fileHashEntrySep + chunkerFP
		if chunkSet != "" {
			entry += fileHashEntrySep + chunkSet
		}
	}
	return entry
}

// fileHashInfo is a file's per-file fact sheet (hash/size/mtime from the same
// stat+read that produced its chunks), carried explicitly to the sidecar write — the
// commit-time strip removes per-file bookkeeping from chromem documents
// (specs/decisions/049-vector-index-memory-reduction.md).
type fileHashInfo struct {
	filePath string
	hash     string
	size     string // file size in bytes, decimal string
	mtime    string // mtime in Unix nanoseconds, decimal string
}

// fileHashInfoFromMetadata extracts per-file facts from document metadata — the
// pre-upgrade carrier still used by standalone AddDocuments and tests. ok is false
// without a content hash: no sidecar entry, ValidateCollection reconciles from disk.
func fileHashInfoFromMetadata(md map[string]string) (fileHashInfo, bool) {
	fp, hash := md["file_path"], md["content_hash"]
	if fp == "" || hash == "" {
		return fileHashInfo{}, false
	}
	return fileHashInfo{
		filePath: fp,
		hash:     hash,
		size:     md["file_size"],
		mtime:    md["file_mtime_unix_nano"],
	}, true
}

// entry renders this file's sidecar value (see fileHashEntryFromMetadata).
func (info fileHashInfo) entry(chunkerFP, chunkSet string) string {
	return fileHashEntryFromMetadata(map[string]string{
		"content_hash":         info.hash,
		"file_size":            info.size,
		"file_mtime_unix_nano": info.mtime,
	}, chunkerFP, chunkSet)
}

// ValidateCollection checks stored hashes against disk and returns stale, new, and
// deleted paths. checker is the caller's ignore resolver, reused so this walk applies
// exactly the IndexFull filtering — a divergence would reindex in a loop.
func (s *Service) ValidateCollection(ctx context.Context, workspacePath string, checker ignore.IgnoreChecker) (staleFiles, newFiles, deletedFiles []string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.current.collection == nil {
		return nil, nil, nil, errors.New("no collection available; call SwitchBranch first")
	}

	cacheStarted := time.Now()
	storedHashes, err := s.getCollectionFileHashes()
	s.telemetry.observe(StageCacheLookup, len(storedHashes), time.Since(cacheStarted))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("getting collection file hashes: %w", err)
	}

	// Periodic full-hash revalidation (see fullHashRevalidationEvery). The counter is
	// atomic only for race-freedom under the read lock; passes are serialized per
	// indexer in practice, so concurrency can at worst force revalidation twice.
	forceFullHash := s.current.validationsSinceFullHash.Add(1) >= fullHashRevalidationEvery
	if forceFullHash {
		s.current.validationsSinceFullHash.Store(0)
	}

	seen := make(map[string]bool, len(storedHashes))

	absRoot, absErr := filepath.Abs(workspacePath)
	if absErr != nil {
		return nil, nil, nil, fmt.Errorf("resolving workspace path: %w", absErr)
	}

	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkDirErr error) error {
		if walkDirErr != nil {
			return walkDirErr
		}

		if d.IsDir() {
			if isHiddenName(d.Name()) || checker.Ignored(path, true) {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.Type().IsRegular() {
			return nil
		}

		if isHiddenName(d.Name()) || checker.Ignored(path, false) {
			return nil
		}

		absPath, pathErr := filepath.Abs(path)
		if pathErr != nil {
			return nil //nolint:nilerr // skip unresolvable paths
		}

		// Size guard: skipped files are never indexed, so skipping here too avoids an
		// infinite reindex loop. The size limit backstops the NUL-byte check, which
		// cannot recognize every binary format (ONNX/safetensors have readable headers).
		info, infoErr := d.Info()
		if infoErr == nil && tooLargeForIndex(info.Size(), s.maxFileSize) {
			return nil //nolint:nilerr // skip oversized files
		}

		// Stat fast path: a current-format entry whose recorded size+mtime still match
		// (and whose chunker fingerprint matches the active config) marks the file seen
		// with no read and no hash — what keeps the 1s-debounced incremental pass from
		// re-hashing the workspace. It is a heuristic: size+mtime-preserving rewrites
		// escape until the periodic full-hash pass (fullHashRevalidationEvery). Falls
		// through on legacy/fingerprint-less entries, any mismatch, a failed d.Info, or
		// a forced full-hash pass; the full-read comparison upgrades the entry next index.
		if !forceFullHash && info != nil {
			if stored, exists := storedHashes[absPath]; exists {
				if _, size, mtime, ok := parseFileHashEntry(stored); ok &&
					size == info.Size() && mtime == info.ModTime().UnixNano() &&
					fileHashEntryChunkerFP(stored) == s.chunkerFingerprint {
					seen[absPath] = true
					return nil //nolint:nilerr // unchanged: skip content read + hash
				}
			}
		}

		// Header pre-read rejects multi-GB binaries before the full read; binary and
		// empty files are never indexed, so reporting them new every pass would loop.
		binary, bErr := isBinaryHeader(absPath)
		if bErr != nil {
			return nil //nolint:nilerr // skip unreadable files
		}
		if binary {
			return nil //nolint:nilerr // skip binary files
		}

		content, readErr := readFileFn(absPath)
		if readErr != nil {
			return nil //nolint:nilerr // skip unreadable files
		}
		if len(content) == 0 {
			return nil //nolint:nilerr // skip empty files
		}

		// Content policy: an excluded file is reported deleted (not seen) so its
		// documents and sidecar entry are removed, and never enters newFiles. The
		// check runs on the same bytes processFile filters, and the filter config
		// feeds the chunker fingerprint, so the fast path only shortcuts files whose
		// verdict cannot have changed.
		if _, skip := DetectContentSkip(content, s.contentFilter); skip {
			return nil //nolint:nilerr // excluded by content policy
		}

		currentHash := computeHash(content)

		if stored, exists := storedHashes[absPath]; exists {
			seen[absPath] = true
			if fileHashEntryHash(stored) != currentHash {
				staleFiles = append(staleFiles, absPath)
			} else if entryFP := fileHashEntryChunkerFP(stored); entryFP != "" && entryFP != s.chunkerFingerprint {
				// Content unchanged but chunked under another configuration: re-chunk.
				// Fingerprint-less entries are exempt (see fileHashEntryChunkerFP).
				staleFiles = append(staleFiles, absPath)
			}
		} else {
			newFiles = append(newFiles, absPath)
		}

		return nil
	})
	if walkErr != nil {
		return nil, nil, nil, fmt.Errorf("walking workspace %s: %w", workspacePath, walkErr)
	}

	for storedPath := range storedHashes {
		if !seen[storedPath] {
			deletedFiles = append(deletedFiles, storedPath)
		}
	}

	return staleFiles, newFiles, deletedFiles, nil
}

// GetCollectionFiles returns all unique file paths and their raw sidecar entries
// (see parseFileHashEntry for the value grammar).
func (s *Service) GetCollectionFiles() (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getCollectionFileHashes()
}

// getCollectionFileHashes returns the file→entry map from the in-memory sidecar,
// avoiding an embedding-bearing Query per validation pass. The map is a defensive
// copy: it may outlive the lock while upsertFileHashes/removeFileHashes mutate the
// original under the write lock. A nil sidecar falls back to a one-shot query.
func (s *Service) getCollectionFileHashes() (map[string]string, error) {
	if s.current.collection == nil {
		return nil, errors.New("no collection available")
	}
	if s.current.fileHashes != nil {
		out := make(map[string]string, len(s.current.fileHashes))
		for k, v := range s.current.fileHashes {
			out[k] = v
		}
		return out, nil
	}
	// Fallback for a collection built before the sidecar existed; the Query may pay
	// an embedding cost, so SwitchBranch loads the sidecar eagerly to keep this cold.
	return s.current.queryCollectionFileHashes(context.Background(), s.unitQueryVector())
}

// queryCollectionFileHashes enumerates this state's collection hashes (see
// queryCollectionFileHashesFrom); unitVec swaps the text Query for a no-ONNX
// QueryEmbedding when the dimension is known. Caller must hold at least RLock.
func (ps *projectState) queryCollectionFileHashes(ctx context.Context, unitVec []float32) (map[string]string, error) {
	return queryCollectionFileHashesFrom(ctx, ps.collection, unitVec)
}

// queryCollectionFileHashesFrom enumerates an explicitly captured collection: the
// migration captures ps.collection under the write lock and queries it WITHOUT the
// lock (chromem serializes internally); a state closed mid-query only drops the
// reference, and the post-query staleness re-check discards the result.
func queryCollectionFileHashesFrom(ctx context.Context, col *chromem.Collection, unitVec []float32) (map[string]string, error) {
	count := col.Count()
	if count == 0 {
		return make(map[string]string), nil
	}

	var results []chromem.Result
	var err error
	if unitVec != nil {
		results, err = col.QueryEmbedding(ctx, unitVec, count, nil, nil)
	} else {
		results, err = col.Query(ctx, " ", count, nil, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("querying collection for file list: %w", err)
	}

	fileHashes := make(map[string]string, len(results))
	for _, r := range results {
		fp := r.Metadata["file_path"]
		if fp == "" {
			continue
		}
		// Stripped documents carry no content_hash; skip them — the write path owns
		// their entries, and a fabricated empty one would flag the file forever.
		if r.Metadata["content_hash"] == "" {
			continue
		}
		// One entry per file: all chunks share hash and size/mtime. Fingerprint and
		// chunk set are not recoverable from committed metadata, so migrated entries
		// are fingerprint-less/set-less (exempt) until the file's next index pass.
		fileHashes[fp] = fileHashEntryFromMetadata(r.Metadata, "", "")
	}

	return fileHashes, nil
}

// fileHashesPath returns the on-disk path of the sidecar for this state's
// branch, or "" if project/branch is unset. Caller must hold s.mu.
func (ps *projectState) fileHashesPath() string {
	if ps.projectPath == "" || ps.currentBranch == "" {
		return ""
	}
	return filepath.Join(ps.projectPath, "file_hashes_"+collectionName(ps.currentBranch)+".json")
}

// contentlessMarkerPath returns the zero-byte content-less migration marker's path —
// its presence certifies every document of this collection stripped, so the one-time
// migration never replays (specs/decisions/049-vector-index-memory-reduction.md).
// Caller must hold s.mu.
func (ps *projectState) contentlessMarkerPath() string {
	if ps.projectPath == "" || ps.currentBranch == "" {
		return ""
	}
	return filepath.Join(ps.projectPath, "contentless_"+collectionName(ps.currentBranch)+".done")
}

// contentlessMarkerExists reports the marker's presence; it gates whether an empty
// sidecar is trustworthy (see loadFileHashes). Caller must hold s.mu.
func contentlessMarkerExists(ps *projectState) bool {
	marker := ps.contentlessMarkerPath()
	if marker == "" {
		return false
	}
	_, err := os.Stat(marker)
	return err == nil
}

// loadFileHashes populates the current state's sidecar from disk; when absent, the
// backfill defers to a background goroutine (see migrateFileHashes) so SwitchBranch
// never pays the embedding-bearing enumeration synchronously. Caller must hold s.mu
// (write).
func (s *Service) loadFileHashes() {
	ps := s.current

	// Settle the content-less migration for whatever state the load leaves behind —
	// on every exit path, under the caller's lock.
	defer s.maybeMigrateContentlessLocked()

	// Reset the backfill-failure flag: this load either trusts a usable on-disk
	// sidecar or starts a fresh backfill (which re-sets the flag on failure).
	ps.fileHashMigrationFailed.Store(false)

	// Cancel any in-flight backfill/content-less migration left over from the previous
	// branch; the deferred trigger above re-evaluates the latter for the incoming one.
	if ps.migrationCancel != nil {
		ps.migrationCancel()
		ps.migrationCancel = nil
	}
	if ps.contentlessCancel != nil {
		ps.contentlessCancel()
		ps.contentlessCancel = nil
	}

	// Trust protocol: an EMPTY sidecar is trusted only when the marker certifies the
	// collection stripped (its backfill could never recover hashes; re-running per open
	// is waste). With the marker absent, empty means an in-flight placeholder or a
	// failed backfill (fileHashMigrationFailed) — never trusted, always re-settled.
	// See specs/domains/workspace.md (file-hash sidecar trust protocol).
	if path := ps.fileHashesPath(); path != "" {
		if data, err := safeio.ReadFile(path); err == nil {
			var m map[string]string
			if jsonErr := json.Unmarshal(data, &m); jsonErr == nil {
				if len(m) > 0 || contentlessMarkerExists(ps) {
					ps.fileHashes = m
					ps.fileHashMigrationPending.Store(false)
					ps.migrationCh = closedChan()
					return
				}
			}
		}
	}

	// Nothing to migrate; IndexFull populates the sidecar via upsertFileHashes.
	if ps.collection == nil || ps.collection.Count() == 0 {
		ps.fileHashes = make(map[string]string)
		ps.fileHashMigrationPending.Store(false)
		ps.migrationCh = closedChan()
		return
	}

	// Non-empty collection, no sidecar: defer the backfill (IndexIncremental waits on
	// migrationCh, so the empty map never triggers a spurious full re-embed). Bound the
	// goroutine to ps, not s.current: a parked/evicted state must settle its own flags.
	ps.fileHashes = make(map[string]string)
	ps.fileHashMigrationPending.Store(true)
	done := make(chan struct{})
	ps.migrationCh = done
	branch := ps.currentBranch
	mctx, cancel := context.WithCancel(context.Background())
	ps.migrationCancel = cancel
	s.migrationWG.Add(1)
	go s.migrateFileHashes(mctx, ps, branch, done)
}

// migrateFileHashes is the background backfill: one embedding-bearing enumeration,
// adopted into ps.fileHashes or discarded on cancellation/branch change. Bound to ps
// so an orphaned migration settles its own flags without clobbering a newer project.
// The caller must NOT hold s.mu. Closes done when settled.
func (s *Service) migrateFileHashes(ctx context.Context, ps *projectState, branch string, done chan struct{}) {
	defer s.migrationWG.Done()
	defer close(done)

	// Clear the pending flag only while THIS migration is still registered on ps: a
	// restore may have installed a replacement while we waited on the lock, and an
	// unconditional clear would let parkCurrentLocked skip a legitimate sidecar save.
	clearIfCurrentLocked := func() {
		if ps.migrationCh == done {
			ps.fileHashMigrationPending.Store(false)
		}
	}

	// Phase 1 (write lock): re-check staleness and capture the collection — a stale
	// migration must not overwrite another branch's sidecar.
	s.mu.Lock()
	if ctx.Err() != nil || ps.currentBranch != branch || ps.collection == nil {
		clearIfCurrentLocked()
		s.mu.Unlock()
		return
	}
	col := ps.collection
	s.mu.Unlock()

	// Heavy phase without the lock (mirrors migrateContentless): chromem serializes
	// the captured collection, and phase 2 re-checks staleness. Holding the write lock
	// here once stalled every transition AND Close() for the whole query (6s observed).
	hashes, qErr := queryCollectionFileHashesFrom(ctx, col, s.unitQueryVector())

	// Phase 2 — re-acquire for the adoption.
	s.mu.Lock()
	defer s.mu.Unlock()

	if qErr != nil {
		if ps.migrationCh == done {
			// Backfill failed: ps.fileHashes stays the untrusted placeholder. Flag it so
			// park/eviction never persists an empty sidecar and the content-less
			// migration does not certify the collection from it.
			ps.fileHashMigrationFailed.Store(true)
		}
		s.logger.Warn("failed to migrate file-hash sidecar from collection", "error", qErr)
		clearIfCurrentLocked()
		return
	}
	// Re-check after the unlocked query (switch/close may have raced it).
	if ctx.Err() != nil || ps.currentBranch != branch || ps.collection == nil {
		clearIfCurrentLocked()
		return
	}
	ps.fileHashes = hashes
	ps.fileHashMigrationPending.Store(false)
	ps.fileHashMigrationFailed.Store(false)
	if len(hashes) == 0 {
		// Zero hashes on a non-empty collection: everything stripped, or a lost sidecar
		// with content-less documents. Either way it cannot be rebuilt here — warn so a
		// lost sidecar (full re-embed on next validation) is visible.
		s.logger.Warn("file-hash sidecar migration produced no entries for a non-empty collection; "+
			"if the sidecar was lost the project will be re-indexed",
			"branch", branch, "documents", ps.collection.Count())
	}
	if err := ps.saveFileHashes(); err != nil {
		s.logger.Warn("failed to persist file-hash sidecar after migration", "error", err)
	}
	s.logger.Info("file-hash sidecar migrated from collection", "branch", branch, "files", len(hashes))
}

// WaitFileHashMigration blocks until the current branch's sidecar backfill settles;
// IndexIncremental waits so ValidateCollection sees the full map. Caller must NOT
// hold s.mu.
func (s *Service) WaitFileHashMigration(ctx context.Context) error {
	ch := func() chan struct{} {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.current.migrationCh
	}()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// closedChan returns an already-closed signal channel.
func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// contentlessProbeGapTolerance bounds the consecutive GetByID misses that may end a
// legacy entry's (set-less) chunk-index scan: poison-dropped chunks leave isolated
// holes, so the scan bridges short gaps — but only after the first hit; a leading run
// of dropped chunks must not end the scan (see migrateContentless).
const contentlessProbeGapTolerance = 8

// writeContentlessMarker best-effort writes the marker; failure only costs one no-op
// probe pass next open. safeio.WriteFileAtomic keeps the deterministic path FIFO- and
// symlink-safe (a planted FIFO would wedge the service under s.mu).
func (s *Service) writeContentlessMarker(path string) {
	if path == "" {
		return
	}
	if err := safeio.WriteFileAtomic(path, nil, 0o644); err != nil {
		s.logger.Warn("failed to write content-less migration marker",
			"path", path, "error", err)
	}
}

// maybeMigrateContentlessLocked settles the one-time content-less migration (lifecycle
// in specs/decisions/049-vector-index-memory-reduction.md): background rewrite when
// non-empty and unmarked, synchronous marker for an empty collection, settle channel
// otherwise. Runs at the end of loadFileHashes and on SetProject's park-restore path —
// the only triggers a park-cancelled migration gets. Caller must hold s.mu (write).
func (s *Service) maybeMigrateContentlessLocked() {
	ps := s.current

	// Abandon any in-flight migration: its documents belong to the state it snapshotted.
	if ps.contentlessCancel != nil {
		ps.contentlessCancel()
		ps.contentlessCancel = nil
	}

	settle := func() {
		ps.contentlessCh = closedChan()
	}

	marker := ps.contentlessMarkerPath()
	if marker == "" || ps.collection == nil {
		// In-memory or branch-less state: nothing to migrate, ever.
		settle()
		return
	}
	if _, err := os.Stat(marker); err == nil {
		// Already migrated — the steady state; never run again.
		settle()
		return
	} else if !errors.Is(err, fs.ErrNotExist) {
		// Unreadable marker (permissions, dangling symlink): treat as absent
		// and let the idempotent probe decide — worst case it strips nothing.
		s.logger.Warn("failed to stat content-less migration marker; treating as absent",
			"path", marker, "error", err)
	}

	if ps.collection.Count() == 0 {
		s.writeContentlessMarker(marker)
		settle()
		return
	}

	// Non-empty and unmarked: defer the rewrite to a ps-bound goroutine that waits
	// for the sidecar first — stripping before the backfill would destroy the
	// full metadata the backfill feeds on.
	done := make(chan struct{})
	ps.contentlessCh = done
	branch := ps.currentBranch
	mctx, cancel := context.WithCancel(context.Background())
	ps.contentlessCancel = cancel
	s.migrationWG.Add(1)
	go s.migrateContentless(mctx, ps, branch, done)
}

// migrateContentless is the one-time rewrite of a legacy collection to the content-less
// shape (protocol: specs/decisions/049-vector-index-memory-reduction.md): probe each
// sidecar-tracked file's chunks, re-commit the fat ones with the SAME ID/vector and
// stripped content/metadata — chromem replaces each gob atomically, so the collection
// stays queryable at every instant. Cancellation abandons WITHOUT the marker (the next
// open replays; stripped docs are skipped for free). The heavy phase holds no lock;
// only each window flush takes the read lock, atomic against RebuildCollection / park /
// closeStateLocked (a delete-and-recreate must never interleave and resurrect ghost
// gobs). The caller must NOT hold s.mu. Closes done when settled.
func (s *Service) migrateContentless(ctx context.Context, ps *projectState, branch string, done chan struct{}) {
	defer s.migrationWG.Done()
	defer close(done)

	// Wait for the sidecar to settle first: the backfill enumerates FULL metadata,
	// and stripping first would destroy the content_hash it feeds on. A re-entrant
	// loadFileHashes can only swap the channel after cancelling our ctx, so we terminate.
	if ch := func() chan struct{} {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return ps.migrationCh
	}(); ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return
		}
	}

	// A failed backfill leaves an untrusted empty placeholder the migration cannot
	// distinguish from "nothing trackable" — and its marker would forbid replay,
	// permanently keeping the fat documents. Abandon without the marker; replay next
	// open (park/eviction likewise refuse the placeholder — fileHashMigrationFailed).
	if ps.fileHashMigrationFailed.Load() {
		return
	}

	// Snapshot entries + collection pointer; per-window re-checks verify the live
	// state still matches.
	s.mu.RLock()
	if ctx.Err() != nil || ps.currentBranch != branch || ps.collection == nil {
		s.mu.RUnlock()
		return
	}
	col := ps.collection
	entries := make(map[string]string, len(ps.fileHashes))
	for fp, entry := range ps.fileHashes {
		entries[fp] = entry
	}
	s.mu.RUnlock()

	if len(entries) == 0 {
		// Nothing sidecar-tracked: the backfill completed (or was unneeded — a failed
		// one returned above), so "empty" is genuine and the marker applies.
		s.finishContentless(ctx, ps, branch, 0, 0)
		return
	}

	// probeCap covers every chunk a file can hold (the indexer caps at
	// max_chunks_per_file).
	probeCap := s.maxChunksPerFile
	if probeCap <= 0 {
		probeCap = DefaultMaxChunksPerFile
	}

	paths := make([]string, 0, len(entries))
	for fp := range entries {
		paths = append(paths, fp)
	}
	slices.Sort(paths) // deterministic replay and log output

	window := make([]chromem.Document, 0, embeddingCommitWindowSize)
	probed, stripped := 0, 0

	// flush re-commits one window under the READ lock (see doc: atomic vs
	// RebuildCollection/park/close). The commit runs on a cancellation-stripped ctx:
	// once the pre-checks pass, the window must commit fully or the marker would
	// certify a half-stripped collection. Staleness surfaces as context.Canceled.
	flush := func() error {
		if len(window) == 0 {
			return nil
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		if ctx.Err() != nil || ps.currentBranch != branch || ps.collection != col {
			return context.Canceled
		}
		started := time.Now()
		err := col.AddDocuments(context.WithoutCancel(ctx), window, 1)
		s.telemetry.observe(StageChromemCommit, len(window), time.Since(started))
		if err != nil {
			return fmt.Errorf("re-committing %d stripped documents: %w", len(window), err)
		}
		stripped += len(window)
		window = window[:0]
		return nil
	}

	add := func(doc chromem.Document) error {
		window = append(window, doc)
		if len(window) < embeddingCommitWindowSize {
			return nil
		}
		return flush()
	}

	// abandon WARNs only genuine commit errors — cancellation is a normal lifecycle
	// event. No marker either way, so the next open replays.
	abandon := func(err error) {
		s.mu.Lock()
		silent := ctx.Err() != nil || ps.currentBranch != branch || ps.collection == nil
		s.mu.Unlock()
		if !silent {
			s.logger.Warn("content-less migration aborted; it will replay on the next open",
				"branch", branch, "error", err)
		}
	}

	for _, fp := range paths {
		if err := ctx.Err(); err != nil {
			return // cancelled (park/close/switch): retry on the next open
		}
		entry := entries[fp]

		// handle probes one chunk, staging it for stripping when still fat; post-upgrade
		// documents are already content-less and skip for free.
		handle := func(i int) error {
			doc, err := col.GetByID(ctx, DocumentID(fp, i))
			if err != nil {
				// Miss: deleted since, or the scan ran past the chunk count.
				return nil //nolint:nilerr // a probe miss is not an error
			}
			probed++
			if doc.Content == "" {
				return nil
			}
			doc.Metadata = narrowCommitMetadata(doc.Metadata)
			doc.Content = ""
			return add(doc)
		}

		if set, ok := fileHashEntryChunkSet(entry); ok {
			// Known set: probe exactly those indices.
			for _, i := range set {
				if err := handle(i); err != nil {
					abandon(err)
					return
				}
			}
			continue
		}
		// Legacy set-less entry: exhaustive scan to probeCap with the gap-tolerant stop
		// of contentlessProbeGapTolerance (applied only after the first hit — see its doc).
		misses, hits := 0, 0
		for i := 0; i < probeCap; i++ {
			doc, err := col.GetByID(ctx, DocumentID(fp, i))
			if err != nil {
				if hits > 0 {
					misses++
					if misses >= contentlessProbeGapTolerance {
						break
					}
				}
				continue
			}
			misses = 0
			hits++
			probed++
			if doc.Content == "" {
				continue
			}
			doc.Metadata = narrowCommitMetadata(doc.Metadata)
			doc.Content = ""
			if err := add(doc); err != nil {
				abandon(err)
				return
			}
		}
	}
	if err := flush(); err != nil {
		abandon(err)
		return
	}

	s.finishContentless(ctx, ps, branch, probed, stripped)
}

// finishContentless re-verifies the state, writes the marker, and hands the
// probe/strip churn back to the OS via freeOSMemory off the lock. Caller must NOT hold s.mu.
func (s *Service) finishContentless(ctx context.Context, ps *projectState, branch string, probed, stripped int) {
	s.mu.Lock()
	if ctx.Err() != nil || ps.currentBranch != branch || ps.collection == nil {
		s.mu.Unlock()
		return
	}
	s.writeContentlessMarker(ps.contentlessMarkerPath())
	s.mu.Unlock()

	s.logger.Info("content-less migration complete",
		"branch", branch, "probed", probed, "stripped", stripped)
	go s.freeOSMemory()
}

// WaitContentlessMigration blocks until the migration settles; IndexIncremental waits
// so its deletes/re-indexes never interleave with the ID-preserving re-commits
// (specs/decisions/049-vector-index-memory-reduction.md). Caller must NOT hold s.mu.
func (s *Service) WaitContentlessMigration(ctx context.Context) error {
	ch := func() chan struct{} {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.current.contentlessCh
	}()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// saveFileHashes atomically writes the sidecar; missing project/branch or a nil map is
// a no-op. Caller must hold the owning Service's mu (write).
func (ps *projectState) saveFileHashes() error {
	path := ps.fileHashesPath()
	if path == "" || ps.fileHashes == nil {
		return nil
	}
	data, err := json.Marshal(ps.fileHashes)
	if err != nil {
		return fmt.Errorf("marshaling file hashes: %w", err)
	}
	// FIFO/symlink-safe: the former fixed "<path>.tmp" staging name was pre-plantable
	// (a planted FIFO wedged the service and shutdown under s.mu); WriteFileAtomic
	// stages under a randomized name.
	if err := safeio.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("writing file-hash sidecar: %w", err)
	}
	return nil
}

// upsertFileHashesFiltered records file→entry rows for the given documents into the
// in-memory sidecar (facts via fileHashInfoFromMetadata, rendering via
// composeFileHashEntryInfo). Hash-less documents are skipped (ValidateCollection
// reconciles their files from disk). The 5th-field chunk set derives from document IDs
// with droppedIDs (poison-dropped chunks) excluded, and UNIONs any existing set so a
// file committed across two AddDocuments calls is not truncated; a fully dropped file
// still gets its "0" entry — otherwise it would re-index forever.
//
// Persistence is deliberately deferred to lifecycle boundaries (SwitchBranch,
// SetProject, Rebuild, Close, post-migration): one upsert per batch would rewrite the
// whole map O(batches × files) per pass; a crash costs a stale sidecar that
// ValidateCollection reconciles. Caller must hold s.mu (write).
func (s *Service) upsertFileHashesFiltered(docs []chromem.Document, droppedIDs map[string]struct{}) {
	if s.current.fileHashes == nil {
		s.current.fileHashes = make(map[string]string)
	}
	type fileUpsert struct {
		info fileHashInfo // per-file facts (identical across a file's chunks)
		ids  []string     // IDs of the chunks that committed
	}
	perFile := make(map[string]*fileUpsert)
	for _, d := range docs {
		info, ok := fileHashInfoFromMetadata(d.Metadata)
		if !ok {
			continue
		}
		fu := perFile[info.filePath]
		if fu == nil {
			fu = &fileUpsert{info: info}
			perFile[info.filePath] = fu
		}
		if _, drop := droppedIDs[d.ID]; drop {
			continue
		}
		fu.ids = append(fu.ids, d.ID)
	}
	for fp, fu := range perFile {
		// Merge with any recorded set: a file may span several AddDocuments calls, and
		// an overwrite would record only the last call's indices — under-deleting via
		// sidecarDocumentIDs and starving RebuildLexical v2.
		ids := fu.ids
		if existing, ok := s.current.fileHashes[fp]; ok {
			if prevSet, prevOK := fileHashEntryChunkSet(existing); prevOK {
				merged := make([]string, 0, len(prevSet)+len(ids))
				for _, i := range prevSet {
					merged = append(merged, DocumentID(fp, i))
				}
				ids = append(merged, ids...)
			}
		}
		s.current.fileHashes[fp] = s.composeFileHashEntryInfo(fu.info, ids)
	}
}

// upsertFileHashEntryInfo records ONE file's entry — the single-file form of
// upsertFileHashesFiltered used by the streaming accumulator's publish step.
// Caller must hold s.mu (write).
func (s *Service) upsertFileHashEntryInfo(info fileHashInfo, committedIDs []string) {
	if info.filePath == "" {
		return
	}
	if s.current.fileHashes == nil {
		s.current.fileHashes = make(map[string]string)
	}
	s.current.fileHashes[info.filePath] = s.composeFileHashEntryInfo(info, committedIDs)
}

// composeFileHashEntryInfo renders one file's entry; any ID outside the DocumentID
// grammar (hand-built/foreign docs) leaves the set unknown — no 5th field, enumeration
// falls back to the collection Query.
func (s *Service) composeFileHashEntryInfo(info fileHashInfo, committedIDs []string) string {
	indices := make([]int, 0, len(committedIDs))
	known := true
	for _, id := range committedIDs {
		idx, ok := chunkIndexFromID(id)
		if !ok {
			known = false
			break
		}
		indices = append(indices, idx)
	}
	chunkSet := ""
	if known {
		chunkSet = encodeChunkIndices(indices)
	}
	return info.entry(s.chunkerFingerprint, chunkSet)
}

// removeFileHashes drops paths from the in-memory sidecar (persisted at lifecycle
// boundaries, like upsertFileHashesFiltered). Caller must hold s.mu (write).
func (s *Service) removeFileHashes(paths []string) {
	if s.current.fileHashes == nil || len(paths) == 0 {
		return
	}
	for _, p := range paths {
		delete(s.current.fileHashes, p)
	}
}

// RebuildCollection deletes the current branch collection and creates a fresh one.
// Caller must hold s.mu (write lock).
func (s *Service) RebuildCollection(ctx context.Context) error {
	if s.current.db == nil {
		return errors.New("no database initialized")
	}
	if s.current.currentBranch == "" {
		return errors.New("no branch set")
	}

	name := collectionName(s.current.currentBranch)

	if err := s.current.db.DeleteCollection(name); err != nil {
		s.logger.Warn("failed to delete collection during rebuild", "collection", name, "error", err)
	}

	col, err := s.current.db.GetOrCreateCollection(name, nil, s.embeddingFunc)
	if err != nil {
		return fmt.Errorf("creating fresh collection %q: %w", name, err)
	}
	s.current.collection = col
	// Publish immediately: the lock-free UI status reader must see the fresh
	// collection without waiting on the indexing that follows.
	s.publishIndexAvailabilityLocked()
	// Reset the sidecar (a rebuilt collection is empty until re-indexed) and drop
	// the in-flight backfill: nothing left to migrate.
	if s.current.migrationCancel != nil {
		s.current.migrationCancel()
		s.current.migrationCancel = nil
	}
	// The content-less migration is abandoned the same way; the fresh collection gets
	// its marker immediately — empty, and every future commit is content-less.
	if s.current.contentlessCancel != nil {
		s.current.contentlessCancel()
		s.current.contentlessCancel = nil
	}
	s.current.contentlessCh = closedChan()
	s.writeContentlessMarker(s.current.contentlessMarkerPath())
	s.current.fileHashes = make(map[string]string)
	s.current.fileHashMigrationPending.Store(false)
	s.current.fileHashMigrationFailed.Store(false)
	s.current.migrationCh = closedChan()
	if err := s.current.saveFileHashes(); err != nil {
		s.logger.Warn("failed to persist file-hash sidecar after rebuild", "error", err)
	}
	s.logger.Info("rebuilt collection", "branch", s.current.currentBranch, "collection", name)
	return nil
}

// BatchEmbedder embeds documents in one internally-chunked inference pass (sp4rk's
// embedding.Embedder). Wiring it into AddDocuments embeds before the commit so chromem
// skips per-document embedding (a pre-populated Document.Embedding is final); queries
// keep using the chromem EmbeddingFunc (specs/decisions/036-vector-index-embedding-optimization-policy.md).
type BatchEmbedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// commitMetadataKeys are the only metadata keys a committed document retains — the
// chunk-position fields; all per-file bookkeeping lives in the sidecar (commit-time
// strip, specs/decisions/049-vector-index-memory-reduction.md).
var commitMetadataKeys = [4]string{"file_path", "start_line", "end_line", "language"}

// strippedForCommit returns the commit view of docs: content dropped wherever an
// embedding is already present (persisting consumed text doubles disk for zero
// readers — content reconstructs from the source; WITHOUT an embedding the text must
// stay or chromem would embed ""), and metadata narrowed to commitMetadataKeys so
// both paths store the same gob shape. The input slice is untouched — callers (and
// tests) keep reading per-file metadata from their originals.
func strippedForCommit(docs []chromem.Document) []chromem.Document {
	out := make([]chromem.Document, len(docs))
	for i, d := range docs {
		if len(d.Embedding) > 0 {
			d.Content = ""
		}
		d.Metadata = narrowCommitMetadata(d.Metadata)
		out[i] = d
	}
	return out
}

// narrowCommitMetadata copies md down to the commitMetadataKeys. A nil map
// stays nil (chromem handles nil metadata); unknown keys are dropped.
func narrowCommitMetadata(md map[string]string) map[string]string {
	if md == nil {
		return nil
	}
	narrowed := make(map[string]string, len(commitMetadataKeys))
	for _, k := range commitMetadataKeys {
		if v, ok := md[k]; ok {
			narrowed[k] = v
		}
	}
	return narrowed
}

// commitEmbeddedDocuments persists one pre-embedded window and mirrors the IDs into
// the lexical index. It does NOT touch the sidecar: the accumulator publishes a file
// hash only after all its chunks committed or were poison-dropped. Caller must hold
// s.mu (write lock).
func (s *Service) commitEmbeddedDocuments(ctx context.Context, vecDocs []chromem.Document, lexDocs []lexical.Doc) error {
	if len(vecDocs) == 0 {
		return nil
	}
	if len(vecDocs) > embeddingCommitWindowSize {
		return fmt.Errorf("embedded commit window contains %d documents; limit is %d", len(vecDocs), embeddingCommitWindowSize)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("committing embedded documents: %w", err)
	}

	commitStarted := time.Now()
	commitErr := s.current.collection.AddDocuments(ctx, strippedForCommit(vecDocs), 1)
	s.telemetry.observe(StageChromemCommit, len(vecDocs), time.Since(commitStarted))
	if commitErr != nil {
		return fmt.Errorf("committing %d embedded documents: %w", len(vecDocs), commitErr)
	}

	if s.current.lexical != nil && len(lexDocs) > 0 {
		upsertStarted := time.Now()
		upsertErr := s.current.lexical.Upsert(ctx, lexDocs)
		s.telemetry.observe(StageBleveUpsert, len(lexDocs), time.Since(upsertStarted))
		if upsertErr != nil {
			s.logger.Warn("lexical upsert failed; will be repaired via RebuildLexical",
				"branch", s.current.currentBranch, "docs", len(lexDocs), "error", upsertErr)
		}
	}
	return nil
}

// AddDocuments commits documents to chromem first and mirrors them to the lexical
// index best-effort (dual-write invariant, specs/domains/workspace.md — RebuildLexical
// repairs drift). Windows of ≤embeddingCommitWindowSize documents are embedded
// up-front when a BatchEmbedder is configured (see embedSubBatch); chromem persists
// the strippedForCommit view and the caller's slice is never mutated. A nil
// BatchEmbedder keeps the legacy per-document path. Caller must hold s.mu (write lock).
func (s *Service) AddDocuments(ctx context.Context, vecDocs []chromem.Document, lexDocs []lexical.Doc) error {
	if s.current.collection == nil {
		return errors.New("no collection available")
	}
	if len(vecDocs) == 0 && len(lexDocs) == 0 {
		return nil
	}

	// droppedIDs: documents the per-text fallback could not embed — excluded from
	// BOTH the chromem commit and the lexical upsert so the indexes never diverge.
	var droppedIDs map[string]struct{}

	for start := 0; start < len(vecDocs); start += embeddingCommitWindowSize {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("adding documents (cancelled mid-batch): %w", err)
		}
		end := start + embeddingCommitWindowSize
		if end > len(vecDocs) {
			end = len(vecDocs)
		}
		sub := vecDocs[start:end]
		if s.batchEmbedder != nil {
			embedStarted := time.Now()
			kept, dropped, embErr := s.embedSubBatch(ctx, sub)
			s.telemetry.observe(StageEmbedding, len(sub), time.Since(embedStarted))
			if embErr != nil {
				return fmt.Errorf("embedding %d documents (offset %d of %d): %w", len(sub), start, len(vecDocs), embErr)
			}
			sub = kept
			if len(dropped) > 0 {
				if droppedIDs == nil {
					droppedIDs = make(map[string]struct{}, len(dropped))
				}
				for id := range dropped {
					droppedIDs[id] = struct{}{}
				}
			}
		}
		if len(sub) == 0 {
			continue
		}
		commitStarted := time.Now()
		commitErr := s.current.collection.AddDocuments(ctx, strippedForCommit(sub), 1)
		s.telemetry.observe(StageChromemCommit, len(sub), time.Since(commitStarted))
		if commitErr != nil {
			return fmt.Errorf("adding %d documents (offset %d of %d): %w", len(sub), start, len(vecDocs), commitErr)
		}
	}
	// The sidecar records only after EVERY sub-batch commits: a hash written after a
	// partial commit would mark a half-indexed file up-to-date whose missing chunks
	// were never re-embedded (publish-after-commit rule, specs/domains/workspace.md).
	s.upsertFileHashesFiltered(vecDocs, droppedIDs)

	if s.current.lexical != nil && len(lexDocs) > 0 {
		if len(droppedIDs) > 0 {
			// Callers pass lexDocs mirroring vecDocs 1:1, so droppedIDs cannot outnumber
			// lexDocs — clamp anyway: a negative make capacity panics.
			filtered := make([]lexical.Doc, 0, max(len(lexDocs)-len(droppedIDs), 0))
			for _, d := range lexDocs {
				if _, drop := droppedIDs[d.ID]; drop {
					continue
				}
				filtered = append(filtered, d)
			}
			lexDocs = filtered
		}
		upsertStarted := time.Now()
		upsertErr := s.current.lexical.Upsert(ctx, lexDocs)
		s.telemetry.observe(StageBleveUpsert, len(lexDocs), time.Since(upsertStarted))
		if upsertErr != nil {
			s.logger.Warn("lexical upsert failed; will be repaired via RebuildLexical",
				"branch", s.current.currentBranch, "docs", len(lexDocs), "error", upsertErr)
		}
	}
	return nil
}

// embedSubBatch pre-populates Embedding for every document missing one (chromem then
// performs zero embedding inferences; already-embedded documents pass through
// untouched), embedding the texts in chunks of the configured batch capacity — one
// EmbedDocuments call per full batch inference, cancellable between chunks.
// Content-triggered failures are isolated, not pass-fatal: a failed chunk retries
// per-text (see embedChunkPerText) and only individually-failing texts are dropped
// (WARN) — one pathological chunk must not block its batch; a chunk failing entirely
// individually means a broken embedder and aborts the pass (isolation contract:
// specs/domains/workspace.md). Returns the commit set (input never reordered) and
// dropped IDs. Caller must hold s.mu (write lock).
func (s *Service) embedSubBatch(ctx context.Context, sub []chromem.Document) ([]chromem.Document, map[string]struct{}, error) {
	texts := make([]string, 0, len(sub))
	targets := make([]int, 0, len(sub))
	for i := range sub {
		if len(sub[i].Embedding) == 0 {
			targets = append(targets, i)
			texts = append(texts, normalizeEmbeddingChunk(sub[i].Content))
		}
	}

	failedTargets := make(map[int]struct{}) // indices into targets/texts dropped via the per-text fallback
	vecs, failed, err := s.resolveEmbeddingChunk(ctx, texts)
	if err != nil {
		return nil, nil, err
	}
	for i, vec := range vecs {
		if _, drop := failed[i]; drop {
			failedTargets[i] = struct{}{}
			continue
		}
		if len(vec) == 0 {
			return nil, nil, fmt.Errorf("batch embedder returned an empty vector (text %d of %d)", i, len(texts))
		}
		sub[targets[i]].Embedding = vec
	}

	if len(failedTargets) == 0 {
		return sub, nil, nil
	}
	dropped := make(map[string]struct{}, len(failedTargets))
	dropIdx := make(map[int]struct{}, len(failedTargets))
	for k := range failedTargets {
		docIdx := targets[k]
		dropIdx[docIdx] = struct{}{}
		dropped[sub[docIdx].ID] = struct{}{}
		s.logger.Warn("dropping document whose embedding failed",
			"id", sub[docIdx].ID,
			"file_path", sub[docIdx].Metadata["file_path"])
	}
	kept := make([]chromem.Document, 0, len(sub)-len(dropIdx))
	for i := range sub {
		if _, drop := dropIdx[i]; drop {
			continue
		}
		kept = append(kept, sub[i])
	}
	return kept, dropped, nil
}

// resolveEmbeddingChunk fills one ordered chunk from the persistent cache and
// embeds only unique misses. The returned failed map is keyed by chunk-local
// index so duplicate poisoned chunks are dropped consistently.
func (s *Service) resolveEmbeddingChunk(ctx context.Context, texts []string) (vecs [][]float32, failed map[int]struct{}, err error) {
	vecs = make([][]float32, len(texts))
	positions := make(map[string][]int, len(texts))
	uniqueTexts := make([]string, 0, len(texts))
	for i, text := range texts {
		if _, exists := positions[text]; !exists {
			uniqueTexts = append(uniqueTexts, text)
		}
		positions[text] = append(positions[text], i)
	}

	misses := make([]string, 0, len(uniqueTexts))
	for _, text := range uniqueTexts {
		if s.current.embeddingCache != nil {
			vec, ok := s.current.embeddingCache.get(text)
			s.telemetry.observeEmbeddingCache(ok)
			if ok {
				for _, pos := range positions[text] {
					vecs[pos] = vec
				}
				continue
			}
		}
		misses = append(misses, text)
	}
	if len(misses) == 0 {
		return vecs, nil, nil
	}

	missVecs := make([][]float32, len(misses))
	failedMisses := make(map[int]struct{})
	for start := 0; start < len(misses); start += s.embeddingBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("embedding documents (cancelled mid-batch): %w", err)
		}
		end := start + s.embeddingBatchSize
		if end > len(misses) {
			end = len(misses)
		}
		chunk := misses[start:end]
		chunkVecs, err := s.batchEmbedder.EmbedDocuments(ctx, chunk)
		chunkFailed := make(map[int]struct{})
		if err != nil {
			chunkVecs, err = s.embedChunkPerText(ctx, chunk, 0, len(chunk), chunkFailed)
			if err != nil {
				return nil, nil, err
			}
		}
		if len(chunkVecs) != len(chunk) {
			return nil, nil, fmt.Errorf("batch embedder returned %d vectors for %d unique texts", len(chunkVecs), len(chunk))
		}
		copy(missVecs[start:end], chunkVecs)
		for local := range chunkFailed {
			failedMisses[start+local] = struct{}{}
		}
	}

	failed = make(map[int]struct{})
	for missIdx, text := range misses {
		if _, drop := failedMisses[missIdx]; drop {
			for _, pos := range positions[text] {
				failed[pos] = struct{}{}
			}
			continue
		}
		vec := missVecs[missIdx]
		if len(vec) == 0 {
			return nil, nil, fmt.Errorf("batch embedder returned an empty vector for unique text %d", missIdx)
		}
		if s.embeddingDimension > 0 && len(vec) != s.embeddingDimension {
			return nil, nil, fmt.Errorf("batch embedder returned dimension %d, want %d", len(vec), s.embeddingDimension)
		}
		s.current.embeddingCache.put(text, vec)
		for _, pos := range positions[text] {
			vecs[pos] = vec
		}
	}
	s.current.embeddingCache.prune()
	if len(failed) == 0 {
		failed = nil
	}
	return vecs, failed, nil
}

// embedChunkPerText retries a failed chunk text-by-text; individual failures are
// WARN-logged and recorded in failedTargets (absolute indices into the sub-batch's
// texts slice). An error returns only when EVERY text fails — the broken-embedder
// signature that must abort the pass (specs/domains/workspace.md, failure isolation).
func (s *Service) embedChunkPerText(ctx context.Context, chunk []string, start, total int, failedTargets map[int]struct{}) ([][]float32, error) {
	vecs := make([][]float32, len(chunk))
	var firstErr error
	failures := 0
	for j, text := range chunk {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("embedding documents (cancelled mid-batch): %w", err)
		}
		v, err := s.batchEmbedder.EmbedDocuments(ctx, []string{text})
		if err != nil || len(v) != 1 || len(v[0]) == 0 {
			failures++
			textErr := err
			if textErr == nil {
				if len(v) != 1 {
					textErr = fmt.Errorf("embedder returned %d vectors for a single text", len(v))
				} else {
					textErr = errors.New("embedder returned an empty vector")
				}
			}
			if firstErr == nil {
				firstErr = textErr
			}
			s.logger.Warn("per-text embedding failed; document will be dropped",
				"chunk_offset", start+j, "total", total, "text_bytes", len(text), "error", textErr)
			failedTargets[start+j] = struct{}{}
			continue
		}
		vecs[j] = v[0]
	}
	if failures == len(chunk) {
		return nil, fmt.Errorf("embedding %d texts (chunk offset %d of %d): all texts failed individually too (first error: %w)",
			len(chunk), start, total, firstErr)
	}
	return vecs, nil
}

// DeleteDocumentsByIDs removes documents from chromem and best-effort from the
// lexical index (dual-write invariant). Caller must hold s.mu (write lock).
func (s *Service) DeleteDocumentsByIDs(ctx context.Context, ids []string) error {
	if s.current.collection == nil {
		return errors.New("no collection available")
	}
	if len(ids) == 0 {
		return nil
	}

	if err := s.current.collection.Delete(ctx, nil, nil, ids...); err != nil {
		return fmt.Errorf("deleting %d documents: %w", len(ids), err)
	}

	if s.current.lexical != nil {
		if err := s.current.lexical.Delete(ctx, ids); err != nil {
			s.logger.Warn("lexical delete failed; will be repaired via RebuildLexical",
				"branch", s.current.currentBranch, "ids", len(ids), "error", err)
		}
	}
	return nil
}

// DocumentID returns a deterministic document ID for a file path and chunk index.
func DocumentID(filePath string, chunkIndex int) string {
	h := sha256.Sum256([]byte(filePath))
	pathHash := hex.EncodeToString(h[:8]) // first 8 bytes = 16 hex chars
	return fmt.Sprintf("%s:%d", pathHash, chunkIndex)
}

// sidecarDocumentIDs derives document IDs arithmetically from committed chunk sets —
// no collection Query (which would materialize every document, vectors included);
// ok is false unless EVERY path has a known set (enumeration without materialization,
// specs/decisions/049-vector-index-memory-reduction.md). The set mirrors the collection:
// written only after commit, removed with the documents. Caller must hold at least
// s.mu.RLock().
func (s *Service) sidecarDocumentIDs(filePaths []string) ([]string, bool) {
	if s.current.fileHashes == nil || len(filePaths) == 0 {
		return nil, false
	}
	var ids []string
	for _, p := range filePaths {
		entry, exists := s.current.fileHashes[p]
		if !exists {
			return nil, false
		}
		indices, ok := fileHashEntryChunkSet(entry)
		if !ok {
			return nil, false
		}
		for _, i := range indices {
			ids = append(ids, DocumentID(p, i))
		}
	}
	return ids, true
}

// collectionUniqueFileCount reads the in-memory sidecar length directly (no copy
// needed under the lock); the rare nil-map case falls back to a query. Caller must
// hold at least s.mu.
func (s *Service) collectionUniqueFileCount() int {
	if s.current.fileHashes != nil {
		return len(s.current.fileHashes)
	}
	hashes, err := s.getCollectionFileHashes()
	if err != nil {
		return 0
	}
	return len(hashes)
}

// computeHash returns the SHA-256 hex digest of the content.
func computeHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}
