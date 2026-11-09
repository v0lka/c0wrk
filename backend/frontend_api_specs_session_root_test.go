package backend

// Session-root coherence for the Specs panel (the "specs root follows the
// active session" protocol, mirroring the research/papers one — ADR-099):
// the agent writes specs inside its session tree, and a managed worktree
// lives OUTSIDE the checkout, so GetSpecsStatus must parse the ACTIVE
// SESSION's specs/ tree and read the ADR add dates from that SAME tree's git
// context (a worktree's HEAD differs from the checkout's), falling back to
// the project checkout when no session of the project resolves. Reuses the
// wtHarness (real git repository + real FrontendAPI/manager) from
// frontend_api_worktrees_test.go.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
)

// TestGetSpecsStatus_FollowsActiveSession pins the session-following
// contract end to end: before any session exists the checkout fallback holds
// (neutral present=false — the checkout carries no specs/ tree), a
// managed-worktree session flips the root to ITS OWN tree, the ADR add dates
// come from the worktree's git context (the worktree-only commit is
// invisible to the checkout's HEAD, so a checkout-rooted enrichment would
// leave every ADR undated), and a newer local session flips the root back to
// the checkout.
func TestGetSpecsStatus_FollowsActiveSession(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	checkoutRoot := filepath.Join(h.project.WorkspacePath, "specs")

	// No sessions yet: the checkout fallback.
	status, err := h.api.GetSpecsStatus(h.project.ID)
	if err != nil {
		t.Fatalf("GetSpecsStatus (no session): %v", err)
	}
	if status.Present {
		t.Error("Present = true, want false for the checkout fallback without a specs/ tree")
	}
	if status.Root != checkoutRoot {
		t.Errorf("Root = %q, want the checkout %q", status.Root, checkoutRoot)
	}

	// A managed session of the project: its tree carries the specs tree,
	// committed there — invisible to the checkout's HEAD.
	managed := h.api.mustManagedSession(t, "", true, "")
	tree := managed.WorkspaceBinding.WorkspacePath
	writeSpecsTree(t, tree)
	commitPathAtDate(t, tree, "specs", specsADRFixtureDate)
	sessionRoot := filepath.Join(tree, "specs")

	// Precondition: the worktree-only commit is NOT visible from the
	// checkout, so the dated ADRs below prove the enrichment ran against the
	// worktree's git context.
	probeCtx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	dates := specsADRDates(probeCtx, nil, h.project.WorkspacePath)
	cancel()
	if len(dates) != 0 {
		t.Fatalf("precondition broken: the checkout's git log sees %v", dates)
	}

	status, err = h.api.GetSpecsStatus(h.project.ID)
	if err != nil {
		t.Fatalf("GetSpecsStatus (managed session): %v", err)
	}
	if !status.Present {
		t.Fatalf("Present = false, want true for the session tree's specs/ root")
	}
	if status.Root != sessionRoot {
		t.Fatalf("Root = %q, want the session tree's %q", status.Root, sessionRoot)
	}
	for slug, date := range adrDateBySlug(status) {
		if date != "2025-06-01" {
			t.Errorf("ADR %q date = %q, want 2025-06-01 from the worktree's git context", slug, date)
		}
	}
	assertSpecsWireShape(t, status)

	// A newer LOCAL session (the checkout workspace) flips the root back.
	// The activity timestamp is stamped an hour ahead explicitly: wall-clock
	// creation times share second-level granularity, and a tie would make
	// the outcome depend on the store's tie-break order.
	local, err := h.manager.CreateSession(h.project.ID, h.project.WorkspacePath)
	if err != nil {
		t.Fatalf("CreateSession (local): %v", err)
	}
	local.LastActiveAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	local.CreatedAt = local.LastActiveAt
	if err := h.store.SaveSession(context.Background(), *local); err != nil {
		t.Fatalf("SaveSession (local): %v", err)
	}
	status, err = h.api.GetSpecsStatus(h.project.ID)
	if err != nil {
		t.Fatalf("GetSpecsStatus (local session): %v", err)
	}
	if status.Present {
		t.Error("Present = true, want false after the root flipped back to the checkout")
	}
	if status.Root != checkoutRoot {
		t.Errorf("Root = %q, want the checkout %q after the local session took over", status.Root, checkoutRoot)
	}
}

// TestGetSpecsStatus_ForeignSessionDoesNotSteer: a session of a DIFFERENT
// project must never steer the specs root — the resolver lists the requesting
// project's sessions only, so the checkout fallback holds (the checkout's own
// tree parses present=true; a steered root would resolve into the foreign
// workspace and come back neutral).
func TestGetSpecsStatus_ForeignSessionDoesNotSteer(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	checkoutRoot := filepath.Join(h.project.WorkspacePath, "specs")
	writeSpecsTree(t, h.project.WorkspacePath)

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

	status, err := h.api.GetSpecsStatus(h.project.ID)
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if !status.Present {
		t.Fatalf("Present = false — a foreign session steered the root away from the checkout")
	}
	if status.Root != checkoutRoot {
		t.Errorf("Root = %q, want the checkout %q", status.Root, checkoutRoot)
	}
}

// TestGetSpecsStatus_NoProjectNeutralWithLiveManager pins the No-Project
// contract against the LIVE stack: even with a real session manager holding
// sessions of real projects, the pseudo-project yields the neutral
// present=false state without an error — session resolution is never
// consulted for No Project.
func TestGetSpecsStatus_NoProjectNeutralWithLiveManager(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	// A real managed session exists, so the manager is live and resolvable.
	h.api.mustManagedSession(t, "", true, "")
	if err := h.api.projStore.SaveProject(context.Background(), project.ProjectInfo{
		ID:   project.NoProjectID,
		Name: "No Project",
	}); err != nil {
		t.Fatalf("SaveProject (No Project row): %v", err)
	}

	status, err := h.api.GetSpecsStatus(project.NoProjectID)
	if err != nil {
		t.Fatalf("GetSpecsStatus (No Project): %v", err)
	}
	if status.Present {
		t.Error("Present = true, want false for No Project")
	}
	if status.Root != "" {
		t.Errorf("Root = %q, want \"\" for No Project", status.Root)
	}
	if status.Files == nil || len(status.Files) != 0 {
		t.Errorf("Files = %v, want a non-nil empty slice", status.Files)
	}
	if status.ADRs == nil || len(status.ADRs) != 0 {
		t.Errorf("ADRs = %v, want a non-nil empty slice", status.ADRs)
	}
	if status.Counts.FindingsByKind == nil || len(status.Counts.FindingsByKind) != 0 {
		t.Errorf("Counts.FindingsByKind = %v, want a non-nil empty map", status.Counts.FindingsByKind)
	}
	assertSpecsWireShape(t, status)
}
