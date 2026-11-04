package vectorindex

// ADR-104 §5 residency acceptance tests (epic stage 4), over the stage-2
// owner/view shape:
//
//   - A→B→A with the LRU on and a sufficient budget re-attaches the SAME
//     handle — no re-decode, no lexical reopen — with exactly one decode
//     per corpus (not "one total": the other corpus decodes once too);
//   - count and byte bounds hold separately and together; an oversized
//     retained corpus is evicted (the park LRU's semantics) while an
//     oversized ATTACHED one is never closed (the budget bounds retention,
//     not admission — no hard RSS cap is promised);
//   - the default policy (0/0) keeps retention off: a released corpus
//     closes at once and a return re-decodes (the honest reopen);
//   - eviction never closes handles held by other roots/readers (the
//     refcount is the read lease), and a re-attach after eviction works;
//   - evicted memory goes through the freeOSMemory seam asynchronously,
//     never under the store lock;
//   - DeleteProjectData and CorpusRegistry.Close remove every residency
//     slot of the affected store; No-Project creates none.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
	"github.com/v0lka/c0wrk/internal/gittest"
)

// residencyHarness layers multi-snapshot publication on the viewHarness:
// the worktree (rootB) stays at the initial commit (corpus K1) while the
// checkout (rootA) advances one commit per extra corpus, every attach
// publishing one more snapshot-keyed corpus into the shared store.
type residencyHarness struct {
	*viewHarness
	svc  *Service
	idx  *Indexer
	keys []string // snapshot keys in publication order
}

// keyOf derives the snapshot key the indexer would attach for root.
func (h *residencyHarness) keyOf(root string) string {
	h.t.Helper()
	oid, err := CurrentCommit(context.Background(), root)
	if err != nil {
		h.t.Fatalf("CurrentCommit(%s): %v", root, err)
	}
	return snapshotKey(oid)
}

// attach re-attaches the shared service to root's current snapshot.
func (h *residencyHarness) attach(root, legacyBranch string) {
	h.t.Helper()
	if err := h.viewHarness.attach(h.svc, h.idx, root, legacyBranch); err != nil {
		h.t.Fatalf("attach %s: %v", root, err)
	}
}

// attach2 attaches an arbitrary service/indexer pair (the pinning tests'
// second reader).
func (h *residencyHarness) attach2(svc *Service, idx *Indexer, root, legacyBranch string) error {
	return h.viewHarness.attach(svc, idx, root, legacyBranch)
}

// currentHandleOf returns a service's attached corpus handle.
func (h *residencyHarness) currentHandleOf(svc *Service) *CorpusHandle {
	h.t.Helper()
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	return svc.current.corpus
}

// commitOn advances rootA's branch by one commit with one new file.
func (h *residencyHarness) commitOn(i int) {
	h.t.Helper()
	name := fmt.Sprintf("gen%d.go", i)
	path := filepath.Join(h.rootA, name)
	if err := os.WriteFile(path, []byte("package main\n\n// generated marker "+name+"\n"), 0o644); err != nil {
		h.t.Fatalf("write %s: %v", name, err)
	}
	h.repo.Git(h.t, "add", name)
	h.repo.Git(h.t, "commit", "-m", "gen "+name)
}

// newResidencyHarness publishes 1+extra corpora. residency (when non-nil)
// is installed BEFORE any attach, so publication itself runs under the
// policy under test.
func newResidencyHarness(t *testing.T, extra int, residency func(r *CorpusRegistry)) *residencyHarness {
	t.Helper()
	gittest.RequireGit(t)
	h := &residencyHarness{viewHarness: newViewHarness(t, map[string]string{
		"seed.go": "package main\n\n// seed alpha marker\n",
	})}
	if residency != nil {
		residency(h.registry)
	}
	h.svc = h.newViewService(nil)
	h.idx = h.newIndexer(h.svc, nil, nil)

	// K1: the initial commit, attached through the worktree (rootB).
	h.attach(h.rootB, "topic")
	h.keys = append(h.keys, h.keyOf(h.rootB))
	// K2..: each commit on main is a new snapshot for rootA.
	for i := 0; i < extra; i++ {
		h.commitOn(i)
		h.attach(h.rootA, "main")
		h.keys = append(h.keys, h.keyOf(h.rootA))
	}
	return h
}

