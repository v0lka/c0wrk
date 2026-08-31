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
	"strconv"
	"strings"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
	"github.com/v0lka/sp4rk/ignore"
)

// sanitizeRe matches characters that are not alphanumeric, hyphens, or underscores.
var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// embeddingSubBatchSize bounds the number of documents handed to chromem's
// AddDocuments in a single call. A batch is embedded end-to-end with ONNX
// inference and held in memory as a unit, so an unbounded call (e.g. a
// pathological file that chunked into tens of thousands of pieces) hangs
// the embedder and risks OOM. 200 keeps each invocation short enough to
// stay responsive to context cancellation and bounded in memory. Because
// the indexer flushes at addDocumentBatchSize (50) docs, sub-batching
// activates whenever a single AddDocuments call receives more than 200
// docs — i.e. routinely for any file producing more than ~150 chunks
// appended to a near-full batch, not only the pathological extremes.
const embeddingSubBatchSize = 200

// collectionName returns a deterministic, sanitized collection name for a branch.
func collectionName(branch string) string {
	sanitized := strings.ReplaceAll(branch, "/", "_")
	sanitized = sanitizeRe.ReplaceAllString(sanitized, "")
	if sanitized == "" {
		sanitized = "default"
	}
	return "branch_" + sanitized
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

	if s.db == nil {
		return errors.New("no database initialized; call SetProject first")
	}

	if branchName == s.currentBranch && s.collection != nil {
		return nil
	}

	// Persist the outgoing branch's in-memory hashes before they are
	// overwritten by loadFileHashes for the new branch.
	if s.currentBranch != "" && s.collection != nil {
		if err := s.saveFileHashes(); err != nil {
			s.logger.Warn("failed to persist file-hash sidecar on branch switch",
				"branch", s.currentBranch, "error", err)
		}
	}

	name := collectionName(branchName)
	col, err := s.db.GetOrCreateCollection(name, nil, s.embeddingFunc)
	if err != nil {
		return fmt.Errorf("getting or creating collection %q: %w", name, err)
	}

	// Close any previously-open lexical index and open the one for this
	// branch. The lexical index is persisted alongside the chromem DB under
	// the project's vector_index directory (set by SetProject).
	if s.lexical != nil {
		if closeErr := s.lexical.Close(); closeErr != nil {
			s.logger.Warn("failed to close previous lexical index", "error", closeErr)
		}
		s.lexical = nil
	}
	// The lexical index directory is derived from the chromem DB path
	// (stored in the database, which was opened from the project path).
	// If the DB is persistent, extract its directory to place the lexical
	// index alongside it; otherwise skip lexical persistence.
	if s.projectPath != "" && s.projectID != "" {
		lexDir := filepath.Join(s.projectPath, "lexical", lexicalBranchDirName(branchName))
		// Ensure the parent directory (…/{projectID}/lexical/) exists;
		// bleve's New() creates the leaf (branch) directory itself.
		if mkErr := os.MkdirAll(filepath.Dir(lexDir), 0o750); mkErr != nil {
			s.logger.Warn("failed to create lexical parent directory", "path", lexDir, "error", mkErr)
		} else {
			lex, lexErr := lexical.Open(lexDir)
			if lexErr != nil {
				s.logger.Warn("failed to open lexical index", "path", lexDir, "error", lexErr)
			} else {
				s.lexical = lex
			}
		}
	}

	s.collection = col
	s.currentBranch = branchName
	// Load (or migrate) the file-hash sidecar so ValidateCollection can compare
	// stored hashes against disk without an embedding-bearing collection Query.
	s.loadFileHashes()
	s.logger.Info("switched branch collection", "branch", branchName, "collection", name)
	return nil
}

// readFileFn is an os.ReadFile indirection used by ValidateCollection's slow
// path. It exists purely as a test seam: tests swap it for a counting wrapper
// to assert that the stat-based fast path skips content reads entirely for
// unchanged files. Production code must not reassign it.
var readFileFn = os.ReadFile

// fileHashEntrySep separates the components of a file-hash sidecar value.
const fileHashEntrySep = "|"

