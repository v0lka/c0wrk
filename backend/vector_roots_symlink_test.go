package backend

// Per-root vector routing across path spellings: a project opened through a
// symlinked directory (a moved-behind-an-alias checkout, or a legacy row that
// kept the aliased spelling) must resolve to its registered project instead
// of failing with the "Index unavailable" surface, duplicate project rows
// claiming one path must pick one project deterministically, and a genuinely
// foreign path must still be refused. See backend/vector_roots.go
// (canonicalRoot, pickRootProject).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/c0wrk/internal/gittest"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// vectorDupWarnMessage is the Warn surface for several project records
// claiming one workspace root (vector_roots.go pickRootProject).
const vectorDupWarnMessage = "vector roots: several project records claim this root; routing to the best candidate"

// skipWithoutSymlinks skips a scenario that needs an unprivileged directory
// symlink.
func skipWithoutSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory symlinks on Windows require elevated privileges; the scenario cannot be modeled here")
	}
}

// TestVectorRoots_SymlinkedProjectRootRoutesToProject pins the checkout half:
// an aliased spelling of the project directory resolves to the registered
// project, ManagerForRoot builds a working manager instead of failing with
// the unavailable sentinel, and both spellings share ONE registry key — a
// second manager over the same storage is exactly what the fold prevents.
func TestVectorRoots_SymlinkedProjectRootRoutesToProject(t *testing.T) {
	skipWithoutSymlinks(t)
	h := newVectorWorktreeHarness(t)
	ctx := context.Background()

	alias := filepath.Join(gittest.TempDir(t), "repo-alias")
	if err := os.Symlink(h.proj.WorkspacePath, alias); err != nil {
		t.Fatalf("create repo symlink alias: %v", err)
	}

	projectID, storage, cache, err := h.api.resolveRootPlan(ctx, alias)
	if err != nil {
		t.Fatalf("resolveRootPlan(symlinked project root): %v", err)
	}
	if projectID != h.proj.ID {
		t.Fatalf("aliased root routed to project %q; want %q", projectID, h.proj.ID)
	}
	if want := config.ProjectVectorIndexPath(h.agentDir, h.proj.ID); storage != want {
		t.Errorf("storage = %q; want the project store %q", storage, want)
	}
	if want := config.ProjectEmbeddingCachePath(h.agentDir, h.proj.ID); cache != want {
		t.Errorf("cache = %q; want the project cache %q", cache, want)
	}

	mgr, err := h.roots.ManagerForRoot(ctx, alias)
	if err != nil {
		t.Fatalf("ManagerForRoot through a symlinked project path: %v", err)
	}
	if state := mgr.GetIndexStatus().State; state == vectorindex.IndexStateUnavailable {
		t.Fatalf("manager for the aliased root reports unavailable: %+v", mgr.GetIndexStatus())
	}
	waitForVectorBranch(t, mgr, "main")

	if got := h.roots.LiveManager(h.proj.WorkspacePath); got != mgr {
		t.Error("the physical spelling must resolve to the alias's manager")
	}
	if got := h.roots.LiveManager(alias); got != mgr {
		t.Error("the aliased spelling must resolve to the same manager")
	}
	if got := h.roots.liveRootsForTest(); got != 1 {
		t.Errorf("live roots = %d; want 1 (an alias must not fork a second manager)", got)
	}
}

// TestVectorRoots_AliasSpelledManagedTreeRoutesToProject pins the worktree
// half: a managed tree reached through an aliased spelling still belongs to
// the project whose container holds it, for both the plan and the agent-side
// routing primitive.
func TestVectorRoots_AliasSpelledManagedTreeRoutesToProject(t *testing.T) {
	skipWithoutSymlinks(t)
	h := newVectorWorktreeHarness(t)
	ctx := context.Background()

	tree := h.addManagedWorktree(t, "s-alias", "feature-alias")
	alias := filepath.Join(gittest.TempDir(t), "tree-alias")
	if err := os.Symlink(tree, alias); err != nil {
		t.Fatalf("create tree symlink alias: %v", err)
	}

	projectID, storage, _, err := h.api.resolveRootPlan(ctx, alias)
	if err != nil {
		t.Fatalf("resolveRootPlan(aliased managed tree): %v", err)
	}
	if projectID != h.proj.ID {
		t.Fatalf("aliased tree routed to project %q; want %q", projectID, h.proj.ID)
	}
	if want := config.ProjectVectorIndexPath(h.agentDir, h.proj.ID); storage != want {
		t.Errorf("aliased tree storage = %q; want the shared project store %q", storage, want)
	}

	ctxTree := sdktools.WithWorkspacePathNoProbe(ctx, alias)
	mgr, err := h.roots.ManagerForContext(ctxTree)
	if err != nil {
		t.Fatalf("ManagerForContext(aliased tree): %v", err)
	}
	waitForVectorBranch(t, mgr, "feature-alias")
}

