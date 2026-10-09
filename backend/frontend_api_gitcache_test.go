package backend

import (
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/v0lka/c0wrk/backend/project"
)

// --- git cache (frontend_api_gitcache.go) tests ---
//
// Contract under test:
//   - GetGitStatus / ListDirectory must not spawn the same heavy git
//     subprocess twice within the cache TTL.
//   - Mutating git RPCs (funnelled through emitGitStatusChanged), the
//     workspace watcher debounce, ignore-rule edits, and a project switch must
//     each evict the relevant snapshot so the next read recomputes.

// newGitCacheTestAPI builds a FrontendAPI with an active (non-No-Project)
// project rooted at ws, which is what GetGitStatus/ListDirectory require before
// they consult the git caches.
func newGitCacheTestAPI(ws string) *FrontendAPI {
	f := &FrontendAPI{}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	f.activeProjectMu.Lock()
	f.activeProjectID = "gitcache-test-project"
	f.activeProjectPath = ws
	f.activeProjectMu.Unlock()
	return f
}

// TestGetGitStatus_CachedWithinTTL verifies that repeated GetGitStatus calls
// inside the TTL reuse one computed snapshot (one git spawn) instead of
// re-running git status --porcelain on every call.
func TestGetGitStatus_CachedWithinTTL(t *testing.T) {
	ws := t.TempDir()
	var calls atomic.Int32
	f := newGitCacheTestAPI(ws)
	f.gitStatusFn = func(string) (map[string]GitStatusEntry, error) {
		calls.Add(1)
		return map[string]GitStatusEntry{}, nil
	}

	if _, err := f.GetGitStatus(ws); err != nil {
		t.Fatalf("GetGitStatus #1: %v", err)
	}
	if _, err := f.GetGitStatus(ws); err != nil {
		t.Fatalf("GetGitStatus #2: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("git status spawned %d times within TTL, want 1", got)
	}
}

// TestGetGitStatus_RecomputedAfterInvalidation verifies that a mutating git RPC
// (which funnels through emitGitStatusChanged) evicts the cached snapshot so
// the next GetGitStatus spawns git again.
func TestGetGitStatus_RecomputedAfterInvalidation(t *testing.T) {
	ws := t.TempDir()
	var calls atomic.Int32
	f := newGitCacheTestAPI(ws)
	f.gitStatusFn = func(string) (map[string]GitStatusEntry, error) {
		calls.Add(1)
		return map[string]GitStatusEntry{}, nil
	}

	if _, err := f.GetGitStatus(ws); err != nil {
		t.Fatalf("GetGitStatus #1: %v", err)
	}
	// Every mutating git RPC (stage/unstage/commit/checkout/...) reaches its
	// event through emitGitStatusChanged when it succeeds.
	f.emitGitStatusChanged(ws)
	if _, err := f.GetGitStatus(ws); err != nil {
		t.Fatalf("GetGitStatus #2: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("git status spawned %d times, want 2 (recompute after invalidation)", got)
	}
}

// TestListDirectory_GitIgnoredPathsCachedOnce verifies that two back-to-back
// ListDirectory calls on a git repo compute the ignored-path set once: the
// second listing is served from the ignored-path cache.
func TestListDirectory_GitIgnoredPathsCachedOnce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ws := t.TempDir()
	// ListDirectory only consults the ignored-path cache for a real git repo
	// (isGitRepo), so the fixture must be one.
	gitRepoFixture(t, ws)

	var calls atomic.Int32
	f := newGitCacheTestAPI(ws)
	f.gitIgnoredFn = func(string) (map[string]bool, error) {
		calls.Add(1)
		return map[string]bool{}, nil
	}

	if _, err := f.ListDirectory(ws, false); err != nil {
		t.Fatalf("ListDirectory #1: %v", err)
	}
	if _, err := f.ListDirectory(ws, false); err != nil {
		t.Fatalf("ListDirectory #2: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("GitIgnoredPaths ran %d times across two ListDirectory calls, want 1", got)
	}
}

// TestInvalidateGitCachesOnWatcher covers the debounced watcher invalidation
// policy: any tree change stales the status snapshot, while only an
// ignore-rule file (.gitignore/.aiignore/.ignore) additionally stales the
// ignored-path snapshot.
func TestInvalidateGitCachesOnWatcher(t *testing.T) {
	root := "/repo"
	var statusCalls, ignoredCalls atomic.Int32
	f := &FrontendAPI{
		gitStatusFn: func(string) (map[string]GitStatusEntry, error) {
			statusCalls.Add(1)
			return map[string]GitStatusEntry{}, nil
		},
		gitIgnoredFn: func(string) (map[string]bool, error) {
			ignoredCalls.Add(1)
			return map[string]bool{}, nil
		},
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)

	// Prime both caches.
	if _, err := f.cachedGitStatus(root); err != nil {
		t.Fatalf("prime status: %v", err)
	}
	if _, err := f.cachedGitIgnoredPaths(root); err != nil {
		t.Fatalf("prime ignored: %v", err)
	}

	// A regular file change stales only the status snapshot.
	f.invalidateGitCachesOnWatcher(root, []string{filepath.Join(root, "main.go")})
	if _, err := f.cachedGitStatus(root); err != nil {
		t.Fatalf("status after regular change: %v", err)
	}
	if _, err := f.cachedGitIgnoredPaths(root); err != nil {
		t.Fatalf("ignored after regular change: %v", err)
	}
	if got := statusCalls.Load(); got != 2 {
		t.Fatalf("status must recompute after a tree change: spawned %d, want 2", got)
	}
	if got := ignoredCalls.Load(); got != 1 {
		t.Fatalf("ignored must stay cached after a regular change: spawned %d, want 1", got)
	}

	// An ignore-rule edit stales the ignored snapshot too.
	f.invalidateGitCachesOnWatcher(root, []string{filepath.Join(root, ".gitignore")})
	if _, err := f.cachedGitIgnoredPaths(root); err != nil {
		t.Fatalf("ignored after .gitignore change: %v", err)
	}
	if got := ignoredCalls.Load(); got != 2 {
		t.Fatalf("ignored must recompute after a .gitignore change: spawned %d, want 2", got)
	}
}

// TestSwitchProject_InvalidatesGitCaches verifies that switching projects drops
// both per-repo caches wholesale, so no snapshot from the previous workspace
// can be served against the new one.
func TestSwitchProject_InvalidatesGitCaches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH (CODE-mode SwitchProject requires it)")
	}
	h := newProjectSwitchHarness(t)
	// Same activation-path scaffolding the sibling SwitchProject tests use:
	// a builder override (the harness Application has no real builder, so
	// switchProjectActivate would otherwise panic on a typed-nil builder), an
	// event sink, a seeded session, and watcher cleanup.
	prepareAtomicSwitchHarness(t, h)
	defer h.close(t)

	// Seed both caches with stale entries.
	h.api.gitStatusCacheMu.Lock()
	h.api.gitStatusCache = map[string]gitStatusCacheEntry{"stale": {}}
	h.api.gitStatusCacheMu.Unlock()
	h.api.gitIgnoredCacheMu.Lock()
	h.api.gitIgnoredCache = map[string]gitIgnoredCacheEntry{"stale": {}}
	h.api.gitIgnoredCacheMu.Unlock()

	if err := h.api.SwitchProject(h.projectID); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}

	h.api.gitStatusCacheMu.Lock()
	statusCleared := h.api.gitStatusCache == nil
	h.api.gitStatusCacheMu.Unlock()
	h.api.gitIgnoredCacheMu.Lock()
	ignoredCleared := h.api.gitIgnoredCache == nil
	h.api.gitIgnoredCacheMu.Unlock()

	if !statusCleared || !ignoredCleared {
		t.Fatalf("SwitchProject must drop both git caches: status cleared=%v, ignored cleared=%v", statusCleared, ignoredCleared)
	}
}

