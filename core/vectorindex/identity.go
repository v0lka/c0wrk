package vectorindex

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Identity model of ADR-104 (specs/decisions/104-content-addressed-vector-store.md):
// every artifact is keyed at one of four levels, and the ABSOLUTE WORKSPACE ROOT
// is never part of a shared identity. L1 content/chunk (the only duplication
// boundary), L2 file occurrence ((snapshot, path) — path is root-RELATIVE),
// L3 snapshot/view, L4 root-local state (the bound workspace root, the sidecar's
// size/mtime stat facts, the ignore policy a root indexes under). This file
// owns the coordinate primitives the levels share.

const (
	// storeFormatV2 is the format version recorded in a branch generation
	// manifest (ADR-104 §3). An unknown version makes the store read as
	// ABSENT — forward compatibility is deliberately refused: silent partial
	// reads are worse than a re-index.
	storeFormatV2 = 2

	// coordsRelativeV1 identifies the document-coordinate scheme of the
	// current generation: document IDs, sidecar keys and lexical IDs are
	// derived from the file's path RELATIVE to the workspace root (L2
	// occurrence identity), never from an absolute path. Pre-ADR-104 stores
	// keyed documents by absolute path; a manifest without this coordinate
	// tag (or with a different one) belongs to another generation and is
	// never mixed with the current one — bump the tag when the grammar
	// changes again.
	coordsRelativeV1 = "rel-v1"

	// relPathMetaKey is the chromem metadata key carrying a chunk's
	// root-relative occurrence path (slash-separated). It survives the
	// commit-time metadata strip (see commitMetadataKeys) alongside the
	// legacy absolute file_path, which stays for pre-ADR-104 documents and
	// per-tree diagnostics — the RELATIVE key is the coordinate read paths
	// resolve against the requesting root (per-request hydration).
	relPathMetaKey = "rel_path"
)

// relPathFromAbs returns abs expressed relative to root as a slash-separated
// path ("pkg/file.go"); "" when root is empty or abs is not under root. The
// relative form is the L2 occurrence coordinate: identical repository layouts
// checked out under any workspace root (a branch, a managed worktree, a second
// machine's clone) share one identity, which is what lets occurrences — and,
// at the store level, chunks — be shared instead of re-derived per tree.
func relPathFromAbs(root, abs string) string {
	if root == "" {
		return ""
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// rootFingerprint scopes root-local facts to ONE workspace tree: the SHA-256
// of the cleaned absolute root, first 16 hex chars. The file-hash sidecar's
// size/mtime stat fast-path is root-local state (ADR-104 L4): a sidecar
// written under one root must never validate another root's files — same
// relative paths in two trees carry independent mtimes, and treating them as
// shared would let a stale stat classification hide real rewrites. The empty
// root (in-memory services, standalone tests) fingerprints as "".
func rootFingerprint(root string) string {
	if root == "" {
		return ""
	}
	clean, err := filepath.Abs(root)
	if err != nil {
		clean = filepath.Clean(root)
	}
	sum := sha256.Sum256([]byte(clean))
	return hex.EncodeToString(sum[:8])
}

// occurrencePath returns the path a stored document's ID was derived from:
// the root-relative coordinate when the document carries one (post-ADR-104
// documents), the legacy absolute file_path otherwise. Every ID re-derivation
// or sidecar-key comparison against STORED documents goes through this helper
// so the two coordinate eras never get mixed up at a call site.
func occurrencePath(md map[string]string) string {
	if rel := md[relPathMetaKey]; rel != "" {
		return rel
	}
	return md["file_path"]
}

// IgnoreFingerprint fingerprints the ignore/eligibility policy a root indexes
// under (ADR-104 §2 fingerprint I): the honoured ignore-file set, the extra
// exclude patterns, the submodule flag, and the walk's size/chunk caps. It is
// recorded in the branch generation manifest so a policy change is visible as
// a membership change — the per-pass walk re-applies the CURRENT policy
// regardless (files a new policy excludes are reported deleted and removed;
// see ValidateCollection), and a future shared-store reader can gate table
// loading on it instead of trusting a table built under another policy.
func IgnoreFingerprint(ignoreFiles, excludePatterns []string, includeSubmodules bool, maxFileSize int64, maxChunksPerFile int) string {
	sorted := func(parts []string) string {
		out := slices.Clone(parts)
		slices.Sort(out)
		return strings.Join(out, ",")
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("v1|ig=%s|ex=%s|sub=%t|size=%d|chunks=%d",
		sorted(ignoreFiles), sorted(excludePatterns), includeSubmodules, maxFileSize, maxChunksPerFile)))
	return hex.EncodeToString(sum[:6])
}
