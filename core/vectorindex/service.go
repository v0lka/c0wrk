package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
	"github.com/v0lka/sp4rk/pathutil"
	"github.com/v0lka/sp4rk/safeio"
)

// ensureRealDir creates dir and any missing parents as REAL directories only:
// a dangling link, a non-directory component, or a link swapped into a
// component being created fails closed instead of being followed (final
// component included); a pre-existing operator-symlinked tree resolves as
// intent. A planted link must not redirect the storage root outside ~/.c0wrk.
// See specs/domains/workspace.md (storage roots are real directories only).
func ensureRealDir(dir string, perm fs.FileMode) error {
	return safeio.MkdirAllReal(dir, perm)
}

// newPersistentDB is a package-variable test seam (production must never
// reassign it): tests swap it to assert a project restored from the park LRU
// does NOT reopen (and re-gob-decode) its persistent DB.
var newPersistentDB = chromem.NewPersistentDB

// estimateStateBytes approximates a parked state's resident memory: one
// embedding vector (embeddingDimension float32s) plus a coarse ~1 KiB per
// document (chromem document + lock-step lexical entry). Package-variable
// test seam mirroring newPersistentDB — tests simulate multi-hundred-MiB
// states without materializing them; production must never reassign it.
var estimateStateBytes = func(ps *projectState, embeddingDimension int) int64 {
	if ps == nil || ps.collection == nil {
		return 0
	}
	return int64(ps.collection.Count()) * (int64(embeddingDimension)*4 + 1024)
}

// ServiceConfig holds configuration for creating a Service.
type ServiceConfig struct {
	// freeOSMemory is an instance-local test dependency; NewService resolves
	// nil to debug.FreeOSMemory. Immutable after construction.
	freeOSMemory func()
	// EmbeddingFunc is the chromem-go compatible embedding function
	// (Embedder.EmbeddingFunc()). Required. Attached to every chromem
	// collection for query-side embedding; also the document-side embedder
	// when BatchEmbedder is nil (legacy per-chunk path).
	EmbeddingFunc chromem.EmbeddingFunc

	// BatchEmbedder enables the batched document-embedding path in
	// AddDocuments: each sub-batch's contents are embedded up-front via
	// EmbedDocuments (chunks of ≤EmbeddingBatchSize texts) and assigned to
	// Document.Embedding BEFORE the chromem commit, so chromem-go skips its
	// per-document embedding calls (chromem-go ≥ v0.7.0 persists a
	// pre-populated `Document.Embedding` as-is) — removing the
	// one-inference-per-chunk bottleneck for the vector and lexical sides
	// alike. nil keeps the legacy per-doc path (tests rely on it);
	// query-side embedding always goes through EmbeddingFunc.
	BatchEmbedder BatchEmbedder

	// HybridConfig tunes Reciprocal Rank Fusion (RRF k, fanout) and the
	// pre-fusion score thresholds; zero disables thresholds and applies
	// built-in defaults for k/fanout. Production wiring sets thresholds via
	// config.VectorIndexConfig.
	HybridConfig HybridConfig

	// MaxFileSize caps a file's size for full reads during validation/walk;
	// larger files are skipped before any read. 0 →
	// DefaultMaxIndexableFileSize (4 MiB); vector_index.max_file_size.
	MaxFileSize int64

	// MaxChunkSize is the chunker's max chunk size in characters, mirrored
	// so content reconstruction bounds a chunk to a chunk-sized payload (see
	// contentResolver). 0 → DefaultMaxChunkSize (1500);
	// vector_index.max_chunk_size.
	MaxChunkSize int

	// MaxChunksPerFile is the indexer's per-file chunk cap, mirrored so the
	// content-less migration's legacy-entry probe scans every index a file
	// could have committed. 0 → DefaultMaxChunksPerFile (4000);
	// vector_index.max_chunks_per_file.
	MaxChunksPerFile int

	// EmbeddingBatchSize is the fixed row capacity of the embedder's batch
	// ONNX session (sp4rk embedding.EmbedderConfig.BatchSize), forwarded
	// from ManagerConfig; the embedder is constructed by the caller with the
	// same value. 0 → DefaultEmbeddingBatchSize (32).
	EmbeddingBatchSize int

	// EmbeddingCacheFingerprint identifies the exact model, tokenizer,
	// max-sequence length, output dimension, and chunk-normalization
	// algorithm. Empty disables the persistent cache.
	EmbeddingCacheFingerprint string
	// EmbeddingDimension is validated on every cache read and write.
	EmbeddingDimension int
	// EmbeddingCacheMaxBytes caps persistent cache entries; non-positive
	// disables the cache.
	EmbeddingCacheMaxBytes int64

	// ChunkerFingerprint identifies the chunker configuration (max chunk
	// size + overlap) embedded as the 4th field of new file-hash sidecar
	// entries: when it stops matching an entry (vector_index.chunk_overlap /
	// max_chunk_size changed), ValidateCollection re-chunks the file even
	// though its content is unchanged; legacy fp-less entries are exempt.
	// Empty → the package-default fingerprint.
	ChunkerFingerprint string

	// ContentFilter controls the same deterministic pre-chunk content policy
	// used by the Indexer. Nil selects DefaultContentFilterConfig.
	ContentFilter *ContentFilterConfig

	// ParkCapacity is the maximum number of recently-closed projects whose
	// vector-index state (chromem DB + lexical index + file-hash sidecar)
	// stays resident so returning to them skips the chromem gob-decode;
	// 0/negative disables parking (the historical reopen-every-switch
	// behaviour). Resolved from vector_index.park_capacity — no default here.
	// See specs/domains/workspace.md (park LRU) and
	// specs/decisions/049-vector-index-memory-reduction.md.
	ParkCapacity int

	// ParkBudgetBytes is the cumulative byte budget of the park LRU
	// (vector_index.park_budget_mb → bytes): the oldest parked states are
	// evicted while the summed estimateStateBytes exceeds it; ≤0 disables the
	// budget (config layer: unset → 1024 MiB). See
	// specs/decisions/049-vector-index-memory-reduction.md.
	ParkBudgetBytes int64

	// Logger for structured logging.
	Logger *slog.Logger

	// Telemetry optionally collects bounded, content-free stage aggregates.
	Telemetry *Telemetry
}

