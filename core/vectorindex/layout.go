package vectorindex

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

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

// rehomeRootLevelLegacyCollections is the filesystem half of the one-time
// pre-ADR-064 re-homing, shared by SetProject's synchronous pass and the
// unified store migration sweep (layout.go): every legacy collection
// directory still sitting directly under the storage root moves into
// branches/<collectionName>/, so opening a branch decodes exactly one
// collection. Directory renames only; an unrecoverable name moves under
// branches/_orphan/. Returns the re-homed directory names (both recovered
// and orphaned destinations) and the ones that could not be moved (I/O
// failures — reported, retried by no one: the next sweep records them).
func rehomeRootLevelLegacyCollections(logger *slog.Logger, projectPath string) (moved, failed []string) {
	entries, err := os.ReadDir(projectPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logger.Warn("vector index: cannot read storage root for layout migration",
				"path", projectPath, "error", err)
		}
		return nil, nil
	}

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
			logger.Warn("vector index: cannot create branch root during migration",
				"path", destDir, "error", err)
			failed = append(failed, e.Name())
			continue
		}
		dest := filepath.Join(destDir, e.Name())
		if _, statErr := os.Stat(dest); statErr == nil {
			// Already migrated (re-run) or a genuine collision: never
			// overwrite an existing branch root; leave the source in place.
			logger.Warn("vector index: migration target already exists; leaving legacy directory",
				"src", src, "dest", dest)
			failed = append(failed, e.Name())
			continue
		}
		if err := os.Rename(src, dest); err != nil {
			logger.Warn("vector index: migration rename failed",
				"src", src, "dest", dest, "error", err)
			failed = append(failed, e.Name())
			continue
		}
		moved = append(moved, e.Name())
	}
	return moved, failed
}

// migrateLegacyLayoutLocked re-homes every legacy collection directory still
// sitting directly under the storage root into branches/<collectionName>/,
// so opening a branch decodes exactly one collection (ADR-064). Idempotent
// and cheap — directory renames only; an unrecoverable name moves under
// branches/_orphan/. The caller must hold s.mu. See
// specs/decisions/064-branch-scoped-lazy-vector-index-open.md.
func (s *Service) migrateLegacyLayoutLocked(projectPath string) {
	moved, _ := rehomeRootLevelLegacyCollections(s.logger, projectPath)
	if len(moved) > 0 {
		s.logger.Info("vector index: migrated legacy collection layout to per-branch roots",
			"project", projectPath, "moved", len(moved))
	}
}

// openBranchDBLocked opens (creating if absent) the chromem DB root for a
// single branch. Only this branch's documents are gob-decoded. The open is
// gated by the branch generation manifest (ADR-104): a root whose stored
// generation is incompatible with the current identity/embedding era is reset
// first, so embeddings produced under other coordinates are never mixed with
// the current ones. The caller must hold s.mu.
func (s *Service) openBranchDBLocked(branch string) (*chromem.DB, error) {
	root := branchRootPath(s.current.projectPath, branch)
	if err := s.ensureBranchGeneration(branch, root); err != nil {
		return nil, fmt.Errorf("ensuring branch generation at %s: %w", root, err)
	}
	db, err := newPersistentDB(root, false)
	if err != nil {
		return nil, fmt.Errorf("opening branch DB at %s: %w", root, err)
	}
	return db, nil
}

// ---------------------------------------------------------------------------
// Unified legacy-layout migration (ADR-104 §3, stage 3)
// ---------------------------------------------------------------------------

const (
	// legacyWorktreesDirName is the container of the per-worktree legacy
	// storage roots (ADR-081 layout: <viRoot>/worktrees/<name>/ held a full
	// branch-scoped store per managed tree). New attaches never create it;
	// the sweep drains it and the tree-release cleanup removes it.
	legacyWorktreesDirName = "worktrees"

	// storeMigrationStateName is the versioned completion record of the
	// unified migration (the migration marker of ADR-104 §3): staged
	// per-unit progress plus terminal skip records, atomically rewritten
	// after every unit, and flipped to done ONLY after the full recognized
	// volume settled — a partial run (crash, cancel, transient failure)
	// replays from the recorded units on the next sweep.
	storeMigrationStateName = "store_migration.json"

	// storeMigrationVersion is the state file's schema version. A state
	// written by a NEWER binary (higher version) reads as off-limits: the
	// sweep refuses to run or rewrite it (forward compatibility refused,
	// ADR-104 §3 format rule).
	storeMigrationVersion = 1

	storeMigrationStatusRunning = "running"
	storeMigrationStatusDone    = "done"
)

