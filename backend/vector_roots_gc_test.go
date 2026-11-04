package backend

// ADR-104 stage-5 backend acceptance: deletion aligns with the shared
// store's OWNERSHIP records instead of "delete the whole worktree store":
// a released-then-RESURRECTED tree keeps its vector data, a released tree's
// own delta and marker go away while other roots' data (and the corpus they
// still reach) survives, and the switch sweep over the project's shared
// store is reachability-driven (root markers + branch references), never
// branch-name-keyed for shared artifacts.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// wireCorpusFactory re-wires the harness manager factory with the shared
// corpus registry (the desktop wiring): managed-tree attaches build
// snapshot corpora + per-root overlays and write refs/ ownership records.
func wireCorpusFactory(h *vectorWorktreeHarness) {
	ready := make(chan struct{})
	close(ready)
	h.roots.SetFactory(func() (*vectorindex.Manager, error) {
		return vectorindex.NewManager(vectorindex.ManagerConfig{
			EmbeddingFunc: func(context.Context, string) ([]float32, error) {
				return make([]float32, 4), nil
			},
			ShutdownGrace: 2 * time.Minute,
			Corpus:        h.roots.CorpusRegistry(),
		})
	}, ready, nil)
}

// sharedStoreDir is the project's shared vector store (branches/, refs/, …).
func (h *vectorWorktreeHarness) sharedStoreDir() string {
	return config.ProjectVectorIndexPath(h.agentDir, h.proj.ID)
}

// storeEntryNames lists dir/file names under the shared store's sub with
// the given prefix.
func (h *vectorWorktreeHarness) storeEntryNames(t *testing.T, sub, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(h.sharedStoreDir(), sub))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if prefix == "" || filepathPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// waitAttached drives a root's manager to a ready, fully attached state and
// waits (bounded) until its overlay store landed in the shared store.
func (h *vectorWorktreeHarness) waitAttached(t *testing.T, root string) *vectorindex.Manager {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mgr, err := h.roots.ManagerForRoot(ctx, root)
	if err != nil {
		t.Fatalf("ManagerForRoot(%s): %v", root, err)
	}
	wctx, wcancel := context.WithTimeout(ctx, 60*time.Second)
	defer wcancel()
	if err := mgr.Service().WaitReady(wctx); err != nil {
		t.Fatalf("root %s never became ready: %v", root, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for len(h.storeEntryNames(t, "branches", "branch_root_")) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("root %s never landed its overlay in the shared store", root)
		}
		// Bounded watchdog retry over a real disk effect (not a scheduler
		// sleep): the receive parks this goroutine until the next tick.
		<-time.After(50 * time.Millisecond)
	}
	return mgr
}

// TestCleanupReleasedTreeVectorData_ResurrectionGuard pins the release-path
// guard: a released tree that a racing adoption RE-CREATED (the tree path
// exists again, its manager is live) keeps every vector artifact — the
// cleanup is deferred to that tree's own next release. Once the tree is
// really gone, the release removes exactly its OWN delta (overlay +
// ownership marker) and leaves the shared corpus (its branch reference
// still reaches it).
func TestCleanupReleasedTreeVectorData_ResurrectionGuard(t *testing.T) {
	h := newVectorWorktreeHarness(t)
	wireCorpusFactory(h)
	wt := h.addManagedWorktree(t, "s-res", "wt-res")

	h.waitAttached(t, wt)
	if got := len(h.storeEntryNames(t, "branches", "branch_snap_")); got != 1 {
		t.Fatalf("expected one snapshot corpus after the attach, got %d", got)
	}
	overlaysBefore := h.storeEntryNames(t, "branches", "branch_root_")
	if len(overlaysBefore) != 1 {
		t.Fatalf("expected one root overlay after the attach, got %v", overlaysBefore)
	}
	if got := len(h.storeEntryNames(t, "refs", "root_")); got != 1 {
		t.Fatalf("expected one root ownership marker, got %d", got)
	}

	// The resurrection: the tree still exists (a racing adoption recreated
	// it) — nothing is released, nothing is removed.
	h.api.cleanupReleasedTreeVectorData(h.proj.WorkspacePath, h.proj.ID, "s-res", wt)
	if got := len(h.storeEntryNames(t, "branches", "branch_root_")); got != 1 {
		t.Errorf("resurrected tree's overlay removed: %v", h.storeEntryNames(t, "branches", "branch_root_"))
	}
	if got := len(h.storeEntryNames(t, "refs", "root_")); got != 1 {
		t.Errorf("resurrected tree's ownership marker removed")
	}
	if h.roots.LiveManager(wt) == nil {
		t.Error("resurrected tree's manager released by the cleanup")
	}

	// The tree is really gone: its OWN delta goes, the corpus stays (the
	// branch's reference reaches it — no root does anymore).
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("removing the tree: %v", err)
	}
	h.api.cleanupReleasedTreeVectorData(h.proj.WorkspacePath, h.proj.ID, "s-res", wt)
	if got := len(h.storeEntryNames(t, "branches", "branch_root_")); got != 0 {
		t.Errorf("released tree's overlay still present: %v", h.storeEntryNames(t, "branches", "branch_root_"))
	}
	if got := len(h.storeEntryNames(t, "refs", "root_")); got != 0 {
		t.Errorf("released tree's ownership marker still present: %d", got)
	}
	if got := len(h.storeEntryNames(t, "branches", "branch_snap_")); got != 1 {
		t.Errorf("shared corpus removed with the tree (the branch reference still reaches it): %d left", got)
	}
}

// TestSharedStoreSweep_KeepsLiveRootsData pins the switch sweep against the
// shared store: after one tree of two is released, the sweep (with every
// branch surviving) keeps the live root's overlay, marker and the shared
// corpus — deletion of a session/root never removes another root's data.
func TestSharedStoreSweep_KeepsLiveRootsData(t *testing.T) {
	h := newVectorWorktreeHarness(t)
	wireCorpusFactory(h)
	wtA := h.addManagedWorktree(t, "tree-ga", "feat-ga")
	wtB := h.addManagedWorktree(t, "tree-gb", "feat-gb") // same commit as feat-a

	h.waitAttached(t, wtA)
	h.waitAttached(t, wtB)

	// Release tree B the way the session flow does (tree removed, then the
	// cleanup): B's overlay and marker go; the corpus is shared with A.
	if err := os.RemoveAll(wtB); err != nil {
		t.Fatalf("removing tree B: %v", err)
	}
	h.api.cleanupReleasedTreeVectorData(h.proj.WorkspacePath, h.proj.ID, "tree-gb", wtB)

	mgr, disposable := h.roots.ManagerForCleanup()
	if mgr == nil {
		t.Fatal("no cleanup manager for the sweep")
	}
	if disposable {
		defer mgr.Shutdown()
	}
	removed := mgr.Service().PruneMissingBranchData(h.sharedStoreDir(), []string{"main", "feat-ga", "feat-gb"})
	if removed != 0 {
		t.Fatalf("sweep removed %d artifacts while every owner survives", removed)
	}

	if got := len(h.storeEntryNames(t, "branches", "branch_root_")); got != 1 {
		t.Errorf("live root A's overlay not exactly one after the sweep: %d", got)
	}
	if got := len(h.storeEntryNames(t, "refs", "root_")); got != 1 {
		t.Errorf("live root A's ownership marker not exactly one after the sweep: %d", got)
	}
	if got := len(h.storeEntryNames(t, "branches", "branch_snap_")); got != 1 {
		t.Errorf("shared corpus not exactly one after the sweep: %d", got)
	}
}
