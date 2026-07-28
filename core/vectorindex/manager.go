package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/embedding"
)

// ManagerConfig holds configuration for creating a Manager.
// The caller creates the embedder and passes EmbeddingFunc; ManagerConfig no longer
// depends on model paths, making the package usable with any embedding backend.
type ManagerConfig struct {
	EmbeddingFunc chromem.EmbeddingFunc // Required: embedding function for vector storage
	CloseFn       func() error          // Optional: called in Shutdown (e.g., embedder.Close)
	ChunkFn       ChunkFunc             // Optional: defaults to adapter over embedding.ChunkFile
	HashFn        HashFunc              // Optional: defaults to embedding.ComputeFileHash
	HybridConfig  HybridConfig          // RRF tuning + pre-fusion score thresholds (zero = defaults, thresholds off)
	Logger        *slog.Logger
}

// ProjectCallbacks holds callbacks for project-level indexing events.
type ProjectCallbacks struct {
	OnProgress ProgressCallback
	// OnFailure is invoked when asynchronous project initialization fails on
	// an init-fatal step (persistent DB open, branch detection, branch
	// collection switch), so the caller can surface a terminal status — e.g.
	// emit EventVectorIndexStatus{State: "unavailable"} — reflecting that
	// vector search is disabled for the active project.
	//
	// Failures are otherwise swallowed inside the init goroutine because
	// SwitchProject has already returned (init runs off the RPC path). It is
	// NOT called for the non-fatal git-monitor creation/start failures: those
	// leave search functional (only branch-switch re-indexing degrades).
	OnFailure func(err error)
}

// Manager owns the full lifecycle of vector indexing:
// service management, per-project indexing, git monitoring, and orderly shutdown.
// The embedder lifecycle is managed by the caller via ManagerConfig.CloseFn.
type Manager struct {
	service *Service

	indexer     *Indexer
	gitMonitor  *GitMonitor
	indexCancel context.CancelFunc
	indexCtx    context.Context
	mu          sync.RWMutex

	debounceMu    sync.Mutex
	debounceTimer *time.Timer
	// indexing coalesces trailing incremental passes. Embedding itself is
	// serialized by the service write lock (IndexIncremental → AddDocuments
	// holds s.mu), so two concurrent passes can never embed at once; this flag
	// has the narrower job of avoiding a redundant trailing pass: while a pass
	// is in flight, a newly-armed debounce re-arms itself instead of launching
	// a serial no-op pass once the in-flight one finishes, coalescing
	// late-arriving changes into a single final run.
	indexing atomic.Bool

	logger *slog.Logger

	// Index status tracking for the frontend GetVectorIndexStatus API.
	statusMu     sync.RWMutex
	currentState IndexState
	currentPhase IndexPhase
	filesIndexed int
	totalFiles   int
	currentFile  string
	branch       string

	// workspacePath is the active project's workspace, stored during
	// SwitchProject so that NotifyFileChange can trigger incremental
	// indexing without the caller needing to supply the path each time.
	// This enables the PostExecuteHook (which doesn't have access to the
	// workspace path) to request a re-index after file-mutating tools.
	workspacePath string

	// Chunk and hash functions for indexing.
	chunkFn ChunkFunc
	hashFn  HashFunc

	// WaitGroup for tracking in-flight reindex goroutines (vs Shutdown).
	reindexWG sync.WaitGroup

	// initCancel cancels the in-flight async project initialization
	// (initProject), which runs SetProject + SwitchBranch + indexer setup +
	// background indexing + git monitor off the SwitchProject RPC path.
	// The next SwitchProject / Shutdown cancels it so a stale init can't
	// touch a freshly-switched (or closed) service. Set under m.mu.
	initCancel context.CancelFunc
	// initWG tracks the initProject goroutine. Shutdown waits on it before
	// closing the service so the init goroutine never operates on a closed
	// service. initProject checks initCtx between each step and aborts early
	// when cancelled (e.g. by a rapid follow-up SwitchProject).
	initWG sync.WaitGroup

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

	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: cfg.EmbeddingFunc,
		Logger:        logger,
		HybridConfig:  cfg.HybridConfig,
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
		service: svc,
		logger:  logger,
		chunkFn: chunkFn,
		hashFn:  hashFn,
		closeFn: cfg.CloseFn,
	}, nil
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

// IsAnyIndexablePath reports whether at least one of changedPaths is indexable
// in the active project's workspace, reusing the indexer's cached .gitignore
// patterns so the workspace watcher does not re-read .gitignore on every
// debounce flush. Returns false when no project/indexer is configured (e.g.
// No Project / CHAT mode).
func (m *Manager) IsAnyIndexablePath(changedPaths []string) bool {
	m.mu.RLock()
	idx := m.indexer
	ws := m.workspacePath
	m.mu.RUnlock()

	if idx == nil || ws == "" {
		return false
	}
	return idx.IsAnyIndexablePath(changedPaths, ws)
}

