package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/research"
)

// researchTwoProjectTestFrontend wires a FrontendAPI with TWO research-enabled
// projects (A: R-001-test, B: R-009-other — each root carrying its own open
// H-001 card) so a cross-project R-NNN mix-up is representable: projB's root
// does contain an H-001 card, but only inside R-009.
func researchTwoProjectTestFrontend(t *testing.T) (api *FrontendAPI, projA, projB, rootA, rootB string) {
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

	mgr := project.NewManager(store, base, nil)
	api = &FrontendAPI{
		projectManager: mgr,
		config: &config.Config{
			Experimental: config.ExperimentalConfig{Enabled: true},
		},
		emitEvent: func(_ string, _ ...any) {},
	}
	return api, "proj-a", "proj-b", rootA, rootB
}

// TestResearchRPC_ConcurrentCreateNoLostUpdate runs many concurrent
// CreateHypothesis RPCs against one research root. Serialized by the per-root
// mutation mutex, every call must observe the previous one's max H-NNN: no
// duplicate ids, no lost card files, no lost graph rows. Run under -race this
// also proves the mutex keeps the load→mutate→write chain data-race-free.
func TestResearchRPC_ConcurrentCreateNoLostUpdate(t *testing.T) {
	f, projectID, researchRoot := researchMutationTestFrontend(t)

	const creators = 16
	var wg sync.WaitGroup
	errs := make([]error, creators)
	for i := 0; i < creators; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.CreateHypothesis(projectID, NewHypothesisCard{
				Title: fmt.Sprintf("Concurrent hypothesis %d", i),
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("CreateHypothesis %d: %v", i, err)
		}
	}

	projectDir, err := research.ActiveProjectDir(researchRoot)
	if err != nil {
		t.Fatalf("ActiveProjectDir: %v", err)
	}
	proj, err := research.ParseProject(projectDir)
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}

	// Seeded H-001 + 16 created = 17 distinct hypotheses; a duplicate max+1
	// id or a lost card write would leave fewer (or duplicated) nodes.
	const want = 1 + creators
	if len(proj.Graph.Nodes) != want {
		t.Fatalf("graph nodes = %d, want %d (lost update)", len(proj.Graph.Nodes), want)
	}
	seen := make(map[string]struct{}, want)
	for _, n := range proj.Graph.Nodes {
		if _, dup := seen[n.ID]; dup {
			t.Errorf("duplicate hypothesis id %s after concurrent creates", n.ID)
		}
		seen[n.ID] = struct{}{}
	}
	for i := 1; i <= want; i++ {
		id := fmt.Sprintf("H-%03d", i)
		if _, ok := seen[id]; !ok {
			t.Errorf("hypothesis %s missing from graph", id)
		}
		if _, err := os.Stat(filepath.Join(projectDir, "hypotheses", id+".md")); err != nil {
			t.Errorf("card file %s.md missing: %v", id, err)
		}
	}

	// The catalog must carry one row per hypothesis — two creators racing the
	// same graph.md snapshot would drop one of the rows.
	graphContent, err := os.ReadFile(filepath.Join(projectDir, "hypotheses", "graph.md"))
	if err != nil {
		t.Fatalf("read graph.md: %v", err)
	}
	if rows := len(research.ParseCatalog(string(graphContent))); rows != want {
		t.Errorf("catalog rows = %d, want %d (lost graph row)", rows, want)
	}
}

// mermaidClassRe extracts the Mermaid CSS class of the H-001 node from a
// graph.md (e.g. `H001["H-001: …"]:::open` → "open").
var mermaidClassRe = regexp.MustCompile(`H001\[[^\]]*\]:::(\w+)`)

// TestResearchRPC_ConcurrentUpdateNoLostUpdate hammers one card with
// concurrent status updates. Serialized by the per-root mutation mutex, each
// writer validates its transition against the fresh card state; the final
// state must be a legal outcome with card and graph in agreement — never a
// torn card-vs-graph write from interleaved read-modify-write cycles.
func TestResearchRPC_ConcurrentUpdateNoLostUpdate(t *testing.T) {
	f, projectID, researchRoot := researchMutationTestFrontend(t)

	const writers = 8
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Half move H-001 open→in-progress, half open→cancelled. Losers
			// whose transition became illegal against the fresh state are
			// expected to error; the final state must still be consistent.
			status := "in-progress"
			if i%2 == 1 {
				status = "cancelled"
			}
			_, _ = f.UpdateHypothesis(projectID, "R-001", "H-001", HypothesisUpdateFields{Status: &status})
		}(i)
	}
	wg.Wait()

	projectDir, err := research.ActiveProjectDir(researchRoot)
	if err != nil {
		t.Fatalf("ActiveProjectDir: %v", err)
	}
	proj, err := research.ParseProject(projectDir)
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	n := proj.Graph.Node("H-001")
	if n == nil {
		t.Fatal("H-001 missing after concurrent updates")
	}
	if n.Status != research.StatusInProgress && n.Status != research.StatusCancelled {
		t.Fatalf("final status %q is not a legal concurrent outcome", n.Status)
	}

	// Card and graph must agree (no torn write). BuildGraph lets the card
	// override the catalog, so compare the raw artifacts directly.
	cardContent, err := os.ReadFile(filepath.Join(projectDir, "hypotheses", "H-001.md"))
	if err != nil {
		t.Fatalf("read card: %v", err)
	}
	cardNode, err := research.ParseCard(string(cardContent))
	if err != nil {
		t.Fatalf("ParseCard: %v", err)
	}
	graphContent, err := os.ReadFile(filepath.Join(projectDir, "hypotheses", "graph.md"))
	if err != nil {
		t.Fatalf("read graph: %v", err)
	}
	m := mermaidClassRe.FindStringSubmatch(string(graphContent))
	if m == nil {
		t.Fatal("H-001 mermaid node not found in graph.md")
	}
	classStatus := research.NormalizeStatus(m[1]) // maps in_progress → in-progress
	if cardNode.Status != classStatus {
		t.Errorf("torn write: card status %q vs graph class %q", cardNode.Status, classStatus)
	}
}

