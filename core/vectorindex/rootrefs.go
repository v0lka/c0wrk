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
// branch's short name, through the same sanitizer every other branch-keyed
// identity uses (a collision keeps the shared reference — the historical
// lossy-sanitizer posture).
func branchRefKey(branch string) string {
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
// pointer, never through a directory name (ADR-104 §1 L3).
func writeBranchRef(storageRoot, branch, snapKey string) error {
	if branch == "" || snapKey == "" {
		return os.ErrInvalid
	}
	return writeRefRecord(storageRoot, branchRefKey(branch), rootRef{Snapshot: snapKey})
}

// writeRefRecord is the shared atomic write of one refs/ record.
func writeRefRecord(storageRoot, key string, r rootRef) error {
	return writeRefRecordIn(filepath.Join(storageRoot, refsDirName), key, r)
}

// writeRefRecordIn writes one refs/ record into an EXPLICIT refs directory —
// the §3 relocation seam (chunkstore.go): once a store generation exists,
// ownership markers live under the generation's refs/ tree, and the writers
// resolve that directory before calling this.
func writeRefRecordIn(refsDir, key string, r rootRef) error {
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
// DeleteBranchData). Missing-is-no-op like removeRootRef.
func removeBranchRef(storageRoot, branch string) error {
	return removeRootRef(storageRoot, branchRefKey(branch))
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

// readRootRefs loads every ownership marker under storageRoot. ok is false
// when the refs/ directory does not exist — a store that never recorded an
// owner (pre-stage-5 corpora, or a store only ever touched by the legacy
// branch model); the sweep then keeps every corpus/overlay artifact
// (conservative upgrade posture: the first marker-writing attach opts the
// store into reachability GC). Malformed entries are returned as zero-value
// refs keyed by their (shape-validated) file name — never trusted, never
// deleted by the sweep.
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
			continue // not an owner record — never removed, never trusted
		}
		data, err := safeio.ReadFile(rootRefPath(storageRoot, key))
		if err != nil {
			refs[key] = rootRef{} // present but unreadable: not live, not deleted
			continue
		}
		var r rootRef
		if json.Unmarshal(data, &r) != nil || r.Format != rootRefFormat {
			refs[key] = rootRef{}
			continue
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
//     way — the branch's own reference, not its data, is the detach.
//
// The data itself is reclaimed by the caller's sweep only if nothing else
// reaches it. Fail-soft: an unreadable refs dir reports no owners
// (ok=false), leaving every shared artifact kept.
func liveRootReferences(logger *slog.Logger, storageRoot string, keepBranches []string, extraProtectedSnapKeys map[string]struct{}) (keepIdentities map[string]struct{}) {
	refs, ok := readRootRefs(storageRoot)
	if !ok {
		return nil
	}
	survivingBranches := make(map[string]struct{}, len(keepBranches))
	for _, branch := range keepBranches {
		if branch != "" {
			survivingBranches[branchRefKey(branch)] = struct{}{}
		}
	}
	keep := make(map[string]struct{}, len(refs)+len(extraProtectedSnapKeys))
	for key, r := range refs {
		wellFormed := r.Snapshot != "" && isSafeStoreKeySegment(r.Snapshot)
		if !wellFormed {
			continue // unreadable or foreign record: trusted by no one, deleted by no one
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
