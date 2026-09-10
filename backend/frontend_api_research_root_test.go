package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/research"
)

// ---------------------------------------------------------------------------
// Root-level RPCs (SetActiveResearch / DeleteResearch / pins / next step)
// ---------------------------------------------------------------------------

// capturedEvent is one recorded event emission: its name and, when the payload
// was a map[string]string (the research:changed shape), its fields.
type capturedEvent struct {
	name    string
	payload map[string]string
}

// researchEventRecorder captures emitted events so tests can assert on the
// research:changed action values. Safe for concurrent use (the RPCs may emit
// from any goroutine).
type researchEventRecorder struct {
	mu     sync.Mutex
	events []capturedEvent
}

func (r *researchEventRecorder) emit(name string, args ...any) {
	e := capturedEvent{name: name}
	if len(args) == 1 {
		if payload, ok := args[0].(map[string]string); ok {
			e.payload = payload
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// researchChanged returns every captured research:changed payload in
// emission order.
func (r *researchEventRecorder) researchChanged() []map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]string
	for _, e := range r.events {
		if e.name == EventResearchChanged {
			out = append(out, e.payload)
		}
	}
	return out
}

// researchRootTestFrontend wires a FrontendAPI with one research-enabled
// project whose workspace carries a research root with the seeded R-NNN
// directories, a canonical index.md listing them in the given order, and the
// persisted pins. It returns the API, the project ID, the research root, and
// the event recorder. Each seeded project carries a brief, a graph, and one
// open H-001 card (see seedResearchProjectDir).
func researchRootTestFrontend(t *testing.T, seedDirs []string, pins project.ResearchPins) (api *FrontendAPI, projectID, researchRoot string, recorder *researchEventRecorder) {
	t.Helper()
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	researchRoot = filepath.Join(ws, ".research")

	rows := make([]string, 0, len(seedDirs))
	for _, dir := range seedDirs {
		rid := research.NormalizeResearchID(dir)
		if rid == "" {
			t.Fatalf("seed dir %q carries no R-NNN id", dir)
		}
		seedResearchProjectDir(t, researchRoot, dir)
		rows = append(rows, fmt.Sprintf("| %s | Test %s | Active | — | — | — | [brief](%s/brief.md) |", rid, rid, dir))
	}
	if len(rows) > 0 {
		index := "# Research Index\n\n" +
			"Active research projects following the Iterative Engineering Research\n" +
			"Methodology.\n\n" +
			"| ID | Title | Status | Domain | Quarter | Researcher(s) | Brief |\n" +
			"|---|---|---|---|---|---|---|\n" +
			strings.Join(rows, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(researchRoot, "index.md"), []byte(index), 0o644); err != nil {
			t.Fatalf("index.md: %v", err)
		}
	}

	db := openResearchTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("create project store: %v", err)
	}
	if err := store.SaveProject(context.Background(), project.ProjectInfo{
		ID:            "proj-1",
		Name:          "Research",
		WorkspacePath: ws,
		ResearchRoot:  researchRoot,
		ResearchPins:  pins,
	}); err != nil {
		t.Fatalf("save project: %v", err)
	}

	recorder = &researchEventRecorder{}
	api = &FrontendAPI{
		projectManager: project.NewManager(store, base, nil),
		projStore:      store,
		emitEvent:      recorder.emit,
	}
	return api, "proj-1", researchRoot, recorder
}

// researchTwoRootTestFrontend wires a FrontendAPI with TWO research-enabled
// projects (A: R-001-test, B: R-009-other — each root carrying its own open
// H-001 card) plus a project store and event recorder, so the foreign-R-NNN
// rejection of the pin/delete/activate RPCs is observable end to end.
func researchTwoRootTestFrontend(t *testing.T) (api *FrontendAPI, projA, projB, rootA, rootB string) {
	t.Helper()
	base := t.TempDir()
	wsA := filepath.Join(base, "wsA")
	wsB := filepath.Join(base, "wsB")
	rootA = filepath.Join(wsA, ".research")
	rootB = filepath.Join(wsB, ".research")

	seedResearchProjectDir(t, rootA, "R-001-test")
	seedResearchProjectDir(t, rootB, "R-009-other")

	db := openResearchTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("create project store: %v", err)
	}
	for _, p := range []project.ProjectInfo{
		{ID: "proj-a", Name: "A", WorkspacePath: wsA, ResearchRoot: rootA},
		{ID: "proj-b", Name: "B", WorkspacePath: wsB, ResearchRoot: rootB},
	} {
		if err := store.SaveProject(context.Background(), p); err != nil {
			t.Fatalf("save project %s: %v", p.ID, err)
		}
	}

	recorder := &researchEventRecorder{}
	api = &FrontendAPI{
		projectManager: project.NewManager(store, base, nil),
		projStore:      store,
		emitEvent:      recorder.emit,
	}
	return api, "proj-a", "proj-b", rootA, rootB
}