// sweepLeaseWait bounds the sweep's wait on an in-flight corpus build lease
// (see corpusStore.waitBuildFor). The sweep is background hygiene: losing a
// lease to a live build defers the unit to the next run — the opened view's
// own attach keeps the full corpusBuildWait drain window. A var solely so
// tests can shorten it; production never touches it.
var sweepLeaseWait = 30 * time.Second

// isViewStoreCollectionName reports whether name — a branches/ entry — is a
// view-model store identity (a shared snapshot corpus or a root overlay),
// never a legacy branch store: the sweep must not discover it as migratable
// (its lifetime is governed by the stage-5 reachability keep sets, not by
// the legacy-layout migration).
func isViewStoreCollectionName(name string) bool {
	rest, ok := strings.CutPrefix(name, collectionNamePrefix)
	return ok && (strings.HasPrefix(rest, snapshotKeyPrefix) || strings.HasPrefix(rest, overlayKeyPrefix))
}

// MigrationSkipRecord is one unit the migration willfully did NOT move into
// the new format, with the honest reason. Skipped units are terminal — the
// completion marker may still be written — and the source is always left in
// place: the record is the diagnosis, never a deletion license.
type MigrationSkipRecord struct {
	Unit   string `json:"unit"`
	Reason string `json:"reason"`
}

// StoreMigrationState is the versioned completion record of one physical
// store's unified migration (ADR-104 §3). Completed lists unit ids whose
// content reached the new format (promoted by rename, superseded by an
// already-published corpus, or nothing-to-do); Skipped carries the excluded
// volume with reasons. Unknown entries — unrecognized directories, the
// orphan root — are excluded from the recognized volume and reported as
// skip records instead of blocking completion.
type StoreMigrationState struct {
	Version    int                   `json:"version"`
	Status     string                `json:"status"`
	Completed  []string              `json:"completed,omitempty"`
	Skipped    []MigrationSkipRecord `json:"skipped,omitempty"`
	UpdatedAt  string                `json:"updated_at"`
	FinishedAt string                `json:"finished_at,omitempty"`
}

// StoreMigrationFacts supplies the environment facts the sweeps need:
// local branch names (identity source — a legacy branch store maps back to
// a live ref before its snapshot key can be derived), a branch's commit
// OID, and — for the v1→v2 chunk-record completion (chunkstore_migration.go)
// — the focused root's source-file access and the live chunker identity
// (re-chunking must reproduce the committing chunker exactly). Wired by
// the Manager over the project's workspace root; tests inject fakes.
type StoreMigrationFacts struct {
	ListBranches func(ctx context.Context) ([]string, error)
	BranchCommit func(ctx context.Context, branch string) (string, error)

	// ReadSource returns a corpus file's RAW bytes by occurrence path
	// (root-relative). Missing/mismatched sources defer their snapshot.
	ReadSource func(ctx context.Context, relPath string) ([]byte, error)
	// ChunkFn is the live chunker (the committing pipeline's own).
	ChunkFn ChunkFunc
	// HashFn is the live content hash (nil defers the hash gate — tests
	// with synthetic corpora).
	HashFn HashFunc
	// ChunkerFP is the live chunker fingerprint (the conversion gate).
	ChunkerFP string
	// MaxChunkSize / Overlap parameterize ChunkFn (the Indexer's own).
	MaxChunkSize int
	Overlap      int
}

func storeMigrationStatePath(storageRoot string) string {
	return filepath.Join(storageRoot, storeMigrationStateName)
}

// readStoreMigrationState loads the completion record; nil when absent or
// unparsable (an unreadable state is a missing one — the sweep restarts and
// replays against the filesystem truth, which is always authoritative).
func readStoreMigrationState(storageRoot string) *StoreMigrationState {
	data, err := safeio.ReadFile(storeMigrationStatePath(storageRoot))
	if err != nil {
		return nil
	}
	var st StoreMigrationState
	if json.Unmarshal(data, &st) != nil {
		return nil
	}
	return &st
}

// persist atomically rewrites the completion record after a unit settled.
func (st *StoreMigrationState) persist(storageRoot string) error {
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return safeio.WriteFileAtomic(storeMigrationStatePath(storageRoot), data, 0o644)
}

// legacyStoreUnit is one discovered legacy branch store: the collection
// directory dirName under srcRoot's branches/ (srcRoot is the project's
// shared storage root or one legacy per-worktree store root).
type legacyStoreUnit struct {
	id      string // stable unit id recorded in the state file
	srcRoot string
	dirName string
}