// projectState bundles every piece of mutable, per-project runtime state that
// used to live directly on Service. SetProject swaps the whole bundle; the
// outgoing state is parked in RAM (chromem DB + lexical index stay open) so a
// later return restores it without the gob-decode. Every field is guarded by
// the owning Service's mu, EXCEPT the atomics read/written without the write
// lock where their own docs say so.
type projectState struct {
	db         *chromem.DB
	collection *chromem.Collection
	lexical    lexical.Index

	projectID   string
	projectPath string // full path to project vector storage (set by SetProject)

	currentBranch string

	// fileHashes maps file_path → sidecar entry — legacy bare hash,
	// "hash|size|mtime", or "hash|size|mtime|chunkerFP" (formats and parsing:
	// parseFileHashEntry; specs/decisions/049-vector-index-memory-reduction.md).
	// Lets ValidateCollection compare against disk WITHOUT querying the
	// collection (which would trigger an ONNX embedding) and — current
	// format — WITHOUT re-reading/re-hashing unchanged files (stat match
	// only). Loaded per-branch in SwitchBranch; flushed at lifecycle
	// boundaries, not per batch.
	fileHashes map[string]string

	// fileHashMigrationPending is true while a background sidecar backfill
	// (collection → file_hashes) is in flight for the current branch.
	fileHashMigrationPending atomic.Bool

	// fileHashMigrationFailed is true when the last sidecar backfill for this
	// branch failed, so ps.fileHashes is an untrusted empty placeholder: the
	// empty map is NOT persisted on park/eviction and the content-less
	// migration does NOT write its marker. Cleared at the start of every
	// loadFileHashes.
	fileHashMigrationFailed atomic.Bool

	// migrationCh is closed when the current branch's sidecar has settled;
	// WaitFileHashMigration selects on it. nil before the first SwitchBranch.
	migrationCh chan struct{}

	// migrationCancel cancels an in-flight migrateFileHashes goroutine
	// (branch switch / project switch / close).
	migrationCancel context.CancelFunc

	// embeddingCache is branch-independent, rooted in the active project's
	// vector-index storage; recreated on a fresh open, not on a park restore.
	embeddingCache *embeddingCache

	// validationsSinceFullHash counts ValidateCollection passes since the last
	// full re-hash backstop (see fullHashRevalidationEvery). Atomic: the pass
	// runs under the read lock, so the counter must not rely on it.
	validationsSinceFullHash atomic.Int64

	// sidecarReloadOnRestore is set when the state is parked with a sidecar
	// backfill in flight (parkCurrentLocked cancels it, leaving the empty
	// placeholder); SetProject re-runs loadFileHashes on restore instead of
	// trusting it — which would make ValidateCollection treat every file as
	// new. Touched only under the owning Service's mu.
	sidecarReloadOnRestore bool

	// contentlessCh is closed when this branch's one-time content-less
	// migration has settled; WaitContentlessMigration selects on it. nil
	// before the first SwitchBranch; closedChan when none is needed (marker
	// present, empty collection, or in-memory state).
	contentlessCh chan struct{}

	// contentlessCancel cancels an in-flight migrateContentless goroutine
	// (branch switch / project switch / close / rebuild).
	contentlessCancel context.CancelFunc
}

// pendingNoProjectResetRequest is one queued No Project reset (see
// Service.pendingNoProjectReset): the projectID to install plus the
// Service.openGen value observed when the request was queued.
type pendingNoProjectResetRequest struct {
	projectID string
	openGen   int64
}