// fullHashRevalidationEvery bounds how many consecutive ValidateCollection
// passes may rely on the stat-based fast-path before one pass skips it and
// re-reads + re-hashes every file. This is the backstop that catches content
// rewrites preserving both size and mtime (see the fast-path comment in
// ValidateCollection). 20 keeps the amortized revalidation cost at ~5% of a
// pass while bounding the detection window to ~20 incremental passes.
const fullHashRevalidationEvery = 20

// parseFileHashEntry parses a file-hash sidecar value. New-format values are
// "hash|size|mtimeUnixNano" or "hash|size|mtimeUnixNano|chunkerFP", where
// size is the file size in bytes recorded at index time, mtimeUnixNano is
// the file's ModTime in Unix-nanoseconds recorded at index time, and the
// optional chunkerFP is the chunker-configuration fingerprint the file was
// chunked under (see ChunkerFingerprint; absent for intermediate-format
// entries written before the fingerprint existed — retrieve it separately
// via fileHashEntryChunkerFP). Legacy values are the bare content hash with
// no separators; for those (and for anything malformed) ok is false and the
// caller must fall back to the full read+hash comparison.
func parseFileHashEntry(entry string) (hash string, size, mtimeUnixNano int64, ok bool) {
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

// fileHashEntryChunkerFP returns the chunker-configuration fingerprint field
// of a sidecar entry, or "" when the entry has none (legacy bare hashes and
// intermediate "hash|size|mtime" entries written before the fingerprint
// existed). Callers treat "" as "configuration unknown — exempt from
// fingerprint-based staleness" rather than "default configuration".
func fileHashEntryChunkerFP(entry string) string {
	parts := strings.Split(entry, fileHashEntrySep)
	if len(parts) != 4 {
		return ""
	}
	return parts[3]
}

// fileHashEntryHash returns the content-hash component of a sidecar value
// regardless of whether the value is new-format or a legacy bare hash.
func fileHashEntryHash(entry string) string {
	if hash, _, _, ok := parseFileHashEntry(entry); ok {
		return hash
	}
	return entry
}

// ChunkerFingerprint returns a short stable fingerprint of the chunker
// configuration (max chunk size + overlap — the two ChunkerConfig inputs
// that determine how a file's content is split). It is embedded as the 4th
// field of new sidecar entries so ValidateCollection can detect files whose
// chunks were produced under a different chunking configuration
// (vector_index.chunk_overlap / max_chunk_size changes) and re-chunk them
// even though their content hash is unchanged.
func ChunkerFingerprint(maxChunkSize, overlap int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("v1|max=%d|overlap=%d", maxChunkSize, overlap)))
	return hex.EncodeToString(sum[:6]) // 12 hex chars, plenty for a config fingerprint
}

// fileHashEntryFromMetadata composes the sidecar value for a document.
// processFile stats every file it reads and records "file_size" and
// "file_mtime_unix_nano" metadata, so the preferred output is the new-format
// "hash|size|mtimeUnixNano|chunkerFP" entry that lets ValidateCollection
// classify the file as unchanged from stat alone (and detect chunker-config
// changes). chunkerFP is the active fingerprint the document was indexed
// under; pass "" for entries rebuilt from collection metadata whose chunker
// configuration is unknown (migration/fallback paths) — such entries are
// exempt from fingerprint-based staleness. When the size/mtime metadata is
// absent — legacy documents written before the upgrade, or hand-built
// documents — the legacy bare-hash value is returned; ValidateCollection
// still honors it via the full read+hash comparison, and the entry is
// upgraded to the new format the next time the file is indexed.
func fileHashEntryFromMetadata(md map[string]string, chunkerFP string) string {
	hash := md["content_hash"]
	size, mtime := md["file_size"], md["file_mtime_unix_nano"]
	if hash == "" || size == "" || mtime == "" {
		return hash
	}
	entry := hash + fileHashEntrySep + size + fileHashEntrySep + mtime
	if chunkerFP != "" {
		entry += fileHashEntrySep + chunkerFP
	}
	return entry
}

