package backend

// Legacy research-root detection and migration (the .research → research
// rename of pre-rename builds): ResearchStatusDTO.LegacyResearchRoots
// reports the detected <workspace>/.research directories (the checkout's and
// the active-session workspace's, deduplicated, always non-nil), and
// MigrateResearchRoots renames each into the canonical
// <workspace>/research — removing an empty target first, refusing a
// non-empty target and symlinks — then re-registers the research watches,
// emits research:changed (action=root_migrated) + papers:changed, and
// returns the fresh status.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/workspace"
)

// eventsByName returns every captured emission with the given event name,
// in emission order.
func (r *researchEventRecorder) eventsByName(name string) []capturedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []capturedEvent
	for _, e := range r.events {
		if e.name == name {
			out = append(out, e)
		}
	}
	return out
}

// migrationTestFrontend wires the lightweight FrontendAPI used by the
// migration tests: one real project whose workspace is a temp dir, a project
// store, and an event recorder. No app cell is installed, so
// activeSessionWorkspace never resolves and the detection boundary is always
// the checkout (the session-tree candidate is covered separately via the
// wtHarness).
func migrationTestFrontend(t *testing.T) (api *FrontendAPI, projectID, ws string, recorder *researchEventRecorder) {
	t.Helper()
	base := t.TempDir()
	ws = filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	db := openResearchTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("create project store: %v", err)
	}
	if err := store.SaveProject(context.Background(), project.ProjectInfo{
		ID:            "proj-1",
		Name:          "Migration",
		WorkspacePath: ws,
	}); err != nil {
		t.Fatalf("save project: %v", err)
	}

	recorder = &researchEventRecorder{}
	api = &FrontendAPI{
		projectManager: project.NewManager(store, base, nil),
		projStore:      store,
		emitEvent:      recorder.emit,
	}
	api.seedPublished.Store(true)
	return api, "proj-1", ws, recorder
}

// seedLegacyResearchRoot writes a minimal parseable research root (one
// R-001 project, active) under dir — the on-disk shape an older build left
// behind at <workspace>/.research.
func seedLegacyResearchRoot(t *testing.T, dir string) {
	t.Helper()
	seedResearchProjectDir(t, dir, "R-001-test")
	index := "# Research Index\n\n" +
		"Active research projects following the Iterative Engineering Research\n" +
		"Methodology.\n\n" +
		"| ID | Title | Status | Domain | Quarter | Researcher(s) | Brief |\n" +
		"|---|---|---|---|---|---|---|\n" +
		"| R-001 | Test R-001 | Active | — | — | — | [brief](R-001-test/brief.md) |\n"
	if err := os.WriteFile(filepath.Join(dir, "index.md"), []byte(index), 0o644); err != nil {
		t.Fatalf("index.md: %v", err)
	}
}

// TestMigrateResearchRoots_Detection pins the detection contract of the
// status DTO: LegacyResearchRoots is always non-nil, stays empty while no
// legacy root exists (and never leaks into ResearchRoot, which stays the
// canonical <workspace>/research), and lists the checkout's .research once
// it exists.
func TestMigrateResearchRoots_Detection(t *testing.T) {
	api, projectID, ws, _ := migrationTestFrontend(t)

	status, err := api.GetResearchStatus(projectID)
	if err != nil {
		t.Fatalf("GetResearchStatus: %v", err)
	}
	if status.LegacyResearchRoots == nil || len(status.LegacyResearchRoots) != 0 {
		t.Errorf("LegacyResearchRoots = %#v, want a non-nil empty slice", status.LegacyResearchRoots)
	}

	legacy := config.LegacyProjectResearchPath(ws)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir legacy root: %v", err)
	}
	status, err = api.GetResearchStatus(projectID)
	if err != nil {
		t.Fatalf("GetResearchStatus after legacy appears: %v", err)
	}
	if want := []string{legacy}; !reflect.DeepEqual(status.LegacyResearchRoots, want) {
		t.Errorf("LegacyResearchRoots = %v, want %v", status.LegacyResearchRoots, want)
	}
	if status.ResearchRoot != config.ProjectResearchPath(ws) {
		t.Errorf("ResearchRoot = %q, want the canonical %q (never derived from the legacy root)",
			status.ResearchRoot, config.ProjectResearchPath(ws))
	}
}