// Service manages chromem-go collections with git-branch awareness,
// readiness state, and vector search capabilities. It also owns a
// per-branch bleve lexical index written in lock-step with the chromem
// collection.
type Service struct {
	// current is the active project's state, never nil after NewService
	// (No Project installs an empty in-memory state). Swapped only under mu;
	// the lock-free readers (GetCollection, GetDB) observe whichever state
	// is current at that instant.
	current *projectState

	// indexAvailability is a metadata-only snapshot for UI status; reading it
	// must never wait for mu — indexing holds that lock across embedding and
	// persistence, while focus changes need status immediately.
	indexAvailability atomic.Uint32

	// parked is the LRU of recently-parked states, oldest first (parked[0] is
	// the eviction victim). Guarded by mu; empty when parking is disabled
	// (parkCapacity <= 0 — every switch closes the outgoing state).
	parked []*projectState

	// parkCapacity is the max number of parked states (see
	// ServiceConfig.ParkCapacity; 0/negative disables parking).
	parkCapacity int

	// parkBudgetBytes is the cumulative byte budget of the park LRU; ≤0
	// leaves parkCapacity as the only bound (see ServiceConfig.ParkBudgetBytes).
	parkBudgetBytes int64

	embeddingFunc chromem.EmbeddingFunc
	batchEmbedder BatchEmbedder
	// migrationWG lets Close wait for the migration goroutines (current and
	// parked states) to unwind before closing resources.
	migrationWG sync.WaitGroup
	mu          sync.RWMutex
	ready       atomic.Bool
	readyCh     chan struct{} // closed when ready becomes true; recreated on false
	readyMu     sync.Mutex    // protects readyCh swaps + readyGen

	// pendingNoProjectReset holds a No Project reset that could not be applied
	// immediately because s.mu was held (TryLock fails under write and read
	// holders alike); nil when none is queued. Atomic because the requester
	// records it WITHOUT taking s.mu. Requests are stamped with the openGen
	// observed at queue time and applied only while that generation is
	// current, so a queued reset can never wipe a project opened after it was
	// queued. See ResetForNoProject and specs/domains/workspace.md (No
	// Project reset).
	pendingNoProjectReset atomic.Pointer[pendingNoProjectResetRequest]

	// openGen counts SetProject invocations, bumped BEFORE acquiring s.mu, so
	// an unlocked read identifies the most recent open initiated. Queued No
	// Project resets carry the generation they observed; superseded/dropped
	// per specs/domains/workspace.md (No Project reset).
	openGen atomic.Int64

	// openPending counts SetProject critical sections in flight; the
	// background No Project applier waits on openCond until it drains, so a
	// queued reset is applied BY the open it was stamped with, never before
	// it. openCond is guarded by s.mu.
	openPending atomic.Int32
	openCond    *sync.Cond

	// closed is set by Close under s.mu, so a background No Project applier
	// that acquires the lock only afterwards never mutates a closed service.
	closed atomic.Bool
	// readyGen is bumped by every SetReady(false)/MarkNotReady. An indexing
	// pass captures the gen at start and passes it to RestoreReady on exit, so
	// a stale pass from an outgoing project — its defer running late after
	// cancellation — cannot mark a freshly-switched project ready.
	// See specs/domains/workspace.md (readiness generation).
	readyGen  int64
	logger    *slog.Logger
	telemetry *Telemetry
	// freeOSMemory is resolved at construction, never reassigned; eviction
	// and migration invoke it asynchronously, outside the service lock.
	freeOSMemory func()

	// hybridConfig holds resolved RRF tuning + pre-fusion score thresholds
	// (threshold fields of 0 mean "disabled").
	hybridConfig HybridConfig

	// maxFileSize is the resolved indexable-size bound; see
	// ServiceConfig.MaxFileSize.
	maxFileSize int64

	// maxChunkSize bounds reconstructed chunk content
	// (contentResolver.maxChunkSize); see ServiceConfig.MaxChunkSize.
	maxChunkSize int

	// maxChunksPerFile is the resolved per-file chunk cap; the content-less
	// migration's legacy-entry probe uses it as its scan bound (see
	// migrateContentless). See ServiceConfig.MaxChunksPerFile.
	maxChunksPerFile int

	// embeddingBatchSize is the resolved batch row capacity for the
	// batched-embedding path; see ServiceConfig.EmbeddingBatchSize.
	embeddingBatchSize int

	// embeddingCacheFingerprint/Dimension/MaxBytes configure the per-project
	// embedding cache held in projectState; (re)created on a fresh open, not
	// on a park restore.
	embeddingCacheFingerprint string
	embeddingDimension        int
	embeddingCacheMaxBytes    int64

	// chunkerFingerprint is the resolved chunker fingerprint embedded in new
	// sidecar entries; never empty (NewService defaults it to the
	// package-default configuration).
	chunkerFingerprint string

	// contentFilter is the resolved pre-chunk content policy, so
	// ValidateCollection keeps skipped files out of new/stale sets the same
	// way processFile keeps them out of the collection.
	contentFilter ContentFilterConfig
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
		freeOSMemory:              cfg.freeOSMemory,
		embeddingFunc:             cfg.EmbeddingFunc,
		batchEmbedder:             cfg.BatchEmbedder,
		readyCh:                   make(chan struct{}),
		logger:                    logger,
		telemetry:                 cfg.Telemetry,
		hybridConfig:              ResolveHybridConfig(cfg.HybridConfig),
		embeddingCacheFingerprint: cfg.EmbeddingCacheFingerprint,
		embeddingDimension:        cfg.EmbeddingDimension,
		embeddingCacheMaxBytes:    cfg.EmbeddingCacheMaxBytes,
		parkCapacity:              cfg.ParkCapacity,
		parkBudgetBytes:           cfg.ParkBudgetBytes,
	}
	if s.freeOSMemory == nil {
		s.freeOSMemory = debug.FreeOSMemory
	}
	// current starts as an empty in-memory state so every accessor (including
	// the lock-free GetCollection/GetDB) is safe before the first SetProject.
	s.current = &projectState{}
	// Publish the initial snapshot explicitly: the lock-free UI status reader
	// must not rely on the zero value matching the empty state.
	s.publishIndexAvailabilityLocked()
	s.openCond = sync.NewCond(&s.mu)
	if cfg.MaxFileSize > 0 {
		s.maxFileSize = cfg.MaxFileSize
	} else {
		s.maxFileSize = DefaultMaxIndexableFileSize
	}
	if cfg.MaxChunkSize > 0 {
		s.maxChunkSize = cfg.MaxChunkSize
	} else {
		s.maxChunkSize = DefaultMaxChunkSize
	}
	if cfg.MaxChunksPerFile > 0 {
		s.maxChunksPerFile = cfg.MaxChunksPerFile
	} else {
		s.maxChunksPerFile = DefaultMaxChunksPerFile
	}
	if cfg.EmbeddingBatchSize > 0 {
		s.embeddingBatchSize = cfg.EmbeddingBatchSize
	} else {
		s.embeddingBatchSize = DefaultEmbeddingBatchSize
	}
	s.chunkerFingerprint = cfg.ChunkerFingerprint
	if s.chunkerFingerprint == "" {
		// Default fingerprint must describe the configuration this Service
		// actually chunks with: with an explicit ContentFilter but no
		// fingerprint, derive from that policy, not the package default.
		// Production wiring always passes both from one resolved config.
		s.chunkerFingerprint = ChunkerFingerprint(
			DefaultMaxChunkSize, DefaultChunkOverlap,
			resolveContentFilterConfig(cfg.ContentFilter).Fingerprint(),
		)
	}
	s.contentFilter = resolveContentFilterConfig(cfg.ContentFilter)

	return s, nil
}

