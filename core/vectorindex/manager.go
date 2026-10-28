package vectorindex

import (
	"context"
	"errors"
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

// Resource-consumption defaults for indexing/search — the historical hardcoded
// values, now exposed as vector_index.* config defaults; backend/config/
// defaults.go references them, so a config without the block resolves to
// exactly the pre-knob behaviour.
const (
	// DefaultDebounce is the watcher debounce window before one incremental
	// pass runs (vector_index.debounce_ms), coalescing file-change bursts
	// into a single pass.
	DefaultDebounce = 1 * time.Second

	// DefaultPrepWorkers is the number of parallel file-preparation workers
	// (vector_index.prep_workers): read/hash/chunk overlaps with embedding,
	// which stays single-threaded under the service write lock.
	DefaultPrepWorkers = 2

	// DefaultSearchWaitTimeout is the default readiness-wait bound for search
	// (vector_index.search_wait_timeout_ms); the explicit 0 "fail fast"
	// sentinel is applied only at the config layer, never here.
	DefaultSearchWaitTimeout = 3 * time.Second

	// DefaultEmbeddingCacheMaxBytes caps each project's branch-independent
	// content-addressed embedding cache at 512 MiB.
	DefaultEmbeddingCacheMaxBytes int64 = 512 << 20

	// DefaultParkCapacity (vector_index.park_capacity): balances the memory
	// cost of resident parked states against the avoided chromem gob-decode
	// on the return path.
	DefaultParkCapacity = 3

	// DefaultParkBudgetBytes (vector_index.park_budget_mb): the summed
	// estimateStateBytes of the parked states is kept under this budget, so
	// RAM-rich machines can raise it alongside park_capacity; negative (the
	// config disable sentinel) leaves park_capacity as the only bound. See
	// specs/decisions/049-vector-index-memory-reduction.md.
	DefaultParkBudgetBytes int64 = 1024 << 20
)

// DefaultEmbeddingBatchSize mirrors sp4rk's embedding.DefaultBatchSize — the
// fixed row capacity of the embedder's persistent batch ONNX session
// (vector_index.embedding_batch_size). Assigned, not duplicated, so the two
// can never drift.
const DefaultEmbeddingBatchSize = embedding.DefaultBatchSize

// shutdownIndexGracePeriod bounds Shutdown's wait for background init/indexing
// goroutines after cancellation. A goroutine stuck in non-interruptible work
// (a synchronous chromem gob-decode or ONNX inference ignoring ctx.Done())
// cannot be interrupted, so Shutdown waits this period, then proceeds and
// skips the blocking service.Close()/closeFn(), letting the OS reclaim.
//
// SwitchProject deliberately never drains the init goroutine on this (or any)
// bound: it only cancels the previous init and relies on initProject's
// per-step ctx checks plus s.mu for serialization, so the project-switch RPC
// never blocks on a stuck init. See SwitchProject.
// See specs/domains/workspace.md (bounded shutdown drain).
const shutdownIndexGracePeriod = 10 * time.Second

// ManagerConfig holds configuration for creating a Manager. The caller creates
// the embedder and passes EmbeddingFunc; the config no longer depends on model
// paths, so the package works with any embedding backend.
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

	// ContentFilter controls deterministic early rejection of generated,
	// minified, and pathological files before chunking; nil selects
	// DefaultContentFilterConfig, non-nil is used exactly as supplied. The
	// resolved policy feeds the chunker fingerprint, so policy changes
	// invalidate sidecar entries like chunk-size changes.
	ContentFilter *ContentFilterConfig

	// IgnoreFiles selects which ignore files the indexing walks honour:
	// nil (the zero value) = both .gitignore and .aiignore; a non-nil slice
	// is the exact honoured set (".gitignore"/".aiignore"); an empty
	// non-nil slice honours neither file (vector_index.respect_gitignore /
	// respect_aiignore). Forwarded to the per-project IndexerConfig.
	IgnoreFiles []string

	// ExcludePatterns are subtractive gitignore-style patterns layered on
	// top of the honoured ignore files (vector_index.exclude_patterns);
	// a previously-indexed file a new pattern covers is dropped by the next
	// incremental pass. Forwarded to the per-project IndexerConfig.
	ExcludePatterns []string

	// IncludeSubmodules controls whether nested-repository directories
	// (a checked-out submodule: .git is a regular gitdir-pointer file) are
	// indexed (vector_index.index_submodules; default false). Forwarded to
	// the per-project IndexerConfig.
	IncludeSubmodules bool

	// EmbeddingBatchSize is the fixed row capacity of the embedder's batch
	// ONNX session (sp4rk embedding.EmbedderConfig.BatchSize); the embedder
	// is constructed by the caller (desktop startup) with the same value,
	// and the Manager forwards it to the Service. 0 →
	// DefaultEmbeddingBatchSize (32).
	EmbeddingBatchSize int

	// EmbeddingCacheFingerprint identifies model/tokenizer bytes and all
	// vector-producing parameters. Empty disables persistent embedding reuse.
	EmbeddingCacheFingerprint string
	// EmbeddingDimension is checked against every cached vector.
	EmbeddingDimension int
	// EmbeddingCacheMaxBytes is the per-project disk cap. Non-positive disables
	// the cache.
	EmbeddingCacheMaxBytes int64

	// PrepWorkers is the number of parallel file-preparation workers
	// (read/hash/chunk), forwarded to IndexerConfig.PrepWorkers in
	// initProject; 0 → DefaultPrepWorkers (2), 1 = the historical strictly
	// serial pipeline.
	PrepWorkers int

	// Debounce is how long file-change notifications wait before a single
	// incremental pass runs; 0 → DefaultDebounce (1s, the historical value).
	Debounce time.Duration

	// ChunkOverlap is the character overlap between adjacent chunks, passed
	// into IndexerConfig.Overlap; 0 → DefaultChunkOverlap (200, historical).
	ChunkOverlap int

	// SearchWaitTimeout bounds how long search callers may wait for index
	// readiness; 0 means "fail fast" — no default here, the config layer
	// resolves an unset key to DefaultSearchWaitTimeout, so production
	// wiring always carries an explicit value.
	SearchWaitTimeout time.Duration

	// ParkCapacity is the max number of recently-closed projects whose
	// vector-index state stays resident so returning skips the chromem
	// gob-decode (vector_index.park_capacity); 0/negative disables parking.
	// No default here — the config layer resolves unset → DefaultParkCapacity
	// (so zero means "disabled" for zero-value literals). See
	// specs/domains/workspace.md (park LRU).
	ParkCapacity int

	// ParkBudgetBytes is the cumulative byte budget of the park LRU
	// (vector_index.park_budget_mb → bytes): the effective bound is
	// min(ParkCapacity, ParkBudgetBytes); ≤0 (the config disable sentinel)
	// applies ParkCapacity alone. NO default here; forwarded verbatim to
	// ServiceConfig.ParkBudgetBytes. See
	// specs/decisions/049-vector-index-memory-reduction.md.
	ParkBudgetBytes int64

	Logger    *slog.Logger
	Telemetry *Telemetry
}