// researchPinsOf loads the project's persisted pins straight from the store.
func researchPinsOf(t *testing.T, f *FrontendAPI, projectID string) project.ResearchPins {
	t.Helper()
	proj, err := f.projectManager.GetProject(projectID)
	if err != nil || proj == nil {
		t.Fatalf("GetProject(%s): %v", projectID, err)
	}
	return proj.ResearchPins
}

// TestResearchRPC_SetActiveResearchChangesActiveProject pins the RPC contract:
// SetActiveResearch rewrites index.md so the named R-NNN becomes the active
// project (the last index entry), returns the refreshed status (with the new
// active project's root and the project's pins), and emits research:changed
// with action=active_changed.
func TestResearchRPC_SetActiveResearchChangesActiveProject(t *testing.T) {
	f, projectID, researchRoot, recorder := researchRootTestFrontend(t,
		[]string{"R-001-test", "R-002-second"},
		project.ResearchPins{Research: []string{"R-002-second/brief.md"}})

	// Canonical order: R-002 is the last index entry → active.
	parsed, err := research.ParseResearchRoot(researchRoot)
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	if parsed.ActiveProjectID != "R-002" {
		t.Fatalf("active before = %q, want R-002 (last index entry)", parsed.ActiveProjectID)
	}

	status, err := f.SetActiveResearch(projectID, "R-001")
	if err != nil {
		t.Fatalf("SetActiveResearch: %v", err)
	}
	if !status.Enabled || status.ResearchRoot != researchRoot {
		t.Errorf("status = enabled:%v root:%q, want enabled with root %q", status.Enabled, status.ResearchRoot, researchRoot)
	}
	if status.Root == nil || status.Root.ActiveProjectID != "R-001" {
		t.Errorf("status.Root.ActiveProjectID = %+v, want R-001", status.Root)
	}
	if !reflect.DeepEqual(status.PinnedResearch, []string{"R-002-second/brief.md"}) {
		t.Errorf("PinnedResearch = %v, want the persisted pins", status.PinnedResearch)
	}
	if status.PinnedHypotheses == nil {
		t.Error("PinnedHypotheses = nil, want a normalized non-nil (empty) map")
	}

	// The disk state changed: a fresh parse picks R-001 as active.
	parsed, err = research.ParseResearchRoot(researchRoot)
	if err != nil {
		t.Fatalf("ParseResearchRoot after activate: %v", err)
	}
	if parsed.ActiveProjectID != "R-001" {
		t.Errorf("active after = %q, want R-001 (moved row is the last entry)", parsed.ActiveProjectID)
	}

	// Exactly one research:changed event with action=active_changed.
	changed := recorder.researchChanged()
	if len(changed) != 1 {
		t.Fatalf("research:changed emissions = %d, want 1", len(changed))
	}
	if changed[0]["action"] != "active_changed" || changed[0]["project_id"] != projectID {
		t.Errorf("research:changed payload = %v, want action=active_changed for %s", changed[0], projectID)
	}
}

