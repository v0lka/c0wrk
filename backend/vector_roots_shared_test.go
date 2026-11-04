package backend

// ADR-104 stage-2 backend acceptance: the per-root registry routes managed
// worktrees onto the PROJECT's shared store (no per-tree storage, no cache
// copy), and a focus switch never cancels another root's in-flight indexing
// — the roots are independent managers; the shared corpus build is
// single-flighted across them (one writer per physical store).

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// TestVectorRoots_WorktreeSharesProjectStore pins the provisioning seam: a
// managed worktree resolves to the project's storage root and the project's
// embedding cache — the shared corpus + per-root overlay replace the
// per-tree copies.
func TestVectorRoots_WorktreeSharesProjectStore(t *testing.T) {
	h := newVectorWorktreeHarness(t)
	wt := h.addManagedWorktree(t, "tree-a", "feat-a")

	ctx := context.Background()
	projectID, storage, cache, err := h.api.resolveRootPlan(ctx, wt)
	if err != nil {
		t.Fatalf("resolveRootPlan(worktree): %v", err)
	}
	if projectID != h.proj.ID {
		t.Errorf("worktree routed to project %q; want %q", projectID, h.proj.ID)
	}
	wantStorage := config.ProjectVectorIndexPath(h.agentDir, h.proj.ID)
	if storage != wantStorage {
		t.Errorf("worktree storage = %q; want the project store %q", storage, wantStorage)
	}
	wantCache := config.ProjectEmbeddingCachePath(h.agentDir, h.proj.ID)
	if cache != wantCache {
		t.Errorf("worktree cache = %q; want the project cache %q (shared, no per-tree copy)", cache, wantCache)
	}
	if legacy, _ := config.WorktreeVectorIndexPath(h.agentDir, h.proj.ID, "tree-a"); storage == legacy {
		t.Errorf("worktree storage resolved to the legacy per-tree root %q", legacy)
	}

	// The user-facing focus target resolves the same way.
	target, ok := h.api.managedWorktreeVectorTarget(h.proj, wt)
	if !ok {
		t.Fatal("managedWorktreeVectorTarget rejected a managed worktree")
	}
	if target.storagePath != wantStorage {
		t.Errorf("focus target storage = %q; want %q", target.storagePath, wantStorage)
	}
}

// filepathPrefix reports whether s starts with prefix.
func filepathPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestVectorRoots_FocusSwitchDoesNotCancelOtherRootsIndexing pins criterion
// 4 together with the cross-manager single-flight: while root B's corpus
// build is in flight (its embedder gated), a focus switch to root A attaches
// A onto the SAME unpublished corpus — A waits on the build lease instead of
// opening a parallel writer — and after the gate opens BOTH roots are ready
// and searchable; B's pass was never cancelled by the focus move.
func TestVectorRoots_FocusSwitchDoesNotCancelOtherRootsIndexing(t *testing.T) {
	h := newVectorWorktreeHarness(t)
	wtA := h.addManagedWorktree(t, "tree-a", "feat-a")
	wtB := h.addManagedWorktree(t, "tree-b", "feat-b") // same commit as feat-a

	// Gate the shared embedder: the FIRST embedding call announces that a
	// corpus build is in flight and blocks until released. Every manager
	// build re-wires this factory (the live-config snapshot path).
	ready := make(chan struct{})
	close(ready)
	buildStarted := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.roots.SetFactory(func() (*vectorindex.Manager, error) {
		return vectorindex.NewManager(vectorindex.ManagerConfig{
			EmbeddingFunc: func(ctx context.Context, text string) ([]float32, error) {
				once.Do(func() { close(buildStarted) })
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return make([]float32, 4), nil
			},
			ShutdownGrace: 2 * time.Minute,
			Corpus:        h.roots.CorpusRegistry(),
		})
	}, ready, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Root B attaches agent-side; its init builds the (gated) corpus.
	mgrB, err := h.roots.ManagerForRoot(ctx, wtB)
	if err != nil {
		t.Fatalf("ManagerForRoot(B): %v", err)
	}
	select {
	case <-buildStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("watchdog: root B never entered its corpus build")
	}

	// Focus moves to A while B's build is in flight. A attaches the SAME
	// unpublished corpus and must WAIT for B's build lease — ApplyFocus
	// returns only after A's attach settles, which needs the gate open.
	focusDone := make(chan error, 1)
	go func() { focusDone <- h.roots.ApplyFocus(ctx, wtA) }()
	// No settle barrier before the release: A either parks on the lease or
	// finds the published corpus, and both assertions below (one corpus on
	// disk, both roots ready) hold in either interleaving — A can never
	// become a second writer (single-flight lease).
	close(release)

	select {
	case err := <-focusDone:
		if err != nil {
			t.Fatalf("ApplyFocus(A) mid-build of B: %v", err)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("watchdog: ApplyFocus(A) never settled after the gate opened")
	}

	// Both roots are ready — B's indexing was not cancelled by the focus
	// switch, and A's attach completed through the shared build.
	waitReady := func(m *vectorindex.Manager, name string) {
		t.Helper()
		wctx, wcancel := context.WithTimeout(ctx, 60*time.Second)
		defer wcancel()
		if err := m.Service().WaitReady(wctx); err != nil {
			t.Fatalf("watchdog: root %s never became ready: %v", name, err)
		}
	}
	waitReady(mgrB, "B")
	mgrA, err := h.roots.ManagerForRoot(ctx, wtA)
	if err != nil {
		t.Fatalf("ManagerForRoot(A): %v", err)
	}
	waitReady(mgrA, "A")

	// Exactly ONE snapshot corpus exists on disk: the two roots (different
	// branches, same commit) shared a single build — no per-root copy.
	branches := filepath.Join(config.ProjectVectorIndexPath(h.agentDir, h.proj.ID), "branches")
	entries, err := os.ReadDir(branches)
	if err != nil {
		t.Fatalf("reading the shared store: %v", err)
	}
	corpora := 0
	for _, e := range entries {
		// Snapshot corpora are keyed branch_snap_<oid16>; per-root overlays
		// (branch_root_<fp>) are NOT corpora.
		if e.IsDir() && len(e.Name()) == len("branch_snap_")+16 && filepathPrefix(e.Name(), "branch_snap_") {
			corpora++
		}
	}
	if corpora != 1 {
		t.Errorf("shared store holds %d snapshot corpora; want 1 (branches of one commit share it)", corpora)
	}

	// The focus root is A; B's manager is untouched by the focus bookkeeping.
	if got := h.roots.FocusRoot(); got != wtA {
		t.Errorf("focus root = %q; want %q", got, wtA)
	}
}
