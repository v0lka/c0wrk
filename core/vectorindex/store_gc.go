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
//  1. superseded generations — every generation other than CURRENT: lower
//     numbers are the atomic superseded units; a HIGHER number is debris
//     of a raced or crashed advance (publishCurrentGeneration is
//     monotonic, so CURRENT never names below the winner) and is removed
//     after a grace window. A generation directory is removable once no
//     open corpus handle was decoded from it (never a partial removal:
//     chunks and the tables pointing at them go together, so no dangling
//     reference can survive);
//  2. doomed snapshots — §3 snapshot directories no owner reaches (the
//     stage-5 reachability keep set: live root markers, surviving branch
//     refs, held handles and build leases), removed together with their
//     v1 legs (the chromem branch root and the lexical directory);
//  3. orphan chunk records — chunks of the CURRENT generation referenced
//     by no published table (a crash between chunk write and table
//     publish; a superseded table's exclusive chunks). A grace window on
//     the record's mtime keeps chunks of an in-flight writer whose table
//     flush is legitimately deferred (the completeness rule,
//     chunkstore_service.go). The pass is ALL-OR-NOTHING: a published
//     table that fails to READ with anything other than fs.ErrNotExist
//     makes its chunk set UNKNOWABLE, and the pass collects nothing that
//     run (chunks are sharded by content hash and shared across
//     snapshots — there is no narrower conservative unit);
//  4. disk budget — when the ledger exceeds the byte cap, LRU snapshot
//     directories that no owner pins are evicted (oldest table first)
//     until the store fits; each eviction reclaims the snapshot's §3
//     tables AND its referenced chunk records (minus the records other
//     surviving tables still reference) in the same pass, so the budget
//     is enforced on real bytes, not only on table bytes. An evicted
//     snapshot is re-derived by the next attach's corpus pass (honest
//     cost, paid lazily).
//
// Corpus builds are NOT serialized against this sweep: a build holds its
// snapshot's per-key build lease, not sweepMu, so the passes can run
// mid-build. The safety rests on write ordering plus verification:
// a build's chunk records land before its table (chunks-before-tables),
// and writeSnapshotTable verifies every referenced record still exists
// before publishing — a lost record only defers the table (never publishes
// torn) and the build replays. The grace window guards exactly pass 3
// (the orphan sweep): a build younger than the window keeps its records
// through it, and a build that outlives the window may lose early records
// there. Pass 4 (budget eviction) reclaims count-zero records with no
// mtime check, so a build sharing chunk content with an evicted snapshot
// may lose a just-written record at any age — the publish verification is
// what defers that build's table.
//
// A store without a refs/ directory never recorded an owner: passes 2 and
// 4 keep everything (the stage-5 conservative posture — the first
// marker-writing attach opts the store into reachability GC); passes 1
// and 3 are unconditional (they only ever remove provably dead data, and
// pass 3 skips itself whenever a table's read leaves its chunk set
// unknowable).

import (
	"errors"
	"io/fs"
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
	// SupersededBytes is every whole generation other than CURRENT — lower
	// numbers are removable wholesale by the sweep; a HIGHER number is
	// raced/crashed advance debris, reclaimed after the advance grace
	// window. Reported separately so a budget breach can be attributed.
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
		}
	}
	if entries, err := os.ReadDir(filepath.Join(genDir, storeRootsDirName)); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			b, _ := treeSize(filepath.Join(genDir, storeRootsDirName, e.Name()))
			l.OverlayBytes[overlayKeyPrefix+e.Name()] = b
		}
	}
	// The map lengths are the authoritative counts (each walk iteration
	// writes exactly one map entry).
	l.Snapshots = len(l.SnapshotBytes)
	l.Overlays = len(l.OverlayBytes)
	debrisBytes := int64(0)
	if gens, err := os.ReadDir(filepath.Join(storePath(storageRoot), storeGenerationsDirName)); err == nil {
		for _, e := range gens {
			n, convErr := strconv.Atoi(e.Name())
			if convErr != nil || !e.IsDir() || n == genName {
				continue
			}
			b, _ := treeSize(filepath.Join(storePath(storageRoot), storeGenerationsDirName, e.Name()))
			if n < genName {
				l.SupersededBytes += b
			} else {
				// Higher-numbered debris of a raced/crashed advance: real
				// bytes the budget must see, but NOT "removable wholesale"
				// — the advance grace window governs its reclamation, so
				// it is charged to TotalBytes without claiming the
				// superseded label.
				debrisBytes += b
			}
		}
	}
	l.TotalBytes = l.ChunkBytes
	for _, b := range l.SnapshotBytes {
		l.TotalBytes += b
	}
	for _, b := range l.OverlayBytes {
		l.TotalBytes += b
	}
	l.TotalBytes += l.SupersededBytes + debrisBytes
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

