package vectorindex

// Reachability-GC tests of ADR-104 stage 5 (rootrefs.go + the prune sweep):
// shared artifacts (snapshot corpora and per-root overlays) live or die by
// OWNER reachability — the on-disk root references plus the registry's held
// snapshots and build leases — never by branch-name presence in refs/heads.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// gcFixture is one storage root dressed with shared artifacts and root
// references. liveRoot is an existing workspace tree; deadRoot points at a
// path that does not exist (a tree removed while the app was not running).
type gcFixture struct {
	storage  string
	liveRoot string
	deadRoot string
}

// newGCFixture creates the storage root and the two root paths.
func newGCFixture(t *testing.T) gcFixture {
	t.Helper()
	base := t.TempDir()
	f := gcFixture{
		storage:  filepath.Join(base, "vector_index"),
		liveRoot: filepath.Join(base, "alive-tree"),
		deadRoot: filepath.Join(base, "gone-tree"),
	}
	pruneMkdir(t, f.liveRoot)
	return f
}

// addCorpus lays out one snapshot corpus's artifacts (branch store, lexical
// directory, sidecar pair) without embedding anything.
func (f gcFixture) addCorpus(t *testing.T, snapKey string) {
	t.Helper()
	pruneMkdir(t, branchRootPath(f.storage, snapKey))
	pruneWrite(t, filepath.Join(branchRootPath(f.storage, snapKey), "doc.gob"), "x")
	pruneMkdir(t, lexicalIndexPath(f.storage, snapKey))
	pruneWrite(t, fileHashesPathFor(f.storage, snapKey), "{}")
	pruneWrite(t, contentlessMarkerPathFor(f.storage, snapKey), "")
}

func (f gcFixture) addOverlay(t *testing.T, rootKey string) {
	t.Helper()
	pruneMkdir(t, branchRootPath(f.storage, rootKey))
	pruneMkdir(t, lexicalIndexPath(f.storage, rootKey))
	pruneWrite(t, fileHashesPathFor(f.storage, rootKey), "{}")
}

func (f gcFixture) corpusDir(snapKey string) string  { return branchRootPath(f.storage, snapKey) }
func (f gcFixture) overlayDir(rootKey string) string { return branchRootPath(f.storage, rootKey) }
func (f gcFixture) lexicalDir(k string) string       { return lexicalIndexPath(f.storage, k) }
func (f gcFixture) sidecar(k string) string          { return fileHashesPathFor(f.storage, k) }
func (f gcFixture) markerPath(rootKey string) string { return rootRefPath(f.storage, rootKey) }
func (f gcFixture) markerExists(k string) bool       { _, err := os.Stat(f.markerPath(k)); return err == nil }
func (f gcFixture) dirExists(path string) bool       { _, err := os.Stat(path); return err == nil }
func (f gcFixture) fileExists(path string) bool      { _, err := os.Stat(path); return err == nil }

// TestBranchRefKey_DistinctAcrossFolds pins the WRITE side of the bref key
// space: the pre-hash case-preserving sanitizer collapsed distinct
// branch names onto one refs/bref_<sanitized>.ref file — case-insensitive
// filesystems fold "Feature/X" into "feature/x", and the sanitizer's own
// folds ('/'→'_', every stripped character) collide on every filesystem —
// so the second live branch's attach overwrote the first's ownership
// record and the sweep reclaimed a live branch's corpus. The hash key
// keeps every spelling distinct on disk.
func TestBranchRefKey_DistinctAcrossFolds(t *testing.T) {
	f := newGCFixture(t)
	snap := snapshotKey("4242424242424242")
	branches := []string{"Feature/X", "feature/x", "feature_x", "featureX"}
	for _, b := range branches {
		if err := writeBranchRef(f.storage, b, snap); err != nil {
			t.Fatalf("writeBranchRef(%q): %v", b, err)
		}
	}
	if branchRefKey("feature/x") == branchRefKey("feature_x") {
		t.Fatal("the sanitizer fold '/'→'_' still collapses bref keys")
	}
	if branchRefKey("Feature/X") == branchRefKey("feature/x") {
		t.Fatal("the case fold still collapses bref keys")
	}
	refs, ok := readRootRefs(f.storage)
	if !ok {
		t.Fatal("readRootRefs failed")
	}
	for _, b := range branches {
		if _, seen := refs[branchRefKey(b)]; !seen {
			t.Errorf("branch %q lost its marker to a key fold", b)
		}
	}
}

