package vectorindex

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	chromem "github.com/philippgille/chromem-go"
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

// addDocumentBatchSize is the number of documents added per batch call.
const addDocumentBatchSize = 50

// ProgressCallback is called to report indexing progress.
type ProgressCallback func(state IndexState, filesIndexed, totalFiles int, currentFile string)

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
		onProgress = func(IndexState, int, int, string) {}
	}
	return &Indexer{
		service:      cfg.Service,
		chunkFn:      cfg.ChunkFn,
		hashFn:       hashFn,
		maxChunkSize: maxChunkSize,
		overlap:      overlap,
		onProgress:   onProgress,
		logger:       logger,
		ignoreDirs:       cfg.IgnoreDirs,
		ignoreExtensions: cfg.IgnoreExtensions,
		ignoreFileNames:  cfg.IgnoreFileNames,
	}
}

// IndexFull performs a full workspace indexing: walks all files, chunks them,
// and adds the resulting documents to the collection.
func (idx *Indexer) IndexFull(ctx context.Context, workspacePath string) error {
	idx.service.SetReady(false)

	files, err := walkProjectFiles(workspacePath, idx.ignoreDirs, idx.ignoreExtensions, idx.ignoreFileNames)
	if err != nil {
		return fmt.Errorf("walking project files: %w", err)
	}

	totalFiles := len(files)
	idx.onProgress(IndexStateIndexing, 0, totalFiles, "")
	idx.logger.Info("starting full index", "workspace", workspacePath, "files", totalFiles)

	idx.service.AcquireWriteLock()
	defer idx.service.ReleaseWriteLock()

	var batch []chromem.Document
	indexed := 0

	for _, filePath := range files {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("indexing cancelled: %w", err)
		}

		docs, chunkErr := idx.processFile(filePath)
		if chunkErr != nil {
			idx.logger.Warn("skipping file", "path", filePath, "error", chunkErr)
			continue
		}
		batch = append(batch, docs...)

		if len(batch) >= addDocumentBatchSize {
			idx.logger.Debug("embedding document batch", "batchSize", len(batch), "indexed", indexed, "total", totalFiles)
			if addErr := idx.service.AddDocuments(ctx, batch); addErr != nil {
				return fmt.Errorf("adding document batch: %w", addErr)
			}
			idx.logger.Debug("batch embedded successfully")
			batch = batch[:0]
		}

		indexed++
		idx.onProgress(IndexStateIndexing, indexed, totalFiles, filePath)
	}

	// Flush remaining documents.
	if len(batch) > 0 {
		idx.logger.Debug("embedding final document batch", "batchSize", len(batch), "indexed", indexed, "total", totalFiles)
		if addErr := idx.service.AddDocuments(ctx, batch); addErr != nil {
			return fmt.Errorf("adding final document batch: %w", addErr)
		}
		idx.logger.Debug("final batch embedded successfully")
	}

	idx.service.SetReady(true)
	idx.onProgress(IndexStateReady, totalFiles, totalFiles, "")
	idx.logger.Info("full index complete", "files", indexed)
	return nil
}

// IndexIncremental performs an incremental re-index: validates the current
// collection against disk, then updates only changed/new/deleted files.
func (idx *Indexer) IndexIncremental(ctx context.Context, workspacePath string) error {
	idx.service.SetReady(false)

	stale, newFiles, deleted, err := idx.service.ValidateCollection(ctx, workspacePath)
	if err != nil {
		idx.service.SetReady(true)
		return fmt.Errorf("validating collection: %w", err)
	}

	totalChanges := len(stale) + len(newFiles) + len(deleted)
	if totalChanges == 0 {
		idx.service.SetReady(true)
		idx.logger.Info("incremental index: no changes detected")
		idx.onProgress(IndexStateReady, 0, 0, "")
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

	idx.onProgress(IndexStateReindexing, 0, totalChanges, "")

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
	}

	if len(filesToDelete) > 0 {
		idx.onProgress(IndexStateReindexing, len(deleted), totalChanges, "")
	}

	// Re-index stale + new files.
	filesToIndex := make([]string, 0, len(stale)+len(newFiles))
	filesToIndex = append(filesToIndex, stale...)
	filesToIndex = append(filesToIndex, newFiles...)

	var batch []chromem.Document
	progress := len(deleted)

	for _, filePath := range filesToIndex {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("incremental indexing cancelled: %w", err)
		}

		idx.logger.Debug("processing file", "path", filePath, "progress", progress+1, "total", totalChanges)

		docs, chunkErr := idx.processFile(filePath)
		if chunkErr != nil {
			idx.logger.Warn("skipping file during incremental index", "path", filePath, "error", chunkErr)
			progress++
			idx.onProgress(IndexStateReindexing, progress, totalChanges, filePath)
			continue
		}
		batch = append(batch, docs...)

		if len(batch) >= addDocumentBatchSize {
			idx.logger.Debug("embedding document batch (incremental)", "batchSize", len(batch), "progress", progress, "total", totalChanges)
			if addErr := idx.service.AddDocuments(ctx, batch); addErr != nil {
				return fmt.Errorf("adding document batch: %w", addErr)
			}
			idx.logger.Debug("batch embedded successfully")
			batch = batch[:0]
		}

		progress++
		idx.onProgress(IndexStateReindexing, progress, totalChanges, filePath)
	}

	if len(batch) > 0 {
		idx.logger.Debug("embedding final document batch (incremental)", "batchSize", len(batch), "progress", progress, "total", totalChanges)
		if addErr := idx.service.AddDocuments(ctx, batch); addErr != nil {
			return fmt.Errorf("adding final document batch: %w", addErr)
		}
		idx.logger.Debug("final batch embedded successfully")
	}

	idx.service.SetReady(true)
	totalInIndex := idx.service.collectionUniqueFileCount()
	idx.onProgress(IndexStateReady, totalChanges, totalInIndex, "")
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

// processFile reads a file, computes its hash, chunks it, and returns chromem documents.
func (idx *Indexer) processFile(filePath string) ([]chromem.Document, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading file %s: %w", filePath, err)
	}

	if isBinaryFile(content) {
		return nil, nil
	}

	if len(content) == 0 {
		return nil, nil
	}

	hash := idx.hashFn(content)

	chunks, err := idx.chunkFn(filePath, content, idx.maxChunkSize, idx.overlap)
	if err != nil {
		return nil, fmt.Errorf("chunking file %s: %w", filePath, err)
	}

	fileName := filepath.Base(filePath)
	info, statErr := os.Stat(filePath)
	lastModified := ""
	if statErr == nil {
		lastModified = info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
	}

	docs := make([]chromem.Document, 0, len(chunks))
	for i, chunk := range chunks {
		docs = append(docs, chromem.Document{
			ID:      DocumentID(filePath, i),
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
	}
	return docs, nil
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
func walkProjectFiles(root string, extraIgnoreDirs, extraIgnoreExtensions, extraIgnoreFileNames map[string]bool) ([]string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving root path: %w", err)
	}

	gitignorePatterns := loadGitignorePatterns(absRoot)

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
