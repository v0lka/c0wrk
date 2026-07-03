package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/sdk/embedding"
)

// ManagerConfig holds configuration for creating a Manager.
// The caller creates the embedder and passes EmbeddingFunc; ManagerConfig no longer
// depends on model paths, making the package usable with any embedding backend.
type ManagerConfig struct {
	EmbeddingFunc    chromem.EmbeddingFunc // Required: embedding function for vector storage
	CloseFn          func() error          // Optional: called in Shutdown (e.g., embedder.Close)
	ChunkFn          ChunkFunc             // Optional: defaults to adapter over embedding.ChunkFile
	HashFn           HashFunc              // Optional: defaults to embedding.ComputeFileHash
	IgnoreDirs       []string              // user-configured dirs to skip (merged with defaults)
	IgnoreExtensions []string              // user-configured extensions to skip
	IgnoreFileNames  []string              // user-configured file names to skip
	Logger           *slog.Logger
}

// ProjectCallbacks holds callbacks for project-level indexing events.
type ProjectCallbacks struct {
	OnProgress ProgressCallback
}

// Manager owns the full lifecycle of vector indexing:
// service management, per-project indexing, git monitoring, and orderly shutdown.
// The embedder lifecycle is managed by the caller via ManagerConfig.CloseFn.
type Manager struct {
	service *Service

	indexer     *Indexer
	gitMonitor  *GitMonitor
	indexCancel context.CancelFunc
	mu          sync.RWMutex

	debounceMu    sync.Mutex
	debounceTimer *time.Timer

	logger *slog.Logger

	// Index status tracking for the frontend GetVectorIndexStatus API.
	statusMu     sync.RWMutex
	currentState IndexState
	currentPhase IndexPhase
	filesIndexed int
	totalFiles   int
	currentFile  string
	branch       string

	// Ignore patterns for file filtering.
	ignoreDirs       map[string]bool
	ignoreExtensions map[string]bool
	ignoreFileNames  map[string]bool

	// Chunk and hash functions for indexing.
	chunkFn ChunkFunc
	hashFn  HashFunc

	// WaitGroup for tracking in-flight reindex goroutines (vs Shutdown).
	reindexWG sync.WaitGroup

	// closeFn is called during Shutdown to release the embedder (if provided).
	closeFn func() error
}

// NewManager creates a Manager from the provided EmbeddingFunc and config.
// Returns nil, nil when EmbeddingFunc is nil (vector search disabled).
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.EmbeddingFunc == nil {
		return nil, nil //nolint:nilnil // intentional: vector search is optional
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	ignoreDirs := mergeMapWithSlice(defaultIgnoreDirs, cfg.IgnoreDirs)
	ignoreExts := buildMap(cfg.IgnoreExtensions)
	ignoreNames := buildMap(cfg.IgnoreFileNames)

	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:    cfg.EmbeddingFunc,
		Logger:           logger,
		IgnoreDirs:       ignoreDirs,
		IgnoreExtensions: ignoreExts,
		IgnoreFileNames:  ignoreNames,
	})
	if err != nil {
		return nil, err
	}

	// Resolve chunk and hash functions with defaults.
	chunkFn := cfg.ChunkFn
	if chunkFn == nil {
		chunkFn = defaultChunkFn
	}
	hashFn := cfg.HashFn
	if hashFn == nil {
		hashFn = embedding.ComputeFileHash
	}

	return &Manager{
		service:          svc,
		logger:           logger,
		ignoreDirs:       ignoreDirs,
		ignoreExtensions: ignoreExts,
		ignoreFileNames:  ignoreNames,
		chunkFn:          chunkFn,
		hashFn:           hashFn,
		closeFn:          cfg.CloseFn,
	}, nil
}

// buildMap returns a map from slice elements for O(1) lookup.
func buildMap(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, item := range items {
		m[item] = true
	}
	return m
}

