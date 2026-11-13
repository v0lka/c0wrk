// Package vectorindex provides vector-based code search for the desktop UI.
// It combines an ONNX-powered embedding index (chromem-go) with a lexical
// (bleve-based) text search index that share a unified indexing pipeline.
package vectorindex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
	"github.com/v0lka/sp4rk/ignore"
	"github.com/v0lka/sp4rk/safeio"
)

// IndexState represents the current state of the indexer.
type IndexState string

const (
	// IndexStateIndexing indicates a full indexing operation is in progress.
	IndexStateIndexing IndexState = "indexing"
	// IndexStateReindexing indicates an incremental reindexing operation is in progress.
	IndexStateReindexing IndexState = "reindexing"
	// IndexStateReady indicates the index is ready for queries.
	IndexStateReady IndexState = "ready"
	// IndexStateUnavailable indicates vector indexing is unavailable for the
	// active project (a failed open/init, or a failed pass with no collection
	// left to fall back on); search returns a clean "no collection" error and
	// the UI renders the state honestly — reindex disabled — until a branch
	// change or project switch re-opens the collection. See
	// specs/domains/workspace.md (Vector Index).
	IndexStateUnavailable IndexState = "unavailable"
	// IndexStateLoading indicates the branch-scoped persistent DB is being
	// OPENED (ADR-064) before any indexing pass has started: no progress
	// fraction, rendered as a plain "Preparing index…", never the
	// indexing/building shape. See specs/domains/workspace.md.
	IndexStateLoading IndexState = "loading"
	// IndexStatePreparing indicates the view-model attach window (ADR-104
	// stage 2): snapshot identity resolution, corpus lease acquisition or
	// the bounded wait for an in-flight corpus build — before anything is
	// opened or indexed. Distinct from loading (a store decode) and from
	// indexing (a pass): the UI renders an honest "preparing" state.
	IndexStatePreparing IndexState = "preparing"
	// IndexStateMigrating indicates the one-time store promotion of a
	// legacy branch-keyed store into the shared snapshot corpus
	// (migration by rename, no re-embed) — or, when nothing compatible
	// exists, the rebuild pass that replaces it. Distinct from indexing:
	// the work is store-shape repair, not fresh content.
	IndexStateMigrating IndexState = "migration-rebuild"
)

// IndexPhase describes which side(s) of the dual index are being produced
// for a given progress event; PhaseLexical distinguishes the lazy BM25
// backfill (RebuildLexical) from a cold-start embedding pass. The phase is
// surfaced via the vector_index:status event. See specs/domains/workspace.md.
type IndexPhase string

const (
	// PhaseBoth indicates progress for combined vector + lexical indexing.
	PhaseBoth IndexPhase = "both"
	// PhaseEmbedding indicates progress for vector embedding only.
	PhaseEmbedding IndexPhase = "embedding"
	// PhaseLexical indicates progress for lexical (BM25) indexing only.
	PhaseLexical IndexPhase = "lexical"
	// PhaseOpen accompanies IndexStateLoading: the branch-scoped persistent DB
	// is being opened, before any indexing pass exists.
	PhaseOpen IndexPhase = "open"
	// PhasePreparation accompanies IndexStatePreparing: the view-model
	// attach window (snapshot resolution, corpus lease) before any open.
	PhasePreparation IndexPhase = "preparation"
	// PhaseMigration accompanies IndexStateMigrating: a legacy branch store
	// is being promoted into the shared corpus, or rebuilt for it.
	PhaseMigration IndexPhase = "migration-rebuild"
)

// addDocumentBatchSize is the number of documents added per batch call.
const addDocumentBatchSize = 50

// ProgressCallback is called to report indexing progress. The phase
// argument identifies which side of the dual index the event relates to.
type ProgressCallback func(phase IndexPhase, state IndexState, filesIndexed, totalFiles int, currentFile string)

// ChunkResult represents a chunk of file content produced by a chunker.
type ChunkResult struct {
	Content   string
	StartLine int
	EndLine   int
	Language  string
}

// ChunkFunc chunks file content into pieces for embedding.
type ChunkFunc func(filePath string, content []byte, maxChunkSize, overlap int) ([]ChunkResult, error)

// HashFunc computes a content hash string.
type HashFunc func(content []byte) string

// IndexerConfig holds configuration for creating an Indexer.
type IndexerConfig struct {
	Service          *Service
	ChunkFn          ChunkFunc
	HashFn           HashFunc
	MaxChunkSize     int
	MaxFileSize      int64
	MaxChunksPerFile int
	Overlap          int
	// ContentFilter controls deterministic early rejection before chunking.
	// Nil selects DefaultContentFilterConfig; a non-nil config is exact.
	ContentFilter *ContentFilterConfig

	// IgnoreFiles selects which ignore files the indexing walks honour:
	// nil (the zero value) = both .gitignore and .aiignore (the historical
	// behaviour); a non-nil slice is the exact honoured set (each entry must
	// be ".gitignore" or ".aiignore" — anything else fails the resolver
	// build and the indexer falls open to the noop checker); an empty
	// non-nil slice honours neither file, which combined with no
	// ExcludePatterns selects the noop checker outright (vector_index.
	// respect_gitignore / respect_aiignore).
	IgnoreFiles []string

	// ExcludePatterns are gitignore-style patterns layered on top of the
	// honoured ignore files, scoped as if written in a root ignore file
	// ("/dist" root-anchored, "*.log" any depth, "build/" dir-only). They
	// are subtractive-only and validated at config load
	// (vector_index.exclude_patterns); files they match never enter a walk,
	// and a previously-indexed file that a new pattern covers is dropped by
	// the next incremental pass (ValidateCollection reports it deleted).
	ExcludePatterns []string

	// IncludeSubmodules controls whether nested-repository directories — a
	// directory whose .git is a regular gitdir-pointer file, the on-disk
	// shape of a checked-out submodule — are indexed. false (the zero
	// value, matching the vector_index.index_submodules default) prunes
	// them from every walk and the watcher's indexability check; true
	// indexes their files like ordinary workspace files (vector_index.
	// index_submodules).
	IncludeSubmodules bool

	// PrepWorkers is the number of goroutines that run file preparation
	// (read/hash/chunk — pure I/O+CPU work) in parallel with the embedding
	// consumer, so one file's ONNX inference overlaps the next file's disk
	// read and chunking. 1 reproduces the historical strictly serial
	// pipeline; 0 (or unset) defaults to DefaultPrepWorkers. It bounds only
	// the prep stage: a single consumer still drives Service.AddDocuments,
	// keeping embedding single-threaded under the service write lock.
	// ChunkFn and HashFn must be safe for concurrent use.
	PrepWorkers int

	// OnStallChange receives every pass-stall watchdog transition (opened by
	// the 5-minute stall threshold, closed by the next progress point) so a
	// status surface can carry the pass-liveness sub-status. Nil = no
	// consumer; the watchdog still logs its evidence Warn.
	OnStallChange func(StallSnapshot)
	OnProgress    ProgressCallback
	Logger        *slog.Logger
	// Telemetry optionally collects bounded, content-free stage aggregates.
	Telemetry *Telemetry
}

// Indexer orchestrates initial and incremental indexing of project files.
type Indexer struct {
	service *Service
	chunkFn ChunkFunc
	hashFn  HashFunc
	// readFileFn and binaryHeaderFn are the read-stage seams the per-file
	// read watchdog wraps (see readwatchdog.go): production wires
	// safeio.ReadFile and isBinaryHeader, and tests substitute a
	// blocking reader to drive the watchdog in virtual time. Both run
	// under fileReadWatchdogTimeout; a stage timeout skips the file and
	// spends one pass read-skip budget allowance.
	readFileFn       func(string) ([]byte, error)
	binaryHeaderFn   func(string) (bool, error)
	maxChunkSize     int
	maxFileSize      int64
	maxChunksPerFile int
	overlap          int
	contentFilter    ContentFilterConfig
	prepWorkers      int
	onProgress       ProgressCallback
	// onStallChange is the IndexerConfig.OnStallChange consumer, handed to
	// every pass watchdog this indexer starts (see startPassWatchdog).
	onStallChange func(StallSnapshot)
	logger        *slog.Logger
	telemetry     *Telemetry
	// stallAfterFunc is an instance-local timer factory for the per-pass
	// stall watchdog; nil uses time.AfterFunc (virtualized under
	// testing/synctest). Tests set it before running a pass.
	stallAfterFunc func(time.Duration, func()) *time.Timer
	// ignoreOptions is the resolved ignore policy (honoured ignore files +
	// extra exclude patterns) the checker is built from; includeSubmodules
	// gates nested-repository directories in the walks and the watcher's
	// indexability check. Both are fixed for the Indexer's lifetime.
	ignoreOptions     ignore.Options
	includeSubmodules bool

	// ignoreMu guards ignoreRoot / ignoreChecker, the per-project cache of
	// the workspace's ignore.Resolver (.gitignore + .aiignore, root and
	// nested), so the walk and the watcher filter share one source of truth
	// instead of re-walking per debounce flush; the Indexer — and this cache
	// with it — is recreated on SwitchProject.
	ignoreMu      sync.RWMutex
	ignoreRoot    string
	ignoreChecker ignore.IgnoreChecker
}

// NewIndexer creates a new Indexer with the given configuration.
func NewIndexer(cfg IndexerConfig) *Indexer {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	maxChunkSize := cfg.MaxChunkSize
	if maxChunkSize <= 0 {
		maxChunkSize = DefaultMaxChunkSize
	}
	maxFileSize := cfg.MaxFileSize
	if maxFileSize <= 0 {
		maxFileSize = DefaultMaxIndexableFileSize
	}
	maxChunksPerFile := cfg.MaxChunksPerFile
	if maxChunksPerFile <= 0 {
		maxChunksPerFile = DefaultMaxChunksPerFile
	}
	overlap := cfg.Overlap
	if overlap <= 0 {
		overlap = DefaultChunkOverlap
	}
	contentFilter := resolveContentFilterConfig(cfg.ContentFilter)
	prepWorkers := cfg.PrepWorkers
	if prepWorkers <= 0 {
		prepWorkers = DefaultPrepWorkers
	}
	hashFn := cfg.HashFn
	if hashFn == nil {
		hashFn = computeHash
	}
	readFileFn := safeio.ReadFile
	binaryHeaderFn := isBinaryHeader
	onProgress := cfg.OnProgress
	if onProgress == nil {
		onProgress = func(IndexPhase, IndexState, int, int, string) {}
	}
	return &Indexer{
		service:          cfg.Service,
		chunkFn:          cfg.ChunkFn,
		hashFn:           hashFn,
		readFileFn:       readFileFn,
		binaryHeaderFn:   binaryHeaderFn,
		maxChunkSize:     maxChunkSize,
		maxFileSize:      maxFileSize,
		maxChunksPerFile: maxChunksPerFile,
		overlap:          overlap,
		contentFilter:    contentFilter,
		prepWorkers:      prepWorkers,
		onProgress:       onProgress,
		onStallChange:    cfg.OnStallChange,
		logger:           logger,
		telemetry:        cfg.Telemetry,
		ignoreOptions: ignore.Options{
			IgnoreFiles:   cfg.IgnoreFiles,
			ExtraPatterns: cfg.ExcludePatterns,
		},
		includeSubmodules: cfg.IncludeSubmodules,
	}
}

