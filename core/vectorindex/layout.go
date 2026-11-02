package vectorindex

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/sp4rk/safeio"
)

// Storage layout (ADR-064). A project's vector-index storage root
// (<project>/vector_index/) holds one STABLE chromem DB root per branch under
// branches/<collectionName(branch)>/, so chromem.NewPersistentDB — which
// eagerly gob-decodes every collection in the directory it is given — decodes
// only the ACTIVE branch (before ADR-064 every open decoded every branch's
// collection, minutes on a large multi-branch index, all under the write
// lock). The storage root itself is never handed to chromem again, so
// lexical/ and embedding_cache/ can never be parsed as collections. See
// specs/decisions/064-branch-scoped-lazy-vector-index-open.md and
// specs/domains/workspace.md.

const (
	// branchRootsDirName is the storage subdirectory holding the per-branch
	// chromem DB roots.
	branchRootsDirName = "branches"

	// orphanBranchRootName is a non-branch pseudo-name under branches/ that
	// receives legacy collection directories whose name could not be
	// recovered during the one-time migration; it matches no real branch, so
	// it is kept on disk, never loaded, and the affected branch is
	// re-indexed.
	orphanBranchRootName = "_orphan"

	// legacyCollectionDirNameLen is chromem's per-collection directory name
	// length (hash2hex => 8 lowercase hex chars), used ONLY by the one-time
	// legacy-layout migration to recognize stray root-level collection
	// directories.
	legacyCollectionDirNameLen = 8

	// collectionNamePrefix is what collectionName prepends to the sanitized
	// branch name, so every real branch identity — collection name, sidecar,
	// marker and branch root alike — starts with it; the one-time legacy
	// migration requires it of a name recovered from disk (see
	// isSafeBranchRootName).
	collectionNamePrefix = "branch_"

	// chromemMetadataFileName mirrors chromem-go's per-collection metadata
	// file name; read ONLY by the legacy migration to recover a collection's
	// name without replicating chromem's directory hashing. If chromem ever
	// changes this encoding, the affected directories land under
	// branches/_orphan/ — degraded, never wrong.
	chromemMetadataFileName = "00000000.gob"
)

// chromemCollectionMetadata mirrors the anonymous struct chromem gob-encodes
// into a collection's metadata file; gob matches fields by name, so decoding
// into this named struct is valid. Only Name is used by the migration.
type chromemCollectionMetadata struct {
	Name     string
	Metadata map[string]string
}

// branchRootPath returns the stable chromem DB root for a branch, keyed by
// collectionName(branch) — the same identity as the file-hash sidecar and
// content-less marker — so branch ⇒ directory is deterministic and needs no
// chromem-internal hashing.
func branchRootPath(projectPath, branch string) string {
	return filepath.Join(projectPath, branchRootsDirName, collectionName(branch))
}

// isLegacyCollectionDirName reports whether name looks like a chromem
// per-collection directory (8 lowercase hex characters). lexical/ and
// embedding_cache/ are not hex and are therefore never migrated.
func isLegacyCollectionDirName(name string) bool {
	if len(name) != legacyCollectionDirNameLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// readLegacyCollectionName recovers the collection name recorded in a legacy
// collection directory's metadata file, "" when the file is missing or
// unreadable (the read is a single small-file decode).
func readLegacyCollectionName(dir string) string {
	f, err := safeio.Open(filepath.Join(dir, chromemMetadataFileName))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var md chromemCollectionMetadata
	if err := gob.NewDecoder(f).Decode(&md); err != nil {
		return ""
	}
	return md.Name
}

// isSafeBranchRootName reports whether name may become a single path segment
// under branches/. The migration decodes a chromem metadata file and joins
// the recovered name into a destination path, so only the exact shape
// collectionName produces is accepted — the "branch_" prefix followed by a
// non-empty run entirely within sanitizeRe's allowed set ([A-Za-z0-9_-]).
// Anything else — unreadable or foreign metadata, a corrupted gob, a name
// carrying a separator or a ".." element — would let foreign metadata squat
// an arbitrary name under branches/ and is rejected to the orphan root.
func isSafeBranchRootName(name string) bool {
	rest, ok := strings.CutPrefix(name, collectionNamePrefix)
	return ok && rest != "" && rest == sanitizeRe.ReplaceAllString(rest, "")
}

// migrateLegacyLayoutLocked re-homes every legacy collection directory still
// sitting directly under the storage root into branches/<collectionName>/, so
// opening a branch decodes exactly one collection (ADR-064). Idempotent and
// cheap — directory renames only; an unrecoverable name moves under
// branches/_orphan/. The caller must hold s.mu. See
// specs/decisions/064-branch-scoped-lazy-vector-index-open.md.
func (s *Service) migrateLegacyLayoutLocked(projectPath string) {
	entries, err := os.ReadDir(projectPath)
	if err != nil {
		s.logger.Warn("vector index: cannot read storage root for layout migration",
			"path", projectPath, "error", err)
		return
	}

	moved := 0
	for _, e := range entries {
		if !e.IsDir() || !isLegacyCollectionDirName(e.Name()) {
			continue
		}
		src := filepath.Join(projectPath, e.Name())
		// The recovered name becomes a path segment: trust only the shape
		// collectionName writes; anything else is parked under the orphan
		// root.
		name := readLegacyCollectionName(src)
		if !isSafeBranchRootName(name) {
			name = orphanBranchRootName
		}
		destDir := filepath.Join(projectPath, branchRootsDirName, name)
		if err := ensureRealDir(destDir, 0o750); err != nil {
			s.logger.Warn("vector index: cannot create branch root during migration",
				"path", destDir, "error", err)
			continue
		}
		dest := filepath.Join(destDir, e.Name())
		if _, statErr := os.Stat(dest); statErr == nil {
			// Already migrated (re-run) or a genuine collision: never
			// overwrite an existing branch root; leave the source in place.
			s.logger.Warn("vector index: migration target already exists; leaving legacy directory",
				"src", src, "dest", dest)
			continue
		}
		if err := os.Rename(src, dest); err != nil {
			s.logger.Warn("vector index: migration rename failed",
				"src", src, "dest", dest, "error", err)
			continue
		}
		moved++
	}

	if moved > 0 {
		s.logger.Info("vector index: migrated legacy collection layout to per-branch roots",
			"project", projectPath, "moved", moved)
	}
}

// openBranchDBLocked opens (creating if absent) the chromem DB root for a
// single branch. Only this branch's documents are gob-decoded. The caller must
// hold s.mu.
func (s *Service) openBranchDBLocked(branch string) (*chromem.DB, error) {
	root := branchRootPath(s.current.projectPath, branch)
	if err := ensureRealDir(root, 0o750); err != nil {
		return nil, fmt.Errorf("creating branch root %s: %w", root, err)
	}
	db, err := newPersistentDB(root, false)
	if err != nil {
		return nil, fmt.Errorf("opening branch DB at %s: %w", root, err)
	}
	return db, nil
}
