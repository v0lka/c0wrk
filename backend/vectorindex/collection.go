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

	"github.com/user/agent/backend/vectorindex/lexical"
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

// lexicalBranchDirName returns a sanitized directory name for a branch,
// used as the per-branch subdirectory under the project's lexical folder.
func lexicalBranchDirName(branch string) string {
	sanitized := strings.ReplaceAll(branch, "/", "_")
	sanitized = sanitizeRe.ReplaceAllString(sanitized, "")
	if sanitized == "" {
		sanitized = "default"
	}
	return sanitized
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

	// Close any previously-open lexical index and open the one for this
	// branch. The lexical index is only persisted when the service was
	// configured with a PersistPath; in-memory mode skips it entirely.
	if s.lexical != nil {
		if closeErr := s.lexical.Close(); closeErr != nil {
			s.logger.Warn("failed to close previous lexical index", "error", closeErr)
		}
		s.lexical = nil
	}
	if s.persistPath != "" && s.projectID != "" {
		lexDir := filepath.Join(s.persistPath, s.projectID, "lexical", lexicalBranchDirName(branchName))
		// Ensure the parent directory (…/{projectID}/lexical/) exists;
		// bleve's New() creates the leaf (branch) directory itself.
		if mkErr := os.MkdirAll(filepath.Dir(lexDir), 0o750); mkErr != nil {
			s.logger.Warn("failed to create lexical parent directory", "path", lexDir, "error", mkErr)
		} else {
			lex, lexErr := lexical.Open(lexDir)
			if lexErr != nil {
				s.logger.Warn("failed to open lexical index", "path", lexDir, "error", lexErr)
			} else {
				s.lexical = lex
			}
		}
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
			if isIgnoredDir(path, absRoot, gitignorePatterns, s.ignoreDirs) {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.Type().IsRegular() {
			return nil
		}

		if isIgnoredFile(path, absRoot, gitignorePatterns, s.ignoreExtensions, s.ignoreFileNames) {
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

// AddDocuments adds documents to the current collection and mirrors them
// to the per-branch lexical index. Chromem commits first; lexical errors
// are logged but not returned, since the reconciliation loop in the
// manager will repair drift via RebuildLexical on the next project open.
// Caller must hold s.mu (write lock).
func (s *Service) AddDocuments(ctx context.Context, vecDocs []chromem.Document, lexDocs []lexical.Doc) error {
	if s.collection == nil {
		return errors.New("no collection available")
	}
	if len(vecDocs) == 0 && len(lexDocs) == 0 {
		return nil
	}

	if len(vecDocs) > 0 {
		if err := s.collection.AddDocuments(ctx, vecDocs, 1); err != nil {
			return fmt.Errorf("adding %d documents: %w", len(vecDocs), err)
		}
	}

	if s.lexical != nil && len(lexDocs) > 0 {
		if err := s.lexical.Upsert(ctx, lexDocs); err != nil {
			s.logger.Warn("lexical upsert failed; will be repaired via RebuildLexical",
				"branch", s.currentBranch, "docs", len(lexDocs), "error", err)
		}
	}
	return nil
}

// DeleteDocumentsByIDs removes documents with the given IDs from the current
// chromem collection and from the lexical index (best-effort).
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

	if s.lexical != nil {
		if err := s.lexical.Delete(ctx, ids); err != nil {
			s.logger.Warn("lexical delete failed; will be repaired via RebuildLexical",
				"branch", s.currentBranch, "ids", len(ids), "error", err)
		}
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
