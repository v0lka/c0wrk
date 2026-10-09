package backend

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"sort"
	"time"

	"github.com/v0lka/c0wrk/core/workspace"
)

// Heavy git subprocesses (git status --porcelain -uall, git ls-files
// --others --ignored) are the dominant cost of a single ListDirectory /
// GetGitStatus round-trip. Both are repo-root keyed and were recomputed on
// every call, so a rapid file-tree refresh or a burst of frontend polls
// spawned the same process repeatedly. The two caches below memoize their
// results per repo root, mirroring the existing gitRepoCache pattern
// (mu-guarded, TTL, bounded eviction) and are invalidated event-first:
// mutating git RPCs (via emitGitStatusChanged), the workspace watcher
// debounce, and a project switch.
const (
	// gitStatusCacheTTL bounds how long a computed git-status snapshot is
	// reused. Short: the working tree changes constantly while editing, and
	// the watcher plus every mutating git RPC invalidate explicitly.
	gitStatusCacheTTL = 10 * time.Second
	// gitIgnoredCacheTTL bounds how long the git-ignored path set is reused.
	// Longer than the status TTL because the ignore set only changes when an
	// ignore-rule file or the tracked file set changes — both of which
	// invalidate explicitly.
	gitIgnoredCacheTTL = 30 * time.Second
	// gitCacheMaxSize bounds each repo-keyed cache so memory stays finite on
	// long-lived instances that visit many work directories.
	gitCacheMaxSize = 100
)

// gitStatusCacheEntry holds a cached git-status snapshot with expiry.
type gitStatusCacheEntry struct {
	status map[string]GitStatusEntry
	expiry time.Time
}

// gitIgnoredCacheEntry holds a cached git-ignored path set with expiry.
type gitIgnoredCacheEntry struct {
	ignored map[string]bool
	expiry  time.Time
}

// gitIgnoreRuleFiles is the set of file names whose modification invalidates
// the git-ignored path cache. Mirrors the session package's ignoreFileNames.
var gitIgnoreRuleFiles = map[string]struct{}{
	".gitignore": {},
	".aiignore":  {},
	".ignore":    {},
}

// hasIgnoreRuleChange reports whether any changed path is an ignore-rule file.
func hasIgnoreRuleChange(changedPaths []string) bool {
	for _, p := range changedPaths {
		if _, ok := gitIgnoreRuleFiles[filepath.Base(p)]; ok {
			return true
		}
	}
	return false
}

// evictOldestGitEntries bounds a repo-keyed cache by dropping the entries with
// the earliest expiry once the map exceeds max. The caller must hold the map's
// mutex.
func evictOldestGitEntries[V any](m map[string]V, expiry func(V) time.Time, maxSize int) {
	if len(m) <= maxSize {
		return
	}
	type keyed struct {
		key    string
		expiry time.Time
	}
	entries := make([]keyed, 0, len(m))
	for k, v := range m {
		entries = append(entries, keyed{key: k, expiry: expiry(v)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].expiry.Before(entries[j].expiry) })
	for i := 0; i < len(entries)-maxSize; i++ {
		delete(m, entries[i].key)
	}
}

// runGitStatus delegates to the injectable gitStatusFn test seam, falling back
// to the real workspace helper in production.
func (f *FrontendAPI) runGitStatus(repoRoot string) (map[string]GitStatusEntry, error) {
	if f.gitStatusFn != nil {
		return f.gitStatusFn(repoRoot)
	}
	// f.ctx() is the application context — no deadline, cancelled only at
	// shutdown — and GitStatus spawns git with no internal bound, so a
	// wedged git would hang the GetGitStatus RPC indefinitely. Bound it
	// with the same 30s budget every other local git call gets (review
	// [176]); expiry surfaces as the RPC's ordinary git-failure path.
	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()
	return workspace.GitStatus(ctx, repoRoot)
}

// runGitIgnoredPaths delegates to the injectable gitIgnoredFn test seam,
// falling back to the real workspace helper in production.
func (f *FrontendAPI) runGitIgnoredPaths(repoRoot string) (map[string]bool, error) {
	if f.gitIgnoredFn != nil {
		return f.gitIgnoredFn(repoRoot)
	}
	// Same never-deadlined-context bound as runGitStatus (review [176]).
	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()
	return workspace.GitIgnoredPaths(ctx, repoRoot)
}

// cachedGitStatus returns the git status for repoRoot, reusing a snapshot
// computed within gitStatusCacheTTL. Errors are not cached so a transient git
// failure is retried on the next call. The returned map is always a shallow
// copy (maps.Clone) owned by the caller: mutating it can never corrupt the
// cached snapshot other readers share. Cost is one map allocation per call —
// negligible against the git subprocess the cache saves.
func (f *FrontendAPI) cachedGitStatus(repoRoot string) (map[string]GitStatusEntry, error) {
	if repoRoot == "" {
		return nil, errors.New("empty repo root")
	}
	now := time.Now()

	f.gitStatusCacheMu.Lock()
	if e, ok := f.gitStatusCache[repoRoot]; ok && now.Before(e.expiry) {
		status := e.status
		f.gitStatusCacheMu.Unlock()
		return maps.Clone(status), nil
	}
	if f.gitStatusCache != nil {
		evictOldestGitEntries(f.gitStatusCache, func(e gitStatusCacheEntry) time.Time { return e.expiry }, gitCacheMaxSize)
	}
	f.gitStatusCacheMu.Unlock()

	status, err := f.runGitStatus(repoRoot)
	if err != nil {
		return nil, err
	}

	f.gitStatusCacheMu.Lock()
	if f.gitStatusCache == nil {
		f.gitStatusCache = make(map[string]gitStatusCacheEntry)
	}
	f.gitStatusCache[repoRoot] = gitStatusCacheEntry{status: status, expiry: now.Add(gitStatusCacheTTL)}
	f.gitStatusCacheMu.Unlock()

	return maps.Clone(status), nil
}