// TestResearchRPC_SetActiveResearchRejectsForeignAndInvalid verifies the
// ownership and shape checks: a foreign R-NNN (belonging to another project's
// research root), an unknown id, or a malformed id is rejected before any
// file is touched, while each project's own R-NNN activates fine (including
// the missing-index path, which creates index.md with a minimal row).
func TestResearchRPC_SetActiveResearchRejectsForeignAndInvalid(t *testing.T) {
	f, projA, projB, rootA, rootB := researchTwoRootTestFrontend(t)

	if _, err := f.SetActiveResearch(projA, "R-009"); err == nil {
		t.Error("expected error: R-009 belongs to proj-b's research root, not proj-a's")
	}
	if _, err := f.SetActiveResearch(projA, "R-042"); err == nil {
		t.Error("expected error: R-042 matches nothing under proj-a's root")
	}
	if _, err := f.SetActiveResearch(projA, ""); err == nil {
		t.Error("expected error for an empty research id")
	}
	if _, err := f.SetActiveResearch(projA, "not-a-rid"); err == nil {
		t.Error("expected error for a malformed research id")
	}

	// proj-a's active project is untouched (its single R-001 stays active).
	parsed, err := research.ParseResearchRoot(rootA)
	if err != nil {
		t.Fatalf("ParseResearchRoot(rootA): %v", err)
	}
	if parsed.ActiveProjectID != "R-001" {
		t.Errorf("proj-a active = %q, want R-001 (rejected call must not mutate)", parsed.ActiveProjectID)
	}

	// Positive control: proj-b's own R-009 activates (no index.md exists in
	// rootB — the minimal-row creation path).
	if _, err := f.SetActiveResearch(projB, "R-009"); err != nil {
		t.Fatalf("SetActiveResearch with proj-b's own R-009: %v", err)
	}
	parsed, err = research.ParseResearchRoot(rootB)
	if err != nil {
		t.Fatalf("ParseResearchRoot(rootB): %v", err)
	}
	if parsed.ActiveProjectID != "R-009" {
		t.Errorf("proj-b active = %q, want R-009", parsed.ActiveProjectID)
	}
}

// TestResearchRPC_DeleteResearchRemovesProjectAndCleansPins pins the full
// delete contract: the R-NNN's index rows and directory disappear, the next
// project becomes active, the pins referencing the deleted project (its brief
// and its hypothesis cards) are removed from ProjectInfo.ResearchPins — for
// a hypothesis pinned across two R-NNNs only the deleted project's card path
// is dropped — the returned status reflects the cleaned state, and
// research:changed is emitted with action=project_deleted.
func TestResearchRPC_DeleteResearchRemovesProjectAndCleansPins(t *testing.T) {
	f, projectID, researchRoot, recorder := researchRootTestFrontend(t,
		[]string{"R-001-test", "R-002-second"},
		project.ResearchPins{
			Research: []string{"R-001-test/brief.md", "R-002-second/brief.md"},
			Hypotheses: map[string][]string{
				"H-001": {"R-001-test/hypotheses/H-001.md", "R-002-second/hypotheses/H-001.md"},
				"H-009": {"R-001-test/hypotheses/H-009.md"},
			},
		})

	status, err := f.DeleteResearch(projectID, "R-001")
	if err != nil {
		t.Fatalf("DeleteResearch: %v", err)
	}

	// The directory tree is gone.
	if _, statErr := os.Stat(filepath.Join(researchRoot, "R-001-test")); !os.IsNotExist(statErr) {
		t.Errorf("R-001-test directory still exists (stat err: %v)", statErr)
	}
	// The index carries no entry for R-001 any more, and R-002 is the active
	// (and only) project.
	indexContent, err := os.ReadFile(filepath.Join(researchRoot, "index.md"))
	if err != nil {
		t.Fatalf("read index.md: %v", err)
	}
	if strings.Contains(string(indexContent), "R-001") {
		t.Errorf("index.md still references R-001:\n%s", indexContent)
	}
	if status.Root == nil || status.Root.ActiveProjectID != "R-002" || len(status.Root.Projects) != 1 {
		t.Errorf("status.Root = %+v, want R-002 as the single remaining project", status.Root)
	}

	// Pins cleaned: R-001's brief dropped, R-002's kept; H-001 keeps only
	// R-002's card; H-009 (only R-001 cards) dropped entirely.
	wantResearch := []string{"R-002-second/brief.md"}
	wantHypotheses := map[string][]string{"H-001": {"R-002-second/hypotheses/H-001.md"}}
	if !reflect.DeepEqual(status.PinnedResearch, wantResearch) {
		t.Errorf("status.PinnedResearch = %v, want %v", status.PinnedResearch, wantResearch)
	}
	if !reflect.DeepEqual(status.PinnedHypotheses, wantHypotheses) {
		t.Errorf("status.PinnedHypotheses = %v, want %v", status.PinnedHypotheses, wantHypotheses)
	}

	// The cleaned pins are persisted, not just reported.
	pins := researchPinsOf(t, f, projectID)
	if !reflect.DeepEqual(pins.Research, wantResearch) {
		t.Errorf("persisted Research pins = %v, want %v", pins.Research, wantResearch)
	}
	if !reflect.DeepEqual(pins.Hypotheses, wantHypotheses) {
		t.Errorf("persisted Hypotheses pins = %v, want %v", pins.Hypotheses, wantHypotheses)
	}

	changed := recorder.researchChanged()
	if len(changed) != 1 || changed[0]["action"] != "project_deleted" || changed[0]["project_id"] != projectID {
		t.Errorf("research:changed emissions = %v, want one action=project_deleted for %s", changed, projectID)
	}
}

