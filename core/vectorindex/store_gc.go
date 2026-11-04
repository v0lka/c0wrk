package vectorindex

// The §5 disk ledger and the §6 hygiene sweep of ADR-104, over the §3
// chunk-record tree (chunkstore.go). Everything here is SILENT,
// switch-riding, single-flighted hygiene — the same contract as the
// stale-branch sweep (workspace.md): Debug-only logs, never blocks a
// switch, fail-soft on every I/O error, retried by the next sweep.
//
// The disk ledger counts REAL bytes from a walk of the store tree — never
// a maintained shadow counter that could drift from the filesystem truth:
//
//   - unique chunk bytes: every chunks/<aa>/<C>.chunk file ONCE, however
//     many occurrences, snapshots, branches or roots reference it (L1 is
//     the duplication boundary; the ledger cannot double-count what
//     content addressing collapsed — ADR-104 §5's invariant);
//   - per-snapshot bytes: each snapshot's occurrences.tab + table.json +
//     lexical directory;
//   - per-root overlay bytes: each root's §3 state directory;
//   - superseded bytes: whole older generations, removable wholesale.
//
// The embedding cache keeps its OWN accounting (its byte cap lives in the
// cache itself, embedding_cache.go) and lives outside the storage root —
// it is deliberately not re-counted here; the store budget governs the
// store tree.
//
// The sweep's passes, in order:
//
//  1. superseded generations — every generation older than CURRENT is an
//     atomic unit whose tables were carried into the successor; the whole
//     directory is removable once no open corpus handle was decoded from
//     it (never a partial removal: chunks and the tables pointing at them
//     go together, so no dangling reference can survive);
//  2. doomed snapshots — §3 snapshot directories no owner reaches (the
//     stage-5 reachability keep set: live root markers, surviving branch
//     refs, held handles and build leases), removed together with their
//     v1 legs (the chromem branch root and the lexical directory);
//  3. orphan chunk records — chunks of the CURRENT generation referenced
//     by no published table (a crash between chunk write and table
//     publish; a superseded table's exclusive chunks). A grace window on
//     the record's mtime keeps chunks of an in-flight writer whose table
//     flush is legitimately deferred (the completeness rule,
//     chunkstore_service.go);
//  4. disk budget — when the ledger exceeds the byte cap, LRU snapshot
//     directories that no owner pins are evicted (oldest table first)
//     until the store fits; an evicted snapshot is re-derived by the next
//     attach's corpus pass (honest cost, paid lazily).
//
// A store without a refs/ directory never recorded an owner: passes 2 and
// 4 keep everything (the stage-5 conservative posture — the first
// marker-writing attach opts the store into reachability GC); passes 1
// and 3 are unconditional (they only ever remove provably dead data).

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// orphanChunkGrace is how young a referenced-by-nothing chunk record must
// be before the sweep may collect it. Working stores land chunk records
// BEFORE their table (the §3 write ordering) and the table's flush is
// legitimately deferred by the completeness rule — anything younger than
// the grace window belongs to a live writer, not to a crash.
const orphanChunkGrace = time.Hour

// StoreLedger is the §5 disk ledger snapshot of one physical store: real
// bytes walked from the tree, charged once per identity level. It is
// recomputed by the hygiene sweep and cached for status reads (a status
// read never walks).
type StoreLedger struct {
	Generation    int              `json:"generation"`
	ChunkRecords  int              `json:"chunk_records"`
	ChunkBytes    int64            `json:"chunk_bytes"`
	ChunkOrphans  int              `json:"chunk_orphans"`
	SnapshotBytes map[string]int64 `json:"snapshot_bytes"`
	OverlayBytes  map[string]int64 `json:"overlay_bytes"`
	Snapshots     int              `json:"snapshots"`
	Overlays      int              `json:"overlays"`
	// SupersededBytes is every whole generation older than CURRENT —
	// removable wholesale by the sweep, reported separately so a budget
	// breach can be attributed.
	SupersededBytes int64  `json:"superseded_bytes"`
	TotalBytes      int64  `json:"total_bytes"`
	EvictedLastPass int    `json:"evicted_last_pass"`
	UpdatedAt       string `json:"updated_at"`
}

