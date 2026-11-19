package backend

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
)

// --- git snapshot caches (frontend_api_gitcache.go) tests ---
//
// Contract under test:
//   - GetGitStatus / GetGitStatusLite / ListDirectory must not spawn the same
//     heavy git subprocess twice within the cache TTL, and concurrent cold
//     callers must share one run (singleflight).
//   - A stale status snapshot serves instantly while at most one background
//     refresh per gitRefreshMinInterval runs (stale-while-revalidate); the
//     watcher only MARKS entries stale, so a tree_changed storm costs no git
//     run by itself.
//   - Ignore flags revalidate synchronously (a stale read re-runs the batched
//     check-ignore), and only an ignore-rule edit stales them.
//   - Mutating git RPCs (funnelled through emitGitStatusChanged) and a project
//     switch must drop the status snapshots so the next read recomputes.

// newGitCacheTestAPI builds a FrontendAPI with an active (non-No-Project)
// project rooted at ws, which is what GetGitStatus/ListDirectory require before
// they consult the git caches.
func newGitCacheTestAPI(ws string) *FrontendAPI {
	f := &FrontendAPI{}
	f.seedPublished.Store(true)
	f.activeProjectMu.Lock()
	f.activeProjectID = "gitcache-test-project"
	f.activeProjectPath = ws
	f.activeProjectMu.Unlock()
	return f
}

// gitCacheEntryCount reports how many snapshots a cache currently holds.
func gitCacheEntryCount[M ~map[string]V2, V2 any](c *gitSnapshotCache[M, V2]) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// genStatusSeam returns a gitStatusFn whose snapshots are unique per call
// ("f<N>.txt"), so a stale-serving read is distinguishable from a recompute.
func genStatusSeam(calls *atomic.Int32) func(string) (map[string]GitStatusEntry, error) {
	return func(string) (map[string]GitStatusEntry, error) {
		n := calls.Add(1)
		return map[string]GitStatusEntry{fmt.Sprintf("f%d.txt", n): {Status: "M"}}, nil
	}
}

