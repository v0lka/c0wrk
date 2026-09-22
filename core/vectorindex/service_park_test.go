package vectorindex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
)

// persistentDBOpenCounter counts chromem.NewPersistentDB opens per cleaned
// path so the park tests can prove that restoring a parked project does NOT
// reopen (and therefore re-gob-decode) its persistent DB.
type persistentDBOpenCounter struct {
	mu    sync.Mutex
	opens map[string]int
}

// installPersistentDBOpenCounter swaps the package-level newPersistentDB seam
// for a counting wrapper and restores the original on test cleanup. Safe
// because the package's tests run sequentially (only the Telemetry tests opt
// into t.Parallel, and they never touch this seam).
func installPersistentDBOpenCounter(t *testing.T) *persistentDBOpenCounter {
	t.Helper()
	c := &persistentDBOpenCounter{opens: make(map[string]int)}
	orig := newPersistentDB
	newPersistentDB = func(path string, compress bool) (*chromem.DB, error) {
		c.mu.Lock()
		c.opens[filepath.Clean(path)]++
		c.mu.Unlock()
		return orig(path, compress)
	}
	t.Cleanup(func() { newPersistentDB = orig })
	return c
}

func (c *persistentDBOpenCounter) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens[filepath.Clean(path)]
}

func (c *persistentDBOpenCounter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.opens {
		n += v
	}
	return n
}

// parkAddDocs opens a write-locked AddDocuments call with a single document so
// the state gains a collection entry and a file-hash sidecar entry.
func parkAddDocs(t *testing.T, svc *Service, dir, name, hash string) {
	t.Helper()
	docs := []chromem.Document{{
		ID:       name + ":0",
		Content:  name,
		Metadata: map[string]string{"file_path": filepath.Join(dir, name), "content_hash": hash},
	}}
	svc.AcquireWriteLock()
	defer svc.ReleaseWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		t.Fatalf("AddDocuments: %v", err)
	}
}

// TestService_ParkRestoreSkipsReopen is the A→B→A acceptance test: with parking
// enabled, returning to A restores the SAME chromem collection, lexical index,
// and DB objects and never reopens A's persistent DB.
func TestService_ParkRestoreSkipsReopen(t *testing.T) {
	counter := installPersistentDBOpenCounter(t)

	// Storage roots first, close-cleanup second: t.Cleanup is LIFO, so the
	// TempDir RemoveAll must be registered BEFORE svc.Close for Close to run
	// first and release the lexical bolt/zap handles — an open handle fails
	// the unlink on Windows.
	dirA := filepath.Join(t.TempDir(), "A")
	dirB := filepath.Join(t.TempDir(), "B")

	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc(), ParkCapacity: 2})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	// Open and populate A.
	if err := svc.SetProject("A", dirA); err != nil {
		t.Fatalf("SetProject A: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch A: %v", err)
	}
	parkAddDocs(t, svc, dirA, "a.go", "hashA")

	colA, lexA, dbA := svc.current.collection, svc.current.lexical, svc.current.db
	if colA == nil || lexA == nil || dbA == nil {
		t.Fatalf("A not fully open: collection=%v lexical=%v db=%v", colA != nil, lexA != nil, dbA != nil)
	}

	// Park A by switching to B.
	if err := svc.SetProject("B", dirB); err != nil {
		t.Fatalf("SetProject B: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch B: %v", err)
	}
	if len(svc.parked) != 1 || svc.parked[0].projectID != "A" {
		t.Fatalf("parked = %d entries, want [A]", len(svc.parked))
	}

	// Return to A: must restore, not reopen.
	if err := svc.SetProject("A", dirA); err != nil {
		t.Fatalf("SetProject A (restore): %v", err)
	}

	if svc.current.collection != colA {
		t.Error("restore did not reuse the chromem collection object")
	}
	if svc.current.lexical != lexA {
		t.Error("restore did not reuse the lexical index object")
	}
	if svc.current.db != dbA {
		t.Error("restore did not reuse the chromem DB object")
	}
	if got := counter.count(branchRootPath(dirA, "main")); got != 1 {
		t.Errorf("persistent DB for A opened %d times, want 1 (restore must not reopen)", got)
	}
	if got := counter.total(); got != 2 {
		t.Errorf("total persistent DB opens = %d, want 2 (A once, B once)", got)
	}
	if got := svc.current.collection.Count(); got != 1 {
		t.Errorf("restored collection has %d docs, want 1", got)
	}
	if svc.current.fileHashes[filepath.Join(dirA, "a.go")] == "" {
		t.Error("restored sidecar lost A's file entry")
	}
	// B is now parked.
	if len(svc.parked) != 1 || svc.parked[0].projectID != "B" {
		t.Errorf("parked = %v, want [B]", parkedIDs(svc))
	}
}