// TestResearchRPC_DeleteResearchLastProject verifies deleting the only
// research project: the call succeeds, RESEARCH stays enabled (the toggle is
// independent of the projects), the parsed root reports no projects, and the
// pins are fully cleaned.
func TestResearchRPC_DeleteResearchLastProject(t *testing.T) {
	f, projectID, _, recorder := researchRootTestFrontend(t,
		[]string{"R-001-test"},
		project.ResearchPins{
			Research:   []string{"R-001-test/brief.md"},
			Hypotheses: map[string][]string{"H-001": {"R-001-test/hypotheses/H-001.md"}},
		})

	status, err := f.DeleteResearch(projectID, "R-001")
	if err != nil {
		t.Fatalf("DeleteResearch: %v", err)
	}
	if !status.Enabled {
		t.Error("Enabled = false, want true (the RESEARCH toggle survives deleting the last project)")
	}
	if status.Root != nil && len(status.Root.Projects) != 0 {
		t.Errorf("Root.Projects = %d, want 0", len(status.Root.Projects))
	}
	if len(status.PinnedResearch) != 0 || len(status.PinnedHypotheses) != 0 {
		t.Errorf("pins = %v / %v, want both empty", status.PinnedResearch, status.PinnedHypotheses)
	}
	if changed := recorder.researchChanged(); len(changed) != 1 || changed[0]["action"] != "project_deleted" {
		t.Errorf("research:changed emissions = %v, want one action=project_deleted", changed)
	}
}

// TestResearchRPC_DeleteResearchRejectsForeignAndInvalid verifies the delete
// ownership and shape checks: a foreign R-NNN is rejected before anything is
// touched (both roots keep their directories), and malformed ids are
// rejected.
func TestResearchRPC_DeleteResearchRejectsForeignAndInvalid(t *testing.T) {
	f, projA, projB, rootA, rootB := researchTwoRootTestFrontend(t)

	// R-001 belongs to proj-a; deleting it through proj-b must fail.
	if _, err := f.DeleteResearch(projB, "R-001"); err == nil {
		t.Error("expected error: R-001 belongs to proj-a's research root, not proj-b's")
	}
	if _, err := f.DeleteResearch(projA, ""); err == nil {
		t.Error("expected error for an empty research id")
	}
	if _, err := f.DeleteResearch(projA, "not-a-rid"); err == nil {
		t.Error("expected error for a malformed research id")
	}

	// Both roots are untouched.
	for _, dir := range []string{filepath.Join(rootA, "R-001-test"), filepath.Join(rootB, "R-009-other")} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("project directory %s removed by a rejected delete: %v", dir, err)
		}
	}

	// Positive control: proj-a deletes its own R-001.
	if _, err := f.DeleteResearch(projA, "R-001"); err != nil {
		t.Fatalf("DeleteResearch with proj-a's own R-001: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootA, "R-001-test")); !os.IsNotExist(err) {
		t.Errorf("R-001-test still exists after a successful delete (stat err: %v)", err)
	}
}

