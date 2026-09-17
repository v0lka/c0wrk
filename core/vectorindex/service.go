package vectorindex

import (
	"context"
	"errors"
	"fmt"
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
)

// newPersistentDB opens a project's persistent chromem DB. It is a package
// variable — not a direct chromem.NewPersistentDB call — purely as a test
// seam: tests swap it for a counting wrapper to assert that a project restored
// from the park LRU does NOT reopen (and therefore re-gob-decode) its
// persistent DB. Production code must never reassign it.
var newPersistentDB = chromem.NewPersistentDB

// estimateStateBytes approximates the resident memory a parked project state
// keeps alive: one embedding vector (embeddingDimension float32s) plus a
// coarse ~1 KiB per-document allowance covering the chromem document (content,
// metadata, map entry) and its lock-step bleve lexical entry. It is a package
// variable — not a method — purely as a test seam (mirroring newPersistentDB):
// tests swap it to simulate multi-hundred-MiB states without materializing
// them. Production code must never reassign it.
var estimateStateBytes = func(ps *projectState, embeddingDimension int) int64 {
	if ps == nil || ps.collection == nil {
		return 0
	}
	return int64(ps.collection.Count()) * (int64(embeddingDimension)*4 + 1024)
}

// freeOSMemory is a test seam over debug.FreeOSMemory: an actual park
// eviction schedules it (as its own goroutine, off the service lock) so the
// potentially hundreds of MiB a parked state kept resident are returned to
// the OS promptly instead of lingering until the next natural GC cycle.
// Tests swap it to observe the call without a real stop-the-world pause.
var freeOSMemory = debug.FreeOSMemory

