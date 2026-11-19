package backend

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/core/workspace"
	"golang.org/x/sync/singleflight"
)

// The heavy git subprocesses behind the workspace surface are memoized in
// per-repo snapshot caches (specs/domains/workspace.md):
//
//   - status / status-lite (git status --porcelain) — expensive, so a stale
//     snapshot serves instantly while at most one background refresh per
//     gitRefreshMinInterval runs (stale-while-revalidate). That path is
//     reachable only while the tree is QUIET: any real change drops the
//     entries outright (below), and the consumers' single refetch per
//     workspace:tree_changed is the only delivery of git status to the UI —
//     serving a pre-change snapshot there would pin it until the next event.
//   - ignore flags (git check-ignore over the listed entries) — a batch over
//     a handful of paths, so a stale entry revalidates SYNCHRONOUSLY on read
//     (SWR would render freshly listed files with last-cycle flags).
//
// Every real change therefore hard-drops the status entries — mutating git
// RPCs via emitGitStatusChanged and watcher batches alike — so the refetch
// each triggers is a synchronous cold fetch (singleflight-coalesced; the
// cache generation discards any fetch that spans a drop, so a slow git run
// can never re-cache pre-drop state). The flags cache is deliberately NOT
// dropped there: check-ignore is pattern-only (it never consults the index
// or the tracked set), so a commit or stage cannot change its answer.
const (
	// gitStatusCacheTTL stays short: the tree changes constantly while
	// editing, and the watcher plus every mutating git RPC invalidate anyway.
	gitStatusCacheTTL = 10 * time.Second
	// gitIgnoredCacheTTL runs longer: ignore flags change only when an
	// ignore-rule file changes or the listed set itself changes — a stale
	// read revalidates synchronously either way.
	gitIgnoredCacheTTL = 30 * time.Second
	// gitRefreshMinInterval bounds how often stale-while-revalidate launches
	// an actual git run for one entry: between launches every reader shares
	// the last snapshot.
	gitRefreshMinInterval = 3 * time.Second
	// gitCacheMaxSize bounds each repo-keyed cache so memory stays finite on
	// long-lived instances that visit many work directories.
	gitCacheMaxSize = 100
	// gitShutdownJoinBudget bounds Shutdown's wait for in-flight background
	// git refreshes (desktop/startup.go Shutdown step joinGitBackground).
	GitShutdownJoinBudget = 5 * time.Second
)

// gitIgnoreRuleFiles marks a watcher change as an ignore-rule edit; mirrors
// the session package's ignoreFileNames.
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

// gitMutationFiles marks a watcher change as a git-mutation signal: the index
// (stage/unstage/commit/stash) and HEAD plus the pending-operation heads
// (checkout, merge, rebase, cherry-pick). An EXTERNAL git mutation — a
// terminal command, another tool — reaches no backend RPC, so without this
// funnel nothing would move the viewer-diff epoch and the Git panel would
// rely on the status TTL alone, even though the watcher sees the .git write.
// Refs updates (commit, branch, fetch of remote-tracking refs) match by path.
var gitMutationFiles = map[string]struct{}{
	"index":            {},
	"HEAD":             {},
	"MERGE_HEAD":       {},
	"REBASE_HEAD":      {},
	"CHERRY_PICK_HEAD": {},
}