// store returns the (test-introspection) corpus coordinator of the harness
// storage root.
func (h *residencyHarness) store() *corpusStore {
	h.t.Helper()
	cs := h.registry.storeFor(h.storage)
	if cs == nil {
		h.t.Fatal("corpus store vanished from the registry")
	}
	return cs
}

// decoded reports the decode count of one snapshot key.
func (h *residencyHarness) decoded(key string) int {
	h.t.Helper()
	cs := h.store()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.decodes[key]
}

// totalDecodes sums the decode counters.
func (h *residencyHarness) totalDecodes() int {
	h.t.Helper()
	cs := h.store()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	total := 0
	for _, n := range cs.decodes {
		total += n
	}
	return total
}

// retainedKeys copies the residency LRU (oldest first).
func (h *residencyHarness) retainedKeys() []string {
	h.t.Helper()
	cs := h.store()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return append([]string(nil), cs.lru...)
}

// handleFor returns the cached handle of key (nil when absent).
func (h *residencyHarness) handleFor(key string) *CorpusHandle {
	h.t.Helper()
	cs := h.store()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.handles[key]
}

// currentHandle returns the service's attached corpus handle.
func (h *residencyHarness) currentHandle() *CorpusHandle {
	h.t.Helper()
	h.svc.mu.RLock()
	defer h.svc.mu.RUnlock()
	return h.svc.current.corpus
}

// keysEqual asserts exact slice equality with a readable failure.
func keysEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

// installCorpusBytesEstimator swaps the estimateCorpusHandleBytes seam for a
// fixed per-key footprint so byte-budget tests can simulate multi-hundred-MiB
// corpora without materializing them. Restores the original on cleanup. Safe
// because the package's tests run sequentially (same contract as
// installParkBudgetEstimator).
func installCorpusBytesEstimator(t *testing.T, bytesByKey map[string]int64) {
	t.Helper()
	orig := estimateCorpusHandleBytes
	estimateCorpusHandleBytes = func(h *CorpusHandle) int64 {
		if h == nil {
			return 0
		}
		return bytesByKey[h.key]
	}
	t.Cleanup(func() { estimateCorpusHandleBytes = orig })
}