// TestVectorRoots_DuplicateProjectRecordsPickDeterministically pins the
// duplicate-registration resolution: two rows on ONE workspace path always
// route to the same project — the freshest last_active_at, independent of
// listing order — and the choice is reported at Warn with the candidate list
// so the data defect is visible.
func TestVectorRoots_DuplicateProjectRecordsPickDeterministically(t *testing.T) {
	h := newVectorWorktreeHarness(t)
	ctx := context.Background()

	older := *h.proj
	older.LastActiveAt = "2024-01-01T00:00:00Z"
	if err := h.api.projStore.SaveProject(ctx, older); err != nil {
		t.Fatalf("rewrite the canonical row: %v", err)
	}
	duplicate := project.ProjectInfo{
		ID:            "proj-duplicate",
		Name:          "duplicate registration",
		WorkspacePath: h.proj.WorkspacePath,
		IsExternal:    true,
		CreatedAt:     "2024-06-01T00:00:00Z",
		LastActiveAt:  "2025-01-01T00:00:00Z",
	}
	if err := h.api.projStore.SaveProject(ctx, duplicate); err != nil {
		t.Fatalf("save the duplicate row: %v", err)
	}

	expectWarn := func(projectID string) miscExpectedDiagnostic {
		return miscExpectedDiagnostic{message: vectorDupWarnMessage, match: func(got miscExpectedDiagnostic) bool {
			return got.attrs["project"] == projectID &&
				strings.Contains(got.attrs["candidates"], duplicate.ID+"=") &&
				strings.Contains(got.attrs["candidates"], older.ID+"=")
		}}
	}
	// Three resolutions with the duplicate freshest, then a fourth after the
	// activity order is flipped.
	captureMiscDiagnostics(t, h.api,
		expectWarn(duplicate.ID), expectWarn(duplicate.ID), expectWarn(duplicate.ID),
		expectWarn(older.ID),
	)

	for i := 0; i < 3; i++ {
		projectID, storage, _, err := h.api.resolveRootPlan(ctx, h.proj.WorkspacePath)
		if err != nil {
			t.Fatalf("resolveRootPlan (call %d): %v", i, err)
		}
		if projectID != duplicate.ID {
			t.Fatalf("call %d routed to %q; want the freshest duplicate %q", i, projectID, duplicate.ID)
		}
		if want := config.ProjectVectorIndexPath(h.agentDir, duplicate.ID); storage != want {
			t.Fatalf("call %d storage = %q; want the picked project's store %q", i, storage, want)
		}
	}

	// Flip the activity order: the SAME two rows now pick the other project,
	// proving last_active_at — not listing order — decides the tie.
	older.LastActiveAt = "2025-06-01T00:00:00Z"
	duplicate.LastActiveAt = "2024-06-01T00:00:00Z"
	for _, p := range []project.ProjectInfo{older, duplicate} {
		if err := h.api.projStore.SaveProject(ctx, p); err != nil {
			t.Fatalf("re-save project %q: %v", p.ID, err)
		}
	}
	projectID, _, _, err := h.api.resolveRootPlan(ctx, h.proj.WorkspacePath)
	if err != nil {
		t.Fatalf("resolveRootPlan after the activity flip: %v", err)
	}
	if projectID != older.ID {
		t.Fatalf("after the flip routed to %q; want the now-freshest %q", projectID, older.ID)
	}
}

// TestVectorRoots_ExactSpellingOutranksAliasRow pins the first ordering key:
// an exact spelling of the requested root beats an aliased registration even
// when the alias row is the most recently active one.
func TestVectorRoots_ExactSpellingOutranksAliasRow(t *testing.T) {
	skipWithoutSymlinks(t)
	h := newVectorWorktreeHarness(t)
	ctx := context.Background()

	alias := filepath.Join(gittest.TempDir(t), "repo-alias")
	if err := os.Symlink(h.proj.WorkspacePath, alias); err != nil {
		t.Fatalf("create repo symlink alias: %v", err)
	}
	aliased := project.ProjectInfo{
		ID:            "proj-aliased",
		Name:          "aliased registration",
		WorkspacePath: alias,
		IsExternal:    true,
		CreatedAt:     "2024-06-01T00:00:00Z",
		LastActiveAt:  "2030-01-01T00:00:00Z",
	}
	if err := h.api.projStore.SaveProject(ctx, aliased); err != nil {
		t.Fatalf("save the aliased row: %v", err)
	}

	captureMiscDiagnostics(t, h.api,
		miscExpectedDiagnostic{message: vectorDupWarnMessage, match: func(got miscExpectedDiagnostic) bool {
			return got.attrs["project"] == h.proj.ID &&
				strings.Contains(got.attrs["candidates"], aliased.ID+"=")
		}},
	)

	projectID, _, _, err := h.api.resolveRootPlan(ctx, h.proj.WorkspacePath)
	if err != nil {
		t.Fatalf("resolveRootPlan: %v", err)
	}
	if projectID != h.proj.ID {
		t.Fatalf("routed to %q; want the exact-spelling row %q over the fresher alias %q", projectID, h.proj.ID, aliased.ID)
	}
}

// TestVectorRoots_ForeignRootReturnsNoTarget is the regression guard: a path
// belonging to no project checkout and no managed tree still fails with
// errVectorNoTarget and never builds a manager.
func TestVectorRoots_ForeignRootReturnsNoTarget(t *testing.T) {
	h := newVectorWorktreeHarness(t)
	ctx := context.Background()

	foreign := t.TempDir()
	if _, _, _, err := h.api.resolveRootPlan(ctx, foreign); !errors.Is(err, errVectorNoTarget) {
		t.Fatalf("resolveRootPlan(foreign) = %v; want errVectorNoTarget", err)
	}
	if _, err := h.roots.ManagerForRoot(ctx, foreign); !errors.Is(err, errVectorNoTarget) {
		t.Fatalf("ManagerForRoot(foreign) = %v; want errVectorNoTarget", err)
	}
	if got := h.roots.liveRootsForTest(); got != 0 {
		t.Errorf("live roots = %d after a foreign root; want 0", got)
	}
}
