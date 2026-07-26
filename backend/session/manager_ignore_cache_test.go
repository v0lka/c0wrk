package session

import (
	"path/filepath"
	"testing"

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
	root := t.TempDir()
	storeFakeResolver(m, root)

	m.InvalidateIgnoreCache([]string{filepath.Join(root, ".gitignore")})

	if _, ok := m.ignoreCache.Load(root); ok {
		t.Fatal("expected cached resolver for root to be evicted after .gitignore change")
	}
}

func TestInvalidateIgnoreCache_NestedGitignoreEvicts(t *testing.T) {
	m, _, _ := testManager(t)
	root := t.TempDir()
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
	root := t.TempDir()
	storeFakeResolver(m, root)

	m.InvalidateIgnoreCache([]string{filepath.Join(root, ".aiignore")})

	if _, ok := m.ignoreCache.Load(root); ok {
		t.Fatal("expected cached resolver evicted after .aiignore change")
	}
}

func TestInvalidateIgnoreCache_NonIgnoreFileKeepsCache(t *testing.T) {
	m, _, _ := testManager(t)
	root := t.TempDir()
	storeFakeResolver(m, root)

	m.InvalidateIgnoreCache([]string{filepath.Join(root, "main.go"), filepath.Join(root, "README.md")})

	if _, ok := m.ignoreCache.Load(root); !ok {
		t.Fatal("cached resolver must survive non-ignore-file changes")
	}
}

func TestInvalidateIgnoreCache_OnlyAffectedRootEvicted(t *testing.T) {
	m, _, _ := testManager(t)
	rootA := t.TempDir()
	rootB := t.TempDir()
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
	root := t.TempDir()
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
	root := t.TempDir()
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
