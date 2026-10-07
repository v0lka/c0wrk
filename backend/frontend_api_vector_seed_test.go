package backend

// Embedding-cache seeding for freshly provisioned managed worktrees
// (frontend_api_vector.go). A session tree is 99-100% identical to its branch
// point, and the content-addressed embedding cache is the layer where that
// identity is reusable: entries are keyed by chunk-text hash + model
// fingerprint with paths excluded, so copying the checkout's cache into the
// tree's cache root lets its first index pass hit the cache instead of
// re-running ONNX inference. These tests pin the file-level contract: faithful
// mirroring, idempotence, tolerance to irregular source entries, and the
// create/fork provisioning flows calling the seed after their success point.

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/session"
)

// seedLogger returns a FrontendAPI whose logger captures records in a buffer,
// plus a lookup helper for assertions on the seed's diagnostics. The seed's
// own Info/Warn lines are part of its contract (per AGENTS.md, expected
// diagnostics are asserted at their source, never silenced).
func seedLogger(t *testing.T) (*FrontendAPI, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	api := &FrontendAPI{
		agentDir: t.TempDir(),
		logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})),
	}
	return api, &buf
}

// writeCacheEntry materializes one fake cache entry in the two-level fanout
// shape the embedding cache uses (xx/yyyy…vec). The bytes are arbitrary: the
// copier is content-agnostic (checksum validation lives in core/vectorindex
// and is covered by its own tests).
func writeCacheEntry(t *testing.T, root, fanout, name, payload string) string {
	t.Helper()
	dir := filepath.Join(root, fanout)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// cacheFileSet returns the relative path → contents map of every regular file
// under root.
func cacheFileSet(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		files[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walking cache root %s: %v", root, err)
	}
	return files
}

func TestSeedWorktreeEmbeddingCache_CopiesEntriesAndSkipsWhenPresent(t *testing.T) {
	api, logs := seedLogger(t)

	src := config.ProjectEmbeddingCachePath(api.agentDir, "proj-1")
	writeCacheEntry(t, src, "ab", "abcd0123456789ef.vec", "vector-1")
	writeCacheEntry(t, src, "cd", "cdef0123456789ab.vec", "vector-2")

	worktree := "s-seed1"
	api.seedWorktreeEmbeddingCache("proj-1", worktree)

	dst, err := config.WorktreeEmbeddingCachePath(api.agentDir, "proj-1", worktree)
	if err != nil {
		t.Fatal(err)
	}
	got := cacheFileSet(t, dst)
	want := cacheFileSet(t, src)
	if len(got) != 2 {
		t.Fatalf("seeded cache has %d entries, want 2: %v", len(got), got)
	}
	for rel, payload := range want {
		if got[rel] != payload {
			t.Errorf("seeded entry %s = %q, want %q", rel, got[rel], payload)
		}
	}
	// Entry permissions must match the cache writer's (0o600 files): a
	// world-readable vector is not a vulnerability, but a mode drift would
	// silently diverge the seed from the cache's own put path. Windows
	// persists only the read-only bit, so ModePerm there reports 0666 for
	// a writable file no matter which perm O_CREATE requested — the copier
	// passes the source's own mode through unchanged, but that is only
	// observable on POSIX platforms.
	if runtime.GOOS != "windows" {
		for rel := range got {
			info, statErr := os.Stat(filepath.Join(dst, rel))
			if statErr != nil {
				t.Fatal(statErr)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("seeded entry %s mode = %v, want -rw-------", rel, info.Mode().Perm())
			}
		}
	}
	if !strings.Contains(logs.String(), "seeded worktree embedding cache from the project checkout") {
		t.Errorf("success seed must log its Info line; logs:\n%s", logs.String())
	}

	// Idempotence: an existing target directory is never touched, so entries
	// the tree's (hypothetically live) manager already wrote survive, and the
	// source's later additions do not leak into a seeded tree.
	marker := writeCacheEntry(t, src, "ef", "effe0123456789cd.vec", "added-after-seed")
	api.seedWorktreeEmbeddingCache("proj-1", worktree)
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatal(statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dst, "ef", "effe0123456789cd.vec")); !os.IsNotExist(statErr) {
		t.Fatal("re-seed must not touch an existing worktree cache root")
	}
}