// ServiceConfig holds configuration for creating a Service.
type ServiceConfig struct {
	// EmbeddingFunc is the chromem-go compatible embedding function
	// (from Embedder.EmbeddingFunc()). Required. It is attached to every
	// chromem collection and used for query-side embedding; when
	// BatchEmbedder is nil it is also the document-side embedder (legacy
	// one-inference-per-chunk path via chromem).
	EmbeddingFunc chromem.EmbeddingFunc

	// BatchEmbedder optionally enables the batched document-embedding path
	// in AddDocuments: the contents of each embedding sub-batch are embedded
	// up-front via EmbedDocuments (in chunks of at most EmbeddingBatchSize
	// texts) and assigned to Document.Embedding BEFORE the chromem commit,
	// so chromem-go skips its per-document embedding calls entirely (v0.7.0
	// AddDocument only normalizes + persists a pre-populated embedding).
	// This removes the one-inference-per-chunk bottleneck for both the
	// vector and lexical sides of indexing. nil keeps the legacy per-doc
	// path (tests rely on it). Query-side embedding still goes through
	// EmbeddingFunc.
	BatchEmbedder BatchEmbedder

	// HybridConfig tunes Reciprocal Rank Fusion (RRF k, fanout, and
	// pre-fusion score thresholds). A zero value disables score
	// thresholds and uses built-in defaults for k/fanout; production
	// wiring sets thresholds via config.VectorIndexConfig.
	HybridConfig HybridConfig

	// MaxFileSize is the upper bound (in bytes) on a file's size for it to
	// be read fully into memory during validation/walk. Files above this are
	// skipped before any full read. Defaults to DefaultMaxIndexableFileSize
	// (4 MiB) when zero. Configurable via vector_index.max_file_size.
	MaxFileSize int64

	// MaxChunkSize is the chunker's maximum chunk size in characters, mirrored
	// here so content reconstruction can bound a reconstructed chunk to a
	// chunk-sized payload (see contentResolver). Defaults to DefaultMaxChunkSize
	// (1500) when zero. Configurable via vector_index.max_chunk_size.
	MaxChunkSize int

	// MaxChunksPerFile is the indexer's per-file chunk cap, mirrored here so the
	// content-less migration's legacy-entry probe scans every index a file
	// could have committed. Defaults to DefaultMaxChunksPerFile (4000) when
	// zero. Configurable via vector_index.max_chunks_per_file.
	MaxChunksPerFile int

	// EmbeddingBatchSize is the fixed row capacity of the embedder's batch
	// ONNX session (sp4rk embedding.EmbedderConfig.BatchSize), forwarded
	// from ManagerConfig by the Manager. The embedder itself is constructed
	// by the caller (desktop startup), which sets the same value there; the
	// Service stores it for the batched-embedding path. 0 (or unset)
	// defaults to DefaultEmbeddingBatchSize (32) — identical to the previous
	// behaviour, where sp4rk applied its own default.
	EmbeddingBatchSize int

	// EmbeddingCacheFingerprint identifies the exact model, tokenizer,
	// max-sequence length, output dimension, and chunk-normalization algorithm.
	// Empty disables the persistent cache.
	EmbeddingCacheFingerprint string
	// EmbeddingDimension is validated on every cache read and write.
	EmbeddingDimension int
	// EmbeddingCacheMaxBytes caps persistent cache entries. Non-positive
	// disables the cache.
	EmbeddingCacheMaxBytes int64

	// ChunkerFingerprint identifies the chunker configuration (max chunk
	// size + overlap) that the per-project Indexer chunks files with. It is
	// embedded as the 4th field of new file-hash sidecar entries; when the
	// active fingerprint no longer matches an entry's (i.e. vector_index.
	// chunk_overlap / max_chunk_size changed), ValidateCollection reports
	// the file as stale so it is re-chunked even though its content is
	// unchanged. Entries without a fingerprint (legacy formats) are exempt
	// from this check. Empty defaults to the fingerprint of the package
	// defaults (DefaultMaxChunkSize, DefaultChunkOverlap).
	ChunkerFingerprint string

	// ContentFilter controls the same deterministic pre-chunk content policy
	// used by the Indexer. Nil selects DefaultContentFilterConfig.
	ContentFilter *ContentFilterConfig

	// ParkCapacity is the maximum number of recently-closed projects whose
	// vector-index state (chromem DB + lexical index + file-hash sidecar) is
	// kept resident in memory so returning to them skips the expensive
	// chromem gob-decode. 0 (or negative) disables parking and reproduces the
	// historical behaviour where every project switch reopened the persistent
	// DB from scratch. Resolved from vector_index.park_capacity (default 3).
	ParkCapacity int

	// ParkBudgetBytes is the cumulative byte budget for the park LRU
	// (vector_index.park_budget_mb, converted to bytes by desktop startup):
	// right after a state is appended, the oldest parked states are evicted
	// while the summed estimateStateBytes of the LRU exceeds the budget, so
	// the effective bound is min(ParkCapacity, ParkBudgetBytes). Like
	// ParkCapacity, NO default is applied here: the config layer resolves an
	// unset key to 1024 MiB, while 0 (or negative — the config disable
	// sentinel) keeps ParkCapacity as the only bound, which is also the
	// zero-value behaviour for test literals. ParkCapacity <= 0 still
	// disables parking entirely regardless of this budget.
	ParkBudgetBytes int64

	// Logger for structured logging.
	Logger *slog.Logger

	// Telemetry optionally collects bounded, content-free stage aggregates.
	Telemetry *Telemetry
}