func parkedIDs(svc *Service) []string {
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	ids := make([]string, 0, len(svc.parked))
	for _, ps := range svc.parked {
		ids = append(ids, ps.projectID)
	}
	return ids
}

// TestService_ParkEvictionFlushesSidecar verifies that overflowing the park LRU
// evicts the oldest state and flushes its file-hash sidecar to disk.
func TestService_ParkEvictionFlushesSidecar(t *testing.T) {
	// Storage roots before the Close-cleanup registration (t.Cleanup is
	// LIFO): svc.Close must release the lexical handles before the TempDir
	// RemoveAll, or the unlink fails on Windows.
	base := t.TempDir()
	dirA, dirB, dirC := filepath.Join(base, "A"), filepath.Join(base, "B"), filepath.Join(base, "C")

	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc(), ParkCapacity: 1})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("A", dirA); err != nil {
		t.Fatalf("SetProject A: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch A: %v", err)
	}
	parkAddDocs(t, svc, dirA, "a.go", "hashA")

	// Park A (capacity 1 → LRU holds A).
	if err := svc.SetProject("B", dirB); err != nil {
		t.Fatalf("SetProject B: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch B: %v", err)
	}

	sidecarA := filepath.Join(dirA, "file_hashes_branch_main.json")
	if err := os.Remove(sidecarA); err != nil {
		t.Fatalf("removing A sidecar to make the eviction flush observable: %v", err)
	}

	// Park B → LRU overflow → A evicted (sidecar flushed from its in-memory map).
	if err := svc.SetProject("C", dirC); err != nil {
		t.Fatalf("SetProject C: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch C: %v", err)
	}

	if ids := parkedIDs(svc); len(ids) != 1 || ids[0] != "B" {
		t.Fatalf("parked = %v, want [B] (A must be evicted)", ids)
	}

	data, err := os.ReadFile(sidecarA)
	if err != nil {
		t.Fatalf("evicted A's sidecar was not flushed to disk: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("evicted sidecar is not valid JSON: %v", err)
	}
	if m[filepath.Join(dirA, "a.go")] == "" {
		t.Errorf("evicted sidecar is missing A's entry; got %v", m)
	}
}

// TestService_ParkDisabledReopens verifies that park_capacity=0 reproduces the
// historical behaviour: every switch reopens the persistent DB and nothing is
// parked.
func TestService_ParkDisabledReopens(t *testing.T) {
	counter := installPersistentDBOpenCounter(t)

	// Storage roots before the Close-cleanup registration (t.Cleanup is
	// LIFO): svc.Close must release the lexical handles before the TempDir
	// RemoveAll, or the unlink fails on Windows.
	dirA := filepath.Join(t.TempDir(), "A")
	dirB := filepath.Join(t.TempDir(), "B")

	// ParkCapacity left at its zero value → parking disabled.
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("A", dirA); err != nil {
		t.Fatalf("SetProject A: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch A: %v", err)
	}
	parkAddDocs(t, svc, dirA, "a.go", "hashA")
	colA := svc.current.collection

	if err := svc.SetProject("B", dirB); err != nil {
		t.Fatalf("SetProject B: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch B: %v", err)
	}
	if err := svc.SetProject("A", dirA); err != nil {
		t.Fatalf("SetProject A (reopen): %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch A (reopen): %v", err)
	}

	if svc.current.collection == colA {
		t.Error("with parking disabled, A should have been reopened (new collection object)")
	}
	if got := counter.count(branchRootPath(dirA, "main")); got != 2 {
		t.Errorf("persistent DB for A opened %d times, want 2 (reopen expected)", got)
	}
	if len(svc.parked) != 0 {
		t.Errorf("parked = %d, want 0 when parking is disabled", len(svc.parked))
	}
}

