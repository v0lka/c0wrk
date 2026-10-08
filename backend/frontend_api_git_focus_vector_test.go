package backend

// Git-panel focus → vector-registry focus binding (the documented "USER
// routing resolves the Git-panel focus root" contract): SetGitPanelFocus
// moves the visible index identity to the focused tree and — on reset —
// back to the project checkout, fail-soft when the vector subsystem cannot
// serve the focus. Tests run against the vectorWorktreeHarness (real git
// worktrees + real per-root managers with fake embeddings); async index
// init is awaited with bounded watchdogs and channel receives, never sleeps.

import (
	"testing"
	"time"
)

// TestSetGitPanelFocus_VectorStatusFollowsFocus pins the focus-binding AC:
// focusing a worktree through the git RPC makes GetVectorIndexStatus report
// THAT tree's branch, and the empty-path reset lands the visible identity
// back on the project checkout (the default target — never a LeaveFocus
// blank).
func TestSetGitPanelFocus_VectorStatusFollowsFocus(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	treeB := h.addManagedWorktree(t, "s-gpf-b", "gpf-beta")

	// Focus tree B via the git RPC: the status RPC must report B's branch.
	// SwitchProject returns before initProject detects the branch, so
	// WaitReady is the lifecycle signal that the branch identity settled.
	if err := h.api.SetGitPanelFocus(treeB); err != nil {
		t.Fatalf("SetGitPanelFocus(treeB): %v", err)
	}
	mgrB, err := h.roots.FocusManager()
	if err != nil {
		t.Fatalf("focus manager after focusing tree B: %v", err)
	}
	waitForVectorBranch(t, mgrB, "gpf-beta")
	if got := h.api.GetVectorIndexStatus().Branch; got != "gpf-beta" {
		t.Errorf("status branch after SetGitPanelFocus(treeB) = %q, want gpf-beta", got)
	}

	// Reset (empty path): the checkout — the default focus target — becomes
	// the visible index identity again.
	if err := h.api.SetGitPanelFocus(""); err != nil {
		t.Fatalf("SetGitPanelFocus reset: %v", err)
	}
	mgrCheckout, err := h.roots.FocusManager()
	if err != nil {
		t.Fatalf("focus manager after reset: %v", err)
	}
	waitForVectorBranch(t, mgrCheckout, "main")
	if got := h.api.GetVectorIndexStatus().Branch; got != "main" {
		t.Errorf("status branch after reset = %q, want main (the checkout)", got)
	}
}

// TestSetGitPanelFocus_StatusStreamCarriesNewTreeProgress pins the stream
// AC: focusing a tree that has no manager yet re-syncs vector_index:status
// synchronously (ApplyFocus emits the new focus's snapshot before the RPC
// returns), and the fresh manager's indexing progress for THAT tree then
// flows on the focused stream (indexing → ready with the tree's file
// totals).
func TestSetGitPanelFocus_StatusStreamCarriesNewTreeProgress(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	treeB := h.addManagedWorktree(t, "s-gpf-stream", "gpf-stream")

	// Capture the vector_index:status stream the way the frontend sees it.
	// The producer only sends on the channel; the slice is touched by the
	// test goroutine alone.
	stream := make(chan VectorIndexStatus, 64)
	var seen []VectorIndexStatus
	h.api.emitEvent = func(event string, data ...any) {
		if event != EventVectorIndexStatus || len(data) == 0 {
			return
		}
		st, ok := data[0].(VectorIndexStatus)
		if !ok {
			return
		}
		select {
		case stream <- st:
		default:
		}
	}

	if err := h.api.SetGitPanelFocus(treeB); err != nil {
		t.Fatalf("SetGitPanelFocus(treeB): %v", err)
	}

	// ApplyFocus re-syncs the stream synchronously: the RPC cannot return
	// without having emitted at least one status for the new focus. Bounded
	// watchdog — detects the hang, never asserts elapsed time.
	select {
	case st := <-stream:
		seen = append(seen, st)
	case <-time.After(10 * time.Second):
		t.Fatal("no vector_index:status emitted by SetGitPanelFocus(treeB)")
	}

	// The new tree's own indexing progress flows on the focused stream: the
	// fresh manager's pass reports ready with the tree's file totals.
	deadline := time.After(20 * time.Second)
	for {
		select {
		case st := <-stream:
			seen = append(seen, st)
			if st.State == "ready" && st.TotalFiles > 0 {
				return
			}
		case <-deadline:
			t.Fatalf("status stream never carried tree B's indexing progress; captured: %+v", seen)
		}
	}
}

// TestSetGitPanelFocus_FailSoftWhenVectorUnavailable pins the fail-soft AC:
// with the registry's manager factory still unwired (the startup race —
// the background ONNX init has not settled), SetGitPanelFocus still
// succeeds and the git focus still lands, for both the focus and the reset
// path. The vector miss is a Warn on the log — intercepted here with
// severity/payload assertions — never a git-RPC failure.
func TestSetGitPanelFocus_FailSoftWhenVectorUnavailable(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	tree := h.addManagedWorktree(t, "s-gpf-soft", "gpf-soft")
	checkout := h.proj.WorkspacePath

	// Swap in a registry whose factory is still unwired. Restore before the
	// diagnostics cleanup runs (LIFO) so the assert sees only this test's
	// records, and before the harness cleanup so ShutdownAll sees the live
	// registry.
	live := h.roots
	h.api.vectorRoots = newVectorRoots(h.api)
	t.Cleanup(func() { h.api.vectorRoots = live })

	// Each focus move logs exactly the expected pair — the registry's
	// internal focus-manager miss plus the RPC's fail-soft Warn — at Warn
	// with the root and the still-loading error, and nothing else.
	notReady := errVectorStillLoading.Error()
	captureMiscDiagnostics(t, h.api,
		miscExpectedDiagnostic{message: "vector roots: focus manager unavailable", attrs: map[string]string{"root": canonicalRoot(tree), "error": notReady}},
		miscExpectedDiagnostic{message: "vector focus unavailable for git panel target", attrs: map[string]string{"root": canonicalRoot(tree), "error": notReady}},
		miscExpectedDiagnostic{message: "vector roots: focus manager unavailable", attrs: map[string]string{"root": canonicalRoot(checkout), "error": notReady}},
		miscExpectedDiagnostic{message: "vector focus unavailable for git panel target", attrs: map[string]string{"root": canonicalRoot(checkout), "error": notReady}},
	)

	if err := h.api.SetGitPanelFocus(tree); err != nil {
		t.Fatalf("SetGitPanelFocus with unwired vector factory: %v", err)
	}
	root, err := h.api.resolveGitRepoRoot()
	if err != nil {
		t.Fatalf("resolveGitRepoRoot: %v", err)
	}
	if root != tree {
		t.Fatalf("focus root = %q, want %q (a vector failure must not disturb the git focus)", root, tree)
	}

	if err := h.api.SetGitPanelFocus(""); err != nil {
		t.Fatalf("SetGitPanelFocus reset with unwired vector factory: %v", err)
	}
	if root, err = h.api.resolveGitRepoRoot(); err != nil || root != checkout {
		t.Fatalf("focus after reset = %q (err=%v), want the checkout %q", root, err, checkout)
	}
}
