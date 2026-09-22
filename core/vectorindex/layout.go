package vectorindex

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	chromem "github.com/philippgille/chromem-go"
)

// Layout (ADR-064). A project's vector-index storage root
// (<project>/vector_index/) is laid out as:
//
//	<root>/branches/<collectionName(branch)>/   chromem DB root for ONE branch
//	<root>/lexical/<branch>/                    bleve lexical index (unchanged)
//	<root>/embedding_cache/                     content-addressed embed cache
//	<root>/file_hashes_<collectionName>.json    per-branch file-hash sidecar
//	<root>/contentless_<collectionName>.done    per-branch migration marker
//
// Each branch owns a STABLE chromem DB root under branches/, so
// chromem.NewPersistentDB — which eagerly gob-decodes every collection in the
// directory it is given — only ever decodes the ACTIVE branch. Before ADR-064
// the DB root was the storage root itself, so every open decoded EVERY branch
// collection (one per git branch), which on a large multi-branch index took
// minutes while holding the service write lock.
//
// The storage root is never handed to chromem again, so lexical/ and
// embedding_cache/ can never be mistaken for collections (they would be
// skipped anyway, but this removes the coupling entirely).

const (
	// branchRootsDirName is the storage subdirectory holding the per-branch
	// chromem DB roots.
	branchRootsDirName = "branches"

	// orphanBranchRootName is a non-branch pseudo-name under branches/ that
	// receives legacy collection directories whose name could not be
	// recovered during the one-time migration. It matches no real branch, so
	// it is never opened; the affected branch is simply re-indexed.
	orphanBranchRootName = "_orphan"

	// legacyCollectionDirNameLen is the length of chromem's per-collection
	// directory name (hash2hex => 8 lowercase hex characters). It is used
	// ONLY by the one-time legacy-layout migration to recognize the stray
	// collection directories that used to sit directly under the storage root.
	legacyCollectionDirNameLen = 8

	// collectionNamePrefix is what collectionName prepends to the sanitized
	// branch name, so every real branch identity — chromem collection name,
	// file-hash sidecar, content-less marker and branch root alike — starts
	// with it. The one-time legacy migration requires it of a name recovered
	// from disk (see isSafeBranchRootName).
	collectionNamePrefix = "branch_"

	// chromemMetadataFileName mirrors chromem-go's per-collection metadata
	// file name: metadataFileName ("00000000") plus the ".gob" extension it
	// appends for uncompressed DBs. It is read ONLY by the one-time legacy
	// migration, to recover a collection's name from disk without replicating
	// chromem's directory hashing. If chromem ever changes this encoding, the
	// affected directories land under branches/_orphan/ and their branches are
	// re-indexed — degraded, never wrong.
	chromemMetadataFileName = "00000000.gob"
)

// chromemCollectionMetadata mirrors the anonymous struct chromem gob-encodes
// into a collection's metadata file ({Name string; Metadata map[string]string}).
// gob matches fields by name, so decoding into this named struct is valid.
// Only Name is used by the migration.
type chromemCollectionMetadata struct {
	Name     string
	Metadata map[string]string
}

// branchRootPath returns the stable chromem DB root directory for a branch
// under a project's vector-index storage root. It is keyed by
// collectionName(branch) — the same identity used for the branch's file-hash
// sidecar and content-less marker — so the mapping branch => directory is
// deterministic and needs no chromem-internal hashing.
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
// collection directory's metadata file, or "" when the file is missing or
// unreadable. Caller must NOT hold s.mu longer than necessary; the read is a
// single small-file decode.
func readLegacyCollectionName(dir string) string {
	f, err := os.Open(filepath.Join(dir, chromemMetadataFileName))
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

// isSafeBranchRootName reports whether name may be used as a single path
// segment under branches/. The migration recovers the name by decoding a
// chromem metadata file and then joins it into a destination path, so it is
// accepted only in the exact shape collectionName produces: the "branch_"
// prefix followed by a non-empty run entirely within sanitizeRe's allowed set
// ([A-Za-z0-9_-]). Anything else — unreadable or foreign metadata, a
// corrupted gob, a name carrying a separator or a ".." element, or a
// sanitized-but-unprefixed value that is not a branch identity and would
// otherwise let foreign metadata squat an arbitrary name under branches/ — is
// rejected and routed to the orphan root instead of being trusted as a path.
func isSafeBranchRootName(name string) bool {
	rest, ok := strings.CutPrefix(name, collectionNamePrefix)
	return ok && rest != "" && rest == sanitizeRe.ReplaceAllString(rest, "")
}

// migrateLegacyLayoutLocked re-homes every legacy collection directory that
// still sits directly under the project's storage root into its per-branch DB
// root (branches/<collectionName>/<hash2hex>/), so that opening a branch can
// decode exactly one collection. The migration is idempotent and cheap —
// directory renames only, never per-document work — and a no-op once no legacy
// directories remain. A legacy directory whose name cannot be recovered is
// moved under branches/_orphan/ (kept on disk, never loaded).
//
// The caller must hold s.mu.
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
		// The recovered name becomes a path segment, so it is trusted only in
		// the shape collectionName wrote it in; anything else — missing or
		// foreign metadata, a corrupted gob — is parked under the orphan root.
		name := readLegacyCollectionName(src)
		if !isSafeBranchRootName(name) {
			name = orphanBranchRootName
		}
		destDir := filepath.Join(projectPath, branchRootsDirName, name)
		if err := os.MkdirAll(destDir, 0o750); err != nil {
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
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("creating branch root %s: %w", root, err)
	}
	db, err := newPersistentDB(root, false)
	if err != nil {
		return nil, fmt.Errorf("opening branch DB at %s: %w", root, err)
	}
	return db, nil
}