// treeSize walks path summing regular-file sizes (and counting them).
// Missing path is a zero tree, not an error (the sweep is fail-soft and
// the ledger reads what is there).
func treeSize(path string) (bytes int64, files int) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		full := filepath.Join(path, e.Name())
		if e.IsDir() {
			b, n := treeSize(full)
			bytes += b
			files += n
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		bytes += info.Size()
		files++
	}
	return bytes, files
}

// computeStoreLedger walks the storage root's §3 tree and charges real
// bytes per identity level. A store without a CURRENT generation has no
// §3 form yet; nil is returned (status omits the ledger).
func computeStoreLedger(storageRoot string) *StoreLedger {
	genDir, _, ok := readCurrentGeneration(storageRoot)
	if !ok {
		return nil
	}
	genName, _ := strconv.Atoi(filepath.Base(genDir))
	l := &StoreLedger{
		Generation:    genName,
		SnapshotBytes: map[string]int64{},
		OverlayBytes:  map[string]int64{},
	}
	chunkBytes, chunkFiles := treeSize(filepath.Join(genDir, storeChunksDirName))
	l.ChunkBytes = chunkBytes
	l.ChunkRecords = chunkFiles
	if entries, err := os.ReadDir(filepath.Join(genDir, storeSnapshotsDirName)); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			snapDir := filepath.Join(genDir, storeSnapshotsDirName, e.Name())
			b, _ := treeSize(snapDir)
			l.SnapshotBytes[snapshotKeyPrefix+e.Name()] = b
			l.Snapshots++
		}
	}
	if entries, err := os.ReadDir(filepath.Join(genDir, storeRootsDirName)); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			b, _ := treeSize(filepath.Join(genDir, storeRootsDirName, e.Name()))
			l.OverlayBytes[overlayKeyPrefix+e.Name()] = b
			l.Overlays++
		}
	}
	l.Snapshots = len(l.SnapshotBytes)
	l.Overlays = len(l.OverlayBytes)
	if gens, err := os.ReadDir(filepath.Join(storePath(storageRoot), storeGenerationsDirName)); err == nil {
		for _, e := range gens {
			n, convErr := strconv.Atoi(e.Name())
			if convErr != nil || !e.IsDir() || n == genName {
				continue
			}
			b, _ := treeSize(filepath.Join(storePath(storageRoot), storeGenerationsDirName, e.Name()))
			l.SupersededBytes += b
		}
	}
	l.TotalBytes = l.ChunkBytes
	for _, b := range l.SnapshotBytes {
		l.TotalBytes += b
	}
	for _, b := range l.OverlayBytes {
		l.TotalBytes += b
	}
	l.TotalBytes += l.SupersededBytes
	l.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return l
}

// storeSnapshotKeys lists the §3 snapshot directory identities under the
// generation (store-key space: snap_<oid16>).
func storeSnapshotKeys(genDir string) []string {
	entries, err := os.ReadDir(filepath.Join(genDir, storeSnapshotsDirName))
	if err != nil {
		return nil
	}
	var keys []string
	for _, e := range entries {
		if e.IsDir() && isSafeStoreKeySegment(e.Name()) {
			keys = append(keys, snapshotKeyPrefix+e.Name())
		}
	}
	return keys
}

// referencedChunkIDs collects every chunk identity the generation's
// PUBLISHED tables reference: each snapshot with its completion marker
// (table.json) and every root's overlay.tab. Unpublished snapshots (a
// crash between the chunk writes and the table publish) contribute
// nothing — their chunks are orphans by definition, subject to the grace
// window.
func referencedChunkIDs(genDir string) map[string]struct{} {
	referenced := map[string]struct{}{}
	collect := func(rows []occurrenceRow) {
		for _, row := range rows {
			for _, occ := range row.Chunks {
				referenced[occ.C] = struct{}{}
			}
		}
	}
	for _, key := range storeSnapshotKeys(genDir) {
		if !snapshotTablePublished(genDir, key) {
			continue
		}
		if rows, _, ok := readSnapshotTable(genDir, key); ok {
			collect(rows)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(genDir, storeRootsDirName)); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if rows, ok := readOverlayTable(genDir, overlayKeyPrefix+e.Name()); ok {
				collect(rows)
			}
		}
	}
	return referenced
}