// projectState bundles every piece of mutable, per-project runtime state that
// used to live directly on Service. SetProject swaps the whole bundle: the
// outgoing project's state is parked (kept in RAM without closing its chromem
// DB or bleve lexical index), so a later return to the same project restores
// it without paying the expensive chromem gob-decode again. Every field is
// guarded by the owning Service's mu, EXCEPT the two atomics
// (fileHashMigrationPending, validationsSinceFullHash), which are read/written
// without the write lock where their documentation allows.
type projectState struct {
	db         *chromem.DB
	collection *chromem.Collection
	lexical    lexical.Index

	projectID   string
	projectPath string // full path to project vector storage (set by SetProject)

	currentBranch string

	// fileHashes is a sidecar store mapping file_path → sidecar entry, kept in
	// sync with the chromem collection. Entries are either a bare content
	// hash (legacy), "hash|size|mtimeUnixNano" (intermediate format), or
	// "hash|size|mtimeUnixNano|chunkerFP" (current format, see
	// parseFileHashEntry in collection.go). It lets ValidateCollection compare
	// stored state against disk WITHOUT querying the collection (which would
	// trigger an ONNX embedding via Query) and — for current-format entries —
	// WITHOUT reading or re-hashing unchanged files at all (stat match only).
	// Loaded per-branch in SwitchBranch and flushed at lifecycle boundaries
	// (not on every batch).
	fileHashes map[string]string

	// fileHashMigrationPending is true while a background sidecar backfill
	// (collection → file_hashes) is in flight for the current branch.
	fileHashMigrationPending atomic.Bool

	// fileHashMigrationFailed is true when the last sidecar backfill for this
	// branch failed (the collection enumeration errored), so ps.fileHashes is
	// an untrusted empty placeholder rather than "nothing trackable". It gates
	// two decisions: the empty sidecar is NOT persisted on park/eviction (it
	// would make the next open trust an empty map), and the content-less
	// migration does NOT write its marker (an empty entry set would certify a
	// collection whose legacy documents still carry content — see finding
	// migrateContentless). Cleared at the start of every loadFileHashes.
	fileHashMigrationFailed atomic.Bool

	// migrationCh is closed when the current branch's sidecar has settled
	// (loaded, empty, or migrated). WaitFileHashMigration selects on it.
	// nil before the first SwitchBranch.
	migrationCh chan struct{}

	// migrationCancel cancels an in-flight migrateFileHashes goroutine
	// (e.g. on branch switch / project switch / close).
	migrationCancel context.CancelFunc

	// embeddingCache is branch-independent and rooted inside the active
	// project's vector-index storage. It is recreated when a fresh state is
	// opened (not on a restore from park).
	embeddingCache *embeddingCache

	// validationsSinceFullHash counts ValidateCollection passes since the
	// last pass that skipped the stat-based fast-path and re-read +
	// re-hashed every file (the periodic full-hash revalidation backstop;
	// see fullHashRevalidationEvery). Atomic: ValidateCollection runs under
	// the read lock, so the counter must not rely on it.
	validationsSinceFullHash atomic.Int64

	// sidecarReloadOnRestore is set when the state is parked while a sidecar
	// backfill migration is still in flight (parkCurrentLocked cancels that
	// migration, so ps.fileHashes is left as the empty placeholder). On the
	// next restore, SetProject re-runs loadFileHashes to re-settle the sidecar
	// (re-triggering the migration when the collection is non-empty) instead
	// of trusting the empty placeholder — which would make ValidateCollection
	// treat every file as new. Not guarded by mu on its own; it is only read
	// and written while holding the owning Service's mu.
	sidecarReloadOnRestore bool

	// contentlessCh is closed when this branch's one-time content-less
	// migration has settled (completed, nothing to do, or abandoned on
	// park/close/branch switch). WaitContentlessMigration selects on it.
	// nil before the first SwitchBranch; closedChan when no migration is
	// needed (marker present, empty collection, or in-memory state).
	contentlessCh chan struct{}

	// contentlessCancel cancels an in-flight migrateContentless goroutine
	// (on branch switch / project switch / close / rebuild).
	contentlessCancel context.CancelFunc
}

