package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/ignore"
)

// storeFakeResolver puts a non-nil resolver into the cache under root,
// simulating a completed background build. The invalidation logic never
// inspects the resolver value — only the key (resolved root) — so a bare
// &ignore.Resolver{} is sufficient.
func storeFakeResolver(m *Manager, root string) {
	m.ignoreCache.Store(root, &ignore.Resolver{})
}

func TestInvalidateIgnoreCache_GitignoreAtRootEvicts(t *testing.T) {
	m, _, _ := testManager(t)
	root := runtimeTempDir(t)
	storeFakeResolver(m, root)

	m.InvalidateIgnoreCache([]string{filepath.Join(root, ".gitignore")})

	if _, ok := m.ignoreCache.Load(root); ok {
		t.Fatal("expected cached resolver for root to be evicted after .gitignore change")
	}
}

func TestInvalidateIgnoreCache_NestedGitignoreEvicts(t *testing.T) {
	m, _, _ := testManager(t)
	root := runtimeTempDir(t)
	storeFakeResolver(m, root)

	// ignore.NewResolver walks the entire tree collecting every .gitignore,
	// so a nested occurrence must invalidate the root just like a root-level one.
	m.InvalidateIgnoreCache([]string{filepath.Join(root, "pkg", "sub", ".gitignore")})

	if _, ok := m.ignoreCache.Load(root); ok {
		t.Fatal("expected cached resolver evicted after nested .gitignore change")
	}
}

func TestInvalidateIgnoreCache_AIIgnoreEvicts(t *testing.T) {
	m, _, _ := testManager(t)
	root := runtimeTempDir(t)
	storeFakeResolver(m, root)

	m.InvalidateIgnoreCache([]string{filepath.Join(root, ".aiignore")})

	if _, ok := m.ignoreCache.Load(root); ok {
		t.Fatal("expected cached resolver evicted after .aiignore change")
	}
}

func TestInvalidateIgnoreCache_NonIgnoreFileKeepsCache(t *testing.T) {
	m, _, _ := testManager(t)
	root := runtimeTempDir(t)
	storeFakeResolver(m, root)

	m.InvalidateIgnoreCache([]string{filepath.Join(root, "main.go"), filepath.Join(root, "README.md")})

	if _, ok := m.ignoreCache.Load(root); !ok {
		t.Fatal("cached resolver must survive non-ignore-file changes")
	}
}

func TestInvalidateIgnoreCache_OnlyAffectedRootEvicted(t *testing.T) {
	m, _, _ := testManager(t)
	rootA := runtimeTempDir(t)
	rootB := runtimeTempDir(t)
	storeFakeResolver(m, rootA)
	storeFakeResolver(m, rootB)

	m.InvalidateIgnoreCache([]string{filepath.Join(rootA, ".gitignore")})

	if _, ok := m.ignoreCache.Load(rootA); ok {
		t.Fatal("affected root A must be evicted")
	}
	if _, ok := m.ignoreCache.Load(rootB); !ok {
		t.Fatal("unaffected root B must be retained")
	}
}

func TestInvalidateIgnoreCache_EmptyNoop(t *testing.T) {
	m, _, _ := testManager(t)
	root := runtimeTempDir(t)
	storeFakeResolver(m, root)

	// Must not panic and must not evict when no ignore files are involved.
	m.InvalidateIgnoreCache(nil)
	m.InvalidateIgnoreCache([]string{})

	if _, ok := m.ignoreCache.Load(root); !ok {
		t.Fatal("cached resolver must survive empty change list")
	}
}

func TestInvalidateIgnoreCache_MixedBatchEvictsOnlyIgnoreFiles(t *testing.T) {
	m, _, _ := testManager(t)
	root := runtimeTempDir(t)
	storeFakeResolver(m, root)

	// A debounced watcher batch mixing regular files and an ignore file.
	m.InvalidateIgnoreCache([]string{
		filepath.Join(root, "src", "app.go"),
		filepath.Join(root, ".gitignore"),
		filepath.Join(root, "docs", "guide.md"),
	})

	if _, ok := m.ignoreCache.Load(root); ok {
		t.Fatal("expected eviction because the batch contained a .gitignore change")
	}
}