// sweepOrphanChunkRecords removes the current generation's chunk records
// that no published table references and whose grace window has elapsed.
// Returns the number of collected records. The caller guarantees no
// concurrent build is mid-flight for this store (the sweepMu discipline).
func sweepOrphanChunkRecords(logger *slog.Logger, genDir string) int {
	referenced := referencedChunkIDs(genDir)
	cutoff := time.Now().Add(-orphanChunkGrace)
	collected := 0
	shards, err := chunkShardDirs(genDir)
	if err != nil {
		logger.Debug("vector store: orphan sweep could not list chunk shards", "error", err)
		return 0
	}
	for _, shard := range shards {
		ids, err := chunkIDsFromShard(shard)
		if err != nil {
			logger.Debug("vector store: orphan sweep could not list a chunk shard", "shard", shard, "error", err)
			continue
		}
		for _, c := range ids {
			if _, ok := referenced[c]; ok {
				continue
			}
			path := chunkRecordPath(genDir, c)
			info, statErr := os.Stat(path)
			if statErr != nil || !info.Mode().IsRegular() {
				continue
			}
			if info.ModTime().After(cutoff) {
				continue // a live writer's deferred flush, not a crash orphan
			}
			if err := os.Remove(path); err != nil {
				logger.Debug("vector store: orphan chunk removal deferred", "chunk", c, "error", err)
				continue
			}
			collected++
		}
	}
	return collected
}

// removeSupersededGenerations deletes every generation older than CURRENT.
// A generation is an atomic unit (its tables and their chunks go
// together), removed only when no open corpus handle was decoded from it —
// keepDirs carries exactly those (the caller reads the registry's handle
// table; the manager-free form passes nil: a disabled registry has no
// open handles). Returns the number of generations removed.
func removeSupersededGenerations(logger *slog.Logger, storageRoot string, keepDirs map[string]struct{}) int {
	genDir, _, ok := readCurrentGeneration(storageRoot)
	if !ok {
		return 0
	}
	current := filepath.Base(genDir)
	currentN, _ := strconv.Atoi(current)
	entries, err := os.ReadDir(filepath.Join(storePath(storageRoot), storeGenerationsDirName))
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		n, convErr := strconv.Atoi(name)
		if convErr != nil || n >= currentN {
			// Non-numeric debris (never ours) and the current-or-newer
			// generations stay; a HIGHER number is a concurrent advance's
			// target — its CURRENT pointer may not have landed yet.
			// Numeric comparison: "10" sorts BELOW "9" lexicographically.
			continue
		}
		dir := filepath.Join(storePath(storageRoot), storeGenerationsDirName, name)
		if _, held := keepDirs[dir]; held {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			logger.Debug("vector store: superseded generation removal deferred", "generation", name, "error", err)
			continue
		}
		removed++
		logger.Debug("vector store: removed a superseded generation", "generation", name)
	}
	return removed
}

// removeSnapshotArtifacts deletes one snapshot's every on-disk leg: the §3
// snapshot directory (tables + lexical), the v1 chromem branch root and
// the v1 lexical directory. Fail-soft; the count of removed top-level
// legs is advisory telemetry.
func removeSnapshotArtifacts(logger *slog.Logger, storageRoot, genDir, snapKey string) int {
	removed := 0
	for _, victim := range []string{
		snapshotDir(genDir, snapKey),
		branchRootPath(storageRoot, snapKey),
		lexicalIndexPath(storageRoot, snapKey),
	} {
		if victim == "" {
			continue
		}
		if err := os.RemoveAll(victim); err != nil {
			logger.Debug("vector store: snapshot leg removal deferred", "snapshot", snapKey, "path", victim, "error", err)
			continue
		}
		removed++
	}
	return removed
}

// evictionCandidate is one budget-eviction candidate snapshot.
type evictionCandidate struct {
	key       string
	createdAt time.Time
}