// Service manages chromem-go collections with git-branch awareness,
// readiness state, and vector search capabilities. It also owns a
// per-branch bleve lexical index that is written in lock-step with the
// chromem collection.
type Service struct {
	// current is the active project's state. It is never nil after
	// NewService: switching to No Project installs an empty in-memory state.
	// It is swapped only under mu, so any method holding mu (or RLock) sees
	// a stable state for its whole duration. The lock-free readers
	// (GetCollection, GetDB) observe whichever state is current at that
	// instant — exactly as the pre-park code observed the old fields.
	current *projectState

	// parked is the LRU list of recently-parked project states, oldest
	// first (parked[0] is the eviction victim). Guarded by mu. Bounded by
	// parkCapacity; when parkCapacity <= 0 parking is disabled and this stays
	// empty (every switch closes the outgoing state, the historical
	// behaviour).
	parked []*projectState

	// parkCapacity is the maximum number of parked states retained. Resolved
	// from vector_index.park_capacity (default 3). 0 (or negative) disables
	// parking. See ServiceConfig.ParkCapacity.
	parkCapacity int

	// parkBudgetBytes is the cumulative byte budget for the park LRU
	// (vector_index.park_budget_mb → bytes). 0 (or negative) disables
	// budget-based eviction — parkCapacity alone bounds the LRU. See
	// ServiceConfig.ParkBudgetBytes.
	parkBudgetBytes int64

	embeddingFunc chromem.EmbeddingFunc
	batchEmbedder BatchEmbedder
	// migrationWG lets Close wait for the migration goroutine(s) of the
	// current and any parked state to unwind before closing resources.
	migrationWG sync.WaitGroup
	mu          sync.RWMutex
	ready       atomic.Bool
	readyCh     chan struct{} // closed when ready becomes true; recreated on false
	readyMu     sync.Mutex    // protects readyCh swaps + readyGen

	// pendingNoProjectReset holds the id of a No Project reset that was
	// requested while the write lock was held by an in-flight SetProject (a
	// persistent DB open). Nil when none is queued. Atomic because the requester
	// records it WITHOUT taking s.mu — taking it is exactly what it cannot do.
	// SetProject applies (and clears) it as its final act, still under the lock.
	// See ResetForNoProject.
	pendingNoProjectReset atomic.Pointer[string]
	// readyGen is bumped every time SetReady(false) / MarkNotReady is called.
	// An indexing pass captures the gen at start (via MarkNotReady) and
	// passes it to RestoreReady on exit; if a project switch (or any other
	// SetReady(false)) has intervened, the gen no longer matches and
	// RestoreReady is a no-op. This prevents a stale indexer from an
	// outgoing project — whose defer runs late after cancellation — from
	// prematurely marking a freshly-switched project's service as ready.
	readyGen  int64
	logger    *slog.Logger
	telemetry *Telemetry

	// hybridConfig holds resolved RRF tuning + pre-fusion score
	// thresholds. Threshold fields of 0 mean "disabled".
	hybridConfig HybridConfig

	// maxFileSize is the upper bound on a file's size for indexing. See
	// ServiceConfig.MaxFileSize.
	maxFileSize int64

	// maxChunkSize is the resolved vector_index.max_chunk_size used to bound
	// reconstructed chunk content (see contentResolver.maxChunkSize). See
	// ServiceConfig.MaxChunkSize.
	maxChunkSize int

	// maxChunksPerFile is the resolved vector_index.max_chunks_per_file, the
	// per-file chunk cap the indexer enforces. The content-less migration's
	// legacy-entry probe uses it as its scan bound so no committed chunk is
	// missed (see migrateContentless). See ServiceConfig.MaxChunksPerFile.
	maxChunksPerFile int

	// embeddingBatchSize is the resolved batch row capacity for the
	// batched-embedding path. See ServiceConfig.EmbeddingBatchSize.
	embeddingBatchSize int

	// embeddingCacheFingerprint, embeddingDimension and embeddingCacheMaxBytes
	// configure the per-project embedding cache held in projectState; it is
	// (re)created whenever a fresh project state is opened, not on a restore.
	embeddingCacheFingerprint string
	embeddingDimension        int
	embeddingCacheMaxBytes    int64

	// chunkerFingerprint is the resolved chunker-configuration fingerprint
	// embedded in new sidecar entries (see ServiceConfig.ChunkerFingerprint
	// and the ChunkerFingerprint helper). Never empty: NewService defaults
	// it to the package-default configuration.
	chunkerFingerprint string

	// contentFilter is the resolved pre-chunk content policy used by
	// ValidateCollection to keep skipped files out of new/stale sets the
	// same way processFile keeps them out of the collection.
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
	// current starts as an empty in-memory state so every accessor (including
	// the lock-free GetCollection/GetDB) is safe before the first SetProject.
	s.current = &projectState{}
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
		// actually chunks with: when an explicit ContentFilter was supplied
		// (but no fingerprint), derive it from that policy instead of the
		// package default, so sidecar entries stay consistent with the
		// effective skip rules. Production wiring (NewManager) always
		// passes both fields derived from the same resolved config.
		s.chunkerFingerprint = ChunkerFingerprint(
			DefaultMaxChunkSize, DefaultChunkOverlap,
			resolveContentFilterConfig(cfg.ContentFilter).Fingerprint(),
		)
	}
	s.contentFilter = resolveContentFilterConfig(cfg.ContentFilter)

	return s, nil
}