// TestService_DeleteProjectDataDropsParked verifies that deleting a project's
// data also discards its parked slot (so a later switch back cannot restore a
// collection rooted at a removed directory).
func TestService_DeleteProjectDataDropsParked(t *testing.T) {
	// Storage roots before the Close-cleanup registration (t.Cleanup is
	// LIFO): svc.Close must release the lexical handles before the TempDir
	// RemoveAll, or the unlink fails on Windows.
	dirA := filepath.Join(t.TempDir(), "A")
	dirB := filepath.Join(t.TempDir(), "B")

	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc(), ParkCapacity: 2})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("A", dirA); err != nil {
		t.Fatalf("SetProject A: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch A: %v", err)
	}
	parkAddDocs(t, svc, dirA, "a.go", "hashA")

	// Park A by switching to B.
	if err := svc.SetProject("B", dirB); err != nil {
		t.Fatalf("SetProject B: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch B: %v", err)
	}
	if len(svc.parked) != 1 {
		t.Fatalf("parked = %d, want 1", len(svc.parked))
	}

	if err := svc.DeleteProjectData(dirA); err != nil {
		t.Fatalf("DeleteProjectData: %v", err)
	}
	if len(svc.parked) != 0 {
		t.Errorf("parked = %d after DeleteProjectData, want 0", len(svc.parked))
	}
	if _, err := os.Stat(dirA); !os.IsNotExist(err) {
		t.Errorf("A's storage directory still exists after delete (err=%v)", err)
	}
}

// TestService_CloseReleasesParked verifies Close releases the current AND every
// parked state's handles and clears the LRU, without hanging.
func TestService_CloseReleasesParked(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc(), ParkCapacity: 3})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	dirA := filepath.Join(t.TempDir(), "A")
	dirB := filepath.Join(t.TempDir(), "B")

	if err := svc.SetProject("A", dirA); err != nil {
		t.Fatalf("SetProject A: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch A: %v", err)
	}
	parkAddDocs(t, svc, dirA, "a.go", "hashA")

	// Park A, open B.
	if err := svc.SetProject("B", dirB); err != nil {
		t.Fatalf("SetProject B: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch B: %v", err)
	}
	parkedLex := svc.parked[0].lexical
	if len(svc.parked) != 1 || parkedLex == nil {
		t.Fatalf("expected A parked with an open lexical index")
	}

	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if svc.parked != nil {
		t.Errorf("parked not cleared on Close: %v", svc.parked)
	}
	if svc.current.collection != nil || svc.current.db != nil || svc.current.lexical != nil {
		t.Error("Close did not release the current state's handles")
	}
	// The parked lexical index must have been closed: reopening the same path
	// succeeds only when the previous handle was released.
	relPath := filepath.Join(dirA, "lexical", "main")
	if lex, err := lexical.Open(relPath); err != nil {
		t.Errorf("parked lexical index was not closed (reopen failed): %v", err)
	} else if lex != nil {
		_ = lex.Close()
	}
}

// TestService_ParkConcurrentSwitches hammers SetProject from many goroutines
// (with parking enabled) and asserts the LRU invariants survive: one current
// state, no duplicate parked projectIDs, and never more than parkCapacity
// parked slots. Run under -race it also guards the park/restore bookkeeping.
func TestService_ParkConcurrentSwitches(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc(), ParkCapacity: 2})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	base := t.TempDir()
	dirs := map[string]string{
		"A": filepath.Join(base, "A"),
		"B": filepath.Join(base, "B"),
		"C": filepath.Join(base, "C"),
	}
	order := []string{"A", "B", "C"}

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := order[i%len(order)]
			if err := svc.SetProject(id, dirs[id]); err != nil {
				t.Errorf("SetProject(%s): %v", id, err)
			}
		}(i)
	}
	wg.Wait()

	svc.mu.RLock()
	defer svc.mu.RUnlock()

	if svc.current == nil {
		t.Fatal("current is nil after concurrent switches")
	}
	if len(svc.parked) > svc.parkCapacity {
		t.Errorf("parked = %d, exceeds capacity %d", len(svc.parked), svc.parkCapacity)
	}
	seen := make(map[string]bool, len(svc.parked))
	for _, ps := range svc.parked {
		if seen[ps.projectID] {
			t.Errorf("duplicate parked state for project %q", ps.projectID)
		}
		seen[ps.projectID] = true
	}
	if svc.current.projectID != "" && seen[svc.current.projectID] {
		t.Errorf("current project %q is also present in the park LRU", svc.current.projectID)
	}
}