// SetProject switches to a project directory: it prepares the per-project
// storage layout but no longer opens the chromem DB — the branch-scoped open
// is deferred to SwitchBranch, which decodes only the active branch's
// collection (ADR-064). Instead of tearing the previous project down, the
// outgoing state is parked (parkCurrentLocked); a later call with the same
// projectID and path restores it from RAM — DB, lexical index and file-hash
// sidecar intact. Parking disabled or not a real project → closed (the
// historical reopen-every-switch behaviour).
// See specs/decisions/064-branch-scoped-lazy-vector-index-open.md.
func (s *Service) SetProject(projectID, fullPath string, embeddingCachePaths ...string) error {
	// Stamp this open pending + bump its generation BEFORE taking the lock
	// (see pendingNoProjectReset/openGen); under the lock, drop older-
	// generation queued resets — this open supersedes them — and keep a
	// same-generation one for the deferred applier (the open's last act).
	s.openPending.Add(1)
	gen := s.openGen.Add(1)
	s.mu.Lock()
	s.dropSupersededNoProjectResetsLocked(gen)
	// LIFO: the gen-scoped applier runs first (lock held), then the pending
	// count settles, then the lock releases.
	defer s.mu.Unlock()
	defer s.publishIndexAvailabilityLocked()
	defer s.finishOpenLocked()
	defer s.applyPendingNoProjectResetForGenLocked(gen)

	var embeddingCachePath string
	if len(embeddingCachePaths) > 0 {
		embeddingCachePath = embeddingCachePaths[0]
	}

	s.SetReady(false)

	// Retire the outgoing state: park it (best case) or close it.
	s.parkCurrentLocked()

	// Fast path: restore a parked state for the same project and path —
	// no newPersistentDB, no gob-decode; the collection and lexical index
	// are the very objects that were live before.
	if ps := s.takeParkedLocked(projectID, fullPath); ps != nil {
		s.current = ps
		if ps.sidecarReloadOnRestore {
			// The park cancelled this state's sidecar backfill; re-settle it
			// (loadFileHashes also settles the content-less migration, so the
			// explicit call below is skipped on this path).
			ps.sidecarReloadOnRestore = false
			s.loadFileHashes()
		} else {
			// A content-less migration cancelled by the park must replay
			// here (the marker is only written on full completion, and
			// SwitchBranch early-returns on the restored state). No-op when
			// the marker exists or the collection is empty.
			s.maybeMigrateContentlessLocked()
		}
		s.logger.Info("project restored from park", "projectID", projectID)
		return nil
	}

	// Slow path: open a fresh state. s.current stays non-nil on every return
	// path (an empty state on error) so the lock-free accessors never see nil.
	ps := &projectState{projectID: projectID, projectPath: fullPath}
	if fullPath != "" {
		if err := ensureRealDir(fullPath, 0o750); err != nil {
			s.current = &projectState{}
			return fmt.Errorf("creating project directory %s: %w", fullPath, err)
		}
		// Do NOT open the persistent DB here: chromem eagerly gob-decodes
		// EVERY collection in the directory — O(all branches × documents),
		// minutes on a large index, all under s.mu. The branch-scoped open
		// is deferred to SwitchBranch; here we only run the one-time
		// migration that re-homes legacy collection directories into their
		// per-branch roots. See
		// specs/decisions/064-branch-scoped-lazy-vector-index-open.md.
		s.migrateLegacyLayoutLocked(fullPath)
		if embeddingCachePath != "" {
			within, containmentErr := pathutil.IsWithinPath(fullPath, embeddingCachePath)
			if containmentErr != nil || !within {
				s.logger.Warn("embedding cache disabled: path is outside vector-index storage", "error", containmentErr)
				embeddingCachePath = ""
			}
		}
		ps.embeddingCache = newEmbeddingCache(
			embeddingCachePath,
			s.embeddingCacheFingerprint,
			s.embeddingDimension,
			s.embeddingCacheMaxBytes,
			s.logger,
		)
		// Seed the cache's byte accounting off the write lock: a warm cache's
		// first walk is a full directory traversal and would stall every
		// search behind s.mu. Until the seed lands, prunes take the deferred
		// fast path (seedAccountingAsync).
		ps.embeddingCache.seedAccountingAsync()
	} else {
		ps.db = chromem.NewDB()
	}
	s.current = ps

	s.logger.Info("project set for vector index", "projectID", projectID)
	return nil
}

