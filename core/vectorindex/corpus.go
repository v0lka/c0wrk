package vectorindex

// The shared-corpus layer of ADR-104 stage 2 (owner/view contract,
// specs/decisions/104-content-addressed-vector-store.md §3/§4): one
// IMMUTABLE, snapshot-keyed corpus per physical store, shared read-only by
// every workspace root of the project, with per-root overlays carrying each
// root's divergence. A corpus is a branch-store-shaped directory
// (branches/<collectionName(snapKey)>/) built once by a single-flight lease
// holder through the ordinary IndexFull pipeline, PUBLISHED by an atomically
// written corpus.json (the publication gate — a store without it is not
// reusable and never served to another root), and thereafter opened read-only:
// the chromem collection is decoded ONCE per process and shared by every
// attached manager, and the lexical index is opened bleve-read-only (no
// persister/merger), so N roots of one corpus never multiply its resident
// bytes (criterion: no full copy on disk or in RAM).
//
// Snapshot identity is the commit OID ("two branches at the same commit
// share tables exactly", ADR-104 §1 L3): SnapshotKey(oid) maps every branch
// and every worktree checked out at that commit onto ONE corpus directory,
// which is what lets a second clean root reuse the corpus with a bounded
// reconciliation walk and no embed/chunk/commit pass. The per-root overlay
// (overlayRootKey) is a private branch store under the same project root;
// it is written solely by its root's manager (single-writer-per-store) and
// never shared.

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
	"github.com/v0lka/sp4rk/safeio"
)

const (
	// corpusManifestName is the publication record of a snapshot corpus,
	// written inside the corpus's branch root. It lands LAST and atomically:
	// its presence is the only proof the corpus is complete (ADR-104 §3
	// write ordering — a crash mid-build leaves an unpublished store that is
	// rebuilt, never half-served).
	corpusManifestName = "corpus.json"

	// snapshotKeyPrefix namespaces commit-keyed corpora under branches/ so a
	// snapshot identity can never collide with a legacy branch-named store
	// (branch names are user data; snapshot keys are ours).
	snapshotKeyPrefix = "snap_"

	// overlayKeyPrefix namespaces per-root overlay stores under branches/.
	// The suffix is the root fingerprint (identity.go), so a root's overlay
	// is stable across sessions and unique per physical tree.
	overlayKeyPrefix = "root_"
)

// corpusBuildWait bounds how long a non-building attacher waits for an
// in-flight corpus build (the single-flight lease's drain window). On
// expiry the attach fails closed — it never opens a parallel store. A var
// (not a const) so tests can shrink the window; the value is only read at
// wait time and never mutated outside tests.
var corpusBuildWait = 10 * time.Minute

// Sentinel errors of the corpus layer. Fail-closed attach semantics
// (ADR-104): an attach that cannot open the exact snapshot's corpus NEVER
// falls back to serving another snapshot's or root's collection. Most
// sentinels here name a PROVEN store defect the repair path owns (Repairable
// true); the two WORKSPACE sentinels — ErrCorpusNoIndexableFiles and
// ErrCorpusWalkIncomplete — describe what the TREE contains, not what the
// store holds, so no rebuild of the store can change them
// (specs/domains/workspace.md, shared corpus).
var (
	// ErrCorpusNotPublished is returned when a corpus store exists on disk
	// without a publication manifest — the caller must build (or wait for
	// the builder), never read it.
	ErrCorpusNotPublished = errors.New("vector corpus not published")
	// ErrCorpusEraMismatch is returned when a published corpus was produced
	// under a different embedding era — the caller must rebuild it.
	ErrCorpusEraMismatch = errors.New("vector corpus embedding era mismatch")
	// ErrCorpusLegMissing refuses a publication (or an open) whose corpus
	// lexical leg is not live; the wrapped cause names the root failure.
	ErrCorpusLegMissing = errors.New("vector corpus lexical leg missing")
	// ErrCorpusUnopenable marks a published corpus the read path cannot
	// open — repair (rebuild), never retry.
	ErrCorpusUnopenable = errors.New("vector corpus unopenable")
	// ErrOverlayUnopenable marks a root overlay whose store cannot be
	// opened — repair that root's private store.
	ErrOverlayUnopenable = errors.New("vector overlay unopenable")
	// ErrCorpusNoIndexableFiles marks a build that produced no indexable
	// file at all — a corpus without occurrences is not a publication. It
	// describes the WORKSPACE, not the store, so no repair can change it:
	// QuarantineScopeOf keeps it un-quarantined and the attach reports the
	// actionable reason instead of rebuilding identically.
	ErrCorpusNoIndexableFiles = errors.New("vector corpus has no indexable files")
	// ErrCorpusWalkIncomplete refuses a publication whose pass could not
	// traverse the whole workspace (the walk's per-entry gaps): an empty
	// index from such a pass does not prove the tree has no indexable files,
	// and destroying the store cannot change what the tree contains. The
	// refusal renders the first gap as TEXT — a per-entry traversal cause is
	// diagnostic, not the error's identity, so it must not reclassify the
	// refusal as a store defect.
	ErrCorpusWalkIncomplete = errors.New("vector corpus build walk was incomplete")
)

// Transient reports whether err names a TRANSIENT OS-level fault — an
// interrupted (cancelled/expired) operation, denied permissions, a full
// disk, descriptor exhaustion, an I/O error. Such a fault proves nothing
// about the store, so it is neither repairable (see Repairable) nor a
// settled verdict on the index: retry later. Everything else — the sentinel
// family above, a missing/torn record, and any unclassified failure — is
// NOT transient.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// An expired wait for someone else's build is the same class of
	// interruption as a deadline: it proves congestion, nothing about the
	// store (see errCorpusBuildOvertime).
	if errors.Is(err, errCorpusBuildOvertime) {
		return true
	}
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.ENOSPC) ||
		errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) ||
		errors.Is(err, syscall.EIO)
}

// Repairable is the ONE classifier between a PROVEN store defect and a
// transient fault: a defect (missing/torn record, unopenable leg) is healed
// by rebuilding its construct; a transient OS fault (permissions, no space,
// fd exhaustion, I/O, a cancelled operation) proves nothing about the store
// (specs/domains/workspace.md, shared corpus + §3 chunk records).
func Repairable(err error) bool {
	if err == nil {
		return false
	}
	if Transient(err) {
		return false // a transient fault proves nothing about the store
	}
	return errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, errChunkRecordTorn) ||
		errors.Is(err, ErrCorpusNotPublished) ||
		errors.Is(err, ErrCorpusEraMismatch) ||
		errors.Is(err, ErrCorpusLegMissing) ||
		errors.Is(err, ErrCorpusUnopenable) ||
		errors.Is(err, ErrOverlayUnopenable) ||
		errors.Is(err, ErrCorpusNoIndexableFiles) ||
		// A lexical lock-gate timeout (lexical.ErrLockBusy) is a PROVEN
		// stuck/foreign holder on the leg's bleve state: bbolt would wait
		// forever, the gate refused fast, and no retry can clear a holder
		// that never leaves. The ADR-120 attach rung heals it by rebuilding
		// the leg on a fresh inode while the holder keeps the orphaned old
		// one — the gate sentinel dies with the quarantined directory, so
		// the rebuilt index locks freely.
		errors.Is(err, lexical.ErrLockBusy)
}

// QuarantineScope names the ONE store a proven-defect repair may destroy.
// The scope is chosen by the STAGE that failed — AttachView tags each stage
// with its own sentinel (ErrCorpusUnopenable / ErrOverlayUnopenable), so the
// repair never guesses from an error message which store to delete
// (specs/domains/workspace.md, shared corpus).
type QuarantineScope int