// IndexFull performs a full workspace indexing: walks all files, chunks them,
// and adds the resulting documents to both the vector collection and the
// lexical index.
func (idx *Indexer) IndexFull(ctx context.Context, workspacePath string) (err error) {
	// Bind the walked tree as the service's workspace root (ADR-104 L4):
	// every occurrence coordinate this pass writes — document IDs, sidecar
	// keys, lexical IDs — is relative to it, and ValidateCollection /
	// deletion re-derive against the same binding. Production wires the
	// identical root through Manager.initProject, so this is an anchor of
	// record for standalone-service callers, not a switch.
	idx.service.SetWorkspaceRoot(workspacePath)
	// Readiness gate (ADR-123): the pass owns the readiness cycle unless it
	// entered with the service ALREADY ready over a queryable collection —
	// the watcher-driven mid-session pass — which keeps readiness UP, so
	// searches keep answering from the committed-so-far state between the
	// pass's write windows. A pass entered NOT-ready (fresh project,
	// post-switch reconciliation, corpus build) keeps the historical cycle:
	// MarkNotReady atomically captures a readiness generation; RestoreReady
	// uses it to restore readiness on EVERY exit, including the cancellation
	// and batch-add error paths that once returned without SetReady(true),
	// leaving WaitReady callers blocked forever.
	if !idx.service.IsReady() || !idx.service.queryableCollection() {
		readyGen := idx.service.MarkNotReady()
		defer idx.service.RestoreReady(readyGen)
	}
	// Emit a terminal progress event on any non-nil return so the UI does not
	// keep showing stale pass progress: "ready" when a queryable collection
	// remains, "unavailable" when nothing exists to query. On success the
	// explicit onProgress below handles it. See specs/domains/workspace.md.
	defer func() {
		if err != nil {
			if idx.service.GetCollection() == nil {
				idx.onProgress(PhaseBoth, IndexStateUnavailable, 0, 0, "")
				return
			}
			idx.onProgress(PhaseBoth, IndexStateReady, 0, 0, "")
		}
	}()

	// Pass-level stall watchdog: the walk below is plain filesystem I/O —
	// the stage most exposed to slow or wedged storage — and every later
	// stage heartbeats, so a full passStallTimeout of silence means a wedged
	// pass; the Warn carries the stage, the last file and the goroutine dump
	// naming the blocking call. The walk itself heartbeats per walked entry
	// (throttled): slow-but-alive is never a stall, a hung stat still is.
	wd := idx.startPassWatchdog()
	defer wd.stop()
	wd.watchContext(ctx)
	wd.heartbeat(StageWalkValidation, "")
	walkStarted := time.Now()
	// The branch the pass builds into, sampled BEFORE the walk: the corpus
	// gate reads this pass's traversal gaps from the same store the walk
	// belongs to, so a displaced builder's report can never describe another
	// store's publication (see Service.noteWalkGaps).
	walkBranch := idx.service.CurrentBranchName()
	walk, err := walkProjectFilesDetailed(workspacePath, idx.resolverFor(workspacePath), idx.maxFileSize, idx.includeSubmodules, newWalkHeartbeater(wd, StageWalkValidation).entry)
	idx.telemetry.observe(StageWalkValidation, len(walk.Files), time.Since(walkStarted))
	if err != nil {
		return fmt.Errorf("walking project files: %w", err)
	}
	idx.service.noteWalkGaps(walkBranch, walk.Gaps)
	if len(walk.Gaps) > 0 {
		// ONE aggregated line per pass: a deleted subtree is an ordinary
		// live-workspace event, and per-entry logging would drown the log of
		// a tree that drops many generated directories at once.
		idx.logger.Warn("workspace walk skipped unreadable entries; indexing the reachable files",
			"skipped", len(walk.Gaps), "path", walk.Gaps[0].Path, "error", walk.Gaps[0].Err)
	}
	// Walk completed — the per-entry heartbeats above held the window open
	// however long the tree took; this stamps the stage transition.
	wd.heartbeat(StageWalkValidation, "")
	files := walk.Files

	totalFiles := len(files)
	idx.onProgress(PhaseBoth, IndexStateIndexing, 0, totalFiles, "")
	idx.logger.Info("starting full index", "workspace", workspacePath, "files", totalFiles)

	// Staged acquisition (ADR-123): the pass lease restores the pass-vs-pass
	// exclusion the whole-pass service write hold used to provide, and every
	// mutating step below commits through a short identity-checked window —
	// so search, status and Close are never frozen by the pass. The stall
	// watchdog suspends across the lease acquisition: the wait is unbounded
	// (one pass at a time, and a predecessor may legitimately run for many
	// minutes), and a healthy pass queued behind it must not read as
	// stalled — see passWatchdog.suspend.
	wd.suspend()
	pass := idx.beginIndexPass()
	wd.resume()
	defer pass.release()
	pass.wd = wd

	indexed, idxErr := idx.indexPrepared(ctx, files, indexPipelineOpts{
		state:        IndexStateIndexing,
		total:        totalFiles,
		cancelMsg:    "indexing cancelled",
		skipLogLabel: "skipping file",
		root:         workspacePath,
	}, pass)
	if idxErr != nil {
		return idxErr
	}

	idx.onProgress(PhaseBoth, IndexStateReady, totalFiles, totalFiles, "")
	idx.logger.Info("full index complete", "files", indexed)
	return nil
}