// ResetForNoProject retires the current project and installs the empty
// in-memory No Project state without waiting for the s.mu holder (the request
// is queued, generation-stamped, delivered by the stamped in-flight open or
// the background applier; a newer SetProject supersedes it). Readiness is
// dropped by the caller first.
// Full protocol: specs/domains/workspace.md (No Project reset).
func (s *Service) ResetForNoProject(projectID string) {
	if s.mu.TryLock() {
		s.pendingNoProjectReset.Store(nil)
		s.resetForNoProjectLocked(projectID)
		s.mu.Unlock()
		return
	}
	gen := s.openGen.Load()
	s.pendingNoProjectReset.Store(&pendingNoProjectResetRequest{projectID: projectID, openGen: gen})
	s.kickNoProjectResetApplier()
}

// kickNoProjectResetApplier spawns the goroutine that — unlike the caller —
// may block: it waits on openCond until in-flight opens drain, then applies
// the still-current request (no-op once superseded or the service is closed).
func (s *Service) kickNoProjectResetApplier() {
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for s.openPending.Load() > 0 {
			// Wait releases s.mu, so in-flight opens keep making progress;
			// finishOpenLocked broadcasts when the count drains to zero.
			s.openCond.Wait()
		}
		s.applyPendingNoProjectResetLocked()
	}()
}

// dropSupersededNoProjectResetsLocked drops a queued No Project reset whose
// generation is strictly older than gen (the open that stamped gen
// supersedes it); same- and newer-generation requests are left in place.
// The caller must hold s.mu.
func (s *Service) dropSupersededNoProjectResetsLocked(gen int64) {
	for {
		p := s.pendingNoProjectReset.Load()
		if p == nil || p.openGen >= gen {
			return
		}
		if s.pendingNoProjectReset.CompareAndSwap(p, nil) {
			return
		}
	}
}

// finishOpenLocked settles the open-pending count; when the last in-flight
// open drains, the background applier waiting on openCond is woken so a
// request no open will consume is still applied. The caller must hold s.mu.
func (s *Service) finishOpenLocked() {
	if s.openPending.Add(-1) == 0 {
		s.openCond.Broadcast()
	}
}

// applyPendingNoProjectResetForGenLocked applies, as the open's last act, a
// queued reset carrying gen — one requested while THAT open was in flight;
// a newer-generation request belongs to a younger open and is untouched, a
// strictly older one is dropped. The caller must hold s.mu (SetProject's
// deferred call).
func (s *Service) applyPendingNoProjectResetForGenLocked(gen int64) {
	for {
		p := s.pendingNoProjectReset.Load()
		if p == nil || p.openGen > gen {
			return
		}
		if !s.pendingNoProjectReset.CompareAndSwap(p, nil) {
			continue
		}
		if p.openGen == gen && !s.closed.Load() {
			s.resetForNoProjectLocked(p.projectID)
		}
		return
	}
}

// applyPendingNoProjectResetLocked applies a queued reset whose generation is
// still current (background applier, under s.mu); a superseded generation is
// dropped instead of wiping that open's state.
func (s *Service) applyPendingNoProjectResetLocked() {
	pending := s.pendingNoProjectReset.Swap(nil)
	if pending == nil {
		return
	}
	if s.closed.Load() {
		return
	}
	if pending.openGen != s.openGen.Load() {
		return
	}
	s.resetForNoProjectLocked(pending.projectID)
}

// resetForNoProjectLocked installs the empty in-memory No Project state (the
// same state SetProject(NoProjectID, "") produces). The caller must hold s.mu.
func (s *Service) resetForNoProjectLocked(projectID string) {
	s.SetReady(false)
	s.parkCurrentLocked()
	ps := &projectState{projectID: projectID}
	ps.db = chromem.NewDB()
	s.current = ps
	// Publish the empty state explicitly: the background No Project applier
	// reaches this path with no SetProject defer to publish for it.
	s.publishIndexAvailabilityLocked()
	s.logger.Info("project set for vector index", "projectID", projectID)
}

