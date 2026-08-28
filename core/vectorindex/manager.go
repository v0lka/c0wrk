package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/embedding"
)

// ErrEmbeddingDisabled is returned by NewManager when no EmbeddingFunc is
// provided, indicating vector search is intentionally not configured.
var ErrEmbeddingDisabled = errors.New("vector search disabled: no embedding function provided")

// Resource-consumption defaults for indexing/search. These are the historical
// hardcoded values, now exposed as config defaults (vector_index.* in
// config.yaml). backend/config/defaults.go references them so a config without
// a vector_index block resolves to exactly the pre-knob behaviour.
const (
	// DefaultDebounce is the watcher debounce window before one incremental
	// index pass runs (vector_index.debounce_ms). It coalesces bursts of
	// file-change events into a single pass.
	DefaultDebounce = 1 * time.Second

	// DefaultPrepWorkers is the default number of parallel file-preparation
	// workers in the indexing pipeline (vector_index.prep_workers): the
	// read/hash/chunk stage overlaps with embedding, while embedding itself
	// stays single-threaded under the service write lock.
	DefaultPrepWorkers = 2

	// DefaultSearchWaitTimeout is the default bound for how long search waits
	// for index readiness (vector_index.search_wait_timeout_ms). The explicit
	// 0 "fail fast" sentinel is applied only at the config layer, never here.
	DefaultSearchWaitTimeout = 3 * time.Second
)

// DefaultEmbeddingBatchSize mirrors sp4rk's embedding.DefaultBatchSize — the
// fixed row capacity of the embedder's persistent batch ONNX session
// (vector_index.embedding_batch_size). Assigned (not duplicated) so the two
// can never drift.
const DefaultEmbeddingBatchSize = embedding.DefaultBatchSize

// shutdownIndexGracePeriod is the maximum time Shutdown and SwitchProject wait
// for background init/indexing goroutines to exit after cancelling their
// contexts. Context cancellation normally unblocks them within milliseconds
// (the embedder is ctx-aware), but a goroutine stuck in non-interruptible work
// — most notably a synchronous chromem gob-decode (init) or ONNX inference call
// (indexing) that ignores ctx.Done() — cannot be interrupted. Rather than block
// app shutdown or a project switch forever (which would leave the user with no
// option but to force-kill the process), the caller waits for this period, then
// proceeds — in Shutdown it skips the blocking service.Close()/closeFn() and lets
// the OS reclaim resources; in SwitchProject the new init goroutine blocks on
// s.mu in the background until the old one releases it.
const shutdownIndexGracePeriod = 10 * time.Second

