// Package vectorindex provides vector-based code search for the desktop UI.
// It combines an ONNX-powered embedding index (chromem-go) with a lexical
// (bleve-based) text search index that share a unified indexing pipeline.
package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
	"github.com/v0lka/sp4rk/ignore"
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
	// IndexStateUnavailable indicates vector indexing is disabled for the
	// active project because asynchronous initialization failed (e.g. the
	// persistent DB could not be opened, or the git branch collection could
	// not be switched). Search then returns a clean "no collection" error.
	// It mirrors the state string the frontend already renders as dormant
	// (see indexPhaseStatus.deriveDotStatus), so no UI changes are needed.
	IndexStateUnavailable IndexState = "unavailable"
)

// IndexPhase describes which side(s) of the dual index are being produced
// for a given progress event. Hybrid indexing normally reports PhaseBoth;
// the lazy BM25 backfill (RebuildLexical) reports PhaseLexical so the UI
// can distinguish it from a cold-start embedding pass.
type IndexPhase string

const (
	// PhaseBoth indicates progress for combined vector + lexical indexing.
	PhaseBoth IndexPhase = "both"
	// PhaseEmbedding indicates progress for vector embedding only.
	PhaseEmbedding IndexPhase = "embedding"
	// PhaseLexical indicates progress for lexical (BM25) indexing only.
	PhaseLexical IndexPhase = "lexical"
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

	// PrepWorkers is the number of goroutines that run file preparation
	// (read/hash/chunk — pure I/O+CPU work) in parallel with the embedding
	// consumer, so one file's ONNX inference overlaps the next file's disk
	// read and chunking. 1 reproduces the historical strictly serial
	// pipeline; 0 (or unset) defaults to DefaultPrepWorkers (2). It bounds
	// only the prep stage: a single consumer still drives
	// Service.AddDocuments, keeping embedding single-threaded under the
	// service write lock exactly as before. ChunkFn and HashFn must be safe
	// for concurrent use (the defaults are pure functions over the file's
	// bytes).
	PrepWorkers int
	OnProgress  ProgressCallback
	Logger      *slog.Logger
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
	prepWorkers      int
	onProgress       ProgressCallback
	logger           *slog.Logger

	// ignoreMu guards ignoreRoot / ignoreChecker, a cache of the workspace's
	// ignore.Resolver (.gitignore + .aiignore, root and nested) built once
	// (during the first walk or watcher filter) and reused thereafter, so the
	// watcher does not re-walk the workspace on every debounce flush. The
	// cache is per-project: the Indexer is recreated on SwitchProject.
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
		prepWorkers:      prepWorkers,
		onProgress:       onProgress,
		logger:           logger,
	}
}

