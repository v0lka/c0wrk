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

	"github.com/bmatcuk/doublestar/v4"
	chromem "github.com/philippgille/chromem-go"

	"github.com/user/agent/backend/vectorindex/lexical"
)

// ServiceConfig holds configuration for creating a Service.
type ServiceConfig struct {
	// PersistPath is the base path for vector storage (e.g., ~/.c0wrk/vector_index/).
	// If empty, the database runs in-memory only.
	PersistPath string

	// EmbeddingFunc is the chromem-go compatible embedding function
	// (from Embedder.EmbeddingFunc()).
	EmbeddingFunc chromem.EmbeddingFunc

	// Ignore patterns for file filtering in ValidateCollection.
	// nil means no extra patterns beyond hardcoded defaults.
	IgnoreDirs       map[string]bool
	IgnoreExtensions map[string]bool
	IgnoreFileNames  map[string]bool

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
	persistPath   string
	projectID     string
	currentBranch string
	mu            sync.RWMutex
	ready         atomic.Bool
	readyCh       chan struct{} // closed when ready becomes true; recreated on false
	readyMu       sync.Mutex    // protects readyCh swaps
	logger        *slog.Logger

	// Extra ignore patterns for ValidateCollection file filtering.
	ignoreDirs       map[string]bool
	ignoreExtensions map[string]bool
	ignoreFileNames  map[string]bool
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
		embeddingFunc:    cfg.EmbeddingFunc,
		persistPath:      cfg.PersistPath,
		readyCh:          make(chan struct{}),
		logger:           logger,
		ignoreDirs:       cfg.IgnoreDirs,
		ignoreExtensions: cfg.IgnoreExtensions,
		ignoreFileNames:  cfg.IgnoreFileNames,
	}

	return s, nil
}

// SetProject switches to a project directory, creating a project-specific
// subdirectory for persistence and initializing the chromem-go DB.
func (s *Service) SetProject(projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.SetReady(false)

	// Drop references to the previous project so any lingering goroutines
	// can't modify the old collection after we've switched.
	s.collection = nil
	s.currentBranch = ""
	s.projectID = projectID
	s.db = nil
	if s.lexical != nil {
		if err := s.lexical.Close(); err != nil {
			s.logger.Warn("failed to close previous lexical index", "error", err)
		}
		s.lexical = nil
	}

	if s.persistPath != "" {
		projectPath := filepath.Join(s.persistPath, projectID)
		if err := os.MkdirAll(projectPath, 0o750); err != nil {
			return fmt.Errorf("creating project directory %s: %w", projectPath, err)
		}
		db, err := chromem.NewPersistentDB(projectPath, false)
		if err != nil {
			return fmt.Errorf("opening persistent DB at %s: %w", projectPath, err)
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
			matched, matchErr := doublestar.Match(fileFilter, sr.FilePath)
			if matchErr != nil {
				s.logger.Warn("invalid file filter pattern", "pattern", fileFilter, "error", matchErr)
				continue
			}
			if !matched {
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
func (s *Service) DeleteProjectData(projectID string) error {
	if s.persistPath == "" {
		return nil
	}
	projectPath := filepath.Join(s.persistPath, projectID)
	if err := os.RemoveAll(projectPath); err != nil {
		return fmt.Errorf("removing vector data for project %s: %w", projectID, err)
	}
	return nil
}

// Close cleans up resources.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.collection = nil
	s.db = nil
	if s.lexical != nil {
		if err := s.lexical.Close(); err != nil {
			s.logger.Warn("failed to close lexical index on service close", "error", err)
		}
		s.lexical = nil
	}
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