const (
	// QuarantineNoStore destroys nothing: no store defect was proven (a
	// transient fault), so the publication stays intact and the attach
	// reports unavailable with the cause.
	QuarantineNoStore QuarantineScope = iota
	// QuarantineCorpusStore destroys the shared snapshot corpus: its §3
	// publication is revoked (table.json removed, which is the unpublish)
	// and its artifacts — occurrences.tab + snapshot lexical, the v1
	// chromem root (corpus.json included), the per-key lexical index, the
	// file-hash sidecar and the content-less marker — are removed so the
	// next EnsureCorpus rebuilds the snapshot from the workspace.
	QuarantineCorpusStore
	// QuarantineOverlayStore destroys ONE root's private overlay: its
	// branch store, lexical index, file-hash sidecar, content-less marker
	// and its §3 roots/<fp> state directory. Every shared corpus is left
	// untouched — other roots may still view it, and this root's overlay
	// carries no corpus bytes.
	QuarantineOverlayStore
)

// String renders the scope for logs and error messages.
func (q QuarantineScope) String() string {
	switch q {
	case QuarantineCorpusStore:
		return "corpus"
	case QuarantineOverlayStore:
		return "overlay"
	default:
		return "none"
	}
}

// errCorpusBuildBusy reports that a repair could not take the corpus build
// lease: another writer owns the store and did not settle within the drain
// window. Deliberately NOT part of the sentinel family above — it says
// nothing about the store's content, so Repairable/Transient both read it as
// an unclassified failure.
var errCorpusBuildBusy = errors.New("corpus store is owned by an unsettled build")

// errCorpusBuildOvertime reports that the bounded wait for another attacher's
// in-flight corpus build expired (corpusBuildWait). Like errCorpusBuildBusy
// it is deliberately NOT part of the sentinel family above: an expired wait
// is CONGESTION, not a verdict on the store — the builder may still be
// mid-pass and publish a perfectly healthy corpus moments later — so
// Repairable must not read it as a proven defect (the quarantine path would
// destroy exactly that publication) and Transient classifies it as the
// interruption it is: retry once the machine is less loaded.
var errCorpusBuildOvertime = errors.New("timed out waiting for a concurrent corpus build to settle")

// QuarantineScopeOf classifies an attach failure into the store a repair may
// destroy. A transient fault proves nothing about the store, so it yields
// QuarantineNoStore (the publication stays, the caller reports the cause);
// the stage sentinels decide the scope for a proven defect. An untagged
// repairable failure can only come from the ensure/build stage that precedes
// the tagged attach stages, so it is corpus-scoped.
func QuarantineScopeOf(err error) QuarantineScope {
	// A degenerate WORKSPACE is not store damage: destroying the store cannot
	// change what the tree contains, so the repair retry would repeat the
	// identical failing build (two attempts, two log lines, one useless
	// quarantine per attach). One attempt per attach — the caller reports the
	// actionable "no indexable files" / "walk was incomplete" reason instead
	// (specs/domains/workspace.md, shared corpus).
	if errors.Is(err, ErrCorpusNoIndexableFiles) || errors.Is(err, ErrCorpusWalkIncomplete) {
		return QuarantineNoStore
	}
	if !Repairable(err) {
		return QuarantineNoStore
	}
	switch {
	case errors.Is(err, ErrOverlayUnopenable):
		return QuarantineOverlayStore
	case errors.Is(err, ErrCorpusUnopenable):
		return QuarantineCorpusStore
	default:
		return QuarantineCorpusStore
	}
}

// CorpusManifest is the publication record of one snapshot corpus: the era
// it was produced under (same fields as the branch generation manifest, so
// era gating composes), the chromem collection name inside the store root
// (a migrated corpus keeps its legacy collection name), and the per-file
// content-hash map — the corpus's occurrence table lite (L2), and the
// visibility arbiter for every attached root (a corpus entry is visible to a
// root exactly while the root's own sidecar records the same hash for the
// path).
type CorpusManifest struct {
	Format      int               `json:"format"`
	Coords      string            `json:"coords"`
	EmbeddingFP string            `json:"embedding_fp"`
	Dims        int               `json:"dims"`
	ChunkerFP   string            `json:"chunker_fp"`
	IgnoreFP    string            `json:"ignore_fp"`
	Collection  string            `json:"collection"`
	Files       map[string]string `json:"files"`
	CreatedAt   string            `json:"created_at"`
}

// compatible reports whether the manifest may be read under the given
// embedding era. Chunker/ignore fingerprints do not gate reads (ADR-104 §2:
// they own re-derivation, not generation boundaries).
func (m *CorpusManifest) compatible(embeddingFP string, dims int) bool {
	return m != nil &&
		m.Format == storeFormatV2 &&
		m.Coords == coordsRelativeV1 &&
		m.EmbeddingFP == embeddingFP &&
		m.Dims == dims
}

// readCorpusManifest loads a corpus publication record; nil (no error) when
// absent or unparsable — absence means "not published", never "trust the
// directory". A var (like newPersistentDB) so a test can steer the reads of
// one snapshot deterministically across the quarantine path's two
// publication checks.
var readCorpusManifest = func(branchRoot string) *CorpusManifest {
	data, err := safeio.ReadFile(filepath.Join(branchRoot, corpusManifestName))
	if err != nil {
		return nil
	}
	var m CorpusManifest
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return &m
}

// writeCorpusManifest atomically publishes the corpus record. Callers must
// have completed every store write (documents, lexical, sidecar) first.
func writeCorpusManifest(branchRoot string, m *CorpusManifest) error {
	m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return safeio.WriteFileAtomic(filepath.Join(branchRoot, corpusManifestName), data, 0o644)
}

// snapshotKey derives the corpus store key from a commit OID (or a
// synthetic, root-owned id for non-git trees — ADR-104 §1: "the synthetic
// default/non-git snapshot is root-owned"). Every branch and worktree at
// the same commit maps onto one key.
func snapshotKey(oid string) string {
	if oid == "" {
		return ""
	}
	return snapshotKeyPrefix + sanitizeRe.ReplaceAllString(oid, "")
}

// overlayRootKey derives the per-root overlay store key from a workspace
// root: the root fingerprint (identity.go) namespaces the root's private
// store under the project's shared storage tree.
func overlayRootKey(workspaceRoot string) string {
	return overlayKeyPrefix + rootFingerprint(workspaceRoot)
}

// CorpusHandle is one shared, read-only view of a published corpus. The
// chromem collection is decoded exactly once per process per corpus and the
// handle is refcounted across every manager that attached it; the lexical
// index is opened bleve-read-only. Nothing reachable from a handle may be
// written — corpus immutability after publication is what makes per-root
// isolation structural (a diverging root never mutates what other roots
// read).
type CorpusHandle struct {
	key        string
	manifest   *CorpusManifest
	collection *chromem.Collection
	lexical    lexical.Index
	store      *corpusStore

	// v2 is the §3-backed view (ADR-104 §3 read path): non-nil when the
	// snapshot's occurrence table served the open — then collection is nil
	// (no chromem decode: the tables and chunk records ARE the corpus) and
	// every read leg branches through chunkstore_corpus.go. Exactly one of
	// collection/v2 carries the corpus; manifest and lexical serve both.
	v2 *v2Corpus

	// retired marks a handle whose store left the registry
	// (CorpusRegistry.DeleteStore / Close): the LAST release closes it
	// instead of retaining it in the residency LRU, so a deleted store's
	// corpora can never come back through the residency back door. Set and
	// read only under the store's mutex.
	retired bool
}

// Key returns the handle's snapshot store key.
func (h *CorpusHandle) Key() string { return h.key }

// Manifest returns the corpus's publication record (do not mutate).
func (h *CorpusHandle) Manifest() *CorpusManifest { return h.manifest }

// CorpusCount returns the corpus collection's document count (0 when the
// handle is nil). A §3-backed handle counts its chunk occurrences.
func (h *CorpusHandle) CorpusCount() int {
	if h == nil {
		return 0
	}
	if h.v2 != nil {
		return h.v2.count()
	}
	if h.collection == nil {
		return 0
	}
	return h.collection.Count()
}

// Release drops one reference; the last release either closes the handle
// (retention disabled / store retired) or retains it in the owner's
// residency LRU under the registry's count/byte budget — see
// corpus_residency.go.
func (h *CorpusHandle) Release() {
	if h == nil || h.store == nil {
		return
	}
	h.store.release(h)
}