// mergeMapWithSlice returns a new map containing all entries from base
// plus additional entries from the extra slice.
func mergeMapWithSlice(base map[string]bool, extra []string) map[string]bool {
	result := make(map[string]bool, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}
	for _, item := range extra {
		result[item] = true
	}
	return result
}

// Service returns the underlying Service for search operations.
func (m *Manager) Service() *Service {
	return m.service
}

// Searcher returns a narrow interface suitable for external consumers that only
// need search capabilities without access to Service internals (S-24).
func (m *Manager) Searcher() VectorSearcher {
	return m.service
}

// DeleteProjectData removes the on-disk vector data for a project.
func (m *Manager) DeleteProjectData(fullPath string) error {
	return m.service.DeleteProjectData(fullPath)
}

// SwitchProject sets up vector indexing for the given project and workspace.
// It cancels any in-flight indexing, configures the service for the project,
// detects the git branch, creates an indexer, starts background indexing,
// and starts a git branch monitor.
func (m *Manager) SwitchProject(projectID, workspacePath, vectorIndexFullPath string, cbs ProjectCallbacks) error {
	// Cancel previous indexing and any pending debounced incremental runs.
	m.mu.Lock()
	if m.indexCancel != nil {
		m.indexCancel()
		m.indexCancel = nil
	}
	if m.gitMonitor != nil {
		_ = m.gitMonitor.Stop()
		m.gitMonitor = nil
	}
	m.indexer = nil
	m.mu.Unlock()

	m.stopDebounce()

	// Set project on service.
	if err := m.service.SetProject(projectID, vectorIndexFullPath); err != nil {
		return err
	}

	// Detect branch. CurrentBranch returns DefaultBranch for non-git
	// directories; any other error (git misbehaving, disk corruption,
	// etc.) is a hard failure — git is a declared prerequisite, so we
	// refuse to silently paper over real problems.
	branch, err := CurrentBranch(context.Background(), workspacePath)
	if err != nil {
		return fmt.Errorf("detecting branch for vector index: %w", err)
	}

	// Create chunker using the resolved chunk function.
	chunkFn := m.chunkFn

	// Create indexer with a wrapped progress callback that also updates
	// the Manager's internal status for GetVectorIndexStatus.
	userOnProgress := cbs.OnProgress
	m.setStatus(map[string]any{"branch": branch})
	indexer := NewIndexer(IndexerConfig{
		Service:          m.service,
		ChunkFn:          chunkFn,
		HashFn:           m.hashFn,
		OnProgress:       m.wrapProgress(userOnProgress),
		Logger:           m.logger,
		IgnoreDirs:       m.ignoreDirs,
		IgnoreExtensions: m.ignoreExtensions,
		IgnoreFileNames:  m.ignoreFileNames,
	})

	m.mu.Lock()
	m.indexer = indexer
	m.mu.Unlock()

	// Switch to branch collection.
	if switchErr := m.service.SwitchBranch(context.Background(), branch); switchErr != nil {
		return fmt.Errorf("switching vector index branch: %w", switchErr)
	}

	// Start background indexing.
	indexCtx, indexCancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.indexCancel = indexCancel
	m.mu.Unlock()

	go func() {
		col := m.service.GetCollection()
		var idxErr error
		if col == nil || col.Count() == 0 {
			m.logger.Info("empty collection, running full index", "project", projectID)
			idxErr = indexer.IndexFull(indexCtx, workspacePath)
		} else {
			// Reconciliation: if the chromem collection has data but the
			// lexical index is empty (e.g. the project was indexed before
			// the BM25 upgrade, or a previous dual-write failed), backfill
			// the lexical side first so hybrid search works immediately.
			lexCount, lexCountErr := m.service.LexicalCount()
			if lexCountErr != nil {
				m.logger.Warn("failed to read lexical count; skipping reconciliation",
					"project", projectID, "error", lexCountErr)
			} else if lexCount == 0 && m.service.GetLexical() != nil {
				m.logger.Info("lexical index empty, running BM25 backfill",
					"project", projectID, "chunks", col.Count())
				if rbErr := indexer.RebuildLexical(indexCtx); rbErr != nil {
					if context.Cause(indexCtx) != nil {
						m.logger.Info("lexical backfill cancelled", "project", projectID)
					} else {
						m.logger.Warn("lexical backfill failed", "error", rbErr)
					}
				}
			}
			m.logger.Info("existing collection found, running incremental index", "project", projectID)
			idxErr = indexer.IndexIncremental(indexCtx, workspacePath)
		}
		if idxErr != nil {
			if context.Cause(indexCtx) != nil {
				m.logger.Info("vector indexing cancelled", "project", projectID)
			} else {
				m.logger.Warn("vector indexing failed", "error", idxErr)
			}
		}
	}()

	// Start git branch monitor.
	gitMon, monErr := NewGitMonitor(
		workspacePath,
		func(newBranch string) {
			go func() {
				if bsErr := indexer.HandleBranchSwitch(context.Background(), workspacePath, newBranch); bsErr != nil {
					m.logger.Warn("branch switch indexing failed", "error", bsErr)
				}
			}()
		},
		m.logger,
	)
	if monErr != nil {
		return fmt.Errorf("creating git monitor: %w", monErr)
	}
	m.mu.Lock()
	m.gitMonitor = gitMon
	m.mu.Unlock()
	if startErr := gitMon.Start(); startErr != nil {
		return fmt.Errorf("starting git monitor: %w", startErr)
	}

	return nil
}