// ManagerConfig holds configuration for creating a Manager.
// The caller creates the embedder and passes EmbeddingFunc; ManagerConfig no longer
// depends on model paths, making the package usable with any embedding backend.
type ManagerConfig struct {
	EmbeddingFunc    chromem.EmbeddingFunc // Required: embedding function for vector storage
	BatchEmbedder    BatchEmbedder         // Optional: batched document embedding; nil = legacy per-doc path via chromem
	CloseFn          func() error          // Optional: called in Shutdown (e.g., embedder.Close)
	ChunkFn          ChunkFunc             // Optional: defaults to adapter over embedding.ChunkFile
	HashFn           HashFunc              // Optional: defaults to embedding.ComputeFileHash
	HybridConfig     HybridConfig          // RRF tuning + pre-fusion score thresholds (zero = defaults, thresholds off)
	MaxFileSize      int64                 // Optional: defaults to DefaultMaxIndexableFileSize (4 MiB)
	MaxChunkSize     int                   // Optional: defaults to DefaultMaxChunkSize (1500 chars)
	MaxChunksPerFile int                   // Optional: defaults to DefaultMaxChunksPerFile (4000)

	// EmbeddingBatchSize is the fixed row capacity of the embedder's batch
	// ONNX session (sp4rk embedding.EmbedderConfig.BatchSize). The embedder
	// itself is constructed by the caller (desktop startup), which sets the
	// same value there; the Manager forwards it to the Service, where it is
	// stored for the upcoming batched-embedding path. 0 (or unset) defaults
	// to DefaultEmbeddingBatchSize (32) — identical to the previous
	// behaviour, where sp4rk applied its own default.
	EmbeddingBatchSize int

	// PrepWorkers is the number of parallel file-preparation workers
	// (read/hash/chunk) in the indexing pipeline, forwarded to the
	// per-project IndexerConfig.PrepWorkers in initProject; 0 (or unset)
	// defaults to DefaultPrepWorkers (2). 1 reproduces the historical
	// strictly serial pipeline.
	PrepWorkers int

	// Debounce is how long file-change notifications wait before a single
	// incremental index pass runs, coalescing bursts of watcher events.
	// 0 (or unset) defaults to DefaultDebounce (1s, the historical value).
	Debounce time.Duration

	// ChunkOverlap is the character overlap between adjacent chunks, passed
	// into the per-project IndexerConfig.Overlap. 0 (or unset) defaults to
	// DefaultChunkOverlap (200, the historical value).
	ChunkOverlap int

	// SearchWaitTimeout bounds how long search callers may wait for the
	// index to become ready. Stored on the Manager for the search-path
	// wiring (a later task); 0 means "fail fast" — do not wait at all — so
	// no default is applied here. The config layer resolves an unset key to
	// DefaultSearchWaitTimeout (3s), so production wiring always carries an
	// explicit value.
	SearchWaitTimeout time.Duration

	Logger *slog.Logger
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
	// debounce is the configured watcher debounce window (ManagerConfig.Debounce
	// via vector_index.debounce_ms). Zero falls back to DefaultDebounce at arm
	// time (see effectiveDebounce) so zero-value Manager literals — used by
	// tests and older constructions — keep the historical 1s behaviour.
	debounce time.Duration
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
	chunkFn          ChunkFunc
	hashFn           HashFunc
	maxFileSize      int64
	maxChunkSize     int
	maxChunksPerFile int

	// chunkOverlap is the resolved character overlap between adjacent chunks
	// (ManagerConfig.ChunkOverlap via vector_index.chunk_overlap), passed
	// into the per-project IndexerConfig.Overlap in initProject.
	chunkOverlap int

	// embeddingBatchSize is the resolved embedder batch row capacity
	// (ManagerConfig.EmbeddingBatchSize via
	// vector_index.embedding_batch_size), forwarded to the Service (see
	// ServiceConfig.EmbeddingBatchSize).
	embeddingBatchSize int

	// prepWorkers is the resolved number of parallel file-preparation
	// workers in the indexing pipeline (ManagerConfig.PrepWorkers via
	// vector_index.prep_workers), forwarded to the per-project
	// IndexerConfig.PrepWorkers in initProject.
	prepWorkers int

	// searchWaitTimeout bounds how long search callers may wait for the
	// index to become ready (ManagerConfig.SearchWaitTimeout via
	// vector_index.search_wait_timeout_ms). Reserved: stored for the
	// search-path wiring (a later task). 0 means "fail fast".
	searchWaitTimeout time.Duration

	// indexingWG tracks ALL background indexing goroutines launched by the
	// Manager (the initProject-launched full/incremental pass and any
	// Reindex goroutine). These goroutines hold the service write lock during
	// AddDocuments, so Shutdown must wait for them before calling
	// service.Close() (which needs the same lock). See Shutdown.
	indexingWG sync.WaitGroup

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

	// shutdownGrace overrides shutdownIndexGracePeriod when non-zero, so
	// tests can verify the bounded-wait Shutdown logic without waiting the
	// full 10 s production default.
	shutdownGrace time.Duration
}

