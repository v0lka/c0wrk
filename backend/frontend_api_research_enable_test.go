package backend

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
)

// TestEnableResearch_DoesNotRetargetNonActiveProject verifies the [65] guard:
// EnableResearch invoked for a project that is NOT the active one must not
// overwrite the app-global active-project trio (ID + path + research root) —
// those drive session creation, the file tree, git status, and the
// skills-cache key, and retargeting them without the project-switch flow
// would silently point those operations at the wrong project. DisableResearch
// already guards the identical block with projectID == activeProjectID;
// EnableResearch must mirror it. The non-active project's toggle must still
// persist (and the ACTIVE project's enable must still track its root).
func TestEnableResearch_DoesNotRetargetNonActiveProject(t *testing.T) {
	base := t.TempDir()
	wsActive := filepath.Join(base, "ws-active")
	wsOther := filepath.Join(base, "ws-other")
	for _, ws := range []string{wsActive, wsOther} {
		if err := os.MkdirAll(ws, 0o755); err != nil {
			t.Fatalf("MkdirAll workspace: %v", err)
		}
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("create project store: %v", err)
	}
	for _, p := range []project.ProjectInfo{
		{ID: "proj-active", Name: "Active", WorkspacePath: wsActive},
		{ID: "proj-other", Name: "Other", WorkspacePath: wsOther},
	} {
		if err := store.SaveProject(context.Background(), p); err != nil {
			t.Fatalf("save project %s: %v", p.ID, err)
		}
	}

	f := &FrontendAPI{
		projectManager: project.NewManager(store, base, nil),
		projStore:      store,
		config:         &config.Config{Experimental: config.ExperimentalConfig{Enabled: true}},
		emitEvent:      func(_ string, _ ...any) {},
	}

	// A different project is active (the project-switch flow owns the trio).
	f.activeProjectMu.Lock()
	f.activeProjectID = "proj-active"
	f.activeProjectPath = wsActive
	f.activeResearchRoot = ""
	f.activeProjectMu.Unlock()

	// Enable RESEARCH for the NON-active project.
	if _, err := f.EnableResearch("proj-other", ""); err != nil {
		t.Fatalf("EnableResearch(proj-other): %v", err)
	}

	f.activeProjectMu.RLock()
	gotID, gotPath, gotRoot := f.activeProjectID, f.activeProjectPath, f.activeResearchRoot
	f.activeProjectMu.RUnlock()
	if gotID != "proj-active" {
		t.Errorf("activeProjectID = %q, want proj-active (non-active enable must not retarget)", gotID)
	}
	if gotPath != wsActive {
		t.Errorf("activeProjectPath = %q, want %q", gotPath, wsActive)
	}
	if gotRoot != "" {
		t.Errorf("activeResearchRoot = %q, want unchanged (empty)", gotRoot)
	}

	// The non-active project's toggle must still persist for later switches.
	other, err := f.projectManager.GetProject("proj-other")
	if err != nil || other == nil {
		t.Fatalf("GetProject(proj-other): %v", err)
	}
	if other.ResearchRoot == "" {
		t.Error("ResearchRoot not persisted for the non-active project")
	}

	// Positive control: enabling for the ACTIVE project still tracks the root
	// (the watcher needs it).
	if _, err := f.EnableResearch("proj-active", ""); err != nil {
		t.Fatalf("EnableResearch(proj-active): %v", err)
	}
	f.activeProjectMu.RLock()
	gotRoot = f.activeResearchRoot
	f.activeProjectMu.RUnlock()
	if gotRoot == "" {
		t.Error("activeResearchRoot not set when enabling for the active project")
	}
}