// installParkBudgetEstimator swaps the estimateStateBytes seam for a fixed
// per-project footprint so budget-eviction tests can simulate
// multi-hundred-MiB parked states without materializing them. Restores the
// original on cleanup. Safe because the package's tests run sequentially
// (only the Telemetry tests opt into t.Parallel, and they never touch seams).
func installParkBudgetEstimator(t *testing.T, bytesByID map[string]int64) {
	t.Helper()
	orig := estimateStateBytes
	estimateStateBytes = func(ps *projectState, _ int) int64 {
		if ps == nil {
			return 0
		}
		return bytesByID[ps.projectID]
	}
	t.Cleanup(func() { estimateStateBytes = orig })
}

// installFreeOSMemoryRecorder swaps the freeOSMemory seam for a recorder and
// returns a channel receiving one value per call. The eviction path invokes
// the seam in its own goroutine, so tests wait on the channel with a timeout
// (assertFreeOSMemoryCalled).
func installFreeOSMemoryRecorder(t *testing.T) <-chan struct{} {
	t.Helper()
	orig := freeOSMemory
	called := make(chan struct{}, 16)
	freeOSMemory = func() { called <- struct{}{} }
	t.Cleanup(func() { freeOSMemory = orig })
	return called
}

// assertFreeOSMemoryCalled waits for the asynchronous freeOSMemory nudge that
// follows an actual eviction.
func assertFreeOSMemoryCalled(t *testing.T, called <-chan struct{}) {
	t.Helper()
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("freeOSMemory seam was not called after an eviction")
	}
}