// TestResearchRPC_UpdateRejectsForeignResearchID verifies the cross-project
// save race guard ([19]b): a research id that belongs to ANOTHER project's
// research root does not resolve under the requesting project's root and is
// rejected before any file is touched — even though the requesting project
// has its own same-id H-001 card, so the rejection demonstrably comes from
// the R-NNN ownership check.
func TestResearchRPC_UpdateRejectsForeignResearchID(t *testing.T) {
	f, projA, projB, rootA, rootB := researchTwoProjectTestFrontend(t)

	status := "in-progress"
	if _, err := f.UpdateHypothesis(projB, "R-001", "H-001", HypothesisUpdateFields{Status: &status}); err == nil {
		t.Fatal("expected error: R-001 belongs to proj-a's research root, not proj-b's")
	}

	// proj-b's own card (H-001 inside R-009) must be untouched.
	dirB, err := research.ProjectDir(rootB, "R-009")
	if err != nil {
		t.Fatalf("ProjectDir R-009: %v", err)
	}
	proj, err := research.ParseProject(dirB)
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	if n := proj.Graph.Node("H-001"); n == nil || n.Status != research.StatusOpen {
		t.Errorf("proj-b H-001 mutated by the rejected foreign-R call: %+v", n)
	}

	// Positive controls: each project's own R-NNN still updates fine.
	if _, err := f.UpdateHypothesis(projB, "R-009", "H-001", HypothesisUpdateFields{Status: &status}); err != nil {
		t.Fatalf("UpdateHypothesis with proj-b's own R-009: %v", err)
	}
	if _, err := f.UpdateHypothesis(projA, "R-001", "H-001", HypothesisUpdateFields{Status: &status}); err != nil {
		t.Fatalf("UpdateHypothesis with proj-a's own R-001: %v", err)
	}

	dirA, err := research.ProjectDir(rootA, "R-001")
	if err != nil {
		t.Fatalf("ProjectDir R-001: %v", err)
	}
	projA_, err := research.ParseProject(dirA)
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	if n := projA_.Graph.Node("H-001"); n == nil || n.Status != research.StatusInProgress {
		t.Errorf("proj-a H-001 not updated by its own R-001 call: %+v", n)
	}
}

// TestResearchRPC_UpdateTargetsExpectedResearchProject pins the non-active
// targeting semantics: with a newer R-002 active, a save carrying R-001 must
// mutate R-001's card — not the backend's current active project — and the
// response must describe the mutated project.
func TestResearchRPC_UpdateTargetsExpectedResearchProject(t *testing.T) {
	f, projectID, researchRoot := researchMutationTestFrontend(t)

	// A second, higher-numbered project becomes the active one (no index.md →
	// highest-numbered R-NNN wins) while the caller still edits R-001.
	seedResearchProjectDir(t, researchRoot, "R-002-second")

	active, err := research.ActiveProjectDir(researchRoot)
	if err != nil {
		t.Fatalf("ActiveProjectDir: %v", err)
	}
	if filepath.Base(active) != "R-002-second" {
		t.Fatalf("test setup: active = %q, want R-002-second", filepath.Base(active))
	}

	status := "in-progress"
	dto, err := f.UpdateHypothesis(projectID, "R-001", "H-001", HypothesisUpdateFields{Status: &status})
	if err != nil {
		t.Fatalf("UpdateHypothesis: %v", err)
	}
	if dto.ProjectID != "R-001" {
		t.Errorf("response graph = %q, want R-001 (the mutated project)", dto.ProjectID)
	}

	dir1, err := research.ProjectDir(researchRoot, "R-001")
	if err != nil {
		t.Fatalf("ProjectDir R-001: %v", err)
	}
	p1, err := research.ParseProject(dir1)
	if err != nil {
		t.Fatalf("ParseProject R-001: %v", err)
	}
	if n := p1.Graph.Node("H-001"); n == nil || n.Status != research.StatusInProgress {
		t.Errorf("R-001 H-001 not updated: %+v", n)
	}

	dir2, err := research.ProjectDir(researchRoot, "R-002")
	if err != nil {
		t.Fatalf("ProjectDir R-002: %v", err)
	}
	p2, err := research.ParseProject(dir2)
	if err != nil {
		t.Fatalf("ParseProject R-002: %v", err)
	}
	if n := p2.Graph.Node("H-001"); n == nil || n.Status != research.StatusOpen {
		t.Errorf("active R-002 mutated by an R-001 save: %+v", n)
	}
}

// TestResearchRPC_UpdateRejectsInvalidResearchID verifies the fail-closed
// shape check: an empty or non-R-NNN research id is rejected before any
// filesystem work.
func TestResearchRPC_UpdateRejectsInvalidResearchID(t *testing.T) {
	f, projectID, _ := researchMutationTestFrontend(t)

	if _, err := f.UpdateHypothesis(projectID, "", "H-001", HypothesisUpdateFields{}); err == nil {
		t.Error("expected error for empty research id")
	}
	if _, err := f.UpdateHypothesis(projectID, "not-a-rid", "H-001", HypothesisUpdateFields{}); err == nil {
		t.Error("expected error for non-R-NNN research id")
	}
}