// parkCurrentLocked retires the current project state (caller holds s.mu): it
// flushes the outgoing branch's file-hash sidecar (unless a migration is
// pending or failed — the empty placeholder must not be persisted), cancels
// in-flight migrations, then either parks the state (chromem DB + lexical
// index stay resident) or closes it (parking disabled, or not a real
// project). Beyond the LRU capacity and the byte budget the oldest parked
// states are evicted; each actual eviction schedules freeOSMemory
// asynchronously, outside the lock. After the call s.current holds a fresh
// empty state — never nil, so the lock-free GetCollection/GetDB readers can
// never dereference nil.
// See specs/decisions/049-vector-index-memory-reduction.md.
func (s *Service) parkCurrentLocked() {
	ps := s.current
	// Empty placeholder for the (brief, mu-protected) window until SetProject
	// assigns the incoming state — never nil: the lock-free accessors would
	// fault.
	s.current = &projectState{}
	s.publishIndexAvailabilityLocked()
	if ps == nil {
		return
	}

	// Persist the outgoing hashes before parking/closing, and cancel any
	// in-flight sidecar migration. Skip the save while a migration is pending
	// or the last one failed: loadFileHashes then seeds ps.fileHashes as an
	// empty map, and persisting it would write an empty sidecar the next open
	// would trust and re-embed every file over — re-settle on restore instead
	// (sidecarReloadOnRestore).
	if ps.fileHashes != nil && ps.currentBranch != "" {
		if ps.fileHashMigrationPending.Load() || ps.fileHashMigrationFailed.Load() {
			ps.sidecarReloadOnRestore = true
		} else if err := ps.saveFileHashes(); err != nil {
			s.logger.Warn("failed to persist file-hash sidecar on project switch", "error", err)
		}
	}
	if ps.migrationCancel != nil {
		ps.migrationCancel()
		ps.migrationCancel = nil
	}
	// A parked state's in-flight content-less migration is abandoned the same
	// way: it re-triggers on the next restore or open. Its per-window flushes
	// take s.mu.RLock, so they cannot interleave with this critical section.
	if ps.contentlessCancel != nil {
		ps.contentlessCancel()
		ps.contentlessCancel = nil
	}

	if s.parkCapacity <= 0 || ps.projectPath == "" || ps.db == nil {
		// Parking disabled, nothing worth keeping (the No-Project state), or
		// a branch-scoped DB never opened (ADR-064): close immediately.
		s.closeStateLocked(ps)
		return
	}

	s.parked = append(s.parked, ps)
	evicted := false
	for len(s.parked) > s.parkCapacity {
		s.evictLocked(s.parked[0])
		s.parked = s.parked[1:]
		evicted = true
	}
	if s.parkBudgetBytes > 0 {
		for len(s.parked) > 0 && s.parkedBytesLocked() > s.parkBudgetBytes {
			s.evictLocked(s.parked[0])
			s.parked = s.parked[1:]
			evicted = true
		}
	}
	if evicted {
		// The freed states can hold hundreds of MiB; FreeOSMemory forces a
		// full (stop-the-world) GC cycle, so it runs in its own goroutine and
		// never takes s.mu.
		go s.freeOSMemory()
	}
}

// parkedBytesLocked sums the estimated resident footprints of the parked
// states (see estimateStateBytes). The caller must hold s.mu.
func (s *Service) parkedBytesLocked() int64 {
	var total int64
	for _, ps := range s.parked {
		total += estimateStateBytes(ps, s.embeddingDimension)
	}
	return total
}

// takeParkedLocked returns and removes the parked state matching
// (projectID, fullPath), or nil; a same-project entry at a DIFFERENT path is
// stale (moved or recreated storage) and evicted rather than restored. The
// caller must hold s.mu.
func (s *Service) takeParkedLocked(projectID, fullPath string) *projectState {
	for i, ps := range s.parked {
		if ps.projectID != projectID {
			continue
		}
		s.parked = append(s.parked[:i], s.parked[i+1:]...)
		if filepath.Clean(ps.projectPath) != filepath.Clean(fullPath) {
			s.logger.Info("evicting stale parked project (storage path changed)",
				"projectID", projectID, "parkedPath", ps.projectPath, "requestedPath", fullPath)
			s.evictLocked(ps)
			return nil
		}
		return ps
	}
	return nil
}

// closeStateLocked releases a state's file handles (lexical index + chromem
// DB) and drops its in-memory references; it does NOT flush the sidecar
// (parkCurrentLocked/evictLocked do) and leaves goroutine-lifetime tracking
// (migrationWG) to the caller. The caller must hold s.mu.
func (s *Service) closeStateLocked(ps *projectState) {
	if ps == nil {
		return
	}
	if ps.lexical != nil {
		if err := ps.lexical.Close(); err != nil {
			s.logger.Warn("failed to close lexical index", "error", err)
		}
		ps.lexical = nil
	}
	ps.db = nil
	ps.collection = nil
	ps.fileHashes = nil
	ps.migrationCh = nil
	if ps.contentlessCancel != nil {
		ps.contentlessCancel()
		ps.contentlessCancel = nil
	}
	ps.contentlessCh = nil
	ps.currentBranch = ""
	if ps == s.current {
		s.publishIndexAvailabilityLocked()
	}
}

// releaseBranchResidentsLocked drops only the ACTIVE BRANCH's in-memory
// residents (lexical index, chromem DB, collection), keeping the project
// identity, the file-hash sidecar and the queued reset. Unlike
// closeStateLocked it is not a teardown: SwitchBranch calls it right before
// opening the incoming branch, so a switch peaks at one branch's working set
// instead of two (every branch owns its own chromem DB since ADR-064). The
// outgoing hashes must already be persisted (SwitchBranch does) — dropping
// the collection makes them unrecoverable from RAM; a subsequent open failure
// leaves the state closed (GetCollection nil, search fails fast). The caller
// must hold s.mu.
// See specs/decisions/064-branch-scoped-lazy-vector-index-open.md.
func (s *Service) releaseBranchResidentsLocked() {
	if s.current.lexical != nil {
		if err := s.current.lexical.Close(); err != nil {
			s.logger.Warn("failed to close previous lexical index", "error", err)
		}
		s.current.lexical = nil
	}
	s.current.db = nil
	s.current.collection = nil
	s.publishIndexAvailabilityLocked()
}

// evictLocked drops a parked state to make room in the LRU: it flushes the
// file-hash sidecar to disk one final time (unless a migration is pending),
// then closes the state's handles. The caller must hold s.mu.
func (s *Service) evictLocked(ps *projectState) {
	if ps == nil {
		return
	}
	if ps.fileHashes != nil && ps.currentBranch != "" &&
		!ps.fileHashMigrationPending.Load() && !ps.fileHashMigrationFailed.Load() {
		if err := ps.saveFileHashes(); err != nil {
			s.logger.Warn("failed to persist file-hash sidecar on park eviction", "error", err)
		}
	}
	if ps.migrationCancel != nil {
		ps.migrationCancel()
		ps.migrationCancel = nil
	}
	s.closeStateLocked(ps)
}

