package backend

// Session-root coherence for the research/papers subsystems (the "research
// root follows the active session" protocol): the agent writes research
// artifacts under <session-workspace>/.research, so the panel RPCs, the
// papers library paths, and the watcher emit roots must resolve the SAME
// root — the active session's tree, falling back to the checkout when no
// session of the project resolves. Reuses the wtHarness (real git repository
// + real FrontendAPI/manager) from frontend_api_worktrees_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/workspace"
)

// TestEffectiveResearchRoot_FollowsActiveSession pins the resolver contract:
// a managed-worktree session of the project resolves the root to ITS OWN tree
// (the agent writes research there — the former checkout-only resolution made
// it invisible to the panel), a newer local session flips the root back to
// the checkout, and a project without sessions falls back to the checkout.
func TestEffectiveResearchRoot_FollowsActiveSession(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	checkoutRoot := config.ProjectResearchPath(h.project.WorkspacePath)

	// No sessions yet: the checkout fallback.
	if got := h.api.effectiveResearchRoot(h.project); got != checkoutRoot {
		t.Fatalf("no-session root = %q, want the checkout %q", got, checkoutRoot)
	}

	// A managed session of the project: the root is its own tree.
	managed := h.api.mustManagedSession(t, "", true, "")
	tree := managed.WorkspaceBinding.WorkspacePath
	wantTreeRoot := config.ProjectResearchPath(tree)
	if got := h.api.effectiveResearchRoot(h.project); got != wantTreeRoot {
		t.Fatalf("managed-session root = %q, want the session tree %q", got, wantTreeRoot)
	}
	if got := h.api.effectiveResearchRoot(h.project); got == checkoutRoot {
		t.Fatal("managed session must not inherit the checkout's research root")
	}

	// A newer LOCAL session (checkout workspace) flips the root back. The
	// activity timestamp is stamped an hour ahead explicitly: wall-clock
	// creation times share second-level granularity, and a tie would make the
	// "which session is latest" outcome depend on the store's tie-break order.
	local, err := h.manager.CreateSession(h.project.ID, h.project.WorkspacePath)
	if err != nil {
		t.Fatalf("CreateSession (local): %v", err)
	}
	local.LastActiveAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	local.CreatedAt = local.LastActiveAt
	if err := h.store.SaveSession(context.Background(), *local); err != nil {
		t.Fatalf("SaveSession (local): %v", err)
	}
	if got := h.api.effectiveResearchRoot(h.project); got != checkoutRoot {
		t.Fatalf("local-session root = %q, want the checkout %q", got, checkoutRoot)
	}
}

// TestEffectiveResearchRoot_ForeignSessionDoesNotSteer: a session of a
// DIFFERENT project must never steer the root — the resolver lists the
// requesting project's sessions only, so the checkout fallback holds.
func TestEffectiveResearchRoot_ForeignSessionDoesNotSteer(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	checkoutRoot := config.ProjectResearchPath(h.project.WorkspacePath)

	foreignDir := t.TempDir()
	foreign, err := h.api.projectManager.CreateProject("Foreign", foreignDir)
	if err != nil {
		t.Fatalf("CreateProject (foreign): %v", err)
	}
	created, err := h.manager.CreateSession(foreign.ID, foreignDir)
	if err != nil {
		t.Fatalf("CreateSession (foreign): %v", err)
	}
	if err := h.store.SaveSession(context.Background(), *created); err != nil {
		t.Fatalf("SaveSession (foreign): %v", err)
	}

	// Precondition: the foreign session exists and resolves for ITS project —
	// so the assertion below is discriminating, not vacuous.
	if ws, ok := h.manager.WorkspacePathFor(context.Background(), created.ID); !ok || ws == "" {
		t.Fatalf("precondition broken: foreign session workspace unresolved (ws=%q ok=%v)", ws, ok)
	}

	if got := h.api.effectiveResearchRoot(h.project); got != checkoutRoot {
		t.Fatalf("foreign session steered the root: got %q, want the checkout %q", got, checkoutRoot)
	}
}