// IndexFull performs a full workspace indexing: walks all files, chunks them,
// and adds the resulting documents to both the vector collection and the
// lexical index.
func (idx *Indexer) IndexFull(ctx context.Context, workspacePath string) (err error) {
	// MarkNotReady atomically captures a readiness generation; RestoreReady
	// uses it to avoid racing with a concurrent project switch. Together
	// they guarantee readiness is ALWAYS restored on exit — including
	// cancellation and batch-add error paths that previously returned
	// without SetReady(true), leaving WaitReady callers blocked forever.
	readyGen := idx.service.MarkNotReady()
	defer idx.service.RestoreReady(readyGen)
	// Emit a terminal "ready" progress event on any non-nil return so the UI
	// does not keep showing stale "indexing N/M" progress after the pass is
	// cancelled or fails — the previous data is still queryable. On success
	// the explicit onProgress below handles it and err stays nil.
	defer func() {
		if err != nil {
			idx.onProgress(PhaseBoth, IndexStateReady, 0, 0, "")
		}
	}()

	files, err := walkProjectFiles(workspacePath, idx.resolverFor(workspacePath), idx.maxFileSize)
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
	// MarkNotReady atomically captures a readiness generation; RestoreReady
	// uses it to avoid racing with a concurrent project switch. Together
	// they guarantee readiness is ALWAYS restored on exit — including
	// cancellation and batch-add error paths that previously returned
	// without SetReady(true), leaving WaitReady callers blocked forever.
	readyGen := idx.service.MarkNotReady()
	defer idx.service.RestoreReady(readyGen)
	// Emit a terminal "ready" progress event on any non-nil return so the UI
	// does not keep showing stale "reindexing N/M" progress after the pass is
	// cancelled or fails — the previous data is still queryable. On success
	// the explicit onProgress below handles it and err stays nil.
	defer func() {
		if err != nil {
			idx.onProgress(PhaseBoth, IndexStateReady, 0, 0, "")
		}
	}()

	// Wait for any background file-hash migration (first run after the sidecar
	// upgrade, or a branch whose collection was built elsewhere) so
	// ValidateCollection sees the full hash map instead of re-embedding every
	// file. IndexFull is unaffected: an empty collection has nothing to migrate.
	if err := idx.service.WaitFileHashMigration(ctx); err != nil {
		return fmt.Errorf("waiting for file-hash migration: %w", err)
	}

	stale, newFiles, deleted, err := idx.service.ValidateCollection(ctx, workspacePath, idx.resolverFor(workspacePath))
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

	// progressStart counts the deletions already processed above;
	// countSkipped makes files that fail preparation still advance progress,
	// matching the former serial loop's semantics (the incremental
	// denominator always reaches totalChanges).
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
// and re-indexing as needed.
func (idx *Indexer) HandleBranchSwitch(ctx context.Context, workspacePath, newBranch string) error {
	// MarkNotReady captures the gen so RestoreReady (called by the delegated
	// IndexFull/IndexIncremental's own defer) only restores readiness if no
	// SetReady(false) has intervened. SwitchBranch failure returns before the
	// delegation, so we also need to restore here.
	readyGen := idx.service.MarkNotReady()
	defer idx.service.RestoreReady(readyGen)

	if err := idx.service.SwitchBranch(ctx, newBranch); err != nil {
		return fmt.Errorf("switching branch to %q: %w", newBranch, err)
	}

	idx.logger.Info("branch switched, checking collection", "branch", newBranch)

	col := idx.service.GetCollection()
	if col == nil || col.Count() == 0 {
		idx.logger.Info("empty collection for branch, running full index", "branch", newBranch)
		return idx.IndexFull(ctx, workspacePath)
	}

	return idx.IndexIncremental(ctx, workspacePath)
}

// processFile reads a file, computes its hash, chunks it, and returns
// parallel chromem and lexical document slices keyed by the same shared
// document ID.
func (idx *Indexer) processFile(filePath string) ([]chromem.Document, []lexical.Doc, error) {
	// Size guard (stat once; reused below for lastModified): reject oversized
	// files before any full read. isBinaryHeader's NUL heuristic cannot catch
	// every binary format — ONNX/safetensors protobufs begin with readable
	// ASCII and contain no NUL in their header — so without this guard a
	// multi-hundred-MB model file would be loaded whole by os.ReadFile and
	// explode memory during chunking. The limit (idx.maxFileSize) is
	// configurable via vector_index.max_file_size.
	//
	// This read-time check is the universal backstop: walkProjectFiles and
	// ValidateCollection also apply the same limit as an optimization (so
	// oversized files never enter the pipeline), but processFile may be
	// reached via paths that bypass the walk — e.g. a debounced incremental
	// reindex triggered by a watcher event on a file that grew past the limit.
	// This check guarantees no oversized file is ever fully read regardless of
	// the caller.
	info, statErr := os.Stat(filePath)
	if statErr != nil {
		return nil, nil, fmt.Errorf("stat file %s: %w", filePath, statErr)
	}
	if tooLargeForIndex(info.Size(), idx.maxFileSize) {
		idx.logger.Debug("skipping oversized file", "path", filePath, "size", info.Size(),
			"limit", idx.maxFileSize)
		return nil, nil, nil
	}

	// Bounded header pre-read for binary detection: reject multi-GB binary
	// assets (e.g. .onnx, .safetensors, .psd) before loading them fully into
	// memory. isBinaryHeader reads at most binaryHeaderSize bytes.
	binary, bErr := isBinaryHeader(filePath)
	if bErr != nil {
		return nil, nil, fmt.Errorf("reading file %s: %w", filePath, bErr)
	}
	if binary {
		return nil, nil, nil
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading file %s: %w", filePath, err)
	}

	if len(content) == 0 {
		return nil, nil, nil
	}

	hash := idx.hashFn(content)

	chunks, err := idx.chunkFn(filePath, content, idx.maxChunkSize, idx.overlap)
	if err != nil {
		return nil, nil, fmt.Errorf("chunking file %s: %w", filePath, err)
	}

	// Chunk-count guard: a pathological chunker result — most often a
	// data-format file (BPE vocab/merges JSON, lockfiles, minified assets)
	// that the structure-aware splitter fragments into tens of thousands of
	// tiny chunks — would otherwise be handed to AddDocuments and hang/OOM
	// the embedder (30k ONNX passes). The cap turns such a file into a clean
	// skip logged at WARN, leaving the rest of the index pass intact. It is
	// also a second line of defense against future chunker regressions.
	// Embedding itself is sub-batched (embeddingSubBatchSize), so the cap's
	// job is to catch the pathological case, not to bound every per-call
	// batch; the default (4000) sits above any legitimate source file.
	if len(chunks) > idx.maxChunksPerFile {
		idx.logger.Warn("skipping file: chunk count exceeds per-file cap",
			"path", filePath, "chunks", len(chunks), "cap", idx.maxChunksPerFile)
		return nil, nil, nil
	}

	fileName := filepath.Base(filePath)
	// info is guaranteed non-nil here: processFile returned early above if the
	// opening os.Stat failed, so lastModified is always populated.
	lastModified := info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
	// file_size + file_mtime_unix_nano feed the file-hash sidecar's
	// "hash|size|mtimeUnixNano" entries so ValidateCollection can classify
	// the file as unchanged from stat alone (no ReadFile + SHA-256) on
	// incremental passes. Recorded from the SAME stat that guarded the read,
	// so the entry always describes the exact bytes that were hashed.
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
// never touches the service or other shared state, and the default chunk/hash
// functions are pure functions over the file's bytes — so files are prepared
// concurrently while the single consumer embeds earlier batches. Workers pull
// paths from an unbuffered input channel, which bounds in-flight preparation
// to at most prepWorkers files.
//
// The returned stream is closed after every path has been prepared, skipped,
// or dropped due to cancellation. Every send and the feeder select on
// ctx.Done(), so a consumer that returns early (after cancelling ctx) never
// deadlocks the pool: the feeder stops feeding, workers abandon in-flight
// sends, and all goroutines exit.
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
				vecDocs, lexDocs, err := idx.processFile(path)
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

// indexPrepared runs one indexing pass over paths: a bounded pool of
// idx.prepWorkers goroutines prepares files (read/hash/chunk) while this
// single consumer accumulates the resulting documents and flushes them into
// Service.AddDocuments in batches of addDocumentBatchSize. Embedding stays
// single-threaded — only this goroutine calls AddDocuments — and the caller
// must already hold the service write lock, exactly as the former serial
// loops did. PrepWorkers=1 reproduces the serial behavior: one file prepared
// at a time, in input order.
//
// It returns the number of files counted toward pass progress. Per-file prep
// failures are logged and skipped; ctx cancellation aborts the pass with an
// error wrapping ctx.Err(); an AddDocuments failure aborts the pass.
func (idx *Indexer) indexPrepared(ctx context.Context, paths []string, o indexPipelineOpts) (int, error) {
	// Derive a cancellable context so the prep pool is released promptly when
	// this consumer returns early (cancellation or a batch-add failure):
	// the feeder and every worker send select on ctx.Done.
	poolCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	units := idx.streamPreparedFiles(poolCtx, paths, o.skipLogLabel)

	progress := o.progressStart
	var vecBatch []chromem.Document
	var lexBatch []lexical.Doc

	for unit := range units {
		if err := ctx.Err(); err != nil {
			return progress, fmt.Errorf("%s: %w", o.cancelMsg, err)
		}
		if unit.skipped && !o.countSkipped {
			continue
		}
		if !unit.skipped {
			vecBatch = append(vecBatch, unit.vecDocs...)
			lexBatch = append(lexBatch, unit.lexDocs...)
			if len(vecBatch) >= addDocumentBatchSize {
				idx.logger.Debug("embedding document batch"+o.batchLogLabel,
					"batchSize", len(vecBatch), "progress", progress, "total", o.total)
				if addErr := idx.service.AddDocuments(ctx, vecBatch, lexBatch); addErr != nil {
					return progress, fmt.Errorf("adding document batch: %w", addErr)
				}
				idx.logger.Debug("batch embedded successfully")
				vecBatch = vecBatch[:0]
				lexBatch = lexBatch[:0]
			}
		}
		progress++
		idx.onProgress(PhaseBoth, o.state, progress, o.total, unit.path)
	}
	// The stream ended. When ctx was cancelled mid-pass the workers drop
	// their remaining paths without emitting units, so the loop above can
	// exit through range-end instead of the per-unit check — surface the
	// cancellation here so a cancelled pass ALWAYS reports an error.
	if err := ctx.Err(); err != nil {
		return progress, fmt.Errorf("%s: %w", o.cancelMsg, err)
	}

	// Flush remaining documents.
	if len(vecBatch) > 0 || len(lexBatch) > 0 {
		idx.logger.Debug("embedding final document batch"+o.batchLogLabel,
			"batchSize", len(vecBatch), "progress", progress, "total", o.total)
		if addErr := idx.service.AddDocuments(ctx, vecBatch, lexBatch); addErr != nil {
			return progress, fmt.Errorf("adding final document batch: %w", addErr)
		}
		idx.logger.Debug("final batch embedded successfully")
	}
	return progress, nil
}

// RebuildLexical enumerates the current chromem collection and rebuilds the
// per-branch lexical index from scratch. It is used as a one-time backfill
// when a project that was indexed before the BM25 upgrade is opened.
//
// The caller must not hold s.mu; this method acquires the write lock
// internally to prevent concurrent mutation of the lexical index.
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

	idx.service.AcquireWriteLock()
	defer idx.service.ReleaseWriteLock()

	idx.onProgress(PhaseLexical, IndexStateIndexing, 0, count, "")
	idx.logger.Info("starting lexical backfill", "chunks", count)

	// chromem has no ListAll API; a single-space query returns all docs
	// ranked by similarity (the ranking is irrelevant here — we only need
	// to enumerate every document for the lexical backfill).
	results, err := col.Query(ctx, " ", count, nil, nil)
	if err != nil {
		return fmt.Errorf("enumerating collection for lexical rebuild: %w", err)
	}

	batch := make([]lexical.Doc, 0, addDocumentBatchSize)
	processed := 0
	for _, r := range results {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("lexical rebuild cancelled: %w", err)
		}
		batch = append(batch, lexical.Doc{
			ID:       r.ID,
			FilePath: r.Metadata["file_path"],
			Language: r.Metadata["language"],
			Content:  r.Content,
		})
		if len(batch) >= addDocumentBatchSize {
			if upErr := lex.Upsert(ctx, batch); upErr != nil {
				return fmt.Errorf("lexical upsert batch: %w", upErr)
			}
			batch = batch[:0]
		}
		processed++
		idx.onProgress(PhaseLexical, IndexStateIndexing, processed, count, r.Metadata["file_path"])
	}
	if len(batch) > 0 {
		if upErr := lex.Upsert(ctx, batch); upErr != nil {
			return fmt.Errorf("lexical final upsert: %w", upErr)
		}
	}

	idx.onProgress(PhaseLexical, IndexStateReady, count, count, "")
	idx.logger.Info("lexical backfill complete", "chunks", processed)
	return nil
}

