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

	// PrepWorkers is the number of goroutines that run file preparation
	// (read/hash/chunk — pure I/O+CPU work) in parallel with the embedding
	// consumer, so one file's ONNX inference overlaps the next file's disk
	// read and chunking. 1 reproduces the historical strictly serial
	// pipeline; 0 (or unset) defaults to DefaultPrepWorkers. It bounds only
	// the prep stage: a single consumer still drives Service.AddDocuments,
	// keeping embedding single-threaded under the service write lock.
	// ChunkFn and HashFn must be safe for concurrent use.
	PrepWorkers int
	OnProgress  ProgressCallback
	Logger      *slog.Logger
	// Telemetry optionally collects bounded, content-free stage aggregates.
	Telemetry *Telemetry
}

// Indexer orchestrates initial and incremental indexing of project files.
type Indexer struct {
	service          *Service
	chunkFn          ChunkFunc
	hashFn           HashFunc
	maxChunkSize     int
	maxFileSize      int64
	maxChunksPerFile int
	overlap          int
	contentFilter    ContentFilterConfig
	prepWorkers      int
	onProgress       ProgressCallback
	logger           *slog.Logger
	telemetry        *Telemetry

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
	onProgress := cfg.OnProgress
	if onProgress == nil {
		onProgress = func(IndexPhase, IndexState, int, int, string) {}
	}
	return &Indexer{
		service:          cfg.Service,
		chunkFn:          cfg.ChunkFn,
		hashFn:           hashFn,
		maxChunkSize:     maxChunkSize,
		maxFileSize:      maxFileSize,
		maxChunksPerFile: maxChunksPerFile,
		overlap:          overlap,
		contentFilter:    contentFilter,
		prepWorkers:      prepWorkers,
		onProgress:       onProgress,
		logger:           logger,
		telemetry:        cfg.Telemetry,
	}
}