// referencedChunkIDs is retired in favor of chunkReferenceCounts (the
// sweep and the budget eviction both need per-table counts so an evicted
// snapshot reclaims only its EXCLUSIVE chunk records).

// chunkReferenceCounts counts, for every chunk identity the generation's
// READABLE tables reference, how many tables reference it (each published
// snapshot table with its completion marker, and every root's
// overlay.tab). ok is false when a table that should be readable — a
// published snapshot, or a roots/ entry holding an overlay.tab — fails to
// read with anything other than absence: its chunk set is then UNKNOWABLE
// (chunks are content-addressed and shared across snapshots and overlays,
// so no narrower conservative unit exists), and the caller must treat the
// whole pass as unresolvable: the sweep collects nothing, the budget
// evicts nothing, and the next pass retries against the intact tables.
// Unpublished snapshots (a crash between the chunk writes and the table
// publish) contribute nothing — their chunks are orphans by definition.
func chunkReferenceCounts(genDir string) (map[string]int, bool) {
	refs := map[string]int{}
	perTable := func(rows []occurrenceRow) {
		seen := map[string]struct{}{}
		for _, row := range rows {
			for _, occ := range row.Chunks {
				if _, dup := seen[occ.C]; dup {
					continue // one count per table, however many occurrences
				}
				seen[occ.C] = struct{}{}
				refs[occ.C]++
			}
		}
	}
	for _, key := range storeSnapshotKeys(genDir) {
		if !snapshotTablePublished(genDir, key) {
			continue
		}
		rows, _, ok := readSnapshotTable(genDir, key)
		if !ok {
			return nil, false // published but torn/unreadable: unknowable
		}
		perTable(rows)
	}
	entries, err := os.ReadDir(filepath.Join(genDir, storeRootsDirName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return refs, true
		}
		return nil, false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		key := overlayKeyPrefix + e.Name()
		if !pathExists(overlayTabPath(genDir, key)) {
			continue // no overlay table: nothing to reference
		}
		rows, ok := readOverlayTable(genDir, key)
		if !ok {
			return nil, false // present but torn/unreadable: unknowable
		}
		perTable(rows)
	}
	return refs, true
}