// TestPapersRoots_FollowActiveSession pins the papers side of the protocol:
// both library roots and the GetPapers read context resolve inside the
// active session's tree — including its containment boundary (a managed
// tree lives OUTSIDE the checkout).
func TestPapersRoots_FollowActiveSession(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	managed := h.api.mustManagedSession(t, "", true, "")
	tree := managed.WorkspaceBinding.WorkspacePath
	sessionRoot := config.ProjectResearchPath(tree)

	if got := h.api.papersRootForProject(h.project); got != config.PaperLibraryPathIn(sessionRoot) {
		t.Fatalf("papers root = %q, want the session tree's library %q", got, config.PaperLibraryPathIn(sessionRoot))
	}
	if got := h.api.comparisonsRootForProject(h.project); got != config.ComparisonsPathIn(sessionRoot) {
		t.Fatalf("comparisons root = %q, want the session tree's comparisons %q", got, config.ComparisonsPathIn(sessionRoot))
	}

	dto, err := h.api.GetPapers(h.project.ID)
	if err != nil {
		t.Fatalf("GetPapers: %v", err)
	}
	if dto.ResearchRoot != sessionRoot {
		t.Fatalf("GetPapers research root = %q, want the session tree's root %q", dto.ResearchRoot, sessionRoot)
	}
	if dto.Root != config.PaperLibraryPathIn(sessionRoot) {
		t.Fatalf("GetPapers library root = %q, want %q", dto.Root, config.PaperLibraryPathIn(sessionRoot))
	}
}

