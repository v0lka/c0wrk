package vectorindex

// Root-reference markers — the durable ownership record of the shared store
// (ADR-104 stage 5, reachability GC). Stage 2 gave the shared store two new
// artifact families keyed by OUR identities instead of branch names — the
// snapshot corpora (branches/branch_snap_<oid16>/) and the per-root overlays
// (branches/branch_root_<fingerprint>/) — so branch-name reachability
// (refs/heads) can no longer decide their lifetime: a corpus is shared by
// every root at that commit regardless of which branches exist, and an
// overlay belongs to a TREE, not to a branch. The ownership record is
// therefore per ROOT: every successful view attach writes
// refs/<rootKey>.ref under the storage root, recording the root's workspace
// path and the snapshot it currently views; the marker is durable (it
// survives manager eviction, restart and shutdown — only the tree's removal
// takes it away), so reachability is assembled from LIVE OWNERS:
//
//   - a corpus is reachable while some live root's marker names it (or the
//     process registry holds a live handle / build lease on it);
//   - an overlay is reachable while its root's marker is live;
//   - a marker is LIVE while its recorded workspace path still exists on
//     disk (the checkout lives as long as the project; a managed tree lives
//     until it is released — including while the app was not running);
//   - a branch's legacy store keeps the historical refs/heads reachability.
//
// The sweep (prune.go) collects everything else. The marker file name is the
// root's overlay key, so a root's marker, overlay store, lexical directory
// and sidecar are all derived from one identity.

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/safeio"
)

const (
	// refsDirName is the storage-root subdirectory holding the ownership
	// markers of the shared store (ADR-104 stage 5). The name matches the
	// ADR's §3 refs/ concept; the stage-2/5 store keys markers by OWNER —
	// the workspace roots that view a corpus (root_*) and the branches
	// whose tip commit it captures (bref_*) — never by storage directory
	// name.
	refsDirName = "refs"

	// rootRefSuffix terminates every marker file name under refs/.
	rootRefSuffix = ".ref"

	// rootRefFormat is the marker envelope version.
	rootRefFormat = 1

	// branchRefKeyPrefix namespaces BRANCH references under refs/: a branch
	// whose tip commit a corpus captures owns one (writeBranchRef), removed
	// by the branch's deletion (DeleteBranchData) or by the sweep when the
	// branch is gone. The branch is a REF, not a storage key (ADR-104 §1
	// L3): reachability flows through this pointer, never through a branch
	// NAME keying a directory.
	branchRefKeyPrefix = "bref_"
)

// rootRef is one owner's marker under refs/: the workspace path liveness is
// judged against (the tree the root IS; empty for branch refs, whose owner
// is detached explicitly, not by path disappearance) and the snapshot the
// owner keeps reachable.
type rootRef struct {
	Format   int    `json:"format"`
	Root     string `json:"root"`
	Snapshot string `json:"snapshot"`
	Updated  string `json:"updated"`
}

// rootRefPath returns the marker file path of rootKey under storageRoot.
func rootRefPath(storageRoot, rootKey string) string {
	return filepath.Join(storageRoot, refsDirName, rootKey+rootRefSuffix)
}

// branchRefKey derives the refs/ file key of a branch reference from the
// branch's name as a SHA-256 of the name — NOT through the historical
// lossy sanitizer. The sanitizer (and case-insensitive filesystems) fold
// distinct branch names onto ONE marker file: `bref_Feature.ref` and
// `bref_feature.ref` are the same file on macOS/Windows defaults, and the
// sanitizer's own `/`→`_` and character-stripping folds collapse
// `feature/x` with `feature_x` on EVERY filesystem — the second live
// branch's write would silently replace the first's ownership record and
// the sweep would reclaim a live branch's corpus. The hash key is
// injective over branch names. Markers written by earlier builds under
// the sanitized key are still honored: readers keep every key derivable
// from a SURVIVING branch by EITHER derivation, so an upgrade never drops
// an existing keep.
func branchRefKey(branch string) string {
	return branchRefKeyPrefix + computeHash([]byte(branch))
}

// legacyBranchRefKey is the pre-hash sanitized marker key of a branch
// (kept for reading and removing markers written by earlier builds; see
// branchRefKey).
func legacyBranchRefKey(branch string) string {
	sanitized := strings.ReplaceAll(branch, "/", "_")
	return branchRefKeyPrefix + sanitizeRe.ReplaceAllString(sanitized, "")
}

// isSafeStoreKeySegment reports whether s may serve as a single path segment
// naming a store artifact (a marker file, a branches/ child): non-empty and
// a fixed point of the branch sanitizer (no separators, no "..", nothing the
// sanitizer would strip). Our snap_/root_ keys always qualify; a foreign
// name that does not is never treated as an owner record.
func isSafeStoreKeySegment(s string) bool {
	return s != "" && s == sanitizeRe.ReplaceAllString(s, "") &&
		!strings.ContainsRune(s, filepath.Separator)
}

