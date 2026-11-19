package backend

import (
	"context"
	"fmt"
	"os"
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
//     refresh per gitRefreshMinInterval runs (stale-while-revalidate) — a
//     state reachable only while the tree is quiet, because every real
//     change drops the entries instead.
//   - Ignore flags revalidate synchronously (a stale read re-runs the batched
//     check-ignore), and only an ignore-rule edit stales them.
//   - Mutating git RPCs (funnelled through emitGitStatusChanged), watcher
//     batches, and a project switch must drop the status snapshots so the
//     next read recomputes: the consumers' one refetch per
//     workspace:tree_changed is the only delivery of git status to the UI.

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
// stale entry whose last git run is INSIDE the min-refresh window serves its
// stale snapshot without launching a second background refresh.
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

// TestGitSnapshotCache_DropDiscardsInFlightFetch pins the generation guard's
// synchronous leg: a fetch spanning a watcher drop must never store its
// pre-drop snapshot, and a reader arriving after the drop must start its own
// fetch (generation-scoped flight key) instead of joining — and receiving —
// the pre-drop result. Without the guard the post-drop read would either
// join the stale flight or find the stale entry re-cached with a fresh TTL:
// pre-change status pinned with no event to heal it.
func TestGitSnapshotCache_DropDiscardsInFlightFetch(t *testing.T) {
	ws := t.TempDir()
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	f := newGitCacheTestAPI(ws)
	f.gitStatusFn = func(string) (map[string]GitStatusEntry, error) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			<-release // hold the pre-drop flight open across the drop
		}
		return map[string]GitStatusEntry{fmt.Sprintf("f%d.txt", n): {Status: "M"}}, nil
	}
	f.ensureGitCaches()

	type result struct {
		status map[string]GitStatusEntry
		err    error
	}
	done := make(chan result, 1)
	go func() {
		st, err := f.cachedGitStatus(ws)
		done <- result{st, err}
	}()
	<-started

	// The change lands while the fetch is in flight: the drop bumps the
	// generation, so this in-flight result must never enter the cache.
	f.invalidateGitCachesOnWatcher(ws, []string{filepath.Join(ws, "late.txt")})

	// A post-drop reader must not join the held pre-drop flight: it runs its
	// own fetch (call 2) and stores the post-drop snapshot.
	post, err := f.cachedGitStatus(ws)
	if err != nil {
		t.Fatalf("post-drop read: %v", err)
	}
	if _, ok := post["f2.txt"]; !ok {
		t.Fatalf("post-drop read must run its own fetch, got %v", post)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("post-drop read joined the pre-drop flight: %d calls, want 2", got)
	}

	// Release the held fetch; its pre-drop result reaches its own caller but
	// must not enter the cache behind the post-drop entry.
	close(release)
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("pre-drop fetch: %v", res.err)
		}
		if _, ok := res.status["f1.txt"]; !ok {
			t.Fatalf("pre-drop caller must receive its own result, got %v", res.status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pre-drop fetch never settled")
	}

	again, err := f.cachedGitStatus(ws)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if _, ok := again["f2.txt"]; !ok {
		t.Fatalf("cache must hold the post-drop snapshot, got %v", again)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("pre-drop result must not have re-entered the cache: %d calls, want 2", got)
	}
}