// TestMigrateResearchRoots_DetectionIncludesSessionTree pins the
// session-following half of the detection: a managed worktree session adds
// ITS tree's .research as a second entry (checkout first), and a newer local
// session (workspace == checkout) collapses the boundary back onto the
// checkout, deduplicating the two candidates to one.
func TestMigrateResearchRoots_DetectionIncludesSessionTree(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	checkout := h.project.WorkspacePath

	managed := h.api.mustManagedSession(t, "", true, "")
	tree := managed.WorkspaceBinding.WorkspacePath
	if tree == checkout {
		t.Fatalf("managed session tree %q must differ from the checkout %q", tree, checkout)
	}

	for _, dir := range []string{checkout, tree} {
		if err := os.MkdirAll(config.LegacyProjectResearchPath(dir), 0o755); err != nil {
			t.Fatalf("mkdir legacy root in %s: %v", dir, err)
		}
	}

	status, err := h.api.GetResearchStatus(h.project.ID)
	if err != nil {
		t.Fatalf("GetResearchStatus: %v", err)
	}
	want := []string{
		config.LegacyProjectResearchPath(checkout),
		config.LegacyProjectResearchPath(tree),
	}
	if !reflect.DeepEqual(status.LegacyResearchRoots, want) {
		t.Errorf("LegacyResearchRoots = %v, want the checkout's and the session tree's roots %v",
			status.LegacyResearchRoots, want)
	}

	// A newer LOCAL session (checkout workspace): the boundary is the
	// checkout again, so the two candidates dedup to one.
	local, err := h.manager.CreateSession(h.project.ID, checkout)
	if err != nil {
		t.Fatalf("CreateSession (local): %v", err)
	}
	// Stamped explicitly ahead: wall-clock creation times share second-level
	// granularity and a tie would make the outcome depend on the store's
	// tie-break order (same trick as TestEffectiveResearchRoot).
	local.LastActiveAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	local.CreatedAt = local.LastActiveAt
	if err := h.store.SaveSession(context.Background(), *local); err != nil {
		t.Fatalf("SaveSession (local): %v", err)
	}

	status, err = h.api.GetResearchStatus(h.project.ID)
	if err != nil {
		t.Fatalf("GetResearchStatus after local session: %v", err)
	}
	if want := []string{config.LegacyProjectResearchPath(checkout)}; !reflect.DeepEqual(status.LegacyResearchRoots, want) {
		t.Errorf("LegacyResearchRoots = %v, want the deduplicated single checkout entry %v",
			status.LegacyResearchRoots, want)
	}
}