// evictSnapshotsUnderBudget removes LRU snapshot directories (oldest
// table first) that no owner pins until the ledger's total fits the
// budget byte cap. Pinned keys (keep) are never evicted; the eviction
// removes every leg of the snapshot (§3 tables, v1 store, lexical) — the
// next attach of any root re-derives the corpus by an honest build pass
// (ADR-104 §5). Returns the number of evicted snapshots.
func evictSnapshotsUnderBudget(logger *slog.Logger, storageRoot, genDir string, keep map[string]struct{}, budgetBytes int64, ledger *StoreLedger) int {
	if budgetBytes <= 0 || ledger == nil || ledger.TotalBytes <= budgetBytes {
		return 0
	}
	var candidates []evictionCandidate
	for _, key := range storeSnapshotKeys(genDir) {
		if _, pinned := keep[key]; pinned {
			continue
		}
		_, header, ok := readSnapshotTable(genDir, key)
		created := time.Time{}
		if ok && header != nil {
			if t, err := time.Parse(time.RFC3339, header.CreatedAt); err == nil {
				created = t
			}
		}
		if created.IsZero() {
			// An unpublished snapshot dir (no completion marker) is crash
			// debris: oldest possible and never a data loss.
			created = time.Unix(0, 0)
		}
		candidates = append(candidates, evictionCandidate{key: key, createdAt: created})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].createdAt.Before(candidates[j].createdAt) })
	evicted := 0
	for _, cand := range candidates {
		if ledger.TotalBytes <= budgetBytes {
			break
		}
		ledger.TotalBytes -= ledger.SnapshotBytes[cand.key]
		delete(ledger.SnapshotBytes, cand.key)
		ledger.Snapshots--
		removeSnapshotArtifacts(logger, storageRoot, genDir, cand.key)
		evicted++
		logger.Debug("vector store: evicted a snapshot under the disk budget",
			"snapshot", cand.key, "budget_bytes", budgetBytes)
	}
	return evicted
}

// storeChunkRecordsDoneName is the v1→v2 chunk-record migration's project
// completion marker (chunkstore_migration.go): written only after every
// published corpus of the store has its §3 tables; its presence opens the
// v1 chromem leg removal (the migration's rollback window, ADR-104 §3).
const storeChunkRecordsDoneName = "store_chunk_records.done"

func storeChunkRecordsDonePath(storageRoot string) string {
	return filepath.Join(storageRoot, storeChunkRecordsDoneName)
}

func storeChunkRecordsDone(storageRoot string) bool {
	return pathExists(storeChunkRecordsDonePath(storageRoot))
}

// removeMigratedV1Legs deletes the v1 chromem branch roots of snapshots
// whose §3 tables are published — gated on the store's chunk-record
// migration DONE marker (the rollback window, ADR-104 §3; the hygiene
// caller checks nothing else, so the gate lives here). The lexical leg
// deliberately STAYS: migrated snapshots opened from the §3 form fall
// back to the legacy lexical directory until a rebuild lands the lexical
// index inside the snapshot directory (openV2Corpus). Keys with live
// handles are skipped (Windows close-ordering; the next sweep reaps them).
func removeMigratedV1Legs(logger *slog.Logger, storageRoot, genDir string, heldKeys map[string]struct{}) int {
	if !storeChunkRecordsDone(storageRoot) {
		return 0
	}
	removed := 0
	for _, key := range storeSnapshotKeys(genDir) {
		if _, held := heldKeys[key]; held {
			continue
		}
		if !snapshotTablePublished(genDir, key) {
			continue // nothing supersedes the v1 store yet
		}
		victim := branchRootPath(storageRoot, key)
		if !pathExists(victim) {
			continue
		}
		if err := os.RemoveAll(victim); err != nil {
			logger.Debug("vector store: migrated v1 leg removal deferred", "snapshot", key, "error", err)
			continue
		}
		removed++
		logger.Debug("vector store: removed a migrated snapshot's v1 store", "snapshot", key)
	}
	return removed
}

// hygieneStoreFS is the manager-free hygiene pass: superseded generations,
// orphan chunks, and (with a live ledger) budget eviction against the
// reachability keep set. Used by the FS prune forms (a disabled registry
// has no open handles); the registry form (runHygieneLocked below) adds
// handle knowledge. Returns the fresh ledger (nil when no §3 store).
func hygieneStoreFS(logger *slog.Logger, storageRoot string, keepBranches []string, budgetBytes int64) *StoreLedger {
	genDir, _, ok := readCurrentGeneration(storageRoot)
	if !ok {
		return nil
	}
	removeSupersededGenerations(logger, storageRoot, nil)
	keep := liveRootReferences(logger, storageRoot, keepBranches, nil)
	if keep != nil {
		for _, key := range storeSnapshotKeys(genDir) {
			if _, pinned := keep[key]; pinned {
				continue
			}
			removeSnapshotArtifacts(logger, storageRoot, genDir, key)
		}
	}
	sweepOrphanChunkRecords(logger, genDir)
	ledger := computeStoreLedger(storageRoot)
	if ledger != nil && keep != nil {
		// Budget eviction runs only under a resolved keep set: a store
		// without refs/ never recorded an owner, and evicting from an
		// empty keep set would treat EVERY snapshot as unpinned (the
		// stage-5 conservative posture governs the disk budget too).
		ledger.EvictedLastPass = evictSnapshotsUnderBudget(logger, storageRoot, genDir, keep, budgetBytes, ledger)
	}
	return ledger
}