// corpusBuild is the single-flight record of one in-flight corpus build.
type corpusBuild struct {
	done chan struct{}
	err  error
}

// corpusStore is the per-physical-store corpus coordinator: the handle
// cache (one decode per corpus per process), the build leases, and the
// RESIDENCY LRU of ADR-104 §5 (retention of zero-reference handles, bounded
// by count and bytes). The handle map is the ONLY cache — the lru list just
// orders its zero-ref entries, so there is no parallel branch-keyed cache to
// drift against.
type corpusStore struct {
	root   string
	logger *slog.Logger
	// reg is the owning registry (residency knobs' source of truth and the
	// memory-free seam holder); nil only for hand-built stores in tests.
	reg     *CorpusRegistry
	mu      sync.Mutex
	handles map[string]*CorpusHandle
	refs    map[string]int
	// lru orders the RETAINED (zero-ref) handles, oldest first (lru[0] is
	// the next eviction candidate). An attached handle is never listed: the
	// refcount is the read lease (ADR-104 §5 reader pinning) — attachment
	// removes the key, the last release re-appends it, so a live view is
	// structurally un-evictable and no second lock is needed.
	lru []string
	// decodes counts real corpus decodes per key (test introspection: the
	// residency contract is "a re-attach of a resident corpus never
	// decodes").
	decodes map[string]int
	builds  map[string]*corpusBuild

	// sweepMu single-flights the unified legacy-layout migration sweep
	// (layout.go RunStoreMigration): at most one sweep walks a physical
	// store at a time; it is NEVER held while any other lock is taken, and
	// the sweep itself holds no service/registry lock across its run.
	sweepMu sync.Mutex

	// resCapacity / resBudget bound the retained set (0/0 = retention
	// disabled — a zero-ref handle closes immediately, the pre-residency
	// behavior). Copied from the registry by SetResidency/storeFor under
	// the store lock; never read through reg on the hot path.
	resCapacity int
	resBudget   int64

	// diskBudgetBytes is the store-tree disk budget (0 = eviction off,
	// store_gc.go). Copied from the registry by SetDiskBudget/storeFor.
	diskBudgetBytes atomic.Int64

	// ledger caches the last hygiene pass's §5 disk ledger snapshot for
	// status reads (a status read never walks the tree).
	ledger atomic.Pointer[StoreLedger]
}

// residencyEnabled reports whether zero-ref handles are retained at all:
// both bounds zero (the default) disables retention entirely.
func (cs *corpusStore) residencyEnabled() bool {
	return cs.resCapacity > 0 || cs.resBudget > 0
}

// publishedCompatible reports whether key's corpus has a publication
// record readable UNDER THE GIVEN EMBEDDING ERA — the v1 corpus.json
// manifest OR the §3 snapshot table (either is a complete publication;
// the §3 table is the canonical form, corpus.json the legacy gate kept
// until the chunk-record migration removes the v1 store). BOTH legs are
// era-gated: a stale-era record of either form is NOT a publication (the
// corpus must rebuild under the current era — an era-blind v1 leg would
// block the rebuild forever while every open fails with
// ErrCorpusEraMismatch, wedging the snapshot).
func (cs *corpusStore) publishedCompatible(key, embeddingFP string, dims int) bool {
	if cs == nil || key == "" {
		return false
	}
	if gen, m, ok := readCurrentGeneration(cs.root); ok && m.compatible(embeddingFP, dims) && snapshotTablePublished(gen, key) {
		// The §3 open resolves the snapshot's lexical leg from the snapshot
		// directory, falling back to the legacy per-key path (the shape a
		// migrated snapshot keeps). Require ONE of them: a table whose
		// lexical leg vanished is not servable, and reporting it published
		// would wedge the snapshot behind a failing open with no rebuild —
		// the same dead end the v1 leg below closes.
		return pathExists(filepath.Join(snapshotDir(gen, key), "lexical")) ||
			pathExists(lexicalIndexPath(cs.root, key))
	}
	m := readCorpusManifest(branchRootPath(cs.root, key))
	if m == nil || !m.compatible(embeddingFP, dims) {
		return false
	}
	// A publication record is only half the contract: every v1 open ALSO
	// opens the corpus lexical leg fail-closed (corpusStore.open). A
	// corpus.json whose lexical leg is gone is therefore not a publication
	// — nothing can serve it — and reporting it as published wedges the
	// snapshot forever: EnsureCorpus returns early, every attach fails with
	// "opening corpus lexical index", and no reindex can help because the
	// manager has no collection to build into. Treat the absent leg as
	// UNPUBLISHED so the ordinary repair path runs (promotion refuses the
	// occupied target, the store is reset and rebuilt cache-spared). This
	// mirrors the §3 rule that PROVEN damage unpublishes the snapshot while
	// a transient fault leaves the publication intact.
	return pathExists(lexicalIndexPath(cs.root, key))
}

// beginBuild acquires key's single-flight build lease: the first caller
// becomes the builder (started=true); concurrent callers receive the lease's
// done channel and must wait for it (bounded by the caller), then re-check
// Published — a failed build leaves nothing published and the attach fails
// closed. There is never a second writer to one store.
func (cs *corpusStore) beginBuild(key string) (started bool, wait <-chan struct{}) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if b, ok := cs.builds[key]; ok {
		return false, b.done
	}
	b := &corpusBuild{done: make(chan struct{})}
	if cs.builds == nil {
		cs.builds = make(map[string]*corpusBuild)
	}
	cs.builds[key] = b
	return true, b.done
}

// endBuild settles a build lease: the error is handed to waiters through the
// closed channel and the lease is dropped (a later attacher may retry).
func (cs *corpusStore) endBuild(key string, err error) {
	cs.mu.Lock()
	b := cs.builds[key]
	delete(cs.builds, key)
	cs.mu.Unlock()
	if b != nil {
		b.err = err
		close(b.done)
	}
}

// liveBuildLeases returns a READ-ONLY snapshot of the store's in-flight
// build lease keys (the snapshot keys a corpus build or the chunk-record
// migration is writing right now). beginBuild/endBuild already maintain the
// lease map; this is its consumer-side view, a detached copy taken under
// cs.mu so the caller never observes the map after the unlock. It is the
// hygiene sweep's chunk-record-reap gate (store_gc.go): a build's records
// land BEFORE its table (the chunks-before-tables ordering) and carry no
// owner marker of their own, so the store-wide lease set is the only sound
// granularity — chunk records are content-addressed and shared across
// snapshots and overlays, and no narrower unit exists.
func (cs *corpusStore) liveBuildLeases() map[string]struct{} {
	if cs == nil {
		return nil
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.builds) == 0 {
		return nil
	}
	leases := make(map[string]struct{}, len(cs.builds))
	for key := range cs.builds {
		leases[key] = struct{}{}
	}
	return leases
}

// waitBuildOutcome blocks until the given in-flight build settles and tells
// the two failure modes apart: settled reports whether the build settled
// within the bound (false = the wait expired), published whether its outcome
// is an era-compatible publication. Callers that must never destroy a
// possibly-healthy concurrent build (the quarantine path) need the
// distinction; plain waiters only need published.
func (cs *corpusStore) waitBuildOutcome(waitCh <-chan struct{}, key, embeddingFP string, dims int, bound time.Duration) (settled, published bool) {
	select {
	case <-waitCh:
		return true, cs.publishedCompatible(key, embeddingFP, dims)
	case <-time.After(bound):
		return false, false
	}
}

// waitBuildFor blocks until the given in-flight build settles; ok is false
// when the build expired (the caller's bound) or failed to publish under the
// given era. The unified store migration uses a short one: a sweep that
// loses the lease to a live corpus build defers the unit to its next run
// instead of parking for the attach path's full drain window — the sweep is
// background hygiene, never a blocker of the opened view.
func (cs *corpusStore) waitBuildFor(waitCh <-chan struct{}, key, embeddingFP string, dims int, bound time.Duration) bool {
	settled, published := cs.waitBuildOutcome(waitCh, key, embeddingFP, dims, bound)
	return settled && published
}

