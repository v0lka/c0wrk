package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/ignore"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// resolvedTempDir returns a symlink-resolved temp dir so direct ignoreCache
// lookups match the resolved form ignoreCacheKey derives (macOS /var →
// /private/var).
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := runtimeTempDir(t)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return resolved
}

// IgnoreResolverFor is the blocking entry point over the shared per-root
// ignore cache (the file tree's ListDirectory), while injectIgnoreChecker
// stays non-blocking. These tests pin the singleflight, caching, eviction and
// cancellation contract.

func TestIgnoreResolverFor_BuildsOnceAndCaches(t *testing.T) {
	m, _, _ := testManager(t)
	root := resolvedTempDir(t)

	var builds atomic.Int32
	m.ignoreResolverBuild = func(ctx context.Context, gotRoot string) (*ignore.Resolver, error) {
		builds.Add(1)
		return ignore.NewResolverContext(ctx, gotRoot)
	}

	r1, err := m.IgnoreResolverFor(context.Background(), root)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	r2, err := m.IgnoreResolverFor(context.Background(), root)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if r1 != r2 {
		t.Fatal("expected the same cached resolver instance")
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("builds = %d, want 1", got)
	}
}

// TestIgnoreResolverFor_JoinsBackgroundBuild: a blocking caller arriving while
// the tracked background build is in flight waits for it and shares its
// result — no second walk. synctest.Wait proves the joiner is parked on the
// in-flight entry before the build is released.
func TestIgnoreResolverFor_JoinsBackgroundBuild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager(nil, func(Event) {}, "")
		defer m.Shutdown()
		const root = "/nonexistent/joins-root"

		release := make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })
		built := &ignore.Resolver{}
		var builds atomic.Int32
		m.ignoreResolverBuild = func(context.Context, string) (*ignore.Resolver, error) {
			builds.Add(1)
			<-release
			return built, nil
		}

		m.startIgnoreBuild(root)
		type result struct {
			r   *ignore.Resolver
			err error
		}
		done := make(chan result, 1)
		go func() {
			r, err := m.IgnoreResolverFor(context.Background(), root)
			done <- result{r, err}
		}()
		synctest.Wait()
		select {
		case res := <-done:
			t.Fatalf("joiner returned before the build finished: %+v", res)
		default:
		}

		releaseOnce.Do(func() { close(release) })
		res := <-done
		if res.err != nil {
			t.Fatalf("join: %v", res.err)
		}
		if res.r != built {
			t.Fatal("joiner must receive the in-flight build's resolver")
		}
		if got := builds.Load(); got != 1 {
			t.Fatalf("builds = %d, want 1 (the joiner must share the in-flight build)", got)
		}
	})
}

func TestIgnoreResolverFor_ErrorNotCached(t *testing.T) {
	m, _, _ := testManager(t)
	root := resolvedTempDir(t)

	var builds atomic.Int32
	buildErr := errors.New("walk failed")
	m.ignoreResolverBuild = func(ctx context.Context, gotRoot string) (*ignore.Resolver, error) {
		if builds.Add(1) == 1 {
			return nil, buildErr
		}
		return ignore.NewResolverContext(ctx, gotRoot)
	}

	if _, err := m.IgnoreResolverFor(context.Background(), root); !errors.Is(err, buildErr) {
		t.Fatalf("first call err = %v, want %v", err, buildErr)
	}
	if _, err := m.IgnoreResolverFor(context.Background(), root); err != nil {
		t.Fatalf("second call must retry the build, got %v", err)
	}
	if got := builds.Load(); got != 2 {
		t.Fatalf("builds = %d, want 2", got)
	}
}