// cachedGitIgnoredPaths returns the git-ignored path set for repoRoot, reusing
// a result computed within gitIgnoredCacheTTL. Errors are not cached so a
// transient git failure is retried on the next call. The returned map is
// always a shallow copy (maps.Clone) owned by the caller — the same
// read-isolation contract as cachedGitStatus.
func (f *FrontendAPI) cachedGitIgnoredPaths(repoRoot string) (map[string]bool, error) {
	if repoRoot == "" {
		return nil, errors.New("empty repo root")
	}
	now := time.Now()

	f.gitIgnoredCacheMu.Lock()
	if e, ok := f.gitIgnoredCache[repoRoot]; ok && now.Before(e.expiry) {
		ignored := e.ignored
		f.gitIgnoredCacheMu.Unlock()
		return maps.Clone(ignored), nil
	}
	if f.gitIgnoredCache != nil {
		evictOldestGitEntries(f.gitIgnoredCache, func(e gitIgnoredCacheEntry) time.Time { return e.expiry }, gitCacheMaxSize)
	}
	f.gitIgnoredCacheMu.Unlock()

	ignored, err := f.runGitIgnoredPaths(repoRoot)
	if err != nil {
		return nil, err
	}

	f.gitIgnoredCacheMu.Lock()
	if f.gitIgnoredCache == nil {
		f.gitIgnoredCache = make(map[string]gitIgnoredCacheEntry)
	}
	f.gitIgnoredCache[repoRoot] = gitIgnoredCacheEntry{ignored: ignored, expiry: now.Add(gitIgnoredCacheTTL)}
	f.gitIgnoredCacheMu.Unlock()

	return maps.Clone(ignored), nil
}

// invalidateGitCaches evicts both cached git snapshots for a single repo root.
// It is the invalidation hook for mutating git operations, which all funnel
// through emitGitStatusChanged.
func (f *FrontendAPI) invalidateGitCaches(repoRoot string) {
	f.invalidateGitStatusCache(repoRoot)
	f.invalidateGitIgnoredCache(repoRoot)
}

// invalidateGitStatusCache evicts only the status snapshot for repoRoot; the
// working tree may have changed (a file was saved/created/removed).
func (f *FrontendAPI) invalidateGitStatusCache(repoRoot string) {
	if repoRoot == "" {
		return
	}
	f.gitStatusCacheMu.Lock()
	delete(f.gitStatusCache, repoRoot)
	f.gitStatusCacheMu.Unlock()
}

// invalidateGitIgnoredCache evicts only the ignored-path snapshot for repoRoot;
// an ignore-rule file changed.
func (f *FrontendAPI) invalidateGitIgnoredCache(repoRoot string) {
	if repoRoot == "" {
		return
	}
	f.gitIgnoredCacheMu.Lock()
	delete(f.gitIgnoredCache, repoRoot)
	f.gitIgnoredCacheMu.Unlock()
}

// invalidateGitCachesOnWatcher handles a debounced watcher batch for the active
// repo: any tree change can alter the working-tree status, and an edit to an
// ignore-rule file additionally invalidates the ignored-path set.
func (f *FrontendAPI) invalidateGitCachesOnWatcher(repoRoot string, changedPaths []string) {
	if repoRoot == "" {
		return
	}
	f.invalidateGitStatusCache(repoRoot)
	if hasIgnoreRuleChange(changedPaths) {
		f.invalidateGitIgnoredCache(repoRoot)
	}
}

// invalidateAllGitCaches drops every cached git snapshot. Called on a project
// switch so state from the previous workspace can never leak into the new one.
func (f *FrontendAPI) invalidateAllGitCaches() {
	f.gitStatusCacheMu.Lock()
	f.gitStatusCache = nil
	f.gitStatusCacheMu.Unlock()

	f.gitIgnoredCacheMu.Lock()
	f.gitIgnoredCache = nil
	f.gitIgnoredCacheMu.Unlock()

	// The Git-panel focus belongs to the previous project's repository; a
	// stale override would resolve every PATHLESS git RPC (commit, stage
	// all, stash, pull/push/fetch, reset) into the previous project's
	// repository during the window between this switch and the frontend's
	// asynchronous focus re-apply — wrong-repo commit/push/reset (review
	// [78]). Drop it so resolution falls back to the new project's checkout;
	// the frontend re-applies the focus right after the switch (and the
	// focus path of the NEW project is revalidated against the live
	// worktree list at that point, so a focused external linked tree
	// restores correctly).
	f.setGitFocusPath("")
}