// writeRootRef atomically records rootKey's ownership marker: workspaceRoot
// is the liveness anchor, snapKey the snapshot the root views. Called on
// every view attach (an intent pin lands BEFORE the corpus build, so the
// sweep can never reclaim a corpus a root is in the middle of attaching).
func writeRootRef(storageRoot, rootKey, workspaceRoot, snapKey string) error {
	if !isSafeStoreKeySegment(rootKey) || snapKey == "" {
		return os.ErrInvalid
	}
	return writeRefRecord(storageRoot, rootKey, rootRef{Root: filepath.Clean(workspaceRoot), Snapshot: snapKey})
}

// writeBranchRef atomically records a branch reference: the branch's tip
// commit is captured by snapKey's corpus, so the branch owns a keep on it.
// The ref is written by the attaching root (its HEAD is the branch tip) and
// removed by the branch's deletion — the branch is an OWNER through this
// pointer, never through a directory name (ADR-104 §1 L3). A legacy
// sanitized-name marker of the same branch is deliberately NOT removed
// here: legacy keys can COLLIDE across distinct branch spellings (the
// sanitizer's /→_ and character stripping, and case-insensitive
// filesystems), so removing one could drop a different SURVIVING branch's
// only ownership record — the exact keep this hash keying exists to
// protect. The legacy spelling is honored on read (liveRootReferences
// keeps both derivations for every surviving branch) and removed only by
// the branch-deletion path (removeBranchRef removes both spellings of the
// deleted branch); a stale legacy marker therefore only ever over-keeps.
func writeBranchRef(storageRoot, branch, snapKey string) error {
	if branch == "" || snapKey == "" {
		return os.ErrInvalid
	}
	return writeRefRecord(storageRoot, branchRefKey(branch), rootRef{Snapshot: snapKey})
}

// writeRefRecord is the shared atomic write of one refs/ record. Markers
// live ONLY at <storageRoot>/refs/ — the storage-root level, outside any
// §3 generation — so they survive every generation advance without any
// carry (the generation tree's own refs/ placeholder directory is empty
// by design; see storeRefsDirName).
func writeRefRecord(storageRoot, key string, r rootRef) error {
	refsDir := filepath.Join(storageRoot, refsDirName)
	if err := ensureRealDir(refsDir, 0o750); err != nil {
		return err
	}
	r.Format = rootRefFormat
	r.Updated = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(&r, "", "  ")
	if err != nil {
		return err
	}
	return safeio.WriteFileAtomic(filepath.Join(refsDir, key+rootRefSuffix), data, 0o644)
}