// TestIgnoreResolverFor_MidBuildEvictionNotStored: a build whose slot was
// evicted while the walk was in flight (an ignore-file edit landed mid-walk)
// serves its caller but is not cached — it may predate the edit.
func TestIgnoreResolverFor_MidBuildEvictionNotStored(t *testing.T) {
	m, _, _ := testManager(t)
	root := resolvedTempDir(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	var builds atomic.Int32
	m.ignoreResolverBuild = func(ctx context.Context, gotRoot string) (*ignore.Resolver, error) {
		if builds.Add(1) == 1 {
			close(entered)
			<-release
		}
		return ignore.NewResolverContext(ctx, gotRoot)
	}

	done := make(chan error, 1)
	go func() {
		_, err := m.IgnoreResolverFor(context.Background(), root)
		done <- err
	}()
	<-entered
	m.InvalidateIgnoreCache([]string{filepath.Join(root, ".gitignore")})
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first call: %v", err)
	}

	if _, ok := m.ignoreCache.Load(root); ok {
		t.Fatal("a mid-build-evicted result must not be cached")
	}
	if _, err := m.IgnoreResolverFor(context.Background(), root); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := builds.Load(); got != 2 {
		t.Fatalf("builds = %d, want 2", got)
	}
}

// TestIgnoreResolverFor_ContextCancelWhileJoining: the joiner waits on its own
// context — a cancelled caller stops waiting, and the build it joined still
// completes and is cached.
func TestIgnoreResolverFor_ContextCancelWhileJoining(t *testing.T) {
	m, _, _ := testManager(t)
	root := resolvedTempDir(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	m.ignoreResolverBuild = func(ctx context.Context, gotRoot string) (*ignore.Resolver, error) {
		close(entered)
		<-release
		return ignore.NewResolverContext(ctx, gotRoot)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		if !waitBackgroundSettled(m, 5*time.Second) {
			t.Errorf("background build did not settle after release")
		}
	})

	m.startIgnoreBuild(root)
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.IgnoreResolverFor(ctx, root)
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("join with cancelled context = %v, want context.Canceled", err)
	}

	releaseOnce.Do(func() { close(release) })
	if !waitBackgroundSettled(m, 5*time.Second) {
		t.Fatal("background build did not settle after release")
	}
	v, ok := m.ignoreCache.Load(root)
	if !ok {
		t.Fatal("the joined build must still be cached after the joiner cancelled")
	}
	if entry, ok := v.(*ignoreResolverEntry); !ok || entry.r == nil {
		t.Fatalf("cache must hold a finished entry, got %T", v)
	}
}

// TestPrewarmIgnoreResolver_SharesEntryAcrossSymlink: prewarm returns without
// waiting for the walk (a synchronous build would deadlock the bubble), and
// it stores under the symlink-resolved key, so a later IgnoreResolverFor via
// the physical path reuses that build instead of walking again.
func TestPrewarmIgnoreResolver_SharesEntryAcrossSymlink(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager(nil, func(Event) {}, "")
		defer m.Shutdown()
		base := resolvedTempDir(t)
		physical := filepath.Join(base, "real")
		link := filepath.Join(base, "link")
		if err := os.Mkdir(physical, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(physical, link); err != nil {
			t.Fatal(err)
		}

		release := make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })
		var prewarmed *ignore.Resolver
		var builds atomic.Int32
		m.ignoreResolverBuild = func(context.Context, string) (*ignore.Resolver, error) {
			r := &ignore.Resolver{} // a fresh instance per build
			if builds.Add(1) == 1 {
				prewarmed = r
			}
			<-release
			return r, nil
		}

		m.PrewarmIgnoreResolver(link) // must not block on the walk
		releaseOnce.Do(func() { close(release) })
		synctest.Wait() // the prewarm build has finished and stored its entry

		r, err := m.IgnoreResolverFor(context.Background(), physical)
		if err != nil {
			t.Fatalf("IgnoreResolverFor: %v", err)
		}
		if r != prewarmed {
			t.Fatal("IgnoreResolverFor via the physical path must reuse the prewarmed build")
		}
		if got := builds.Load(); got != 1 {
			t.Fatalf("builds = %d, want 1", got)
		}
	})
}