// TestResearchRPC_SetResearchPinnedRoundTrip pins the research-pin toggle:
// pinning records the project's brief path (root-relative, forward slashes),
// is idempotent, unpins symmetrically, and persists through the project
// store. Foreign, unknown, and malformed ids are rejected; no events are
// emitted by the pin RPCs.
func TestResearchRPC_SetResearchPinnedRoundTrip(t *testing.T) {
	f, projectID, _, recorder := researchRootTestFrontend(t,
		[]string{"R-001-test", "R-002-second"}, project.ResearchPins{})

	if err := f.SetResearchPinned(projectID, "R-001", true); err != nil {
		t.Fatalf("pin R-001: %v", err)
	}
	want := []string{"R-001-test/brief.md"}
	if pins := researchPinsOf(t, f, projectID); !reflect.DeepEqual(pins.Research, want) {
		t.Fatalf("pins after first pin = %v, want %v", pins.Research, want)
	}

	// Double pin is a no-op.
	if err := f.SetResearchPinned(projectID, "R-001", true); err != nil {
		t.Fatalf("re-pin R-001: %v", err)
	}
	if pins := researchPinsOf(t, f, projectID); !reflect.DeepEqual(pins.Research, want) {
		t.Errorf("pins after re-pin = %v, want %v (idempotent)", pins.Research, want)
	}

	// A second project pins alongside.
	if err := f.SetResearchPinned(projectID, "R-002", true); err != nil {
		t.Fatalf("pin R-002: %v", err)
	}
	wantBoth := []string{"R-001-test/brief.md", "R-002-second/brief.md"}
	if pins := researchPinsOf(t, f, projectID); !reflect.DeepEqual(pins.Research, wantBoth) {
		t.Errorf("pins = %v, want %v", pins.Research, wantBoth)
	}

	// Unpin one; the other survives.
	if err := f.SetResearchPinned(projectID, "R-001", false); err != nil {
		t.Fatalf("unpin R-001: %v", err)
	}
	wantR2 := []string{"R-002-second/brief.md"}
	if pins := researchPinsOf(t, f, projectID); !reflect.DeepEqual(pins.Research, wantR2) {
		t.Errorf("pins after unpin = %v, want %v", pins.Research, wantR2)
	}
	// Unpin again: a no-op, not an error.
	if err := f.SetResearchPinned(projectID, "R-001", false); err != nil {
		t.Fatalf("re-unpin R-001: %v", err)
	}

	// Foreign / unknown / malformed ids are rejected before any write.
	if err := f.SetResearchPinned(projectID, "R-042", true); err == nil {
		t.Error("expected error: R-042 matches nothing under the root")
	}
	if err := f.SetResearchPinned(projectID, "bogus", true); err == nil {
		t.Error("expected error for a malformed research id")
	}
	if pins := researchPinsOf(t, f, projectID); !reflect.DeepEqual(pins.Research, wantR2) {
		t.Errorf("pins mutated by rejected calls: %v", pins.Research)
	}

	// The pin RPCs never emit research:changed — the caller's resolved
	// promise is its refresh signal.
	if changed := recorder.researchChanged(); len(changed) != 0 {
		t.Errorf("research:changed emissions = %v, want none from pin RPCs", changed)
	}
}