// ValidateCollection checks stored file hashes against current files on disk.
// It returns lists of stale (modified), new, and deleted file paths.
// workspacePath is the root directory to walk for source files. checker is the
// ignore resolver (over .gitignore + .aiignore) the caller already built for
// this workspace; ValidateCollection reuses it so its walk applies exactly the
// same filtering as the Indexer's IndexFull walk (preventing reindex loops).
func (s *Service) ValidateCollection(ctx context.Context, workspacePath string, checker ignore.IgnoreChecker) (staleFiles, newFiles, deletedFiles []string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.collection == nil {
		return nil, nil, nil, errors.New("no collection available; call SwitchBranch first")
	}

	// Get stored file hashes from the collection.
	storedHashes, err := s.getCollectionFileHashes()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("getting collection file hashes: %w", err)
	}

	// Periodic full-hash revalidation: the stat fast-path below trusts
	// size+mtime as a content proxy, so a rewrite that preserves both would
	// otherwise escape detection until the file next really changes. Every
	// fullHashRevalidationEvery-th validation pass skips the fast-path
	// entirely and re-reads + re-hashes every file, catching such rewrites
	// eventually. The counter is atomic because this function runs under the
	// read lock; validation passes are serialized per indexer in practice,
	// so concurrent calls could at worst force the revalidation twice.
	forceFullHash := s.validationsSinceFullHash.Add(1) >= fullHashRevalidationEvery
	if forceFullHash {
		s.validationsSinceFullHash.Store(0)
	}

	// Track which stored files we've seen on disk.
	seen := make(map[string]bool, len(storedHashes))

	// Walk workspace files using the same filtering as walkProjectFiles.
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

		// Size guard: skip oversized files before the full os.ReadFile. Like
		// binary/empty files they are never added to the collection, so
		// excluding them here avoids an infinite reindex loop on every pass.
		// The NUL-byte binary check below cannot recognize every binary format
		// (ONNX/safetensors protobufs have readable headers), so the size
		// limit is the reliable backstop against loading a multi-hundred-MB
		// asset into memory just to hash it. The limit is configurable via
		// vector_index.max_file_size (default 4 MiB).
		info, infoErr := d.Info()
		if infoErr == nil && tooLargeForIndex(info.Size(), s.maxFileSize) {
			return nil //nolint:nilerr // skip oversized files
		}

		// Fast path (stat-based unchanged detection): when the sidecar entry
		// is current-format ("hash|size|mtimeUnixNano|chunkerFP") and the
		// size and mtime recorded at index time still match the file's
		// stat, the content is HEURISTICALLY the same bytes that were
		// indexed — same length and same modification time almost always
		// mean an untouched file, but a rewrite that preserves both (an
		// archive extraction restoring archived mtimes, some file-sync
		// tools) escapes detection here. The periodic full-hash
		// revalidation above catches such a rewrite within
		// fullHashRevalidationEvery passes instead of letting it survive
		// until the file next changes. The entry's chunker fingerprint
		// must also match the active chunker configuration; on a config
		// change the file needs re-chunking and falls through. The file
		// necessarily passed the oversized/binary/empty guards back then,
		// so we can mark it seen WITHOUT the header pre-read, the full
		// os.ReadFile, or the SHA-256 pass. This is what keeps the
		// 1s-debounced incremental pass (triggered after every file edit)
		// from re-reading and re-hashing the entire workspace each time.
		// Legacy bare-hash entries, fingerprint-less intermediate entries,
		// any size/mtime mismatch, a failed d.Info, or a forced full-hash
		// pass fall through to the full-read comparison below, which also
		// upgrades the entry the next time the file is indexed.
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

		// Bounded header pre-read: reject multi-GB binary assets before the
		// full os.ReadFile. Binary and empty files are never added to the
		// collection by processFile, so reporting them as "new" every pass
		// would cause an infinite reindexing loop.
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

		currentHash := computeHash(content)

		if stored, exists := storedHashes[absPath]; exists {
			seen[absPath] = true
			if fileHashEntryHash(stored) != currentHash {
				staleFiles = append(staleFiles, absPath)
			} else if entryFP := fileHashEntryChunkerFP(stored); entryFP != "" && entryFP != s.chunkerFingerprint {
				// Content unchanged, but the stored chunks were produced
				// under a different chunker configuration (vector_index.
				// chunk_overlap / max_chunk_size changed since the file was
				// indexed): report stale so the file is re-chunked. Entries
				// without a fingerprint are exempt — their configuration is
				// unknown, and forcing a re-chunk of every legacy file in
				// one pass would cost a full re-embed for no user action.
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

	// Files in collection but not on disk are deleted.
	for storedPath := range storedHashes {
		if !seen[storedPath] {
			deletedFiles = append(deletedFiles, storedPath)
		}
	}

	return staleFiles, newFiles, deletedFiles, nil
}

// GetCollectionFiles returns all unique file paths and their sidecar entries
// (content hash, or "hash|size|mtimeUnixNano" for new-format entries) stored
// in the current collection.
func (s *Service) GetCollectionFiles() (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getCollectionFileHashes()
}

// getCollectionFileHashes returns the file→sidecar-entry map from the sidecar
// store, avoiding an embedding-bearing collection Query on every validation
// pass. Values are content hashes for legacy entries or
// "hash|size|mtimeUnixNano" for new-format entries (see parseFileHashEntry);
// callers that only need the hash should go through fileHashEntryHash.
// Caller must hold at least s.mu.RLock(). The returned map is a defensive
// copy: it may outlive the lock and must not race the in-place mutations done
// by upsertFileHashes/removeFileHashes under the write lock. The sidecar is
// loaded (or migrated) in SwitchBranch; if it is somehow nil, we fall back to
// a one-shot query.
func (s *Service) getCollectionFileHashes() (map[string]string, error) {
	if s.collection == nil {
		return nil, errors.New("no collection available")
	}
	if s.fileHashes != nil {
		out := make(map[string]string, len(s.fileHashes))
		for k, v := range s.fileHashes {
			out[k] = v
		}
		return out, nil
	}
	// Fallback (e.g. collection built before sidecar existed): enumerate via
	// Query. This pays an embedding cost, so loadFileHashes populates the
	// sidecar eagerly in SwitchBranch to keep this path cold.
	return s.queryCollectionFileHashes(context.Background())
}

// queryCollectionFileHashes enumerates stored file hashes directly from the
// chromem collection via a broad Query. This triggers an embedding and is used
// only for the one-time sidecar migration (or the rare fallback). Caller must
// hold at least s.mu.RLock(). ctx propagates cancellation to the underlying
// Query (e.g. service shutdown while the background migration is in flight).
func (s *Service) queryCollectionFileHashes(ctx context.Context) (map[string]string, error) {
	count := s.collection.Count()
	if count == 0 {
		return make(map[string]string), nil
	}

	results, err := s.collection.Query(ctx, " ", count, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("querying collection for file list: %w", err)
	}

	fileHashes := make(map[string]string, len(results))
	for _, r := range results {
		fp := r.Metadata["file_path"]
		if fp != "" {
			// Keep one hash per file (all chunks of a file share the same hash
			// and size/mtime metadata, so composing from any chunk is fine).
			// The chunker configuration these chunks were produced under is
			// not recoverable from metadata, so no fingerprint is attached:
			// the entry stays exempt from fingerprint-based staleness until
			// the file is next indexed.
			fileHashes[fp] = fileHashEntryFromMetadata(r.Metadata, "")
		}
	}

	return fileHashes, nil
}

// fileHashesPath returns the on-disk path of the sidecar for the current
// branch, or "" if project/branch is unset. Caller must hold s.mu.
func (s *Service) fileHashesPath() string {
	if s.projectPath == "" || s.currentBranch == "" {
		return ""
	}
	return filepath.Join(s.projectPath, "file_hashes_"+collectionName(s.currentBranch)+".json")
}

// loadFileHashes populates s.fileHashes for the current branch from the sidecar
// on disk. If the sidecar is absent, the backfill is deferred to a short-lived
// background goroutine (see migrateFileHashes) so SwitchBranch never pays the
// embedding cost of enumerating the collection synchronously — that cost used
// to block the whole service for the duration of one ONNX inference on the
// upgrade / first-switch path. Caller must hold s.mu (write).
func (s *Service) loadFileHashes() {
	// Cancel any in-flight migration left over from a previous branch and
	// reset its signal channel.
	if s.migrationCancel != nil {
		s.migrationCancel()
		s.migrationCancel = nil
	}

	// Fast path: a usable sidecar exists on disk.
	if path := s.fileHashesPath(); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			var m map[string]string
			if jsonErr := json.Unmarshal(data, &m); jsonErr == nil {
				s.fileHashes = m
				s.fileHashMigrationPending.Store(false)
				s.migrationCh = closedChan()
				return
			}
		}
	}

	// An empty collection has nothing to migrate; IndexFull will populate the
	// sidecar via upsertFileHashes, so start empty and settled.
	if s.collection == nil || s.collection.Count() == 0 {
		s.fileHashes = make(map[string]string)
		s.fileHashMigrationPending.Store(false)
		s.migrationCh = closedChan()
		return
	}

	// Non-empty collection with no sidecar (first run after the sidecar
	// upgrade, or a branch whose collection was built elsewhere). Defer the
	// single-embedding backfill to a background goroutine; IndexIncremental
	// waits on migrationCh before calling ValidateCollection, so the empty map
	// never causes a spurious full re-embed.
	s.fileHashes = make(map[string]string)
	s.fileHashMigrationPending.Store(true)
	done := make(chan struct{})
	s.migrationCh = done
	branch := s.currentBranch
	mctx, cancel := context.WithCancel(context.Background())
	s.migrationCancel = cancel
	s.migrationWG.Add(1)
	go s.migrateFileHashes(mctx, branch, done)
}