// TestGitCaches_NoProjectUntouched pins the No Project contract: with the No
// Project pseudo-project active, isGitRepo reports "not a repo" without writing
// to the git-repo cache (and, by extension, neither git-status nor ignored
// cache is ever populated for a session workspace).
func TestGitCaches_NoProjectUntouched(t *testing.T) {
	f := &FrontendAPI{}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	f.activeProjectMu.Lock()
	f.activeProjectID = project.NoProjectID
	f.activeProjectMu.Unlock()

	if f.isGitRepo(t.TempDir()) {
		t.Fatal("No Project must never be treated as a git repo")
	}
	if f.gitRepoCache != nil || f.gitStatusCache != nil || f.gitIgnoredCache != nil {
		t.Fatalf("No Project must not populate git caches: repo=%v status=%v ignored=%v",
			f.gitRepoCache, f.gitStatusCache, f.gitIgnoredCache)
	}
}

// TestCachedGitMaps_AreCallerOwned pins review [C3-5]: the maps handed out by
// the git caches are shallow copies (maps.Clone) at the cache boundary — a
// caller mutating its returned map must never corrupt the cached snapshot
// served to other readers within the TTL.
func TestCachedGitMaps_AreCallerOwned(t *testing.T) {
	ws := t.TempDir()
	var statusCalls, ignoredCalls atomic.Int32
	f := newGitCacheTestAPI(ws)
	f.gitStatusFn = func(string) (map[string]GitStatusEntry, error) {
		statusCalls.Add(1)
		return map[string]GitStatusEntry{"a.txt": {Status: "M"}}, nil
	}
	f.gitIgnoredFn = func(string) (map[string]bool, error) {
		ignoredCalls.Add(1)
		return map[string]bool{"build/": true}, nil
	}

	// Miss path: the freshly computed snapshot is stored, the caller gets a copy.
	first, err := f.cachedGitStatus(ws)
	if err != nil {
		t.Fatalf("cachedGitStatus #1: %v", err)
	}
	first["caller-added.txt"] = GitStatusEntry{Status: "M"}
	delete(first, "a.txt")

	// Hit path: served from the cache — must reflect neither mutation.
	second, err := f.cachedGitStatus(ws)
	if err != nil {
		t.Fatalf("cachedGitStatus #2: %v", err)
	}
	if _, polluted := second["caller-added.txt"]; polluted {
		t.Error("status cache polluted by caller mutation of a previously returned map")
	}
	if _, missing := second["a.txt"]; !missing {
		t.Error("cached status entry lost through caller mutation of a previously returned map")
	}
	if got := statusCalls.Load(); got != 1 {
		t.Fatalf("second status call must be a cache hit: spawned %d, want 1", got)
	}

	// Same contract for the ignored-path cache.
	ignoredFirst, err := f.cachedGitIgnoredPaths(ws)
	if err != nil {
		t.Fatalf("cachedGitIgnoredPaths #1: %v", err)
	}
	ignoredFirst["caller-added/"] = true
	delete(ignoredFirst, "build/")

	ignoredSecond, err := f.cachedGitIgnoredPaths(ws)
	if err != nil {
		t.Fatalf("cachedGitIgnoredPaths #2: %v", err)
	}
	if _, polluted := ignoredSecond["caller-added/"]; polluted {
		t.Error("ignored-path cache polluted by caller mutation of a previously returned map")
	}
	if !ignoredSecond["build/"] {
		t.Error("cached ignored-path entry lost through caller mutation of a previously returned map")
	}
	if got := ignoredCalls.Load(); got != 1 {
		t.Fatalf("second ignored call must be a cache hit: spawned %d, want 1", got)
	}
}
