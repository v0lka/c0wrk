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
	Service      *Service
	ChunkFn      ChunkFunc
	HashFn       HashFunc
	MaxChunkSize int
	Overlap      int
	OnProgress   ProgressCallback
	Logger       *slog.Logger
}

// Indexer orchestrates initial and incremental indexing of project files.
type Indexer struct {
	service      *Service
	chunkFn      ChunkFunc
	hashFn       HashFunc
	maxChunkSize int
	overlap      int
	onProgress   ProgressCallback
	logger       *slog.Logger

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
		maxChunkSize = 1500
	}
	overlap := cfg.Overlap
	if overlap <= 0 {
		overlap = 200
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
		service:      cfg.Service,
		chunkFn:      cfg.ChunkFn,
		hashFn:       hashFn,
		maxChunkSize: maxChunkSize,
		overlap:      overlap,
		onProgress:   onProgress,
		logger:       logger,
	}
}

// IndexFull performs a full workspace indexing: walks all files, chunks them,
// and adds the resulting documents to both the vector collection and the
// lexical index.
func (idx *Indexer) IndexFull(ctx context.Context, workspacePath string) error {
	idx.service.SetReady(false)

	files, err := walkProjectFiles(workspacePath, idx.resolverFor(workspacePath))
	if err != nil {
		return fmt.Errorf("walking project files: %w", err)
	}

	totalFiles := len(files)
	idx.onProgress(PhaseBoth, IndexStateIndexing, 0, totalFiles, "")
	idx.logger.Info("starting full index", "workspace", workspacePath, "files", totalFiles)

	idx.service.AcquireWriteLock()
	defer idx.service.ReleaseWriteLock()

	var vecBatch []chromem.Document
	var lexBatch []lexical.Doc
	indexed := 0

	for _, filePath := range files {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("indexing cancelled: %w", err)
		}

		vecDocs, lexDocs, chunkErr := idx.processFile(filePath)
		if chunkErr != nil {
			idx.logger.Warn("skipping file", "path", filePath, "error", chunkErr)
			continue
		}
		vecBatch = append(vecBatch, vecDocs...)
		lexBatch = append(lexBatch, lexDocs...)

		if len(vecBatch) >= addDocumentBatchSize {
			idx.logger.Debug("embedding document batch", "batchSize", len(vecBatch), "indexed", indexed, "total", totalFiles)
			if addErr := idx.service.AddDocuments(ctx, vecBatch, lexBatch); addErr != nil {
				return fmt.Errorf("adding document batch: %w", addErr)
			}
			idx.logger.Debug("batch embedded successfully")
			vecBatch = vecBatch[:0]
			lexBatch = lexBatch[:0]
		}

		indexed++
		idx.onProgress(PhaseBoth, IndexStateIndexing, indexed, totalFiles, filePath)
	}

	// Flush remaining documents.
	if len(vecBatch) > 0 || len(lexBatch) > 0 {
		idx.logger.Debug("embedding final document batch", "batchSize", len(vecBatch), "indexed", indexed, "total", totalFiles)
		if addErr := idx.service.AddDocuments(ctx, vecBatch, lexBatch); addErr != nil {
			return fmt.Errorf("adding final document batch: %w", addErr)
		}
		idx.logger.Debug("final batch embedded successfully")
	}

	idx.service.SetReady(true)
	idx.onProgress(PhaseBoth, IndexStateReady, totalFiles, totalFiles, "")
	idx.logger.Info("full index complete", "files", indexed)
	return nil
}