// parkBudgetSwitchProject drives the standard open-a-project flow used by the
// budget tests: SetProject + SwitchBranch, so the state gains a branch (and
// with it a flushable file-hash sidecar).
func parkBudgetSwitchProject(t *testing.T, svc *Service, id, dir string) {
	t.Helper()
	if err := svc.SetProject(id, dir); err != nil {
		t.Fatalf("SetProject %s: %v", id, err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch %s: %v", id, err)
	}
}

// TestService_ParkBudgetEvictsOldest is the byte-budget seam acceptance test:
// with every state estimated at ~600 MiB and a 1024 MiB budget, parking the
// second state (cumulative 1200 MiB > budget) evicts the OLDEST parked state
// while the freshly parked one stays; capacity 3 alone would have kept both.
func TestService_ParkBudgetEvictsOldest(t *testing.T) {
	sixHundredMiB := int64(600) << 20
	installParkBudgetEstimator(t, map[string]int64{
		"A": sixHundredMiB,
		"B": sixHundredMiB,
		"C": sixHundredMiB,
	})

	base := t.TempDir()
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:   fakeEmbeddingFunc(),
		ParkCapacity:    3, // generous: only the byte budget may evict
		ParkBudgetBytes: 1024 << 20,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	for _, id := range []string{"A", "B", "C"} {
		parkBudgetSwitchProject(t, svc, id, filepath.Join(base, id))
	}

	// Parking B left [A] (600 MiB ≤ 1024 MiB). Parking C pushed the sum to
	// 1200 MiB > budget, so the OLDEST (A) was evicted; B (fresh) remains
	// parked and C is current.
	if ids := parkedIDs(svc); len(ids) != 1 || ids[0] != "B" {
		t.Fatalf("parked = %v, want [B] (A must be evicted by the byte budget)", ids)
	}
	if svc.current.projectID != "C" {
		t.Fatalf("current = %q, want C", svc.current.projectID)
	}
}

// TestService_ParkBudgetNegativeDisablesByteBudget pins the negative disable
// sentinel at the service layer: with ParkBudgetBytes < 0 the byte estimate
// never evicts anything and the LRU is bounded by park_capacity alone.
func TestService_ParkBudgetNegativeDisablesByteBudget(t *testing.T) {
	sixHundredMiB := int64(600) << 20
	installParkBudgetEstimator(t, map[string]int64{
		"A": sixHundredMiB,
		"B": sixHundredMiB,
		"C": sixHundredMiB,
	})

	base := t.TempDir()
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:   fakeEmbeddingFunc(),
		ParkCapacity:    3,
		ParkBudgetBytes: -1,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	for _, id := range []string{"A", "B", "C"} {
		parkBudgetSwitchProject(t, svc, id, filepath.Join(base, id))
	}

	// The summed 1200 MiB estimate would exceed the 1024 MiB default budget,
	// but a negative budget disables the byte bound: nothing is evicted and
	// the capacity-3 LRU holds both A and B (C is current).
	if ids := parkedIDs(svc); len(ids) != 2 || ids[0] != "A" || ids[1] != "B" {
		t.Fatalf("parked = %v, want [A B] (negative budget: capacity alone applies)", ids)
	}
}

// TestService_ParkBudgetEvictionFlushesSidecarAndFreesMemory verifies the
// budget-eviction contract end to end: the evicted state's file-hash sidecar
// is flushed to disk (the same contract a capacity eviction honours) and the
// freeOSMemory seam is invoked asynchronously so the freed RAM is returned
// to the OS.
func TestService_ParkBudgetEvictionFlushesSidecarAndFreesMemory(t *testing.T) {
	called := installFreeOSMemoryRecorder(t)
	installParkBudgetEstimator(t, map[string]int64{
		"A": 100,
		"B": 1000,
		"C": 1,
	})

	base := t.TempDir()
	dirA := filepath.Join(base, "A")

	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:   fakeEmbeddingFunc(),
		ParkCapacity:    5, // generous: only the byte budget may evict
		ParkBudgetBytes: 1024,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	parkBudgetSwitchProject(t, svc, "A", dirA)
	parkAddDocs(t, svc, dirA, "a.go", "hashA")

	// Park A by switching to B (100 ≤ 1024: stays parked), then remove A's
	// sidecar so the budget eviction's flush is observable on disk.
	parkBudgetSwitchProject(t, svc, "B", filepath.Join(base, "B"))
	sidecarA := filepath.Join(dirA, "file_hashes_branch_main.json")
	if err := os.Remove(sidecarA); err != nil {
		t.Fatalf("removing A sidecar to make the eviction flush observable: %v", err)
	}

	// Park B by switching to C: 100 + 1000 > 1024 → A (oldest) evicted by the
	// byte budget alone (capacity 5 is nowhere near exceeded).
	parkBudgetSwitchProject(t, svc, "C", filepath.Join(base, "C"))

	if ids := parkedIDs(svc); len(ids) != 1 || ids[0] != "B" {
		t.Fatalf("parked = %v, want [B] (A must be evicted by the byte budget)", ids)
	}

	data, err := os.ReadFile(sidecarA)
	if err != nil {
		t.Fatalf("budget-evicted A's sidecar was not flushed to disk: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("evicted sidecar is not valid JSON: %v", err)
	}
	if m[filepath.Join(dirA, "a.go")] == "" {
		t.Errorf("budget-evicted sidecar is missing A's entry; got %v", m)
	}

	assertFreeOSMemoryCalled(t, called)
}

// TestService_ParkDisabledDespiteBudget pins that park_capacity: 0 (the
// zero value here) disables parking entirely even when a byte budget is
// configured: every switch reopens the persistent DB and nothing is parked.
func TestService_ParkDisabledDespiteBudget(t *testing.T) {
	counter := installPersistentDBOpenCounter(t)

	// Storage roots before the Close-cleanup registration (t.Cleanup is
	// LIFO): svc.Close must release the lexical handles before the TempDir
	// RemoveAll, or the unlink fails on Windows.
	dirA := filepath.Join(t.TempDir(), "A")
	dirB := filepath.Join(t.TempDir(), "B")

	// ParkCapacity left at its zero value (parking disabled) while a budget
	// is present: the budget must not resurrect parking.
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc(), ParkBudgetBytes: 1 << 20})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	parkBudgetSwitchProject(t, svc, "A", dirA)
	parkAddDocs(t, svc, dirA, "a.go", "hashA")

	parkBudgetSwitchProject(t, svc, "B", dirB)
	if len(svc.parked) != 0 {
		t.Errorf("parked = %d, want 0 (park_capacity 0 disables parking regardless of the budget)", len(svc.parked))
	}

	// Returning to A reopens it — there is no parked state to restore from.
	parkBudgetSwitchProject(t, svc, "A", dirA)
	if got := counter.count(branchRootPath(dirA, "main")); got != 2 {
		t.Errorf("persistent DB for A opened %d times, want 2 (reopen expected)", got)
	}
}