// NotifyFileChange triggers debounced incremental indexing for the given workspace.
func (m *Manager) NotifyFileChange(workspacePath string) {
	m.mu.RLock()
	idx := m.indexer
	m.mu.RUnlock()

	if idx == nil {
		return
	}

	m.debounceMu.Lock()
	if m.debounceTimer != nil {
		m.debounceTimer.Stop()
	}
	m.debounceTimer = time.AfterFunc(1*time.Second, func() {
		if idxErr := idx.IndexIncremental(context.Background(), workspacePath); idxErr != nil {
			m.logger.Warn("incremental indexing failed", "error", idxErr)
		}
	})
	m.debounceMu.Unlock()
}

// CancelIndexing cancels any in-flight indexing operation and stops pending debounces.
func (m *Manager) CancelIndexing() {
	m.mu.Lock()
	if m.indexCancel != nil {
		m.indexCancel()
		m.indexCancel = nil
	}
	m.mu.Unlock()

	m.stopDebounce()
}

// stopDebounce stops any pending debounced incremental indexing.
func (m *Manager) stopDebounce() {
	m.debounceMu.Lock()
	if m.debounceTimer != nil {
		m.debounceTimer.Stop()
		m.debounceTimer = nil
	}
	m.debounceMu.Unlock()
}

// wrapProgress returns a ProgressCallback that updates the Manager's
// internal status and then calls the user-provided callback (if not nil).
func (m *Manager) wrapProgress(userFn ProgressCallback) ProgressCallback {
	return func(phase IndexPhase, state IndexState, filesIndexed, totalFiles int, currentFile string) {
		m.statusMu.Lock()
		m.currentPhase = phase
		m.currentState = state
		m.filesIndexed = filesIndexed
		m.totalFiles = totalFiles
		m.currentFile = currentFile
		m.statusMu.Unlock()

		if userFn != nil {
			userFn(phase, state, filesIndexed, totalFiles, currentFile)
		}
	}
}

// setStatus updates the Manager's internal index status from a map.
// Used for initial/reset values.
func (m *Manager) setStatus(vals map[string]any) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()

	if s, ok := vals["state"].(IndexState); ok {
		m.currentState = s
	}
	if p, ok := vals["phase"].(IndexPhase); ok {
		m.currentPhase = p
	}
	if n, ok := vals["filesIndexed"].(int); ok {
		m.filesIndexed = n
	}
	if n, ok := vals["totalFiles"].(int); ok {
		m.totalFiles = n
	}
	if f, ok := vals["currentFile"].(string); ok {
		m.currentFile = f
	}
	if b, ok := vals["branch"].(string); ok {
		m.branch = b
	}
}