// SetProject switches to a project directory, creating a project-specific
// subdirectory for persistence and initializing the chromem-go DB.
//
// Instead of unconditionally tearing the previous project down, the outgoing
// state is parked (see parkCurrentLocked): a later call with the SAME
// projectID and path restores it from RAM — chromem DB, lexical index and
// file-hash sidecar intact — so a project round-trip stops paying the full
// chromem gob-decode. When parking is disabled (park_capacity <= 0) or the
// outgoing state is not a real project, it is closed, reproducing the
// historical reopen-on-every-switch behaviour.
func (s *Service) SetProject(projectID, fullPath string, embeddingCachePaths ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Apply — as the last act of this open, while the lock is still held — a
	// No Project reset that was requested while the open was in flight. It is
	// registered AFTER the Unlock defer, so it runs BEFORE the lock is
	// released; the state this call installed is therefore overwritten by the
	// reset, which is precisely what the requester asked for (see
	// ResetForNoProject).
	defer s.applyPendingNoProjectResetLocked()

	var embeddingCachePath string
	if len(embeddingCachePaths) > 0 {
		embeddingCachePath = embeddingCachePaths[0]
	}

	s.SetReady(false)

	// Retire the outgoing state: park it (best case) or close it.
	s.parkCurrentLocked()

	// Fast path: restore a parked state for the same project and path. This
	// skips newPersistentDB entirely — no gob-decode, and the collection and
	// lexical index are the very same objects that were live before.
	if ps := s.takeParkedLocked(projectID, fullPath); ps != nil {
		s.current = ps
		if ps.sidecarReloadOnRestore {
			// The sidecar backfill was cancelled when this state was parked
			// (see parkCurrentLocked); re-settle it — a disk read, or a fresh
			// background migration when the collection is non-empty and no
			// sidecar exists — so ValidateCollection sees a complete hash map
			// rather than re-embedding every file. loadFileHashes settles the
			// content-less migration itself (deferred
			// maybeMigrateContentlessLocked), so the explicit call below is
			// skipped on this path to avoid cancelling and restarting the
			// migration it just started.
			ps.sidecarReloadOnRestore = false
			s.loadFileHashes()
		} else {
			// A content-less migration cancelled by the park must replay: the
			// marker is only written on full completion, and SwitchBranch on the
			// restored state early-returns (same branch, live collection), so
			// this is the re-entry point. No-op when the marker already exists
			// or the collection is empty.
			s.maybeMigrateContentlessLocked()
		}
		s.logger.Info("project restored from park", "projectID", projectID)
		return nil
	}

	// Slow path: open a fresh state (the existing behaviour). s.current stays
	// non-nil on every return path (an empty state on error) so the lock-free
	// accessors never see a nil current.
	ps := &projectState{projectID: projectID, projectPath: fullPath}
	if fullPath != "" {
		if err := os.MkdirAll(fullPath, 0o750); err != nil {
			s.current = &projectState{}
			return fmt.Errorf("creating project directory %s: %w", fullPath, err)
		}
		db, err := newPersistentDB(fullPath, false)
		if err != nil {
			s.current = &projectState{}
			return fmt.Errorf("opening persistent DB at %s: %w", fullPath, err)
		}
		ps.db = db
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
		// Seed the cache's byte accounting off the service write lock: the
		// first walk of a warm cache is a full directory traversal, and doing
		// it synchronously here would stall every search behind s.mu for the
		// duration. The background seed evicts/reconciles exactly like a
		// synchronous prune; until it lands, prunes from the embed path take
		// the deferred fast path (seedAccountingAsync).
		ps.embeddingCache.seedAccountingAsync()
	} else {
		ps.db = chromem.NewDB()
	}
	s.current = ps

	s.logger.Info("project set for vector index", "projectID", projectID)
	return nil
}