// TestMigrateResearchRoots_RenamesLegacyRoot pins the happy path: the legacy
// root's content lands under the canonical <workspace>/research verbatim,
// the legacy directory is gone, the returned status is the fresh post-
// migration state (canonical root, parsed R-001, no legacy entries), both
// events fire (research:changed action=root_migrated, papers:changed), and
// the workspace watcher — live during the call — reports a write inside the
// migrated research/papers tree, proving the research watches were
// re-registered (NewWatcher alone watches only the root level).
func TestMigrateResearchRoots_RenamesLegacyRoot(t *testing.T) {
	api, projectID, ws, recorder := migrationTestFrontend(t)
	legacy := config.LegacyProjectResearchPath(ws)
	canonical := config.ProjectResearchPath(ws)

	seedLegacyResearchRoot(t, legacy)
	if err := os.MkdirAll(filepath.Join(legacy, "papers"), 0o755); err != nil {
		t.Fatalf("mkdir legacy papers: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "papers", "note.md"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("write legacy paper note: %v", err)
	}

	var papersEvents atomic.Int32
	papersRoot := config.PaperLibraryPathIn(canonical)
	w, err := workspace.NewWatcher(ws, func(changed []string) {
		for _, p := range changed {
			if config.IsResearchPath(papersRoot, p) {
				papersEvents.Add(1)
				return
			}
		}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	api.watcherMu.Lock()
	api.watcher = w
	api.watcherMu.Unlock()

	status, err := api.MigrateResearchRoots(projectID)
	if err != nil {
		t.Fatalf("MigrateResearchRoots: %v", err)
	}

	if _, statErr := os.Lstat(legacy); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("legacy root still exists (stat err: %v)", statErr)
	}
	if _, err := os.Stat(filepath.Join(canonical, "index.md")); err != nil {
		t.Errorf("migrated index.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(canonical, "R-001-test", "hypotheses", "H-001.md")); err != nil {
		t.Errorf("migrated hypothesis card missing: %v", err)
	}

	if !status.Enabled || status.ResearchRoot != canonical {
		t.Errorf("status = enabled:%v root:%q, want enabled with the canonical root %q",
			status.Enabled, status.ResearchRoot, canonical)
	}
	if status.Root == nil || status.Root.ActiveProjectID != "R-001" {
		t.Errorf("status.Root = %+v, want the migrated R-001 as active", status.Root)
	}
	if status.LegacyResearchRoots == nil || len(status.LegacyResearchRoots) != 0 {
		t.Errorf("LegacyResearchRoots = %#v, want a non-nil empty slice after migration", status.LegacyResearchRoots)
	}

	changed := recorder.eventsByName(EventResearchChanged)
	if len(changed) != 1 {
		t.Fatalf("research:changed emissions = %d, want 1", len(changed))
	}
	if changed[0].payload["action"] != "root_migrated" || changed[0].payload["project_id"] != projectID {
		t.Errorf("research:changed payload = %v, want action=root_migrated for %s", changed[0].payload, projectID)
	}
	papersChanged := recorder.eventsByName(EventPapersChanged)
	if len(papersChanged) != 1 {
		t.Fatalf("papers:changed emissions = %d, want 1", len(papersChanged))
	}
	if papersChanged[0].payload["project_id"] != projectID || !strings.Contains(papersChanged[0].payload["paths"], canonical) {
		t.Errorf("papers:changed payload = %v, want project %s and paths containing %s",
			papersChanged[0].payload, projectID, canonical)
	}

	// Watcher smoke: a write inside the MIGRATED papers tree reaches the
	// watcher callback only when the research tree was re-registered.
	writeUntilEmitted(t, filepath.Join(canonical, "papers", "note.md"), []byte("v2"), &papersEvents, 1)
}

// TestMigrateResearchRoots_EmptyTargetReplaced pins the empty-target rule:
// an existing but EMPTY canonical research directory is removed and replaced
// by the renamed legacy root (content preserved, legacy gone, success event).
func TestMigrateResearchRoots_EmptyTargetReplaced(t *testing.T) {
	api, projectID, ws, recorder := migrationTestFrontend(t)
	legacy := config.LegacyProjectResearchPath(ws)
	canonical := config.ProjectResearchPath(ws)

	seedLegacyResearchRoot(t, legacy)
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("mkdir empty target: %v", err)
	}

	status, err := api.MigrateResearchRoots(projectID)
	if err != nil {
		t.Fatalf("MigrateResearchRoots: %v", err)
	}
	if _, err := os.Stat(filepath.Join(canonical, "index.md")); err != nil {
		t.Errorf("migrated index.md missing: %v", err)
	}
	if _, statErr := os.Lstat(legacy); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("legacy root still exists (stat err: %v)", statErr)
	}
	if !status.Enabled || status.ResearchRoot != canonical {
		t.Errorf("status = enabled:%v root:%q, want enabled with the canonical root %q",
			status.Enabled, status.ResearchRoot, canonical)
	}
	changed := recorder.eventsByName(EventResearchChanged)
	if len(changed) != 1 || changed[0].payload["action"] != "root_migrated" {
		t.Errorf("research:changed emissions = %v, want one action=root_migrated", changed)
	}
}

// TestMigrateResearchRoots_NonEmptyTargetRefused pins the non-empty-target
// rule: a canonical research directory that already carries content is never
// clobbered — the migration fails with an actionable message, BOTH trees are
// left untouched, and no events fire.
func TestMigrateResearchRoots_NonEmptyTargetRefused(t *testing.T) {
	api, projectID, ws, recorder := migrationTestFrontend(t)
	legacy := config.LegacyProjectResearchPath(ws)
	canonical := config.ProjectResearchPath(ws)

	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir legacy root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "index.md"), []byte("legacy"), 0o644); err != nil {
		t.Fatalf("write legacy index: %v", err)
	}
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("mkdir non-empty target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(canonical, "keep.txt"), []byte("target"), 0o644); err != nil {
		t.Fatalf("write target stray file: %v", err)
	}

	_, err := api.MigrateResearchRoots(projectID)
	if err == nil {
		t.Fatal("expected an error for a non-empty target directory")
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("error = %q, want it to name the non-empty target", err)
	}

	if _, statErr := os.Stat(filepath.Join(legacy, "index.md")); statErr != nil {
		t.Errorf("legacy index.md must be untouched: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(canonical, "keep.txt")); statErr != nil {
		t.Errorf("target keep.txt must be untouched: %v", statErr)
	}
	if changed := recorder.eventsByName(EventResearchChanged); len(changed) != 0 {
		t.Errorf("research:changed emissions = %v, want none on refusal", changed)
	}
	if changed := recorder.eventsByName(EventPapersChanged); len(changed) != 0 {
		t.Errorf("papers:changed emissions = %v, want none on refusal", changed)
	}
}