// v1FallbackErr joins a v1-fallback verdict with the §3 open failure that
// preceded it (ADR-120 D7: the §3 reason is never erased). Both causes stay
// matchable (errors.Is/As — Repairable/Transient read either leg) and named
// in the text; without a §3 failure the verdict stands byte-for-byte alone
// (legacy-only stores never grow a phantom §3 mention).
func v1FallbackErr(v2Err, v1Err error) error {
	if v2Err == nil {
		return v1Err
	}
	return fmt.Errorf("§3 open failed (%w); v1 fallback: %w", v2Err, v1Err)
}

// open returns the shared handle for key's published corpus, fail-closed:
// unpublished, foreign-era, or unreadable corpora return an error naming the
// snapshot — never another snapshot's data. The chromem decode is cached and
// refcounted; the lexical index is opened read-only. A cache hit — attached
// OR retained by the residency LRU — re-serves the SAME handle object: no
// decode, no lexical reopen (the A→B→A contract of ADR-104 §5).
func (cs *corpusStore) open(ctx context.Context, key string, ef chromem.EmbeddingFunc, embeddingFP string, dims int) (*CorpusHandle, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if h, ok := cs.handles[key]; ok {
		if h.manifest != nil && !h.manifest.compatible(embeddingFP, dims) {
			// A retained (zero-ref, residency LRU) or still-referenced
			// handle of a PREVIOUS embedding era must never be re-served:
			// the cache is keyed by snapshot key only, and a cache hit
			// would bypass BOTH era gates the fresh path enforces — silent
			// wrong-era vectors and a stale visibility map. Evictable
			// handles are dropped here and the open continues through the
			// fresh gated path; an attached handle (refs > 0) cannot be
			// closed under its reader, so the open fails closed with the
			// era error until the old reference drains.
			if cs.refs[key] > 0 {
				return nil, fmt.Errorf("%w: %s (fp %q dims %d)", ErrCorpusEraMismatch, key, embeddingFP, dims)
			}
			delete(cs.handles, key)
			cs.detachLRULocked(key)
			cs.closeHandleLocked(h)
		} else {
			cs.refs[key]++
			cs.detachLRULocked(key) // attached again — never an eviction candidate
			return h, nil
		}
	}
	// §3 read path first (ADR-104 §3): a published occurrence table opens
	// the corpus without any chromem decode. Failure with an existing v1
	// corpus falls back to the v1 open (the migration will repair); failure
	// without one propagates (fail-closed attach). The current generation
	// resolves ONCE for both paths: the v1 fallback shares the §3 open's
	// two-legged lexical resolve, and its snapshot-directory leg lives
	// under this generation. v2Err survives the branch (ADR-120 D7: the §3
	// reason is never erased) — every v1-fallback verdict below joins it,
	// so a failed fallback matches BOTH causes and a successful one has
	// just logged what it papered over.
	var v2Err error
	gen, genManifest, genOK := readCurrentGeneration(cs.root)
	// The snapshot-directory lexical leg the fallback below may resolve is
	// servable only under a CURRENT-era generation: a foreign-era CURRENT
	// (the fail-soft shape — ensureStoreGeneration refused the advance and
	// the legacy shape keeps serving) carries snapshot legs of the OLD era,
	// and preferring one over the legacy per-key leg would serve a
	// stale-era lexical index beside the fresh corpus. The §3 branch rides
	// the same compatibility gate, and publishedCompatible era-gates both
	// of its publication legs — this is the third wheel of that discipline.
	genLex := ""
	if genOK && genManifest.compatible(embeddingFP, dims) {
		genLex = gen
		v2, err := openV2Corpus(gen, key, genManifest, cs.logger)
		switch {
		case v2 != nil:
			if cs.decodes == nil {
				cs.decodes = make(map[string]int)
			}
			cs.decodes[key]++
			h := &CorpusHandle{
				key:      key,
				manifest: v2.corpusManifest(genManifest),
				lexical:  v2.lexical,
				v2:       v2,
				store:    cs,
			}
			if cs.handles == nil {
				cs.handles = make(map[string]*CorpusHandle)
				cs.refs = make(map[string]int)
			}
			cs.handles[key] = h
			cs.refs[key] = 1
			return h, nil
		case err != nil:
			v2Err = err
			cs.logger.Warn("vector corpus: §3 open failed; falling back to the v1 store",
				"snapshot", key, "error", err)
		}
	}
	branchRoot := branchRootPath(cs.root, key)
	m := readCorpusManifest(branchRoot)
	if m == nil {
		return nil, v1FallbackErr(v2Err, fmt.Errorf("%w: %s", ErrCorpusNotPublished, key))
	}
	if !m.compatible(embeddingFP, dims) {
		return nil, v1FallbackErr(v2Err, fmt.Errorf("%w: %s (fp %q dims %d)", ErrCorpusEraMismatch, key, m.EmbeddingFP, m.Dims))
	}
	db, err := newPersistentDB(branchRoot, false)
	if err != nil {
		return nil, v1FallbackErr(v2Err, fmt.Errorf("opening corpus DB for %s: %w", key, err))
	}
	name := m.Collection
	if name == "" {
		name = collectionName(key)
	}
	col := db.GetCollection(name, ef)
	if col == nil {
		return nil, v1FallbackErr(v2Err, fmt.Errorf("corpus collection %q missing for %s", name, key))
	}
	// The SAME two-legged resolve the §3 open applies
	// (openCorpusLexicalReadOnly), era-gated like every other leg:
	// snapshot directory first (genLex — empty, legacy-only, when the
	// current generation is foreign-era), legacy per-key path second. A §3
	// failure that falls back here may have just unpublished a table whose
	// only lexical leg is the snapshot directory's (a native build damaged
	// by a torn chunk record), and the publication gate
	// (publishedCompatible) already accepts either leg as servable —
	// resolving the legacy path alone would structurally ENOENT exactly
	// the snapshots this fallback exists to serve.
	lex, lexErr := openCorpusLexicalReadOnly(genLex, cs.root, key)
	if lexErr != nil {
		return nil, v1FallbackErr(v2Err, fmt.Errorf("opening corpus lexical index for %s: %w", key, lexErr))
	}
	if cs.decodes == nil {
		cs.decodes = make(map[string]int)
	}
	cs.decodes[key]++
	h := &CorpusHandle{key: key, manifest: m, collection: col, lexical: lex, store: cs}
	if cs.handles == nil {
		cs.handles = make(map[string]*CorpusHandle)
		cs.refs = make(map[string]int)
	}
	cs.handles[key] = h
	cs.refs[key] = 1
	return h, nil
}

// detachLRULocked removes key from the retention list (a handle became
// attached again). The caller must hold cs.mu.
func (cs *corpusStore) detachLRULocked(key string) {
	for i, k := range cs.lru {
		if k == key {
			cs.lru = append(cs.lru[:i], cs.lru[i+1:]...)
			return
		}
	}
}

// release drops a handle reference. While references remain, the handle
// stays live and un-evictable (the refcount is the read lease). The LAST
// release either closes the handle (retention disabled, or the store was
// retired) or retains it in the residency LRU, evicting the oldest eligible
// handles beyond the configured count/byte bounds (corpus.go's park-mechanic
// posture: enforcement right at the state change, memory freed through the
// seam outside the lock).
func (cs *corpusStore) release(h *CorpusHandle) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if n := cs.refs[h.key]; n > 1 {
		cs.refs[h.key] = n - 1
		return
	}
	delete(cs.refs, h.key)
	if h.retired || !cs.residencyEnabled() {
		// Closed for good: drop the cache entry so the next open re-decodes.
		delete(cs.handles, h.key)
		cs.closeHandleLocked(h)
		return
	}
	// Retained: the handle STAYS in the handles cache (a later open finds
	// it and re-serves the same object — no decode, no lexical reopen);
	// only its lru listing marks it eligible for eviction.
	cs.lru = append(cs.lru, h.key)
	cs.enforceResidencyLocked()
}