// SwitchProject sets up vector indexing for the given project and workspace.
//
// For a CODE project the heavy initialization — opening the persistent chromem
// DB (which synchronously gob-decodes every document of every branch into
// RAM), detecting the branch, switching the branch collection, creating the
// indexer, launching background indexing, and starting the git branch monitor
// — runs in a background goroutine (see initProject) so it does not block the
// frontend's project-load waterfall. SwitchProject returns once the previous
// project is torn down and readiness is dropped; vector search then gates on
// WaitReady until init + indexing settle. For No Project (CHAT mode) the
// teardown + reset stays fully synchronous.
func (m *Manager) SwitchProject(projectID, workspacePath, vectorIndexFullPath string, cbs ProjectCallbacks) error {
	// No Project (CHAT mode): the vector index subsystem is fully disabled.
	// Tear down any previous project's in-flight indexing and reset the
	// service to an empty in-memory state so stale documents from a
	// previously-active CODE project cannot leak into search results or RAG
	// hints. No embedder is loaded, no persistent DB directory is created,
	// no git branch is detected, and no indexing goroutine or git monitor is
	// started.
	if projectID == core.NoProjectID {
		// Cancel any in-flight async init from a prior CODE project and wait
		// for that goroutine to exit before resetting the service, so it
		// can't (re)set a collection/db after we go in-memory below. Mirrors
		// the CODE path's single-flight teardown.
		m.mu.Lock()
		if m.initCancel != nil {
			m.initCancel()
			m.initCancel = nil
		}
		m.mu.Unlock()
		m.initWG.Wait()

		m.mu.Lock()
		if m.indexCancel != nil {
			m.indexCancel()
			m.indexCancel = nil
			m.indexCtx = nil
		}
		if m.gitMonitor != nil {
			_ = m.gitMonitor.Stop()
			m.gitMonitor = nil
		}
		m.indexer = nil
		m.workspacePath = ""
		m.mu.Unlock()
		m.stopDebounce()
		if err := m.service.SetProject(projectID, ""); err != nil {
			return fmt.Errorf("resetting vector index for No Project: %w", err)
		}
		return nil
	}

	// Displace any previous project. Cancel its async init's context and
	// WAIT for that init goroutine to fully exit before touching shared
	// state, so only one init runs at a time — no two inits can overlap and
	// race on m.indexer / m.gitMonitor / the service. In normal use the
	// previous project's init completed long ago (search became ready), so
	// this returns immediately; it only blocks during a rapid double-switch,
	// and then for no longer than the previous synchronous SwitchProject did
	// (≈ one chromem open).
	m.mu.Lock()
	if m.initCancel != nil {
		m.initCancel()
		m.initCancel = nil
	}
	m.mu.Unlock()
	m.initWG.Wait()

	m.mu.Lock()
	if m.indexCancel != nil {
		m.indexCancel()
		m.indexCancel = nil
		m.indexCtx = nil
	}
	if m.gitMonitor != nil {
		_ = m.gitMonitor.Stop()
		m.gitMonitor = nil
	}
	m.indexer = nil
	m.mu.Unlock()

	m.stopDebounce()

	// Drop the previous project's readiness synchronously so a search RPC
	// issued right after SwitchProject returns blocks on WaitReady instead of
	// racing the async init. initProject (or, on failure, its SetReady(true)
	// fallback) restores readiness.
	m.service.SetReady(false)

	// Launch project initialization off the RPC path. initProject opens the
	// persistent chromem DB (the dominant cost — gob-decoding every document
	// of every branch collection into RAM), then detects the branch, switches
	// the branch collection (bleve.Open), creates the indexer, launches a
	// background indexing goroutine, and starts the git branch monitor. None
	// of that needs to block the frontend's project-load waterfall: vector
	// search gates on WaitReady, which only unblocks once init + indexing
	// settle. The next SwitchProject / Shutdown cancels initCtx; the goroutine
	// aborts at its next ctx check.
	//
	// Error semantics: init failures (chromem open, branch detect, bleve
	// open) are logged here, not returned, because SwitchProject has already
	// returned. Vector indexing is an optional, asynchronously-loaded
	// subsystem; a failed init leaves search returning clean "no collection"
	// errors instead of blocking the whole project switch. This is a
	// deliberate softening of the prior hard-failure behavior.
	initCtx, initCancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.initCancel = initCancel
	m.mu.Unlock()
	m.initWG.Add(1)
	go m.initProject(initCtx, projectID, workspacePath, vectorIndexFullPath, cbs)

	return nil
}

