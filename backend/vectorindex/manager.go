package vectorindex

import (
	"context"
	"log/slog"
	"sync"
	"time"

	chromem "github.com/philippgille/chromem-go"

	"github.com/user/agent/core"
)

// ManagerConfig holds configuration for creating a Manager.
// Fields are flattened so callers never need to import core/ or sdk/.
type ManagerConfig struct {
	ModelPath     string // ONNX model path
	TokenizerPath string // tokenizer.json path
	LibraryPath   string // libonnxruntime path
	MaxSeqLength  int    // default 512
	HiddenDim     int    // default 512 for jina-v2-small
	PersistPath   string // base path for vector storage
	Logger        *slog.Logger
}

// ProjectCallbacks holds callbacks for project-level indexing events.
type ProjectCallbacks struct {
	OnProgress ProgressCallback
}

// Manager owns the full lifecycle of vector indexing:
// embedder creation, service management, per-project indexing,
// git monitoring, and orderly shutdown.
type Manager struct {
	embedder core.Embedder
	service  *Service

	indexer     *Indexer
	gitMonitor  *GitMonitor
	indexCancel context.CancelFunc
	mu          sync.RWMutex

	debounceMu    sync.Mutex
	debounceTimer *time.Timer

	logger *slog.Logger
}

// NewManager creates embedder and service from flattened config.
// Returns nil, nil when all model paths are empty (vector search disabled).
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.ModelPath == "" || cfg.TokenizerPath == "" || cfg.LibraryPath == "" {
		return nil, nil //nolint:nilnil // intentional: vector search is optional
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	emb, err := core.NewEmbedder(core.EmbedderConfig{
		ModelPath:     cfg.ModelPath,
		TokenizerPath: cfg.TokenizerPath,
		LibraryPath:   cfg.LibraryPath,
		MaxSeqLength:  cfg.MaxSeqLength,
		HiddenDim:     cfg.HiddenDim,
		Logger:        logger,
	})
	if err != nil {
		return nil, err
	}

	svc, err := NewService(ServiceConfig{
		PersistPath:   cfg.PersistPath,
		EmbeddingFunc: chromem.EmbeddingFunc(emb.EmbeddingFunc()),
		Logger:        logger,
	})
	if err != nil {
		if closeErr := emb.Close(); closeErr != nil {
			logger.Warn("failed to close embedder after service init failure", "error", closeErr)
		}
		return nil, err
	}

	return &Manager{
		embedder: emb,
		service:  svc,
		logger:   logger,
	}, nil
}

// Service returns the underlying Service for search operations.
func (m *Manager) Service() *Service {
	return m.service
}

// DeleteProjectData removes the on-disk vector data for a project.
func (m *Manager) DeleteProjectData(projectID string) error {
	return m.service.DeleteProjectData(projectID)
}

// SwitchProject sets up vector indexing for the given project and workspace.
// It cancels any in-flight indexing, configures the service for the project,
// detects the git branch, creates an indexer, starts background indexing,
// and starts a git branch monitor.
func (m *Manager) SwitchProject(projectID, workspacePath string, cbs ProjectCallbacks) error {
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
	if err := m.service.SetProject(projectID); err != nil {
		return err
	}

	// Detect branch.
	branch, err := CurrentBranch(workspacePath)
	if err != nil {
		m.logger.Warn("failed to detect git branch for vector index", "error", err)
		branch = DefaultBranch
	}

	// Create chunker adapter: bridges core.ChunkFile to vectorindex.ChunkFunc.
	chunkFn := func(filePath string, content []byte, maxSize, overlap int) ([]ChunkResult, error) {
		chunks, chunkErr := core.ChunkFile(filePath, content, maxSize, overlap)
		if chunkErr != nil {
			return nil, chunkErr
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

	// Create indexer.
	indexer := NewIndexer(IndexerConfig{
		Service:    m.service,
		ChunkFn:    chunkFn,
		HashFn:     core.ComputeFileHash,
		OnProgress: cbs.OnProgress,
		Logger:     m.logger,
	})

	m.mu.Lock()
	m.indexer = indexer
	m.mu.Unlock()

	// Switch to branch collection.
	if switchErr := m.service.SwitchBranch(context.Background(), branch); switchErr != nil {
		m.logger.Warn("vector index branch switch failed", "error", switchErr)
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
		m.logger.Warn("failed to create git monitor", "error", monErr)
	} else {
		m.mu.Lock()
		m.gitMonitor = gitMon
		m.mu.Unlock()
		if startErr := gitMon.Start(); startErr != nil {
			m.logger.Warn("failed to start git monitor", "error", startErr)
		}
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

// Shutdown performs orderly cleanup: cancel indexing, stop monitor,
// close service, close embedder.
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
	if m.embedder != nil {
		if err := m.embedder.Close(); err != nil {
			m.logger.Error("failed to close embedder", "error", err)
		}
	}
}