// IndexStatus is a snapshot of the current indexing status.
type IndexStatus struct {
	State        IndexState
	Phase        IndexPhase
	FilesIndexed int
	TotalFiles   int
	CurrentFile  string
	Branch       string
}

// GetIndexStatus returns the current indexing status for the frontend.
func (m *Manager) GetIndexStatus() IndexStatus {
	m.statusMu.RLock()
	defer m.statusMu.RUnlock()
	return IndexStatus{
		State:        m.currentState,
		Phase:        m.currentPhase,
		FilesIndexed: m.filesIndexed,
		TotalFiles:   m.totalFiles,
		CurrentFile:  m.currentFile,
		Branch:       m.branch,
	}
}

// Reindex triggers a full rebuild of the vector index for the given
// workspace. The caller is responsible for passing the active workspace
// path. Returns an error if no indexer is currently configured.
func (m *Manager) Reindex(ctx context.Context, workspacePath string) error {
	m.mu.RLock()
	idx := m.indexer
	m.mu.RUnlock()

	if idx == nil {
		return errors.New("no indexer configured; open a project first")
	}

	// Cancel any in-flight indexing.
	m.mu.Lock()
	if m.indexCancel != nil {
		m.indexCancel()
		m.indexCancel = nil
	}
	m.mu.Unlock()

	m.stopDebounce()

	// Reset the collection and run a full index.
	indexCtx, indexCancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.indexCancel = indexCancel
	m.mu.Unlock()

	m.reindexWG.Add(1)
	go func() {
		defer m.reindexWG.Done()
		col := m.service.GetCollection()
		var idxErr error
		if col == nil || col.Count() == 0 {
			m.logger.Info("empty collection, running full index")
			idxErr = idx.IndexFull(indexCtx, workspacePath)
		} else {
			m.logger.Info("existing collection found, running incremental index")
			idxErr = idx.IndexIncremental(indexCtx, workspacePath)
		}
		if idxErr != nil {
			if context.Cause(indexCtx) != nil {
				m.logger.Info("vector indexing cancelled")
			} else {
				m.logger.Warn("vector indexing failed", "error", idxErr)
			}
		}
	}()

	return nil
}

// Shutdown performs orderly cleanup: cancel indexing, stop monitor,
// close service, and close the embedder via CloseFn (if provided).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	if m.indexCancel != nil {
		m.indexCancel()
		m.indexCancel = nil
	}
	if m.gitMonitor != nil {
		if err := m.gitMonitor.Stop(); err != nil {
			m.logger.Error("failed to stop git monitor", "error", err)
		}
		m.gitMonitor = nil
	}
	m.mu.Unlock()

	m.stopDebounce()

	if m.service != nil {
		if err := m.service.Close(); err != nil {
			m.logger.Error("failed to close vector service", "error", err)
		}
	}
	if m.closeFn != nil {
		if err := m.closeFn(); err != nil {
			m.logger.Error("failed to close embedder", "error", err)
		}
	}

	// Wait for in-flight reindex goroutine to finish before closing resources.
	m.reindexWG.Wait()
}

// defaultChunkFn adapts embedding.ChunkFile to the ChunkFunc signature.
func defaultChunkFn(filePath string, content []byte, maxChunkSize, overlap int) ([]ChunkResult, error) {
	chunks, err := embedding.ChunkFile(filePath, content, embedding.ChunkerConfig{
		MaxChunkSize: maxChunkSize,
		Overlap:      overlap,
	})
	if err != nil {
		return nil, err
	}
	results := make([]ChunkResult, len(chunks))
	for i, c := range chunks {
		results[i] = ChunkResult{
			Content:   c.Content,
			StartLine: c.StartLine,
			EndLine:   c.EndLine,
			Language:  c.Language,
		}
	}
	return results, nil
}