// initProject performs the asynchronous portion of SwitchProject for a CODE
// project: open the persistent chromem DB, detect the git branch, switch the
// branch collection, create the indexer, launch background indexing, and start
// the git branch monitor. It runs off the SwitchProject RPC path so the
// frontend's project-load waterfall is not blocked by chromem's synchronous
// gob-decode of all branch documents. ctx is checked between steps so a
// follow-up SwitchProject / Shutdown (which cancels it) aborts a stale init
// early instead of doing needless work.
//
// Failures are logged (not returned): SwitchProject has already returned, and
// vector indexing is optional. On failure readiness is flipped to true so
// WaitReady callers unblock and search returns a clear "no collection" error
// instead of hanging.
func (m *Manager) initProject(ctx context.Context, projectID, workspacePath, vectorIndexFullPath string, cbs ProjectCallbacks) {
	defer m.initWG.Done()

	// SetProject loads the persistent chromem DB. This is the dominant cost
	// (gob-decoding every document of every branch collection into RAM) and
	// holds the service write lock for the duration.
	if err := m.service.SetProject(projectID, vectorIndexFullPath); err != nil {
		m.logger.Warn("vector index init failed; search disabled",
			"project", projectID, "step", "open persistent DB", "error", err)
		// Surface the soft failure so the backend can emit a terminal
		// "unavailable" status instead of leaving the UI on a stale state.
		m.notifyInitFailure(cbs, err)
		// Unblock WaitReady; queries then fail fast with "no collection".
		m.service.SetReady(true)
		return
	}
	if err := ctx.Err(); err != nil {
		m.logger.Info("vector index init cancelled after open", "project", projectID)
		return
	}

	// Detect branch. CurrentBranch returns DefaultBranch for non-git
	// directories; git-on-PATH is already verified synchronously by the
	// backend before SwitchProject is called, so a failure here indicates a
	// genuinely broken repo.
	branch, err := CurrentBranch(ctx, workspacePath)
	if err != nil {
		m.logger.Warn("vector index init failed; search disabled",
			"project", projectID, "step", "detect branch", "error", err)
		m.notifyInitFailure(cbs, err)
		m.service.SetReady(true)
		return
	}
	if err := ctx.Err(); err != nil {
		m.logger.Info("vector index init cancelled after branch detect", "project", projectID)
		return
	}

	// Create indexer with a wrapped progress callback that also updates
	// the Manager's internal status for GetVectorIndexStatus. The indexer is
	// kept in a local until SwitchBranch succeeds: only then is it published
	// to m.indexer. This prevents NotifyFileChange / debounce / Reindex from
	// arming an incremental pass against a service whose collection is still
	// nil (SwitchBranch opens it), which on every file change would otherwise
	// drive doomed passes logging "no collection available" until the next
	// SwitchProject. The background indexing goroutine and git-monitor
	// callback below capture this local via closure, so they are unaffected.
	m.setStatus(map[string]any{"branch": branch})
	indexer := NewIndexer(IndexerConfig{
		Service:    m.service,
		ChunkFn:    m.chunkFn,
		HashFn:     m.hashFn,
		OnProgress: m.wrapProgress(cbs.OnProgress),
		Logger:     m.logger,
	})

	// Switch to branch collection (opens the per-branch bleve index).
	if switchErr := m.service.SwitchBranch(ctx, branch); switchErr != nil {
		m.logger.Warn("vector index init failed; search disabled",
			"project", projectID, "step", "switch branch", "error", switchErr)
		m.notifyInitFailure(cbs, switchErr)
		m.service.SetReady(true)
		return
	}
	if err := ctx.Err(); err != nil {
		m.logger.Info("vector index init cancelled after branch switch", "project", projectID)
		return
	}

	// SwitchBranch succeeded: publish the indexer + workspace now that the
	// collection is live and incremental passes can do useful work.
	m.mu.Lock()
	m.indexer = indexer
	m.workspacePath = workspacePath
	m.mu.Unlock()

	// Start background indexing.
	indexCtx, indexCancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.indexCancel = indexCancel
	m.indexCtx = indexCtx
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
				if bsErr := indexer.HandleBranchSwitch(indexCtx, workspacePath, newBranch); bsErr != nil {
					m.logger.Warn("branch switch indexing failed", "error", bsErr)
				}
			}()
		},
		m.logger,
	)
	if monErr != nil {
		m.logger.Warn("vector index init: failed to create git monitor",
			"project", projectID, "error", monErr)
		return
	}
	m.mu.Lock()
	m.gitMonitor = gitMon
	m.mu.Unlock()
	if startErr := gitMon.Start(); startErr != nil {
		m.logger.Warn("vector index init: failed to start git monitor",
			"project", projectID, "error", startErr)
		return
	}
}

