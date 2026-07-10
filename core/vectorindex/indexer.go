package vectorindex

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
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

	// Ignore patterns for file filtering (merged with defaults).
	IgnoreDirs       map[string]bool
	IgnoreExtensions map[string]bool
	IgnoreFileNames  map[string]bool
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

	ignoreDirs       map[string]bool
	ignoreExtensions map[string]bool
	ignoreFileNames  map[string]bool

	// gitignoreMu guards gitignoreRoot / gitignorePatterns, a cache of the
	// workspace's .gitignore patterns loaded once (during the first walk or
	// watcher filter) and reused thereafter, so the watcher does not re-read
	// .gitignore on every debounce flush. The cache is per-project: the
	// Indexer is recreated on SwitchProject.
	gitignoreMu       sync.RWMutex
	gitignoreRoot     string
	gitignorePatterns []string
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
		service:          cfg.Service,
		chunkFn:          cfg.ChunkFn,
		hashFn:           hashFn,
		maxChunkSize:     maxChunkSize,
		overlap:          overlap,
		onProgress:       onProgress,
		logger:           logger,
		ignoreDirs:       cfg.IgnoreDirs,
		ignoreExtensions: cfg.IgnoreExtensions,
		ignoreFileNames:  cfg.IgnoreFileNames,
	}
}