// TestPruneSharedArtifacts_ReachabilityGC pins the stage-5 contract on the
// manager-free sweep: corpora and overlays of LIVE roots survive, the dead
// root's reference is removed, and everything only the dead root reached is
// reclaimed — while branch data keeps the refs/heads rules.
func TestPruneSharedArtifacts_ReachabilityGC(t *testing.T) {
	f := newGCFixture(t)
	liveOverlay := overlayRootKey(f.liveRoot)
	deadOverlay := overlayRootKey(f.deadRoot)

	snapShared := snapshotKey("aaaa1111aaaa1111") // viewed by the LIVE root
	snapOrphan := snapshotKey("bbbb2222bbbb2222") // viewed only by the DEAD root
	f.addCorpus(t, snapShared)
	f.addCorpus(t, snapOrphan)
	f.addOverlay(t, liveOverlay)
	f.addOverlay(t, deadOverlay)
	// A live branch store keeps the historical rules.
	pruneMkdir(t, branchRootPath(f.storage, "main"))

	if err := writeRootRef(f.storage, liveOverlay, f.liveRoot, snapShared); err != nil {
		t.Fatalf("writeRootRef(live): %v", err)
	}
	if err := writeRootRef(f.storage, deadOverlay, f.deadRoot, snapOrphan); err != nil {
		t.Fatalf("writeRootRef(dead): %v", err)
	}
	// Unknown files under refs/ are not the sweep's to remove.
	unknownRef := filepath.Join(f.storage, refsDirName, "not-a-marker.txt")
	pruneWrite(t, unknownRef, "junk")

	removed := PruneMissingBranchDataFS(slog.New(slog.DiscardHandler), f.storage, []string{"main"})
	if removed == 0 {
		t.Fatal("expected the dead root's artifacts to be removed")
	}

	// Live root: overlay, its corpus, its marker — all intact.
	for _, path := range []string{
		f.overlayDir(liveOverlay), f.lexicalDir(liveOverlay), f.sidecar(liveOverlay),
		f.corpusDir(snapShared), f.lexicalDir(snapShared), f.sidecar(snapShared),
	} {
		if !f.dirExists(path) && !f.fileExists(path) {
			t.Errorf("live root artifact removed: %s", path)
		}
	}
	if !f.markerExists(liveOverlay) {
		t.Error("live root's marker removed")
	}
	// Branch data: the surviving branch stays.
	if !f.dirExists(branchRootPath(f.storage, "main")) {
		t.Error("surviving branch store removed")
	}

	// Dead root: reference, overlay and the corpus only it reached — gone.
	if f.markerExists(deadOverlay) {
		t.Error("dead root's marker not removed")
	}
	for _, path := range []string{
		f.overlayDir(deadOverlay), f.lexicalDir(deadOverlay), f.sidecar(deadOverlay),
		f.corpusDir(snapOrphan), f.lexicalDir(snapOrphan), f.sidecar(snapOrphan),
	} {
		if f.dirExists(path) || f.fileExists(path) {
			t.Errorf("dead root artifact still present: %s", path)
		}
	}
	if !f.fileExists(unknownRef) {
		t.Error("unknown refs/ entry removed by the sweep")
	}
}