// removeRootRef deletes rootKey's ownership marker. A missing marker is a
// no-op; other errors propagate to the caller's fail-soft handling.
func removeRootRef(storageRoot, rootKey string) error {
	if err := os.Remove(rootRefPath(storageRoot, rootKey)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// removeBranchRef deletes a branch's reference (the branch's own detach —
// DeleteBranchData). Both marker forms of the branch are removed. Missing
// -is-no-op like removeRootRef.
func removeBranchRef(storageRoot, branch string) error {
	if err := removeRootRef(storageRoot, branchRefKey(branch)); err != nil {
		return err
	}
	return removeRootRef(storageRoot, legacyBranchRefKey(branch))
}

// rootRefLive reports whether the marker's workspace tree still exists. The
// stat follows symlinks by design: a legacy row's stored spelling routes
// through a symlinked ancestor and is live exactly while the tree is.
func rootRefLive(r rootRef) bool {
	if r.Root == "" {
		return false
	}
	info, err := os.Stat(r.Root)
	return err == nil && info.IsDir()
}

// readRootRefs loads every ownership marker under storageRoot. ok is
// false when the refs/ directory does not exist (a store that never
// recorded an owner — the sweep then keeps every corpus/overlay artifact,
// the conservative upgrade posture: the first marker-writing attach opts
// the store into reachability GC) OR when any single marker entry exists
// but cannot be READ or PARSED (a transient EACCES/EMFILE/EIO, an
// AV/backup lock, or a foreign marker format written by a newer binary).
// Folding such an entry into a zero-value ref would silently drop a
// possibly-LIVE owner's identity from the keep set and the sweep would
// delete its overlay and corpora — the per-entry conservative posture
// mirrors the directory-level one: resolve nothing, keep everything, and
// let the next sweep retry against the intact markers.
func readRootRefs(storageRoot string) (refs map[string]rootRef, ok bool) {
	entries, err := os.ReadDir(filepath.Join(storageRoot, refsDirName))
	if err != nil {
		return nil, false
	}
	refs = make(map[string]rootRef, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		key, ok := strings.CutSuffix(entry.Name(), rootRefSuffix)
		if !ok || !isSafeStoreKeySegment(key) {
			continue // not an owner record — never trusted, never removed
		}
		data, err := safeio.ReadFile(rootRefPath(storageRoot, key))
		if err != nil {
			// Present but unreadable: the owner may well be live — only
			// the read failed. Resolve nothing this pass.
			return nil, false
		}
		var r rootRef
		if json.Unmarshal(data, &r) != nil || r.Format != rootRefFormat {
			// A foreign/newer marker format: this binary cannot interpret
			// the ownership it records — keep everything rather than
			// detach owners on an upgrade/downgrade cycle.
			return nil, false
		}
		refs[key] = r
	}
	return refs, true
}

// corpusIdentity classifies a store-key identity (the pre-collectionName
// key a branches/ directory, lexical directory or sidecar is named after):
// snapshot corpora (snap_…) and per-root overlays (root_…) are OUR key
// spaces; anything else is a legacy branch name.
func isCorpusIdentity(identity string) bool {
	return strings.HasPrefix(identity, snapshotKeyPrefix)
}

// isOverlayIdentity reports whether identity is a per-root overlay key.
func isOverlayIdentity(identity string) bool {
	return strings.HasPrefix(identity, overlayKeyPrefix)
}

// identityFromCollectionName strips the layout's collection-name prefix from
// a branches/ child name, reporting whether the name has the exact safe
// shape collectionName produces.
func identityFromCollectionName(name string) (string, bool) {
	identity, ok := strings.CutPrefix(name, collectionNamePrefix)
	if !ok || !isSafeStoreKeySegment(identity) {
		return "", false
	}
	return identity, true
}

// liveRootReferences assembles the shared store's reachability keep set
// from the on-disk ownership records: the identities (store-key space) of
// every LIVE root's overlay key, plus the snapshots reached by live root
// markers and by SURVIVING branch references. Two detach rules run here:
//
//   - a ROOT marker whose workspace path is gone (a tree removed outside
//     the app's lifecycle) is removed — the root detached, its reference
//     goes with it;
//   - a BRANCH reference whose branch is absent from keepBranches (the
//     branch was deleted while the app was not running) is removed the same
//     way — the branch's own reference, not its data, is the detach. A
//     marker keyed by EITHER branchRefKey derivation (the current hash key
//     or the legacy sanitized key written by earlier builds) survives
//     while its branch survives.
//
// The data itself is reclaimed by the caller's sweep only if nothing else
// reaches it. Fail-soft: an unreadable refs dir — or a single unreadable
// or foreign-format marker in it — reports no owners (ok=false), leaving
// every shared artifact kept.
func liveRootReferences(logger *slog.Logger, storageRoot string, keepBranches []string, extraProtectedSnapKeys map[string]struct{}) (keepIdentities map[string]struct{}) {
	refs, ok := readRootRefs(storageRoot)
	if !ok {
		return nil
	}
	survivingBranches := make(map[string]struct{}, 2*len(keepBranches))
	for _, branch := range keepBranches {
		if branch == "" {
			continue
		}
		survivingBranches[branchRefKey(branch)] = struct{}{}
		survivingBranches[legacyBranchRefKey(branch)] = struct{}{}
	}
	keep := make(map[string]struct{}, len(refs)+len(extraProtectedSnapKeys))
	for key, r := range refs {
		wellFormed := r.Snapshot != "" && isSafeStoreKeySegment(r.Snapshot)
		if !wellFormed {
			continue // a well-formed envelope recording no usable snapshot: trusted by no one, deleted by no one
		}
		if isBranchRefKey := strings.HasPrefix(key, branchRefKeyPrefix); isBranchRefKey {
			if _, survives := survivingBranches[key]; !survives {
				if err := removeRootRef(storageRoot, key); err != nil {
					logger.Debug("vector index: could not remove a gone branch's reference",
						"ref", key, "error", err)
				}
				continue
			}
			keep[r.Snapshot] = struct{}{}
			continue
		}
		// Root marker: live while its workspace tree exists.
		if r.Root == "" || !rootRefLive(r) {
			if r.Root != "" {
				// A well-formed marker whose tree is gone: the root
				// detached outside the app's lifecycle — drop its
				// reference now (the release path normally does).
				if err := removeRootRef(storageRoot, key); err != nil {
					logger.Debug("vector index: could not remove dead root reference",
						"root", key, "error", err)
				}
			}
			continue
		}
		keep[key] = struct{}{}
		keep[r.Snapshot] = struct{}{}
	}
	for key := range extraProtectedSnapKeys {
		keep[key] = struct{}{}
	}
	return keep
}