// IndexFull performs a full workspace indexing: walks all files, chunks them,
// and adds the resulting documents to both the vector collection and the
// lexical index.
func (idx *Indexer) IndexFull(ctx context.Context, workspacePath string) error {
	idx.service.SetReady(false)

	files, err := walkProjectFiles(workspacePath, idx.gitignorePatternsFor(workspacePath), idx.ignoreDirs, idx.ignoreExtensions, idx.ignoreFileNames)
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

	stale, newFiles, deleted, err := idx.service.ValidateCollection(ctx, workspacePath)
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
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading file %s: %w", filePath, err)
	}

	if isBinaryFile(content) {
		return nil, nil, nil
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
// gitignorePatterns must be pre-loaded by the caller (the Indexer caches them)
// so the walk and the watcher filter share one source of truth.
func walkProjectFiles(root string, gitignorePatterns []string, extraIgnoreDirs, extraIgnoreExtensions, extraIgnoreFileNames map[string]bool) ([]string, error) {
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
			if isIgnoredDir(path, absRoot, gitignorePatterns, extraIgnoreDirs) {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.Type().IsRegular() {
			return nil
		}

		if isIgnoredFile(path, absRoot, gitignorePatterns, extraIgnoreExtensions, extraIgnoreFileNames) {
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

// defaultIgnoreDirs are directory names that should always be skipped.
var defaultIgnoreDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"build":        true,
	"dist":         true,
	".cache":       true,
	"__pycache__":  true,
	".idea":        true,
	".vscode":      true,
	".next":        true,
	".nuxt":        true,
	"target":       true,
	"coverage":     true,
	".terraform":   true,
	".svn":         true,
	".hg":          true,
}

// defaultIgnoreExtensions are file extensions that should always be skipped.
var defaultIgnoreExtensions = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".bin": true, ".o": true, ".a": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".ico": true, ".svg": true, ".webp": true, ".bmp": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	".mp3": true, ".mp4": true, ".wav": true, ".avi": true,
	".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".xz": true, ".7z": true, ".rar": true,
	".lock": true, ".pyc": true, ".pyo": true, ".class": true,
	".db": true, ".sqlite": true,
}

// defaultIgnoreFileNames are specific file names that should be skipped.
var defaultIgnoreFileNames = map[string]bool{
	"package-lock.json": true,
	"go.sum":            true,
	".DS_Store":         true,
}

// isIgnoredDir checks if a directory should be skipped during file walking.
func isIgnoredDir(path, root string, gitignorePatterns []string, extraIgnoreDirs map[string]bool) bool {
	base := filepath.Base(path)

	// Always skip hidden directories (starting with .)
	if strings.HasPrefix(base, ".") {
		return true
	}

	if defaultIgnoreDirs[base] {
		return true
	}

	if extraIgnoreDirs != nil && extraIgnoreDirs[base] {
		return true
	}

	// Check gitignore patterns.
	relPath, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	relPath = filepath.ToSlash(relPath) + "/"
	for _, pattern := range gitignorePatterns {
		if matched, _ := doublestar.Match(pattern, relPath); matched {
			return true
		}
		// Also try matching without trailing slash.
		if matched, _ := doublestar.Match(pattern, strings.TrimSuffix(relPath, "/")); matched {
			return true
		}
	}

	return false
}

// isIgnoredFile checks if a file should be skipped during file walking.
func isIgnoredFile(path, root string, gitignorePatterns []string, extraIgnoreExtensions, extraIgnoreFileNames map[string]bool) bool {
	base := filepath.Base(path)

	if strings.HasPrefix(base, ".") {
		return true
	}

	if defaultIgnoreFileNames[base] {
		return true
	}

	if extraIgnoreFileNames != nil && extraIgnoreFileNames[base] {
		return true
	}

	ext := strings.ToLower(filepath.Ext(base))
	if defaultIgnoreExtensions[ext] {
		return true
	}

	if extraIgnoreExtensions != nil && extraIgnoreExtensions[ext] {
		return true
	}

	relPath, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	relPath = filepath.ToSlash(relPath)
	for _, pattern := range gitignorePatterns {
		if matched, _ := doublestar.Match(pattern, relPath); matched {
			return true
		}
	}

	return false
}

// IsIndexablePath reports whether absPath (which must be under root) refers to
// a file or directory the indexer would process. It returns false if any
// ancestor directory is ignored (e.g. .git, node_modules, hidden dirs) or if
// the path itself matches ignore-file rules (e.g. .DS_Store, binary
// extensions). It applies the same hardcoded defaults as walkProjectFiles.
//
// It is intended for the workspace watcher to decide whether a file change
// warrants vector re-indexing, so that churn inside ignored locations (most
// notably .git maintenance: gc, repack, reflog cleanup) does not trigger a
// spurious — and costly (ONNX inference) — reindex pass that always ends with
// "no changes detected". User-configured extra ignore patterns are NOT applied
// here (they live on the Indexer); a change in an extra-ignored location would
// at worst trigger a harmless no-op reindex.
func IsIndexablePath(absPath, root string) bool {
	return isIndexablePathWithPatterns(absPath, root, loadGitignorePatterns(root))
}

// IsAnyIndexablePath reports whether at least one of paths is indexable under
// root. It loads .gitignore patterns once for the whole batch. An empty/nil
// path list reports false (nothing indexable).
func IsAnyIndexablePath(paths []string, root string) bool {
	patterns := loadGitignorePatterns(root)
	for _, p := range paths {
		if isIndexablePathWithPatterns(p, root, patterns) {
			return true
		}
	}
	return false
}

// gitignorePatternsFor returns the cached .gitignore patterns for root, loading
// and caching them on first use. The Indexer (and thus the cache) is recreated
// on every SwitchProject, so patterns are re-read at most once per active
// project instead of once per watcher debounce flush. A mid-session edit of
// .gitignore is not picked up until the next source-driven index pass triggers
// a cache miss — acceptable, since a stale filter only risks a harmless no-op
// reindex that the walker then drops.
func (idx *Indexer) gitignorePatternsFor(root string) []string {
	idx.gitignoreMu.RLock()
	if idx.gitignoreRoot == root && idx.gitignorePatterns != nil {
		p := idx.gitignorePatterns
		idx.gitignoreMu.RUnlock()
		return p
	}
	idx.gitignoreMu.RUnlock()

	patterns := loadGitignorePatterns(root)
	idx.gitignoreMu.Lock()
	idx.gitignoreRoot = root
	idx.gitignorePatterns = patterns
	idx.gitignoreMu.Unlock()
	return patterns
}

// IsAnyIndexablePath reports whether at least one of changedPaths is indexable
// under root, reusing the Indexer's cached .gitignore patterns. It is the
// watcher-facing variant of the package-level IsAnyIndexablePath and avoids
// re-reading .gitignore on every debounce flush.
func (idx *Indexer) IsAnyIndexablePath(changedPaths []string, root string) bool {
	patterns := idx.gitignorePatternsFor(root)
	for _, p := range changedPaths {
		if isIndexablePathWithPatterns(p, root, patterns) {
			return true
		}
	}
	return false
}

// isIndexablePathWithPatterns is the core single-path check with pre-loaded
// gitignore patterns. It avoids os.Stat by inspecting path segments directly,
// so it works for both file and directory change events.
func isIndexablePathWithPatterns(absPath, root string, gitignorePatterns []string) bool {
	relPath, err := filepath.Rel(root, absPath)
	if err != nil {
		return false
	}
	relPath = filepath.ToSlash(relPath)
	if relPath == "." || strings.HasPrefix(relPath, "..") {
		// The root itself, or outside it: not a concrete indexable file.
		return false
	}

	segments := strings.Split(relPath, "/")
	// Check directory components (every segment except the last).
	dirRel := ""
	for i := 0; i < len(segments)-1; i++ {
		seg := segments[i]
		if isIgnoredDirSegment(seg) {
			return false
		}
		// Build the cumulative directory relative path and apply gitignore
		// patterns (mirroring isIgnoredDir: match both "dir/" and "dir") so a
		// directory excluded only via .gitignore (e.g. "coverage_report/") is
		// filtered here too — otherwise the watcher would fire for churn under
		// it and the walker would silently drop it, leaving a confusing gap.
		if dirRel == "" {
			dirRel = seg
		} else {
			dirRel += "/" + seg
		}
		if isGitIgnoredDir(dirRel, gitignorePatterns) {
			return false
		}
	}

	// Check the last segment (the changed file/dir itself).
	last := segments[len(segments)-1]
	if last == "" || strings.HasPrefix(last, ".") {
		return false // hidden file/dir
	}
	if defaultIgnoreDirs[last] || defaultIgnoreFileNames[last] {
		return false
	}
	if ext := strings.ToLower(filepath.Ext(last)); defaultIgnoreExtensions[ext] {
		return false
	}

	// gitignore pattern matching against the full relative path.
	for _, pattern := range gitignorePatterns {
		if matched, _ := doublestar.Match(pattern, relPath); matched {
			return false
		}
	}
	return true
}

// isIgnoredDirSegment reports whether a single path segment (a directory base
// name) is ignored by default rules: hidden dirs (leading dot) or a member of
// defaultIgnoreDirs.
func isIgnoredDirSegment(seg string) bool {
	if seg == "" {
		return false
	}
	if strings.HasPrefix(seg, ".") {
		return true
	}
	return defaultIgnoreDirs[seg]
}

// isGitIgnoredDir reports whether a directory — given as a slash-relative path
// without a trailing slash — is excluded by any gitignore pattern. It mirrors
// isIgnoredDir by matching both "dir/" and "dir", so directory-only patterns
// such as "build/" or "coverage/" are honoured by the watcher filter exactly as
// they are by the walker.
func isGitIgnoredDir(relDirPath string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	withSlash := relDirPath + "/"
	for _, pattern := range patterns {
		if matched, _ := doublestar.Match(pattern, withSlash); matched {
			return true
		}
		if matched, _ := doublestar.Match(pattern, relDirPath); matched {
			return true
		}
	}
	return false
}

// isBinaryFile checks the first 512 bytes for null bytes, indicating a binary file.
func isBinaryFile(content []byte) bool {
	checkLen := len(content)
	if checkLen > 512 {
		checkLen = 512
	}
	for i := 0; i < checkLen; i++ {
		if content[i] == 0 {
			return true
		}
	}
	return false
}

// loadGitignorePatterns reads a .gitignore file from root and returns
// doublestar-compatible patterns.
func loadGitignorePatterns(root string) []string {
	gitignorePath := filepath.Join(root, ".gitignore")
	f, err := os.Open(gitignorePath)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var patterns []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Negation patterns are not supported in this simple implementation.
		if strings.HasPrefix(line, "!") {
			continue
		}
		// Strip trailing spaces (not preceded by backslash).
		line = strings.TrimRight(line, " ")
		// Normalize the pattern for doublestar matching.
		pattern := normalizeGitignorePattern(line)
		patterns = append(patterns, pattern)
	}
	return patterns
}

// normalizeGitignorePattern converts a .gitignore pattern to a
// doublestar-compatible glob pattern.
func normalizeGitignorePattern(pattern string) string {
	// Remove leading slash (anchored to root).
	pattern = strings.TrimPrefix(pattern, "/")

	// Remove trailing slash (directory indicator) — we handle dirs separately.
	pattern = strings.TrimSuffix(pattern, "/")

	// If pattern doesn't contain a slash, it matches anywhere in the tree.
	if !strings.Contains(pattern, "/") {
		return "**/" + pattern
	}

	return pattern
}