// closeHandleLocked releases a handle's OS-level residents (the read-only
// lexical index; the chromem decode becomes garbage with the handle). The
// caller must hold cs.mu and must already have dropped the handle from the
// maps.
func (cs *corpusStore) closeHandleLocked(h *CorpusHandle) {
	if h.lexical != nil {
		if err := h.lexical.Close(); err != nil {
			cs.logger.Warn("vector corpus: closing read-only lexical index failed", "key", h.key, "error", err)
		}
	}
}

// promoteLegacyStore promotes a legacy branch-keyed store (ADR-064 layout)
// living under srcRoot — the project's shared storage root or a released
// worktree's legacy storage root — into the snapshot-keyed corpus for key,
// WITHOUT copying bytes and WITHOUT a single embedding: directory renames
// plus a corpus.json derived from the legacy sidecar's hash column (both
// sidecar grammars — v6 envelope and bare map — feed the same hash column).
// The publication manifest lands LAST (see corpusManifestName), so an
// interrupted promotion is simply not published and the caller rebuilds or
// replays.
//
// The reusable-mapping gates, in order, each with an honest reason string
// for the caller's fallback diagnostics (the unified migration records them
// verbatim, layout.go):
//
//   - key already published → nothing to do ("already published");
//   - no source store for branch under srcRoot → ("source store absent");
//   - legacy branch generation manifest ABSENT → the era is derived on this
//     first touch, gated by a sampled document's embedding width (a width
//     mismatch or an undecodable store refuses with its own reason) and
//     stamped with the current era (stores written by older binaries
//     predate the manifest writer, which first runs inside a focused-root
//     SwitchBranch — without the stamp the promotion would be unreachable
//     for exactly the pre-ADR-104 stores it exists for; from then on the
//     durable record gates every later era change through the ordinary
//     generation reset);
//   - legacy branch generation manifest names a foreign embedding era →
//     ("generation manifest names a foreign embedding era") — nothing
//     trustworthy to promote, embeddings of another era must never be
//     served (ADR-104 §2);
//   - no usable sidecar hash map → ("no usable sidecar hash map") — a
//     corpus without a per-file hash map cannot arbitrate visibility;
//   - content-less marker absent → ("content-less marker absent") — the
//     store predates ADR-049's commit-time strip and holds fat documents
//     whose content a read-only shared decode would pin in every attached
//     process's RAM; the rebuild fallback re-derives content-less documents
//     through the embedding cache (inference-spared).
//
// Reports migrated=false with a non-empty reason when there is nothing
// promotable (the caller builds a fresh corpus), and err for I/O failures
// (transient: the unified migration replays such units on its next run).
// Only the store's current process may run this (the app's single-instance
// lock, ADR-075, is the cross-process guard), and only under the store's
// build lease for key.
// chromemDocumentProbe mirrors the exported fields of chromem-go's persisted
// Document gob (gob matches fields by name — the same trick
// chromemCollectionMetadata uses in layout.go) so the promotion can inspect a
// legacy store's embedding width WITHOUT opening the store as a chromem DB.
type chromemDocumentProbe struct {
	ID        string
	Metadata  map[string]string
	Embedding []float32
	Content   string
}

// sampleLegacyEmbeddingWidth decodes ONE document from the legacy branch
// store's collection directories and returns its embedding width. ok=false
// when no document file decodes (the store is empty, unreadable or torn) —
// the caller treats that as an unverifiable era and refuses the stamp. The
// collection metadata file is skipped by name, exactly as chromem's own open
// differentiates it from document files.
func sampleLegacyEmbeddingWidth(branchRoot string) (int, bool) {
	cols, err := os.ReadDir(branchRoot)
	if err != nil {
		return 0, false
	}
	for _, col := range cols {
		if !col.IsDir() {
			continue
		}
		entries, readErr := os.ReadDir(filepath.Join(branchRoot, col.Name()))
		if readErr != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || e.Name() == chromemMetadataFileName || !strings.HasSuffix(e.Name(), ".gob") {
				continue
			}
			width, ok := decodeEmbeddingWidth(filepath.Join(branchRoot, col.Name(), e.Name()))
			if ok {
				return width, true
			}
		}
	}
	return 0, false
}

// decodeEmbeddingWidth gob-decodes one document file and returns its
// embedding width; ok=false on any decode failure (the caller tries the
// next file — a torn write must not veto an otherwise verifiable store).
func decodeEmbeddingWidth(path string) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close() //nolint:errcheck // read-only probe close
	var d chromemDocumentProbe
	if err := gob.NewDecoder(f).Decode(&d); err != nil || len(d.Embedding) == 0 {
		return 0, false
	}
	return len(d.Embedding), true
}

func (cs *corpusStore) promoteLegacyStore(srcRoot, branch, key, embeddingFP string, dims int) (migrated bool, reason string, err error) {
	if key == "" || branch == "" || branch == key {
		return false, "no source store for this snapshot key", nil
	}
	if cs.publishedCompatible(key, embeddingFP, dims) {
		return false, "already published", nil
	}
	legacyRoot := branchRootPath(srcRoot, branch)
	if !dirHasEntries(legacyRoot) && !pathExists(fileHashesPathFor(srcRoot, branch)) {
		return false, "source store absent", nil
	}
	legacyManifest := readBranchManifest(legacyRoot)
	if legacyManifest == nil {
		// Stores written by older binaries carry no manifest (the writer
		// first runs inside a focused-root SwitchBranch), so the era gate
		// below would reject every pre-ADR-104 store as "absent" and the
		// promotion would never fire for exactly its targets. First touch
		// derives the era: the sidecar hash map and content-less marker
		// gates below certify a completed content-less store, and the
		// store predates the era record, so this stamps the CURRENT era —
		// the durable record exists from then on, and any LATER era change
		// resets the store through the ordinary generation gate instead of
		// relying on this adoption. The chunker fingerprint is derived from
		// the sidecar below; the ignore fingerprint stays unrecorded (the
		// v1→v2 migration gates only recorded values).
		//
		// One dimension of the era IS verifiable in place — the embedding
		// width of the store's own documents — and the stamp is refused
		// when it does not match the current era: a model swap that
		// changed dimensions must never promote mismatched-width vectors
		// as current-era. A same-width model swap stays indistinguishable
		// (the store predates era records entirely); the adoption accepts
		// it deliberately, same posture as firstGenerationAdoption.
		//
		// The chunker fingerprint is DERIVED from the sidecar's own
		// entries when every recorded entry agrees (the corpus's chunks
		// really were produced by that chunker, so the corpus-current
		// adoption gate and the v1→v2 migration gate compare real values
		// instead of an always-mismatching "" that would silently
		// no-op the promotion); disagreeing or absent records stay "" —
		// each downstream gate then applies its own documented semantics.
		// The ignore fingerprint stays unrecorded (the sidecar carries
		// none).
		width, ok := sampleLegacyEmbeddingWidth(legacyRoot)
		if !ok {
			return false, "generation manifest absent and no document decodes to verify the embedding width", nil
		}
		if width != dims {
			return false, "generation manifest absent and the sampled embedding width does not match the current era", nil
		}
		legacyManifest = &branchGenerationManifest{
			Format:      storeFormatV2,
			Coords:      coordsRelativeV1,
			EmbeddingFP: embeddingFP,
			Dims:        dims,
			ChunkerFP:   legacySidecarChunkerFP(srcRoot, branch),
		}
		if err := writeBranchManifest(legacyRoot, legacyManifest); err != nil {
			return false, "", fmt.Errorf("stamping generation manifest onto legacy branch store %s: %w", branch, err)
		}
	}
	if !legacyManifest.compatible(embeddingFP, dims) {
		// A manifest naming a foreign era: nothing trustworthy to promote.
		return false, "generation manifest names a foreign embedding era", nil
	}
	files, ok := legacySidecarHashMap(srcRoot, branch)
	if !ok || len(files) == 0 {
		// A corpus without a per-file hash map cannot arbitrate visibility.
		return false, "no usable sidecar hash map", nil
	}
	if !pathExists(contentlessMarkerPathFor(srcRoot, branch)) {
		// Pre-ADR-049 documents carry their content; a read-only shared
		// decode would pin it in RAM in every attached process.
		return false, "content-less marker absent", nil
	}
	if !pathExists(lexicalIndexPath(srcRoot, branch)) {
		// The READ contract, not a nicety: corpusStore.open opens the
		// corpus lexical leg fail-closed, so promoting a store without one
		// publishes a corpus.json no attach can ever serve — exactly the
		// wedge that strands a project at "Index unavailable" with no
		// collection to rebuild into. Refuse the promotion instead: the
		// caller resets the target and builds a fresh corpus, cache-spared.
		return false, "lexical index leg absent", nil
	}

	snapRoot := branchRootPath(cs.root, key)
	if err := ensureRealDir(filepath.Dir(snapRoot), 0o750); err != nil {
		return false, "", fmt.Errorf("creating corpus parent for %s: %w", key, err)
	}
	// The lexical leg renames into the destination store's lexical/ tree,
	// which a store that never indexed in legacy mode may not have yet.
	if err := ensureRealDir(filepath.Dir(lexicalIndexPath(cs.root, key)), 0o750); err != nil {
		return false, "", fmt.Errorf("creating lexical corpus parent for %s: %w", key, err)
	}
	// Never overwrite an existing target leg: an existing corpus for the
	// snapshot (or the debris of an interrupted promotion) is left exactly
	// as it is and the source stays in place — the sweep reports the
	// collision, the attach path's build decides the store's fate.
	for _, leg := range []string{snapRoot, fileHashesPathFor(cs.root, key), lexicalIndexPath(cs.root, key), contentlessMarkerPathFor(cs.root, key)} {
		if _, statErr := os.Stat(leg); statErr == nil {
			return false, "promotion target already exists", nil
		}
	}
	// Renames are individually atomic; publication lands last.
	moves := []struct{ from, to string }{
		{legacyRoot, snapRoot},
		{fileHashesPathFor(srcRoot, branch), fileHashesPathFor(cs.root, key)},
		{lexicalIndexPath(srcRoot, branch), lexicalIndexPath(cs.root, key)},
		{contentlessMarkerPathFor(srcRoot, branch), contentlessMarkerPathFor(cs.root, key)},
	}
	for _, mv := range moves {
		if mv.from == "" || mv.to == "" || mv.from == mv.to {
			continue
		}
		if _, statErr := os.Stat(mv.from); statErr != nil {
			// Every leg above is validated present before this loop, so an
			// absent source here is a race (something removed it between the
			// checks and the move), never a partially written store. Skipping
			// keeps the promotion from failing on a harmless leftover while
			// the validated contract legs remain mandatory.
			continue
		}
		if err := os.Rename(mv.from, mv.to); err != nil {
			return false, "", fmt.Errorf("renaming %s into the corpus for %s: %w", mv.from, key, err)
		}
	}
	if err := writeCorpusManifest(snapRoot, &CorpusManifest{
		Format:      storeFormatV2,
		Coords:      coordsRelativeV1,
		EmbeddingFP: embeddingFP,
		Dims:        dims,
		ChunkerFP:   legacyManifest.ChunkerFP,
		IgnoreFP:    legacyManifest.IgnoreFP,
		Collection:  collectionName(branch),
		Files:       files,
	}); err != nil {
		return false, "", fmt.Errorf("publishing migrated corpus for %s: %w", key, err)
	}
	cs.logger.Info("vector corpus: promoted legacy branch store into the shared corpus",
		"branch", branch, "snapshot", key, "files", len(files), "source_root", srcRoot)
	return true, "", nil
}

