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
	"strings"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
	"github.com/v0lka/sp4rk/ignore"
)

// sanitizeRe matches characters that are not alphanumeric, hyphens, or underscores.
var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

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

		content, readErr := os.ReadFile(absPath)
		if readErr != nil {
			return nil //nolint:nilerr // skip unreadable files
		}
		if len(content) == 0 {
			return nil //nolint:nilerr // skip empty files
		}

		currentHash := computeHash(content)

		if storedHash, exists := storedHashes[absPath]; exists {
			seen[absPath] = true
			if storedHash != currentHash {
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

// GetCollectionFiles returns all unique file paths and their content hashes
// stored in the current collection.
func (s *Service) GetCollectionFiles() (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getCollectionFileHashes()
}

// getCollectionFileHashes returns the file→hash map from the sidecar store,
// avoiding an embedding-bearing collection Query on every validation pass.
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
		hash := r.Metadata["content_hash"]
		if fp != "" {
			// Keep one hash per file (all chunks of a file share the same hash).
			fileHashes[fp] = hash
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

// upsertFileHashes records file_path→content_hash for the given documents into
// the in-memory sidecar. It deliberately does NOT persist on every call: a
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
			s.fileHashes[fp] = d.Metadata["content_hash"]
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

// AddDocuments adds documents to the current collection and mirrors them
// to the per-branch lexical index. Chromem commits first; lexical errors
// are logged but not returned, since the reconciliation loop in the
// manager will repair drift via RebuildLexical on the next project open.
// Caller must hold s.mu (write lock).
func (s *Service) AddDocuments(ctx context.Context, vecDocs []chromem.Document, lexDocs []lexical.Doc) error {
	if s.collection == nil {
		return errors.New("no collection available")
	}
	if len(vecDocs) == 0 && len(lexDocs) == 0 {
		return nil
	}

	if len(vecDocs) > 0 {
		if err := s.collection.AddDocuments(ctx, vecDocs, 1); err != nil {
			return fmt.Errorf("adding %d documents: %w", len(vecDocs), err)
		}
		// Keep the file-hash sidecar in sync so ValidateCollection never needs
		// to embed. All chunks of a file share one hash; upsert is idempotent.
		s.upsertFileHashes(vecDocs)
	}

	if s.lexical != nil && len(lexDocs) > 0 {
		if err := s.lexical.Upsert(ctx, lexDocs); err != nil {
			s.logger.Warn("lexical upsert failed; will be repaired via RebuildLexical",
				"branch", s.currentBranch, "docs", len(lexDocs), "error", err)
		}
	}
	return nil
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