// migrateFileHashes is the background sidecar backfill: it enumerates the
// chromem collection once (a single embedding of the query vector) and adopts
// the result into s.fileHashes. branch is the branch being migrated; if the
// branch changes (or the service closes) before completion, the result is
// discarded. The caller must NOT hold s.mu. Closes done when settled.
func (s *Service) migrateFileHashes(ctx context.Context, branch string, done chan<- struct{}) {
	defer s.migrationWG.Done()
	defer close(done)

	s.mu.Lock()
	defer s.mu.Unlock()

	// The branch may have changed (or the service closed) while we waited for
	// the write lock; abandon a stale migration rather than overwriting another
	// branch's sidecar.
	if ctx.Err() != nil || s.currentBranch != branch || s.collection == nil {
		s.fileHashMigrationPending.Store(false)
		return
	}

	hashes, qErr := s.queryCollectionFileHashes(ctx)
	if qErr != nil {
		s.logger.Warn("failed to migrate file-hash sidecar from collection", "error", qErr)
		s.fileHashMigrationPending.Store(false)
		return
	}
	// Re-check after the embedding-bearing Query in case we raced a switch.
	if s.currentBranch != branch || s.collection == nil {
		s.fileHashMigrationPending.Store(false)
		return
	}
	s.fileHashes = hashes
	s.fileHashMigrationPending.Store(false)
	if err := s.saveFileHashes(); err != nil {
		s.logger.Warn("failed to persist file-hash sidecar after migration", "error", err)
	}
	s.logger.Info("file-hash sidecar migrated from collection", "branch", branch, "files", len(hashes))
}