// IndexFull performs a full workspace indexing: walks all files, chunks them,
// and adds the resulting documents to both the vector collection and the
// lexical index.
func (idx *Indexer) IndexFull(ctx context.Context, workspacePath string) (err error) {
	// MarkNotReady atomically captures a readiness generation; RestoreReady
	// uses it to restore readiness on EVERY exit, including the cancellation
	// and batch-add error paths that once returned without SetReady(true),
	// leaving WaitReady callers blocked forever.
	readyGen := idx.service.MarkNotReady()
	defer idx.service.RestoreReady(readyGen)
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

	walkStarted := time.Now()
	files, err := walkProjectFiles(workspacePath, idx.resolverFor(workspacePath), idx.maxFileSize)
	idx.telemetry.observe(StageWalkValidation, len(files), time.Since(walkStarted))
	if err != nil {
		return fmt.Errorf("walking project files: %w", err)
	}

	totalFiles := len(files)
	idx.onProgress(PhaseBoth, IndexStateIndexing, 0, totalFiles, "")
	idx.logger.Info("starting full index", "workspace", workspacePath, "files", totalFiles)

	idx.service.AcquireWriteLock()
	defer idx.service.ReleaseWriteLock()

	indexed, idxErr := idx.indexPrepared(ctx, files, indexPipelineOpts{
		state:        IndexStateIndexing,
		total:        totalFiles,
		cancelMsg:    "indexing cancelled",
		skipLogLabel: "skipping file",
	})
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
	readyGen := idx.service.MarkNotReady()
	defer idx.service.RestoreReady(readyGen)
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

	// Wait for the background file-hash migration (first run after the
	// sidecar upgrade, or a branch whose collection was built elsewhere) so
	// ValidateCollection sees the full hash map instead of re-embedding
	// every file. IndexFull is unaffected: an empty collection has nothing
	// to migrate. See specs/decisions/049-vector-index-memory-reduction.md.
	if err := idx.service.WaitFileHashMigration(ctx); err != nil {
		return fmt.Errorf("waiting for file-hash migration: %w", err)
	}
	// Same gate for the content-less migration: its ID-preserving re-commits
	// must not interleave with this pass's deletions and re-indexing, or a
	// just-deleted legacy document could be resurrected from the migration's
	// probe snapshot. Empty collections carry the marker from the start.
	// See specs/decisions/049-vector-index-memory-reduction.md.
	if err := idx.service.WaitContentlessMigration(ctx); err != nil {
		return fmt.Errorf("waiting for content-less migration: %w", err)
	}

	validationStarted := time.Now()
	stale, newFiles, deleted, err := idx.service.ValidateCollection(ctx, workspacePath, idx.resolverFor(workspacePath))
	idx.telemetry.observe(StageWalkValidation, len(stale)+len(newFiles)+len(deleted), time.Since(validationStarted))
	if err != nil {
		return fmt.Errorf("validating collection: %w", err)
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

	idx.service.AcquireWriteLock()
	defer idx.service.ReleaseWriteLock()

	idx.onProgress(PhaseBoth, IndexStateReindexing, 0, totalChanges, "")

	// Delete documents for deleted and stale files.
	filesToDelete := make([]string, 0, len(deleted)+len(stale))
	filesToDelete = append(filesToDelete, deleted...)
	filesToDelete = append(filesToDelete, stale...)

	if len(filesToDelete) > 0 {
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
	}

	if len(filesToDelete) > 0 {
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
	}); perr != nil {
		return perr
	}

	totalInIndex := idx.service.collectionUniqueFileCount()
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
// document ID.
func (idx *Indexer) processFile(filePath string) ([]chromem.Document, []lexical.Doc, error) {
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

	// Bounded header pre-read rejects multi-GB binary assets (.onnx,
	// .safetensors, .psd) before they are loaded fully into memory; see
	// isBinaryHeader.
	binary, bErr := isBinaryHeader(filePath)
	if bErr != nil {
		return nil, nil, fmt.Errorf("reading file %s: %w", filePath, bErr)
	}
	if binary {
		return nil, nil, nil
	}

	content, err := safeio.ReadFile(filePath)
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
		docID := DocumentID(filePath, i)
		vecDocs = append(vecDocs, chromem.Document{
			ID:      docID,
			Content: chunk.Content,
			Metadata: map[string]string{
				"file_path":            filePath,
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
		lexDocs = append(lexDocs, lexical.Doc{
			ID:       docID,
			FilePath: filePath,
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
// per-pass policy (indexPipelineOpts.countSkipped).
type prepUnit struct {
	path    string
	skipped bool
	vecDocs []chromem.Document
	lexDocs []lexical.Doc
}

// streamPreparedFiles fans file preparation out across idx.prepWorkers
// goroutines and returns the stream of finished units. Preparation is pure
// I/O+CPU work — stat, binary-header check, read, hash, chunk; processFile
// never touches the service or other shared state — so files are prepared
// concurrently while the single consumer embeds earlier batches; the
// unbuffered input channel bounds in-flight preparation to prepWorkers files.
//
// The stream closes after every path has been prepared, skipped, or dropped:
// every send and the feeder select on ctx.Done(), so a consumer that returns
// early never deadlocks the pool — the feeder stops feeding, workers abandon
// in-flight sends, all goroutines exit.
func (idx *Indexer) streamPreparedFiles(ctx context.Context, paths []string, skipLogLabel string) <-chan prepUnit {
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
				prepStarted := time.Now()
				vecDocs, lexDocs, err := idx.processFile(path)
				idx.telemetry.observe(StageReadHashChunk, len(vecDocs), time.Since(prepStarted))
				unit := prepUnit{path: path, vecDocs: vecDocs, lexDocs: lexDocs}
				if err != nil {
					idx.logger.Warn(skipLogLabel, "path", path, "error", err)
					unit.skipped = true
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
	service          *Service
	telemetry        *Telemetry
	pendingInference []pendingDocument
	pendingCommit    []pendingDocument
}

func newDocumentAccumulator(service *Service, telemetry *Telemetry) *documentAccumulator {
	return &documentAccumulator{
		service:          service,
		telemetry:        telemetry,
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
	embedStarted := time.Now()
	kept, dropped, err := a.service.embedSubBatch(ctx, docs)
	a.telemetry.observe(StageEmbedding, len(docs), time.Since(embedStarted))
	if err != nil {
		return err
	}
	if len(kept)+len(dropped) != len(docs) {
		return fmt.Errorf("embedding accumulator lost documents: kept=%d dropped=%d input=%d", len(kept), len(dropped), len(docs))
	}

	keptIdx := 0
	for i := range a.pendingInference {
		item := a.pendingInference[i]
		if _, drop := dropped[item.vec.ID]; drop {
			item.file.completed++
			a.publishFileIfComplete(item.file)
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
	if err := a.service.commitEmbeddedDocuments(ctx, vecDocs, lexDocs); err != nil {
		return err
	}
	for i := range a.pendingCommit {
		file := a.pendingCommit[i].file
		file.committedIDs = append(file.committedIDs, a.pendingCommit[i].vec.ID)
		file.completed++
		a.publishFileIfComplete(file)
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
// inference tail flushes once, at pass end. Embedding stays single-threaded,
// and the caller must already hold the service write lock.
//
// Returns the number of files counted toward pass progress: per-file prep
// failures are logged and skipped; ctx cancellation and AddDocuments failures
// abort the pass.
func (idx *Indexer) indexPrepared(ctx context.Context, paths []string, o indexPipelineOpts) (int, error) {
	// Derived cancellable context: the prep pool is released promptly when
	// this consumer returns early (feeder and worker sends select on it).
	poolCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	units := idx.streamPreparedFiles(poolCtx, paths, o.skipLogLabel)

	progress := o.progressStart
	var vecBatch []chromem.Document
	var lexBatch []lexical.Doc
	var accumulator *documentAccumulator
	if idx.service.batchEmbedder != nil {
		accumulator = newDocumentAccumulator(idx.service, idx.telemetry)
	}

	for unit := range units {
		if err := ctx.Err(); err != nil {
			return progress, fmt.Errorf("%s: %w", o.cancelMsg, err)
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
					if addErr := idx.service.AddDocuments(ctx, vecBatch, lexBatch); addErr != nil {
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
		if addErr := idx.service.AddDocuments(ctx, vecBatch, lexBatch); addErr != nil {
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

	// The sidecar is the v2 driving table; wait for its one-time backfill so
	// the snapshot below is complete, not the empty placeholder.
	if err := idx.service.WaitFileHashMigration(ctx); err != nil {
		return fmt.Errorf("waiting for file-hash migration before lexical rebuild: %w", err)
	}
	hashes, err := idx.service.GetCollectionFiles()
	if err != nil {
		return fmt.Errorf("reading file-hash sidecar for lexical rebuild: %w", err)
	}
	if len(hashes) == 0 {
		// Transitional fallback: no sidecar at all (the backfill found
		// nothing trackable). Enumerate the collection the old way.
		idx.logger.Info("lexical rebuild: empty sidecar, using collection enumeration")
		return idx.rebuildLexicalFromCollection(ctx, lex, col, count)
	}

	paths := make([]string, 0, len(hashes))
	for fp := range hashes {
		paths = append(paths, fp)
	}
	slices.Sort(paths)

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
		docs, reason := idx.lexicalDocsForSidecarFile(fp, hashes[fp])
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
		return idx.rebuildLexicalFromCollection(ctx, lex, col, count)
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
func (idx *Indexer) lexicalDocsForSidecarFile(filePath, entry string) (docs []lexical.Doc, reason string) {
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

	content, err := readBounded(filePath, idx.maxFileSize)
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

	chunks, err := idx.chunkFn(filePath, content, idx.maxChunkSize, idx.overlap)
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
			ID:       DocumentID(filePath, i),
			FilePath: filePath,
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
// The caller must not hold s.mu.
func (idx *Indexer) rebuildLexicalFromCollection(ctx context.Context, lex lexical.Index, col *chromem.Collection, count int) error {
	idx.onProgress(PhaseLexical, IndexStateIndexing, 0, count, "")
	idx.logger.Info("starting lexical backfill (collection enumeration)", "chunks", count)

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
	skippedUnreadable := 0

	batch := make([]lexical.Doc, 0, addDocumentBatchSize)
	processed := 0
	for _, r := range results {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("lexical rebuild cancelled: %w", err)
		}
		content := r.Content
		if content == "" {
			startLine, _ := strconv.Atoi(r.Metadata["start_line"])
			endLine, _ := strconv.Atoi(r.Metadata["end_line"])
			reconstructed, ok := resolver.chunkContentOK(r.Metadata["file_path"], startLine, endLine)
			if !ok {
				skippedUnreadable++
				continue
			}
			content = reconstructed
		}
		batch = append(batch, lexical.Doc{
			ID:       r.ID,
			FilePath: r.Metadata["file_path"],
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

	pathSet := make(map[string]bool, len(filePaths))
	for _, p := range filePaths {
		pathSet[p] = true
	}

	var ids []string
	for _, r := range results {
		if pathSet[r.Metadata["file_path"]] {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}

// walkProjectFiles walks the workspace and returns all indexable file paths.
// checker must be pre-built by the caller (the Indexer caches it) so the walk
// and the watcher filter share one source of truth. Ignored (.gitignore /
// .aiignore, root and nested) and hidden entries are skipped; binary content
// is a read-time guard in processFile, not detected here.
func walkProjectFiles(root string, checker ignore.IgnoreChecker, maxFileSize int64) ([]string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving root path: %w", err)
	}

	var files []string
	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkDirErr error) error {
		if walkDirErr != nil {
			return walkDirErr
		}

		if d.IsDir() {
			if isHiddenName(d.Name()) || checker.Ignored(path, true) {
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

		files = append(files, absPath)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walking workspace %s: %w", absRoot, walkErr)
	}

	return files, nil
}

// noopIgnoreChecker is the fail-open fallback when an ignore.Resolver cannot
// be built for a root (e.g. the root vanished mid-index): index every
// non-hidden path rather than nothing.
type noopIgnoreChecker struct{}

func (noopIgnoreChecker) Ignored(string, bool) bool { return false }

// buildIgnoreChecker constructs an ignore resolver (.gitignore + .aiignore,
// root and nested) for root; on failure it returns a fail-open
// noopIgnoreChecker plus the error, so callers log once without re-attempting
// on every debounce flush.
func buildIgnoreChecker(root string) (ignore.IgnoreChecker, error) {
	r, err := ignore.NewResolver(root)
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

	c, err := buildIgnoreChecker(root)
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
		if isIndexablePath(p, root, c) {
			return true
		}
	}
	return false
}

// isIndexablePath is the core single-path check with a pre-built ignore
// resolver. It avoids os.Stat by inspecting path segments, so it works for
// both file and directory change events: a path is not indexable when any
// segment is hidden (leading dot) or the resolver reports it (or an ancestor)
// as ignored — the resolver walks ancestors with directory semantics, so
// "once a directory is ignored, so are its contents" holds in one call.
func isIndexablePath(absPath, root string, checker ignore.IgnoreChecker) bool {
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
	for _, seg := range strings.Split(relPath, "/") {
		if seg == "" || isHiddenName(seg) {
			return false
		}
	}

	// .gitignore / .aiignore, including directory-only rules via ancestor
	// walking. The leaf is tested as a file (watcher events are predominantly
	// file creates/modifies); ancestor dirs are treated as dirs by the resolver.
	return !checker.Ignored(absPath, false)
}

// isHiddenName reports whether name is a hidden entry (leading dot) — the
// universal guard on top of the resolver: hidden dirs (.git, .cache) and
// files (.DS_Store) are never indexed regardless of ignore-file contents.
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
