package vectorindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	chromem "github.com/philippgille/chromem-go"
)

// sanitizeRe matches characters that are not alphanumeric, hyphens, or underscores.
var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// collectionName returns a deterministic, sanitized collection name for a branch.
func collectionName(branch string) string {
	sanitized := strings.ReplaceAll(branch, "/", "_")
	sanitized = sanitizeRe.ReplaceAllString(sanitized, "")
	if sanitized == "" {
		sanitized = "default"
	}
	return "branch_" + sanitized
}

// SwitchBranch switches to (or creates) a collection for the given branch.
// The caller must NOT hold s.mu.
func (s *Service) SwitchBranch(ctx context.Context, branchName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return errors.New("no database initialized; call SetProject first")
	}

	if branchName == s.currentBranch && s.collection != nil {
		return nil
	}

	name := collectionName(branchName)
	col, err := s.db.GetOrCreateCollection(name, nil, s.embeddingFunc)
	if err != nil {
		return fmt.Errorf("getting or creating collection %q: %w", name, err)
	}

	s.collection = col
	s.currentBranch = branchName
	s.logger.Info("switched branch collection", "branch", branchName, "collection", name)
	return nil
}

// ValidateCollection checks stored file hashes against current files on disk.
// It returns lists of stale (modified), new, and deleted file paths.
// workspacePath is the root directory to walk for source files.
func (s *Service) ValidateCollection(ctx context.Context, workspacePath string) (staleFiles, newFiles, deletedFiles []string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.collection == nil {
		return nil, nil, nil, errors.New("no collection available; call SwitchBranch first")
	}

	// Get stored file hashes from the collection.
	storedHashes, err := s.getCollectionFileHashes()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("getting collection file hashes: %w", err)
	}

	// Track which stored files we've seen on disk.
	seen := make(map[string]bool, len(storedHashes))

	// Walk workspace files using the same filtering as walkProjectFiles.
	absRoot, absErr := filepath.Abs(workspacePath)
	if absErr != nil {
		return nil, nil, nil, fmt.Errorf("resolving workspace path: %w", absErr)
	}
	gitignorePatterns := loadGitignorePatterns(absRoot)

	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkDirErr error) error {
		if walkDirErr != nil {
			return walkDirErr
		}

		if d.IsDir() {
			if isIgnoredDir(path, absRoot, gitignorePatterns) {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.Type().IsRegular() {
			return nil
		}

		if isIgnoredFile(path, absRoot, gitignorePatterns) {
			return nil
		}

		absPath, pathErr := filepath.Abs(path)
		if pathErr != nil {
			return nil //nolint:nilerr // skip unresolvable paths
		}

		content, readErr := os.ReadFile(absPath)
		if readErr != nil {
			return nil //nolint:nilerr // skip unreadable files
		}

		currentHash := computeHash(content)

		if storedHash, exists := storedHashes[absPath]; exists {
			seen[absPath] = true
			if storedHash != currentHash {
				staleFiles = append(staleFiles, absPath)
			}
		} else {
			newFiles = append(newFiles, absPath)
		}

		return nil
	})
	if walkErr != nil {
		return nil, nil, nil, fmt.Errorf("walking workspace %s: %w", workspacePath, walkErr)
	}

	// Files in collection but not on disk are deleted.
	for storedPath := range storedHashes {
		if !seen[storedPath] {
			deletedFiles = append(deletedFiles, storedPath)
		}
	}

	return staleFiles, newFiles, deletedFiles, nil
}

// GetCollectionFiles returns all unique file paths and their content hashes
// stored in the current collection.
func (s *Service) GetCollectionFiles() (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getCollectionFileHashes()
}

// getCollectionFileHashes retrieves stored file hashes from the collection.
// Caller must hold at least s.mu.RLock().
func (s *Service) getCollectionFileHashes() (map[string]string, error) {
	if s.collection == nil {
		return nil, errors.New("no collection available")
	}

	// chromem-go doesn't provide a direct way to list all documents' metadata.
	// We use a broad query with a large nResults to enumerate documents.
	// This is a pragmatic approach; for large collections a separate metadata
	// store would be more efficient.
	count := s.collection.Count()
	if count == 0 {
		return make(map[string]string), nil
	}

	ctx := context.Background()
	results, err := s.collection.Query(ctx, " ", count, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("querying collection for file list: %w", err)
	}

	fileHashes := make(map[string]string, len(results))
	for _, r := range results {
		fp := r.Metadata["file_path"]
		hash := r.Metadata["content_hash"]
		if fp != "" {
			// Keep one hash per file (all chunks of a file share the same hash).
			fileHashes[fp] = hash
		}
	}

	return fileHashes, nil
}

// RebuildCollection deletes the current branch collection and creates a fresh one.
// Caller must hold s.mu (write lock).
func (s *Service) RebuildCollection(ctx context.Context) error {
	if s.db == nil {
		return errors.New("no database initialized")
	}
	if s.currentBranch == "" {
		return errors.New("no branch set")
	}

	name := collectionName(s.currentBranch)

	if err := s.db.DeleteCollection(name); err != nil {
		s.logger.Warn("failed to delete collection during rebuild", "collection", name, "error", err)
	}

	col, err := s.db.GetOrCreateCollection(name, nil, s.embeddingFunc)
	if err != nil {
		return fmt.Errorf("creating fresh collection %q: %w", name, err)
	}
	s.collection = col
	s.logger.Info("rebuilt collection", "branch", s.currentBranch, "collection", name)
	return nil
}

// AddDocuments adds documents to the current collection.
// Caller must hold s.mu (write lock).
func (s *Service) AddDocuments(ctx context.Context, docs []chromem.Document) error {
	if s.collection == nil {
		return errors.New("no collection available")
	}
	if len(docs) == 0 {
		return nil
	}

	if err := s.collection.AddDocuments(ctx, docs, 1); err != nil {
		return fmt.Errorf("adding %d documents: %w", len(docs), err)
	}
	return nil
}

// DeleteDocumentsByIDs removes documents with the given IDs from the current collection.
// Caller must hold s.mu (write lock).
func (s *Service) DeleteDocumentsByIDs(ctx context.Context, ids []string) error {
	if s.collection == nil {
		return errors.New("no collection available")
	}
	if len(ids) == 0 {
		return nil
	}

	if err := s.collection.Delete(ctx, nil, nil, ids...); err != nil {
		return fmt.Errorf("deleting %d documents: %w", len(ids), err)
	}
	return nil
}

// DocumentID returns a deterministic document ID for a file path and chunk index.
func DocumentID(filePath string, chunkIndex int) string {
	h := sha256.Sum256([]byte(filePath))
	pathHash := hex.EncodeToString(h[:8]) // first 8 bytes = 16 hex chars
	return fmt.Sprintf("%s:%d", pathHash, chunkIndex)
}

// collectionUniqueFileCount returns the number of unique files in the collection.
// Caller must hold at least s.mu (read or write).
func (s *Service) collectionUniqueFileCount() int {
	hashes, err := s.getCollectionFileHashes()
	if err != nil {
		return 0
	}
	return len(hashes)
}

// computeHash returns the SHA-256 hex digest of the content.
func computeHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}