// legacySidecarChunkerFP derives the legacy store's chunker fingerprint
// from its sidecar entries: the single distinct non-empty value every
// recorded entry agrees on, or "" when the entries disagree or carry no
// fingerprint (legacy/intermediate formats). "" keeps both downstream
// consumers honest in their own way — the v1→v2 migration treats it as
// ungated, the corpus-current adoption gate treats it as a mismatch —
// rather than guessing a configuration the store never recorded.
func legacySidecarChunkerFP(storageRoot, branch string) string {
	data, err := safeio.ReadFile(fileHashesPathFor(storageRoot, branch))
	if err != nil {
		return ""
	}
	var envelope struct {
		Version int               `json:"version"`
		Root    string            `json:"root"`
		Entries map[string]string `json:"entries"`
	}
	entries := envelope.Entries
	if json.Unmarshal(data, &envelope) != nil || entries == nil {
		var bare map[string]string
		if json.Unmarshal(data, &bare) != nil {
			return ""
		}
		entries = bare
	}
	single := ""
	for _, entry := range entries {
		fp := fileHashEntryChunkerFP(entry)
		if fp == "" {
			continue
		}
		if single == "" {
			single = fp
		} else if single != fp {
			return "" // disagreeing records: nothing honest to stamp
		}
	}
	return single
}

// legacySidecarHashMap extracts the rel-path → content-hash map from a
// legacy sidecar file (v6 envelope or bare map). ok=false when the file is
// absent or holds no usable entries.
func legacySidecarHashMap(storageRoot, branch string) (map[string]string, bool) {
	data, err := safeio.ReadFile(fileHashesPathFor(storageRoot, branch))
	if err != nil {
		return nil, false
	}
	var envelope struct {
		Version int               `json:"version"`
		Root    string            `json:"root"`
		Entries map[string]string `json:"entries"`
	}
	if json.Unmarshal(data, &envelope) == nil && envelope.Entries != nil {
		return hashColumn(envelope.Entries)
	}
	var bare map[string]string
	if json.Unmarshal(data, &bare) != nil {
		return nil, false
	}
	return hashColumn(bare)
}

// hashColumn maps sidecar entries to their hash field, keeping only
// well-formed values.
func hashColumn(entries map[string]string) (map[string]string, bool) {
	out := make(map[string]string, len(entries))
	for k, v := range entries {
		if h := fileHashEntryHash(v); h != "" {
			out[k] = h
		}
	}
	return out, len(out) > 0
}

