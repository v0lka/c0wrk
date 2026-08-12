package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
)

// ServiceConfig holds configuration for creating a Service.
type ServiceConfig struct {
	// EmbeddingFunc is the chromem-go compatible embedding function
	// (from Embedder.EmbeddingFunc()).
	EmbeddingFunc chromem.EmbeddingFunc

	// HybridConfig tunes Reciprocal Rank Fusion (RRF k, fanout, and
	// pre-fusion score thresholds). A zero value disables score
	// thresholds and uses built-in defaults for k/fanout; production
	// wiring sets thresholds via config.VectorIndexConfig.
	HybridConfig HybridConfig

	// Logger for structured logging.
	Logger *slog.Logger
}

// Service manages chromem-go collections with git-branch awareness,
// readiness state, and vector search capabilities. It also owns a
// per-branch bleve lexical index that is written in lock-step with the
// chromem collection.
type Service struct {
	db            *chromem.DB
	collection    *chromem.Collection
	lexical       lexical.Index
	embeddingFunc chromem.EmbeddingFunc
	projectID     string
	projectPath   string // full path to project vector storage (set by SetProject)
	currentBranch string
	// fileHashes is a sidecar store mapping file_path → content_hash, kept in
	// sync with the chromem collection. It lets ValidateCollection compare
	// stored hashes against disk WITHOUT querying the collection (which would
	// trigger an ONNX embedding via Query). Loaded per-branch in SwitchBranch
	// and flushed at lifecycle boundaries (not on every batch).
	fileHashes map[string]string
	// fileHashMigrationPending is true while a background sidecar backfill
	// (collection → file_hashes) is in flight for the current branch.
	fileHashMigrationPending atomic.Bool
	// migrationCh is closed when the current branch's sidecar has settled
	// (loaded, empty, or migrated). WaitFileHashMigration selects on it.
	// nil before the first SwitchBranch.
	migrationCh chan struct{}
	// migrationCancel cancels an in-flight migrateFileHashes goroutine
	// (e.g. on branch switch / project switch / close).
	migrationCancel context.CancelFunc
	// migrationWG lets Close/SetProject wait for the migration goroutine.
	migrationWG sync.WaitGroup
	mu          sync.RWMutex
	ready       atomic.Bool
	readyCh     chan struct{} // closed when ready becomes true; recreated on false
	readyMu     sync.Mutex    // protects readyCh swaps + readyGen
	// readyGen is bumped every time SetReady(false) / MarkNotReady is called.
	// An indexing pass captures the gen at start (via MarkNotReady) and
	// passes it to RestoreReady on exit; if a project switch (or any other
	// SetReady(false)) has intervened, the gen no longer matches and
	// RestoreReady is a no-op. This prevents a stale indexer from an
	// outgoing project — whose defer runs late after cancellation — from
	// prematurely marking a freshly-switched project's service as ready.
	readyGen int64
	logger   *slog.Logger

	// hybridConfig holds resolved RRF tuning + pre-fusion score
	// thresholds. Threshold fields of 0 mean "disabled".
	hybridConfig HybridConfig
}

// NewService creates a new vector index Service.
func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.EmbeddingFunc == nil {
		return nil, errors.New("EmbeddingFunc is required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	s := &Service{
		embeddingFunc: cfg.EmbeddingFunc,
		readyCh:       make(chan struct{}),
		logger:        logger,
		hybridConfig:  ResolveHybridConfig(cfg.HybridConfig),
	}

	return s, nil
}

// SetProject switches to a project directory, creating a project-specific
// subdirectory for persistence and initializing the chromem-go DB.
func (s *Service) SetProject(projectID, fullPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.SetReady(false)

	// Persist the outgoing project's in-memory hashes before discarding them,
	// and cancel any in-flight sidecar migration from the previous branch.
	// Skip the save while a background migration is pending: loadFileHashes
	// seeds s.fileHashes as an empty map during a migration, so persisting it
	// would write an empty sidecar for the outgoing branch — the next open of
	// that branch would then hit the (empty) sidecar fast path and re-embed
	// every file. Leaving it absent lets the migration re-trigger on next open.
	if s.fileHashes != nil && s.currentBranch != "" && !s.fileHashMigrationPending.Load() {
		if err := s.saveFileHashes(); err != nil {
			s.logger.Warn("failed to persist file-hash sidecar on project switch", "error", err)
		}
	}
	if s.migrationCancel != nil {
		s.migrationCancel()
		s.migrationCancel = nil
	}

	// Drop references to the previous project so any lingering goroutines
	// can't modify the old collection after we've switched.
	s.collection = nil
	s.currentBranch = ""
	s.fileHashes = nil
	s.migrationCh = nil
	s.projectID = projectID
	s.projectPath = fullPath
	s.db = nil
	if s.lexical != nil {
		if err := s.lexical.Close(); err != nil {
			s.logger.Warn("failed to close previous lexical index", "error", err)
		}
		s.lexical = nil
	}

	if fullPath != "" {
		if err := os.MkdirAll(fullPath, 0o750); err != nil {
			return fmt.Errorf("creating project directory %s: %w", fullPath, err)
		}
		db, err := chromem.NewPersistentDB(fullPath, false)
		if err != nil {
			return fmt.Errorf("opening persistent DB at %s: %w", fullPath, err)
		}
		s.db = db
	} else {
		s.db = chromem.NewDB()
	}

	s.logger.Info("project set for vector index", "projectID", projectID)
	return nil
}