// discoverLegacyStoreUnits enumerates the legacy branch stores of a physical
// storage tree: every branches/ entry that is neither a view-model store
// (snapshot corpus / root overlay) nor the orphan root, in the shared root
// and inside every legacy per-worktree store. Unrecognized entries are NOT
// units — they are reported by the caller as skip records and left in place.
func discoverLegacyStoreUnits(storageRoot string) (units []legacyStoreUnit, unknown []MigrationSkipRecord) {
	record := func(srcRoot, label, name string) {
		switch {
		case name == orphanBranchRootName:
			// The orphan root is the migration's own keep-forever parking
			// lot: inventoried (the state file is the complete inventory),
			// never loaded, never deleted.
			unknown = append(unknown, MigrationSkipRecord{Unit: label + ":" + name, Reason: "orphan root (unrecoverable legacy name; kept in place)"})
		case isViewStoreCollectionName(name):
			// Corpus/overlay stores are the new format itself.
		case strings.HasPrefix(name, collectionNamePrefix):
			units = append(units, legacyStoreUnit{
				id:      label + ":" + name,
				srcRoot: srcRoot,
				dirName: name,
			})
		default:
			unknown = append(unknown, MigrationSkipRecord{Unit: label + ":" + name, Reason: "unrecognized directory under branches/ (left in place)"})
		}
	}

	if entries, err := os.ReadDir(filepath.Join(storageRoot, branchRootsDirName)); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				record(storageRoot, "branch", e.Name())
			}
		}
	}
	if wtEntries, err := os.ReadDir(filepath.Join(storageRoot, legacyWorktreesDirName)); err == nil {
		for _, w := range wtEntries {
			if !w.IsDir() {
				continue
			}
			wtRoot := filepath.Join(storageRoot, legacyWorktreesDirName, w.Name())
			label := "worktree:" + w.Name()
			sub, subErr := os.ReadDir(filepath.Join(wtRoot, branchRootsDirName))
			if subErr != nil || len(sub) == 0 {
				unknown = append(unknown, MigrationSkipRecord{
					Unit:   label,
					Reason: "legacy worktree store without branch stores (left in place; removed by tree-release cleanup)",
				})
				continue
			}
			for _, e := range sub {
				if e.IsDir() {
					record(wtRoot, label, e.Name())
				}
			}
		}
	}
	return units, unknown
}

// removeLegacyStoreArtifacts deletes one legacy branch store's on-disk legs
// (chromem root, sidecar, lexical directory, content-less marker). Called
// only when the snapshot's corpus is ALREADY PUBLISHED — the validated new
// state supersedes the duplicate — so the sweep converges the tree instead
// of leaving every superseded legacy copy on disk forever. Fail-soft.
func removeLegacyStoreArtifacts(logger *slog.Logger, srcRoot, branch string) {
	for _, victim := range []string{
		branchRootPath(srcRoot, branch),
		fileHashesPathFor(srcRoot, branch),
		lexicalIndexPath(srcRoot, branch),
		contentlessMarkerPathFor(srcRoot, branch),
	} {
		if victim == "" {
			continue
		}
		if err := os.RemoveAll(victim); err != nil {
			logger.Warn("vector index: store migration could not remove a superseded legacy artifact",
				"path", victim, "error", err)
		}
	}
}

