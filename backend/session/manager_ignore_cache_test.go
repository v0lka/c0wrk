package session

import (
	"fmt"
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

// TestStartIgnoreBuild_MidWalkShutdownJoinIsPrompt pins the real-world shape
// of the frozen-quit bug: the walk is IN FLIGHT (not pre-cancelled) when
// Shutdown cancels the manager's context. On a root with hundreds of
// thousands of entries the walk runs for minutes, so a straggler walk would
// make stopBackground burn the whole stopTimeout on the main thread — the
// exact multi-second freeze reported on quit. The ctx check fires per entry,
// so cancel-then-join must drain within milliseconds regardless of tree size.
func TestStartIgnoreBuild_MidWalkShutdownJoinIsPrompt(t *testing.T) {
	m, _, _ := testManager(t)
	root := runtimeTempDir(t)

	// A tree big enough that the walk is still mid-flight when the test
	// cancels: 20 dirs x 500 files = 10k entries.
	for d := 0; d < 20; d++ {
		dir := filepath.Join(root, fmt.Sprintf("dir%02d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		for f := 0; f < 500; f++ {
			name := filepath.Join(dir, fmt.Sprintf("file%03d.txt", f))
			if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
	}

	m.startIgnoreBuild(root)
	// Let the walk get going: by the time cancel fires it must be somewhere
	// mid-tree, not already finished and not still before the first entry.
	time.Sleep(30 * time.Millisecond)

	m.shutdownCancel()
	start := time.Now()
	if !waitBackgroundSettled(m, 2*time.Second) {
		t.Fatal("mid-walk cancellation must let the background tracker drain promptly")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("tracker drained in %v after mid-walk cancel; want ~instantaneous", elapsed)
	}
}