// waitBackgroundSettled reports whether every manager-tracked background
// goroutine finished within timeout.
func waitBackgroundSettled(m *Manager, timeout time.Duration) bool {
	return m.bg.closeAndWait(timeout)
}

func TestStartIgnoreBuild_CachesWorkingResolver(t *testing.T) {
	m, _, _ := testManager(t)
	root := testWorkspacePath(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	m.startIgnoreBuild(root)

	if !waitBackgroundSettled(m, 5*time.Second) {
		t.Fatal("background ignore resolver build did not finish in time")
	}
	v, ok := m.ignoreCache.Load(root)
	if !ok {
		t.Fatal("expected resolver cached after the background build")
	}
	resolver, ok := v.(*ignore.Resolver)
	if !ok || resolver == nil {
		t.Fatalf("cache must hold a non-nil *ignore.Resolver, got %T", v)
	}
	// The build must have replaced the "building" sentinel with a working
	// resolver: a path matching the rule is reported ignored.
	if !resolver.Ignored(filepath.Join(root, "x.log"), false) {
		t.Fatal("cached resolver does not honour the root's .gitignore rules")
	}
}

func TestStartIgnoreBuild_ShutdownCancelAbortsBuildAndCleansSentinel(t *testing.T) {
	m, _, _ := testManager(t)
	root := testWorkspacePath(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	// Simulate Shutdown having already run its first step (shutdownCancel):
	// the walk derives from the manager's shutdown context, so it must abort
	// at its first entry and let the tracker drain immediately. Regression
	// guard for the frozen-quit bug: on a huge work-directory root (a Go
	// module cache — hundreds of thousands of entries, minutes to walk) the
	// un-cancellable walk made stopBackground burn the whole stopTimeout on
	// the main thread every time the app quit mid-walk.
	m.shutdownCancel()
	m.startIgnoreBuild(root)

	if !waitBackgroundSettled(m, 2*time.Second) {
		t.Fatal("aborted ignore build must let the background tracker drain immediately")
	}
	if _, ok := m.ignoreCache.Load(root); ok {
		t.Fatal("aborted build must remove its sentinel so a later call can retry")
	}
}

// TestStartIgnoreBuild_MidWalkShutdownJoinIsPrompt pins the frozen-quit fix
// deterministically: while the ignore walk is IN FLIGHT, Shutdown cancels the
// manager's context and the background tracker must drain promptly — the walk
// derives from shutdownCtx, so cancel-then-join must never burn the whole
// stopTimeout on the quiescing main thread (the multi-second quit freeze
// reported on a huge work-directory root).
//
// A real walk is timing-dependent — it may finish before the cancel lands or
// not have started yet — so the test substitutes the resolver builder with an
// entered/blocked/release barrier (m.ignoreResolverBuild): the goroutine
// signals it is live, blocks until the manager's shutdown context is
// cancelled, then returns that context's error. This exercises the exact
// cancellation contract (the build receives the manager's shutdown context and
// the join drains on cancel) with no wall-clock sleep.
func TestStartIgnoreBuild_MidWalkShutdownJoinIsPrompt(t *testing.T) {
	m, _, _ := testManager(t)
	root := runtimeTempDir(t)

	entered := make(chan struct{})
	m.ignoreResolverBuild = func(ctx context.Context, gotRoot string) (*ignore.Resolver, error) {
		if gotRoot != root {
			t.Errorf("resolver build root = %q, want %q", gotRoot, root)
		}
		close(entered)
		// Stay in flight until shutdown cancels the manager's context — the
		// mid-walk state whose join must be prompt.
		<-ctx.Done()
		return nil, ctx.Err()
	}

	m.startIgnoreBuild(root)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the ignore build never entered the walk")
	}

	start := time.Now()
	// Cancel exactly as Shutdown's first step (shutdownCancel) does.
	m.shutdownCancel()
	if !waitBackgroundSettled(m, 2*time.Second) {
		t.Fatal("mid-walk cancellation must let the background tracker drain promptly")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("tracker drained in %v after mid-walk cancel; want ~instantaneous", elapsed)
	}
	// The aborted build must remove its sentinel so a later call can retry.
	if _, ok := m.ignoreCache.Load(root); ok {
		t.Fatal("aborted build must remove its sentinel so a later call can retry")
	}
}