// IndexIncremental performs an incremental re-index: validates the current
// collection against disk, then updates only changed/new/deleted files
// across both the vector collection and the lexical index.
func (idx *Indexer) IndexIncremental(ctx context.Context, workspacePath string) (err error) {
	// Same root binding as IndexFull: the pass's coordinate anchor (see
	// IndexFull).
	idx.service.SetWorkspaceRoot(workspacePath)
	// Same conditional readiness gate as IndexFull (ADR-123): a pass entered
	// NOT-ready (post-switch reconciliation) keeps the historical
	// MarkNotReady→RestoreReady cycle, while a pass that found the service
	// already ready over a queryable collection keeps readiness up so
	// searches answer between this pass's windows.
	if !idx.service.IsReady() || !idx.service.queryableCollection() {
		readyGen := idx.service.MarkNotReady()
		defer idx.service.RestoreReady(readyGen)
	}
	// Same terminal progress contract as IndexFull: "ready" vs "unavailable"
	// by collection presence. See specs/domains/workspace.md.
	defer func() {
		if err != nil {
			if idx.service.GetCollection() == nil {
				idx.onProgress(PhaseBoth, IndexStateUnavailable, 0, 0, "")
				return
			}
			idx.onProgress(PhaseBoth, IndexStateReady, 0, 0, "")
		}
	}()

	// Pass-level stall watchdog, same lifecycle as IndexFull: the validation
	// walk below is the first monitored stage — the migration waits before
	// it run suspended (see waitSidecarMigrations).
	wd := idx.startPassWatchdog()
	defer wd.stop()
	wd.watchContext(ctx)
	wd.heartbeat(StageCacheLookup, "")

	if err := idx.waitSidecarMigrations(ctx, wd); err != nil {
		return err
	}

	wd.heartbeat(StageWalkValidation, "")
	validationStarted := time.Now()
	stale, newFiles, deleted, err := idx.service.ValidateCollection(ctx, workspacePath, idx.resolverFor(workspacePath), idx.includeSubmodules, newWalkHeartbeater(wd, StageWalkValidation).entry)
	idx.telemetry.observe(StageWalkValidation, len(stale)+len(newFiles)+len(deleted), time.Since(validationStarted))
	if err != nil {
		return fmt.Errorf("validating collection: %w", err)
	}
	// Validation completed — the sidecar reads answered; adoptions and
	// deletions follow under their own heartbeats.
	wd.heartbeat(StageCacheLookup, "")

	// Corpus adoptions (ADR-104 stage 2): files whose current bytes match the
	// shared corpus were classified corpus-current by ValidateCollection.
	// Persist their sidecar rows — this is what makes the corpus entries
	// VISIBLE to this root — and retire any overlay documents for adopted
	// paths (a reverted file's overlay version is superseded by the corpus
	// one). Nothing here chunks, embeds, or commits: the bounded
	// reconciliation of a clean attach is exactly this plus the walk. This
	// runs BEFORE the no-changes early return — a clean attach has zero
	// overlay changes and its whole value IS the adoption.
	// Staged acquisition (ADR-123): the pass lease restores the pass-vs-pass
	// exclusion the whole-pass service write hold used to provide, and the
	// working-store identity captured here is what every window below
	// re-validates before committing. The stall watchdog suspends across
	// the lease acquisition for the same reason as IndexFull: the unbounded
	// pass-lease queue is not a stall of THIS pass — see
	// passWatchdog.suspend.
	wd.suspend()
	pass := idx.beginIndexPass()
	wd.resume()
	defer pass.release()
	pass.wd = wd

	if adopted := idx.service.TakeCorpusAdoptions(); len(adopted) > 0 {
		adoptPaths := make([]string, 0, len(adopted))
		for rel := range adopted {
			adoptPaths = append(adoptPaths, filepath.Join(workspacePath, filepath.FromSlash(rel)))
		}
		// Retire the superseded overlay documents FIRST and persist the
		// adoption only on success: the sidecar row is what makes the
		// corpus entry visible, and a landed row whose stale overlay
		// duplicate survived a transient delete failure would never be
		// re-driven (the stat fast-path skips corpus reconciliation), so
		// the file would serve twice forever. Deferring instead is safe —
		// the classification is content-based, so the next
		// ValidateCollection re-records and retries the adoption.
		pass.wd.heartbeat(StageChromemCommit, "")
		if err := pass.window("persisting corpus adoptions", func() error {
			retired := true
			if ids, idErr := idx.collectDocumentIDs(ctx, adoptPaths); idErr != nil {
				idx.logger.Warn("collecting superseded overlay document IDs failed; adoption deferred to the next pass", "error", idErr)
				retired = false
			} else if len(ids) > 0 {
				if delErr := idx.service.DeleteDocumentsByIDs(ctx, ids); delErr != nil {
					idx.logger.Warn("retiring superseded overlay documents failed; adoption deferred to the next pass", "error", delErr)
					retired = false
				}
			}
			if retired {
				// The retired paths' §3 occurrence rows follow their documents
				// out (the same coupling removeFileHashes applies for deleted
				// files): surviving rows would forever out-count the collection
				// and wedge the overlay table's completeness-gated flush.
				idx.service.dropChunkRowsLocked(adoptPaths)
				if err := idx.service.adoptCorpusFilesLocked(adopted); err != nil {
					idx.logger.Warn("persisting corpus adoptions into the sidecar failed", "error", err)
				} else {
					idx.logger.Info("incremental index: adopted corpus-current files", "count", len(adopted))
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}

	totalChanges := len(stale) + len(newFiles) + len(deleted)
	if totalChanges == 0 {
		idx.logger.Info("incremental index: no changes detected")
		idx.onProgress(PhaseBoth, IndexStateReady, 0, 0, "")
		return nil
	}

	idx.logger.Info("incremental index starting",
		"stale", len(stale), "new", len(newFiles), "deleted", len(deleted))

	if len(stale) > 0 {
		idx.logger.Info("stale files detected", "files", stale)
	}
	if len(newFiles) > 0 {
		idx.logger.Info("new files detected", "files", newFiles)
	}
	if len(deleted) > 0 {
		idx.logger.Info("deleted files detected", "files", deleted)
	}

	idx.onProgress(PhaseBoth, IndexStateReindexing, 0, totalChanges, "")

	// Delete documents for deleted and stale files — ONE identity-checked
	// window: enumeration, the chromem+lexical deletion and the sidecar drop
	// land together, atomic against searches and against a displacement
	// (ADR-123).
	filesToDelete := make([]string, 0, len(deleted)+len(stale))
	filesToDelete = append(filesToDelete, deleted...)
	filesToDelete = append(filesToDelete, stale...)

	if len(filesToDelete) > 0 {
		pass.wd.heartbeat(StageChromemCommit, "")
		if err := pass.window("deleting stale documents", func() error {
			ids, idErr := idx.collectDocumentIDs(ctx, filesToDelete)
			if idErr != nil {
				return fmt.Errorf("collecting document IDs for deletion: %w", idErr)
			}
			if len(ids) > 0 {
				if delErr := idx.service.DeleteDocumentsByIDs(ctx, ids); delErr != nil {
					return fmt.Errorf("deleting stale documents: %w", delErr)
				}
			}
			// Drop truly-deleted files from the file-hash sidecar. Stale files
			// remain in the sidecar and are refreshed by the upsert during re-index.
			if len(deleted) > 0 {
				idx.service.removeFileHashes(deleted)
			}
			return nil
		}); err != nil {
			return err
		}
		idx.onProgress(PhaseBoth, IndexStateReindexing, len(deleted), totalChanges, "")
	}

	// Re-index stale + new files.
	filesToIndex := make([]string, 0, len(stale)+len(newFiles))
	filesToIndex = append(filesToIndex, stale...)
	filesToIndex = append(filesToIndex, newFiles...)

	// progressStart counts the deletions already processed above; countSkipped
	// keeps the incremental denominator reaching totalChanges (see the field
	// docs on indexPipelineOpts).
	if _, perr := idx.indexPrepared(ctx, filesToIndex, indexPipelineOpts{
		state:         IndexStateReindexing,
		total:         totalChanges,
		progressStart: len(deleted),
		countSkipped:  true,
		cancelMsg:     "incremental indexing cancelled",
		skipLogLabel:  "skipping file during incremental index",
		batchLogLabel: " (incremental)",
		root:          workspacePath,
	}, pass); perr != nil {
		return perr
	}

	var totalInIndex int
	if err := pass.window("reading the final file count", func() error {
		totalInIndex = idx.service.collectionUniqueFileCount()
		return nil
	}); err != nil {
		return err
	}
	idx.onProgress(PhaseBoth, IndexStateReady, totalChanges, totalInIndex, "")
	idx.logger.Info("incremental index complete", "changes", totalChanges, "totalFiles", totalInIndex)
	return nil
}

// HandleBranchSwitch handles a git branch change by switching the collection
// and re-indexing as needed. It reports whether the switch ran a FULL
// indexing pass (the empty-target-branch case); every other switch — including
// the same-branch no-op where SwitchBranch early-returns — reconciles
// incrementally and reports false, so callers can skip the post-full-pass
// memory scavenge (Manager.freeOSMemoryAfterFullIndex).
func (idx *Indexer) HandleBranchSwitch(ctx context.Context, workspacePath, newBranch string) (bool, error) {
	// MarkNotReady captures the gen so RestoreReady (called by the delegated
	// IndexFull/IndexIncremental's own defer) only restores readiness if no
	// SetReady(false) has intervened; SwitchBranch failure returns before the
	// delegation, so this defer is the only restore on that path.
	readyGen := idx.service.MarkNotReady()
	defer idx.service.RestoreReady(readyGen)

	// View model (ADR-104 stage 2): a branch change re-resolves the SNAPSHOT
	// (the new HEAD's commit key — possibly the same corpus another root
	// already built), re-attaches the read-only corpus, and reconciles the
	// root's overlay. The working store stays the root's overlay throughout;
	// the legacy branch store is never opened as a working store again.
	if idx.service.CorpusEnabled() {
		if err := idx.AttachSnapshot(ctx, workspacePath, newBranch); err != nil {
			idx.onProgress(PhaseBoth, IndexStateUnavailable, 0, 0, "")
			return false, err
		}
		return false, idx.IndexIncremental(ctx, workspacePath)
	}

	// ADR-064 made a branch switch a real open: SwitchBranch releases the
	// outgoing branch's residents and gob-decodes the target branch's own
	// chromem DB (seconds on a large branch), so announce the honest pre-open
	// state for that window, exactly as initProject does. The guard is the
	// exact negation of SwitchBranch's early return, so it fires precisely
	// for the calls that really open — never the same-branch no-op — and
	// still covers a retry after a FAILED open, which leaves currentBranch
	// naming a branch whose collection is gone; both reads are harmless if
	// another switch interleaves (it announces for itself). A FAILED open
	// emits the terminal `unavailable` below. See specs/domains/workspace.md
	// (pre-open status).
	if idx.service.CurrentBranchName() != newBranch || idx.service.GetCollection() == nil {
		idx.onProgress(PhaseOpen, IndexStateLoading, 0, 0, "")
	}

	if err := idx.service.SwitchBranch(ctx, newBranch); err != nil {
		idx.onProgress(PhaseBoth, IndexStateUnavailable, 0, 0, "")
		return false, fmt.Errorf("switching branch to %q: %w", newBranch, err)
	}

	idx.logger.Info("branch switched, checking collection", "branch", newBranch)

	col := idx.service.GetCollection()
	if col == nil || col.Count() == 0 {
		idx.logger.Info("empty collection for branch, running full index", "branch", newBranch)
		if err := idx.IndexFull(ctx, workspacePath); err != nil {
			return false, err
		}
		return true, nil
	}

	return false, idx.IndexIncremental(ctx, workspacePath)
}

// AttachSnapshot drives the view-model attach of one workspace tree (ADR-104
// stage 2): resolve the tree's snapshot identity (the HEAD commit OID — the
// root-owned synthetic id for non-git trees), drive the snapshot's corpus to
// a published state under the registry's single-flight lease (building it
// with this indexer's full pass when nobody has, promoting a compatible
// legacy branch store by rename when one exists), then bind the service to
// the shared read-only corpus plus the root's private overlay.
//
// Status honesty (preparation/migration-rebuild/indexing) rides the ordinary
// progress callback; every failure is terminal for the attach — the caller
// surfaces unavailable and NOTHING of another snapshot is ever served
// (fail-closed).
//
// Self-healing (specs/domains/workspace.md, shared corpus): an attach that
// fails with a PROVEN store defect quarantines the affected store and retries
// EXACTLY ONCE — a missing lexical leg, a torn record or a corrupt store root
// heals without user involvement, while a transient OS fault (permissions, no
// space, descriptors, I/O, a cancelled operation) leaves the publication
// intact and reports unavailable with its cause. "Exactly once" is structural:
// attachSnapshotPass contains no repair logic and is called at most twice, so
// there is no cycle a persistent defect could spin.
func (idx *Indexer) AttachSnapshot(ctx context.Context, workspacePath, legacyBranch string) error {
	if !idx.service.CorpusEnabled() {
		return errors.New("corpus registry not wired")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := idx.attachSnapshotPass(ctx, workspacePath, legacyBranch)
	if err == nil {
		return nil
	}
	scope := QuarantineScopeOf(err)
	if scope == QuarantineNoStore {
		// No store defect is proven — a transient OS fault, or a failure the
		// store cannot own (a degenerate workspace: "no indexable files", an
		// incomplete walk; a congested build wait; a git-identity failure) —
		// so the publication stays, no artifact is destroyed, and the caller
		// surfaces unavailable with this cause. The suffix names the class
		// honestly: only a genuinely TRANSIENT fault says "transient"
		// (retry later) — the same word rendered beside `error_kind:
		// transient`; every other no-store outcome (a fatal
		// git-identity/wiring failure, a degenerate workspace, a busy or
		// expired build lease) gets the class-neutral form, so the message
		// never contradicts the kind the same surface reports.
		if Transient(err) {
			return fmt.Errorf("%w (transient fault: the publication stays, nothing was quarantined)", err)
		}
		return fmt.Errorf("%w (nothing was quarantined: the publication stays intact)", err)
	}
	rootKey := overlayRootKey(workspacePath)
	idx.logger.Warn("vector attach: proven store defect; quarantining the affected store and retrying once",
		"snapshot", key, "scope", scope.String(), "root", workspacePath, "error", err)
	if qErr := idx.service.QuarantineStoreView(ctx, key, rootKey, scope); qErr != nil {
		return fmt.Errorf("%w; the %s store could not be quarantined: %w", err, scope, qErr)
	}
	if _, retryErr := idx.attachSnapshotPass(ctx, workspacePath, legacyBranch); retryErr != nil {
		return fmt.Errorf("retrying the attach after quarantining the %s store: %w", scope, retryErr)
	}
	return nil
}

// attachSnapshotPass is one complete attach attempt: pin the root's
// ownership, drive the corpus to a published state, then bind the view. It
// returns the snapshot key it resolved so the caller's repair path can name
// the store it may quarantine. No repair happens here — the caller owns the
// single quarantine-and-retry, which is what keeps the retry cycle-free.
func (idx *Indexer) attachSnapshotPass(ctx context.Context, workspacePath, legacyBranch string) (string, error) {
	oid, oidErr := CurrentCommit(ctx, workspacePath)
	if oidErr != nil {
		return "", fmt.Errorf("resolving snapshot identity: %w", oidErr)
	}
	if oid == "" {
		// Non-git tree: the synthetic snapshot is root-owned (ADR-104 §1).
		oid = "ngit-" + rootFingerprint(workspacePath)
	}
	key := snapshotKey(oid)
	storage := idx.service.StorageRoot()

	// Durable ownership pin (ADR-104 stage 5): record the root's reference
	// to this snapshot BEFORE driving the corpus, so the reachability sweep
	// can never reclaim a corpus this root is in the middle of building or
	// adopting (an in-flight build also carries registry protection, but
	// the marker is the record that survives eviction, restart and
	// shutdown). Fail-soft: an unrecordable reference degrades to GC churn
	// (the corpus is rebuilt on the next attach), never to a wrong attach.
	if err := idx.service.PinCorpusView(workspacePath, key); err != nil {
		idx.logger.Warn("vector corpus: could not record the root reference; the corpus stays sweep-reclaimable",
			"root", workspacePath, "snapshot", key, "error", err)
	}
	// The branch reference: this root's HEAD is legacyBranch's tip, so the
	// corpus captures the branch's commit — the branch owns a keep on it
	// until its deletion (ADR-104 §1 L3; a branch reaches its corpus
	// through this pointer, never through a branch-named store). Same
	// fail-soft posture as the root pin.
	if legacyBranch != "" {
		if err := idx.service.PinBranchView(legacyBranch, key); err != nil {
			idx.logger.Warn("vector corpus: could not record the branch reference; the corpus stays sweep-reclaimable",
				"branch", legacyBranch, "snapshot", key, "error", err)
		}
	}

	idx.onProgress(PhasePreparation, IndexStatePreparing, 0, 0, "")
	ensureErr := idx.service.EnsureCorpus(ctx, storage, key, legacyBranch, func() error {
		// The build lease is held: bind the working store to the corpus,
		// run the full pass through the ordinary pipeline, publish, and
		// ONLY THEN release the corpus's read-write residents — the lease
		// settles with no write handle left behind, so a concurrent
		// attacher's read-only open never contends the lexical lock.
		//
		// Every release is OWNERSHIP-GATED (CloseWorkingStoreIfCurrent):
		// the lease is single-flight per snapshot KEY, so an attach of a
		// different snapshot racing this build can displace the working
		// store between the full pass and the publish — that slot is the
		// displacer's now, and closing it would strand the displacer's
		// pass on a nil collection mid-commit (a process-killing panic in
		// chromem's commit goroutines). A displaced build simply fails its
		// own publish and replays on the next attach.
		if err := idx.service.BuildCorpusStore(ctx, key); err != nil {
			return err
		}
		if err := idx.IndexFull(ctx, workspacePath); err != nil {
			idx.service.CloseWorkingStoreIfCurrent(key)
			return err
		}
		if err := idx.service.PublishCorpus(ctx, key); err != nil {
			idx.service.CloseWorkingStoreIfCurrent(key)
			return err
		}
		idx.service.CloseWorkingStoreIfCurrent(key)
		return nil
	}, func() {
		idx.onProgress(PhaseMigration, IndexStateMigrating, 0, 0, "")
	})
	if ensureErr != nil {
		return key, fmt.Errorf("ensuring corpus %s: %w", key, ensureErr)
	}

	idx.onProgress(PhaseOpen, IndexStateLoading, 0, 0, "")
	if err := idx.service.AttachView(ctx, key, overlayRootKey(workspacePath)); err != nil {
		return key, err
	}
	idx.logger.Info("view attached", "snapshot", key, "root", workspacePath)
	return key, nil
}

// sanitizeChunkContent replaces undecodable UTF-8 byte sequences with U+FFFD
// so the tokenizer can process the bytes, reporting whether anything was
// replaced. Protocol owner: callers MUST hash the RAW bytes first
// (ValidateCollection re-hashes raw bytes for unchanged-file detection), then
// sanitize before chunking, so every chunk-boundary derivation (processFile,
// lexicalDocsForSidecarFile) sees the same bytes and reproduces the same
// deterministic IDs.
func sanitizeChunkContent(content []byte) ([]byte, bool) {
	if utf8.Valid(content) {
		return content, false
	}
	return bytes.ToValidUTF8(content, []byte("\uFFFD")), true
}

// processFile reads a file, computes its hash, chunks it, and returns
// parallel chromem and lexical document slices keyed by the same shared
// document ID. root is the workspace root the pass walks; the file's
// root-RELATIVE occurrence path (see relPathFromAbs) becomes the shared
// identity — document ID, sidecar key, lexical ID — while the absolute path
// stays per-request data (L4, ADR-104). An empty root (standalone services)
// keeps the historical absolute-path identity.
//
// Both read stages (the binary-header pre-read and the full read) run under
// the per-file read watchdog (fileReadWatchdogTimeout, see readwatchdog.go):
// a read that outlasts the deadline is abandoned in place and the file is
// returned as a "read timeout" skip. Each timeout spends one allowance of
// budget — the pass's read-timeout skip budget, nil for budget-free direct
// callers — and a spent budget refuses the file with
// ErrReadSkipBudgetExhausted before any read is attempted, so a sick pass
// cannot strand wedged reader goroutines without bound.
func (idx *Indexer) processFile(filePath, root string, budget *readSkipBudget) ([]chromem.Document, []lexical.Doc, error) {
	// The occurrence coordinate: root-relative when the file sits under the
	// bound root, the absolute path otherwise (identity must be stable even
	// for paths a caller feeds from outside the tree).
	relPath := relPathFromAbs(root, filePath)
	if relPath == "" {
		relPath = filepath.ToSlash(filePath)
	}
	// Size guard (stat once; reused below for lastModified): the universal
	// backstop that no oversized file is ever fully read, from any caller —
	// the NUL-header binary check cannot recognize protobuf-format binaries
	// (readable ASCII, no NUL), which would otherwise be loaded whole and
	// explode memory during chunking (see DefaultMaxIndexableFileSize; the
	// walk and ValidateCollection apply the same limit only as an
	// optimization).
	info, statErr := os.Stat(filePath)
	if statErr != nil {
		return nil, nil, fmt.Errorf("stat file %s: %w", filePath, statErr)
	}
	// Only regular files are read: processFile may be reached via a watcher
	// event that bypasses the walk, and a FIFO/socket/device planted at a
	// workspace path would otherwise block the read-open — and hang the
	// pass plus any shutdown join waiting on it — indefinitely. os.Stat
	// never blocks, so the skip costs nothing and no open is attempted.
	if !info.Mode().IsRegular() {
		idx.logger.Debug("skipping non-regular file", "path", filePath)
		return nil, nil, nil
	}
	if tooLargeForIndex(info.Size(), idx.maxFileSize) {
		idx.logger.Debug("skipping oversized file", "path", filePath, "size", info.Size(),
			"limit", idx.maxFileSize)
		return nil, nil, nil
	}

	// Read watchdog: safeio hardens the OPEN (FIFOs/devices refused via the
	// O_NONBLOCK open + fstat), but the read(2) of a regular file can still
	// block indefinitely — a wedged network/FUSE mount hangs the prep
	// worker, the pass and every shutdown join waiting on it. Both read
	// stages therefore run bounded; a stage timeout abandons the stuck
	// reader goroutine in place, spends one skip-budget allowance and skips
	// the file. A spent budget refuses before the read so no further readers
	// are attempted.
	if budget != nil && budget.exhausted() {
		return nil, nil, fmt.Errorf("reading file %s: %w: skipping before the read", filePath, ErrReadSkipBudgetExhausted)
	}

	// Bounded header pre-read rejects multi-GB binary assets (.onnx,
	// .safetensors, .psd) before they are loaded fully into memory; see
	// isBinaryHeader. The read seams are captured before the go statement:
	// a timed-out read abandons its goroutine in place, and that goroutine
	// must not read mutable receiver state — a late-started abandoned
	// reader would race any seam reassignment (the watchdog tests swap
	// these seams between calls).
	binaryHeaderFn := idx.binaryHeaderFn
	binary, timedOut, bErr := boundedRead(fileReadWatchdogTimeout, func() (bool, error) {
		return binaryHeaderFn(filePath)
	})
	if timedOut {
		return nil, nil, readTimeoutSkipError(filePath, budget)
	}
	if bErr != nil {
		return nil, nil, fmt.Errorf("reading file %s: %w", filePath, bErr)
	}
	if binary {
		return nil, nil, nil
	}

	readFileFn := idx.readFileFn
	content, timedOut, err := boundedRead(fileReadWatchdogTimeout, func() ([]byte, error) {
		return readFileFn(filePath)
	})
	if timedOut {
		return nil, nil, readTimeoutSkipError(filePath, budget)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading file %s: %w", filePath, err)
	}

	if len(content) == 0 {
		return nil, nil, nil
	}

	// Content policy runs on the SAME raw bytes ValidateCollection reads, so
	// the skip verdict is identical on both paths regardless of UTF-8
	// validity; a skipped file produces no documents and its raw-hash sidecar
	// entry is simply never written.
	if reason, skip := DetectContentSkip(content, idx.contentFilter); skip {
		idx.logger.Info("skipping file by content policy", "path", filePath, "reason", reason)
		return nil, nil, nil
	}

	hash := idx.hashFn(content)

	// The embedding tokenizer (via sp4rk) panics on invalid UTF-8, and such
	// bytes pass the NUL-header check above, so sanitize before chunking
	// (see sanitizeChunkContent for the hash-raw-first protocol this shares
	// with the sidecar-driven lexical rebuild).
	sanitized, wasSanitized := sanitizeChunkContent(content)
	if wasSanitized {
		idx.logger.Debug("sanitizing non-UTF-8 file content before indexing", "path", filePath)
	}
	content = sanitized

	chunks, err := idx.chunkFn(filePath, content, idx.maxChunkSize, idx.overlap)
	if err != nil {
		return nil, nil, fmt.Errorf("chunking file %s: %w", filePath, err)
	}

	// Chunk-count guard: a pathological chunker result (a data-format file
	// fragmented into tens of thousands of tiny chunks) would hang/OOM the
	// embedder via AddDocuments; the cap turns it into a clean WARN skip.
	// See the canonical rationale on DefaultMaxChunksPerFile.
	if len(chunks) > idx.maxChunksPerFile {
		idx.logger.Warn("skipping file: chunk count exceeds per-file cap",
			"path", filePath, "chunks", len(chunks), "cap", idx.maxChunksPerFile)
		return nil, nil, nil
	}

	fileName := filepath.Base(filePath)
	// info is guaranteed non-nil here: processFile returned early above if the
	// opening os.Stat failed, so lastModified is always populated.
	lastModified := info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
	// file_size + file_mtime_unix_nano feed the sidecar's stat-only
	// unchanged-file classification (no ReadFile + SHA-256 on incremental
	// passes); recorded from the SAME stat that guarded the read, so the
	// entry always describes the exact bytes hashed.
	sizeMeta := strconv.FormatInt(info.Size(), 10)
	mtimeMeta := strconv.FormatInt(info.ModTime().UnixNano(), 10)

	vecDocs := make([]chromem.Document, 0, len(chunks))
	lexDocs := make([]lexical.Doc, 0, len(chunks))
	for i, chunk := range chunks {
		docID := DocumentID(relPath, i)
		vecDocs = append(vecDocs, chromem.Document{
			ID:      docID,
			Content: chunk.Content,
			Metadata: map[string]string{
				"file_path":            filePath,
				relPathMetaKey:         relPath,
				"file_name":            fileName,
				"last_modified":        lastModified,
				"content_hash":         hash,
				"file_size":            sizeMeta,
				"file_mtime_unix_nano": mtimeMeta,
				"start_line":           strconv.Itoa(chunk.StartLine),
				"end_line":             strconv.Itoa(chunk.EndLine),
				"language":             chunk.Language,
			},
		})
		// The lexical entry carries the SHARED coordinate (the relative
		// occurrence path) — root-local resolution happens per request, and
		// the sidecar facts (size/mtime) never ride the lexical document.
		lexDocs = append(lexDocs, lexical.Doc{
			ID:       docID,
			FilePath: relPath,
			Language: chunk.Language,
			Content:  chunk.Content,
		})
	}
	return vecDocs, lexDocs, nil
}

// prepUnit is one file's output from the preparation stage: the parallel
// chromem and lexical document slices keyed by the same shared document IDs.
// skipped marks a per-file prep failure (warn-and-skip semantics); such units
// carry no documents, and whether they still count toward pass progress is a
// per-pass policy (indexPipelineOpts.countSkipped). fatalErr marks the
// pass-refusing failure — the read-timeout skip budget exhausted (see
// readwatchdog.go): the consumer aborts the pass with it instead of
// warn-and-skipping, so abandoned reader goroutines stop accumulating.
type prepUnit struct {
	path     string
	skipped  bool
	fatalErr error
	vecDocs  []chromem.Document
	lexDocs  []lexical.Doc
}

// streamPreparedFiles fans file preparation out across idx.prepWorkers
// goroutines and returns the stream of finished units. Preparation is pure
// I/O+CPU work — stat, binary-header check, read, hash, chunk; processFile
// never touches the service or other shared state — so files are prepared
// concurrently while the single consumer embeds earlier batches; the
// unbuffered input channel bounds in-flight preparation to prepWorkers files.
// root is the workspace root the pass walks (the occurrence-coordinate base
// every document ID is derived relative to). budget is the pass's
// read-timeout skip budget (see readwatchdog.go) consumed by the workers'
// read watchdogs; a nil budget tolerates every timeout (direct/test callers).
// wd is the pass stall watchdog; each worker heartbeats around processFile so
// a pass wedged in preparation names the most recently started file.
//
// The stream closes after every path has been prepared, skipped, or dropped:
// every send and the feeder select on ctx.Done(), so a consumer that returns
// early never deadlocks the pool — the feeder stops feeding, workers abandon
// in-flight sends, all goroutines exit.
func (idx *Indexer) streamPreparedFiles(ctx context.Context, paths []string, root, skipLogLabel string, budget *readSkipBudget, wd *passWatchdog) <-chan prepUnit {
	in := make(chan string)
	out := make(chan prepUnit)

	var wg sync.WaitGroup
	wg.Add(idx.prepWorkers)
	for range idx.prepWorkers {
		go func() {
			defer wg.Done()
			for path := range in {
				// A cancelled pass stops preparing new files; remaining input
				// is drained so the feeder never blocks on an unread channel.
				if ctx.Err() != nil {
					continue
				}
				// The file counts as in-flight from here: if every worker
				// ends up wedged inside processFile, the stall Warn names
				// the most recently started read.
				wd.heartbeat(StageReadHashChunk, path)
				prepStarted := time.Now()
				vecDocs, lexDocs, err := idx.processFile(path, root, budget)
				idx.telemetry.observe(StageReadHashChunk, len(vecDocs), time.Since(prepStarted))
				// Completion is progress and re-arms the stall watchdog.
				wd.heartbeat(StageReadHashChunk, path)
				unit := prepUnit{path: path, vecDocs: vecDocs, lexDocs: lexDocs}
				if err != nil {
					if errors.Is(err, ErrReadSkipBudgetExhausted) {
						// The pass refusal travels to the consumer as a
						// fatal unit — not a warn-and-skip: continuing to
						// feed paths would strand more wedged readers.
						unit.fatalErr = err
					} else {
						idx.logger.Warn(skipLogLabel, "path", path, "error", err)
						unit.skipped = true
					}
					unit.vecDocs, unit.lexDocs = nil, nil
				}
				select {
				case out <- unit:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		// Execution order (LIFO): close(in) stops the workers' input,
		// wg.Wait() drains them, close(out) then runs only when no sender
		// can remain — closing earlier would panic on an in-flight send.
		defer close(out)
		defer wg.Wait()
		defer close(in)
		for _, p := range paths {
			select {
			case in <- p:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out
}

// indexPass is one full/incremental pass's staged-acquisition guard (ADR-123):
// the pass lease — the pass-vs-pass exclusion the whole-pass service write
// hold used to provide — plus the working-store identity every write window
// re-validates before touching shared state. The pass holds NO service lock
// between windows: search, status and Close run freely there.
type indexPass struct {
	svc   *Service
	ident workingStoreIdentity
	// wd is the pass-level stall watchdog attached by the driving pass
	// function. Nil for Service-side leases — every watchdog method
	// tolerates nil.
	wd *passWatchdog
}

// beginIndexPass takes the pass lease and captures the working-store identity
// under a short write window. The caller releases via indexPass.release when
// the pass ends — success, failure or displacement.
func (idx *Indexer) beginIndexPass() *indexPass {
	return idx.service.beginPassLease()
}

// startPassWatchdog arms a fresh per-pass stall watchdog on the Indexer's
// logger, attaching the IndexerConfig.OnStallChange consumer when one is
// wired (the Manager records the transitions into its status; without a
// consumer the watchdog still logs its evidence Warn). The calling pass
// function owns the lifecycle: start here, watchContext(ctx) for the
// cancellation release, heartbeat on every progress point, stop via defer
// (and again, idempotently, via indexPass.release).
func (idx *Indexer) startPassWatchdog() *passWatchdog {
	wd := newPassWatchdog(idx.logger, idx.stallAfterFunc)
	wd.setOnStallChange(idx.onStallChange)
	wd.start()
	return wd
}

// waitSidecarMigrations waits for both one-time sidecar migrations with the
// stall watchdog suspended (see passWatchdog.suspend): the waits block on
// background backfill work this pass does not own, so however long they
// last is not silence of THIS pass — the same exclusion the pass-lease
// queue gets, unlike the walk/validation stages, which heartbeat per entry.
//
// The file-hash migration must settle first (first run after the sidecar
// upgrade, or a branch whose collection was built elsewhere) so
// ValidateCollection sees the full hash map instead of re-embedding every
// file; the content-less migration's ID-preserving re-commits must not
// interleave with this pass's deletions and re-indexing, or a just-deleted
// legacy document could be resurrected from the migration's probe snapshot.
// IndexFull is unaffected: an empty collection has nothing to migrate. See
// specs/decisions/049-vector-index-memory-reduction.md.
func (idx *Indexer) waitSidecarMigrations(ctx context.Context, wd *passWatchdog) error {
	wd.suspend()
	defer wd.resume()
	if err := idx.service.WaitFileHashMigration(ctx); err != nil {
		return fmt.Errorf("waiting for file-hash migration: %w", err)
	}
	if err := idx.service.WaitContentlessMigration(ctx); err != nil {
		return fmt.Errorf("waiting for content-less migration: %w", err)
	}
	return nil
}

// release releases the pass lease and the pass stall watchdog.
func (p *indexPass) release() {
	p.wd.stop()
	p.svc.passLease.Unlock()
}

// window runs fn inside one short identity-checked service write window — the
// per-batch discipline RebuildLexical's flush windows established. Under the
// lock the pass re-validates its working-store identity, so a concurrent
// branch switch, project switch, No Project reset or Close that displaced the
// store between windows aborts the pass with ErrWorkingStoreDisplaced instead
// of committing into a store it no longer owns. Within a held window no
// displacement can occur — every displacer takes the same write lock — so fn
// sees a stable working store and the same publication ordering the
// whole-pass hold provided: §3 chunk records before the chromem commit, the
// commit before the file's sidecar row, deletions before re-indexing.
func (p *indexPass) window(what string, fn func() error) error {
	p.svc.AcquireWriteLock()
	defer p.svc.ReleaseWriteLock()
	if p.svc.workingStoreDisplacedLocked(p.ident) {
		return fmt.Errorf("%s: %w (a concurrent switch, reset or close displaced the working store; the next pass reconciles)", what, ErrWorkingStoreDisplaced)
	}
	return fn()
}

// indexPipelineOpts captures the per-pass differences between the full and
// incremental indexing passes that share the prep-worker pipeline.
type indexPipelineOpts struct {
	// state is the IndexState reported by in-pass progress events
	// (IndexStateIndexing for a full pass, IndexStateReindexing otherwise).
	state IndexState
	// total is the denominator reported by in-pass progress events.
	total int
	// progressStart is the pass's initial progress value (0 for a full pass;
	// len(deleted) for an incremental pass that already counted deletions).
	progressStart int
	// countSkipped reports whether files that fail preparation still count
	// toward pass progress (incremental passes count them so the reported
	// progress always reaches total; full passes do not).
	countSkipped bool
	// cancelMsg prefixes the ctx-cancellation error ("indexing cancelled" /
	// "incremental indexing cancelled"), preserving the former serial loops'
	// error wording.
	cancelMsg string
	// batchLogLabel and skipLogLabel keep the per-pass log wording of the
	// former serial loops ("" / " (incremental)").
	batchLogLabel string
	skipLogLabel  string

	// root is the workspace root the pass walks — the base the
	// root-relative occurrence coordinates (document IDs, sidecar keys,
	// lexical IDs) are derived against (ADR-104 L2).
	root string
}

type pendingDocument struct {
	vec  chromem.Document
	lex  lexical.Doc
	file *pendingFile
}

type pendingFile struct {
	total     int
	completed int
	// info carries this file's sidecar facts (hash, size, mtime), captured
	// from the first chunk's metadata: committed documents no longer store
	// them (see strippedForCommit), so the sidecar publish step consumes
	// this structure instead of a metadata map.
	info fileHashInfo
	// committedIDs lists the chunks that actually committed to chromem, in
	// order; poison-dropped chunks never reach pendingCommit, so their
	// indices are absent — the sidecar's 5th field (see
	// composeFileHashEntryInfo) then records the gapped set.
	committedIDs []string
	published    bool
}

// documentAccumulator decouples ONNX inference batches from chromem commit
// windows (embedding optimization policy: see
// specs/decisions/036-vector-index-embedding-optimization-policy.md).
// pendingInference stays below one configured inference batch between Add
// calls, pendingCommit below one bounded commit window between drains.
type documentAccumulator struct {
	service   *Service
	telemetry *Telemetry
	// pass + cache (ADR-123): inference runs OUTSIDE the service write
	// windows through the cache captured with the pass's working-store
	// identity (internally synchronized, never torn down by state teardown);
	// commits and sidecar publishes take identity-checked windows.
	pass             *indexPass
	cache            *embeddingCache
	pendingInference []pendingDocument
	pendingCommit    []pendingDocument
}

func newDocumentAccumulator(service *Service, telemetry *Telemetry, pass *indexPass) *documentAccumulator {
	return &documentAccumulator{
		service:          service,
		telemetry:        telemetry,
		pass:             pass,
		cache:            pass.ident.cache,
		pendingInference: make([]pendingDocument, 0, service.embeddingBatchSize),
		pendingCommit:    make([]pendingDocument, 0, embeddingCommitWindowSize),
	}
}

func (a *documentAccumulator) addFile(ctx context.Context, vecDocs []chromem.Document, lexDocs []lexical.Doc) error {
	if len(vecDocs) != len(lexDocs) {
		return fmt.Errorf("vector/lexical document count mismatch: %d != %d", len(vecDocs), len(lexDocs))
	}
	if len(vecDocs) == 0 {
		return nil
	}
	file := &pendingFile{total: len(vecDocs)}
	if info, ok := fileHashInfoFromMetadata(vecDocs[0].Metadata); ok {
		file.info = info
	}
	for i := range vecDocs {
		if vecDocs[i].ID != lexDocs[i].ID {
			return fmt.Errorf("vector/lexical document ID mismatch at offset %d: %q != %q", i, vecDocs[i].ID, lexDocs[i].ID)
		}
		a.pendingInference = append(a.pendingInference, pendingDocument{vec: vecDocs[i], lex: lexDocs[i], file: file})
		if len(a.pendingInference) == a.service.embeddingBatchSize {
			if err := a.embedPending(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *documentAccumulator) finish(ctx context.Context) error {
	if err := a.embedPending(ctx); err != nil {
		return err
	}
	return a.commitPending(ctx)
}

func (a *documentAccumulator) embedPending(ctx context.Context) error {
	if len(a.pendingInference) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("embedding accumulated documents: %w", err)
	}

	docs := make([]chromem.Document, len(a.pendingInference))
	for i := range a.pendingInference {
		docs[i] = a.pendingInference[i].vec
	}
	a.telemetry.observeBatch(len(docs), a.service.embeddingBatchSize)
	a.pass.wd.heartbeat(StageEmbedding, "")
	embedStarted := time.Now()
	// Inference runs OUTSIDE the service write window (ADR-123) through the
	// cache captured with the pass's working-store identity; a displacement
	// renders the work moot (wasted inference, never corruption) and the
	// commit window aborts the pass.
	kept, dropped, err := a.service.embedSubBatchFrom(ctx, docs, a.cache)
	a.telemetry.observe(StageEmbedding, len(docs), time.Since(embedStarted))
	if err != nil {
		return err
	}
	if len(kept)+len(dropped) != len(docs) {
		return fmt.Errorf("embedding accumulator lost documents: kept=%d dropped=%d input=%d", len(kept), len(dropped), len(docs))
	}

	keptIdx := 0
	var dropCompleted []*pendingFile
	for i := range a.pendingInference {
		item := a.pendingInference[i]
		if _, drop := dropped[item.vec.ID]; drop {
			item.file.completed++
			if !slices.Contains(dropCompleted, item.file) {
				dropCompleted = append(dropCompleted, item.file)
			}
			continue
		}
		if keptIdx >= len(kept) || kept[keptIdx].ID != item.vec.ID {
			return fmt.Errorf("embedded document order mismatch at input offset %d", i)
		}
		item.vec = kept[keptIdx]
		keptIdx++
		a.pendingCommit = append(a.pendingCommit, item)
		if len(a.pendingCommit) == embeddingCommitWindowSize {
			if err := a.commitPending(ctx); err != nil {
				return err
			}
		}
	}
	a.pendingInference = a.pendingInference[:0]
	// Files whose LAST chunk poison-dropped completed during inference: their
	// sidecar publish (publish-after-commit — every committed chunk of such a
	// file already landed in an earlier commit window) takes its own
	// identity-checked window instead of riding inference (ADR-123).
	if len(dropCompleted) > 0 {
		if err := a.pass.window("publishing poison-drop sidecar entries", func() error {
			for _, file := range dropCompleted {
				a.publishFileIfComplete(file)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *documentAccumulator) commitPending(ctx context.Context) error {
	if len(a.pendingCommit) == 0 {
		return nil
	}
	vecDocs := make([]chromem.Document, len(a.pendingCommit))
	lexDocs := make([]lexical.Doc, len(a.pendingCommit))
	for i := range a.pendingCommit {
		vecDocs[i] = a.pendingCommit[i].vec
		lexDocs[i] = a.pendingCommit[i].lex
	}
	// ONE identity-checked window per commit: the §3 chunk records, the
	// chromem commit and the per-file sidecar publishes land together,
	// atomic against searches and against a displacement (ADR-123; the
	// chunks-before-tables and publish-after-commit ordering is exactly the
	// whole-pass hold used to serialize).
	a.pass.wd.heartbeat(StageChromemCommit, "")
	if err := a.pass.window("committing embedded documents", func() error {
		if err := a.service.commitEmbeddedDocuments(ctx, vecDocs, lexDocs); err != nil {
			return err
		}
		for i := range a.pendingCommit {
			file := a.pendingCommit[i].file
			file.committedIDs = append(file.committedIDs, a.pendingCommit[i].vec.ID)
			file.completed++
			a.publishFileIfComplete(file)
		}
		return nil
	}); err != nil {
		return err
	}
	a.pendingCommit = a.pendingCommit[:0]
	return nil
}

func (a *documentAccumulator) publishFileIfComplete(file *pendingFile) {
	if file.published || file.completed != file.total {
		return
	}
	// The sidecar entry records exactly the chunks that committed: the 5th
	// field is derived from committedIDs, so poison-dropped chunks are
	// excluded. A fully poison-dropped file still publishes (with the empty
	// set) — without an entry it would be re-reported as new forever.
	a.service.upsertFileHashEntryInfo(file.info, file.committedIDs)
	file.published = true
}

// indexPrepared runs one indexing pass over paths: a bounded pool of
// idx.prepWorkers goroutines prepares files (read/hash/chunk) while this
// single consumer streams their documents through one configured-capacity
// inference accumulator and a separate bounded chromem commit window; the
// inference tail flushes once, at pass end. Embedding stays single-threaded.
//
// Staged acquisition (ADR-123): preparation touches no shared state and
// inference runs through the embedding cache captured with the pass's
// working-store identity, both WITHOUT the service write lock; only the
// mutating steps — batch commits and sidecar publishes — take the lock, each
// in a short identity-checked window, so search, status and Close are never
// frozen by a pass. pass must be the caller's held lease
// (Indexer.beginIndexPass).
//
// The pass owns one read-timeout skip budget (fileReadTimeoutSkipBudget, see
// readwatchdog.go): each read the watchdog times out is an ordinary
// warn-and-skip while the budget holds, and once it is spent the pass refuses
// explicitly with ErrReadSkipBudgetExhausted instead of stacking more wedged
// reader goroutines.
//
// Returns the number of files counted toward pass progress: per-file prep
// failures are logged and skipped; the read-timeout skip budget exhaustion,
// ctx cancellation, AddDocuments failures and a displaced working store abort
// the pass.
func (idx *Indexer) indexPrepared(ctx context.Context, paths []string, o indexPipelineOpts, pass *indexPass) (int, error) {
	// Derived cancellable context: the prep pool is released promptly when
	// this consumer returns early (feeder and worker sends select on it).
	poolCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	budget := newReadSkipBudget(fileReadTimeoutSkipBudget)
	units := idx.streamPreparedFiles(poolCtx, paths, o.root, o.skipLogLabel, budget, pass.wd)

	progress := o.progressStart
	var vecBatch []chromem.Document
	var lexBatch []lexical.Doc
	var accumulator *documentAccumulator
	if idx.service.batchEmbedder != nil {
		accumulator = newDocumentAccumulator(idx.service, idx.telemetry, pass)
	}

	for unit := range units {
		if err := ctx.Err(); err != nil {
			return progress, fmt.Errorf("%s: %w", o.cancelMsg, err)
		}
		// The read-timeout skip budget refusal: the pass's one explicit
		// per-file failure. Returning cancels the pool, so already-fed
		// paths are dropped without reads instead of stranding further
		// reader goroutines.
		if unit.fatalErr != nil {
			return progress, unit.fatalErr
		}
		if unit.skipped && !o.countSkipped {
			continue
		}
		if !unit.skipped {
			if accumulator != nil {
				if addErr := accumulator.addFile(ctx, unit.vecDocs, unit.lexDocs); addErr != nil {
					return progress, fmt.Errorf("accumulating documents: %w", addErr)
				}
			} else {
				vecBatch = append(vecBatch, unit.vecDocs...)
				lexBatch = append(lexBatch, unit.lexDocs...)
				if len(vecBatch) >= addDocumentBatchSize {
					idx.logger.Debug("embedding document batch"+o.batchLogLabel,
						"batchSize", len(vecBatch), "progress", progress, "total", o.total)
					idx.telemetry.observeBatch(len(vecBatch), addDocumentBatchSize)
					pass.wd.heartbeat(StageEmbedding, unit.path)
					// One identity-checked window per batch: the legacy path
					// embeds inside AddDocuments, so the window spans the
					// commit of one addDocumentBatchSize slice (ADR-123).
					if addErr := pass.window("adding document batch", func() error {
						return idx.service.AddDocuments(ctx, vecBatch, lexBatch)
					}); addErr != nil {
						return progress, fmt.Errorf("adding document batch: %w", addErr)
					}
					idx.logger.Debug("batch embedded successfully")
					vecBatch = vecBatch[:0]
					lexBatch = lexBatch[:0]
				}
			}
		}
		progress++
		idx.onProgress(PhaseBoth, o.state, progress, o.total, unit.path)
	}
	// A cancelled pass can also exit through range-end (workers drop
	// remaining paths without emitting units), so surface cancellation here:
	// a cancelled pass ALWAYS reports an error.
	if err := ctx.Err(); err != nil {
		return progress, fmt.Errorf("%s: %w", o.cancelMsg, err)
	}

	// Flush remaining documents. The batch path carries inference tails across
	// file boundaries and commit windows, so only the final pass drain may be
	// underfilled. The legacy path retains its historical 50-document flush.
	if accumulator != nil {
		if finishErr := accumulator.finish(ctx); finishErr != nil {
			return progress, fmt.Errorf("flushing accumulated documents: %w", finishErr)
		}
	} else if len(vecBatch) > 0 || len(lexBatch) > 0 {
		idx.logger.Debug("embedding final document batch"+o.batchLogLabel,
			"batchSize", len(vecBatch), "progress", progress, "total", o.total)
		idx.telemetry.observeBatch(len(vecBatch), addDocumentBatchSize)
		pass.wd.heartbeat(StageEmbedding, "")
		if addErr := pass.window("adding final document batch", func() error {
			return idx.service.AddDocuments(ctx, vecBatch, lexBatch)
		}); addErr != nil {
			return progress, fmt.Errorf("adding final document batch: %w", addErr)
		}
		idx.logger.Debug("final batch embedded successfully")
	}
	return progress, nil
}

// lexicalSkipNoFingerprint and lexicalSkipUnknownSet are the skip reasons the
// incremental pass will NOT repair (the stat fast path keeps treating such
// files as seen), so any such skip forces RebuildLexical to also run the
// collection-enumeration fallback, recovering those chunks from metadata.
const (
	lexicalSkipNoFingerprint = "entry carries no chunker fingerprint"
	lexicalSkipUnknownSet    = "committed chunk set unknown"
)

// lexicalSkipUnrepairable reports whether reason is a skip the incremental
// pass will not repair (see lexicalSkipNoFingerprint / lexicalSkipUnknownSet);
// every other skip cause is repaired when the pass re-indexes the file.
func lexicalSkipUnrepairable(reason string) bool {
	return reason == lexicalSkipNoFingerprint || reason == lexicalSkipUnknownSet
}

// RebuildLexical rebuilds the per-branch lexical index from scratch: the
// one-time backfill the manager runs when a pre-BM25-upgrade project opens
// with a non-empty chromem collection but an empty lexical index. The v2
// path is sidecar-driven and ONNX-free (bounded reads + hash checks +
// re-chunks reproducing the stored boundaries, ADR-049); an empty or
// ineligible sidecar falls back to collection enumeration. Changed files are
// skipped — the incremental pass rewrites their entries. See
// specs/decisions/049-vector-index-memory-reduction.md and
// specs/domains/workspace.md (dual-write invariant).
//
// The caller must not hold s.mu; lexical upsert windows commit under the
// write lock internally.
func (idx *Indexer) RebuildLexical(ctx context.Context) error {
	lex := idx.service.GetLexical()
	if lex == nil {
		// In-memory mode or lexical open failed; nothing to do.
		return nil
	}
	col := idx.service.GetCollection()
	if col == nil {
		return errors.New("no collection available for lexical rebuild")
	}

	count := col.Count()
	if count == 0 {
		idx.onProgress(PhaseLexical, IndexStateReady, 0, 0, "")
		return nil
	}

	// Pass-level stall watchdog, same lifecycle as IndexFull: the sidecar
	// snapshot and the upsert loop are the monitored stages — the migration
	// wait before them runs suspended (see passWatchdog.suspend).
	wd := idx.startPassWatchdog()
	defer wd.stop()
	wd.watchContext(ctx)
	wd.heartbeat(StageCacheLookup, "")

	// The sidecar is the v2 driving table; wait for its one-time backfill so
	// the snapshot below is complete, not the empty placeholder. The wait
	// runs with the watchdog suspended: it blocks on backfill work this pass
	// does not own, so it is not pass silence — same exclusion as
	// waitSidecarMigrations and the pass-lease queue.
	wd.suspend()
	waitErr := idx.service.WaitFileHashMigration(ctx)
	wd.resume()
	if waitErr != nil {
		return fmt.Errorf("waiting for file-hash migration before lexical rebuild: %w", waitErr)
	}
	hashes, err := idx.service.GetCollectionFiles()
	if err != nil {
		return fmt.Errorf("reading file-hash sidecar for lexical rebuild: %w", err)
	}
	if len(hashes) == 0 {
		// Transitional fallback: no sidecar at all (the backfill found
		// nothing trackable). Enumerate the collection the old way.
		idx.logger.Info("lexical rebuild: empty sidecar, using collection enumeration")
		return idx.rebuildLexicalFromCollection(ctx, lex, col, count, wd)
	}

	paths := make([]string, 0, len(hashes))
	for fp := range hashes {
		paths = append(paths, fp)
	}
	slices.Sort(paths)

	// The workspace root the sidecar's relative keys resolve against for
	// source reads (per-request hydration; the keys themselves are the
	// shared coordinates and stay relative in the emitted IDs).
	rebuildRoot := idx.service.WorkspaceRoot()

	idx.onProgress(PhaseLexical, IndexStateIndexing, 0, len(paths), "")
	idx.logger.Info("starting sidecar-driven lexical backfill", "files", len(paths), "chunks", count)

	batch := make([]lexical.Doc, 0, addDocumentBatchSize)
	processed, skipped, unrepairable := 0, 0, 0
	// flush commits one lexical window under the service write lock (bleve
	// mutation convention; prep stays lock-free so searches are not blocked).
	// The captured lex pointer is re-validated under that lock: a concurrent
	// switch/Close nils bleveIndex — which has no internal synchronization —
	// so a replaced index aborts the rebuild; the swapped-in state is rebuilt
	// by its owner and the incremental pass rewrites anything still missing.
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		wd.heartbeat(StageBleveUpsert, "")
		idx.service.AcquireWriteLock()
		if cur := idx.service.currentLexicalLocked(); cur != lex {
			idx.service.ReleaseWriteLock()
			return fmt.Errorf("lexical rebuild aborted: lexical index replaced by a concurrent switch: %w", context.Canceled)
		}
		upsertStarted := time.Now()
		upsertErr := lex.Upsert(ctx, batch)
		idx.service.ReleaseWriteLock()
		idx.telemetry.observe(StageBleveUpsert, len(batch), time.Since(upsertStarted))
		if upsertErr != nil {
			return fmt.Errorf("lexical upsert batch: %w", upsertErr)
		}
		batch = batch[:0]
		return nil
	}

	for i, fp := range paths {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("lexical rebuild cancelled: %w", err)
		}
		wd.heartbeat(StageBleveUpsert, fp)
		docs, reason := idx.lexicalDocsForSidecarFile(fp, hashes[fp], rebuildRoot)
		if reason != "" {
			skipped++
			if lexicalSkipUnrepairable(reason) {
				unrepairable++
			}
			idx.logger.Debug("lexical rebuild skipped file", "path", fp, "reason", reason)
		} else {
			batch = append(batch, docs...)
			if len(batch) >= addDocumentBatchSize {
				if err := flush(); err != nil {
					return err
				}
			}
			processed++
		}
		idx.onProgress(PhaseLexical, IndexStateIndexing, i+1, len(paths), fp)
	}
	if err := flush(); err != nil {
		return err
	}

	if processed == 0 || unrepairable > 0 {
		// Either nothing was eligible, or an unrepairable skip occurred (the
		// mixed-sidecar case), and the incremental pass leaves such files
		// alone — fall back to collection enumeration, which re-upserts the
		// eligible files too (same deterministic IDs, idempotent), so every
		// stored chunk gets a lexical entry in one pass.
		idx.logger.Info("lexical rebuild: sidecar left files without a usable fingerprint/set; using collection enumeration",
			"sidecar_files", len(paths), "skipped", skipped, "unrepairable", unrepairable)
		return idx.rebuildLexicalFromCollection(ctx, lex, col, count, wd)
	}
	if skipped > 0 {
		idx.logger.Info("lexical backfill skipped files that changed since indexing or lack a chunker fingerprint",
			"skipped", skipped, "processed", processed)
	}

	idx.onProgress(PhaseLexical, IndexStateReady, len(paths), len(paths), "")
	idx.logger.Info("lexical backfill complete", "files", processed, "chunks_processed", count, "skipped_files", skipped)
	return nil
}

// lexicalDocsForSidecarFile re-derives one sidecar-tracked file's lexical
// documents without touching the collection (zero ONNX) — the v2 RebuildLexical
// path of ADR-049: bounded read + hash check, then a re-chunk under the ACTIVE
// chunker configuration (the fingerprint covers max chunk size, overlap and
// the content-filter policy, so a match implies the index-time verdicts still
// hold); deterministic DocumentIDs make the re-chunk reproduce the stored
// boundaries. Only the entry's committed chunk-index set is emitted, mirroring
// the collection (poison-dropped chunks were never lexically indexed).
//
// The returned reason is "" on success; non-empty is a human-readable skip
// cause, and the incremental pass re-indexes — and rewrites — skipped files.
// key is the sidecar key (the occurrence coordinate — root-relative when a
// workspace root is bound); root resolves it to the absolute source path for
// this rebuild's reads, while the emitted IDs stay keyed by the coordinate.
func (idx *Indexer) lexicalDocsForSidecarFile(key, entry, root string) (docs []lexical.Doc, reason string) {
	entryFP := fileHashEntryChunkerFP(entry)
	if entryFP == "" {
		return nil, lexicalSkipNoFingerprint
	}
	if entryFP != idx.service.chunkerFingerprint {
		return nil, "chunker fingerprint mismatch (file was chunked under a different configuration)"
	}
	set, ok := fileHashEntryChunkSet(entry)
	if !ok {
		return nil, lexicalSkipUnknownSet
	}

	absPath := key
	if root != "" && !filepath.IsAbs(key) {
		absPath = filepath.Join(root, filepath.FromSlash(key))
	}
	content, err := readBounded(absPath, idx.maxFileSize)
	if err != nil {
		return nil, "file unreadable: " + err.Error()
	}
	if hash := computeHash(content); hash != fileHashEntryHash(entry) {
		return nil, "content changed since indexing"
	}

	// Sanitize exactly as processFile did (see sanitizeChunkContent), or the
	// re-chunk splits on different rune/byte positions and breaks the
	// deterministic-ID invariant.
	content, _ = sanitizeChunkContent(content)

	chunks, err := idx.chunkFn(absPath, content, idx.maxChunkSize, idx.overlap)
	if err != nil {
		return nil, "chunking failed: " + err.Error()
	}
	if len(chunks) > idx.maxChunksPerFile {
		return nil, "chunk count exceeds per-file cap"
	}
	// The fingerprint matched, so the re-chunk must reproduce the stored
	// boundaries; a set reaching past the re-chunked output means a corrupt
	// or foreign entry — skip rather than emit stale IDs.
	maxIdx := 0
	for _, i := range set {
		if i > maxIdx {
			maxIdx = i
		}
	}
	if maxIdx >= len(chunks) {
		return nil, "committed chunk set exceeds re-chunked chunk count"
	}

	docs = make([]lexical.Doc, 0, len(set))
	for _, i := range set {
		docs = append(docs, lexical.Doc{
			ID:       DocumentID(key, i),
			FilePath: key,
			Language: chunks[i].Language,
			Content:  chunks[i].Content,
		})
	}
	return docs, ""
}

// rebuildLexicalFromCollection is the collection-enumeration fallback of
// RebuildLexical (the pre-sidecar transitional path, ADR-049): one collection
// enumeration mirrors every document into the lexical index, recovering chunk
// boundaries from STORED metadata — correct for unknown chunker
// configurations, at the cost of materializing the whole collection in one
// Query.
//
// Concurrency: a SHORT read lock covers only the enumeration (a concurrent
// park/close cannot free the collection mid-Query); reconstruction and the
// bleve windows run without it, so searches are not blocked for the pass.
// The caller must not hold s.mu. wd heartbeats the enumeration and every
// bleve window so a stall inside the fallback stays attributable to this pass.
func (idx *Indexer) rebuildLexicalFromCollection(ctx context.Context, lex lexical.Index, col *chromem.Collection, count int, wd *passWatchdog) error {
	idx.onProgress(PhaseLexical, IndexStateIndexing, 0, count, "")
	idx.logger.Info("starting lexical backfill (collection enumeration)", "chunks", count)
	wd.heartbeat(StageBleveUpsert, "")

	// chromem has no ListAll; a single-space query enumerates every document
	// (the ranking is irrelevant). Prefer the embedding-free unit-vector
	// enumeration when the dimension is known (ADR-049); the read lock covers
	// only this query.
	//
	// The count is re-sampled INSIDE the lock, immediately before the query:
	// the caller's count predates the migration wait and sidecar walk, and a
	// stale count exceeding the live count makes chromem hard-error
	// ("nResults must be <= the number of documents"), permanently failing
	// the backfill; 0 errors too. The earlier count feeds only the initial
	// progress event.
	idx.service.mu.RLock()
	liveCount := col.Count()
	if liveCount == 0 {
		idx.service.mu.RUnlock()
		idx.onProgress(PhaseLexical, IndexStateReady, 0, 0, "")
		idx.logger.Info("lexical backfill skipped: collection emptied since the count was sampled")
		return nil
	}
	var results []chromem.Result
	var err error
	if unitVec := idx.service.unitQueryVector(); unitVec != nil {
		results, err = col.QueryEmbedding(ctx, unitVec, liveCount, nil, nil)
	} else {
		results, err = col.Query(ctx, " ", liveCount, nil, nil)
	}
	idx.service.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("enumerating collection for lexical rebuild: %w", err)
	}

	// upsertWindow commits one bleve window under the service write lock with
	// the same re-validation as the sidecar path's flush (a concurrent
	// switch/Close would otherwise upsert into a closed, nil'd index).
	upsertWindow := func(window []lexical.Doc) error {
		wd.heartbeat(StageBleveUpsert, "")
		started := time.Now()
		idx.service.AcquireWriteLock()
		if cur := idx.service.currentLexicalLocked(); cur != lex {
			idx.service.ReleaseWriteLock()
			return fmt.Errorf("lexical backfill aborted: lexical index replaced by a concurrent switch: %w", context.Canceled)
		}
		upErr := lex.Upsert(ctx, window)
		idx.service.ReleaseWriteLock()
		idx.telemetry.observe(StageBleveUpsert, len(window), time.Since(started))
		return upErr
	}

	// Content-less documents (see strippedForCommit) are reconstructed from
	// the source file; one resolver serves the pass, so each file is read at
	// most once. Missing/unreadable sources are skipped, not indexed with
	// the placeholder — that text would surface as bogus lexical hits (the
	// deletion reconciliation removes such documents next pass anyway).
	resolver := newContentResolver(idx.maxFileSize, idx.maxChunkSize)
	// The workspace root the occurrence coordinates resolve against for
	// per-request source reads (ADR-104 L4); legacy documents without a
	// rel_path coordinate fall back to their stored absolute path.
	rebuildRoot := idx.service.WorkspaceRoot()
	skippedUnreadable := 0

	batch := make([]lexical.Doc, 0, addDocumentBatchSize)
	processed := 0
	for _, r := range results {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("lexical rebuild cancelled: %w", err)
		}
		// Contentless re-reads are real file I/O — keep the last file fresh.
		wd.heartbeat(StageBleveUpsert, occurrencePath(r.Metadata))
		content := r.Content
		if content == "" {
			startLine, _ := strconv.Atoi(r.Metadata["start_line"])
			endLine, _ := strconv.Atoi(r.Metadata["end_line"])
			reconstructed, ok := resolver.chunkContentOK(sourcePath(r.Metadata, rebuildRoot), startLine, endLine)
			if !ok {
				skippedUnreadable++
				continue
			}
			content = reconstructed
		}
		batch = append(batch, lexical.Doc{
			ID:       r.ID,
			FilePath: occurrencePath(r.Metadata),
			Language: r.Metadata["language"],
			Content:  content,
		})
		if len(batch) >= addDocumentBatchSize {
			if upErr := upsertWindow(batch); upErr != nil {
				return fmt.Errorf("lexical upsert batch: %w", upErr)
			}
			batch = batch[:0]
		}
		processed++
		idx.onProgress(PhaseLexical, IndexStateIndexing, processed, liveCount, r.Metadata["file_path"])
	}
	if len(batch) > 0 {
		if upErr := upsertWindow(batch); upErr != nil {
			return fmt.Errorf("lexical final upsert: %w", upErr)
		}
	}
	if skippedUnreadable > 0 {
		idx.logger.Warn("lexical backfill skipped documents whose source files are missing or unreadable",
			"skipped", skippedUnreadable, "chunks", liveCount)
	}

	idx.onProgress(PhaseLexical, IndexStateReady, liveCount, liveCount, "")
	idx.logger.Info("lexical backfill complete", "chunks", processed)
	return nil
}

// collectDocumentIDs enumerates document IDs for the given file paths. When
// every sidecar entry carries its committed chunk-index set, the IDs derive
// arithmetically from DocumentID — no collection Query, the
// full-materialization cost ADR-049 removed from the incremental deletion
// step; entries without a set keep the Query fallback. The caller must hold
// the service write lock, which also guards the sidecar map read.
func (idx *Indexer) collectDocumentIDs(ctx context.Context, filePaths []string) ([]string, error) {
	col := idx.service.GetCollection()
	if col == nil {
		return nil, nil
	}

	count := col.Count()
	if count == 0 {
		return nil, nil
	}

	if ids, ok := idx.service.sidecarDocumentIDs(filePaths); ok {
		return ids, nil
	}

	return idx.collectDocumentIDsViaQuery(ctx, filePaths)
}

// collectDocumentIDsViaQuery is the Query-based enumeration
// collectDocumentIDs falls back to when an entry lacks its committed chunk
// set: one broad enumeration filtered to the requested paths, via the
// embedding-free unit-vector query when the dimension is known (ADR-049).
func (idx *Indexer) collectDocumentIDsViaQuery(ctx context.Context, filePaths []string) ([]string, error) {
	col := idx.service.GetCollection()
	if col == nil {
		return nil, nil
	}

	count := col.Count()
	if count == 0 {
		return nil, nil
	}

	var results []chromem.Result
	var err error
	if unitVec := idx.service.unitQueryVector(); unitVec != nil {
		results, err = col.QueryEmbedding(ctx, unitVec, count, nil, nil)
	} else {
		results, err = col.Query(ctx, " ", count, nil, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("querying collection for document IDs: %w", err)
	}

	// Compare on the occurrence coordinate: sidecar-keyed paths arrive as
	// absolute workspace paths and stored documents carry either the
	// root-relative rel_path (post-ADR-104) or the legacy absolute
	// file_path — both must match their translated key (see
	// Service.fileHashKeyLocked, which the write-lock-holding caller's
	// collection state is keyed under).
	pathSet := make(map[string]bool, len(filePaths))
	for _, p := range filePaths {
		pathSet[idx.service.fileHashKeyLocked(p)] = true
		pathSet[p] = true
	}

	var ids []string
	for _, r := range results {
		if pathSet[occurrencePath(r.Metadata)] {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}

// walkGap is one workspace entry the walk could not read or descend into —
// a subdirectory deleted or made unreadable between the parent's enumeration
// and its visit (an editor/`git checkout` removing a tree mid-pass, a
// permission change on a mounted tree). Path is absolute; Err is the
// traversal error.
type walkGap struct {
	Path string
	Err  error
}

// walkOutcome is what a workspace walk produced: the indexable absolute file
// paths plus the entries the traversal could not read. Gaps are NOT fatal —
// they are the live-tree race of a workspace mutating under the walk — but
// they are reported so a build can tell "this tree has no indexable files"
// apart from "this tree could not be fully traversed" (the empty-corpus gate
// in PublishCorpus).
type walkOutcome struct {
	Files []string
	Gaps  []walkGap
}

// walkProjectFiles walks the workspace and returns all indexable file paths.
// checker must be pre-built by the caller (the Indexer caches it) so the walk
// and the watcher filter share one source of truth. Files and directories
// ignored by the honoured ignore files plus extra exclude patterns (root and
// nested, resolved via checker) are skipped, as are hidden (leading-dot)
// entries. includeSubmodules=false additionally prunes nested-repository
// directories (a checked-out submodule: .git is a regular gitdir-pointer
// file); the walk root itself is always exempt, so a linked-worktree
// workspace stays fully indexable. Binary content is not detected here — it
// is a read-time guard applied in processFile.
//
// A per-entry read failure BELOW the root (a subdirectory deleted or made
// unreadable mid-walk) is recorded as a gap and the traversal continues: one
// vanishing subtree must never fail a whole pass, or a workspace that keeps
// dropping generated directories fails its rebuild identically every time.
// Only the ROOT itself being unreadable aborts the walk. Callers that need
// the gaps use walkProjectFilesDetailed.
func walkProjectFiles(root string, checker ignore.IgnoreChecker, maxFileSize int64, includeSubmodules bool) ([]string, error) {
	outcome, err := walkProjectFilesDetailed(root, checker, maxFileSize, includeSubmodules, nil)
	if err != nil {
		return nil, err
	}
	return outcome.Files, nil
}

// walkProjectFilesDetailed is walkProjectFiles returning the traversal gaps
// alongside the indexable paths (the full pass publishes the gaps to the
// corpus gate; see walkOutcome). onEntry, when non-nil, receives EVERY
// directory entry the walk visits — directories, files, ignored entries and
// erroring entries alike — because the arrival of entries is what a slow
// filesystem paces; the full pass feeds it into the stall watchdog
// (see walkHeartbeater) so a slow-but-alive walk is never misread as
// stalled while a single hung stat still is.
func walkProjectFilesDetailed(root string, checker ignore.IgnoreChecker, maxFileSize int64, includeSubmodules bool, onEntry func(string)) (walkOutcome, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return walkOutcome{}, fmt.Errorf("resolving root path: %w", err)
	}

	var outcome walkOutcome
	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkDirErr error) error {
		if onEntry != nil {
			onEntry(path)
		}
		if walkDirErr != nil {
			// The root is the walk's subject: if it cannot be read there is
			// nothing to index, so an explicit pass error is the honest
			// outcome (never a silent empty corpus).
			if path == absRoot {
				return walkDirErr
			}
			// Below the root a vanished/unreadable entry is the live-tree
			// race: record the gap, keep walking its siblings.
			outcome.Gaps = append(outcome.Gaps, walkGap{Path: path, Err: walkDirErr})
			return nil
		}

		if d.IsDir() {
			if isHiddenName(d.Name()) || checker.Ignored(path, true) {
				return filepath.SkipDir
			}
			if !includeSubmodules && path != absRoot && isNestedRepoDir(path) {
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

		// Skip oversized files at walk time so they never enter the pipeline
		// (and never reappear as "new" each pass). An optimization only — the
		// universal guarantee is processFile's read-time backstop. A stat
		// failure is treated as skip-safe.
		if info, infoErr := d.Info(); infoErr == nil && tooLargeForIndex(info.Size(), maxFileSize) {
			return nil
		}

		absPath, absErr := filepath.Abs(path)
		if absErr != nil {
			return nil //nolint:nilerr // skip unresolvable paths
		}

		outcome.Files = append(outcome.Files, absPath)
		return nil
	})
	if walkErr != nil {
		return walkOutcome{}, fmt.Errorf("walking workspace %s: %w", absRoot, walkErr)
	}

	return outcome, nil
}

// noopIgnoreChecker is the fail-open fallback when an ignore.Resolver cannot
// be built for a root (e.g. the root vanished mid-index): index every
// non-hidden path rather than nothing.
type noopIgnoreChecker struct{}

func (noopIgnoreChecker) Ignored(string, bool) bool { return false }

// buildIgnoreChecker constructs an ignore resolver over the honoured ignore
// files (root and nested) plus the extra exclude patterns for root, per opts
// (nil IgnoreFiles = both .gitignore and .aiignore, the historical default).
// When no ignore file is honoured and there are no extra patterns, nothing can
// ever be reported ignored, so the resolver's workspace walk is skipped
// entirely and the fail-open noopIgnoreChecker is returned. On any other
// failure it returns the noopIgnoreChecker together with the error so callers
// can log once without re-attempting on every debounce flush.
func buildIgnoreChecker(root string, opts ignore.Options) (ignore.IgnoreChecker, error) {
	if opts.IgnoreFiles != nil && len(opts.IgnoreFiles) == 0 && len(opts.ExtraPatterns) == 0 {
		return noopIgnoreChecker{}, nil
	}
	r, err := ignore.NewResolverWithOptions(root, opts)
	if err != nil {
		return noopIgnoreChecker{}, fmt.Errorf("building ignore resolver for %q: %w", root, err)
	}
	return r, nil
}

// resolverFor returns the cached ignore resolver for root, building it on
// first use; the cache is per-project (recreated on SwitchProject), so the
// workspace is walked at most once per project instead of once per debounce
// flush. A mid-session edit of .gitignore/.aiignore lands on the next
// source-driven pass — a stale filter risks only a harmless no-op reindex.
func (idx *Indexer) resolverFor(root string) ignore.IgnoreChecker {
	idx.ignoreMu.RLock()
	if idx.ignoreRoot == root && idx.ignoreChecker != nil {
		c := idx.ignoreChecker
		idx.ignoreMu.RUnlock()
		return c
	}
	idx.ignoreMu.RUnlock()

	c, err := buildIgnoreChecker(root, idx.ignoreOptions)
	if err != nil {
		idx.logger.Warn("failed to build ignore resolver; indexing all non-hidden files", "root", root, "error", err)
	}
	idx.ignoreMu.Lock()
	idx.ignoreRoot = root
	idx.ignoreChecker = c
	idx.ignoreMu.Unlock()
	return c
}

// IsAnyIndexablePath reports whether at least one of changedPaths is indexable
// under root, reusing the cached ignore resolver (the watcher-facing check).
func (idx *Indexer) IsAnyIndexablePath(changedPaths []string, root string) bool {
	c := idx.resolverFor(root)
	for _, p := range changedPaths {
		if isIndexablePath(p, root, c, idx.includeSubmodules) {
			return true
		}
	}
	return false
}

// isIndexablePath is the core single-path check with a pre-built ignore
// resolver. It avoids os.Stat by inspecting path segments directly, so it
// works for both file and directory change events. A path is not indexable
// when any segment is hidden (leading dot) or when the resolver reports the
// path (or an ancestor directory) as ignored — the resolver walks ancestor
// directories with directory semantics, so "once a directory is ignored, so
// are its contents" holds in a single call. With includeSubmodules=false a
// path is additionally not indexable when any directory from the first
// segment below the root down to the path itself is a nested-repository root
// (.git regular file — a submodule checkout); the walk root is never tested,
// so a linked-worktree workspace keeps indexing its own tree.
func isIndexablePath(absPath, root string, checker ignore.IgnoreChecker, includeSubmodules bool) bool {
	relPath, err := filepath.Rel(root, absPath)
	if err != nil {
		return false
	}
	relPath = filepath.ToSlash(relPath)
	if relPath == "." || strings.HasPrefix(relPath, "..") {
		// The root itself, or outside it: not a concrete indexable file.
		return false
	}

	// Any hidden segment (directory or file, leading dot) is not indexable.
	segs := strings.Split(relPath, "/")
	for _, seg := range segs {
		if seg == "" || isHiddenName(seg) {
			return false
		}
	}

	// Nested-repository roots (submodules) on the ancestor chain — including
	// the path's own segment, so a watcher event naming the submodule
	// directory itself also reports not-indexable. The Lstat on a file
	// segment simply fails and costs nothing semantically.
	if !includeSubmodules {
		dir := root
		for _, seg := range segs {
			dir = filepath.Join(dir, seg)
			if isNestedRepoDir(dir) {
				return false
			}
		}
	}

	// Honoured ignore files + extra exclude patterns, including
	// directory-only rules via ancestor walking. The leaf is tested as a
	// file (watcher events are predominantly file creates/modifies);
	// ancestor dirs are treated as dirs by the resolver.
	return !checker.Ignored(absPath, false)
}

// isNestedRepoDir reports whether dir is the root of a nested repository
// checkout whose .git entry is a regular file — the on-disk shape shared by
// a checked-out submodule (a "gitdir: <path>" pointer into the
// superproject's .git/modules tree) and a linked worktree. Detection is
// deliberately stat-only: git itself treats any regular-file .git as a
// gitdir pointer, so a content read would add per-directory I/O without
// changing the verdict. Callers never apply it to the walk root itself — a
// linked-worktree workspace legitimately carries a pointer .git at its own
// root and is fully indexable (see
// TestIsIndexablePath_ManagedSessionIndexesOwnTree). An unresolvable stat (missing .git, permissions) reports false:
// an ordinary directory must never be mistaken for a nested repository.
func isNestedRepoDir(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

// isHiddenName reports whether name is a hidden entry — one whose base name
// begins with a dot. This is the universal guard layered on top of the ignore
// resolver: hidden directories (e.g. .git, .cache, .idea) and hidden files
// (e.g. .DS_Store) are never indexed regardless of ignore-file contents.
func isHiddenName(name string) bool {
	return strings.HasPrefix(name, ".")
}

// binaryHeaderSize is the number of leading bytes inspected for binary
// detection (git's heuristic; effectively free to read even on multi-GB files).
const binaryHeaderSize = 512

// DefaultMaxIndexableFileSize caps how much of a file is ever read for
// indexing (walk, validation, and read time all enforce it). The NUL-byte
// binary check cannot recognize every binary format — ONNX and safetensors
// are protobuf with readable-ASCII headers — so a multi-hundred-MB model
// would otherwise be loaded whole and explode memory during chunking (a
// 522 MB model yields ~400k chunks). 4 MiB is generous for source code;
// overridable via vector_index.max_file_size.
const DefaultMaxIndexableFileSize int64 = 4 * 1024 * 1024 // 4 MiB

// DefaultMaxChunkSize is the default chunker input bound in characters, sized
// to the embedding model's context window (512 tokens ≈ ~2000 chars) with
// room for overlap; overridable via vector_index.max_chunk_size.
const DefaultMaxChunkSize = 1500

// DefaultMaxChunksPerFile caps the chunks one file may contribute to a pass;
// exceeding it skips the file wholesale. Embedding is bounded by the
// inference batch and embeddingCommitWindowSize, so the cap is a backstop
// against a pathological count (a HuggingFace BPE vocab/merges tokenizer.json
// fragments into tens of thousands of chunks that would hang/OOM the
// embedder). 4000 sits above the worst legitimate source file (a 4 MiB file
// at 1500 chars yields ~3230 chunks), so no real source file is dropped.
const DefaultMaxChunksPerFile = 4000

// DefaultChunkOverlap is the default character overlap between adjacent
// chunks: the historical hardcoded value (200), now the config-layer default
// for vector_index.chunk_overlap, kept here so backend/config need not
// duplicate the number.
const DefaultChunkOverlap = 200

// tooLargeForIndex reports whether a file of the given byte size exceeds the
// indexable limit and must be skipped before any full read.
func tooLargeForIndex(size, limit int64) bool {
	return size > limit
}

// containsNullByte reports whether b contains a NUL byte, the classic binary
// indicator (used by git and diff).
func containsNullByte(b []byte) bool {
	for i := 0; i < len(b); i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

// isBinaryHeader reports whether the file begins with binary content (a NUL
// byte within the first binaryHeaderSize bytes). It reads at most
// binaryHeaderSize bytes, so it is safe on very large files — the walk/read-
// time guard that rejects multi-GB binary assets before a full read. An open
// error is returned for the caller to skip or fail; short reads are normal
// and scanned over whatever arrived.
func isBinaryHeader(filePath string) (bool, error) {
	f, err := safeio.Open(filePath)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, binaryHeaderSize)
	// io.ReadFull's EOF/ErrUnexpectedEOF are both expected (short files); n
	// holds the bytes actually read, which is all we scan.
	n, _ := io.ReadFull(f, header)
	return containsNullByte(header[:n]), nil
}