// Browse returns up to topK chunks from the current collection without
// semantic ordering; blocks via WaitReady if the index is not yet ready.
func (s *Service) Browse(ctx context.Context, topK int) ([]SearchResult, error) {
	return s.BrowseWithFilter(ctx, topK, "")
}

// BrowseWithFilter returns up to topK chunks from the current collection without
// semantic ordering, optionally filtered by a file path glob pattern.
// Blocks via WaitReady if the index is not yet ready.
func (s *Service) BrowseWithFilter(ctx context.Context, topK int, fileFilter string) ([]SearchResult, error) {
	return s.browseWithFilter(ctx, topK, fileFilter, true)
}

// BrowseWithFilterNoWait is the non-blocking form: it never waits for
// readiness and returns ErrNotReady immediately — callers that gate readiness
// themselves (fail-fast search wiring) use it so a pass starting between
// their gate and the call cannot block them.
func (s *Service) BrowseWithFilterNoWait(ctx context.Context, topK int, fileFilter string) ([]SearchResult, error) {
	return s.browseWithFilter(ctx, topK, fileFilter, false)
}

// unitQueryVector returns the embedding-free enumeration query vector — a
// unit vector along the first axis, or nil when the dimension is unknown (0),
// which keeps callers on the text-query path; fixed at construction, no lock.
// See specs/decisions/049-vector-index-memory-reduction.md.
func (s *Service) unitQueryVector() []float32 {
	if s.embeddingDimension <= 0 {
		return nil
	}
	vec := make([]float32, s.embeddingDimension)
	vec[0] = 1
	return vec
}

// browseWithFilter implements BrowseWithFilter; wait selects the blocking
// (WaitReady) or non-blocking (ErrNotReady) readiness gate. See the public
// wrappers for the contracts.
func (s *Service) browseWithFilter(ctx context.Context, topK int, fileFilter string, wait bool) ([]SearchResult, error) {
	if wait {
		if err := s.WaitReady(ctx); err != nil {
			return nil, fmt.Errorf("waiting for index readiness: %w", err)
		}
	} else if !s.IsReady() {
		return nil, ErrNotReady
	}

	s.mu.RLock()

	if s.current.collection == nil {
		s.mu.RUnlock()
		return nil, errors.New("no collection available; call SetProject and SwitchBranch first")
	}

	count := s.current.collection.Count()
	if count == 0 {
		s.mu.RUnlock()
		return []SearchResult{}, nil
	}
	if topK > count {
		topK = count
	}

	// Embedding-free enumeration (see unitQueryVector): dim == 0 keeps the
	// embedding-bearing path; the fileFilter below still narrows the set.
	var results []chromem.Result
	var err error
	if unitVec := s.unitQueryVector(); unitVec != nil {
		results, err = s.current.collection.QueryEmbedding(ctx, unitVec, topK, nil, nil)
	} else {
		results, err = s.current.collection.Query(ctx, " ", topK, nil, nil)
	}
	s.mu.RUnlock()
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

	// Committed documents store no chunk text; hydrate Content from the
	// source files (final top-K only) AFTER the read lock is released —
	// hydration reads files and must not block a concurrent indexing writer.
	hydrateSearchContent(out, newContentResolver(s.maxFileSize, s.maxChunkSize))

	return out, nil
}

// Search queries the current collection for the top-K most similar results —
// a thin shim over HybridSearch(ModeVector) so filtering has one code path.
func (s *Service) Search(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	return s.HybridSearch(ctx, SearchOptions{Query: query, TopK: topK, Mode: ModeVector})
}

// SearchWithFilter queries with an optional file-path glob — a thin shim over
// HybridSearch(ModeVector). Blocks via WaitReady if the index is not yet ready.
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

// ErrNotReady is returned by the NoWait search variants (HybridSearchNoWait,
// BrowseWithFilterNoWait) when the index is not ready; callers with their own
// bounded wait typically translate it via Manager.NotReadyError so users see
// index progress instead of a bare deadline error.
var ErrNotReady = errors.New("vector index not ready")

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
		// Always bump the generation on SetReady(false), even without a
		// transition, so a concurrent pass that captured an older gen cannot
		// falsely restore readiness after a project switch intervenes.
		s.readyGen++
	}
}

// MarkNotReady atomically sets ready=false and returns the readiness
// generation, pairing the state flip with the gen capture in one lock
// acquisition; indexing passes pass the gen to RestoreReady on exit.
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

// RestoreReady marks the service ready ONLY if gen still matches the value
// MarkNotReady returned at pass start; a project switch (or any other
// SetReady(false)) bumps the gen, so a stale pass cannot mark a
// freshly-switched (or closed) project ready.
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

// GetDB returns the underlying chromem-go DB (for use by Indexer). Lock-free
// on purpose: indexing paths already hold the service lock, so taking it here
// would deadlock; nil-checked defensively.
func (s *Service) GetDB() *chromem.DB {
	ps := s.current
	if ps == nil {
		return nil
	}
	return ps.db
}

// GetCollection returns the current collection (for use by Indexer).
// Intentionally lock-free like GetDB; status-honesty callers read it WITHOUT
// the lock as a best-effort snapshot (the tolerance HandleBranchSwitch
// documents).
func (s *Service) GetCollection() *chromem.Collection {
	ps := s.current
	if ps == nil {
		return nil
	}
	return ps.collection
}