// RunStoreMigration drives one physical store's unified legacy-layout
// migration (ADR-104 §3, stage 3) in the BACKGROUND shape the epic demands:
//
//   - staged: one unit (a legacy branch store) at a time, each settled unit
//     recorded in the versioned state file before the next begins;
//   - replay-safe: a crash, cancellation or transient failure leaves
//     status=running with the settled prefix recorded; the next sweep
//     resumes at the first unsettled unit and no document is lost (every
//     move is a rename; the completion marker is written only after the
//     full recognized volume settled);
//   - lock-disciplined: NO service or registry lock is held across the
//     sweep. Each promotion takes only the snapshot key's single-flight
//     build lease — the same lease corpus builds take — so layout
//     migration, corpus builds, overlay indexing and the sidecar/content-less
//     passes on live working stores serialize on exactly the data they
//     share, and the stale-branch pruner's view-store guards (prune.go)
//     keep the results safe from the pruning side;
//   - provable reuse: a promoted store moves by rename — zero embeddings,
//     zero chunking — and every non-reuse outcome carries a reason string
//     recorded in the state file (diagnosable rebuild fallback).
//
// It returns the sweep's final state (nil when the registry is unwired).
func (r *CorpusRegistry) RunStoreMigration(ctx context.Context, storageRoot, embeddingFP string, dims int, facts StoreMigrationFacts) *StoreMigrationState {
	if r == nil || storageRoot == "" {
		return nil
	}
	logger := r.logger
	cs := r.storeFor(storageRoot)
	if cs == nil {
		// Registry closed (app shutdown racing a late migration kick) —
		// the same nil-store guard every sibling entry point (RunHygiene,
		// StoreLedgerSnapshot) carries.
		return nil
	}

	if existing := readStoreMigrationState(storageRoot); existing != nil && existing.Version > storeMigrationVersion {
		// A newer binary's state file: refuse to run or rewrite it.
		logger.Warn("vector index: store migration state has a newer schema; sweep refused",
			"path", storeMigrationStatePath(storageRoot), "version", existing.Version)
		return existing
	}

	// One sweep per physical store at a time (managers of sibling roots may
	// kick concurrently); the cross-process guard is the app's
	// single-instance lock (ADR-075).
	if !cs.sweepMu.TryLock() {
		return readStoreMigrationState(storageRoot)
	}
	defer cs.sweepMu.Unlock()

	st := readStoreMigrationState(storageRoot)
	if st == nil || st.Version != storeMigrationVersion {
		st = &StoreMigrationState{Version: storeMigrationVersion, Status: storeMigrationStatusRunning}
	} else if st.Status == storeMigrationStatusDone {
		// The layout volume is settled. The chunk-record completion
		// (v1→v2, its own replay gates) and the store hygiene pass
		// (§5/§6) keep riding every kick: a kick is the store's cheapest
		// background slot.
		r.runChunkRecordMigration(ctx, cs, storageRoot, embeddingFP, dims, facts)
		if branches, err := facts.ListBranches(ctx); err == nil {
			cs.runHygieneLocked(logger, branches)
		}
		return st // replay gate: the completion marker certifies the tree
	}
	st.Status = storeMigrationStatusRunning
	completed := make(map[string]struct{}, len(st.Completed))
	for _, id := range st.Completed {
		completed[id] = struct{}{}
	}
	skipped := make(map[string]string, len(st.Skipped))
	for _, rec := range st.Skipped {
		skipped[rec.Unit] = rec.Reason
	}
	settle := func() {
		st.Completed = append(st.Completed[:0], slices.Sorted(maps.Keys(completed))...)
		st.Skipped = st.Skipped[:0]
		for _, unit := range slices.Sorted(maps.Keys(skipped)) {
			st.Skipped = append(st.Skipped, MigrationSkipRecord{Unit: unit, Reason: skipped[unit]})
		}
		if err := st.persist(storageRoot); err != nil {
			logger.Warn("vector index: store migration could not persist state", "error", err)
		}
	}
	abandon := func(reason string) *StoreMigrationState {
		logger.Info("vector index: store migration run interrupted; will replay", "reason", reason)
		settle()
		return st
	}

	// Phase 1 — the pre-ADR-064 multi-collection root: re-home any leftover
	// root-level collection directories into per-branch stores first, so
	// phase 2 can promote them like any other legacy branch store (a root
	// re-homed in this run is discovered below in the same run).
	moved, failed := rehomeRootLevelLegacyCollections(logger, storageRoot)
	for _, name := range moved {
		completed["root-collection:"+name] = struct{}{}
	}
	for _, name := range failed {
		skipped["root-collection:"+name] = "could not re-home the root-level collection directory (left in place)"
	}

	// Phase 2 — legacy branch stores (shared root + per-worktree roots).
	units, unknown := discoverLegacyStoreUnits(storageRoot)
	for _, rec := range unknown {
		if _, done := completed[rec.Unit]; !done {
			skipped[rec.Unit] = rec.Reason
		}
	}

	branches, err := facts.ListBranches(ctx)
	if err != nil {
		// Transient (git missing from PATH, repository mid-operation): the
		// recognized volume is NOT certifiably settled — no done marker.
		return abandon(fmt.Sprintf("listing local branches: %v", err))
	}
	slices.Sort(branches)
	branchByCollection := make(map[string]string, len(branches))
	for _, b := range branches {
		if b == "" {
			continue
		}
		if _, taken := branchByCollection[collectionName(b)]; !taken {
			// Sanitizer collisions: the first sorted branch owns the store
			// name; the corpus's per-file hash map, not the branch name, is
			// the content truth either way.
			branchByCollection[collectionName(b)] = b
		}
	}

	for _, unit := range units {
		if ctx.Err() != nil {
			return abandon("cancelled")
		}
		if _, done := completed[unit.id]; done {
			continue
		}
		branch, known := branchByCollection[unit.dirName]
		if !known {
			// The branch is gone from the clone: the store is garbage to
			// the new format, but deleting is not the sweep's call — the
			// stale-branch pruner owns absent-branch cleanup.
			skipped[unit.id] = "branch absent from the local clone (store left in place)"
			settle()
			continue
		}
		oid, oidErr := facts.BranchCommit(ctx, branch)
		if oidErr != nil {
			return abandon(fmt.Sprintf("resolving commit of branch %q: %v", branch, oidErr))
		}
		key := snapshotKey(oid)
		if key == "" {
			skipped[unit.id] = "branch resolves to no commit (store left in place)"
			settle()
			continue
		}
		if cs.publishedCompatible(key, embeddingFP, dims) {
			// The snapshot's corpus already exists (built by an attach, or
			// promoted by an earlier unit/run): the validated new state
			// supersedes this duplicate copy.
			removeLegacyStoreArtifacts(logger, unit.srcRoot, branch)
			// The branch's data now lives only in the corpus: record its
			// reference (ADR-104 stage 5) or the reachability sweep would
			// reclaim a corpus no root ever attached. Fail-soft — a missing
			// ref costs a cache-spared rebuild, never a wrong read.
			if err := writeBranchRef(storageRoot, branch, key); err != nil {
				logger.Debug("vector index: could not record the migrated branch's reference",
					"branch", branch, "snapshot", key, "error", err)
			}
			completed[unit.id] = struct{}{}
			delete(skipped, unit.id)
			settle()
			continue
		}
		started, waitCh := cs.beginBuild(key)
		if !started {
			// A live corpus build owns the key: wait a short bound, then
			// re-check; on expiry defer the unit to the next sweep.
			if !cs.waitBuildFor(waitCh, key, embeddingFP, dims, sweepLeaseWait) {
				return abandon("corpus build lease for " + key + " did not settle in time")
			}
			if cs.publishedCompatible(key, embeddingFP, dims) {
				removeLegacyStoreArtifacts(logger, unit.srcRoot, branch)
				if err := writeBranchRef(storageRoot, branch, key); err != nil {
					logger.Debug("vector index: could not record the migrated branch's reference",
						"branch", branch, "snapshot", key, "error", err)
				}
				completed[unit.id] = struct{}{}
				delete(skipped, unit.id)
				settle()
				continue
			}
			return abandon("concurrent corpus build for " + key + " finished unpublished")
		}
		migrated, reason, promErr := cs.promoteLegacyStore(unit.srcRoot, branch, key, embeddingFP, dims)
		cs.endBuild(key, promErr)
		if promErr != nil {
			// Transient I/O: the unit stays pending, nothing was recorded,
			// nothing was published — the next sweep replays it whole.
			return abandon(promErr.Error())
		}
		if migrated {
			// The promoted corpus is the branch's only index data now (the
			// legacy legs were renamed away): its reference keeps it
			// reachable until the branch's deletion (ADR-104 stage 5).
			if err := writeBranchRef(storageRoot, branch, key); err != nil {
				logger.Debug("vector index: could not record the migrated branch's reference",
					"branch", branch, "snapshot", key, "error", err)
			}
			completed[unit.id] = struct{}{}
			delete(skipped, unit.id)
			logger.Info("vector index: store migration promoted a legacy store (zero embeddings)",
				"unit", unit.id, "snapshot", key)
		} else {
			skipped[unit.id] = reason
			logger.Info("vector index: store migration left a legacy store in place",
				"unit", unit.id, "reason", reason)
		}
		settle()
	}

	if ctx.Err() != nil {
		return abandon("cancelled after the last unit")
	}
	// Every discovered unit settled (promoted, superseded, or skipped with a
	// reason) and the unknown volume is inventoried: write the completion
	// marker — the versioned done state is the only replay gate.
	st.Status = storeMigrationStatusDone
	st.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	settle()
	logger.Info("vector index: store migration complete",
		"storage", storageRoot, "promoted_or_settled", len(st.Completed), "skipped", len(st.Skipped))
	// The v1→v2 chunk-record completion and the §5/§6 hygiene pass ride the
	// same run (sweepMu is still held): the promotions this sweep made are
	// exactly the v1 corpora the completion converts.
	r.runChunkRecordMigration(ctx, cs, storageRoot, embeddingFP, dims, facts)
	cs.runHygieneLocked(logger, branches)
	return st
}