// TestPruneSharedArtifacts_NoRefsDirKeepsEverything pins the conservative
// upgrade posture: a store that never recorded an owner (no refs/
// directory) keeps every corpus/overlay artifact — the first marker-writing
// attach opts the store into reachability GC.
func TestPruneSharedArtifacts_NoRefsDirKeepsEverything(t *testing.T) {
	f := newGCFixture(t)
	snap := snapshotKey("cccc3333cccc3333")
	f.addCorpus(t, snap)
	f.addOverlay(t, overlayRootKey(f.liveRoot))

	if removed := PruneMissingBranchDataFS(slog.New(slog.DiscardHandler), f.storage, nil); removed != 0 {
		t.Fatalf("sweep removed %d artifacts from a store without ownership records", removed)
	}
	if !f.dirExists(f.corpusDir(snap)) || !f.dirExists(f.overlayDir(overlayRootKey(f.liveRoot))) {
		t.Error("shared artifacts removed from an owner-less store")
	}
}

// TestPruneSharedArtifacts_RegistryProtection pins that the process registry
// protects what it holds — an attached corpus (a held snapshot) survives the
// sweep even when no ownership marker references it, and so does an
// in-flight build's target.
func TestPruneSharedArtifacts_RegistryProtection(t *testing.T) {
	f := newGCFixture(t)
	snapAttached := snapshotKey("dddd4444dddd4444")
	snapBuilding := snapshotKey("eeee5555eeee5555")
	snapOrphan := snapshotKey("ffff6666ffff6666")
	f.addCorpus(t, snapAttached)
	f.addCorpus(t, snapBuilding)
	f.addCorpus(t, snapOrphan)

	// A live marker for an unrelated snapshot opts the store into
	// reachability mode (refs/ exists) without referencing the three above.
	otherOverlay := overlayRootKey(f.liveRoot)
	if err := writeRootRef(f.storage, otherOverlay, f.liveRoot, snapshotKey("7777777777777777")); err != nil {
		t.Fatalf("writeRootRef: %v", err)
	}
	f.addOverlay(t, otherOverlay)

	registry := NewCorpusRegistry(nil)
	t.Cleanup(func() { registry.Close() })
	cs := registry.storeFor(f.storage)
	// An attached handle (refs > 0) and an in-flight build lease.
	cs.mu.Lock()
	if cs.handles == nil {
		cs.handles = map[string]*CorpusHandle{}
	}
	if cs.refs == nil {
		cs.refs = map[string]int{}
	}
	cs.handles[snapAttached] = &CorpusHandle{key: snapAttached}
	cs.refs[snapAttached] = 1
	cs.mu.Unlock()
	started, _ := cs.beginBuild(snapBuilding)
	if !started {
		t.Fatal("could not acquire the build lease")
	}
	t.Cleanup(func() { cs.endBuild(snapBuilding, nil) })

	protected := registry.ProtectedKeys(f.storage)
	if _, ok := protected[snapAttached]; !ok {
		t.Error("attached corpus not protected")
	}
	if _, ok := protected[snapBuilding]; !ok {
		t.Error("in-flight build not protected")
	}

	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc(), Corpus: registry})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if removed := svc.PruneMissingBranchData(f.storage, nil); removed == 0 {
		t.Fatal("expected the unreferenced corpus to be removed")
	}
	if !f.dirExists(f.corpusDir(snapAttached)) {
		t.Error("attached corpus reclaimed by the sweep")
	}
	if !f.dirExists(f.corpusDir(snapBuilding)) {
		t.Error("in-flight build target reclaimed by the sweep")
	}
	if f.dirExists(f.corpusDir(snapOrphan)) {
		t.Error("unreferenced corpus survived the sweep")
	}
}