// TestInjectIgnoreChecker_SkipsInFlightRoot: the agent path never blocks on
// an in-flight build (a huge root would hang SendMessage) and never attaches
// a placeholder resolver for it — only finished roots are filtered.
func TestInjectIgnoreChecker_SkipsInFlightRoot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager(nil, func(Event) {}, "")
		defer m.Shutdown()
		const finishedRoot = "/nonexistent/finished"
		const inFlightRoot = "/nonexistent/in-flight"
		finished := &ignore.Resolver{}
		storeFakeEntry(m, finishedRoot, finished)
		m.ignoreCache.Store(inFlightRoot, &ignoreResolverEntry{done: make(chan struct{})})
		m.ignoreResolverBuild = func(context.Context, string) (*ignore.Resolver, error) {
			t.Error("no build may start: both roots already have cache entries")
			return nil, errors.New("unexpected build")
		}

		ctx := m.injectIgnoreChecker(context.Background(),
			&Session{WorkspacePath: finishedRoot},
			[]core.WorkDirectory{{Path: inFlightRoot}})

		multi, ok := sdktools.IgnoreCheckerFrom(ctx).(*ignore.Multi)
		if !ok || multi == nil {
			t.Fatalf("expected an *ignore.Multi checker, got %T", sdktools.IgnoreCheckerFrom(ctx))
		}
		got := multi.Resolvers()
		if len(got) != 1 || got[0] != finished {
			t.Fatalf("checker resolvers = %v, want only the finished root's resolver", got)
		}
	})
}

// TestIgnoreResolverFor_ShutdownReleasesJoiner: a file-tree call waiting on a
// background build is released with an error when Shutdown aborts the walk,
// and the aborted build leaves no cache entry behind.
func TestIgnoreResolverFor_ShutdownReleasesJoiner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager(nil, func(Event) {}, "")
		const root = "/nonexistent/shutdown-root"
		m.ignoreResolverBuild = func(ctx context.Context, _ string) (*ignore.Resolver, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}

		m.startIgnoreBuild(root)
		done := make(chan error, 1)
		go func() {
			_, err := m.IgnoreResolverFor(context.Background(), root)
			done <- err
		}()
		synctest.Wait()

		m.Shutdown()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("joiner err = %v, want context.Canceled", err)
		}
		if _, ok := m.ignoreCache.Load(root); ok {
			t.Fatal("an aborted build must not stay cached")
		}
	})
}

// TestIgnoreResolverFor_FailedBuildKeepsNewerEntry: a build that fails after
// its slot was evicted and re-filled must not delete the newer entry.
func TestIgnoreResolverFor_FailedBuildKeepsNewerEntry(t *testing.T) {
	m, _, _ := testManager(t)
	root := resolvedTempDir(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	buildErr := errors.New("walk failed")
	m.ignoreResolverBuild = func(context.Context, string) (*ignore.Resolver, error) {
		close(entered)
		<-release
		return nil, buildErr
	}

	done := make(chan error, 1)
	go func() {
		_, err := m.IgnoreResolverFor(context.Background(), root)
		done <- err
	}()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	<-entered
	m.InvalidateIgnoreCache([]string{filepath.Join(root, ".gitignore")})
	storeFakeResolver(m, root) // the newer entry a concurrent caller stored
	newer, _ := m.ignoreCache.Load(root)
	close(release)
	if err := <-done; !errors.Is(err, buildErr) {
		t.Fatalf("err = %v, want %v", err, buildErr)
	}

	got, ok := m.ignoreCache.Load(root)
	if !ok || got != newer {
		t.Fatal("a failed stale build must not evict the newer cache entry")
	}
}

// TestInvalidateIgnoreCacheOverlapping evicts the watched root, its
// enclosing root (No Project) and nested roots, and keeps unrelated ones.
func TestInvalidateIgnoreCacheOverlapping(t *testing.T) {
	m, _, _ := testManager(t)
	base := resolvedTempDir(t)
	watch := filepath.Join(base, "proj")
	nested := filepath.Join(watch, "sub")
	other := filepath.Join(base, "other")
	for _, d := range []string{nested, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []string{base, watch, nested, other} {
		storeFakeResolver(m, r)
	}

	m.InvalidateIgnoreCacheOverlapping(watch)

	for _, r := range []string{base, watch, nested} {
		if _, ok := m.ignoreCache.Load(r); ok {
			t.Errorf("expected %s evicted", r)
		}
	}
	if _, ok := m.ignoreCache.Load(other); !ok {
		t.Error("unrelated root must stay cached")
	}
}