// TestWatchDirectory_FollowsSessionResearchRoot pins the watcher half of the
// protocol: after the frontend re-roots the file tree at a managed session's
// workspace (the WatchDirectory call on every session switch), a change under
// <worktree>/.research emits research:file_changed and a paper-card edit
// emits papers:changed. Before the fix the watcher only watched the
// checkout's .research, so session-tree research was invisible to the panel's
// incremental update path.
//
// The watcher state is asserted after the synchronous WatchDirectory call;
// kernel-watch registration itself is asynchronous (OS sampling), covered by
// a bounded settle sleep before the writes and bounded waitForEmission
// deadlines — the same shape TestResearchFileChanged_NestedSubdir uses.
func TestWatchDirectory_FollowsSessionResearchRoot(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	managed := h.api.mustManagedSession(t, "", true, "")
	tree := managed.WorkspaceBinding.WorkspacePath
	sessionRoot := filepath.Join(tree, ".research")

	// Seed the artifact tree BEFORE watching so the edits below are modifies.
	hypDir := filepath.Join(sessionRoot, "R-001-test", "hypotheses")
	if err := os.MkdirAll(hypDir, 0o755); err != nil {
		t.Fatalf("MkdirAll hypotheses: %v", err)
	}
	hypFile := filepath.Join(hypDir, "H-001.md")
	if err := os.WriteFile(hypFile, []byte("# H-001: old\n"), 0o644); err != nil {
		t.Fatalf("seed H-001: %v", err)
	}
	paperDir := filepath.Join(sessionRoot, "papers", "test-paper")
	if err := os.MkdirAll(paperDir, 0o755); err != nil {
		t.Fatalf("MkdirAll paper dir: %v", err)
	}
	paperFile := filepath.Join(paperDir, "paper.md")
	if err := os.WriteFile(paperFile, []byte("# Test paper\n"), 0o644); err != nil {
		t.Fatalf("seed paper: %v", err)
	}

	var researchEvents, papersEvents atomic.Int32
	f := h.api
	f.emitEvent = func(name string, _ ...any) {
		switch name {
		case EventResearchFileChanged:
			researchEvents.Add(1)
		case EventPapersChanged:
			papersEvents.Add(1)
		}
	}

	// The watcher starts checkout-rooted with the checkout roots tracked —
	// exactly the state a project switch leaves behind.
	checkoutRoot := config.ProjectResearchPath(h.project.WorkspacePath)
	f.activeProjectMu.Lock()
	f.activeResearchRoot = checkoutRoot
	f.activePapersRoot = config.PaperLibraryPathIn(checkoutRoot)
	f.activeComparisonsRoot = config.ComparisonsPathIn(checkoutRoot)
	f.activeProjectMu.Unlock()

	// Replicate the CODE-mode callback from switchProjectSetupWatcher so the
	// test exercises the real emit path.
	watcher, err := workspace.NewWatcher(h.project.WorkspacePath, func(changedPaths []string) {
		f.activeProjectMu.RLock()
		snapProjectID := f.activeProjectID
		snapResearchRoot := f.activeResearchRoot
		snapPapersRoot := f.activePapersRoot
		snapComparisonsRoot := f.activeComparisonsRoot
		f.activeProjectMu.RUnlock()
		f.emitResearchFileChanged(snapResearchRoot, snapProjectID, changedPaths)
		f.emitPapersChanged(snapPapersRoot, snapComparisonsRoot, snapProjectID, changedPaths)
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	f.watcherMu.Lock()
	f.watcher = watcher
	f.watcherMu.Unlock()
	// WatchDirectory re-scopes (replaces) f.watcher below, so the cleanup
	// must close whatever watcher is CURRENT — a stale local would leak the
	// re-scoped replacement.
	t.Cleanup(func() {
		f.watcherMu.Lock()
		defer f.watcherMu.Unlock()
		if f.watcher != nil {
			_ = f.watcher.Close()
		}
	})

	// THE SESSION SWITCH: the frontend calls WatchDirectory with the active
	// session's workspace on every session switch. The managed tree lives
	// OUTSIDE the checkout the watcher is rooted at, so the workspace and its
	// research root must be admitted before they can be watched.
	if err := f.WatchDirectory(tree); err != nil {
		t.Fatalf("WatchDirectory(session tree): %v", err)
	}
	f.activeProjectMu.RLock()
	tracked := f.activeResearchRoot
	trackedPapers := f.activePapersRoot
	f.activeProjectMu.RUnlock()
	if tracked != sessionRoot {
		t.Fatalf("tracked research root = %q, want the session tree's %q", tracked, sessionRoot)
	}
	if trackedPapers != config.PaperLibraryPathIn(sessionRoot) {
		t.Fatalf("tracked papers root = %q, want %q", trackedPapers, config.PaperLibraryPathIn(sessionRoot))
	}

	// The writes retry until the watcher reports them (writeUntilEmitted),
	// so watch-registration latency cannot race the first event.
	writeUntilEmitted(t, hypFile, []byte("# H-001: updated\n"), &researchEvents, 1)

	writeUntilEmitted(t, paperFile, []byte("# Test paper\n\nupdated\n"), &papersEvents, 1)
}

// TestFollowSessionResearchRoots_IgnoresForeignPath: a renderer-supplied path
// that is neither inside the project workspace nor one of its managed
// worktrees is REJECTED by WatchDirectory — it can be neither this project's
// session workspace nor anything this watcher should touch — and the rejection
// leaves both the watcher root and the tracked emit roots untouched.
func TestFollowSessionResearchRoots_IgnoresForeignPath(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	managed := h.api.mustManagedSession(t, "", true, "")
	tree := managed.WorkspaceBinding.WorkspacePath
	sessionRoot := filepath.Join(tree, ".research")

	f := h.api
	f.emitEvent = func(string, ...any) {}
	// Production switch state: the CODE watcher follows the active session,
	// and the tracked emit roots point at the session-following research
	// root. (switchProjectActivate itself is not harness-safe — it pushes
	// the MCP workdir into a builder this harness does not wire — so the
	// tracked roots are stamped the way TestWatchDirectory_FollowsSession
	// ResearchRoot stamps them.)
	f.switchProjectSetupWatcher(context.Background(), h.project)
	f.activeProjectMu.Lock()
	f.activeResearchRoot = config.ProjectResearchPath(tree)
	f.activeProjectMu.Unlock()
	f.watcherMu.Lock()
	if f.watcher == nil {
		f.watcherMu.Unlock()
		t.Fatal("switchProjectSetupWatcher did not create a watcher")
	}
	if got := f.watcher.Root(); got != tree {
		f.watcherMu.Unlock()
		t.Fatalf("precondition broken: watcher root = %q, want the session tree %q", got, tree)
	}
	f.watcherMu.Unlock()

	if err := f.WatchDirectory(filepath.Join(t.TempDir(), "unrelated", "ws")); err == nil {
		t.Fatal("WatchDirectory accepted a foreign path; want rejection")
	}

	f.watcherMu.Lock()
	root := "<closed>"
	if f.watcher != nil {
		root = f.watcher.Root()
	}
	f.watcherMu.Unlock()
	if root != tree {
		t.Fatalf("rejected path moved the watcher root to %q, want %q unchanged", root, tree)
	}
	f.activeProjectMu.RLock()
	tracked := f.activeResearchRoot
	f.activeProjectMu.RUnlock()
	if tracked != sessionRoot {
		t.Fatalf("foreign path moved the tracked root to %q, want %q unchanged", tracked, sessionRoot)
	}
	f.watcherMu.Lock()
	if f.watcher != nil {
		_ = f.watcher.Close()
		f.watcher = nil
	}
	f.watcherMu.Unlock()
}