// TestResearchRPC_SetHypothesisPinnedRoundTrip pins the hypothesis-pin
// toggle: pinning records the card path under the hypothesis key (a list —
// the same H-NNN exists across R-NNN projects), the key is dropped when its
// list empties, pinning a missing card is rejected, and unpinning a card
// whose file was deleted still works (stale pins stay removable).
func TestResearchRPC_SetHypothesisPinnedRoundTrip(t *testing.T) {
	f, projectID, researchRoot, _ := researchRootTestFrontend(t,
		[]string{"R-001-test", "R-002-second"}, project.ResearchPins{})

	// Both projects carry an H-001 card; pinning both yields two paths under
	// the single H-001 key.
	if err := f.SetHypothesisPinned(projectID, "R-001", "H-001", true); err != nil {
		t.Fatalf("pin H-001 in R-001: %v", err)
	}
	if err := f.SetHypothesisPinned(projectID, "R-002", "H-001", true); err != nil {
		t.Fatalf("pin H-001 in R-002: %v", err)
	}
	want := map[string][]string{"H-001": {"R-001-test/hypotheses/H-001.md", "R-002-second/hypotheses/H-001.md"}}
	if pins := researchPinsOf(t, f, projectID); !reflect.DeepEqual(pins.Hypotheses, want) {
		t.Fatalf("pins = %v, want %v", pins.Hypotheses, want)
	}

	// Unpin one: the other card path survives under the same key.
	if err := f.SetHypothesisPinned(projectID, "R-001", "H-001", false); err != nil {
		t.Fatalf("unpin H-001 in R-001: %v", err)
	}
	wantR2 := map[string][]string{"H-001": {"R-002-second/hypotheses/H-001.md"}}
	if pins := researchPinsOf(t, f, projectID); !reflect.DeepEqual(pins.Hypotheses, wantR2) {
		t.Errorf("pins after unpin = %v, want %v", pins.Hypotheses, wantR2)
	}

	// Unpin the last: the key is dropped entirely.
	if err := f.SetHypothesisPinned(projectID, "R-002", "H-001", false); err != nil {
		t.Fatalf("unpin H-001 in R-002: %v", err)
	}
	if pins := researchPinsOf(t, f, projectID); len(pins.Hypotheses) != 0 {
		t.Errorf("pins = %v, want the H-001 key dropped", pins.Hypotheses)
	}

	// Pinning a card that does not exist is rejected (no phantom pins).
	if err := f.SetHypothesisPinned(projectID, "R-001", "H-999", true); err == nil {
		t.Error("expected error: H-999 card does not exist in R-001")
	}
	if err := f.SetHypothesisPinned(projectID, "R-001", "", true); err == nil {
		t.Error("expected error for an empty hypothesis id")
	}
	if err := f.SetHypothesisPinned(projectID, "R-042", "H-001", true); err == nil {
		t.Error("expected error: R-042 matches nothing under the root")
	}

	// Unpinning a card whose file has been deleted still works: pin H-001 in
	// R-001, delete the card file, then unpin.
	if err := f.SetHypothesisPinned(projectID, "R-001", "H-001", true); err != nil {
		t.Fatalf("re-pin H-001 in R-001: %v", err)
	}
	if err := os.Remove(filepath.Join(researchRoot, "R-001-test", "hypotheses", "H-001.md")); err != nil {
		t.Fatalf("remove card file: %v", err)
	}
	if err := f.SetHypothesisPinned(projectID, "R-001", "H-001", false); err != nil {
		t.Errorf("unpin of a deleted card must work, got: %v", err)
	}
	if pins := researchPinsOf(t, f, projectID); len(pins.Hypotheses) != 0 {
		t.Errorf("pins = %v, want empty after unpinning the deleted card", pins.Hypotheses)
	}
}

// TestResearchRPC_GetResearchNextStepHypothesisScoped pins the hypothesis-
// scoped next-step contract: an empty hypothesisID yields the project-level
// recommendation; a terminal hypothesis without a Decision recommends
// research-decision on IT (even though the project-level recommendation is an
// experiment on the active front); an unknown hypothesis — or one that
// already carries a Decision — falls back to the project level.
func TestResearchRPC_GetResearchNextStepHypothesisScoped(t *testing.T) {
	f, projectID, _, _ := researchRootTestFrontend(t, []string{"R-001-test"}, project.ResearchPins{})

	// Move H-001 open → in-progress → confirmed (terminal, no Decision), then
	// add a fresh open H-002: the project-level recommendation is an
	// experiment on the active front's H-002, while H-001 scoped recommends
	// deciding on it.
	inProgress := "in-progress"
	if _, err := f.UpdateHypothesis(projectID, "R-001", "H-001", HypothesisUpdateFields{Status: &inProgress}); err != nil {
		t.Fatalf("move H-001 to in-progress: %v", err)
	}
	confirmed := "confirmed"
	if _, err := f.UpdateHypothesis(projectID, "R-001", "H-001", HypothesisUpdateFields{Status: &confirmed}); err != nil {
		t.Fatalf("move H-001 to confirmed: %v", err)
	}
	if _, err := f.CreateHypothesis(projectID, NewHypothesisCard{Title: "Follow-up"}); err != nil {
		t.Fatalf("create H-002: %v", err)
	}

	// Project level (empty hypothesis id).
	dto, err := f.GetResearchNextStep(projectID, "")
	if err != nil {
		t.Fatalf("GetResearchNextStep(project): %v", err)
	}
	if dto.Action != "research-experiment" || dto.Target != "H-002" {
		t.Errorf("project-level = %s/%s, want research-experiment on H-002", dto.Action, dto.Target)
	}

	// Hypothesis-scoped: terminal without Decision → decide on it.
	dto, err = f.GetResearchNextStep(projectID, "H-001")
	if err != nil {
		t.Fatalf("GetResearchNextStep(H-001): %v", err)
	}
	if dto.Action != "research-decision" || dto.Target != "H-001" {
		t.Errorf("H-001-scoped = %s/%s, want research-decision on H-001", dto.Action, dto.Target)
	}

	// Unknown hypothesis falls back to the project level.
	dto, err = f.GetResearchNextStep(projectID, "H-999")
	if err != nil {
		t.Fatalf("GetResearchNextStep(H-999): %v", err)
	}
	if dto.Action != "research-experiment" || dto.Target != "H-002" {
		t.Errorf("unknown-hypothesis fallback = %s/%s, want research-experiment on H-002", dto.Action, dto.Target)
	}

	// A recorded Decision makes the scoped call fall back too.
	decision := "kill"
	if _, err := f.UpdateHypothesis(projectID, "R-001", "H-001", HypothesisUpdateFields{Decision: &decision}); err != nil {
		t.Fatalf("record H-001 decision: %v", err)
	}
	dto, err = f.GetResearchNextStep(projectID, "H-001")
	if err != nil {
		t.Fatalf("GetResearchNextStep(H-001) after decision: %v", err)
	}
	if dto.Action != "research-experiment" || dto.Target != "H-002" {
		t.Errorf("decided-hypothesis fallback = %s/%s, want research-experiment on H-002", dto.Action, dto.Target)
	}
}