// NotifyFileChange triggers debounced incremental indexing for the active
// project's workspace. The workspace path is stored during SwitchProject, so
// callers (watcher callback, PostExecuteHook) don't need to supply it.
// No-op when no indexer is configured (e.g. No Project / CHAT mode) or when
// the workspace path is empty.
func (m *Manager) NotifyFileChange() {
	m.mu.RLock()
	idx := m.indexer
	ws := m.workspacePath
	m.mu.RUnlock()

	if idx == nil || ws == "" {
		return
	}

	m.debounceMu.Lock()
	if m.debounceTimer != nil {
		m.debounceTimer.Stop()
	}
	m.scheduleIncrementalLocked()
	m.debounceMu.Unlock()
}

// scheduleIncrementalLocked arms a 1s debounce that runs one incremental index
// pass. The fired callback (runIncrementalGuarded) re-reads the active
// indexer/workspace/context under m.mu, so a project switch between arming and
// firing is honored rather than indexing a stale workspace. The caller must
// hold m.debounceMu.
func (m *Manager) scheduleIncrementalLocked() {
	m.debounceTimer = time.AfterFunc(1*time.Second, m.runIncrementalGuarded)
}

// runIncrementalGuarded runs a single incremental pass unless one is already in
// flight, in which case it re-arms a debounce to coalesce trailing changes into
// one final run.
//
// On every fire it re-reads the active indexer, workspace path, and index
// context together under m.mu. This is what makes a debounce-fired pass safe
// across SwitchProject/Reindex/CancelIndexing: the context is the project's
// cancellable index context, so cancelling it (on project switch/shutdown)
// aborts an in-flight IndexIncremental at its next ctx.Err() check instead of
// letting a stale pass apply an old project's diff to a freshly-switched
// project's collection. Re-reading idx/ws (rather than capturing them in the
// timer closure) additionally ensures a re-armed trailing pass targets the
// current project, not the one active when the change arrived.
//
// Note: this guard does NOT provide embedding serialization on its own —
// IndexIncremental holds the service write lock around AddDocuments, which is
// what guarantees peak concurrency of 1. The guard's value is coalescing
// (fewer trailing passes) plus the cancellation safety above.
func (m *Manager) runIncrementalGuarded() {
	if m.indexing.Load() {
		// A pass is in flight: re-arm rather than launching a redundant
		// trailing pass that would find no new changes.
		m.debounceMu.Lock()
		m.debounceTimer = time.AfterFunc(1*time.Second, m.runIncrementalGuarded)
		m.debounceMu.Unlock()
		return
	}
	m.mu.Lock()
	idx := m.indexer
	ws := m.workspacePath
	ctx := m.indexCtx
	m.mu.Unlock()
	// No active project (e.g. switched to No Project / CHAT mode): nothing to
	// index. A nil indexCtx only happens before the first SwitchProject; fall
	// back to a non-cancellable context in that edge case.
	if idx == nil || ws == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.indexing.Store(true)
	if idxErr := idx.IndexIncremental(ctx, ws); idxErr != nil {
		m.logger.Warn("incremental indexing failed", "error", idxErr)
	}
	m.indexing.Store(false)
}

// CancelIndexing cancels any in-flight indexing operation and stops pending debounces.
func (m *Manager) CancelIndexing() {
	m.mu.Lock()
	if m.indexCancel != nil {
		m.indexCancel()
		m.indexCancel = nil
		m.indexCtx = nil
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

// notifyInitFailure surfaces an asynchronous init-project failure to the
// caller (when it registered OnFailure) and records a terminal "unavailable"
// state internally so GetIndexStatus reflects that vector search is disabled
// for the active project rather than a stale prior state. It is called from
// initProject's init-fatal paths (DB open / branch detect / branch switch);
// the caller then flips readiness to true so WaitReady unblocks.
func (m *Manager) notifyInitFailure(cbs ProjectCallbacks, err error) {
	m.statusMu.Lock()
	m.currentState = IndexStateUnavailable
	m.currentPhase = ""
	m.statusMu.Unlock()
	if cbs.OnFailure != nil {
		cbs.OnFailure(err)
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
		m.indexCtx = nil
	}
	m.mu.Unlock()

	m.stopDebounce()

	// Reset the collection and run a full index.
	indexCtx, indexCancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.indexCancel = indexCancel
	m.indexCtx = indexCtx
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

// Shutdown performs orderly cleanup: cancel indexing and async init, stop
// monitor, close service, and close the embedder via CloseFn (if provided).
func (m *Manager) Shutdown() {
	// Cancel the async init goroutine first and wait for it to exit before
	// closing the service, so it can't touch a closed service. initProject
	// checks ctx between steps and aborts after its current step (the chromem
	// open, which is not itself interruptible) completes.
	m.mu.Lock()
	if m.initCancel != nil {
		m.initCancel()
		m.initCancel = nil
	}
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

	m.initWG.Wait()
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