// ResetForNoProject retires the current project's state and installs the empty
// in-memory No Project state, WITHOUT waiting for an in-flight persistent DB
// open to release the write lock.
//
// The reset itself is cheap — park/close the outgoing state, then an in-memory
// chromem DB — but it needs the write lock, and SetProject holds that lock for
// the entire chromem gob-decode, which is minutes on a large index (tens of
// thousands of documents per branch collection). Blocking here used to wedge
// the caller's project switch (Manager.SwitchProject) for that whole window;
// the backend's switchMu was held behind it, so every CHAT/CODE toggle failed
// after its bounded acquire — the "clicking CHAT does nothing for minutes"
// failure. So: try the lock, and when it is busy record the request and return
// at once; the in-flight SetProject applies it as its last act.
//
// Readiness is dropped by the caller (Manager.SwitchProject) before this call,
// so a search issued in the window before the queued reset lands never serves
// the outgoing project's collection.
func (s *Service) ResetForNoProject(projectID string) {
	if !s.mu.TryLock() {
		id := projectID
		s.pendingNoProjectReset.Store(&id)
		return
	}
	defer s.mu.Unlock()
	s.pendingNoProjectReset.Store(nil)
	s.resetForNoProjectLocked(projectID)
}

// applyPendingNoProjectResetLocked applies a queued No Project reset. The
// caller must hold s.mu (SetProject's deferred call does).
func (s *Service) applyPendingNoProjectResetLocked() {
	id := s.pendingNoProjectReset.Swap(nil)
	if id == nil {
		return
	}
	s.resetForNoProjectLocked(*id)
}

// resetForNoProjectLocked installs the empty in-memory No Project state — the
// same state SetProject(NoProjectID, "") produced: parking disabled, no
// persistent directory, an in-memory chromem DB, no collection and no lexical
// index. The caller must hold s.mu.
func (s *Service) resetForNoProjectLocked(projectID string) {
	s.SetReady(false)
	s.parkCurrentLocked()
	ps := &projectState{projectID: projectID}
	ps.db = chromem.NewDB()
	s.current = ps
	s.logger.Info("project set for vector index", "projectID", projectID)
}