func TestSeedWorktreeEmbeddingCache_NoSourceCache(t *testing.T) {
	api, logs := seedLogger(t)

	// No project cache on disk at all (the user never indexed the checkout):
	// the seed must be a silent no-op, leaving no target directory behind.
	api.seedWorktreeEmbeddingCache("proj-1", "s-nosrc")

	dst, err := config.WorktreeEmbeddingCachePath(api.agentDir, "proj-1", "s-nosrc")
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatal("no-source seed must not create the worktree cache root")
	}
	if strings.Contains(logs.String(), "seeded worktree embedding cache") {
		t.Errorf("no-source seed must stay silent at Info; logs:\n%s", logs.String())
	}
}

func TestSeedWorktreeEmbeddingCache_ToleratesIrregularSourceEntries(t *testing.T) {
	api, logs := seedLogger(t)

	src := config.ProjectEmbeddingCachePath(api.agentDir, "proj-1")
	writeCacheEntry(t, src, "ab", "abcd0123456789ef.vec", "vector-1")
	// An irregular entry inside a fanout dir (e.g. a nested directory that is
	// not part of the cache's shape): skipped, not fatal.
	stray := filepath.Join(src, "ab", "not-a-cache-entry")
	if err := os.MkdirAll(stray, 0o750); err != nil {
		t.Fatal(err)
	}

	api.seedWorktreeEmbeddingCache("proj-1", "s-tolerant")

	dst, err := config.WorktreeEmbeddingCachePath(api.agentDir, "proj-1", "s-tolerant")
	if err != nil {
		t.Fatal(err)
	}
	got := cacheFileSet(t, dst)
	// Keys are filepath.Rel results: fanout paths must be joined with the
	// platform's own separator, never written as a literal slash.
	entry := filepath.Join("ab", "abcd0123456789ef.vec")
	if len(got) != 1 || got[entry] != "vector-1" {
		t.Fatalf("seeded cache = %v, want exactly the one regular entry", got)
	}
	if strings.Contains(logs.String(), "seed incomplete") {
		t.Errorf("irregular-but-skippable source entries must not warn; logs:\n%s", logs.String())
	}
}

// Both provisioning flows must call the seed after their success point: the
// copied state is in place before the RPC returns, which is what guarantees
// the tree's vector manager (built on the frontend's immediate focus move)
// opens an already-warm cache root.

func TestCreateManagedSession_SeedsWorktreeEmbeddingCache(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	// The checkout has indexed before: its cache holds entries.
	src := config.ProjectEmbeddingCachePath(h.agentDir, h.project.ID)
	writeCacheEntry(t, src, "ab", "abcd0123456789ef.vec", "vector-1")
	writeCacheEntry(t, src, "cd", "cdef0123456789ab.vec", "vector-2")

	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	binding := info.WorkspaceBinding
	if binding == nil || binding.Kind != session.WorkspaceManagedWorktree {
		t.Fatalf("managed binding: %+v", binding)
	}

	dst, err := config.WorktreeEmbeddingCachePath(h.agentDir, h.project.ID, binding.WorktreeName)
	if err != nil {
		t.Fatal(err)
	}
	got := cacheFileSet(t, dst)
	want := cacheFileSet(t, src)
	if len(got) != len(want) {
		t.Fatalf("worktree cache has %d entries, want %d: %v", len(got), len(want), got)
	}
	for rel, payload := range want {
		if got[rel] != payload {
			t.Errorf("worktree cache entry %s = %q, want %q", rel, got[rel], payload)
		}
	}
}

func TestForkManagedSession_SeedsWorktreeEmbeddingCache(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	src := config.ProjectEmbeddingCachePath(h.agentDir, h.project.ID)
	writeCacheEntry(t, src, "ab", "abcd0123456789ef.vec", "vector-1")

	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	fork, err := h.api.ForkSession(info.ID)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	fb := fork.WorkspaceBinding
	if fb == nil {
		t.Fatal("fork binding missing")
	}
	if fb.WorktreeName == info.WorkspaceBinding.WorktreeName {
		t.Fatal("fork must own a distinct tree")
	}

	dst, err := config.WorktreeEmbeddingCachePath(h.agentDir, h.project.ID, fb.WorktreeName)
	if err != nil {
		t.Fatal(err)
	}
	got := cacheFileSet(t, dst)
	entry := filepath.Join("ab", "abcd0123456789ef.vec")
	if len(got) != 1 || got[entry] != "vector-1" {
		t.Fatalf("fork cache = %v, want the seeded checkout entry", got)
	}
}