// ProjectCallbacks holds callbacks for project-level indexing events.
type ProjectCallbacks struct {
	OnProgress ProgressCallback
	// OnFailure is invoked when asynchronous project initialization fails on
	// an init-fatal step (DB open, branch detect, branch collection switch)
	// so the caller can surface the terminal "unavailable" status
	// (specs/domains/workspace.md, pre-open announcement). Other failures are
	// swallowed inside the init goroutine — SwitchProject has already
	// returned. NOT called for non-fatal git-monitor creation/start failures:
	// those leave search functional (only branch-switch re-indexing degrades).
	OnFailure func(err error)
}

// Manager owns the full vector-indexing lifecycle: service management,
// per-project indexing, git monitoring, orderly shutdown. The embedder
// lifecycle is the caller's, via ManagerConfig.CloseFn.
type Manager struct {
	service *Service

	indexer     *Indexer
	gitMonitor  *GitMonitor
	indexCancel context.CancelFunc
	indexCtx    context.Context
	mu          sync.RWMutex

	debounceMu    sync.Mutex
	debounceTimer *time.Timer
	// afterFunc is an immutable instance-local timer factory for tests; nil
	// uses time.AfterFunc. Set it before any file notifications or workers.
	afterFunc func(time.Duration, func()) *time.Timer
	// debounce is the configured watcher debounce window
	// (ManagerConfig.Debounce); zero falls back to DefaultDebounce at arm
	// time (effectiveDebounce), so zero-value Manager literals keep the
	// historical 1s behaviour.
	debounce time.Duration
	// indexing coalesces trailing incremental passes: embedding is already
	// serialized by the service write lock, so this flag's narrower job is
	// avoiding a redundant trailing pass — while a pass is in flight a
	// newly-armed debounce re-arms instead of launching a serial no-op,
	// coalescing late-arriving changes into a single final run.
	indexing atomic.Bool

	logger    *slog.Logger
	telemetry *Telemetry

	// Index status tracking for the frontend GetVectorIndexStatus API.
	statusMu     sync.RWMutex
	currentState IndexState
	currentPhase IndexPhase
	filesIndexed int
	totalFiles   int
	currentFile  string
	branch       string

	// workspacePath is the active project's workspace, stored during
	// SwitchProject so NotifyFileChange can trigger incremental indexing
	// without the caller supplying the path (the PostExecuteHook has no
	// access to it).
	workspacePath string

	// Chunk and hash functions for indexing.
	chunkFn          ChunkFunc
	hashFn           HashFunc
	maxFileSize      int64
	maxChunkSize     int
	maxChunksPerFile int
	contentFilter    ContentFilterConfig

	// ignoreFiles / excludePatterns / includeSubmodules are the resolved
	// ignore policy (ManagerConfig.IgnoreFiles / ExcludePatterns /
	// IncludeSubmodules via vector_index.respect_gitignore /
	// respect_aiignore / exclude_patterns / index_submodules), forwarded to
	// the per-project IndexerConfig in initProject.
	ignoreFiles       []string
	excludePatterns   []string
	includeSubmodules bool

	// chunkOverlap is the resolved character overlap between adjacent chunks,
	// passed into IndexerConfig.Overlap in initProject.
	chunkOverlap int

	// embeddingBatchSize is the resolved batch row capacity, forwarded to the
	// Service (see ServiceConfig.EmbeddingBatchSize).
	embeddingBatchSize int

	// prepWorkers is the resolved number of parallel file-preparation
	// workers, forwarded to IndexerConfig.PrepWorkers in initProject.
	prepWorkers int

	// searchWaitTimeout bounds how long search callers may wait for index
	// readiness; reserved for the search-path wiring. 0 means "fail fast".
	searchWaitTimeout time.Duration

	// indexingWG tracks ALL background indexing goroutines (the
	// initProject-launched pass and any Reindex/debounce/branch-switch
	// goroutine). They hold the service write lock during AddDocuments, so
	// Shutdown must join them before service.Close(). See Shutdown and
	// addIndexingWork for the Add-vs-Wait ordering rule.
	indexingWG sync.WaitGroup

	// initCancel cancels the in-flight async init (initProject); the next
	// SwitchProject / Shutdown cancels it so a stale init can't touch a
	// freshly-switched (or closed) service. Set under m.mu.
	initCancel context.CancelFunc
	// initWG tracks the initProject goroutine. Only Shutdown waits on it
	// (bounded), so the init goroutine never operates on a closed service.
	// SwitchProject cancels initCtx but never waits: initProject aborts at
	// its per-step ctx checks and s.mu serializes any residual overlap.
	initWG sync.WaitGroup

	// wgMu guards wgClosed and serializes every indexingWG.Add against
	// Shutdown's Wait: a positive Add concurrent with Wait is a fatal
	// sync.WaitGroup misuse, so late work registration goes through
	// addIndexingWork, refused once Shutdown sealed the manager — an Add
	// therefore either happens-before the Wait or never happens.
	// See specs/domains/workspace.md (indexing-work seal).
	wgMu sync.Mutex
	// wgClosed is set by Shutdown (under wgMu) before it drains indexingWG.
	wgClosed bool

	// closeFn is called during Shutdown to release the embedder (if provided).
	closeFn func() error

	// shutdownGrace overrides shutdownIndexGracePeriod when non-zero, so
	// tests verify the bounded-wait Shutdown logic without waiting the full
	// 10 s production default.
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
	// Debounce and SearchWaitTimeout are stored raw (no default here):
	// Debounce falls back at arm time (effectiveDebounce), SearchWaitTimeout
	// 0 is the explicit "fail fast" sentinel.

	// The chunker fingerprint derives from the RESOLVED chunking
	// configuration (maxChunkSize + chunkOverlap + resolved content-filter
	// policy) — the exact inputs the per-project Indexer chunks files with —
	// so a later config change (vector_index.chunk_overlap / max_chunk_size /
	// content_filter.*) invalidates sidecar entries and ValidateCollection
	// re-chunks the affected files.
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:             cfg.EmbeddingFunc,
		BatchEmbedder:             cfg.BatchEmbedder,
		Logger:                    logger,
		Telemetry:                 cfg.Telemetry,
		HybridConfig:              cfg.HybridConfig,
		MaxFileSize:               cfg.MaxFileSize,
		MaxChunkSize:              maxChunkSize,
		MaxChunksPerFile:          maxChunksPerFile,
		EmbeddingBatchSize:        cfg.EmbeddingBatchSize,
		EmbeddingCacheFingerprint: cfg.EmbeddingCacheFingerprint,
		EmbeddingDimension:        cfg.EmbeddingDimension,
		EmbeddingCacheMaxBytes:    cfg.EmbeddingCacheMaxBytes,
		ChunkerFingerprint:        ChunkerFingerprint(maxChunkSize, chunkOverlap, resolveContentFilterConfig(cfg.ContentFilter).Fingerprint()),
		ContentFilter:             cfg.ContentFilter,
		ParkCapacity:              cfg.ParkCapacity,
		ParkBudgetBytes:           cfg.ParkBudgetBytes,
	})
	if err != nil {
		return nil, err
	}

	return &Manager{
		service:            svc,
		logger:             logger,
		telemetry:          cfg.Telemetry,
		chunkFn:            chunkFn,
		hashFn:             hashFn,
		maxFileSize:        maxFileSize,
		maxChunkSize:       maxChunkSize,
		maxChunksPerFile:   maxChunksPerFile,
		contentFilter:      resolveContentFilterConfig(cfg.ContentFilter),
		chunkOverlap:       chunkOverlap,
		embeddingBatchSize: embeddingBatchSize,
		prepWorkers:        prepWorkers,
		debounce:           cfg.Debounce,
		searchWaitTimeout:  cfg.SearchWaitTimeout,
		closeFn:            cfg.CloseFn,
		ignoreFiles:        cfg.IgnoreFiles,
		excludePatterns:    cfg.ExcludePatterns,
		includeSubmodules:  cfg.IncludeSubmodules,
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
// For a CODE project the heavy initialization — storage layout, branch
// detect, the branch-scoped chromem DB open (ADR-064), indexer creation,
// background indexing, the git monitor — runs in a background goroutine
// (initProject); SwitchProject returns once the previous project is torn down
// and readiness is dropped, and search gates on WaitReady. For No Project
// (CHAT mode) the teardown + reset stays fully synchronous.
// See specs/domains/workspace.md (vector index lifecycle).
func (m *Manager) SwitchProject(projectID, workspacePath, vectorIndexFullPath string, cbs ProjectCallbacks, embeddingCachePaths ...string) error {
	// No Project (CHAT mode): the subsystem is fully disabled — tear down any
	// previous project's in-flight indexing and reset the service to an empty
	// in-memory state so stale documents cannot leak into search results or
	// RAG hints. No embedder, no persistent DB, no branch detect, no indexing
	// goroutine or git monitor.
	if projectID == core.NoProjectID {
		// Cancel any in-flight async init before resetting, so it can't
		// (re)set a collection/db after we go in-memory. We do NOT wait for
		// it to drain: the orphaned init aborts at its next ctx check, and
		// the s.mu it may hold serializes naturally with SetProject below —
		// a fresh No-Project switch returns immediately. Only Shutdown waits
		// on initWG (bounded).
		m.mu.Lock()
		if m.initCancel != nil {
			m.initCancel()
			m.initCancel = nil
		}
		m.mu.Unlock()

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
		// Drop readiness before the (possibly deferred) reset below so a search
		// issued in the interim never serves the outgoing project's collection.
		m.service.SetReady(false)
		// Non-blocking: the reset needs the service write lock, which an
		// in-flight branch-scoped DB open (ADR-064) holds for its entire
		// gob-decode — waiting here used to hold the backend's switchMu and
		// kill every CHAT/CODE toggle on its bounded acquire. See
		// ResetForNoProject.
		m.service.ResetForNoProject(projectID)
		return nil
	}

	// Displace any previous project: cancel its async init's context first —
	// the cancelled init aborts at its next ctx check. We deliberately do NOT
	// wait for it: draining would put the previous project's gob-decode back
	// on the switch RPC path. initCancel + per-step ctx checks serialize, and
	// residual exclusion comes from s.mu; only Shutdown waits on initWG.
	m.mu.Lock()
	if m.initCancel != nil {
		m.initCancel()
		m.initCancel = nil
	}
	m.mu.Unlock()

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

	// Drop readiness synchronously so a search RPC issued right after
	// SwitchProject returns blocks on WaitReady instead of racing the async
	// init; initProject (or its failure fallback) restores readiness.
	m.service.SetReady(false)

	// Launch project initialization off the RPC path: initProject opens the
	// persistent chromem DB (the dominant cost — gob-decoding the active
	// branch's documents), detects the branch, switches the branch
	// collection, creates the indexer, and starts background indexing + the
	// git monitor; search gates on WaitReady until init + indexing settle.
	//
	// Error semantics: init failures are logged here, not returned —
	// SwitchProject has already returned, and a failed init leaves search
	// returning clean "no collection" errors (deliberate softening).
	initCtx, initCancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.initCancel = initCancel
	m.mu.Unlock()
	var embeddingCachePath string
	if len(embeddingCachePaths) > 0 {
		embeddingCachePath = embeddingCachePaths[0]
	}
	m.initWG.Add(1)
	go m.initProject(initCtx, projectID, workspacePath, vectorIndexFullPath, embeddingCachePath, cbs)

	return nil
}

// initProject performs the asynchronous portion of SwitchProject for a CODE
// project: prepare the storage layout, detect the branch, open that branch's
// persistent chromem DB and collection, create the indexer, launch background
// indexing, start the git monitor — off the RPC path (ADR-064 decodes only
// the active branch). ctx is checked between steps so a follow-up
// SwitchProject / Shutdown aborts a stale init early. Failures are logged
// (not returned); readiness flips true so WaitReady callers unblock into a
// clear "no collection" error.
func (m *Manager) initProject(ctx context.Context, projectID, workspacePath, vectorIndexFullPath, embeddingCachePath string, cbs ProjectCallbacks) {
	defer m.initWG.Done()

	// INVARIANT: every mutating step below is preceded by a ctx check (or,
	// for a step that cannot be pre-empted once entered, immediately followed
	// by one), so an orphaned init bails out instead of clobbering the newer
	// project's state — this is what lets SwitchProject skip waiting on
	// initWG entirely. Publications additionally re-check ctx UNDER m.mu
	// (publishInitState, installGitMonitor): SwitchProject cancels
	// initCancel while holding m.mu, so check-then-publish is atomic with
	// the cancellation point.
	// See specs/domains/workspace.md (init publication protocol).
	if err := ctx.Err(); err != nil {
		m.logger.Info("vector index init cancelled before start", "project", projectID)
		return
	}

	// Announce the pre-open state BEFORE the open: until the first progress
	// callback the frontend has no status at all. ADR-064 made the open
	// branch-scoped (seconds, not minutes), but it is still not indexing —
	// the status is the distinct "loading/open" state, rendered as a plain
	// "Preparing index…", never a fake "Indexing" 0/0 bar; the manager's own
	// status agrees so GetVectorIndexStatus matches the event
	// (specs/domains/workspace.md, pre-open announcement).
	m.setStatus(map[string]any{"state": IndexStateLoading, "phase": PhaseOpen})
	if cbs.OnProgress != nil {
		cbs.OnProgress(PhaseOpen, IndexStateLoading, 0, 0, "")
	}

	// SetProject prepares the layout (and runs the one-time legacy
	// migration); SwitchBranch performs the branch-scoped open under the
	// write lock. Its outcome is checked against ctx immediately afterwards.
	if err := m.service.SetProject(projectID, vectorIndexFullPath, embeddingCachePath); err != nil {
		// A cancellation landing during SetProject belongs to an orphaned
		// init: a newer project owns readiness — do NOT flip it (that would
		// clear the new project's not-ready state and let a search race its
		// init).
		if cerr := ctx.Err(); cerr != nil {
			m.logger.Info("vector index init cancelled during open; ignoring open failure",
				"project", projectID, "error", err)
			return
		}
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
	// directories; git-on-PATH is verified synchronously by the backend
	// before SwitchProject, so a failure here indicates a broken repo.
	branch, err := CurrentBranch(ctx, workspacePath)
	if err != nil {
		// A cancellation landing during the git subprocess belongs to an
		// orphaned init and must not flip the newer project's readiness.
		if cerr := ctx.Err(); cerr != nil {
			m.logger.Info("vector index init cancelled during branch detect; ignoring failure",
				"project", projectID, "error", err)
			return
		}
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

	// Create the indexer with a wrapped progress callback that also updates
	// the Manager's status for GetVectorIndexStatus. It stays local until
	// SwitchBranch succeeds — only then is it published to m.indexer — so
	// NotifyFileChange / debounce / Reindex cannot arm a pass against a nil
	// collection. The goroutine and monitor below capture it via closure.
	m.setStatus(map[string]any{"branch": branch})
	indexer := NewIndexer(IndexerConfig{
		Service:           m.service,
		ChunkFn:           m.chunkFn,
		HashFn:            m.hashFn,
		MaxFileSize:       m.maxFileSize,
		MaxChunkSize:      m.maxChunkSize,
		MaxChunksPerFile:  m.maxChunksPerFile,
		ContentFilter:     &m.contentFilter,
		Overlap:           m.chunkOverlap,
		PrepWorkers:       m.prepWorkers,
		IgnoreFiles:       m.ignoreFiles,
		ExcludePatterns:   m.excludePatterns,
		IncludeSubmodules: m.includeSubmodules,
		OnProgress:        m.wrapProgress(cbs.OnProgress),
		Logger:            m.logger,
		Telemetry:         m.telemetry,
	})

	// Switch to branch collection (opens the per-branch bleve index).
	if switchErr := m.service.SwitchBranch(ctx, branch); switchErr != nil {
		// A cancellation landing while the bleve index opened belongs to an
		// orphaned init and must not flip the newer project's readiness.
		if cerr := ctx.Err(); cerr != nil {
			m.logger.Info("vector index init cancelled during branch switch; ignoring failure",
				"project", projectID, "error", switchErr)
			return
		}
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
	// collection is live. publishInitState re-checks ctx under m.mu, closing
	// the check-to-publish TOCTOU window; indexCancel stays owned by the
	// Manager (the next SwitchProject / Shutdown cancels through it).
	indexCtx, _, published := m.publishInitState(ctx, projectID, workspacePath, indexer)
	if !published {
		return
	}

	// Start background indexing; tracked by indexingWG so Shutdown can join
	// it (bounded) before service.Close().
	m.indexingWG.Add(1)
	go func() {
		defer m.indexingWG.Done()
		col := m.service.GetCollection()
		var idxErr error
		if col == nil || col.Count() == 0 {
			m.logger.Info("empty collection, running full index", "project", projectID)
			idxErr = indexer.IndexFull(indexCtx, workspacePath)
			// Return the transient whole-corpus allocations here instead of
			// waiting for the scavenger — success-gated: a cancelled or
			// failed pass allocated little and must not burn a
			// stop-the-world GC cycle. No locks held.
			if idxErr == nil {
				m.freeOSMemoryAfterFullIndex()
			}
		} else {
			// Reconciliation: collection has data but the lexical index is
			// empty (pre-BM25 project, or a failed dual-write) — backfill
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

	// Start the git branch monitor — the last mutating step, guarded by a ctx
	// check like the rest: an orphaned init must not install its monitor over
	// the new project's. NewGitMonitor allocates an fsnotify watcher + spawns
	// git after this check, so the publication re-checks under m.mu
	// (installGitMonitor).
	if err := ctx.Err(); err != nil {
		m.logger.Info("vector index init cancelled before git monitor", "project", projectID)
		return
	}
	gitMon, monErr := NewGitMonitor(
		workspacePath,
		func(newBranch string) {
			go m.handleBranchSwitch(indexCtx, indexer, workspacePath, newBranch)
		},
		m.logger,
	)
	if monErr != nil {
		m.logger.Warn("vector index init: failed to create git monitor",
			"project", projectID, "error", monErr)
		return
	}
	if !m.installGitMonitor(ctx, projectID, gitMon) {
		return
	}
	if startErr := gitMon.Start(); startErr != nil {
		m.logger.Warn("vector index init: failed to start git monitor",
			"project", projectID, "error", startErr)
		return
	}
}

// publishInitState installs the per-project indexer state (indexer, workspace
// path, background-indexing context), creating that context itself so the
// caller cannot leak it on the abort path. The publication re-checks ctx
// under m.mu, making it atomic with init cancellation — a cancel landing
// between initProject's unlocked check and this publication aborts without
// installing stale state. Returns the context, its cancel, and whether
// anything was installed.
func (m *Manager) publishInitState(ctx context.Context, projectID, workspacePath string, indexer *Indexer) (context.Context, context.CancelFunc, bool) {
	indexCtx, indexCancel := context.WithCancel(context.Background())
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		indexCancel()
		m.logger.Info("vector index init cancelled before publish", "project", projectID)
		return nil, nil, false
	}
	m.indexer = indexer
	m.workspacePath = workspacePath
	m.indexCancel = indexCancel
	m.indexCtx = indexCtx
	return indexCtx, indexCancel, true
}

// installGitMonitor publishes gitMon unless the init context was cancelled
// between the last unlocked check and this publication (same check-to-publish
// atomicity as publishInitState). An uninstalled monitor is stopped before
// returning — NewGitMonitor already allocated its fsnotify watcher, which
// would otherwise leak.
func (m *Manager) installGitMonitor(ctx context.Context, projectID string, gitMon *GitMonitor) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		_ = gitMon.Stop()
		m.logger.Info("vector index init cancelled before git monitor publish", "project", projectID)
		return false
	}
	m.gitMonitor = gitMon
	return true
}

// NotifyFileChange triggers debounced incremental indexing for the active
// project's workspace (stored during SwitchProject); no-op with no indexer
// configured (No Project / CHAT mode) or an empty workspace path.
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

// scheduleIncrementalLocked arms the debounce that runs one incremental pass;
// the fired callback (runIncrementalGuarded) re-reads the active indexer /
// workspace / context under m.mu, so a project switch between arming and
// firing is honored. The caller must hold m.debounceMu.
func (m *Manager) scheduleIncrementalLocked() {
	m.debounceTimer = m.armIncremental()
}

// armIncremental preserves the production timer while allowing in-process
// timing tests to control dispatch independently of filesystem indexing.
func (m *Manager) armIncremental() *time.Timer {
	if m.afterFunc != nil {
		return m.afterFunc(m.effectiveDebounce(), m.runIncrementalGuarded)
	}
	return time.AfterFunc(m.effectiveDebounce(), m.runIncrementalGuarded)
}

// effectiveDebounce returns the configured watcher debounce window, falling
// back to DefaultDebounce at arm time so zero-value Manager literals keep the
// historical 1s behaviour.
func (m *Manager) effectiveDebounce() time.Duration {
	if m.debounce > 0 {
		return m.debounce
	}
	return DefaultDebounce
}

// runIncrementalGuarded runs a single incremental pass unless one is already
// in flight (then it re-arms the debounce to coalesce trailing changes). On
// every fire it re-reads the active indexer, workspace, and index context
// under m.mu, so the pass is safe across SwitchProject/Reindex/CancelIndexing:
// cancelling the context aborts an in-flight pass instead of applying an old
// project's diff to a fresh collection, and a re-armed pass targets the
// current project. It does NOT serialize embedding — IndexIncremental holds
// the service write lock around AddDocuments; the guard's value is coalescing
// plus cancellation safety.
func (m *Manager) runIncrementalGuarded() {
	if m.indexing.Load() {
		// A pass is in flight: re-arm instead of a redundant trailing pass.
		m.debounceMu.Lock()
		m.debounceTimer = m.armIncremental()
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
	// A failed branch open left no collection: the pass would only re-publish
	// the "unavailable" state the failed open already announced — skip the
	// doomed pass; the next successful SwitchBranch reinstates and resumes
	// (specs/domains/workspace.md, failed-open handling).
	if m.service.GetCollection() == nil {
		m.logger.Info("incremental indexing skipped: no collection (index unavailable)")
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.indexing.Store(true)
	// The debounce-fired pass IS background indexing work: register it
	// (refusal-guarded) so Shutdown's bounded drain covers it — a pass can
	// fire just before the user quits, and a timer re-armed before being
	// stopped can fire late into shutdown.
	if !m.addIndexingWork() {
		m.indexing.Store(false)
		m.logger.Info("incremental indexing skipped: manager shutting down")
		return
	}
	if idxErr := idx.IndexIncremental(ctx, ws); idxErr != nil {
		m.logger.Warn("incremental indexing failed", "error", idxErr)
	}
	m.indexing.Store(false)
	m.indexingWG.Done()
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

// notifyInitFailure surfaces an async init failure to the caller (OnFailure)
// and records the terminal "unavailable" state so GetIndexStatus reflects
// that vector search is disabled for the active project, not a stale prior
// state. Called from initProject's init-fatal paths; the caller then flips
// readiness to true so WaitReady unblocks.
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

// SearchWaitTimeout returns the configured readiness-wait bound raw: 0 is the
// explicit "fail fast" sentinel, so callers must special-case it rather than
// pass it to context.WithTimeout.
func (m *Manager) SearchWaitTimeout() time.Duration {
	return m.searchWaitTimeout
}

// NotReadyMessage renders an actionable message from an index-status snapshot
// for callers whose bounded readiness wait expired — current state, progress,
// current file — so the model can retry semantic_search later or fall back
// to ripgrep/glob.
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
// from the current index status, for search call sites whose bounded wait
// expired — the caller learns how far indexing got, not a bare deadline error.
func (m *Manager) NotReadyError() error {
	return errors.New(NotReadyMessage(m.GetIndexStatus()))
}

// freeOSMemoryAfterFullIndex returns the transient allocations of a completed
// FULL pass to the OS via the service's immutable instance-local freeOSMemory
// dependency (runtime/debug.FreeOSMemory; tests inject it per service).
// Incremental passes deliberately do NOT call it: no spike to return, and a
// forced GC per debounce flush would burn CPU. Never under s.mu or m.mu —
// FreeOSMemory blocks for a full GC cycle.
func (m *Manager) freeOSMemoryAfterFullIndex() {
	m.service.freeOSMemory()
}

// handleBranchSwitch reacts to a branch change reported by the git monitor
// and returns the transient allocation spike of a completed FULL pass to the
// OS. The scavenge is gated on the switch actually running a full pass,
// matching the sibling call sites: the common switch (already-indexed target,
// or the same-branch no-op) allocates almost nothing, and an unconditional
// stop-the-world GC per branch hop would freeze the whole app (in-flight LLM
// streaming included) for no memory win.
func (m *Manager) handleBranchSwitch(ctx context.Context, indexer *Indexer, workspacePath, newBranch string) {
	// A stale monitor callback — a HEAD change debounced across a project
	// switch or shutdown — must not mutate the new project's index or touch
	// a closed service: ctx is the originating project's index context,
	// cancelled by the next SwitchProject/Shutdown.
	if err := ctx.Err(); err != nil {
		m.logger.Info("branch switch dropped: index context no longer live",
			"branch", newBranch, "cause", context.Cause(ctx))
		return
	}
	// Join the indexing work set (refused once Shutdown sealed it) so the
	// switch is covered by Shutdown's bounded drain.
	if !m.addIndexingWork() {
		m.logger.Info("branch switch dropped: manager shutting down", "branch", newBranch)
		return
	}
	defer m.indexingWG.Done()

	fullPass, bsErr := indexer.HandleBranchSwitch(ctx, workspacePath, newBranch)
	if bsErr != nil {
		m.logger.Warn("branch switch indexing failed", "error", bsErr)
		return
	}
	if fullPass {
		m.freeOSMemoryAfterFullIndex()
	}
}

// Reindex reconciles the active project's index against the workspace
// (only changed/new/deleted files; empty collection → full build). The
// workspace path is the one stored during SwitchProject and is read together
// with the indexer, so a pass always targets the project the indexer belongs
// to. Errors when no indexer is configured yet.
func (m *Manager) Reindex(ctx context.Context) error {
	m.mu.RLock()
	idx := m.indexer
	workspacePath := m.workspacePath
	m.mu.RUnlock()

	if idx == nil {
		return errors.New("no indexer configured; open a project first")
	}
	if workspacePath == "" {
		return errors.New("no active workspace; open a project first")
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

	// Start a fresh indexing pass: a full build when the collection is empty,
	// otherwise a validation-based reconcile of the whole workspace.
	indexCtx, indexCancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.indexCancel = indexCancel
	m.indexCtx = indexCtx
	m.mu.Unlock()

	// Refusal-guarded against a concurrent Shutdown's Wait (fatal WaitGroup
	// misuse); refusal means the manager is closing and no pass may start.
	if !m.addIndexingWork() {
		return errors.New("vector index manager is shutting down; reindex refused")
	}
	go func() {
		defer m.indexingWG.Done()
		col := m.service.GetCollection()
		if col == nil {
			// A failed branch open left nothing to build into — a successful
			// no-op instead of re-publishing the announced "unavailable"
			// state; the next successful SwitchBranch rebuilds
			// (specs/domains/workspace.md, failed-open handling).
			m.logger.Info("reindex skipped: no collection (index unavailable)")
			return
		}
		var idxErr error
		if col.Count() == 0 {
			m.logger.Info("empty collection, running full index")
			idxErr = idx.IndexFull(indexCtx, workspacePath)
			// Same rationale and success gate as the initProject pass.
			if idxErr == nil {
				m.freeOSMemoryAfterFullIndex()
			}
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

// Shutdown performs orderly cleanup: cancel indexing and async init, stop the
// monitor, close the service, close the embedder via CloseFn. Both goroutine
// groups are waited for with a bounded grace (shutdownIndexGracePeriod); a
// goroutine stuck in non-interruptible work (chromem gob-decode, os.ReadFile,
// synchronous ONNX inference) holds a lock Close needs, so on timeout the
// blocking Close()/closeFn() are skipped — the OS reclaims at process exit.
func (m *Manager) Shutdown() {
	// Cancel the async init first so it cannot touch a closed service;
	// initProject aborts after its current (non-interruptible) step.
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

	// Seal the indexing work registry BEFORE any Wait: a positive Add
	// concurrent with Wait is fatal, so every late registration (a Reindex
	// RPC, a debounce-fired pass, a mid-shutdown branch switch) is refused
	// from here on. The initProject pass's own Add needs no guard: the init
	// goroutine is joined via initWG first, so its Add happens-before this.
	m.sealIndexingWork()

	grace := m.initDrainGrace()

	// The init goroutine must exit first: initProject launches the background
	// indexing goroutine, so Shutdown must know whether indexing even started.
	// Its heavy step (the branch-scoped gob-decode, ADR-064) is not
	// ctx-interruptible; an unbounded Wait would hang app shutdown.
	initClean := m.waitBounded(&m.initWG, grace, "init")

	// Only wait for indexing if init exited cleanly — otherwise indexing may
	// never have been launched, and a stuck init goroutine already holds the
	// service write lock that Close() needs.
	indexClean := initClean && m.waitBounded(&m.indexingWG, grace, "indexing")

	// Close only when both groups drained cleanly — a stuck goroutine may
	// still hold the locks Close()/closeFn() need; the OS reclaims at exit.
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

// initDrainGrace resolves the drain grace: the test-overridable
// shutdownGrace when set, else shutdownIndexGracePeriod. SwitchProject never
// drains initWG, so a stuck init cannot hang a project switch either way.
func (m *Manager) initDrainGrace() time.Duration {
	if m.shutdownGrace != 0 {
		return m.shutdownGrace
	}
	return shutdownIndexGracePeriod
}

// addIndexingWork registers one background indexing goroutine for a late
// launcher (Reindex, a debounce-fired pass, a git-monitor branch switch). A
// false return means Shutdown already sealed the manager and the caller must
// NOT launch — that is what keeps every Add strictly ordered against
// Shutdown's Wait instead of racing it (a fatal sync.WaitGroup misuse).
func (m *Manager) addIndexingWork() bool {
	m.wgMu.Lock()
	defer m.wgMu.Unlock()
	if m.wgClosed {
		return false
	}
	m.indexingWG.Add(1)
	return true
}

// sealIndexingWork marks the manager closed for new indexing work; Shutdown
// must call it before draining indexingWG.
func (m *Manager) sealIndexingWork() {
	m.wgMu.Lock()
	m.wgClosed = true
	m.wgMu.Unlock()
}

// waitBounded waits for wg to drain or grace to elapse; false means the
// caller must skip blocking cleanup that depends on those goroutines having
// exited. This bounds the drain for goroutines stuck in non-interruptible
// work so a single stuck goroutine can never hang app shutdown.
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