// parkCurrentLocked retires the current project state. The caller must hold
// s.mu. It flushes the outgoing branch's file-hash sidecar (skipped while a
// background migration is pending, exactly as SetProject did before), cancels
// any in-flight sidecar migration, and then either parks the state — keeping
// its chromem DB and lexical index resident — or closes it:
//   - parking disabled (parkCapacity <= 0) or the state is not a real project
//     (the empty in-memory No-Project state) → close it;
//   - otherwise → push it onto the LRU, evicting the oldest state(s) when the
//     capacity is exceeded and then, while the summed estimated resident bytes
//     of the parked states exceed the byte budget, oldest-first until the sum
//     fits (a state larger than the whole budget evicts everything, itself
//     included).
//
// An actual eviction (by capacity or budget) schedules the freeOSMemory seam
// asynchronously so the freed RAM is returned to the OS promptly.
//
// After the call s.current holds a fresh empty state (never nil, so the
// lock-free GetCollection/GetDB readers can never dereference nil);
// SetProject installs the incoming state.
func (s *Service) parkCurrentLocked() {
	ps := s.current
	// Install an empty placeholder for the (brief, mu-protected) window until
	// SetProject assigns the incoming state. Never leave s.current nil: the
	// lock-free accessors do not take mu and would fault.
	s.current = &projectState{}
	if ps == nil {
		return
	}

	// Persist the outgoing project's in-memory hashes before parking/closing,
	// and cancel any in-flight sidecar migration. Skip the save while a
	// background migration is pending: loadFileHashes seeds ps.fileHashes as an
	// empty map during a migration, so persisting it would write an empty
	// sidecar for the outgoing branch — the next open would then hit the
	// (empty) sidecar fast path and re-embed every file. Mark the state so the
	// next restore re-settles the sidecar (loadFileHashes) instead of trusting
	// the placeholder.
	if ps.fileHashes != nil && ps.currentBranch != "" {
		if ps.fileHashMigrationPending.Load() || ps.fileHashMigrationFailed.Load() {
			// Either a migration is still in flight, or the last one failed and
			// ps.fileHashes is an untrusted empty placeholder: never persist it
			// (an empty sidecar on disk would be trusted by the next open and
			// re-embed every file); re-settle on restore instead.
			ps.sidecarReloadOnRestore = true
		} else if err := ps.saveFileHashes(); err != nil {
			s.logger.Warn("failed to persist file-hash sidecar on project switch", "error", err)
		}
	}
	if ps.migrationCancel != nil {
		ps.migrationCancel()
		ps.migrationCancel = nil
	}
	// A parked state's in-flight content-less migration is abandoned the
	// same way: it re-triggers on the next restore (SetProject) or open
	// (loadFileHashes → maybeMigrateContentlessLocked). Its per-window
	// flushes take s.mu.RLock, so they cannot interleave with this critical
	// section.
	if ps.contentlessCancel != nil {
		ps.contentlessCancel()
		ps.contentlessCancel = nil
	}

	if s.parkCapacity <= 0 || ps.projectPath == "" {
		// Parking disabled, or nothing worth keeping (the empty in-memory
		// No-Project state): close immediately.
		s.closeStateLocked(ps)
		return
	}

	s.parked = append(s.parked, ps)
	// Evict least-recently-parked states: first beyond the LRU capacity,
	// then — while the cumulative estimated footprint of the parked states
	// exceeds the byte budget — oldest-first until the sum fits. The
	// effective bound is min(capacity, budget); the victim is always
	// parked[0], the oldest.
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
		// The freed states can hold hundreds of MiB of vectors and lexical
		// index entries. Return them to the OS promptly: debug.FreeOSMemory
		// forces a full GC cycle (stop-the-world), so run it in its own
		// goroutine — the func value is captured now, the goroutine never
		// takes s.mu, and the lock is released the moment
		// parkCurrentLocked returns.
		go freeOSMemory()
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
// (projectID, fullPath), or nil when none matches. The caller must hold s.mu.
// A parked entry for the same projectID but a DIFFERENT path is stale (the
// project was moved or its storage directory recreated), so it is evicted
// rather than restored.
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

// closeStateLocked releases the file handles (bleve lexical index + chromem
// DB) held by a project state and drops its in-memory references. The caller
// must hold s.mu. It deliberately does NOT flush the sidecar (parkCurrentLocked
// and evictLocked do) and leaves goroutine-lifetime tracking (migrationWG) to
// the caller.
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

// Browse returns up to topK chunks from the current collection without semantic
// ordering. It enumerates documents with a fixed unit-vector query (no ONNX
// inference) when the embedding dimension is known, falling back to the space
// query otherwise. Blocks via WaitReady if the index is not yet ready.
func (s *Service) Browse(ctx context.Context, topK int) ([]SearchResult, error) {
	return s.BrowseWithFilter(ctx, topK, "")
}

// BrowseWithFilter returns up to topK chunks from the current collection without
// semantic ordering, optionally filtered by a file path glob pattern.
// Blocks via WaitReady if the index is not yet ready.
func (s *Service) BrowseWithFilter(ctx context.Context, topK int, fileFilter string) ([]SearchResult, error) {
	return s.browseWithFilter(ctx, topK, fileFilter, true)
}

// BrowseWithFilterNoWait is the non-blocking form of BrowseWithFilter: it
// never waits for readiness and returns ErrNotReady immediately when the
// index is not ready. Callers that gate readiness themselves (fail-fast
// search wiring) use this form so a pass that starts between their gate and
// the call cannot block them until the pass finishes.
func (s *Service) BrowseWithFilterNoWait(ctx context.Context, topK int, fileFilter string) ([]SearchResult, error) {
	return s.browseWithFilter(ctx, topK, fileFilter, false)
}

// unitQueryVector returns a unit vector along the first axis of the
// configured embedding dimension, for embedding-free enumeration queries
// (chromem QueryEmbedding), or nil when the dimension is unknown (0) — in
// which case callers keep the embedding-bearing text-query path. Browse-style
// callers promise no semantic ordering, so the arbitrary-but-deterministic
// ranking a unit vector induces is equivalent to the space-query it replaces.
// The dimension is fixed at construction (never mutated), so no lock is
// required.
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

	// Embedding-free enumeration: with a known embedding dimension the query
	// vector can be a fixed unit vector instead of embedding " ", removing
	// one ONNX inference per Browse. (QueryEmbedding also skips the query
	// text entirely.) Ranking under a unit vector is arbitrary — Browse
	// promises no semantic ordering — exactly like the space-query it
	// replaces; the fileFilter below still narrows the returned set.
	// dim == 0 (dimension unknown) keeps the embedding-bearing path.
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

	// Committed documents store no chunk text; fill Content from the source
	// files for the (already topK-bounded) returned set. Hydration reads source
	// files, so it runs AFTER the read lock is released — holding it would
	// block a concurrent indexing writer for the duration of the reads. One
	// resolver per call, so each distinct file is read at most once within the
	// cache budget.
	hydrateSearchContent(out, newContentResolver(s.maxFileSize, s.maxChunkSize))

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

// ErrNotReady is returned by the NoWait search variants (HybridSearchNoWait,
// BrowseWithFilterNoWait) when the index is not currently ready. Callers that
// manage their own bounded readiness wait (the vector_index.
// search_wait_timeout_ms wiring) use the NoWait forms so an incremental pass
// starting between their readiness gate and the search can never block them;
// they typically translate this sentinel into an actionable error via
// Manager.NotReadyError so users learn the index progress instead.
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

// GetDB returns the underlying chromem-go DB (for use by Indexer). It is
// lock-free on purpose: GetDB is called from the Indexer while it already
// holds the service lock, so taking the lock here would deadlock. It reads
// the current state pointer once and tolerates a nil current defensively.
func (s *Service) GetDB() *chromem.DB {
	ps := s.current
	if ps == nil {
		return nil
	}
	return ps.db
}

// GetCollection returns the current collection (for use by Indexer). Like
// GetDB it is intentionally lock-free (the Indexer calls it while holding the
// service lock); s.current is never nil after NewService, but the pointer is
// read once and nil-checked defensively.
func (s *Service) GetCollection() *chromem.Collection {
	ps := s.current
	if ps == nil {
		return nil
	}
	return ps.collection
}

// GetLexical returns the current lexical index (may be nil for in-memory
// mode or before a branch is opened).
func (s *Service) GetLexical() lexical.Index {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.lexical
}

// LexicalCount returns the number of documents in the lexical index, or
// 0 if no lexical index is currently open.
func (s *Service) LexicalCount() (uint64, error) {
	s.mu.RLock()
	lex := s.current.lexical
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
	return s.current.currentBranch
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
	// The same applies to a PARKED state for this project: its handles are
	// still open, so it must be dropped too (otherwise a later switch back
	// would restore a collection rooted at a deleted directory).
	target := filepath.Clean(fullPath)
	s.mu.Lock()
	if cur := s.current; cur != nil && filepath.Clean(cur.projectPath) == target {
		if cur.migrationCancel != nil {
			cur.migrationCancel()
			cur.migrationCancel = nil
		}
		s.closeStateLocked(cur)
		// Reset rather than leaving the closed state installed: closeStateLocked
		// drops the handles but keeps projectID/projectPath, and
		// parkCurrentLocked parks any state with a non-empty path — the next
		// SetProject would park this dead state into the LRU, wasting a park
		// slot and, for any future deterministic id+path reuse, restoring a
		// db-less state whose SwitchBranch fails ("no database initialized").
		// The empty placeholder mirrors parkCurrentLocked's reset state and is
		// never parked or restorable.
		s.current = &projectState{}
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
	// Close every parked state too: their chromem DB and lexical handles are
	// still resident. Their sidecars were already flushed at park time (no
	// indexing runs on a parked state), so we only cancel any migration and
	// release the handles.
	for _, ps := range s.parked {
		if ps.migrationCancel != nil {
			ps.migrationCancel()
			ps.migrationCancel = nil
		}
		s.closeStateLocked(ps)
	}
	s.parked = nil
	s.mu.Unlock()

	// Wait for any in-flight migration goroutine (current or parked) to
	// unwind (it aborts after seeing the cancellation / nil collection above).
	// Must happen AFTER releasing the lock, or the goroutine would deadlock
	// waiting for it.
	s.migrationWG.Wait()
	s.logger.Info("vector index service closed")
	return nil
}

// resultToSearchResult converts a chromem-go Result to a SearchResult.
// Content is passed through as-is: committed documents store no chunk text
// (see strippedForCommit), so the caller hydrates the final top-K from the
// source files via hydrateSearchContent. FileName is derived from FilePath —
// documents no longer store a file_name metadata field.
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