// NewManager creates a Manager from the provided EmbeddingFunc and config.
// Returns nil, ErrEmbeddingDisabled when EmbeddingFunc is nil (vector search disabled).
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.EmbeddingFunc == nil {
		return nil, ErrEmbeddingDisabled //nolint:nilnil // intentional: vector search is optional
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
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

	maxFileSize := cfg.MaxFileSize
	if maxFileSize <= 0 {
		maxFileSize = DefaultMaxIndexableFileSize
	}
	maxChunkSize := cfg.MaxChunkSize
	if maxChunkSize <= 0 {
		maxChunkSize = DefaultMaxChunkSize
	}
	maxChunksPerFile := cfg.MaxChunksPerFile
	if maxChunksPerFile <= 0 {
		maxChunksPerFile = DefaultMaxChunksPerFile
	}
	chunkOverlap := cfg.ChunkOverlap
	if chunkOverlap <= 0 {
		chunkOverlap = DefaultChunkOverlap
	}
	embeddingBatchSize := cfg.EmbeddingBatchSize
	if embeddingBatchSize <= 0 {
		embeddingBatchSize = DefaultEmbeddingBatchSize
	}
	prepWorkers := cfg.PrepWorkers
	if prepWorkers <= 0 {
		prepWorkers = DefaultPrepWorkers
	}
	// Debounce and SearchWaitTimeout are stored raw (no default applied
	// here): Debounce falls back to DefaultDebounce at arm time via
	// effectiveDebounce so zero-value Manager literals keep the historical
	// 1s behaviour, and SearchWaitTimeout 0 is the explicit "fail fast"
	// sentinel for the search-path wiring.

	// The chunker fingerprint is derived from the RESOLVED chunking
	// configuration (maxChunkSize + chunkOverlap), the exact values the
	// per-project Indexer chunks with, so sidecar entries record the
	// configuration that produced their chunks. When the config later
	// changes (vector_index.chunk_overlap / max_chunk_size), the
	// fingerprint no longer matches and ValidateCollection re-chunks the
	// affected files.
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:      cfg.EmbeddingFunc,
		BatchEmbedder:      cfg.BatchEmbedder,
		Logger:             logger,
		HybridConfig:       cfg.HybridConfig,
		MaxFileSize:        cfg.MaxFileSize,
		EmbeddingBatchSize: cfg.EmbeddingBatchSize,
		ChunkerFingerprint: ChunkerFingerprint(maxChunkSize, chunkOverlap),
	})
	if err != nil {
		return nil, err
	}

	return &Manager{
		service:            svc,
		logger:             logger,
		chunkFn:            chunkFn,
		hashFn:             hashFn,
		maxFileSize:        maxFileSize,
		maxChunkSize:       maxChunkSize,
		maxChunksPerFile:   maxChunksPerFile,
		chunkOverlap:       chunkOverlap,
		embeddingBatchSize: embeddingBatchSize,
		prepWorkers:        prepWorkers,
		debounce:           cfg.Debounce,
		searchWaitTimeout:  cfg.SearchWaitTimeout,
		closeFn:            cfg.CloseFn,
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
		// Cancel any in-flight async init from a prior CODE project and drain it
		// (bounded) before resetting the service, so it can't (re)set a
		// collection/db after we go in-memory below. Mirrors the CODE path's
		// single-flight teardown. The wait is bounded so a stuck init goroutine
		// (e.g. one blocked in the non-interruptible chromem gob-decode) cannot
		// hang the project-switch RPC forever; on timeout we proceed because
		// initCancel is already called, so the orphaned goroutine aborts at its
		// next ctx check. The s.mu lock it may still hold serializes naturally
		// with the SetProject call below, bounding the effective wait to the
		// actual DB work.
		m.mu.Lock()
		if m.initCancel != nil {
			m.initCancel()
			m.initCancel = nil
		}
		m.mu.Unlock()
		m.waitBounded(&m.initWG, m.initDrainGrace(), "init (no-project switch)")

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
	// drain it (bounded) before touching shared state, so only one init runs
	// at a time — no two inits can overlap and race on m.indexer /
	// m.gitMonitor / the service. In normal use the previous project's init
	// completed long ago (search became ready), so this returns immediately;
	// it only blocks during a rapid double-switch, and then for no longer
	// than the previous synchronous SwitchProject did (≈ one chromem open).
	// The wait is bounded so a stuck init goroutine (e.g. one blocked in the
	// non-interruptible chromem gob-decode) cannot hang the project-switch RPC
	// forever; on timeout the new init goroutine launched below blocks on s.mu
	// in the background until the old one releases it, keeping the UI free.
	m.mu.Lock()
	if m.initCancel != nil {
		m.initCancel()
		m.initCancel = nil
	}
	m.mu.Unlock()
	m.waitBounded(&m.initWG, m.initDrainGrace(), "init (project switch)")

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
		Service:          m.service,
		ChunkFn:          m.chunkFn,
		HashFn:           m.hashFn,
		MaxFileSize:      m.maxFileSize,
		MaxChunkSize:     m.maxChunkSize,
		MaxChunksPerFile: m.maxChunksPerFile,
		Overlap:          m.chunkOverlap,
		PrepWorkers:      m.prepWorkers,
		OnProgress:       m.wrapProgress(cbs.OnProgress),
		Logger:           m.logger,
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

	// Start background indexing. Tracked by indexingWG so Shutdown can wait
	// for it (bounded) before calling service.Close() — the goroutine holds
	// the service write lock during AddDocuments, which Close() also needs.
	m.indexingWG.Add(1)
	go func() {
		defer m.indexingWG.Done()
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

// scheduleIncrementalLocked arms the configured debounce
// (vector_index.debounce_ms, default 1s) that runs one incremental index
// pass. The fired callback (runIncrementalGuarded) re-reads the active
// indexer/workspace/context under m.mu, so a project switch between arming and
// firing is honored rather than indexing a stale workspace. The caller must
// hold m.debounceMu.
func (m *Manager) scheduleIncrementalLocked() {
	m.debounceTimer = time.AfterFunc(m.effectiveDebounce(), m.runIncrementalGuarded)
}

// effectiveDebounce returns the configured watcher debounce window, falling
// back to DefaultDebounce (1s, the historical hardcoded value) when it is
// zero. The fallback happens here — at arm time — rather than in NewManager so
// zero-value Manager literals (used by tests and older constructions) keep the
// historical behaviour.
func (m *Manager) effectiveDebounce() time.Duration {
	if m.debounce > 0 {
		return m.debounce
	}
	return DefaultDebounce
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
		m.debounceTimer = time.AfterFunc(m.effectiveDebounce(), m.runIncrementalGuarded)
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

// SearchWaitTimeout returns the configured bound for how long search callers
// may wait for index readiness (ManagerConfig.SearchWaitTimeout,
// vector_index.search_wait_timeout_ms). The raw value is returned: 0 is the
// explicit "fail fast" sentinel, so callers must special-case it rather than
// pass it to context.WithTimeout.
func (m *Manager) SearchWaitTimeout() time.Duration {
	return m.searchWaitTimeout
}

// NotReadyMessage renders an actionable message from an index-status snapshot
// for callers whose bounded wait for readiness expired. It includes the
// current state, progress percentage, and the file being indexed (when known)
// so the model can retry semantic_search later or fall back to ripgrep/glob.
func NotReadyMessage(st IndexStatus) string {
	state := string(st.State)
	if state == "" {
		state = "initializing"
	}
	var sb strings.Builder
	sb.WriteString("index not yet ready (")
	sb.WriteString(state)
	if st.TotalFiles > 0 {
		pct := st.FilesIndexed * 100 / st.TotalFiles
		sb.WriteString(", ")
		sb.WriteString(strconv.Itoa(pct))
		sb.WriteString("%")
	}
	if st.CurrentFile != "" {
		sb.WriteString(", file ")
		sb.WriteString(st.CurrentFile)
	}
	sb.WriteString("); retry semantic_search later or use ripgrep/glob")
	return sb.String()
}

// NotReadyError returns an error carrying the actionable not-ready message
// built from the current index status. Used by search call sites whose
// bounded wait (vector_index.search_wait_timeout_ms) expired, so the caller
// learns how far indexing got instead of a bare deadline error.
func (m *Manager) NotReadyError() error {
	return errors.New(NotReadyMessage(m.GetIndexStatus()))
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

	m.indexingWG.Add(1)
	go func() {
		defer m.indexingWG.Done()
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
//
// Both the async init goroutine and any background indexing goroutines are
// waited for with a bounded grace period (shutdownIndexGracePeriod). If either
// does not exit within the grace after its context is cancelled — e.g. it is
// stuck inside non-interruptible work such as chromem's gob-decode (init),
// os.ReadFile, or a synchronous ONNX inference call (indexing) — the blocking
// service.Close()/closeFn() are skipped. A stuck goroutine may hold the
// service write lock or the embedder mutex, which Close() needs, so waiting
// would hang shutdown forever. The OS reclaims the leaked resources when the
// process exits.
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

	m.stopDebounce()
	grace := m.initDrainGrace()

	// Wait for the async init goroutine, bounded by the grace period.
	// initProject launches the background indexing goroutine, so it must exit
	// first to know whether indexing was even started. initProject's opening
	// step — SetProject's gob-decode of every branch document into RAM — is
	// synchronous and not ctx-interruptible; an unbounded Wait would hang app
	// shutdown if it stalled on a large or corrupt DB. Bound it instead: if it
	// does not exit within the grace, skip the blocking Close()/closeFn().
	initClean := m.waitBounded(&m.initWG, grace, "init")

	// Only wait for indexing if init exited cleanly — otherwise indexing may
	// never have been launched, and a stuck init goroutine already holds the
	// service write lock that Close() needs.
	indexClean := initClean && m.waitBounded(&m.indexingWG, grace, "indexing")

	// Close resources only when both goroutine groups drained cleanly. A stuck
	// goroutine may still hold the service write lock (during AddDocuments) or
	// the embedder mutex (during synchronous ONNX inference), which
	// Close()/closeFn() need; waiting would deadlock. Let the OS reclaim the
	// leaked resources when the process exits.
	if indexClean {
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
	}
}

// initDrainGrace resolves the grace period for bounding an init/indexing
// goroutine drain. It returns the test-overridable m.shutdownGrace when set,
// falling back to the production default shutdownIndexGracePeriod. The same
// bound applies to Shutdown and to SwitchProject's single-flight init drain so
// a stuck init goroutine (blocked in the non-interruptible chromem gob-decode)
// cannot hang either path indefinitely.
func (m *Manager) initDrainGrace() time.Duration {
	if m.shutdownGrace != 0 {
		return m.shutdownGrace
	}
	return shutdownIndexGracePeriod
}

// waitBounded waits for wg to drain or for grace to elapse, whichever is first.
// It returns true if the waitgroup drained cleanly within the grace period, or
// false if it timed out — in which case the caller must skip any blocking
// cleanup that depends on those goroutines having exited. This bounds the
// drain for goroutines that may be stuck inside non-interruptible work
// (chromem gob-decode, os.ReadFile, or synchronous ONNX inference) so a single
// stuck goroutine can never hang app shutdown or a project switch.
func (m *Manager) waitBounded(wg *sync.WaitGroup, grace time.Duration, what string) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(grace):
		m.logger.Warn("vector goroutine did not exit within grace period; skipping clean close of vector resources",
			"what", what, "grace", grace)
		return false
	}
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
