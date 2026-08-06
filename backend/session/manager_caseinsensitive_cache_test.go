package session

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestDetectCaseInsensitive_CachedPerRoot verifies the case-sensitivity probe
// runs at most once per resolved root: the first call probes the filesystem,
// every subsequent call returns the cached bool without touching the filesystem
// again. Case-sensitivity is a mount-level property that cannot change
// mid-session, so a per-message probe (as happened before the cache) only
// churned the repo with CaseSense-*.probe files.
//
// The assertion is OS-independent: it overrides detectCaseInsensitiveFn with a
// spy that counts invocations and asserts the probe runs exactly once regardless
// of whether the host FS is case-sensitive or case-insensitive. (A chmod-based
// heuristic only catches a re-probe on case-insensitive FSes — a no-op on Linux.)
func TestDetectCaseInsensitive_CachedPerRoot(t *testing.T) {
	m, _, _ := testManager(t)
	root := t.TempDir()

	// Spy: delegate to the real probe but count how many times it runs.
	var calls int
	m.detectCaseInsensitiveFn = func(p string) bool {
		calls++
		return defaultDetectCaseInsensitive(p)
	}

	// Expected outcome is whatever the host filesystem actually reports.
	want := defaultDetectCaseInsensitive(root)

	// First call probes the filesystem and stores the result.
	got := m.detectCaseInsensitive(root)
	if got != want {
		t.Fatalf("first detectCaseInsensitive(%q) = %v, want %v", root, got, want)
	}
	if calls != 1 {
		t.Fatalf("probe invoked %d times after first call, want exactly 1", calls)
	}

	// Repeated calls must return the cached value WITHOUT re-probing.
	for i := 2; i <= 5; i++ {
		if got2 := m.detectCaseInsensitive(root); got2 != got {
			t.Fatalf("call #%d returned %v, want cached %v", i, got2, got)
		}
	}
	if calls != 1 {
		t.Fatalf("probe invoked %d times after repeated calls, want still 1 (cache short-circuited)", calls)
	}

	// Confirm the entry landed in the cache under the resolved key.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	cached, ok := m.caseInsensitiveCache.Load(resolved)
	if !ok {
		t.Fatal("expected case-sensitivity to be cached after first call")
	}
	if ci, _ := caseInsensitiveCachedValue(cached); ci != want {
		t.Fatalf("cached value = %v, want %v", ci, want)
	}
}

// TestDetectCaseInsensitive_DistinctRootsIndependent verifies that distinct
// roots are probed and cached independently — a cached result for one root
// does not leak as the answer for another.
func TestDetectCaseInsensitive_DistinctRootsIndependent(t *testing.T) {
	m, _, _ := testManager(t)
	rootA := t.TempDir()
	rootB := t.TempDir()
	resolvedA, _ := filepath.EvalSymlinks(rootA)
	resolvedB, _ := filepath.EvalSymlinks(rootB)

	want := runtime.GOOS == "darwin" || runtime.GOOS == "windows"

	if got := m.detectCaseInsensitive(rootA); got != want {
		t.Fatalf("detectCaseInsensitive(%q) = %v, want %v", rootA, got, want)
	}
	if got := m.detectCaseInsensitive(rootB); got != want {
		t.Fatalf("detectCaseInsensitive(%q) = %v, want %v", rootB, got, want)
	}

	if _, ok := m.caseInsensitiveCache.Load(resolvedA); !ok {
		t.Fatal("root A not cached")
	}
	if _, ok := m.caseInsensitiveCache.Load(resolvedB); !ok {
		t.Fatal("root B not cached")
	}
}

// TestDetectCaseInsensitive_EmptyPathReturnsFalseWithoutCaching verifies the
// fail-safe contract for No-Project sessions (no workspace): an empty path
// returns the case-sensitive default (false) without storing anything, so the
// cache is not polluted with a sentinel key.
func TestDetectCaseInsensitive_EmptyPathReturnsFalseWithoutCaching(t *testing.T) {
	m, _, _ := testManager(t)

	if got := m.detectCaseInsensitive(""); got != false {
		t.Fatalf("detectCaseInsensitive(\"\") = %v, want false", got)
	}

	m.caseInsensitiveCache.Range(func(key, _ any) bool {
		t.Fatalf("cache must stay empty for empty path, found key %v", key)
		return false
	})
}

// TestDetectCaseInsensitive_NonExistentClimbsToAncestor verifies that a
// non-existent workspace path is resolved via its nearest existing ancestor
// (the probe climbs), so the cached key is the physical ancestor and the
// returned value reflects the real filesystem — not a silent false.
func TestDetectCaseInsensitive_NonExistentClimbsToAncestor(t *testing.T) {
	m, _, _ := testManager(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "DoesNotExist", "subdir", "leaf")

	want := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	if got := m.detectCaseInsensitive(target); got != want {
		t.Fatalf("detectCaseInsensitive(%q) = %v, want %v (GOOS=%s)", target, got, want, runtime.GOOS)
	}

	// Something must be cached (the resolved ancestor, not the bare target).
	found := false
	m.caseInsensitiveCache.Range(func(_, value any) bool {
		found = true
		if ci, _ := caseInsensitiveCachedValue(value); ci != want {
			t.Errorf("cached value = %v, want %v", ci, want)
		}
		return false
	})
	if !found {
		t.Fatal("expected a cached entry after probing via ancestor")
	}
}