// TestReclaimKeys_ClosesRetainedHandles pins the Windows handle-close
// ordering of the sweep: the RESIDENCY slot of a corpus about to be removed
// is closed first (its lexical reader must not keep the directory alive),
// while an attached handle (refs > 0) is never the sweep's to close.
func TestReclaimKeys_ClosesRetainedHandles(t *testing.T) {
	f := newGCFixture(t)
	retained := snapshotKey("8888888888888888")
	attached := snapshotKey("9999999999999999")

	registry := NewCorpusRegistry(nil)
	t.Cleanup(func() { registry.Close() })
	cs := registry.storeFor(f.storage)
	cs.mu.Lock()
	cs.handles = map[string]*CorpusHandle{
		retained: {key: retained},
		attached: {key: attached},
	}
	cs.refs = map[string]int{attached: 1}
	cs.lru = []string{retained}
	cs.mu.Unlock()

	registry.ReclaimKeys(f.storage, map[string]struct{}{retained: {}, attached: {}})

	cs.mu.Lock()
	defer cs.mu.Unlock()
	if _, ok := cs.handles[retained]; ok {
		t.Error("retained handle still cached after ReclaimKeys")
	}
	for _, k := range cs.lru {
		if k == retained {
			t.Error("retained handle still in the residency LRU after ReclaimKeys")
		}
	}
	if _, ok := cs.handles[attached]; !ok || cs.refs[attached] != 1 {
		t.Error("attached handle closed by ReclaimKeys")
	}
}

// TestDeleteRootOverlay_RemovesOwnReferenceNotCorpus pins the detach
// contract: a released root's OWN delta (overlay store, lexical directory,
// sidecar) and its ownership marker go away; the shared corpus another root
// may still view stays untouched.
func TestDeleteRootOverlay_RemovesOwnReferenceNotCorpus(t *testing.T) {
	f := newGCFixture(t)
	snap := snapshotKey("abab1212abab1212")
	overlay := overlayRootKey(f.liveRoot)
	f.addCorpus(t, snap)
	f.addOverlay(t, overlay)
	if err := writeRootRef(f.storage, overlay, f.liveRoot, snap); err != nil {
		t.Fatalf("writeRootRef: %v", err)
	}

	if err := DeleteRootOverlay(f.storage, f.liveRoot); err != nil {
		t.Fatalf("DeleteRootOverlay: %v", err)
	}
	if f.dirExists(f.overlayDir(overlay)) || f.dirExists(f.lexicalDir(overlay)) || f.fileExists(f.sidecar(overlay)) ||
		f.fileExists(contentlessMarkerPathFor(f.storage, overlay)) {
		t.Error("released root's overlay artifacts still present")
	}
	if f.markerExists(overlay) {
		t.Error("released root's ownership marker still present")
	}
	for _, path := range []string{f.corpusDir(snap), f.lexicalDir(snap), f.sidecar(snap)} {
		if !f.dirExists(path) && !f.fileExists(path) {
			t.Errorf("shared corpus artifact removed by a root detach: %s", path)
		}
	}
}

// TestPinCorpusView_WritesAndUpdatesMarker pins the ownership record: the
// marker lands under the service's storage root, carries the workspace path
// (the liveness anchor) and the viewed snapshot, and a re-attach to a new
// snapshot rewrites it in place.
func TestPinCorpusView_WritesAndUpdatesMarker(t *testing.T) {
	f := newGCFixture(t)
	svc := newPruneTestService(t, 0)
	if err := svc.SetProject("pin", f.storage); err != nil {
		t.Fatalf("SetProject: %v", err)
	}

	overlay := overlayRootKey(f.liveRoot)
	snap1 := snapshotKey("1313131313131313")
	if err := svc.PinCorpusView(f.liveRoot, snap1); err != nil {
		t.Fatalf("PinCorpusView: %v", err)
	}
	refs, ok := readRootRefs(f.storage)
	if !ok || len(refs) != 1 {
		t.Fatalf("expected one ownership marker, ok=%v refs=%v", ok, refs)
	}
	got, ok := refs[overlay]
	if !ok || got.Root != filepath.Clean(f.liveRoot) || got.Snapshot != snap1 {
		t.Fatalf("marker content = %+v (root %s snapshot %s)", got, f.liveRoot, snap1)
	}
	if !rootRefLive(got) {
		t.Error("marker of an existing tree reported dead")
	}

	snap2 := snapshotKey("1414141414141414")
	if err := svc.PinCorpusView(f.liveRoot, snap2); err != nil {
		t.Fatalf("PinCorpusView(re-pin): %v", err)
	}
	refs, _ = readRootRefs(f.storage)
	if refs[overlay].Snapshot != snap2 {
		t.Errorf("re-pin did not update the marker: %+v", refs[overlay])
	}
}