// Browse returns up to topK chunks from the current collection without semantic
// ordering. It uses a space query to enumerate documents (the same approach used
// by getCollectionFileHashes). Blocks via WaitReady if the index is not yet ready.
func (s *Service) Browse(ctx context.Context, topK int) ([]SearchResult, error) {
	return s.BrowseWithFilter(ctx, topK, "")
}

// BrowseWithFilter returns up to topK chunks from the current collection without
// semantic ordering, optionally filtered by a file path glob pattern.
// Blocks via WaitReady if the index is not yet ready.
func (s *Service) BrowseWithFilter(ctx context.Context, topK int, fileFilter string) ([]SearchResult, error) {
	if err := s.WaitReady(ctx); err != nil {
		return nil, fmt.Errorf("waiting for index readiness: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.collection == nil {
		return nil, errors.New("no collection available; call SetProject and SwitchBranch first")
	}

	count := s.collection.Count()
	if count == 0 {
		return []SearchResult{}, nil
	}
	if topK > count {
		topK = count
	}

	results, err := s.collection.Query(ctx, " ", topK, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("browsing collection: %w", err)
	}

	out := make([]SearchResult, 0, len(results))
	for _, r := range results {
		sr := resultToSearchResult(r)

		if fileFilter != "" {
			if !matchFilePathPattern(fileFilter, sr.FilePath) {
				continue
			}
		}

		out = append(out, sr)
	}

	return out, nil
}

// Search queries the current collection for the top-K most similar results.
//
// This is a thin shim that delegates to HybridSearch with Mode=ModeVector
// so there is a single code path for glob/must-match filtering.
func (s *Service) Search(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	return s.HybridSearch(ctx, SearchOptions{Query: query, TopK: topK, Mode: ModeVector})
}

// SearchWithFilter queries the current collection with an optional file path
// glob filter. Blocks via WaitReady if the index is not yet ready.
//
// This is a thin shim that delegates to HybridSearch with Mode=ModeVector.
func (s *Service) SearchWithFilter(ctx context.Context, query string, topK int, filePattern string) ([]SearchResult, error) {
	return s.HybridSearch(ctx, SearchOptions{
		Query:       query,
		TopK:        topK,
		Mode:        ModeVector,
		FilePattern: filePattern,
	})
}

// IsReady returns whether the index is ready for queries.
func (s *Service) IsReady() bool {
	return s.ready.Load()
}

// WaitReady blocks until the service is ready or the context is cancelled.
func (s *Service) WaitReady(ctx context.Context) error {
	if s.ready.Load() {
		return nil
	}

	s.readyMu.Lock()
	ch := s.readyCh
	s.readyMu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("context cancelled while waiting for readiness: %w", ctx.Err())
	}
}

// AcquireWriteLock acquires an exclusive write lock for indexing operations.
func (s *Service) AcquireWriteLock() {
	s.mu.Lock()
}

// ReleaseWriteLock releases the exclusive write lock.
func (s *Service) ReleaseWriteLock() {
	s.mu.Unlock()
}

// SetReady updates the readiness state. When transitioning to true,
// all WaitReady callers are unblocked.
func (s *Service) SetReady(ready bool) {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()

	prev := s.ready.Swap(ready)

	if ready && !prev {
		// Transition false→true: close channel to wake all waiters.
		close(s.readyCh)
		s.logger.Info("vector index is ready")
	} else if !ready && prev {
		// Transition true→false: create a new channel for next wait cycle.
		s.readyCh = make(chan struct{})
		s.logger.Info("vector index set to not ready")
	}
	if !ready {
		// Always bump the generation on SetReady(false), even when the
		// state didn't transition, so a concurrent indexing pass that
		// captured an older gen won't falsely "restore" readiness via
		// RestoreReady after a project switch intervenes.
		s.readyGen++
	}
}