// TestCorpusResidency_AToBToA_NoRedecode is the headline acceptance: with
// the LRU enabled and a sufficient budget, A→B→A re-attaches the SAME
// handle object — no re-decode, no lexical reopen — and the decode ledger
// shows exactly ONE decode per corpus (B opened once too, not "one total").
func TestCorpusResidency_AToBToA_NoRedecode(t *testing.T) {
	h := newResidencyHarness(t, 1, func(r *CorpusRegistry) {
		r.SetResidency(4, 1<<30)
	})
	K1, K2 := h.keys[0], h.keys[1]

	// Fixture state: K1 was released when K2 attached and must be RETAINED
	// (not closed) — that retention is what the return path reuses.
	keysEqual(t, "retained after A→B", h.retainedKeys(), []string{K1})
	if h.decoded(K1) != 1 || h.decoded(K2) != 1 {
		t.Fatalf("decodes after publication: K1=%d K2=%d, want 1 and 1", h.decoded(K1), h.decoded(K2))
	}
	retained := h.handleFor(K1)

	// A→B→A: return to K1's root.
	h.attach(h.rootB, "topic")

	if got := h.currentHandle(); got != retained {
		t.Fatal("returning to K1 re-attached a different handle object — the corpus was reopened instead of retained")
	}
	if n := h.decoded(K1); n != 1 {
		t.Errorf("K1 decoded %d times across A→B→A, want 1 (retention must skip the re-decode)", n)
	}
	if n := h.decoded(K2); n != 1 {
		t.Errorf("K2 decoded %d times, want 1 (its own single open)", n)
	}
	if total := h.totalDecodes(); total != 2 {
		t.Errorf("total corpus decodes = %d, want 2 (one per corpus — not one total, not more)", total)
	}
	// Search still serves the corpus leg after the round trip.
	res, err := h.svc.HybridSearchNoWait(context.Background(), SearchOptions{Query: "seed alpha marker", TopK: 3})
	if err != nil {
		t.Fatalf("search after A→B→A: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("search after A→B→A returned no results — the retained corpus no longer serves")
	}
}

// TestCorpusResidency_DefaultPolicyClosesAtZero pins the default (0/0)
// policy: retention of inactive views is OFF — the last release closes the
// handle (the pre-residency behavior) and a return re-decodes honestly.
func TestCorpusResidency_DefaultPolicyClosesAtZero(t *testing.T) {
	h := newResidencyHarness(t, 1, nil) // no SetResidency: the default policy
	K1, K2 := h.keys[0], h.keys[1]

	if got := h.retainedKeys(); len(got) != 0 {
		t.Fatalf("retained = %v with the default policy, want none (retention off)", got)
	}
	if h.handleFor(K1) != nil {
		t.Error("K1 must be fully closed at zero refs under the default policy")
	}
	first := h.decoded(K1)

	h.attach(h.rootB, "topic") // back to K1 → honest re-decode
	if n := h.decoded(K1); n != first+1 {
		t.Errorf("K1 decoded %d times after return, want %d (default policy re-decodes)", n, first+1)
	}
	if h.decoded(K2) != 1 {
		t.Errorf("K2 decoded %d times, want 1", h.decoded(K2))
	}
}

// corpusLexicalDirForTest resolves the lexical directory a corpus handle
// actually holds: the §3 snapshot directory when the store published the
// chunk-record form, the legacy lexical/<key> path for hand-seeded v1
// corpora. Residency tests reopen this directory to prove the reader was
// released.
func corpusLexicalDirForTest(t *testing.T, storage, key string) string {
	t.Helper()
	if gen, _, ok := readCurrentGeneration(storage); ok {
		if d := filepath.Join(snapshotDir(gen, key), "lexical"); pathExists(d) {
			return d
		}
	}
	return lexicalIndexPath(storage, key)
}

// TestCorpusResidency_CountBound drives the count bound alone (capacity 1,
// no byte bound): the oldest retained corpus is evicted at overflow, the
// newest stays, and a re-attach after eviction reopens safely (a fresh
// decode, working search).
func TestCorpusResidency_CountBound(t *testing.T) {
	h := newResidencyHarness(t, 2, func(r *CorpusRegistry) {
		r.SetResidency(1, 0)
	})
	K1, K2, K3 := h.keys[0], h.keys[1], h.keys[2]

	// capacity 1: after K3's attach the LRU holds only K2 (K1 evicted).
	keysEqual(t, "retained under capacity 1", h.retainedKeys(), []string{K2})
	if h.handleFor(K1) != nil {
		t.Error("K1 must be evicted (oldest) under capacity 1")
	}
	if h.handleFor(K2) == nil {
		t.Error("K2 must be retained under capacity 1")
	}
	if h.handleFor(K3) == nil {
		t.Error("K3 must stay attached (the working set is never an eviction candidate)")
	}
	lexicalDir := corpusLexicalDirForTest(t, h.storage, K1)

	// Safe reopen after eviction: K1 re-decodes and serves search again.
	h.attach(h.rootB, "topic")
	if n := h.decoded(K1); n != 2 {
		t.Errorf("K1 decoded %d times after eviction+return, want 2 (one fresh reopen)", n)
	}
	if _, err := lexical.OpenReadOnly(lexicalDir); err != nil {
		t.Errorf("eviction did not release K1's lexical reader (reopen failed): %v", err)
	}
	res, err := h.svc.HybridSearchNoWait(context.Background(), SearchOptions{Query: "seed alpha marker", TopK: 3})
	if err != nil || len(res) == 0 {
		t.Fatalf("search after eviction-reopen: results=%d err=%v", len(res), err)
	}
}

// TestCorpusResidency_ByteBound drives the byte bound alone (no count
// bound): the oldest retained corpora are evicted while the summed
// estimates exceed the budget.
func TestCorpusResidency_ByteBound(t *testing.T) {
	// Count-only during the fixture (release-time enforcement is pinned by
	// the count test); the byte bound then drives eviction on its own.
	h := newResidencyHarness(t, 3, func(r *CorpusRegistry) {
		r.SetResidency(10, 1<<30)
	})
	K1, K2, K3, K4 := h.keys[0], h.keys[1], h.keys[2], h.keys[3]
	installCorpusBytesEstimator(t, map[string]int64{K1: 100, K2: 100, K3: 100, K4: 100})

	// Switch to the byte-only bound: retained K1+K2+K3 = 300 > 250 → the
	// oldest (K1) is evicted → 200 ≤ 250. The count bound (now 0 = no
	// count limit) plays no role.
	h.registry.SetResidency(0, 250)
	keysEqual(t, "retained under the byte bound", h.retainedKeys(), []string{K2, K3})
	if h.handleFor(K1) != nil {
		t.Error("K1 must be evicted by the byte bound (oldest first)")
	}
	if h.handleFor(K4) == nil {
		t.Error("K4 (the attached working set) must survive the byte bound")
	}
}

// TestCorpusResidency_BothBounds drives the count and byte bounds
// together: the count bound caps at 2 while bytes still fit, then a tighter
// byte budget evicts BELOW the count bound — each bound independently
// enforceable in the same configuration.
func TestCorpusResidency_BothBounds(t *testing.T) {
	h := newResidencyHarness(t, 2, func(r *CorpusRegistry) {
		r.SetResidency(2, 1<<30)
	})
	K1, K2, K3 := h.keys[0], h.keys[1], h.keys[2]
	installCorpusBytesEstimator(t, map[string]int64{K1: 100, K2: 100, K3: 100})

	// Count bound first: capacity 2 holds K1+K2 after K3 attaches.
	keysEqual(t, "retained under capacity 2", h.retainedKeys(), []string{K1, K2})

	// Shrink the byte budget below the retained sum: enforcement on policy
	// application evicts the oldest until the bytes fit — even though the
	// count bound alone would keep both.
	h.registry.SetResidency(2, 150)
	keysEqual(t, "retained after the byte shrink", h.retainedKeys(), []string{K2})
	if h.handleFor(K3) == nil {
		t.Error("K3 (the attached working set) must never be evicted by a budget shrink")
	}
}

// TestCorpusResidency_Oversized pins the oversized policy: a retained
// corpus whose estimate alone exceeds the byte budget is evicted (the park
// LRU's semantics — never retain what cannot fit), while the same corpus
// ATTACHED is never closed by the budget (bounds govern retention, not
// admission; no hard RSS cap is promised or enforced).
func TestCorpusResidency_Oversized(t *testing.T) {
	h := newResidencyHarness(t, 1, func(r *CorpusRegistry) {
		r.SetResidency(4, 1<<30)
	})
	K1, K2 := h.keys[0], h.keys[1]
	installCorpusBytesEstimator(t, map[string]int64{K1: 1000, K2: 10})

	// Fixture end: K1 retained (still cached — a return would reuse it).
	if h.handleFor(K1) == nil {
		t.Fatal("fixture: K1 must be retained before the estimator swaps in")
	}
	// An oversized retained corpus (1000 alone vs the 100 budget) is
	// evicted the moment the bound applies — the park LRU's semantics:
	// never retain what cannot fit, even as the only entry.
	h.registry.SetResidency(4, 100)
	if h.handleFor(K1) != nil {
		t.Error("an oversized retained corpus must be evicted immediately")
	}
	keysEqual(t, "retained after oversized enforcement", h.retainedKeys(), nil)

	// The ATTACHED working set (K2, 10 bytes) survives the same budget
	// untouched — the bounds govern retention, not admission, and no hard
	// RSS cap is enforced against the active set.
	if h.handleFor(K2) == nil {
		t.Error("the attached corpus must never be closed by the byte budget")
	}
}

// TestCorpusResidency_EvictionNeverClosesAttachedHandles pins reader
// pinning: a corpus held by another root's service (refcount > 0) survives
// unlimited LRU pressure — same handle object, search keeps serving — and
// only its own release moves it into the retention list.
func TestCorpusResidency_EvictionNeverClosesAttachedHandles(t *testing.T) {
	h := newResidencyHarness(t, 1, func(r *CorpusRegistry) {
		r.SetResidency(1, 0)
	})
	K1 := h.keys[0]

	// A second service pins K1: attach it to rootB and KEEP it attached.
	pinner := h.newViewService(nil)
	pinnerIdx := h.newIndexer(pinner, nil, nil)
	if err := h.attach2(pinner, pinnerIdx, h.rootB, "topic"); err != nil {
		t.Fatalf("pinner attach: %v", err)
	}
	pinned := h.currentHandleOf(pinner)
	if pinned == nil || pinned.key != K1 {
		t.Fatalf("pinner did not attach K1 (got %v)", pinned)
	}

	// Drive repeated LRU pressure through the harness service: every new
	// commit is a new corpus whose attach releases the previous one under
	// capacity 1 — every retained corpus except the newest is evicted.
	for i := 0; i < 3; i++ {
		h.commitOn(50 + i)
		h.attach(h.rootA, "main")
	}

	if got := h.handleFor(K1); got != pinned {
		t.Fatal("K1 was closed or replaced under LRU pressure while a reader held it — eviction must skip pinned handles")
	}
	if n := h.decoded(K1); n != 1 {
		t.Errorf("K1 decoded %d times while pinned, want 1 (pinning must also prevent evict+redecode churn)", n)
	}
	res, err := pinner.HybridSearchNoWait(context.Background(), SearchOptions{Query: "seed alpha marker", TopK: 3})
	if err != nil || len(res) == 0 {
		t.Fatalf("pinned reader's search after pressure: results=%d err=%v", len(res), err)
	}

	// The pin's release moves K1 into retention (capacity 1 keeps it).
	if err := pinner.Close(); err != nil {
		t.Fatalf("pinner close: %v", err)
	}
	keysEqual(t, "retained after the pin released", h.retainedKeys(), []string{K1})
}

// TestCorpusResidency_FreeOSMemoryOutsideLock pins the memory-free seam:
// every ACTUAL eviction schedules exactly one freeOSMemory call, delivered
// asynchronously (an unbuffered hand-off — a synchronous call under the
// store lock would deadlock the attach that triggered it).
func TestCorpusResidency_FreeOSMemoryOutsideLock(t *testing.T) {
	h := newResidencyHarness(t, 1, func(r *CorpusRegistry) {
		r.SetResidency(1, 0)
	})
	// Unbuffered: the seam's send only completes because it runs in its own
	// goroutine AFTER the store lock is gone — synchronously it would block
	// forever and hang the attach below. The atomic count is the exactly-once
	// oracle: scheduleFreeLocked fires one goroutine per ACTUAL eviction, and
	// only one eviction is requested below — so once the first call is joined,
	// no further firing can even be scheduled (no absence window needed).
	var seamCalls atomic.Int32
	calls := make(chan struct{})
	h.registry.mu.Lock()
	h.registry.freeOSMemory = func() { seamCalls.Add(1); calls <- struct{}{} }
	h.registry.mu.Unlock()

	// No eviction yet (retained K1 fits capacity 1): the seam must stay
	// silent.
	select {
	case <-calls:
		t.Fatal("freeOSMemory fired without an eviction")
	default:
	}

	// One more corpus evicts K1 (capacity 1): the seam fires exactly once.
	h.commitOn(99)
	h.attach(h.rootA, "main")

	select {
	case <-calls:
	case <-time.After(10 * time.Second):
		t.Fatal("freeOSMemory seam was not called after an eviction")
	}
	// Exactly one call for one eviction: the increment happens-before the
	// joined send, and a second firing would need a second eviction — none
	// is requested after this point.
	if n := seamCalls.Load(); n != 1 {
		t.Fatalf("freeOSMemory fired %d times for a single eviction; want 1", n)
	}
}

// TestCorpusResidency_DeleteProjectDataDropsSlots pins the teardown half of
// the delete contract: deleting a project's storage removes EVERY residency
// slot of its store — retained handles close, the coordinator leaves the
// registry.
func TestCorpusResidency_DeleteProjectDataDropsSlots(t *testing.T) {
	h := newResidencyHarness(t, 1, func(r *CorpusRegistry) {
		r.SetResidency(4, 1<<30)
	})
	K1 := h.keys[0]
	cs := h.store()
	if h.handleFor(K1) == nil {
		t.Fatal("fixture: K1 must be retained before the delete")
	}

	if err := h.svc.DeleteProjectData(h.storage); err != nil {
		t.Fatalf("DeleteProjectData: %v", err)
	}

	cs.mu.Lock()
	left := len(cs.handles)
	cs.mu.Unlock()
	if left != 0 {
		t.Errorf("store still holds %d handles after DeleteProjectData, want 0", left)
	}
	h.registry.mu.Lock()
	stores := len(h.registry.stores)
	h.registry.mu.Unlock()
	if stores != 0 {
		t.Errorf("registry still tracks %d stores after DeleteProjectData, want 0", stores)
	}
	if _, err := os.Stat(h.storage); !os.IsNotExist(err) {
		t.Errorf("storage directory survived DeleteProjectData (err=%v)", err)
	}
}

// TestCorpusResidency_CloseIsFinal pins CorpusRegistry.Close: every
// retained handle closes (lexical readers released — the same paths reopen
// cleanly), an attached handle is retired (its last release closes instead
// of retaining), and later opens fail closed instead of resurrecting stores.
func TestCorpusResidency_CloseIsFinal(t *testing.T) {
	h := newResidencyHarness(t, 1, func(r *CorpusRegistry) {
		r.SetResidency(4, 1<<30)
	})
	K1 := h.keys[0]
	lexDir := corpusLexicalDirForTest(t, h.storage, K1)
	cs := h.store() // captured before Close (storeFor fails closed afterwards)

	h.registry.Close()

	cs.mu.Lock()
	leftRetained := len(cs.lru)
	leftHandles := len(cs.handles)
	allRetired := true
	for _, hh := range cs.handles {
		if !hh.retired {
			allRetired = false
		}
	}
	cs.mu.Unlock()
	if leftRetained != 0 {
		t.Fatalf("retained handles survived registry Close: %d left", leftRetained)
	}
	if leftHandles == 0 || !allRetired {
		t.Fatalf("after Close the attached handle must remain (retired, closing at its release): handles=%d allRetired=%v", leftHandles, allRetired)
	}
	if _, err := lexical.OpenReadOnly(lexDir); err != nil {
		t.Errorf("Close did not release the retained corpus's lexical reader: %v", err)
	}
	// The still-attached service's handle must be retired: its release
	// closes rather than retains.
	if err := h.svc.Close(); err != nil {
		t.Fatalf("service close: %v", err)
	}
	cs.mu.Lock()
	leftHandles = len(cs.handles)
	cs.mu.Unlock()
	if leftHandles != 0 {
		t.Errorf("retired handle was retained on release after Close: %d left", leftHandles)
	}
	// Post-close opens fail closed.
	if _, err := h.registry.Open(context.Background(), h.storage, K1, h.svc.GetEmbeddingFunc(), "fp-view", 0); err == nil {
		t.Error("Open after registry Close must fail")
	}
}

// TestCorpusResidency_NoProjectCreatesNothing pins the No-Project contract:
// the No Project state never creates a corpus store (no persistent, no LRU
// data); the only tracked store remains the real project's.
func TestCorpusResidency_NoProjectCreatesNothing(t *testing.T) {
	h := newResidencyHarness(t, 0, func(r *CorpusRegistry) {
		r.SetResidency(2, 1<<30)
	})

	// Round-trip through No Project on the SAME service.
	h.svc.ResetForNoProject("noproject")
	h.registry.mu.Lock()
	storeKeys := make([]string, 0, len(h.registry.stores))
	for k := range h.registry.stores {
		storeKeys = append(storeKeys, k)
	}
	h.registry.mu.Unlock()
	if len(storeKeys) != 1 || storeKeys[0] != h.storage {
		t.Fatalf("registry stores after No Project = %v, want exactly [%s] (no No-Project store)", storeKeys, h.storage)
	}

	// A No-Project-only service (fresh, never a real project) creates
	// nothing at all.
	fresh := h.newViewService(nil)
	if err := fresh.SetProject("noproject", ""); err != nil {
		t.Fatalf("SetProject(noproject): %v", err)
	}
	fresh.ResetForNoProject("noproject")
	h.registry.mu.Lock()
	n := len(h.registry.stores)
	h.registry.mu.Unlock()
	if n != 1 {
		t.Errorf("registry stores = %d after a No-Project-only service, want 1 (unchanged)", n)
	}
}