// IndexAvailability returns the last published (vector, lexical) snapshot
// without the service lock or live index handles — UI metadata only, not a
// readiness or authorization check for searches.
func (s *Service) IndexAvailability() (hasVector, hasLexical bool) {
	flags := s.indexAvailability.Load()
	return flags&1 != 0, flags&2 != 0
}

// publishIndexAvailabilityLocked publishes both flags atomically; the caller
// holds s.mu (sole exception NewService, still unshared). Only lifecycle
// transitions change availability, not document writes.
func (s *Service) publishIndexAvailabilityLocked() {
	var flags uint32
	if s.current != nil {
		if s.current.collection != nil {
			flags |= 1
		}
		if s.current.lexical != nil {
			flags |= 2
		}
	}
	s.indexAvailability.Store(flags)
}

// GetLexical returns the current lexical index (may be nil for in-memory
// mode or before a branch is opened).
func (s *Service) GetLexical() lexical.Index {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentLexicalLocked()
}

// currentLexicalLocked returns the live lexical index without acquiring
// s.mu — for callers that already hold the read or the write lock.
func (s *Service) currentLexicalLocked() lexical.Index {
	if s.current == nil {
		return nil
	}
	return s.current.lexical
}

// LexicalCount returns the lexical index's document count (0 when none is
// open); the read lock spans Count — the captured pointer must not outlive
// the lock that serializes Close.
func (s *Service) LexicalCount() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lex := s.current.lexical
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
	return s.current.currentBranch
}

// DeleteProjectData removes the on-disk vector data for a project.
// It is safe to call even if the project was never indexed.
func (s *Service) DeleteProjectData(fullPath string) error {
	if fullPath == "" {
		return nil
	}
	// Release this project's open handles BEFORE removing the directory —
	// Windows refuses to delete open-handle files, Unix leaves stale inodes —
	// and drop any PARKED state for it too: a later switch back would
	// otherwise restore a collection rooted at a deleted directory.
	target := filepath.Clean(fullPath)
	s.mu.Lock()
	if cur := s.current; cur != nil && filepath.Clean(cur.projectPath) == target {
		if cur.migrationCancel != nil {
			cur.migrationCancel()
			cur.migrationCancel = nil
		}
		s.closeStateLocked(cur)
		// Reset rather than leaving the closed state installed: it keeps
		// projectID/projectPath, and parkCurrentLocked would park this dead
		// state into the LRU — restoring later a collection rooted at a
		// deleted directory. The empty placeholder is never parked/restorable.
		s.current = &projectState{}
		// Publish the now-empty snapshot explicitly: this reset is the state
		// the lock-free UI reader must observe, not a coincidence of flags.
		s.publishIndexAvailabilityLocked()
	}
	if len(s.parked) > 0 {
		kept := s.parked[:0]
		for _, ps := range s.parked {
			if filepath.Clean(ps.projectPath) == target {
				if ps.migrationCancel != nil {
					ps.migrationCancel()
					ps.migrationCancel = nil
				}
				// Do NOT flush the sidecar here: the directory is about to be
				// removed, so writing into it is wasted work.
				s.closeStateLocked(ps)
				continue
			}
			kept = append(kept, ps)
		}
		s.parked = kept
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
	// Mark closed under the lock, so a background No Project applier that
	// acquires s.mu only afterwards drops its request instead of mutating a
	// closed service.
	s.closed.Store(true)

	// Persist the current branch's in-memory hashes before shutdown, cancel
	// any in-flight migration, and close the current state's handles.
	if cur := s.current; cur != nil {
		if cur.fileHashes != nil && cur.currentBranch != "" {
			if err := cur.saveFileHashes(); err != nil {
				s.logger.Warn("failed to persist file-hash sidecar on close", "error", err)
			}
		}
		if cur.migrationCancel != nil {
			cur.migrationCancel()
			cur.migrationCancel = nil
		}
		s.closeStateLocked(cur)
	}
	// Close every parked state too: sidecars were already flushed at park
	// time, so only migrations are cancelled and handles released.
	for _, ps := range s.parked {
		if ps.migrationCancel != nil {
			ps.migrationCancel()
			ps.migrationCancel = nil
		}
		s.closeStateLocked(ps)
	}
	s.parked = nil
	s.mu.Unlock()

	// Wait for in-flight migration goroutines to unwind — AFTER the lock is
	// released, or the goroutine would deadlock waiting for it.
	s.migrationWG.Wait()
	s.logger.Info("vector index service closed")
	return nil
}

// resultToSearchResult converts a chromem-go Result to a SearchResult. Content
// passes through as-is: committed documents store no chunk text, so the caller
// hydrates the final top-K from source files via hydrateSearchContent;
// FileName is derived from FilePath (documents store no file_name field).
// See specs/decisions/049-vector-index-memory-reduction.md.
func resultToSearchResult(r chromem.Result) SearchResult {
	startLine, _ := strconv.Atoi(r.Metadata["start_line"])
	endLine, _ := strconv.Atoi(r.Metadata["end_line"])

	fp := r.Metadata["file_path"]
	fileName := ""
	if fp != "" {
		fileName = filepath.Base(fp)
	}

	return SearchResult{
		FilePath:  fp,
		FileName:  fileName,
		Content:   r.Content,
		Score:     r.Similarity,
		StartLine: startLine,
		EndLine:   endLine,
		Language:  r.Metadata["language"],
	}
}