// IndexIncremental performs an incremental re-index: validates the current
// collection against disk, then updates only changed/new/deleted files
// across both the vector collection and the lexical index.
func (idx *Indexer) IndexIncremental(ctx context.Context, workspacePath string) error {
	idx.service.SetReady(false)

	// Wait for any background file-hash migration (first run after the sidecar
	// upgrade, or a branch whose collection was built elsewhere) so
	// ValidateCollection sees the full hash map instead of re-embedding every
	// file. IndexFull is unaffected: an empty collection has nothing to migrate.
	if err := idx.service.WaitFileHashMigration(ctx); err != nil {
		idx.service.SetReady(true)
		return fmt.Errorf("waiting for file-hash migration: %w", err)
	}

	stale, newFiles, deleted, err := idx.service.ValidateCollection(ctx, workspacePath, idx.resolverFor(workspacePath))
	if err != nil {
		idx.service.SetReady(true)
		return fmt.Errorf("validating collection: %w", err)
	}

	totalChanges := len(stale) + len(newFiles) + len(deleted)
	if totalChanges == 0 {
		idx.service.SetReady(true)
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

	var vecBatch []chromem.Document
	var lexBatch []lexical.Doc
	progress := len(deleted)

	for _, filePath := range filesToIndex {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("incremental indexing cancelled: %w", err)
		}

		idx.logger.Debug("processing file", "path", filePath, "progress", progress+1, "total", totalChanges)

		vecDocs, lexDocs, chunkErr := idx.processFile(filePath)
		if chunkErr != nil {
			idx.logger.Warn("skipping file during incremental index", "path", filePath, "error", chunkErr)
			progress++
			idx.onProgress(PhaseBoth, IndexStateReindexing, progress, totalChanges, filePath)
			continue
		}
		vecBatch = append(vecBatch, vecDocs...)
		lexBatch = append(lexBatch, lexDocs...)

		if len(vecBatch) >= addDocumentBatchSize {
			idx.logger.Debug("embedding document batch (incremental)", "batchSize", len(vecBatch), "progress", progress, "total", totalChanges)
			if addErr := idx.service.AddDocuments(ctx, vecBatch, lexBatch); addErr != nil {
				return fmt.Errorf("adding document batch: %w", addErr)
			}
			idx.logger.Debug("batch embedded successfully")
			vecBatch = vecBatch[:0]
			lexBatch = lexBatch[:0]
		}

		progress++
		idx.onProgress(PhaseBoth, IndexStateReindexing, progress, totalChanges, filePath)
	}

	if len(vecBatch) > 0 || len(lexBatch) > 0 {
		idx.logger.Debug("embedding final document batch (incremental)", "batchSize", len(vecBatch), "progress", progress, "total", totalChanges)
		if addErr := idx.service.AddDocuments(ctx, vecBatch, lexBatch); addErr != nil {
			return fmt.Errorf("adding final document batch: %w", addErr)
		}
		idx.logger.Debug("final batch embedded successfully")
	}

	idx.service.SetReady(true)
	totalInIndex := idx.service.collectionUniqueFileCount()
	idx.onProgress(PhaseBoth, IndexStateReady, totalChanges, totalInIndex, "")
	idx.logger.Info("incremental index complete", "changes", totalChanges, "totalFiles", totalInIndex)
	return nil
}

// HandleBranchSwitch handles a git branch change by switching the collection
// and re-indexing as needed.
func (idx *Indexer) HandleBranchSwitch(ctx context.Context, workspacePath, newBranch string) error {
	idx.service.SetReady(false)

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

	fileName := filepath.Base(filePath)
	info, statErr := os.Stat(filePath)
	lastModified := ""
	if statErr == nil {
		lastModified = info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
	}

	vecDocs := make([]chromem.Document, 0, len(chunks))
	lexDocs := make([]lexical.Doc, 0, len(chunks))
	for i, chunk := range chunks {
		docID := DocumentID(filePath, i)
		vecDocs = append(vecDocs, chromem.Document{
			ID:      docID,
			Content: chunk.Content,
			Metadata: map[string]string{
				"file_path":     filePath,
				"file_name":     fileName,
				"last_modified": lastModified,
				"content_hash":  hash,
				"start_line":    strconv.Itoa(chunk.StartLine),
				"end_line":      strconv.Itoa(chunk.EndLine),
				"language":      chunk.Language,
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
func walkProjectFiles(root string, checker ignore.IgnoreChecker) ([]string, error) {
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