// backdateStatusLastRefresh pushes the status entry's lastRefresh past the
// min-refresh window, making the next stale read eligible for a background
// refresh launch. Tests must not sleep out the real interval.
func backdateStatusLastRefresh(t *testing.T, f *FrontendAPI, root string) {
	t.Helper()
	f.ensureGitCaches()
	c := f.gitStatusCache
	c.mu.Lock()
	e := c.entries[gitCacheKey(root)]
	c.mu.Unlock()
	if e == nil {
		t.Fatalf("backdate: no status cache entry for %s", root)
	}
	e.lastRefresh = time.Now().Add(-gitRefreshMinInterval)
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
// the next GetGitStatus spawns git again — instant fresh feedback after a
// user-initiated stage/commit must not be throttled away.
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

// TestCachedGitStatus_ColdSingleflight verifies that concurrent cold callers
// share one git run: the second caller joins the in-flight fetch instead of
// spawning its own subprocess.
func TestCachedGitStatus_ColdSingleflight(t *testing.T) {
	ws := t.TempDir()
	var calls atomic.Int32
	var started sync.Once
	gate := make(chan struct{})
	startedCh := make(chan struct{})
	f := newGitCacheTestAPI(ws)
	f.gitStatusFn = func(string) (map[string]GitStatusEntry, error) {
		n := calls.Add(1)
		started.Do(func() { close(startedCh) })
		<-gate
		return map[string]GitStatusEntry{fmt.Sprintf("f%d.txt", n): {Status: "M"}}, nil
	}

	type result struct {
		status map[string]GitStatusEntry
		err    error
	}
	done := make(chan result, 2)
	go func() {
		st, err := f.cachedGitStatus(ws)
		done <- result{st, err}
	}()
	<-startedCh
	go func() {
		st, err := f.cachedGitStatus(ws)
		done <- result{st, err}
	}()

	close(gate)
	first, second := <-done, <-done
	if first.err != nil || second.err != nil {
		t.Fatalf("cold singleflight: %v / %v", first.err, second.err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent cold callers spawned %d git runs, want 1 (shared fetch)", got)
	}
	if _, ok := first.status["f1.txt"]; !ok {
		t.Errorf("first caller must receive the shared snapshot, got %v", first.status)
	}
	if _, ok := second.status["f1.txt"]; !ok {
		t.Errorf("second caller must receive the shared snapshot, got %v", second.status)
	}
}

// TestCachedGitStatus_StaleServedWhileRefreshing verifies stale-while-
// revalidate: a stale read returns the last snapshot immediately (no git run
// on the RPC's critical path), launches exactly one background refresh, and
// the next read after it settles serves the fresh snapshot.
func TestCachedGitStatus_StaleServedWhileRefreshing(t *testing.T) {
	ws := t.TempDir()
	var calls atomic.Int32
	settled := make(chan error, 4)
	f := newGitCacheTestAPI(ws)
	f.gitRefreshSettled = func(key string, err error) { settled <- err }
	f.gitStatusFn = genStatusSeam(&calls)

	f.ensureGitCaches()
	f.gitStatusCache.seed(gitCacheKey(ws), map[string]GitStatusEntry{"f0.txt": {Status: "A"}}, true)

	// Stale read: instant serve, no git run on this path.
	staleStatus, err := f.cachedGitStatus(ws)
	if err != nil {
		t.Fatalf("stale read: %v", err)
	}
	if _, ok := staleStatus["f0.txt"]; !ok {
		t.Fatalf("stale read must serve the seeded snapshot, got %v", staleStatus)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("stale read must not spawn git on the critical path, spawned %d", got)
	}

	// The background refresh settles (bounded watchdog against a hang).
	select {
	case err := <-settled:
		if err != nil {
			t.Fatalf("background refresh failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background refresh never settled")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("background refresh ran %d times, want 1", got)
	}

	// Next read: fresh snapshot, no further git run.
	fresh, err := f.cachedGitStatus(ws)
	if err != nil {
		t.Fatalf("fresh read: %v", err)
	}
	if _, ok := fresh["f1.txt"]; !ok {
		t.Fatalf("read after refresh must serve the recomputed snapshot, got %v", fresh)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fresh read must be a cache hit, spawned %d total", got)
	}
}

// TestCachedGitStatus_MinRefreshThrottle verifies the refresh throttle: a
// watcher-invalidated entry whose last git run is INSIDE the min-refresh
// window serves its stale snapshot without launching anything — a
// tree_changed storm costs no git run at all.
func TestCachedGitStatus_MinRefreshThrottle(t *testing.T) {
	ws := t.TempDir()
	var calls atomic.Int32
	f := newGitCacheTestAPI(ws)
	f.gitStatusFn = genStatusSeam(&calls)

	f.ensureGitCaches()
	f.gitStatusCache.seed(gitCacheKey(ws), map[string]GitStatusEntry{"f0.txt": {Status: "A"}}, false)
	f.gitStatusCache.markStale(gitCacheKey(ws))

	st, err := f.cachedGitStatus(ws)
	if err != nil {
		t.Fatalf("throttled read: %v", err)
	}
	if _, ok := st["f0.txt"]; !ok {
		t.Fatalf("throttled read must serve the snapshot, got %v", st)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("throttled read must not spawn git inside the min-refresh window, spawned %d", got)
	}
	f.ensureGitCaches()
	f.gitStatusCache.mu.Lock()
	e := f.gitStatusCache.entries[gitCacheKey(ws)]
	refreshing := e != nil && e.refreshing
	f.gitStatusCache.mu.Unlock()
	if refreshing {
		t.Fatal("throttled read must not launch a background refresh")
	}
}

// TestListDirectory_GitIgnoredFlagsCachedOnce verifies that two back-to-back
// ListDirectory calls on a git repo run the batched check-ignore once: the
// second listing is served from the per-directory flags cache.
func TestListDirectory_GitIgnoredFlagsCachedOnce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ws := t.TempDir()
	// ListDirectory only consults the flags cache for a real git repo
	// (isGitRepo), so the fixture must be one.
	gitRepoFixture(t, ws)

	var calls atomic.Int32
	f := newGitCacheTestAPI(ws)
	f.gitIgnoredFlagsFn = func(string, string, []string) (map[string]bool, error) {
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
		t.Fatalf("check-ignore ran %d times across two ListDirectory calls, want 1", got)
	}
}

// TestInvalidateGitCachesOnWatcher covers the debounced watcher policy: any
// tree change only MARKS the status snapshots stale (the next read serves the
// last snapshot and throttles its refresh), while an ignore-rule edit
// (.gitignore/.aiignore/.ignore) additionally stales the per-directory flags,
// which revalidate synchronously on the next read.
func TestInvalidateGitCachesOnWatcher(t *testing.T) {
	root := "/repo"
	var statusCalls, flagsCalls atomic.Int32
	settled := make(chan error, 4)
	f := &FrontendAPI{
		gitStatusFn:       genStatusSeam(&statusCalls),
		gitRefreshSettled: func(_ string, err error) { settled <- err },
		gitIgnoredFlagsFn: func(string, string, []string) (map[string]bool, error) {
			flagsCalls.Add(1)
			return map[string]bool{}, nil
		},
	}
	f.seedPublished.Store(true)

	// Prime both caches (one git run each).
	if _, err := f.cachedGitStatus(root); err != nil {
		t.Fatalf("prime status: %v", err)
	}
	if _, err := f.cachedGitIgnoredFlags(root, root, []string{filepath.Join(root, "main.go")}); err != nil {
		t.Fatalf("prime flags: %v", err)
	}

	// A regular file change stales the status snapshot WITHOUT spawning git:
	// the next read serves the last snapshot (still inside the min-refresh
	// window) and the flags stay fresh entirely.
	f.invalidateGitCachesOnWatcher(root, []string{filepath.Join(root, "main.go")})
	st, err := f.cachedGitStatus(root)
	if err != nil {
		t.Fatalf("status after regular change: %v", err)
	}
	if _, ok := st["f1.txt"]; !ok {
		t.Fatalf("stale read must serve the primed snapshot, got %v", st)
	}
	if got := statusCalls.Load(); got != 1 {
		t.Fatalf("watcher batch must not spawn git by itself: spawned %d, want 1", got)
	}
	if _, err := f.cachedGitIgnoredFlags(root, root, []string{filepath.Join(root, "main.go")}); err != nil {
		t.Fatalf("flags after regular change: %v", err)
	}
	if got := flagsCalls.Load(); got != 1 {
		t.Fatalf("flags must stay cached after a regular change: spawned %d, want 1", got)
	}

	// Past the min-refresh window the same stale read launches exactly one
	// background refresh; the test awaits its settle instead of racing it.
	backdateStatusLastRefresh(t, f, root)
	if _, err := f.cachedGitStatus(root); err != nil {
		t.Fatalf("status after backdate: %v", err)
	}
	select {
	case err := <-settled:
		if err != nil {
			t.Fatalf("background refresh failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background refresh never settled")
	}
	if got := statusCalls.Load(); got != 2 {
		t.Fatalf("stale read past the window must revalidate: spawned %d, want 2", got)
	}

	// An ignore-rule edit stales the flags too; the flags read revalidates
	// synchronously (the batch is cheap by design).
	f.invalidateGitCachesOnWatcher(root, []string{filepath.Join(root, ".gitignore")})
	if _, err := f.cachedGitIgnoredFlags(root, root, []string{filepath.Join(root, "main.go")}); err != nil {
		t.Fatalf("flags after .gitignore change: %v", err)
	}
	if got := flagsCalls.Load(); got != 2 {
		t.Fatalf("flags must revalidate after a .gitignore change: spawned %d, want 2", got)
	}
}

// TestSwitchProject_InvalidatesGitCaches verifies that switching projects drops
// the per-repo caches wholesale, so no snapshot from the previous workspace
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

	// Seed all three caches with stale entries.
	h.api.ensureGitCaches()
	h.api.gitStatusCache.seed("stale", map[string]GitStatusEntry{"x": {}}, true)
	h.api.gitStatusLiteCache.seed("stale", map[string]GitStatusEntry{"x": {}}, true)
	h.api.gitIgnoredFlagsCache.seed("stale", map[string]bool{"x": true}, true)

	if err := h.api.SwitchProject(h.projectID); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}

	statusCleared := gitCacheEntryCount(h.api.gitStatusCache) == 0
	liteCleared := gitCacheEntryCount(h.api.gitStatusLiteCache) == 0
	flagsCleared := gitCacheEntryCount(h.api.gitIgnoredFlagsCache) == 0
	if !statusCleared || !liteCleared || !flagsCleared {
		t.Fatalf("SwitchProject must drop the git caches: status cleared=%v, lite cleared=%v, flags cleared=%v",
			statusCleared, liteCleared, flagsCleared)
	}
}

// TestGitCaches_NoProjectUntouched pins the No Project contract: with the No
// Project pseudo-project active, isGitRepo reports "not a repo" without writing
// to the git-repo cache (and, by extension, no git snapshot cache is ever
// populated for a session workspace).
func TestGitCaches_NoProjectUntouched(t *testing.T) {
	f := &FrontendAPI{}
	f.seedPublished.Store(true)
	f.activeProjectMu.Lock()
	f.activeProjectID = project.NoProjectID
	f.activeProjectMu.Unlock()

	if f.isGitRepo(t.TempDir()) {
		t.Fatal("No Project must never be treated as a git repo")
	}
	if f.gitRepoCache != nil || f.gitStatusCache != nil || f.gitStatusLiteCache != nil || f.gitIgnoredFlagsCache != nil {
		t.Fatalf("No Project must not populate git caches: repo=%v status=%v lite=%v flags=%v",
			f.gitRepoCache, f.gitStatusCache, f.gitStatusLiteCache, f.gitIgnoredFlagsCache)
	}
}

// TestCachedGitMaps_AreCallerOwned pins review [C3-5]: the maps handed out by
// the git caches are shallow copies (maps.Clone) at the cache boundary — a
// caller mutating its returned map must never corrupt the cached snapshot
// served to other readers within the TTL.
func TestCachedGitMaps_AreCallerOwned(t *testing.T) {
	ws := t.TempDir()
	var statusCalls, flagsCalls atomic.Int32
	ignoredKey := filepath.Join(ws, "ignored.txt")
	f := newGitCacheTestAPI(ws)
	f.gitStatusFn = func(string) (map[string]GitStatusEntry, error) {
		statusCalls.Add(1)
		return map[string]GitStatusEntry{"a.txt": {Status: "M"}}, nil
	}
	f.gitIgnoredFlagsFn = func(string, string, []string) (map[string]bool, error) {
		flagsCalls.Add(1)
		return map[string]bool{ignoredKey: true}, nil
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

	// Same contract for the ignore-flags cache.
	flagsFirst, err := f.cachedGitIgnoredFlags(ws, ws, []string{ignoredKey})
	if err != nil {
		t.Fatalf("cachedGitIgnoredFlags #1: %v", err)
	}
	flagsFirst["caller-added/"] = true
	delete(flagsFirst, ignoredKey)

	flagsSecond, err := f.cachedGitIgnoredFlags(ws, ws, []string{ignoredKey})
	if err != nil {
		t.Fatalf("cachedGitIgnoredFlags #2: %v", err)
	}
	if _, polluted := flagsSecond["caller-added/"]; polluted {
		t.Error("flags cache polluted by caller mutation of a previously returned map")
	}
	if !flagsSecond[ignoredKey] {
		t.Error("cached flags entry lost through caller mutation of a previously returned map")
	}
	if got := flagsCalls.Load(); got != 1 {
		t.Fatalf("second flags call must be a cache hit: spawned %d, want 1", got)
	}
}
