package core

import (
	"context"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/research"
	"github.com/v0lka/c0wrk/core/tools"
)

// testdataResearchRoot returns the path to the shared research test fixture
// (relative to the core package test working directory).
func testdataResearchRoot(t *testing.T) string {
	t.Helper()
	return "research/testdata"
}

// TestBuildResearchContextSnapshot_Testdata parses the shared research testdata
// root and verifies the snapshot derives the active R-NNN (the latest index
// entry), project count, root path, and a phase hint.
func TestBuildResearchContextSnapshot_Testdata(t *testing.T) {
	root, err := research.ParseResearchRoot(testdataResearchRoot(t))
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}

	rc := buildResearchContextSnapshot("/ws/.research", root)

	if rc.RootPath != "/ws/.research" {
		t.Errorf("RootPath: got %q, want /ws/.research", rc.RootPath)
	}
	if rc.ProjectCount != 2 {
		t.Errorf("ProjectCount: got %d, want 2", rc.ProjectCount)
	}
	// The latest index entry is R-002 (the Empty Scaffold).
	if rc.ActiveID != "R-002" {
		t.Errorf("ActiveID: got %q, want R-002", rc.ActiveID)
	}
	if rc.PhaseHint == "" {
		t.Error("PhaseHint should not be empty")
	}
}

// TestBuildResearchContextSnapshot_NoProjects verifies a root with no projects
// reports "none yet" semantics and a setup phase.
func TestBuildResearchContextSnapshot_NoProjects(t *testing.T) {
	root := &research.ResearchRoot{Path: "/ws/.research"}
	rc := buildResearchContextSnapshot("/ws/.research", root)

	if rc.ActiveID != "" {
		t.Errorf("ActiveID: got %q, want empty", rc.ActiveID)
	}
	if rc.TotalHypotheses != 0 {
		t.Errorf("TotalHypotheses: got %d, want 0", rc.TotalHypotheses)
	}
	if rc.PhaseHint == "" {
		t.Error("PhaseHint should not be empty for an empty root")
	}
}

// TestPickActiveProject_IndexLast verifies the active project is the
// latest index entry. The selection logic now lives in the research package
// (research.PickActiveProject); this test exercises it via the parsed root.
func TestPickActiveProject_IndexLast(t *testing.T) {
	root, err := research.ParseResearchRoot(testdataResearchRoot(t))
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	active := research.PickActiveProject(root)
	if active == nil {
		t.Fatal("expected non-nil active project")
	}
	if active.ID != "R-002" {
		t.Errorf("active ID: got %q, want R-002", active.ID)
	}
	// The parsed root should also carry the precomputed ActiveProjectID.
	if root.ActiveProjectID != "R-002" {
		t.Errorf("ActiveProjectID: got %q, want R-002", root.ActiveProjectID)
	}
}

// TestPickActiveProject_NilSafe verifies nil inputs do not panic.
func TestPickActiveProject_NilSafe(t *testing.T) {
	if got := research.PickActiveProject(nil); got != nil {
		t.Errorf("nil root: expected nil, got %+v", got)
	}
}

// TestResearchPhaseHint_Setup verifies an empty graph yields a setup phase.
func TestResearchPhaseHint_Setup(t *testing.T) {
	p := &research.ResearchProject{ID: "R-001"}
	got := researchPhaseHint(p)
	if got == "" {
		t.Error("expected non-empty setup phase hint")
	}
}

// TestResearchPhaseHint_Experimenting parses R-001 (which has open/in-progress
// hypotheses) and verifies the phase is "experimenting".
func TestResearchPhaseHint_Experimenting(t *testing.T) {
	p, err := research.ParseProject("research/testdata/R-001-web-exfil")
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	got := researchPhaseHint(p)
	if got == "" {
		t.Fatal("expected non-empty phase hint")
	}
	// R-001 has open/in-progress hypotheses → active front → experimenting.
	if !strings.Contains(got, "experimenting") {
		t.Errorf("expected experimenting phase for R-001, got %q", got)
	}
	if p.Metrics.Total == 0 {
		t.Fatal("R-001 should have hypotheses for this test")
	}
	if p.Metrics.ActiveFront == nil {
		t.Fatal("R-001 should have an active front")
	}
}

// TestResearchPhaseHint_Concluding verifies an all-decided graph with
// confirmations yields a concluding phase.
func TestResearchPhaseHint_Concluding(t *testing.T) {
	g := research.HypothesisGraph{}
	g.Nodes = append(g.Nodes,
		&research.HypothesisNode{ID: "H-001", Status: research.StatusConfirmed},
		&research.HypothesisNode{ID: "H-002", Status: research.StatusRefuted},
	)
	m := research.ComputeMetrics(&g)
	p := &research.ResearchProject{ID: "R-001", Graph: g, Metrics: m}
	got := researchPhaseHint(p)
	if !strings.Contains(got, "concluding") {
		t.Errorf("expected concluding phase, got %q", got)
	}
}

// TestResearchPhaseHint_Falsified verifies an all-refuted graph yields a
// falsified phase.
func TestResearchPhaseHint_Falsified(t *testing.T) {
	g := research.HypothesisGraph{}
	g.Nodes = append(g.Nodes,
		&research.HypothesisNode{ID: "H-001", Status: research.StatusRefuted},
		&research.HypothesisNode{ID: "H-002", Status: research.StatusRefuted},
	)
	m := research.ComputeMetrics(&g)
	p := &research.ResearchProject{ID: "R-001", Graph: g, Metrics: m}
	got := researchPhaseHint(p)
	if !strings.Contains(got, "falsified") {
		t.Errorf("expected falsified phase, got %q", got)
	}
}

// TestInjectResearchContext_NoOpOutsideResearchMode verifies the method is a
// no-op (returns ctx unchanged, no snapshot attached) when IsResearch is false.
func TestInjectResearchContext_NoOpOutsideResearchMode(t *testing.T) {
	o := &Orchestrator{}
	ctx := context.Background()
	out := o.injectResearchContext(ctx)
	if ResearchContextFromContext(out) != nil {
		t.Error("expected no research context outside RESEARCH mode")
	}
}

// TestInjectResearchContext_InjectsSnapshot verifies that with IsResearch set
// and a valid root path, the snapshot is attached to the context.
func TestInjectResearchContext_InjectsSnapshot(t *testing.T) {
	o := &Orchestrator{}
	ctx := tools.WithResearch(context.Background())
	ctx = tools.WithResearchRoot(ctx, testdataResearchRoot(t))

	out := o.injectResearchContext(ctx)
	rc := ResearchContextFromContext(out)
	if rc == nil {
		t.Fatal("expected research context snapshot after injection")
	}
	if rc.RootPath != testdataResearchRoot(t) {
		t.Errorf("RootPath: got %q", rc.RootPath)
	}
	if rc.ActiveID == "" {
		t.Error("expected a non-empty active R-NNN")
	}
}

// TestInjectResearchContext_BadPathIsNoOp verifies an unreadable root path is
// handled gracefully (no snapshot, no panic).
func TestInjectResearchContext_BadPathIsNoOp(t *testing.T) {
	o := &Orchestrator{}
	ctx := tools.WithResearch(context.Background())
	ctx = tools.WithResearchRoot(ctx, "research/testdata/does-not-exist")

	out := o.injectResearchContext(ctx)
	if ResearchContextFromContext(out) != nil {
		t.Error("expected no research context for an unreadable root")
	}
}