// collectDocumentIDs enumerates document IDs for the given file paths
// by querying the collection's stored file info.
func (idx *Indexer) collectDocumentIDs(ctx context.Context, filePaths []string) ([]string, error) {
	col := idx.service.GetCollection()
	if col == nil {
		return nil, nil
	}

	count := col.Count()
	if count == 0 {
		return nil, nil
	}

	results, err := col.Query(ctx, " ", count, nil, nil)
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
// and the watcher filter share one source of truth. Files and directories
// ignored by .gitignore or .aiignore (root and nested, resolved via checker)
// are skipped, as are hidden (leading-dot) entries. Binary content is not
// detected here — it is a read-time guard applied in processFile.
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
		// (and never appear as "new" on every incremental pass, which would
		// otherwise cause a redundant reindex). This is an optimization: the
		// universal guarantee — that no file above the limit is ever fully
		// read — is enforced by processFile's read-time backstop. A stat
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

// noopIgnoreChecker is an IgnoreChecker that ignores nothing. It is the
// fail-open fallback when an ignore.Resolver cannot be built for a root (e.g.
// the root vanished mid-index): rather than indexing nothing, the indexer
// proceeds and applies only the universal hidden-dot guard.
type noopIgnoreChecker struct{}

func (noopIgnoreChecker) Ignored(string, bool) bool { return false }

// buildIgnoreChecker constructs an ignore resolver over .gitignore + .aiignore
// (root and nested) for root. On failure it returns a fail-open
// noopIgnoreChecker together with the error so callers can log once without
// re-attempting on every debounce flush.
func buildIgnoreChecker(root string) (ignore.IgnoreChecker, error) {
	r, err := ignore.NewResolver(root)
	if err != nil {
		return noopIgnoreChecker{}, fmt.Errorf("building ignore resolver for %q: %w", root, err)
	}
	return r, nil
}

// resolverFor returns the cached ignore resolver for root, building and caching
// it on first use. The Indexer (and thus the cache) is recreated on every
// SwitchProject, so the workspace is walked at most once per active project
// instead of once per watcher debounce flush. A mid-session edit of .gitignore
// or .aiignore is not picked up until the next source-driven index pass
// triggers a cache miss — acceptable, since a stale filter only risks a
// harmless no-op reindex that the walker then drops.
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
// under root, reusing the Indexer's cached ignore resolver. It is the
// watcher-facing check and avoids re-walking the workspace on every debounce
// flush.
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
// resolver. It avoids os.Stat by inspecting path segments directly, so it
// works for both file and directory change events. A path is not indexable
// when any segment is hidden (leading dot) or when the resolver reports the
// path (or an ancestor directory) as ignored — the resolver walks ancestor
// directories with directory semantics, so "once a directory is ignored, so
// are its contents" holds in a single call.
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

// isHiddenName reports whether name is a hidden entry — one whose base name
// begins with a dot. This is the universal guard layered on top of the ignore
// resolver: hidden directories (e.g. .git, .cache, .idea) and hidden files
// (e.g. .DS_Store) are never indexed regardless of ignore-file contents.
func isHiddenName(name string) bool {
	return strings.HasPrefix(name, ".")
}

// binaryHeaderSize is the number of leading bytes inspected for binary
// detection. It matches git's heuristic and is small enough that reading it
// from a multi-GB file is effectively free.
const binaryHeaderSize = 512

// DefaultMaxIndexableFileSize is the default upper bound on a file's size (in
// bytes) for it to be read fully into memory for chunking and embedding. Files
// larger than this are skipped at walk, validation, and read time. The limit is
// necessary because the NUL-byte binary check (isBinaryHeader) cannot recognize
// every binary format: ONNX and safetensors models are protobuf whose leading
// bytes are readable ASCII field names with no NUL, so a multi-hundred-MB
// model.onnx would otherwise be loaded whole by os.ReadFile and explode memory
// during chunking (a 522 MB model yields ~400k chunks). 4 MiB is generous for
// genuine source code while excluding the large binary assets that cause the
// hang; it is overridable via vector_index.max_file_size.
const DefaultMaxIndexableFileSize int64 = 4 * 1024 * 1024 // 4 MiB

// DefaultMaxChunkSize is the default maximum chunk size in characters passed
// to the chunker. It is sized to fit within the embedding model's context
// window (MaxSeqLength 512 tokens ≈ ~2000 chars) with room for overlap.
// Overridable via vector_index.max_chunk_size.
const DefaultMaxChunkSize = 1500

// DefaultMaxChunksPerFile is the default upper bound on the number of chunks a
// single file may contribute to one index pass. A file that exceeds this is
// skipped wholesale, because its chunks are accumulated into AddDocuments and
// a runaway count (tens of thousands from a data-format file that the
// structure-aware splitter fragments per-entry — e.g. a HuggingFace BPE
// vocab/merges tokenizer.json) hangs/OOMs the embedder. Embedding is itself
// sub-batched (embeddingSubBatchSize), so the per-call work is already
// bounded; this cap is a backstop that turns a pathological file into a clean
// skip rather than a tight per-call limit. The default (4000) sits above the
// worst legitimate source file — a 4 MiB file chunked at 1500 chars yields
// ~3230 chunks — so no real source file is dropped, while the 30k-chunk data
// files that actually cause the hang are still caught.
const DefaultMaxChunksPerFile = 4000

// DefaultChunkOverlap is the default character overlap between adjacent
// chunks handed to the chunker. The historical hardcoded value (200); now the
// config-layer default for vector_index.chunk_overlap. Kept in the vectorindex
// package (like the other chunk defaults) so backend/config can reference it
// without duplicating the number.
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

// isBinaryHeader reports whether the file at filePath begins with binary
// content (a NUL byte within the first binaryHeaderSize bytes). It reads at
// most binaryHeaderSize bytes and never loads the whole file, so it is safe to
// call on very large files — this is the walk/read-time guard that prevents
// multi-GB binary assets (e.g. .onnx, .safetensors, .psd) from being fully
// read into memory before rejection. An open error is returned to the caller
// so it can decide whether to skip or fail; short reads (fewer bytes than
// requested) are normal and are scanned over whatever was read.
func isBinaryHeader(filePath string) (bool, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, binaryHeaderSize)
	// io.ReadFull returns io.EOF only when zero bytes were read and
	// io.ErrUnexpectedEOF when fewer than len(header) bytes were read — both
	// are expected here; n holds the bytes actually read, which is all we scan.
	n, _ := io.ReadFull(f, header)
	return containsNullByte(header[:n]), nil
}