// runHygieneLocked is the registry form: the caller holds cs.sweepMu
// (single-flighted with the store migration). Adds the registry's handle
// and build-lease knowledge — the corpora an open or retained handle
// serves are never swept, their generations never removed, their v1 legs
// never reaped — and caches the fresh ledger for status reads.
func (cs *corpusStore) runHygieneLocked(logger *slog.Logger, keepBranches []string) *StoreLedger {
	if cs == nil {
		return nil
	}
	genDir, _, ok := readCurrentGeneration(cs.root)
	if !ok {
		return nil
	}
	// Handle knowledge: every held key (attached OR retained — a retained
	// handle's lexical reader points INTO the §3 snapshot directory), and
	// the generation directories open v2 handles were decoded from.
	cs.mu.Lock()
	heldKeys := make(map[string]struct{}, len(cs.handles)+len(cs.builds))
	for key := range cs.handles {
		heldKeys[key] = struct{}{}
	}
	for key := range cs.builds {
		heldKeys[key] = struct{}{}
	}
	openGens := make(map[string]struct{}, len(cs.handles))
	for _, h := range cs.handles {
		if h.v2 != nil && h.v2.gen != "" {
			openGens[h.v2.gen] = struct{}{}
		}
	}
	cs.mu.Unlock()

	removeSupersededGenerations(logger, cs.root, openGens)
	keep := liveRootReferences(logger, cs.root, keepBranches, heldKeys)
	if keep != nil {
		for _, key := range storeSnapshotKeys(genDir) {
			if _, pinned := keep[key]; pinned {
				continue
			}
			removeSnapshotArtifacts(logger, cs.root, genDir, key)
		}
	}
	sweepOrphanChunkRecords(logger, genDir)
	if storeChunkRecordsDone(cs.root) {
		removeMigratedV1Legs(logger, cs.root, genDir, heldKeys)
	}
	ledger := computeStoreLedger(cs.root)
	if ledger != nil && keep != nil {
		// Budget eviction only under a resolved keep set (the conservative
		// refs-less posture; see hygieneStoreFS).
		ledger.EvictedLastPass = evictSnapshotsUnderBudget(logger, cs.root, genDir, keep, cs.diskBudgetBytes.Load(), ledger)
	}
	cs.ledger.Store(ledger)
	return ledger
}

// RunHygiene runs the store hygiene pass for a physical store, best
// effort: single-flighted against the store migration sweep (a busy
// signal means the pass already runs — the next switch retries). Returns
// the fresh ledger when it ran, nil when skipped or absent.
func (r *CorpusRegistry) RunHygiene(storageRoot string, keepBranches []string) *StoreLedger {
	if r == nil || storageRoot == "" {
		return nil
	}
	cs := r.storeFor(storageRoot)
	if cs == nil {
		return nil
	}
	if !cs.sweepMu.TryLock() {
		return nil
	}
	defer cs.sweepMu.Unlock()
	return cs.runHygieneLocked(r.logger, keepBranches)
}

// StoreLedgerSnapshot returns the cached disk ledger of a physical store
// (nil when the store has no §3 form or no sweep has run yet).
func (r *CorpusRegistry) StoreLedgerSnapshot(storageRoot string) *StoreLedger {
	if r == nil || storageRoot == "" {
		return nil
	}
	cs := r.storeFor(storageRoot)
	if cs == nil {
		return nil
	}
	return cs.ledger.Load()
}

// SetDiskBudget installs the process-wide store disk budget (bytes; 0
// disables eviction). Applied to every store the registry owns.
func (r *CorpusRegistry) SetDiskBudget(bytes int64) {
	if r == nil {
		return
	}
	r.diskBudget.Store(bytes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cs := range r.stores {
		cs.diskBudgetBytes.Store(bytes)
	}
}