// MarkNotReady atomically sets ready=false and returns the current readiness
// generation. Indexing passes capture the returned gen and pass it to
// RestoreReady on exit; this pairs SetReady(false) with a gen capture in a
// single lock acquisition, avoiding the race where another goroutine calls
// SetReady(false) between the two operations.
func (s *Service) MarkNotReady() int64 {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if s.ready.Swap(false) {
		s.readyCh = make(chan struct{})
		s.logger.Info("vector index set to not ready")
	}
	s.readyGen++
	return s.readyGen
}

// RestoreReady conditionally marks the service ready ONLY if gen still
// matches the value returned by MarkNotReady at the start of the indexing
// pass. A project switch (or any other SetReady(false)) bumps the gen, so a
// stale indexer whose defer runs after the switch won't prematurely mark a
// freshly-switched (or freshly-closed) project as ready.
func (s *Service) RestoreReady(gen int64) {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if s.readyGen != gen {
		return
	}
	if !s.ready.Swap(true) {
		close(s.readyCh)
		s.logger.Info("vector index is ready")
	}
}

// GetDB returns the underlying chromem-go DB (for use by Indexer).
func (s *Service) GetDB() *chromem.DB {
	return s.db
}

// GetCollection returns the current collection (for use by Indexer).
func (s *Service) GetCollection() *chromem.Collection {
	return s.collection
}

// GetLexical returns the current lexical index (may be nil for in-memory
// mode or before a branch is opened).
func (s *Service) GetLexical() lexical.Index {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lexical
}

// LexicalCount returns the number of documents in the lexical index, or
// 0 if no lexical index is currently open.
func (s *Service) LexicalCount() (uint64, error) {
	s.mu.RLock()
	lex := s.lexical
	s.mu.RUnlock()
	if lex == nil {
		return 0, nil
	}
	return lex.Count()
}

// GetEmbeddingFunc returns the configured embedding function.
func (s *Service) GetEmbeddingFunc() chromem.EmbeddingFunc {
	return s.embeddingFunc
}

// CurrentBranchName returns the currently active branch name.
func (s *Service) CurrentBranchName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentBranch
}

// DeleteProjectData removes the on-disk vector data for a project.
// It is safe to call even if the project was never indexed.
func (s *Service) DeleteProjectData(fullPath string) error {
	if fullPath == "" {
		return nil
	}
	// If this service currently has the project open (its lexical index and
	// chromem DB point at a directory under fullPath), release those file
	// handles BEFORE removing the directory. Windows refuses to delete files
	// that still have open handles (unlinkat returns "process cannot access
	// the file"); Unix would silently unlink them and leave the service
	// holding stale, deleted inodes. Closing first makes both platforms
	// behave consistently and avoids dangling references to removed data.
	target := filepath.Clean(fullPath)
	s.mu.Lock()
	if filepath.Clean(s.projectPath) == target {
		if s.lexical != nil {
			if err := s.lexical.Close(); err != nil {
				s.logger.Warn("failed to close lexical index before deleting project data", "error", err)
			}
			s.lexical = nil
		}
		s.db = nil
		s.collection = nil
		s.fileHashes = nil
		s.currentBranch = ""
	}
	s.mu.Unlock()

	if err := os.RemoveAll(fullPath); err != nil {
		return fmt.Errorf("removing vector data for project: %w", err)
	}
	return nil
}

// Close cleans up resources.
func (s *Service) Close() error {
	s.mu.Lock()

	// Persist the current branch's in-memory hashes before shutdown.
	if s.fileHashes != nil && s.currentBranch != "" {
		if err := s.saveFileHashes(); err != nil {
			s.logger.Warn("failed to persist file-hash sidecar on close", "error", err)
		}
	}
	if s.migrationCancel != nil {
		s.migrationCancel()
		s.migrationCancel = nil
	}

	s.collection = nil
	s.db = nil
	s.fileHashes = nil
	s.migrationCh = nil
	if s.lexical != nil {
		if err := s.lexical.Close(); err != nil {
			s.logger.Warn("failed to close lexical index on service close", "error", err)
		}
		s.lexical = nil
	}
	s.mu.Unlock()

	// Wait for any in-flight migration goroutine to unwind (it aborts after
	// seeing the cancellation / nil collection above). Must happen AFTER
	// releasing the lock, or the goroutine would deadlock waiting for it.
	s.migrationWG.Wait()
	s.logger.Info("vector index service closed")
	return nil
}

// resultToSearchResult converts a chromem-go Result to a SearchResult.
func resultToSearchResult(r chromem.Result) SearchResult {
	startLine, _ := strconv.Atoi(r.Metadata["start_line"])
	endLine, _ := strconv.Atoi(r.Metadata["end_line"])

	return SearchResult{
		FilePath:  r.Metadata["file_path"],
		FileName:  r.Metadata["file_name"],
		Content:   r.Content,
		Score:     r.Similarity,
		StartLine: startLine,
		EndLine:   endLine,
		Language:  r.Metadata["language"],
	}
}