// TestMigrateResearchRoots_SymlinkRefused pins the symlink rule: a legacy
// .research that is a symlink (not a real directory) is detected but NEVER
// followed or renamed — the migration refuses with an actionable error, the
// symlink and its target stay intact, no canonical root materializes, and no
// events fire.
func TestMigrateResearchRoots_SymlinkRefused(t *testing.T) {
	api, projectID, ws, recorder := migrationTestFrontend(t)
	legacy := config.LegacyProjectResearchPath(ws)
	canonical := config.ProjectResearchPath(ws)

	realDir := t.TempDir()
	if err := os.Symlink(realDir, legacy); err != nil {
		t.Fatalf("symlink legacy root: %v", err)
	}

	_, err := api.MigrateResearchRoots(projectID)
	if err == nil {
		t.Fatal("expected an error for a symlinked legacy root")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error = %q, want it to name the non-directory legacy root", err)
	}

	info, statErr := os.Lstat(legacy)
	if statErr != nil {
		t.Fatalf("Lstat legacy: %v", statErr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("legacy path %q is no longer a symlink (mode %s)", legacy, info.Mode())
	}
	if _, statErr := os.Stat(realDir); statErr != nil {
		t.Errorf("symlink target must be untouched: %v", statErr)
	}
	if _, statErr := os.Stat(canonical); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("canonical root must not materialize on refusal (stat err: %v)", statErr)
	}
	if changed := recorder.eventsByName(EventResearchChanged); len(changed) != 0 {
		t.Errorf("research:changed emissions = %v, want none on refusal", changed)
	}
}

// TestMigrateResearchRoots_NoOpSuccess pins the no-op contract: with no
// legacy roots anywhere the RPC is a plain success returning the fresh
// status — no canonical directory is created, and no events fire. Invalid
// arguments (empty/unknown project id) are rejected up front.
func TestMigrateResearchRoots_NoOpSuccess(t *testing.T) {
	api, projectID, ws, recorder := migrationTestFrontend(t)
	canonical := config.ProjectResearchPath(ws)

	if _, err := api.MigrateResearchRoots(""); err == nil {
		t.Error("expected an error for an empty project id")
	}
	if _, err := api.MigrateResearchRoots("no-such-project"); err == nil {
		t.Error("expected an error for an unknown project id")
	}

	status, err := api.MigrateResearchRoots(projectID)
	if err != nil {
		t.Fatalf("MigrateResearchRoots (no-op): %v", err)
	}
	if !status.Enabled || status.ResearchRoot != canonical {
		t.Errorf("status = enabled:%v root:%q, want enabled with the canonical root %q",
			status.Enabled, status.ResearchRoot, canonical)
	}
	if status.LegacyResearchRoots == nil || len(status.LegacyResearchRoots) != 0 {
		t.Errorf("LegacyResearchRoots = %#v, want a non-nil empty slice", status.LegacyResearchRoots)
	}
	if _, statErr := os.Stat(canonical); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a no-op migration must not create the canonical root (stat err: %v)", statErr)
	}
	if total := len(recorder.eventsByName(EventResearchChanged)) + len(recorder.eventsByName(EventPapersChanged)); total != 0 {
		t.Errorf("total event emissions = %d, want none for a no-op", total)
	}
}