// DeleteRootOverlay removes one workspace root's overlay store artifacts
// from a project storage root: the per-root branch store, its lexical
// directory, its file-hash sidecar and its ownership marker (refs/<rootKey>.
// ref — the root's OWN reference to the shared store, ADR-104 stage 5; the
// detach removes exactly this reference and this delta), plus the root's
// §3 state directory (overlay.tab, overlay-lexical/, sidecar.json,
// view.json) in the CURRENT generation — without it every released
// worktree leaks its state dir forever (the hygiene sweep reclaims
// snapshots and superseded generations, never roots/) and a worktree
// recreated at the same path re-adopts the stale overlay rows. The shared
// corpus is never touched — other roots (other worktrees, the checkout)
// may still view it; the next sweep reclaims it only when no live marker
// reaches it. Best-effort by design (the caller usually runs it after the
// tree itself is gone); returns the first removal error encountered.
func DeleteRootOverlay(storageRoot, workspaceRoot string) error {
	key := overlayRootKey(workspaceRoot)
	var firstErr error
	for _, victim := range []string{
		branchRootPath(storageRoot, key),
		lexicalIndexPath(storageRoot, key),
		fileHashesPathFor(storageRoot, key),
		contentlessMarkerPathFor(storageRoot, key),
	} {
		if err := os.RemoveAll(victim); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Superseded generations' roots/ subdirectories are reclaimed with
	// their whole generation (removeSupersededGenerations); only the
	// CURRENT generation's per-root state dir is this call's to remove.
	if genDir, _, ok := readCurrentGeneration(storageRoot); ok {
		if err := os.RemoveAll(rootStateDir(genDir, key)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := removeRootRef(storageRoot, key); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// quarantineVictims lists the artifacts ONE scope owns. chunks/ and
// embedding_cache/ are NEVER listed: chunk records are content-addressed and
// shared by every snapshot and overlay of the store, and the embedding cache
// is re-derivable content that costs inference to rebuild — destroying either
// would punish every other store for one store's defect, and neither can make
// a single store unopenable on its own.
func quarantineVictims(storageRoot, key string, scope QuarantineScope) []string {
	victims := []string{
		branchRootPath(storageRoot, key),           // the v1 chromem root (corpus.json included)
		lexicalIndexPath(storageRoot, key),         // the legacy per-key bleve index
		fileHashesPathFor(storageRoot, key),        // the legacy per-key sidecar
		contentlessMarkerPathFor(storageRoot, key), // the content-less migration marker
	}
	genDir, _, ok := readCurrentGeneration(storageRoot)
	if !ok {
		return victims
	}
	switch scope {
	case QuarantineCorpusStore:
		// The snapshot's §3 state: table.json (the completion marker — the
		// publication itself), occurrences.tab and the snapshot's lexical
		// index.
		victims = append(victims, snapshotDir(genDir, key))
	case QuarantineOverlayStore:
		// The root's §3 state: overlay.tab, overlay-lexical, sidecar.json,
		// view.json.
		victims = append(victims, rootStateDir(genDir, key))
	}
	return victims
}

// removeQuarantinedStore revokes one scope's publication and deletes its
// artifacts. The §3 completion marker is removed FIRST (the unpublish), so a
// partial removal still leaves an unpublished — and therefore rebuildable —
// store instead of a published one with missing parts. Returns the first
// removal error (the caller then reports the attach unavailable rather than
// retrying over a store it could not clean).
func removeQuarantinedStore(storageRoot, key string, scope QuarantineScope) error {
	if scope == QuarantineCorpusStore {
		if genDir, _, ok := readCurrentGeneration(storageRoot); ok {
			unpublishSnapshotTable(genDir, key)
		}
	}
	var firstErr error
	for _, victim := range quarantineVictims(storageRoot, key, scope) {
		if err := os.RemoveAll(victim); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// quarantineCorpusStore destroys a snapshot corpus's artifacts UNDER THE
// BUILD LEASE (exactly one writer to a corpus store, ADR-104 §4): a repair
// never races a build, and a build that owns the lease is allowed to settle
// first. Retained (zero-ref) decode handles are closed afterwards so the
// residency LRU never serves a store that no longer exists; attached handles
// belong to other roots and are left alone (POSIX-safe: their readers hold
// their own state, and the next open rebuilds).
func (cs *corpusStore) quarantineCorpusStore(ctx context.Context, key, embeddingFP string, dims int) error {
	started, waitCh := cs.beginBuild(key)
	if !started {
		// A concurrent build owned the lease. A SETTLED-but-unpublished
		// build proceeds to the removal below (its lease is gone and the
		// store still holds the defect we came to remove); a live
		// PUBLICATION aborts the quarantine: it is a fresh full rebuild
		// whose artifacts are healthy by construction, and the congestion
		// that led here proved nothing about them.
		_, published := cs.waitBuildOutcome(waitCh, key, embeddingFP, dims, corpusBuildWait)
		if published {
			cs.logger.Info("vector corpus: quarantine aborted — the concurrent build published a compatible corpus",
				"snapshot", key)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("waiting for the corpus build lease: %w", err)
		}
		started, _ = cs.beginBuild(key)
		if !started {
			return fmt.Errorf("%w: %s", errCorpusBuildBusy, key)
		}
		// Defense in depth for the gap between the expired wait and this
		// lease: a build that settled right after the bound may have
		// published in that window, and that publication is a fresh full
		// rebuild — removing it would destroy the exact healthy corpus the
		// caller's retry attach is about to open. This mirror of
		// EnsureCorpus's entry check is deliberately scoped to the
		// concurrent-build path ONLY: on the direct path (no wait) the
		// store still holds the defect the caller PROVED, and a
		// published-looking but unopenable corpus is exactly what the
		// quarantine exists to remove.
		if cs.publishedCompatible(key, embeddingFP, dims) {
			// The retry acquired the lease, so this return OWNS it: settle
			// it or the snapshot is wedged for the process lifetime (every
			// later attacher first drains the full corpusBuildWait against
			// a channel nobody will close, and the reachability sweep can
			// never reclaim the corpus). A nil settle is the honest result
			// for any waiter: they re-check the publication, which is
			// exactly why we skipped.
			cs.endBuild(key, nil)
			cs.logger.Info("vector corpus: quarantine skipped — a compatible corpus appeared with the lease",
				"snapshot", key)
			return nil
		}
	}
	err := removeQuarantinedStore(cs.root, key, QuarantineCorpusStore)
	cs.endBuild(key, err)
	cs.reg.ReclaimKeys(cs.root, map[string]struct{}{key: {}})
	if err != nil {
		return fmt.Errorf("removing the corpus store of %s: %w", key, err)
	}
	return nil
}

// CorpusRegistry is the process-level owner of the per-store corpus
// coordinators (ADR-104 §4: "one store owner per project store per
// process"). The backend wires exactly one registry into every Manager of
// every project through the manager factory; managers attach and release
// read-only corpus handles through it. A nil registry (tests, standalone
// services) disables the shared-corpus model entirely — those services keep
// the historical per-branch behavior.
//
// Residency (ADR-104 §5) is REGISTRY-level policy: the count/byte bounds
// below govern every store it owns, so the budget spans all projects'
// shared corpora in the process. The default (0/0) keeps retention off —
// a zero-ref handle closes immediately, exactly the pre-residency
// behavior; additional retention of inactive views is an opt-in.
type CorpusRegistry struct {
	mu     sync.Mutex
	stores map[string]*corpusStore
	logger *slog.Logger

	// resCapacity / resBudget are the residency bounds propagated to every
	// store (see corpusStore.resCapacity). 0/0 = retention disabled.
	resCapacity int
	resBudget   int64

	// diskBudget is the store-tree disk budget propagated to every store
	// (store_gc.go §5); 0 disables snapshot eviction under pressure.
	diskBudget atomic.Int64

	// freeOSMemory is the instance-local memory-free seam resolved at
	// construction (mirrors Service.freeOSMemory); residency evictions
	// schedule it asynchronously so the freed RAM is returned to the OS
	// without holding any lock. In-package tests swap it to observe calls.
	freeOSMemory func()

	// closed is set by Close (app shutdown): every later storeFor/Open
	// fails instead of resurrecting stores over torn-down handles.
	closed bool
}

// NewCorpusRegistry creates the process-level corpus registry.
func NewCorpusRegistry(logger *slog.Logger) *CorpusRegistry {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &CorpusRegistry{
		stores:       make(map[string]*corpusStore),
		logger:       logger,
		freeOSMemory: debug.FreeOSMemory,
	}
}

// storeFor returns (creating if absent) the corpus coordinator of one
// physical storage root. Nil once the registry is closed.
func (r *CorpusRegistry) storeFor(storageRoot string) *corpusStore {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	if r.stores == nil {
		r.stores = make(map[string]*corpusStore)
	}
	cs, ok := r.stores[storageRoot]
	if !ok {
		cs = &corpusStore{
			root:        storageRoot,
			logger:      r.logger,
			reg:         r,
			resCapacity: r.resCapacity,
			resBudget:   r.resBudget,
		}
		cs.diskBudgetBytes.Store(r.diskBudget.Load())
		r.stores[storageRoot] = cs
	}
	return cs
}

// resetUnpublishedCorpusStore deletes the unpublished corpus branch store's
// v1 artifacts (chromem root, lexical index, sidecar, content-less marker)
// under key so a (re)tried build starts from an empty store instead of
// resuming over an interrupted attempt's committed prefix: IndexFull only
// ADDs documents, so a file whose chunk count shrank between attempts would
// keep its stale higher-index chunks alive in the immutable corpus forever
// (and permanently block the snapshot's v1→v2 chunk-record migration). The
// §3 generation tree is deliberately untouched — its chunk records are
// content-addressed and shared, so the retry re-embeds nothing reusable.
// Best-effort per artifact (the rebuild overwrites survivors anyway); only
// called on the fresh-build path, where no era-compatible publication
// exists by definition.
func (cs *corpusStore) resetUnpublishedCorpusStore(storageRoot, key string) {
	for _, victim := range []string{
		branchRootPath(storageRoot, key),
		lexicalIndexPath(storageRoot, key),
		fileHashesPathFor(storageRoot, key),
		contentlessMarkerPathFor(storageRoot, key),
	} {
		if err := os.RemoveAll(victim); err != nil {
			cs.logger.Warn("vector corpus: failed to reset unpublished corpus store artifact",
				"snapshot", key, "path", victim, "error", err)
		}
	}
}

// QuarantineStoreView destroys the artifacts of ONE store after a PROVEN
// attach defect (see QuarantineScope), so the next pass rebuilds it instead
// of wedging the attach forever. A cancelled operation destroys nothing —
// "the store is broken" is never concluded from an interrupted request. The
// corpus scope runs under the build lease; the overlay scope touches only
// the named root's own store.
func (r *CorpusRegistry) QuarantineStoreView(ctx context.Context, storageRoot, snapKey, rootKey string, scope QuarantineScope, embeddingFP string, dims int) error {
	if r == nil {
		return errors.New("corpus registry not wired")
	}
	if scope == QuarantineNoStore {
		return errors.New("quarantining a store needs a scope")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("quarantining the %s store: %w", scope, err)
	}
	cs := r.storeFor(storageRoot)
	if cs == nil {
		return errors.New("corpus registry is closed")
	}
	switch scope {
	case QuarantineCorpusStore:
		if snapKey == "" {
			return errors.New("quarantining a corpus store needs the snapshot key")
		}
		return cs.quarantineCorpusStore(ctx, snapKey, embeddingFP, dims)
	case QuarantineOverlayStore:
		if rootKey == "" {
			return errors.New("quarantining a root overlay needs the root key")
		}
		if err := removeQuarantinedStore(storageRoot, rootKey, QuarantineOverlayStore); err != nil {
			return fmt.Errorf("removing the overlay store of %s: %w", rootKey, err)
		}
		return nil
	default:
		return fmt.Errorf("unknown quarantine scope %d", scope)
	}
}

// EnsureCorpus drives one snapshot's corpus to a published state under
// single-flight (ADR-104 §4: root managers SUBMIT work; at most one writer
// ever touches the store): the first attacher runs build (the caller's
// full-pass callback) under the lease; concurrent attachers wait bounded for
// its outcome, then open the published corpus or fail closed. The migration
// step promotes a compatible legacy branch store by rename before falling
// back to a build. Returns the error of the ensuring caller only.
func (r *CorpusRegistry) EnsureCorpus(ctx context.Context, storageRoot, key, legacyBranch, embeddingFP string, dims int, build func() error, onMigrate func()) error {
	if r == nil || key == "" {
		return errors.New("corpus registry not wired")
	}
	cs := r.storeFor(storageRoot)
	if cs == nil {
		return errors.New("corpus registry is closed")
	}
	if cs.publishedCompatible(key, embeddingFP, dims) {
		return nil
	}
	started, waitCh := cs.beginBuild(key)
	if !started {
		// Someone else is building this corpus: wait bounded, then read the
		// published result — never open a parallel store (single-writer).
		// The two failure modes are deliberately distinct errors: an
		// EXPIRED wait is congestion (errCorpusBuildOvertime — a transient
		// outcome that proves nothing about the store, so the publication
		// stays and the repair ladder must not quarantine it), while a
		// SETTLED build without a compatible publication is the store's own
		// verdict (ErrCorpusNotPublished — the repair ladder owns it).
		settled, published := cs.waitBuildOutcome(waitCh, key, embeddingFP, dims, corpusBuildWait)
		switch {
		case published:
			return nil
		case settled:
			return fmt.Errorf("%w: concurrent corpus build for %s finished without a compatible publication", ErrCorpusNotPublished, key)
		default:
			return fmt.Errorf("%w: concurrent corpus build for %s did not settle within %s", errCorpusBuildOvertime, key, corpusBuildWait)
		}
	}
	buildErr := func() error {
		migrated, reason, mErr := cs.promoteLegacyStore(storageRoot, legacyBranch, key, embeddingFP, dims)
		if mErr != nil {
			return mErr
		}
		if migrated {
			if onMigrate != nil {
				onMigrate()
			}
			return nil
		}
		cs.logger.Debug("vector corpus: legacy store not promotable; building a fresh corpus",
			"branch", legacyBranch, "snapshot", key, "reason", reason)
		// A retried build must never resume over the interrupted attempt's
		// committed prefix: IndexFull only ADDs, so a file whose chunk count
		// shrank between attempts would keep its stale higher-index chunks
		// alive in the immutable corpus forever (and permanently block the
		// snapshot's v1→v2 chunk-record migration). Reset the unpublished
		// v1 store before the build; the §3 generation tree stays untouched
		// (its chunk records are content-addressed and shared, so the retry
		// re-embeds nothing it could have reused).
		cs.resetUnpublishedCorpusStore(storageRoot, key)
		if bErr := build(); bErr != nil {
			return bErr
		}
		if !cs.publishedCompatible(key, embeddingFP, dims) {
			// The builder is responsible for publishing (PublishCorpus);
			// a finished build without publication is a contract breach —
			// fail closed rather than serve an unproven store.
			return fmt.Errorf("%w: build for %s finished without publishing", ErrCorpusNotPublished, key)
		}
		return nil
	}()
	cs.endBuild(key, buildErr)
	return buildErr
}

// Open returns the shared read-only handle of key's published corpus under
// storageRoot (fail-closed; see corpusStore.open).
func (r *CorpusRegistry) Open(ctx context.Context, storageRoot, key string, ef chromem.EmbeddingFunc, embeddingFP string, dims int) (*CorpusHandle, error) {
	if r == nil {
		return nil, errors.New("corpus registry not wired")
	}
	cs := r.storeFor(storageRoot)
	if cs == nil {
		return nil, errors.New("corpus registry is closed")
	}
	return cs.open(ctx, key, ef, embeddingFP, dims)
}

// ProtectedKeys returns the snapshot keys of storageRoot that the process
// currently PROTECTS from reachability GC (ADR-104 stage 5): every key with
// a live shared handle (a root's attached view — a held snapshot) and every
// key under an in-flight build lease (a corpus being built or migrated has
// no ownership marker yet; the lease is its interim owner). The sweep
// consults this before removing corpus artifacts, so it can never delete a
// live reader's decode or a builder's target; a nil registry protects
// nothing (no registry means no corpus mode — nothing to protect).
// RETAINED zero-reference handles (the residency LRU) are deliberately NOT
// protected: retention is a memory cache, not an owner — ReclaimKeys closes
// them on the sweep's behalf.
func (r *CorpusRegistry) ProtectedKeys(storageRoot string) map[string]struct{} {
	if r == nil {
		return nil
	}
	cs := r.storeFor(storageRoot)
	if cs == nil {
		return nil // registry closed — nothing is protected or reclaimable
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	protected := make(map[string]struct{}, len(cs.refs)+len(cs.builds))
	for key, n := range cs.refs {
		if n > 0 {
			protected[key] = struct{}{}
		}
	}
	for key := range cs.builds {
		protected[key] = struct{}{}
	}
	return protected
}

// ReclaimKeys closes the RETAINED (zero-reference) handles of the given
// snapshot keys under storageRoot — the reachability sweep calls it for
// corpora it is about to remove, so a residency slot's open lexical reader
// cannot keep the directory alive (Windows handle-close ordering) and the
// LRU never caches a deleted store. Attached handles (refs > 0) and
// in-flight builds are left untouched — their keys never reach the sweep's
// victim set (ProtectedKeys); if one slipped through anyway it stays open
// and the removal fails soft, retried by the next sweep.
func (r *CorpusRegistry) ReclaimKeys(storageRoot string, keys map[string]struct{}) {
	if r == nil || len(keys) == 0 || storageRoot == "" {
		return
	}
	cs := r.storeFor(storageRoot)
	if cs == nil {
		return // registry closed — teardown already retired every handle
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for key := range keys {
		if cs.refs[key] > 0 {
			continue // attached: not ours to close
		}
		h, ok := cs.handles[key]
		if !ok {
			continue
		}
		delete(cs.handles, key)
		cs.detachLRULocked(key)
		cs.closeHandleLocked(h)
	}
}