func hasGitMutationChange(changedPaths []string) bool {
	for _, p := range changedPaths {
		slash := filepath.ToSlash(p)
		if _, ok := gitMutationFiles[filepath.Base(p)]; ok && strings.Contains(slash, "/.git/") {
			return true
		}
		if strings.Contains(slash, "/.git/refs/") {
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

// gitSnapshotEntry is one cache slot. data/expiry are the served snapshot and
// its freshness window; lastRefresh is when a git run last COMPLETED for the
// key (the stale-while-revalidate throttle measures against it, so failed
// refreshes also back off); refreshing guards a second background launch.
type gitSnapshotEntry[V any] struct {
	data        V
	expiry      time.Time
	lastRefresh time.Time
	refreshing  bool
	// launchGen records the cache generation the entry's data was stored
	// under, re-stamped by every background-refresh launch: finalize matches
	// a refresh run against it, so an unrelated repo's cache-wide generation
	// bump can neither discard — nor strand the refreshing guard of — this
	// entry's refresh, while a same-key drop that re-created the entry under
	// a newer generation invalidates the stale run outright.
	launchGen uint64
}

// gitSnapshotCache memoizes one heavy git result family under derived keys.
// Readers always receive a maps.Clone: mutation can never corrupt the
// snapshot other readers share. Errors are never cached (a transient git
// failure retries on the next call).
//
// M is the cached map type (map[string]GitStatusEntry / map[string]bool);
// cloneSnapshot relies on the ~map constraint to hand out copies.
type gitSnapshotCache[M ~map[string]V2, V2 any] struct {
	mu      sync.Mutex
	entries map[string]*gitSnapshotEntry[M]
	max     int
	ttl     time.Duration
	// minRefresh throttles background refresh launches; staleSync selects the
	// flags-cache mode instead: a stale read revalidates synchronously.
	minRefresh time.Duration
	staleSync  bool
	flight     singleflight.Group
	// gen bumps on every drop: a fetch that starts under one generation and
	// ends under another observed pre-drop state, so its result is discarded
	// instead of re-caching (or clobbering a fresher entry with) a snapshot
	// the drop declared obsolete. The singleflight key is generation-scoped
	// too — a post-drop reader starts its own fetch instead of joining, and
	// receiving, a pre-drop result. Synchronous fetches compare against this
	// cache-wide counter; background refreshes match per-entry (the entry's
	// launchGen), so an unrelated repo's drop can neither discard nor strand
	// them.
	gen uint64
	// spawn runs a background refresh goroutine (tracked upstream so Shutdown
	// joins it); nil means no background path is wired — stale entries then
	// keep serving until the next synchronous path.
	spawn       func(fn func())
	fetchBudget time.Duration
	// settled runs after every background refresh settles — test hook
	// (nil in production).
	settled func(key string, err error)
}

// snapshot returns the cached value for key: a fresh entry serves directly, a
// stale one serves its snapshot plus a (throttled) background refresh — or
// revalidates synchronously in staleSync mode — and a miss runs fetch
// synchronously under singleflight, so concurrent cold callers share one git
// run. base bounds the synchronous fetch (the app context, so shutdown
// cancels an in-flight run); background fetches always detach.
func (c *gitSnapshotCache[M, V2]) snapshot(base context.Context, key string, fetch func(context.Context) (M, error)) (M, error) {
	now := time.Now()

	c.mu.Lock()
	e, ok := c.entries[key]
	if ok && now.Before(e.expiry) {
		out := maps.Clone(e.data)
		c.mu.Unlock()
		return out, nil
	}
	if ok && !c.staleSync {
		out := maps.Clone(e.data)
		launch := !e.refreshing && now.Sub(e.lastRefresh) >= c.minRefresh
		gen := c.gen
		if launch {
			e.refreshing = true
			e.launchGen = gen
		}
		c.mu.Unlock()
		if launch {
			c.launchRefresh(key, gen, fetch)
		}
		return out, nil
	}
	gen := c.gen
	c.mu.Unlock()

	// Clone the shared flight result: every snapshot() caller — the fetch
	// owner and every singleflight joiner — receives its own copy, so a
	// caller-side mutation can never corrupt what was stored.
	out, err := c.fetchNow(base, key, gen, fetch)
	if err != nil {
		return nil, err
	}
	return maps.Clone(out), nil
}

// markStale ends the freshness window of key's entry, if any, without
// launching anything — the next read serves the snapshot and revalidates
// (synchronously under staleSync, else in the background). The watcher batch
// uses it only for the flags family, whose staleSync revalidation answers the
// very next read with fresh data.
func (c *gitSnapshotCache[M, V2]) markStale(key string) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		e.expiry = time.Time{}
	}
	c.mu.Unlock()
}

// dropWhere deletes every entry whose key fails keep and bumps the cache
// generation: every fetch in flight across the drop is discarded (see gen),
// so nothing it observed pre-drop can re-enter the cache.
func (c *gitSnapshotCache[M, V2]) dropWhere(keep func(key string) bool) {
	c.mu.Lock()
	c.gen++
	for k := range c.entries {
		if !keep(k) {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

// seed installs an entry directly — test bootstrap for the stale-serving
// paths (a stale seed backdates lastRefresh past the throttle window).
func (c *gitSnapshotCache[M, V2]) seed(key string, data M, stale bool) {
	now := time.Now()
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*gitSnapshotEntry[M])
	}
	e := &gitSnapshotEntry[M]{data: data, lastRefresh: now, launchGen: c.gen}
	if stale {
		e.expiry = time.Time{}
		e.lastRefresh = now.Add(-c.minRefresh)
	} else {
		e.expiry = now.Add(c.ttl)
	}
	c.entries[key] = e
	c.mu.Unlock()
}

// flightKey scopes the singleflight key by the generation the fetch started
// under: a post-drop caller must not join a pre-drop flight and receive its
// stale result.
func (c *gitSnapshotCache[M, V2]) flightKey(key string, gen uint64) string {
	return key + "\x00" + strconv.FormatUint(gen, 10)
}

// fetchNow runs fetch under singleflight and stores the result; ctx bounds
// the run.
func (c *gitSnapshotCache[M, V2]) fetchNow(ctx context.Context, key string, gen uint64, fetch func(context.Context) (M, error)) (M, error) {
	runCtx, cancel := context.WithTimeout(ctx, c.fetchBudget)
	defer cancel()
	v, err, _ := c.flight.Do(c.flightKey(key, gen), func() (any, error) {
		data, ferr := fetch(runCtx)
		c.finalize(key, data, ferr, time.Now(), true, gen)
		if ferr != nil {
			return nil, ferr
		}
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	out, ok := v.(M)
	if !ok {
		return out, fmt.Errorf("git snapshot cache: unexpected flight result type %T", v)
	}
	return out, nil
}

// launchRefresh runs one background refresh on the cache's tracked runner.
// The context detaches from the caller per the outliving-goroutine rule: a
// request that scheduled the refresh must not cancel it mid-run. A refresh
// landing on a dropped slot (a mutation or a project switch raced it) does
// not recreate the entry — the next read cold-fetches post-drop state
// instead of re-adding a pre-drop snapshot.
func (c *gitSnapshotCache[M, V2]) launchRefresh(key string, gen uint64, fetch func(context.Context) (M, error)) {
	if c.spawn == nil {
		c.finalize(key, nil, errors.New("git snapshot cache: no background runner"), time.Now(), false, gen)
		return
	}
	c.spawn(func() {
		_, err := c.fetchNowDetached(key, gen, fetch)
		if c.settled != nil {
			c.settled(key, err)
		}
	})
}

// fetchNowDetached is fetchNow for the background path: detached context, no
// entry creation on success.
func (c *gitSnapshotCache[M, V2]) fetchNowDetached(key string, gen uint64, fetch func(context.Context) (M, error)) (M, error) {
	runCtx, cancel := context.WithTimeout(context.Background(), c.fetchBudget)
	defer cancel()
	v, err, _ := c.flight.Do(c.flightKey(key, gen), func() (any, error) {
		data, ferr := fetch(runCtx)
		c.finalize(key, data, ferr, time.Now(), false, gen)
		if ferr != nil {
			return nil, ferr
		}
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	out, ok := v.(M)
	if !ok {
		return out, fmt.Errorf("git snapshot cache: unexpected flight result type %T", v)
	}
	return out, nil
}

// finalize records a finished run. A synchronous cold fetch creates the
// entry only when its generation still matches the cache's — a run spanning
// a drop of its own key observed pre-drop state and must never be created
// (or overwrite a fresher post-drop entry). A background refresh stores only
// into the entry it was launched for, matched by the entry's recorded launch
// generation: a same-key drop deleted it or re-created it under a newer
// launch, while an unrelated repo's cache-wide bump says nothing about this
// entry's data. Failure never touches data — it releases the launching
// entry's refreshing guard and backdates lastRefresh, so a persistently
// failing git retries at most once per minRefresh. Caller holds no locks;
// the flight singleton guarantees one finalize per key per run.
func (c *gitSnapshotCache[M, V2]) finalize(key string, data M, err error, now time.Time, create bool, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if err != nil {
		if ok && (create || e.launchGen == gen) {
			e.refreshing = false
			e.lastRefresh = now
		}
		return
	}
	if create {
		if gen != c.gen {
			return
		}
	} else {
		if !ok || gen != e.launchGen {
			return
		}
	}
	if !ok {
		if !create {
			return
		}
		if c.entries == nil {
			c.entries = make(map[string]*gitSnapshotEntry[M])
		}
		if len(c.entries) >= c.max {
			evictOldestGitEntries(c.entries, func(e *gitSnapshotEntry[M]) time.Time { return e.expiry }, c.max)
		}
	}
	c.entries[key] = &gitSnapshotEntry[M]{data: data, expiry: now.Add(c.ttl), lastRefresh: now, launchGen: gen}
}

// --- FrontendAPI wiring -----------------------------------------------------

// gitCacheKey canonicalizes a repo-root cache key (symlink-resolved, failing
// soft to the cleaned input): population and invalidation hand in different
// forms (raw stored project path vs resolved focus root), and a plain string
// key would strand an entry invalidation can never reach.
// See specs/domains/workspace.md (per-root result caches for git RPCs).
func gitCacheKey(repoRoot string) string {
	return canonicalWorktreeRoot(filepath.Clean(repoRoot))
}

// gitFlagsCacheKey keys the ignore-flags cache: one entry per (repo root,
// listed directory, listing scope). The scope discriminator keeps one
// listing's verdict set from being served to a differently-shaped listing of
// the SAME directory: a flat listing caches its immediate children, a
// recursive one (file search, @-completions) its descendants — served
// cross-scope, every path the cached set never covered would silently lose
// its flag.
func gitFlagsCacheKey(repoRoot, dir, scope string) string {
	return gitCacheKey(repoRoot) + "\x00" + gitCacheKey(dir) + "\x00" + scope
}

// ensureGitCaches lazily builds the three snapshot caches; the constructor
// path and the tests that build FrontendAPI by hand both land here.
func (f *FrontendAPI) ensureGitCaches() {
	f.gitStatusCacheMu.Lock()
	defer f.gitStatusCacheMu.Unlock()
	if f.gitStatusCache != nil {
		return
	}
	spawn := func(fn func()) {
		f.trackGitBg(fn)
	}
	f.gitStatusCache = &gitSnapshotCache[map[string]GitStatusEntry, GitStatusEntry]{
		entries:     make(map[string]*gitSnapshotEntry[map[string]GitStatusEntry]),
		max:         gitCacheMaxSize,
		ttl:         gitStatusCacheTTL,
		minRefresh:  gitRefreshMinInterval,
		spawn:       spawn,
		fetchBudget: gitCmdTimeout,
		settled:     f.gitRefreshSettled,
	}
	f.gitStatusLiteCache = &gitSnapshotCache[map[string]GitStatusEntry, GitStatusEntry]{
		entries:     make(map[string]*gitSnapshotEntry[map[string]GitStatusEntry]),
		max:         gitCacheMaxSize,
		ttl:         gitStatusCacheTTL,
		minRefresh:  gitRefreshMinInterval,
		spawn:       spawn,
		fetchBudget: gitCmdTimeout,
		settled:     f.gitRefreshSettled,
	}
	f.gitIgnoredFlagsCache = &gitSnapshotCache[map[string]bool, bool]{
		entries:     make(map[string]*gitSnapshotEntry[map[string]bool]),
		max:         gitCacheMaxSize,
		ttl:         gitIgnoredCacheTTL,
		staleSync:   true,
		fetchBudget: gitCmdTimeout,
	}
}

// runGitStatus delegates to the injectable gitStatusFn test seam, falling back
// to the real workspace helper in production. The snapshot cache bounds the
// run with fetchBudget; the same bound here keeps a future uncached caller
// from spawning an unbounded git.
func (f *FrontendAPI) runGitStatus(repoRoot string) (map[string]GitStatusEntry, error) {
	if f.gitStatusFn != nil {
		return f.gitStatusFn(repoRoot)
	}
	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()
	return workspace.GitStatus(ctx, repoRoot)
}

// runGitStatusLite mirrors runGitStatus for the -unormal tree variant.
func (f *FrontendAPI) runGitStatusLite(repoRoot string) (map[string]GitStatusEntry, error) {
	if f.gitStatusLiteFn != nil {
		return f.gitStatusLiteFn(repoRoot)
	}
	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()
	return workspace.GitStatusLite(ctx, repoRoot)
}

// runGitIgnoredFlags delegates to the injectable gitIgnoredFlagsFn test seam,
// falling back to the batched check-ignore over paths in production. The
// scope only shapes the cache key; the git batch itself is scope-agnostic.
func (f *FrontendAPI) runGitIgnoredFlags(repoRoot, dir, scope string, paths []string) (map[string]bool, error) {
	if f.gitIgnoredFlagsFn != nil {
		return f.gitIgnoredFlagsFn(repoRoot, dir, scope, paths)
	}
	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()
	return workspace.FilterGitIgnoredPaths(ctx, repoRoot, paths)
}

// cachedGitStatus returns the git status for repoRoot (porcelain -uall, the
// Git-panel contract), reusing a snapshot within gitStatusCacheTTL and
// revalidating stale snapshots in the background.
func (f *FrontendAPI) cachedGitStatus(repoRoot string) (map[string]GitStatusEntry, error) {
	if repoRoot == "" {
		return nil, errors.New("empty repo root")
	}
	f.ensureGitCaches()
	return f.gitStatusCache.snapshot(f.ctx(), gitCacheKey(repoRoot), func(ctx context.Context) (map[string]GitStatusEntry, error) {
		return f.runGitStatus(repoRoot)
	})
}

// cachedGitStatusLite is cachedGitStatus for the tree's -unormal variant:
// untracked directories stay collapsed, so git never enumerates every
// untracked file under them.
func (f *FrontendAPI) cachedGitStatusLite(repoRoot string) (map[string]GitStatusEntry, error) {
	if repoRoot == "" {
		return nil, errors.New("empty repo root")
	}
	f.ensureGitCaches()
	return f.gitStatusLiteCache.snapshot(f.ctx(), gitCacheKey(repoRoot), func(ctx context.Context) (map[string]GitStatusEntry, error) {
		return f.runGitStatusLite(repoRoot)
	})
}

// cachedGitIgnoredFlags returns which of the paths listed under dir (a child
// of repoRoot) git's ignore rules exclude. The entry is keyed per (root, dir,
// listing scope — flat vs recursive, see gitFlagsCacheKey) and revalidates
// synchronously when stale: the batched check-ignore is a few milliseconds,
// so the listing always carries current-cycle flags.
func (f *FrontendAPI) cachedGitIgnoredFlags(repoRoot, dir, scope string, paths []string) (map[string]bool, error) {
	if repoRoot == "" || dir == "" {
		return nil, errors.New("empty repo root or dir")
	}
	f.ensureGitCaches()
	return f.gitIgnoredFlagsCache.snapshot(f.ctx(), gitFlagsCacheKey(repoRoot, dir, scope), func(ctx context.Context) (map[string]bool, error) {
		return f.runGitIgnoredFlags(repoRoot, dir, scope, paths)
	})
}

// invalidateGitCaches evicts both status snapshots for one repo root — the
// funnel every real change reaches: mutating git operations via
// emitGitStatusChanged and workspace-watcher batches via
// invalidateGitCachesOnWatcher. The flags cache is intentionally untouched
// (check-ignore never consults the tracked set — see the const block).
func (f *FrontendAPI) invalidateGitCaches(repoRoot string) {
	if repoRoot == "" {
		return
	}
	key := gitCacheKey(repoRoot)
	f.statusCacheDropWhere(func(k string) bool { return k != key })
}

// statusCacheDropWhere routes a hard drop to both status families.
func (f *FrontendAPI) statusCacheDropWhere(keep func(key string) bool) {
	f.ensureGitCaches()
	f.gitStatusCache.dropWhere(keep)
	f.gitStatusLiteCache.dropWhere(keep)
}

// invalidateGitCachesOnWatcher routes a debounced watcher batch: any tree
// change drops the status snapshots so the consumers' own refetch (one
// GetGitStatus / listing per workspace:tree_changed, with no trailing read)
// is a synchronous cold fetch — a mark would leave that read serving
// pre-change status until the next event. A git-internal write (.git index,
// HEAD, refs — a mutation NO backend RPC performed, e.g. a terminal git
// command) additionally funnels through emitGitStatusChanged, so the
// frontend's viewer-diff epoch moves and an externally staged/committed
// overlay heals like a UI-initiated one. An ignore-rule edit stales the
// per-directory ignore flags, which revalidate synchronously on read.
func (f *FrontendAPI) invalidateGitCachesOnWatcher(repoRoot string, changedPaths []string) {
	if repoRoot == "" {
		return
	}
	if hasGitMutationChange(changedPaths) {
		// emitGitStatusChanged drops the status snapshots itself.
		f.emitGitStatusChanged(repoRoot)
	} else {
		f.invalidateGitCaches(repoRoot)
	}
	if hasIgnoreRuleChange(changedPaths) {
		f.markGitFlagsStaleUnderRoot(repoRoot)
	}
}

// markGitFlagsStaleUnderRoot marks every per-directory flags entry of repoRoot
// stale (the ignore-rule-edit watcher path; any other edit leaves the
// pattern verdicts untouched).
func (f *FrontendAPI) markGitFlagsStaleUnderRoot(repoRoot string) {
	if repoRoot == "" {
		return
	}
	f.ensureGitCaches()
	prefix := gitCacheKey(repoRoot) + "\x00"
	c := f.gitIgnoredFlagsCache
	c.mu.Lock()
	keys := make([]string, 0, len(c.entries))
	for k := range c.entries {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	c.mu.Unlock()
	for _, k := range keys {
		c.markStale(k)
	}
}

// invalidateAllGitCaches drops every cached git snapshot. Called on a project
// switch so state from the previous workspace can never leak into the new one.
func (f *FrontendAPI) invalidateAllGitCaches() {
	f.statusCacheDropWhere(func(string) bool { return false })
	f.ensureGitCaches()
	f.gitIgnoredFlagsCache.dropWhere(func(string) bool { return false })

	// The Git-panel focus belongs to the previous project's repository: a
	// stale override would resolve every pathless git RPC into the old repo
	// until the frontend's asynchronous focus re-apply lands — wrong-repo
	// commit/push/reset (review [78]). Drop it; the new focus path is
	// revalidated against the live worktree list when re-applied.
	f.setGitFocusPath("")
}

// trackGitBg admits fn as a tracked background goroutine unless admission is
// closed. The stopped check and the WaitGroup.Add happen under gitBgMu, so an
// admission can never run concurrently with (or after) JoinGitBackground's
// Wait — a bare check-then-Add could Add once the counter was already
// observed zero by Wait, violating the WaitGroup contract and leaking the
// goroutine past the bounded shutdown join. Reports whether fn was admitted.
func (f *FrontendAPI) trackGitBg(fn func()) bool {
	f.gitBgMu.Lock()
	defer f.gitBgMu.Unlock()
	if f.gitBgStopped.Load() {
		return false
	}
	f.gitBgWG.Add(1)
	go func() {
		defer f.gitBgWG.Done()
		fn()
	}()
	return true
}

// JoinGitBackground waits up to budget for in-flight background git work
// (stale-while-revalidate refreshes and ignore-ready deliveries) on api, then
// stops admitting new ones. The bounded wait keeps a wedged git from stalling
// teardown past the shutdown watchdog's patience. Package-level on purpose:
// an exported FrontendAPI METHOD would be promoted onto the Wails binding
// surface as a renderer-callable RPC (it is a shutdown-internal step, not an
// API — an arbitrary caller-supplied budget would wedge an RPC goroutine and
// permanently disable background refresh admission).
func JoinGitBackground(api *FrontendAPI, budget time.Duration) {
	api.gitBgMu.Lock()
	api.gitBgStopped.Store(true)
	api.gitBgMu.Unlock()
	done := make(chan struct{})
	go func() {
		api.gitBgWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(budget):
	}
}