// TestAttachSnapshot_PinsMarkerBeforeCorpus pins the intent ordering: the
// ownership marker exists from the moment the attach starts (before the
// corpus is ensured), so the reachability sweep can never reclaim a corpus
// a live root is in the middle of building.
func TestAttachSnapshot_PinsMarkerBeforeCorpus(t *testing.T) {
	h := newViewHarness(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	svc := h.newViewService(nil)
	if err := svc.SetProject("pin-intent", h.storage); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	svc.SetWorkspaceRoot(h.rootA)
	idx := h.newIndexer(svc, nil, nil)

	oid, err := CurrentCommit(context.Background(), h.rootA)
	if err != nil {
		t.Fatalf("CurrentCommit: %v", err)
	}
	key := snapshotKey(oid)
	if _, statErr := os.Stat(rootRefPath(h.storage, overlayRootKey(h.rootA))); statErr == nil {
		t.Fatal("marker already present before the attach")
	}
	if err := idx.AttachSnapshot(context.Background(), h.rootA, "main"); err != nil {
		t.Fatalf("AttachSnapshot: %v", err)
	}
	refs, ok := readRootRefs(h.storage)
	if !ok {
		t.Fatal("no refs directory after the attach")
	}
	got, ok := refs[overlayRootKey(h.rootA)]
	if !ok || got.Snapshot != key || !rootRefLive(got) {
		t.Fatalf("attach did not pin the root's marker: %+v (want snapshot %s)", got, key)
	}
}

// TestSweepProtectsLiveOverlayState pins the Service form's defensive net:
// a live state's overlay (and its attached corpus, through the registry)
// survives the sweep even when its ownership marker is missing — e.g. the
// pin write failed — and a PARKED overlay state with a live marker is kept
// rather than evicted.
func TestSweepProtectsLiveOverlayState(t *testing.T) {
	h := newViewHarness(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	counter := &embedCounter{}
	svc := h.newViewService(counter)
	idx := h.newIndexer(svc, nil, nil)
	if err := h.attach(svc, idx, h.rootA, "main"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	overlay := overlayRootKey(h.rootA)
	oid, _ := CurrentCommit(context.Background(), h.rootA)
	snap := snapshotKey(oid)

	// Simulate a failed pin: the marker is gone while the view stays live.
	if err := removeRootRef(h.storage, overlay); err != nil {
		t.Fatalf("removeRootRef: %v", err)
	}
	// The checkout store holds a legacy branch artifact for a branch that
	// no longer exists — the branch rules must still apply to it.
	pruneMkdir(t, branchRootPath(h.storage, "gone-branch"))

	if removed := svc.PruneMissingBranchData(h.storage, []string{"main"}); removed == 0 {
		t.Fatal("expected the stale branch artifact to be removed")
	}
	if !dirExistsFS(branchRootPath(h.storage, overlay)) {
		t.Error("live state's overlay reclaimed by the sweep")
	}
	if !dirExistsFS(branchRootPath(h.storage, snap)) {
		t.Error("attached corpus reclaimed by the sweep")
	}
	if dirExistsFS(branchRootPath(h.storage, "gone-branch")) {
		t.Error("stale branch artifact survived the sweep")
	}
}

// dirExistsFS is the package-external shape of the fixture helper (the
// corpus harness fixtures are plain strings, not gcFixture).
func dirExistsFS(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestBranchRef_BranchReachability pins the branch side of the ownership
// model (ADR-104 §1 L3, stage 5): a branch reaches its tip corpus through
// its OWN pointer (refs/bref_*.ref) — while the branch survives the sweep
// keeps the corpus even with no root attached; once the branch is gone from
// the caller's surviving list the reference is removed and the corpus is
// reclaimed. No branch-NAMED directory is ever consulted.
func TestBranchRef_BranchReachability(t *testing.T) {
	f := newGCFixture(t)
	snapTip := snapshotKey("1616161616161616") // the branch's tip commit
	f.addCorpus(t, snapTip)
	if err := writeBranchRef(f.storage, "topic", snapTip); err != nil {
		t.Fatalf("writeBranchRef: %v", err)
	}

	// The branch survives: the sweep keeps the corpus (no root attached,
	// no registry handle — the branch reference alone).
	if removed := PruneMissingBranchDataFS(slog.New(slog.DiscardHandler), f.storage, []string{"main", "topic"}); removed != 0 {
		t.Fatalf("sweep removed %d artifacts while the branch survived", removed)
	}
	if !f.dirExists(f.corpusDir(snapTip)) {
		t.Fatal("branch-referenced corpus reclaimed while the branch survives")
	}
	if !f.markerExists(branchRefKey("topic")) {
		t.Fatal("surviving branch's reference removed")
	}

	// The branch is gone: its reference is detached and the corpus (no
	// other owner) is reclaimed.
	if removed := PruneMissingBranchDataFS(slog.New(slog.DiscardHandler), f.storage, []string{"main"}); removed == 0 {
		t.Fatal("sweep removed nothing after the branch disappeared")
	}
	if f.markerExists(branchRefKey("topic")) {
		t.Error("gone branch's reference still present")
	}
	if f.dirExists(f.corpusDir(snapTip)) {
		t.Error("corpus of a gone branch survived with no owner")
	}
}

// TestDeleteBranchData_RemovesOwnReferenceNotCorpus pins the delete-triggered
// purge against the shared store: deleting a branch removes the branch's
// artifacts AND its own corpus reference, but never the corpus itself (other
// owners may still reach it) and never a colliding surviving branch's
// reference.
func TestDeleteBranchData_RemovesOwnReferenceNotCorpus(t *testing.T) {
	f := newGCFixture(t)
	snapTip := snapshotKey("1717171717171717")
	f.addCorpus(t, snapTip)
	if err := writeBranchRef(f.storage, "feature/x", snapTip); err != nil {
		t.Fatalf("writeBranchRef: %v", err)
	}
	// A live root still viewing the same corpus keeps it regardless.
	liveOverlay := overlayRootKey(f.liveRoot)
	if err := writeRootRef(f.storage, liveOverlay, f.liveRoot, snapTip); err != nil {
		t.Fatalf("writeRootRef: %v", err)
	}

	svc := newPruneTestService(t, 0)
	if removed := svc.DeleteBranchData(f.storage, "feature/x", nil); removed != 0 {
		t.Fatalf("DeleteBranchData removed %d artifacts (none laid out)", removed)
	}
	if f.markerExists(branchRefKey("feature/x")) {
		t.Error("deleted branch's reference still present")
	}
	if !f.dirExists(f.corpusDir(snapTip)) {
		t.Error("purge removed the shared corpus itself")
	}

	// Collision keeps: a surviving branch sanitizing to the same bref key
	// shares the reference — deleting the other branch keeps it.
	if err := writeBranchRef(f.storage, "feature/x", snapTip); err != nil {
		t.Fatalf("writeBranchRef(reset): %v", err)
	}
	if removed := svc.DeleteBranchData(f.storage, "feature_x", []string{"feature/x"}); removed != 0 {
		t.Fatalf("collision DeleteBranchData removed %d artifacts (none laid out)", removed)
	}
	if !f.markerExists(branchRefKey("feature/x")) {
		t.Error("colliding surviving branch's shared reference removed")
	}
	_ = removeBranchRef(f.storage, "feature/x")
}