// sweepOrphanChunkRecords removes the current generation's chunk records
// that no readable table references and whose grace window has elapsed.
// Returns the number of collected records. Corpus builds are NOT
// serialized against this sweep (a build holds its per-key build lease,
// not sweepMu — a switch-riding pass may run mid-build); safety rests on
// the chunks-before-tables write ordering, this grace window, and
// writeSnapshotTable's verify-before-publish. When any table that should
// be readable fails to read, the pass collects NOTHING that run (see
// chunkReferenceCounts).
func sweepOrphanChunkRecords(logger *slog.Logger, genDir string) int {
	refs, ok := chunkReferenceCounts(genDir)
	if !ok {
		logger.Debug("vector store: orphan sweep skipped (a table failed to read; chunk set unknowable)",
			"generation", genDir)
		return 0
	}
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
			if refs[c] > 0 {
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

// advanceDebrisGrace is how old a generation directory OTHER than CURRENT
// must be before the sweep may collect it: the loser of a raced advance —
// in EITHER direction, because publishCurrentGeneration is monotonic but
// the LOSING racer may already have PUBLISHED its lower-numbered
// generation (CURRENT then names the winner above it) and its service
// state keeps writing there until its next SwitchBranch re-adopts CURRENT
// — or the debris of a crashed one. The window therefore covers every
// superseded generation, not only higher-numbered debris: its content is
// never the live truth afterwards, but a live writer may still hold it.
const advanceDebrisGrace = 24 * time.Hour

// removeSupersededGenerations deletes the generation tree's reclaimable
// debris around CURRENT: every generation other than CURRENT (an atomic
// unit — its tables and their chunks go together) once the advance grace
// window has elapsed — lower numbers are superseded legs of an era change
// (or a losing racer's already-published target), higher numbers are
// raced/crashed advance debris; publishCurrentGeneration's monotonicity
// keeps CURRENT at the winner either way. A generation is removed only
// when no open corpus handle was decoded from it — keepDirs carries
// exactly those (the caller reads the registry's handle table; the
// manager-free form passes nil: a disabled registry has no open handles).
// Returns the number of generations removed.
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
	debrisCutoff := time.Now().Add(-advanceDebrisGrace)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		n, convErr := strconv.Atoi(name)
		if convErr != nil || n == currentN {
			// Non-numeric debris (never ours) and the current generation
			// stay. Numeric comparison: "10" sorts BELOW "9"
			// lexicographically, so compare as integers.
			continue
		}
		// Every non-CURRENT generation keeps through the grace window:
		// higher-numbered debris of a raced/crashed advance AND
		// lower-numbered superseded legs alike — a raced era advance's
		// loser may have already PUBLISHED its (lower) generation and
		// still be writing §3 records there until its next re-adopt;
		// without a grace the sweep would race that live writer.
		if info, statErr := e.Info(); statErr != nil || info.ModTime().After(debrisCutoff) {
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
// removes every leg of the snapshot (§3 tables, v1 store, lexical) AND
// the snapshot's EXCLUSIVE chunk records — those no surviving table
// references — in the SAME pass, so the working ledger's stop condition
// sees real bytes (chunk records usually dominate a store's size; leaving
// them to the next sweep's orphan pass would evict every unpinned
// snapshot before the budget holds). The next attach of any root
// re-derives the corpus by an honest build pass (ADR-104 §5). Returns the
// number of evicted snapshots.
func evictSnapshotsUnderBudget(logger *slog.Logger, storageRoot, genDir string, keep map[string]struct{}, budgetBytes int64, ledger *StoreLedger) int {
	if budgetBytes <= 0 || ledger == nil || ledger.TotalBytes <= budgetBytes {
		return 0
	}
	// Per-table chunk-reference counts over every table that survives
	// this pass; an evicted snapshot's records are reclaimed only when
	// their count reaches zero (content addressing shares records across
	// snapshots and overlays). Unreadable tables abort the eviction: the
	// counts would be fiction (chunkReferenceCounts' all-or-nothing
	// posture).
	chunkRefs, ok := chunkReferenceCounts(genDir)
	if !ok {
		logger.Debug("vector store: budget eviction skipped (a table failed to read; chunk set unknowable)",
			"generation", genDir)
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
		reclaimedChunks := int64(0)
		if rows, _, ok := readSnapshotTable(genDir, cand.key); ok {
			// Drop this table's counts first, then collect the records
			// whose count reached zero — exactly the evicted snapshot's
			// exclusive chunks.
			seen := make(map[string]struct{}, len(rows))
			for _, row := range rows {
				for _, occ := range row.Chunks {
					seen[occ.C] = struct{}{}
				}
			}
			for c := range seen {
				if n := chunkRefs[c]; n > 1 {
					chunkRefs[c] = n - 1
					continue
				}
				delete(chunkRefs, c)
				path := chunkRecordPath(genDir, c)
				info, statErr := os.Stat(path)
				if statErr != nil || !info.Mode().IsRegular() {
					continue
				}
				if err := os.Remove(path); err != nil {
					logger.Debug("vector store: evicted chunk record removal deferred", "chunk", c, "error", err)
					continue
				}
				reclaimedChunks += info.Size()
			}
		}
		ledger.TotalBytes -= ledger.SnapshotBytes[cand.key]
		delete(ledger.SnapshotBytes, cand.key)
		ledger.Snapshots--
		ledger.ChunkBytes -= reclaimedChunks
		ledger.TotalBytes -= reclaimedChunks
		removeSnapshotArtifacts(logger, storageRoot, genDir, cand.key)
		evicted++
		logger.Debug("vector store: evicted a snapshot under the disk budget",
			"snapshot", cand.key, "budget_bytes", budgetBytes, "chunk_bytes_reclaimed", reclaimedChunks)
	}
	return evicted
}

// storeChunkRecordsDoneName is the v1→v2 chunk-record migration's
// store-wide completion marker (chunkstore_migration.go): written only
// after every published corpus of the store has its §3 tables. It is
// COMPLETION TELEMETRY — the v1 chromem legs are reaped per snapshot as
// soon as each snapshot's own table.json is published
// (removeMigratedV1Legs), so a store with legitimately deferred corpora
// (branches not checked out) still reclaims what converted.
const storeChunkRecordsDoneName = "store_chunk_records.done"

func storeChunkRecordsDonePath(storageRoot string) string {
	return filepath.Join(storageRoot, storeChunkRecordsDoneName)
}

func storeChunkRecordsDone(storageRoot string) bool {
	return pathExists(storeChunkRecordsDonePath(storageRoot))
}

// removeMigratedV1Legs deletes the v1 chromem branch roots of snapshots
// whose §3 tables are published — PER SNAPSHOT (the rollback window of
// each snapshot closes at its own table.json: once the §3 table is the
// certified complete form, the v1 store is derived state; re-opens of the
// snapshot serve from the table, and a damaged table self-heals by
// unpublish + cache-spared rebuild). The store-wide chunk-record
// migration marker (store_chunk_records.done) is completion telemetry
// only and deliberately does NOT gate this pass: a multi-branch store
// whose not-currently-checked-out corpora legitimately defer their
// migration must still reclaim the snapshots that DID convert. The
// lexical leg deliberately STAYS: migrated snapshots opened from the §3
// form fall back to the legacy lexical directory until a rebuild lands
// the lexical index inside the snapshot directory (openV2Corpus). Keys
// with live handles are skipped (Windows close-ordering; the next sweep
// reaps them).
func removeMigratedV1Legs(logger *slog.Logger, storageRoot, genDir string, heldKeys map[string]struct{}) int {
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
	orphans := sweepOrphanChunkRecords(logger, genDir)
	ledger := computeStoreLedger(storageRoot)
	if ledger != nil && keep != nil {
		// Budget eviction runs only under a resolved keep set: a store
		// without refs/ never recorded an owner, and evicting from an
		// empty keep set would treat EVERY snapshot as unpinned (the
		// stage-5 conservative posture governs the disk budget too).
		ledger.EvictedLastPass = evictSnapshotsUnderBudget(logger, storageRoot, genDir, keep, budgetBytes, ledger)
	}
	if ledger != nil {
		ledger.ChunkOrphans = orphans
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
	orphans := sweepOrphanChunkRecords(logger, genDir)
	// Per-snapshot v1-leg reaping (each snapshot's rollback window closes
	// at its own table.json; the store-wide marker is telemetry only).
	removeMigratedV1Legs(logger, cs.root, genDir, heldKeys)
	ledger := computeStoreLedger(cs.root)
	if ledger != nil && keep != nil {
		// Budget eviction only under a resolved keep set (the conservative
		// refs-less posture; see hygieneStoreFS).
		ledger.EvictedLastPass = evictSnapshotsUnderBudget(logger, cs.root, genDir, keep, cs.diskBudgetBytes.Load(), ledger)
	}
	if ledger != nil {
		ledger.ChunkOrphans = orphans
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
