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
// --others --ignored) dominate a ListDirectory / GetGitStatus round-trip.
// The two caches below memoize them per repo root (mu-guarded, TTL, bounded
// eviction) and are invalidated event-first: mutating git RPCs (via
// emitGitStatusChanged), the watcher debounce, and a project switch.
const (
	// gitStatusCacheTTL stays short: the tree changes constantly while
	// editing, and the watcher plus every mutating git RPC invalidate anyway.
	gitStatusCacheTTL = 10 * time.Second
	// gitIgnoredCacheTTL runs longer: the ignore set changes only when an
	// ignore-rule file or the tracked file set changes — both invalidate
	// explicitly.
	gitIgnoredCacheTTL = 30 * time.Second
	// gitCacheMaxSize bounds each repo-keyed cache so memory stays finite on
	// long-lived instances that visit many work directories.
	gitCacheMaxSize = 100
)

type gitStatusCacheEntry struct {
	status map[string]GitStatusEntry
	expiry time.Time
}

type gitIgnoredCacheEntry struct {
	ignored map[string]bool
	expiry  time.Time
}

// gitIgnoreRuleFiles invalidates the ignored-path cache; mirrors the session
// package's ignoreFileNames.
var gitIgnoreRuleFiles = map[string]struct{}{
	".gitignore": {},
	".aiignore":  {},
	".ignore":    {},
}

func hasIgnoreRuleChange(changedPaths []string) bool {
	for _, p := range changedPaths {
		if _, ok := gitIgnoreRuleFiles[filepath.Base(p)]; ok {
			return true
		}
	}
	return false
}

// evictOldestGitEntries bounds a repo-keyed cache by dropping the earliest-
// expiry entries once the map exceeds max. The caller holds the map's mutex.
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
	// f.ctx() has no deadline, and GitStatus spawns git with no internal
	// bound — a wedged git would hang the RPC. Bound with the shared 30s
	// git budget (review [176]); expiry is the ordinary git-failure path.
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

// gitCacheKey canonicalizes a repo-root cache key (symlink-resolved, failing
// soft to the cleaned input): population and invalidation hand in different
// forms (raw stored project path vs resolved focus root), and a plain string
// key would strand an entry invalidation can never reach.
// See specs/domains/workspace.md (per-root result caches for git RPCs).
func gitCacheKey(repoRoot string) string {
	return canonicalWorktreeRoot(filepath.Clean(repoRoot))
}

// cachedGitStatus returns the git status for repoRoot, reusing a snapshot
// within gitStatusCacheTTL. Errors are not cached (a transient git failure
// retries on the next call); the returned map is a caller-owned maps.Clone,
// so mutation can never corrupt the snapshot other readers share.
func (f *FrontendAPI) cachedGitStatus(repoRoot string) (map[string]GitStatusEntry, error) {
	if repoRoot == "" {
		return nil, errors.New("empty repo root")
	}
	now := time.Now()
	key := gitCacheKey(repoRoot)

	f.gitStatusCacheMu.Lock()
	if e, ok := f.gitStatusCache[key]; ok && now.Before(e.expiry) {
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
	f.gitStatusCache[key] = gitStatusCacheEntry{status: status, expiry: now.Add(gitStatusCacheTTL)}
	f.gitStatusCacheMu.Unlock()

	return maps.Clone(status), nil
}

// cachedGitIgnoredPaths returns the git-ignored set for repoRoot under the
// same contract as cachedGitStatus (errors not cached, caller-owned Clone),
// on gitIgnoredCacheTTL.
func (f *FrontendAPI) cachedGitIgnoredPaths(repoRoot string) (map[string]bool, error) {
	if repoRoot == "" {
		return nil, errors.New("empty repo root")
	}
	now := time.Now()
	key := gitCacheKey(repoRoot)

	f.gitIgnoredCacheMu.Lock()
	if e, ok := f.gitIgnoredCache[key]; ok && now.Before(e.expiry) {
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
	f.gitIgnoredCache[key] = gitIgnoredCacheEntry{ignored: ignored, expiry: now.Add(gitIgnoredCacheTTL)}
	f.gitIgnoredCacheMu.Unlock()

	return maps.Clone(ignored), nil
}

// invalidateGitCaches evicts both snapshots for one repo root; the hook every
// mutating git operation reaches via emitGitStatusChanged.
func (f *FrontendAPI) invalidateGitCaches(repoRoot string) {
	f.invalidateGitStatusCache(repoRoot)
	f.invalidateGitIgnoredCache(repoRoot)
}

func (f *FrontendAPI) invalidateGitStatusCache(repoRoot string) {
	if repoRoot == "" {
		return
	}
	f.gitStatusCacheMu.Lock()
	delete(f.gitStatusCache, gitCacheKey(repoRoot))
	f.gitStatusCacheMu.Unlock()
}

func (f *FrontendAPI) invalidateGitIgnoredCache(repoRoot string) {
	if repoRoot == "" {
		return
	}
	f.gitIgnoredCacheMu.Lock()
	delete(f.gitIgnoredCache, gitCacheKey(repoRoot))
	f.gitIgnoredCacheMu.Unlock()
}

// invalidateGitCachesOnWatcher routes a debounced watcher batch: any tree
// change can alter the status; an ignore-rule edit also drops the
// ignored-path set.
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

	// The Git-panel focus belongs to the previous project's repository: a
	// stale override would resolve every pathless git RPC into the old repo
	// until the frontend's asynchronous focus re-apply lands — wrong-repo
	// commit/push/reset (review [78]). Drop it; the new focus path is
	// revalidated against the live worktree list when re-applied.
	f.setGitFocusPath("")
}