// TestGitSnapshotCache_DropDiscardsStaleBackgroundRefresh pins the
// create=false leg of the generation guard: a background refresh that
// spanned a drop must not clobber the post-drop snapshot a synchronous read
// already stored.
func TestGitSnapshotCache_DropDiscardsStaleBackgroundRefresh(t *testing.T) {
	ws := t.TempDir()
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	settled := make(chan error, 4)
	f := newGitCacheTestAPI(ws)
	f.gitRefreshSettled = func(_ string, err error) { settled <- err }
	f.gitStatusFn = func(string) (map[string]GitStatusEntry, error) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			<-release // hold the background refresh open across the drop
		}
		return map[string]GitStatusEntry{fmt.Sprintf("f%d.txt", n): {Status: "M"}}, nil
	}
	f.ensureGitCaches()

	// Seed a launch-eligible stale entry: freshness window closed and
	// lastRefresh past the throttle, so the next read serves the seed and
	// launches the background refresh (call 1).
	f.gitStatusCache.seed(gitCacheKey(ws), map[string]GitStatusEntry{"f0.txt": {Status: "A"}}, false)
	f.gitStatusCache.mu.Lock()
	e := f.gitStatusCache.entries[gitCacheKey(ws)]
	e.expiry = time.Time{}
	e.lastRefresh = time.Now().Add(-gitRefreshMinInterval)
	f.gitStatusCache.mu.Unlock()

	stale, err := f.cachedGitStatus(ws)
	if err != nil {
		t.Fatalf("stale read: %v", err)
	}
	if _, ok := stale["f0.txt"]; !ok {
		t.Fatalf("stale read must serve the seed, got %v", stale)
	}
	<-started

	// The change lands while the refresh is in flight; the post-drop read
	// stores the fresh snapshot.
	f.invalidateGitCachesOnWatcher(ws, []string{filepath.Join(ws, "late.txt")})
	post, err := f.cachedGitStatus(ws)
	if err != nil {
		t.Fatalf("post-drop read: %v", err)
	}
	if _, ok := post["f2.txt"]; !ok {
		t.Fatalf("post-drop read must recompute, got %v", post)
	}

	// The stale refresh settles and must NOT clobber the post-drop entry.
	close(release)
	select {
	case err := <-settled:
		if err != nil {
			t.Fatalf("background refresh failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background refresh never settled")
	}
	again, err := f.cachedGitStatus(ws)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if _, ok := again["f2.txt"]; !ok {
		t.Fatalf("stale background refresh must not clobber the post-drop snapshot, got %v", again)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("re-read must stay cached: %d calls, want 2", got)
	}
}