// TestResearchRPC_StatusAndEnableCarryPins verifies the ResearchStatusDTO pin
// fields: GetResearchStatus mirrors the persisted pins, an idempotent
// EnableResearch preserves and reports them, and a project without pins gets
// the normalized empty collections (never null).
func TestResearchRPC_StatusAndEnableCarryPins(t *testing.T) {
	pins := project.ResearchPins{
		Research:   []string{"R-001-test/brief.md"},
		Hypotheses: map[string][]string{"H-001": {"R-001-test/hypotheses/H-001.md"}},
	}
	f, projectID, researchRoot, _ := researchRootTestFrontend(t, []string{"R-001-test"}, pins)

	status, err := f.GetResearchStatus(projectID)
	if err != nil {
		t.Fatalf("GetResearchStatus: %v", err)
	}
	if !reflect.DeepEqual(status.PinnedResearch, pins.Research) {
		t.Errorf("PinnedResearch = %v, want %v", status.PinnedResearch, pins.Research)
	}
	if !reflect.DeepEqual(status.PinnedHypotheses, pins.Hypotheses) {
		t.Errorf("PinnedHypotheses = %v, want %v", status.PinnedHypotheses, pins.Hypotheses)
	}

	// Idempotent re-enable preserves and reports the pins.
	status, err = f.EnableResearch(projectID, "")
	if err != nil {
		t.Fatalf("EnableResearch: %v", err)
	}
	if !reflect.DeepEqual(status.PinnedResearch, pins.Research) {
		t.Errorf("EnableResearch PinnedResearch = %v, want %v", status.PinnedResearch, pins.Research)
	}
	if !reflect.DeepEqual(status.PinnedHypotheses, pins.Hypotheses) {
		t.Errorf("EnableResearch PinnedHypotheses = %v, want %v", status.PinnedHypotheses, pins.Hypotheses)
	}

	// A pin-less project reports the normalized empty collections.
	f2, projectID2, _, _ := researchRootTestFrontend(t, []string{"R-001-test"}, project.ResearchPins{})
	status2, err := f2.GetResearchStatus(projectID2)
	if err != nil {
		t.Fatalf("GetResearchStatus (no pins): %v", err)
	}
	if status2.PinnedResearch == nil || len(status2.PinnedResearch) != 0 {
		t.Errorf("PinnedResearch = %v, want a non-nil empty slice", status2.PinnedResearch)
	}
	if status2.PinnedHypotheses == nil || len(status2.PinnedHypotheses) != 0 {
		t.Errorf("PinnedHypotheses = %v, want a non-nil empty map", status2.PinnedHypotheses)
	}
	// researchRoot is referenced by the first harness; keep both workspaces
	// distinct so the assertions above hold for their own roots.
	_ = researchRoot
}