// WaitFileHashMigration blocks until any background sidecar migration for the
// current branch has settled. IndexIncremental uses it so ValidateCollection
// sees the full hash map instead of re-embedding every file. The caller must
// NOT hold s.mu.
func (s *Service) WaitFileHashMigration(ctx context.Context) error {
	ch := func() chan struct{} {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.migrationCh
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

// saveFileHashes atomically writes the sidecar to disk. Caller must hold s.mu
// (write). Missing project/branch or a nil map is a no-op.
func (s *Service) saveFileHashes() error {
	path := s.fileHashesPath()
	if path == "" || s.fileHashes == nil {
		return nil
	}
	data, err := json.Marshal(s.fileHashes)
	if err != nil {
		return fmt.Errorf("marshaling file hashes: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing file-hash sidecar: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming file-hash sidecar: %w", err)
	}
	return nil
}

// upsertFileHashes records file_path→sidecar entry for the given documents
// into the in-memory sidecar. Entries are composed via
// fileHashEntryFromMetadata: new-format "hash|size|mtimeUnixNano" when the
// documents carry the size/mtime metadata processFile records, or the legacy
// bare hash otherwise (upgraded on the file's next index pass).
//
// It deliberately does NOT persist on every call: a
// single index pass issues one upsert per batch (addDocumentBatchSize docs),
// so persisting here would write the full map to disk N times per pass
// (O(batches × files) I/O). The map is flushed at lifecycle boundaries
// (SwitchBranch, SetProject, Rebuild, Close) and after migration. If the
// process crashes in between, the next SwitchBranch reads a slightly stale
// sidecar and ValidateCollection reconciles the diff against the persistent
// chromem collection. Caller must hold s.mu (write).
func (s *Service) upsertFileHashes(docs []chromem.Document) {
	if s.fileHashes == nil {
		s.fileHashes = make(map[string]string)
	}
	for _, d := range docs {
		if fp := d.Metadata["file_path"]; fp != "" {
			s.fileHashes[fp] = fileHashEntryFromMetadata(d.Metadata, s.chunkerFingerprint)
		}
	}
}

// removeFileHashes drops the given file paths from the in-memory sidecar. Like
// upsertFileHashes it does not persist per call; the map is flushed at lifecycle
// boundaries. Caller must hold s.mu (write).
func (s *Service) removeFileHashes(paths []string) {
	if s.fileHashes == nil || len(paths) == 0 {
		return
	}
	for _, p := range paths {
		delete(s.fileHashes, p)
	}
}

// RebuildCollection deletes the current branch collection and creates a fresh one.
// Caller must hold s.mu (write lock).
func (s *Service) RebuildCollection(ctx context.Context) error {
	if s.db == nil {
		return errors.New("no database initialized")
	}
	if s.currentBranch == "" {
		return errors.New("no branch set")
	}

	name := collectionName(s.currentBranch)

	if err := s.db.DeleteCollection(name); err != nil {
		s.logger.Warn("failed to delete collection during rebuild", "collection", name, "error", err)
	}

	col, err := s.db.GetOrCreateCollection(name, nil, s.embeddingFunc)
	if err != nil {
		return fmt.Errorf("creating fresh collection %q: %w", name, err)
	}
	s.collection = col
	// Reset the sidecar: a rebuilt collection is empty until re-indexed. Also
	// drop any in-flight migration: there is nothing left to backfill.
	if s.migrationCancel != nil {
		s.migrationCancel()
		s.migrationCancel = nil
	}
	s.fileHashes = make(map[string]string)
	s.fileHashMigrationPending.Store(false)
	s.migrationCh = closedChan()
	if err := s.saveFileHashes(); err != nil {
		s.logger.Warn("failed to persist file-hash sidecar after rebuild", "error", err)
	}
	s.logger.Info("rebuilt collection", "branch", s.currentBranch, "collection", name)
	return nil
}

// BatchEmbedder embeds a batch of text documents in a single (internally
// chunked) inference pass. It is implemented by sp4rk's embedding.Embedder
// (EmbedDocuments); ServiceConfig.BatchEmbedder wires it into AddDocuments so
// documents are embedded BEFORE the chromem commit, letting chromem-go skip
// its per-document embedding calls (v0.7.0 AddDocument treats a pre-populated
// Document.Embedding as final and only normalizes + persists it). Query-side
// embedding keeps going through the chromem EmbeddingFunc.
type BatchEmbedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// AddDocuments adds documents to the current collection and mirrors them
// to the per-branch lexical index. Chromem commits first; lexical errors
// are logged but not returned, since the reconciliation loop in the
// manager will repair drift via RebuildLexical on the next project open.
//
// Oversized vecDocs are embedded in fixed-size sub-batches (at most
// embeddingSubBatchSize each): a single chromem AddDocuments call
// processes its entire slice with onnx inference, so a runaway batch
// (tens of thousands of chunks from a pathological data file) would
// otherwise hang the embedder and exhaust memory. Sub-batching bounds
// the per-call work and lets ctx cancellation interrupt mid-batch.
//
// When a BatchEmbedder is configured, each sub-batch is embedded up-front
// (see embedSubBatch) in chunks of at most the configured embedding batch
// size, and the vectors are assigned to Document.Embedding before the
// chromem call — chromem then performs zero embedding inferences and only
// normalizes + persists. A nil BatchEmbedder keeps the legacy path where
// chromem embeds each document individually.
// Caller must hold s.mu (write lock).
func (s *Service) AddDocuments(ctx context.Context, vecDocs []chromem.Document, lexDocs []lexical.Doc) error {
	if s.collection == nil {
		return errors.New("no collection available")
	}
	if len(vecDocs) == 0 && len(lexDocs) == 0 {
		return nil
	}

	// droppedIDs accumulates documents that could not be embedded even via
	// the per-text fallback (see embedSubBatch). They are excluded from the
	// chromem commit AND from the lexical upsert below so the two indexes
	// never diverge.
	var droppedIDs map[string]struct{}

	for start := 0; start < len(vecDocs); start += embeddingSubBatchSize {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("adding documents (cancelled mid-batch): %w", err)
		}
		end := start + embeddingSubBatchSize
		if end > len(vecDocs) {
			end = len(vecDocs)
		}
		sub := vecDocs[start:end]
		if s.batchEmbedder != nil {
			kept, dropped, embErr := s.embedSubBatch(ctx, sub)
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
		if err := s.collection.AddDocuments(ctx, sub, 1); err != nil {
			return fmt.Errorf("adding %d documents (offset %d of %d): %w", len(sub), start, len(vecDocs), err)
		}
	}
	// Record the file-hash sidecar only after the full batch commits, not per
	// sub-batch: ValidateCollection reconciles a file solely by comparing its
	// on-disk hash to the sidecar hash (it never counts a file's chunks), so
	// recording the hash after a partial commit — some sub-batches succeeded
	// before a later one failed — would mark a half-indexed file as
	// up-to-date. Its still-missing chunks would never be re-embedded and the
	// file would be permanently under-indexed. Recording once, after every
	// sub-batch has committed, restores the original all-or-nothing semantics
	// while keeping embedding sub-batched. All chunks of a file share one
	// hash; upsert is idempotent.
	s.upsertFileHashes(vecDocs)

	if s.lexical != nil && len(lexDocs) > 0 {
		if len(droppedIDs) > 0 {
			// Clamp the capacity at zero: today's callers pass lexDocs
			// mirroring vecDocs 1:1 (same IDs), so droppedIDs can never
			// outnumber lexDocs — but a negative make capacity panics, and
			// the invariant is not enforced at this boundary.
			filtered := make([]lexical.Doc, 0, max(len(lexDocs)-len(droppedIDs), 0))
			for _, d := range lexDocs {
				if _, drop := droppedIDs[d.ID]; drop {
					continue
				}
				filtered = append(filtered, d)
			}
			lexDocs = filtered
		}
		if err := s.lexical.Upsert(ctx, lexDocs); err != nil {
			s.logger.Warn("lexical upsert failed; will be repaired via RebuildLexical",
				"branch", s.currentBranch, "docs", len(lexDocs), "error", err)
		}
	}
	return nil
}

// embedSubBatch pre-populates the Embedding field of every document in sub
// that does not already carry one, so the subsequent chromem AddDocuments
// call performs zero embedding inferences and only normalizes + persists
// (chromem-go v0.7.0 AddDocument skips c.embed for a non-empty
// Document.Embedding). Documents that already have an embedding are passed
// through untouched — exactly as chromem would treat them on the legacy
// path.
//
// The collected texts are embedded in chunks of at most the configured
// embedding batch size (EmbeddingBatchSize, mirroring the embedder's ONNX
// batch session capacity): each EmbedDocuments call maps to at most one
// full batch inference. ctx is checked per chunk and forwarded to
// EmbedDocuments, so cancellation interrupts the sub-batch between chunks
// (the embedder itself is ctx-aware and aborts a pending chunk).
//
// Content-triggered failures are isolated rather than aborting the whole
// index pass: when a chunk fails as a unit, every text is retried on its
// own (see embedChunkPerText) and only the texts that also fail
// individually are dropped from the returned document set (each logged at
// WARN). This keeps one pathological chunk — e.g. content that trips a
// tokenizer bug — from permanently blocking indexing of everything batched
// with it. Systemic failures (every text in a chunk failing individually)
// still return an error: a broken embedder must abort the pass, not
// silently drop the entire batch.
//
// Returns the documents to commit (sub itself when nothing was dropped;
// the input slice is never reordered) and the IDs of dropped documents so
// the caller can exclude them from the lexical upsert as well.
// Caller must hold s.mu (write lock).
func (s *Service) embedSubBatch(ctx context.Context, sub []chromem.Document) ([]chromem.Document, map[string]struct{}, error) {
	texts := make([]string, 0, len(sub))
	targets := make([]int, 0, len(sub))
	for i := range sub {
		if len(sub[i].Embedding) == 0 {
			targets = append(targets, i)
			texts = append(texts, sub[i].Content)
		}
	}

	failedTargets := make(map[int]struct{}) // indices into targets/texts dropped via the per-text fallback

	for start := 0; start < len(texts); start += s.embeddingBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("embedding documents (cancelled mid-batch): %w", err)
		}
		end := start + s.embeddingBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		chunk := texts[start:end]
		vecs, err := s.batchEmbedder.EmbedDocuments(ctx, chunk)
		if err != nil {
			vecs, err = s.embedChunkPerText(ctx, chunk, start, len(texts), failedTargets)
			if err != nil {
				return nil, nil, err
			}
		}
		if len(vecs) != len(chunk) {
			return nil, nil, fmt.Errorf("batch embedder returned %d vectors for %d texts", len(vecs), len(chunk))
		}
		for j, vec := range vecs {
			if _, drop := failedTargets[start+j]; drop {
				continue
			}
			if len(vec) == 0 {
				return nil, nil, fmt.Errorf("batch embedder returned an empty vector (chunk offset %d of %d, text %d)", start, len(texts), j)
			}
			sub[targets[start+j]].Embedding = vec
		}
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

// embedChunkPerText isolates failures inside a chunk whose batched
// EmbedDocuments call failed: each text is embedded individually, texts
// that also fail alone are logged at WARN and recorded in failedTargets
// (keyed by their absolute index in the sub-batch's texts slice), and the
// successfully embedded texts keep their vectors. It returns an error only
// when EVERY text fails individually — that signature means the embedder
// itself is broken (it fails identically for any input), in which case the
// whole index pass must abort rather than silently drop the batch.
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

// DeleteDocumentsByIDs removes documents with the given IDs from the current
// chromem collection and from the lexical index (best-effort).
// Caller must hold s.mu (write lock).
func (s *Service) DeleteDocumentsByIDs(ctx context.Context, ids []string) error {
	if s.collection == nil {
		return errors.New("no collection available")
	}
	if len(ids) == 0 {
		return nil
	}

	if err := s.collection.Delete(ctx, nil, nil, ids...); err != nil {
		return fmt.Errorf("deleting %d documents: %w", len(ids), err)
	}

	if s.lexical != nil {
		if err := s.lexical.Delete(ctx, ids); err != nil {
			s.logger.Warn("lexical delete failed; will be repaired via RebuildLexical",
				"branch", s.currentBranch, "ids", len(ids), "error", err)
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

// collectionUniqueFileCount returns the number of unique files in the collection.
// Caller must hold at least s.mu (read or write). It reads the in-memory map
// length directly (no defensive copy) since it stays under the lock; only the
// rare nil-map case falls back to a query.
func (s *Service) collectionUniqueFileCount() int {
	if s.fileHashes != nil {
		return len(s.fileHashes)
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