// TestGitSnapshotCache_UnrelatedDropKeepsRefreshLaunchable pins the
// per-entry launch guard: repo A's background refresh must survive an
// unrelated repo B's cache-wide generation bump — its result still refreshes
// A's entry and the refreshing guard never strands. A cache-wide guard alone
// would discard the refresh without releasing the guard, freezing
// stale-while-revalidate for A until A's own next hard drop.
func TestGitSnapshotCache_UnrelatedDropKeepsRefreshLaunchable(t *testing.T) {
	ws := t.TempDir()
	const repoA, repoB = "/repo-a", "/repo-b"
	var calls atomic.Int32
	var held atomic.Bool
	release := make(chan struct{})
	started := make(chan struct{})
	settled := make(chan error, 4)
	f := newGitCacheTestAPI(ws)
	f.gitRefreshSettled = func(_ string, err error) { settled <- err }
	f.gitStatusFn = func(root string) (map[string]GitStatusEntry, error) {
		calls.Add(1)
		if root == repoA && held.CompareAndSwap(false, true) {
			close(started)
			<-release // hold A's refresh open while repo B drops
		}
		return map[string]GitStatusEntry{root + "-f.txt": {Status: "M"}}, nil
	}
	f.ensureGitCaches()

	// Seed repo A launch-eligible stale; the read serves the seed and
	// launches its background refresh (call 1, held).
	keyA := gitCacheKey(repoA)
	f.gitStatusCache.seed(keyA, map[string]GitStatusEntry{"a-old.txt": {Status: "A"}}, false)
	f.gitStatusCache.mu.Lock()
	e := f.gitStatusCache.entries[keyA]
	e.expiry = time.Time{}
	e.lastRefresh = time.Now().Add(-gitRefreshMinInterval)
	f.gitStatusCache.mu.Unlock()
	if _, err := f.cachedGitStatus(repoA); err != nil {
		t.Fatalf("stale read A: %v", err)
	}
	<-started

	// Repo B's watcher batch drops B and bumps the cache-wide generation
	// while A's entry SURVIVES the scoped drop.
	f.invalidateGitCachesOnWatcher(repoB, []string{filepath.Join(repoB, "b.txt")})

	// A's refresh settles and must still refresh A's entry.
	close(release)
	select {
	case err := <-settled:
		if err != nil {
			t.Fatalf("background refresh failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background refresh never settled")
	}
	fresh, err := f.cachedGitStatus(repoA)
	if err != nil {
		t.Fatalf("read A after refresh: %v", err)
	}
	if _, ok := fresh[repoA+"-f.txt"]; !ok {
		t.Fatalf("unrelated repo's drop must not discard A's refresh, got %v", fresh)
	}

	// The refreshing guard must not be stranded: re-stale A and the next
	// read must launch a fresh background refresh again.
	f.gitStatusCache.mu.Lock()
	e = f.gitStatusCache.entries[keyA]
	e.expiry = time.Time{}
	e.lastRefresh = time.Now().Add(-gitRefreshMinInterval)
	f.gitStatusCache.mu.Unlock()
	if _, err := f.cachedGitStatus(repoA); err != nil {
		t.Fatalf("re-staled read A: %v", err)
	}
	select {
	case err := <-settled:
		if err != nil {
			t.Fatalf("second background refresh failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stranded refreshing guard: second refresh never launched")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("second refresh must have run: %d calls, want 2", got)
	}
}

// TestInvalidateGitCachesOnWatcher_ExternalGitMutation: a .git write no
// backend RPC performed (a terminal git add/commit/stash) is a git-mutation
// signal — the funnel emits git:status_changed (the frontend's viewer-diff
// epoch keys off it, so an externally staged overlay heals like a
// UI-initiated one) and the status snapshots drop. A batch without
// git-internal paths stays event-silent and still drops the caches.
func TestInvalidateGitCachesOnWatcher_ExternalGitMutation(t *testing.T) {
	root := "/repo"
	var statusCalls atomic.Int32
	type emitted struct {
		name    string
		payload any
	}
	var events []emitted
	f := &FrontendAPI{
		gitStatusFn: genStatusSeam(&statusCalls),
		emitEvent: func(name string, payloads ...any) {
			var payload any
			if len(payloads) > 0 {
				payload = payloads[0]
			}
			events = append(events, emitted{name, payload})
		},
	}
	f.seedPublished.Store(true)

	if _, err := f.cachedGitStatus(root); err != nil {
		t.Fatalf("prime status: %v", err)
	}

	// A terminal `git add` touches .git/index: the funnel emits
	// git:status_changed and drops the status snapshots.
	f.invalidateGitCachesOnWatcher(root, []string{filepath.Join(root, ".git", "index")})
	if _, err := f.cachedGitStatus(root); err != nil {
		t.Fatalf("status after external stage: %v", err)
	}
	if got := statusCalls.Load(); got != 2 {
		t.Fatalf("external git mutation must drop the status caches: spawned %d, want 2", got)
	}
	statusEvents := 0
	var payload any
	for _, e := range events {
		if e.name == EventGitStatusChanged {
			statusEvents++
			payload = e.payload
		}
	}
	if statusEvents != 1 {
		t.Fatalf("external git mutation must emit git:status_changed once, got %d", statusEvents)
	}
	if payload != root {
		t.Fatalf("git:status_changed payload = %v, want the repo root", payload)
	}

	// A batch without git-internal paths stays event-silent (tree_changed is
	// the watcher callback's own emission, not the cache route's) and still
	// drops the status caches.
	events = nil
	f.invalidateGitCachesOnWatcher(root, []string{filepath.Join(root, "main.go")})
	for _, e := range events {
		if e.name == EventGitStatusChanged {
			t.Fatalf("regular file change must not emit git:status_changed, got %+v", e)
		}
	}
	if _, err := f.cachedGitStatus(root); err != nil {
		t.Fatalf("status after regular change: %v", err)
	}
	if got := statusCalls.Load(); got != 3 {
		t.Fatalf("regular change must still drop the status caches: spawned %d, want 3", got)
	}

	// Remote-tracking ref writes (an external fetch) cannot move local
	// status or hunks — the funnel stays event-silent for them too.
	events = nil
	f.invalidateGitCachesOnWatcher(root, []string{filepath.Join(root, ".git", "refs", "remotes", "origin", "main")})
	for _, e := range events {
		if e.name == EventGitStatusChanged {
			t.Fatalf("remote-tracking ref write must not emit git:status_changed, got %+v", e)
		}
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
	f.gitIgnoredFlagsFn = func(string, string, string, []string) (map[string]bool, error) {
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

// TestCachedGitIgnoredFlags_ScopesAreSeparate pins the scope discriminator in
// the flags cache key: a flat and a recursive listing of the SAME directory
// cache their verdicts separately, so a recursive listing (file search,
// @-completions) never receives the flat listing's child-only set (and vice
// versa) — every requested path the cached set did not cover would silently
// lose its flag.
func TestCachedGitIgnoredFlags_ScopesAreSeparate(t *testing.T) {
	ws := t.TempDir()
	var calls atomic.Int32
	f := newGitCacheTestAPI(ws)
	f.gitIgnoredFlagsFn = func(_, _, scope string, paths []string) (map[string]bool, error) {
		calls.Add(1)
		flags := make(map[string]bool, len(paths))
		for _, p := range paths {
			flags[p] = scope == ignoreScopeRecursive
		}
		return flags, nil
	}

	flatPath := filepath.Join(ws, "child.txt")
	flat, err := f.cachedGitIgnoredFlags(ws, ws, ignoreScopeFlat, []string{flatPath})
	if err != nil {
		t.Fatalf("flat flags: %v", err)
	}
	if flat[flatPath] {
		t.Fatalf("flat scope must serve the flat verdict set, got %v", flat)
	}

	// Same directory, different scope: a distinct cache entry recomputed over
	// the recursive path set — the flat entry's set is not served here.
	deepPath := filepath.Join(ws, "deep", "leaf.txt")
	deep, err := f.cachedGitIgnoredFlags(ws, ws, ignoreScopeRecursive, []string{deepPath})
	if err != nil {
		t.Fatalf("recursive flags: %v", err)
	}
	if !deep[deepPath] {
		t.Fatalf("recursive scope must serve its own verdict set, got %v", deep)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("scopes must not share an entry: %d seam calls, want 2", got)
	}

	// Both entries survive and still serve their own scope.
	flatAgain, err := f.cachedGitIgnoredFlags(ws, ws, ignoreScopeFlat, []string{flatPath})
	if err != nil {
		t.Fatalf("flat flags again: %v", err)
	}
	if flatAgain[flatPath] {
		t.Fatalf("flat entry must stay separate after a recursive read, got %v", flatAgain)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("repeated same-scope reads must stay cached: %d seam calls, want 2", got)
	}
}

// TestInvalidateGitCachesOnWatcher covers the debounced watcher policy: any
// tree change DROPS the status snapshots, so the consumers' own refetch (one
// read per workspace:tree_changed, with no trailing read) recomputes
// synchronously — pre-change status can never be served. An ignore-rule edit
// (.gitignore/.aiignore/.ignore) additionally stales the per-directory flags,
// which revalidate synchronously on the next read.
func TestInvalidateGitCachesOnWatcher(t *testing.T) {
	root := "/repo"
	var statusCalls, statusLiteCalls, flagsCalls atomic.Int32
	f := &FrontendAPI{
		gitStatusFn:     genStatusSeam(&statusCalls),
		gitStatusLiteFn: genStatusSeam(&statusLiteCalls),
		gitIgnoredFlagsFn: func(string, string, string, []string) (map[string]bool, error) {
			flagsCalls.Add(1)
			return map[string]bool{}, nil
		},
	}
	f.seedPublished.Store(true)

	// Prime all three caches (one git run each).
	if _, err := f.cachedGitStatus(root); err != nil {
		t.Fatalf("prime status: %v", err)
	}
	if _, err := f.cachedGitStatusLite(root); err != nil {
		t.Fatalf("prime status-lite: %v", err)
	}
	if _, err := f.cachedGitIgnoredFlags(root, root, ignoreScopeFlat, []string{filepath.Join(root, "main.go")}); err != nil {
		t.Fatalf("prime flags: %v", err)
	}

	// A regular file change drops BOTH status snapshots: the very next read
	// of each is a synchronous cold fetch serving the recomputed snapshot —
	// even inside the min-refresh window, because a drop is not a
	// throttle-backed mark.
	f.invalidateGitCachesOnWatcher(root, []string{filepath.Join(root, "main.go")})
	st, err := f.cachedGitStatus(root)
	if err != nil {
		t.Fatalf("status after regular change: %v", err)
	}
	if _, ok := st["f2.txt"]; !ok {
		t.Fatalf("read after a watcher batch must recompute synchronously, got %v", st)
	}
	stLite, err := f.cachedGitStatusLite(root)
	if err != nil {
		t.Fatalf("status-lite after regular change: %v", err)
	}
	if _, ok := stLite["f2.txt"]; !ok {
		t.Fatalf("lite read after a watcher batch must recompute synchronously, got %v", stLite)
	}
	if got := statusCalls.Load(); got != 2 {
		t.Fatalf("watcher drop must make the next status read a cold fetch: spawned %d, want 2", got)
	}
	if got := statusLiteCalls.Load(); got != 2 {
		t.Fatalf("watcher drop must make the next lite read a cold fetch: spawned %d, want 2", got)
	}

	// The flags stay cached entirely: a non-ignore edit cannot change the
	// pattern verdicts.
	if _, err := f.cachedGitIgnoredFlags(root, root, ignoreScopeFlat, []string{filepath.Join(root, "main.go")}); err != nil {
		t.Fatalf("flags after regular change: %v", err)
	}
	if got := flagsCalls.Load(); got != 1 {
		t.Fatalf("flags must stay cached after a regular change: spawned %d, want 1", got)
	}

	// An ignore-rule edit stales the flags; the flags read revalidates
	// synchronously (the batch is cheap by design).
	f.invalidateGitCachesOnWatcher(root, []string{filepath.Join(root, ".gitignore")})
	if _, err := f.cachedGitIgnoredFlags(root, root, ignoreScopeFlat, []string{filepath.Join(root, "main.go")}); err != nil {
		t.Fatalf("flags after .gitignore change: %v", err)
	}
	if got := flagsCalls.Load(); got != 2 {
		t.Fatalf("flags must revalidate after a .gitignore change: spawned %d, want 2", got)
	}
}

// TestListDirectory_ManagedWorktreeNotFlagged pins the ignore-flag re-rooting:
// a session tree rooted at a managed worktree sits INSIDE the checkout, under
// the /.worktrees/ container exclude (anchored to the checkout root), but the
// flags must be evaluated in the tree's OWN repository — a checkout-rooted
// check-ignore batch matches every deep path of the tree and would dim the
// whole session tree. Rules the tree's repository carries (*.log via the
// shared info/exclude) still flag inside the tree, and the checkout's own
// listing still flags the container.
func TestListDirectory_ManagedWorktreeNotFlagged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	checkout := t.TempDir()
	gitInit(t, checkout)
	commitFile(t, checkout, "base.txt", "v1\n")

	exclude := filepath.Join(checkout, ".git", "info", "exclude")
	fx, err := os.OpenFile(exclude, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open exclude: %v", err)
	}
	if _, err := fx.WriteString("/.worktrees/\n*.log\n"); err != nil {
		t.Fatalf("write exclude: %v", err)
	}
	_ = fx.Close()

	tree := filepath.Join(checkout, ".worktrees", "wt")
	cmd := exec.CommandContext(context.Background(), "git", "-C", checkout, "worktree", "add", "-q", "-b", "wt", tree)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(tree, "plain.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write plain: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tree, "err.log"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write ignored: %v", err)
	}

	api := newGitCacheTestAPI(checkout)
	nodes, err := api.ListDirectory(tree, false)
	if err != nil {
		t.Fatalf("ListDirectory(tree): %v", err)
	}
	flagged := map[string]bool{}
	for _, n := range nodes {
		flagged[n.Name] = n.GitIgnored
	}
	if flagged["plain.txt"] {
		t.Error("plain.txt must not be gitignored — the container exclude is anchored to the checkout root, not the tree")
	}
	if !flagged["err.log"] {
		t.Error("err.log must stay gitignored — rules the tree's repository carries still apply")
	}

	// The checkout's own listing still flags the .worktrees container.
	nodes, err = api.ListDirectory(checkout, false)
	if err != nil {
		t.Fatalf("ListDirectory(checkout): %v", err)
	}
	containerFlagged := false
	for _, n := range nodes {
		if n.Name == ".worktrees" {
			containerFlagged = n.GitIgnored
		}
	}
	if !containerFlagged {
		t.Error(".worktrees container must stay gitignored in the checkout listing")
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
	f.gitIgnoredFlagsFn = func(string, string, string, []string) (map[string]bool, error) {
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
	flagsFirst, err := f.cachedGitIgnoredFlags(ws, ws, ignoreScopeFlat, []string{ignoredKey})
	if err != nil {
		t.Fatalf("cachedGitIgnoredFlags #1: %v", err)
	}
	flagsFirst["caller-added/"] = true
	delete(flagsFirst, ignoredKey)

	flagsSecond, err := f.cachedGitIgnoredFlags(ws, ws, ignoreScopeFlat, []string{ignoredKey})
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
