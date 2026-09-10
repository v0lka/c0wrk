package research

import "testing"

func TestRecommendNextStep_NoProject(t *testing.T) {
	rec := RecommendNextStep(nil)
	if rec.Action != ActionInit {
		t.Errorf("Action = %q, want %q", rec.Action, ActionInit)
	}
	if rec.Skill != "research-init" {
		t.Errorf("Skill = %q, want research-init", rec.Skill)
	}
	if rec.Target != "" {
		t.Errorf("Target = %q, want empty", rec.Target)
	}
	if rec.Reason == "" {
		t.Error("Reason should be non-empty")
	}
}

func TestRecommendNextStep_NoHypotheses(t *testing.T) {
	proj := &ResearchProject{
		ID:    "R-001",
		Brief: Brief{ID: "R-001", Title: "Empty Scaffold"},
		Metrics: Metrics{
			Total:       0,
			ByStatus:    make(map[HypothesisStatus]int),
			ActiveFront: nil,
		},
	}
	rec := RecommendNextStep(proj)
	if rec.Action != ActionHypothesize {
		t.Errorf("Action = %q, want %q", rec.Action, ActionHypothesize)
	}
	if rec.Skill != "research-hypothesis" {
		t.Errorf("Skill = %q, want research-hypothesis", rec.Skill)
	}
	if rec.Target != "" {
		t.Errorf("Target = %q, want empty", rec.Target)
	}
	if rec.Reason == "" {
		t.Error("Reason should be non-empty")
	}
}

func TestRecommendNextStep_ActiveFront(t *testing.T) {
	proj := &ResearchProject{
		ID: "R-001",
		Metrics: Metrics{
			Total:       3,
			ByStatus:    map[HypothesisStatus]int{StatusConfirmed: 1, StatusOpen: 1, StatusInProgress: 1},
			ActiveFront: []string{"H-001", "H-003"}, // sorted by ID
		},
	}
	rec := RecommendNextStep(proj)
	if rec.Action != ActionExperiment {
		t.Errorf("Action = %q, want %q", rec.Action, ActionExperiment)
	}
	if rec.Skill != "research-experiment" {
		t.Errorf("Skill = %q, want research-experiment", rec.Skill)
	}
	// The front's first open/in-progress hypothesis (sorted by ID).
	if rec.Target != "H-001" {
		t.Errorf("Target = %q, want H-001", rec.Target)
	}
	if rec.Reason == "" {
		t.Error("Reason should be non-empty")
	}
}

func TestRecommendNextStep_AllTerminal_NoReport(t *testing.T) {
	proj := &ResearchProject{
		ID:        "R-001",
		HasReport: false,
		Metrics: Metrics{
			Total:       2,
			ByStatus:    map[HypothesisStatus]int{StatusConfirmed: 1, StatusRefuted: 1},
			ActiveFront: nil,
		},
	}
	rec := RecommendNextStep(proj)
	if rec.Action != ActionSynthesize {
		t.Errorf("Action = %q, want %q", rec.Action, ActionSynthesize)
	}
	if rec.Skill != "research-synthesis" {
		t.Errorf("Skill = %q, want research-synthesis", rec.Skill)
	}
	if rec.Target != "" {
		t.Errorf("Target = %q, want empty", rec.Target)
	}
	if rec.Reason == "" {
		t.Error("Reason should be non-empty")
	}
}

func TestRecommendNextStep_AllTerminal_WithReport(t *testing.T) {
	proj := &ResearchProject{
		ID:        "R-001",
		HasReport: true,
		Metrics: Metrics{
			Total:       2,
			ByStatus:    map[HypothesisStatus]int{StatusConfirmed: 2},
			ActiveFront: nil,
		},
	}
	rec := RecommendNextStep(proj)
	if rec.Action != ActionDecision {
		t.Errorf("Action = %q, want %q", rec.Action, ActionDecision)
	}
	if rec.Skill != "research-decision" {
		t.Errorf("Skill = %q, want research-decision", rec.Skill)
	}
	if rec.Target != "" {
		t.Errorf("Target = %q, want empty", rec.Target)
	}
	if rec.Reason == "" {
		t.Error("Reason should be non-empty")
	}
}

func TestRecommendNextStepForHypothesis_ActiveHypothesis(t *testing.T) {
	proj := &ResearchProject{
		ID: "R-001",
		Graph: HypothesisGraph{Nodes: []*HypothesisNode{
			{ID: "H-001", Title: "Leader", Status: StatusOpen},
			{ID: "H-003", Title: "Selected", Status: StatusInProgress},
		}},
		Metrics: Metrics{
			Total:       2,
			ByStatus:    map[HypothesisStatus]int{StatusOpen: 1, StatusInProgress: 1},
			ActiveFront: []string{"H-001", "H-003"},
		},
	}
	// H-001 leads the front, but the SELECTED hypothesis is H-003: the
	// recommendation must target it, not the front leader.
	rec := RecommendNextStepForHypothesis(proj, "H-003")
	if rec.Action != ActionExperiment {
		t.Errorf("Action = %q, want %q", rec.Action, ActionExperiment)
	}
	if rec.Skill != "research-experiment" {
		t.Errorf("Skill = %q, want research-experiment", rec.Skill)
	}
	if rec.Target != "H-003" {
		t.Errorf("Target = %q, want H-003", rec.Target)
	}
	if rec.Reason == "" {
		t.Error("Reason should be non-empty")
	}
}

func TestRecommendNextStepForHypothesis_TerminalWithoutDecision(t *testing.T) {
	proj := &ResearchProject{
		ID: "R-001",
		Graph: HypothesisGraph{Nodes: []*HypothesisNode{
			{ID: "H-002", Title: "Verdict in", Status: StatusRefuted},
		}},
		Metrics: Metrics{
			Total:       1,
			ByStatus:    map[HypothesisStatus]int{StatusRefuted: 1},
			ActiveFront: nil,
		},
		HasReport: true,
	}
	rec := RecommendNextStepForHypothesis(proj, "H-002")
	if rec.Action != ActionDecision {
		t.Errorf("Action = %q, want %q", rec.Action, ActionDecision)
	}
	if rec.Skill != "research-decision" {
		t.Errorf("Skill = %q, want research-decision", rec.Skill)
	}
	if rec.Target != "H-002" {
		t.Errorf("Target = %q, want H-002 (decision scoped to the hypothesis)", rec.Target)
	}
	if rec.Reason == "" {
		t.Error("Reason should be non-empty")
	}
}

func TestRecommendNextStepForHypothesis_TerminalWithDecisionFallsBack(t *testing.T) {
	proj := &ResearchProject{
		ID: "R-001",
		Graph: HypothesisGraph{Nodes: []*HypothesisNode{
			{ID: "H-002", Title: "Decided", Status: StatusConfirmed, Decision: "continue"},
			{ID: "H-005", Title: "Front", Status: StatusOpen},
		}},
		Metrics: Metrics{
			Total:       2,
			ByStatus:    map[HypothesisStatus]int{StatusConfirmed: 1, StatusOpen: 1},
			ActiveFront: []string{"H-005"},
		},
	}
	// H-002 is terminal AND decided: the recommendation falls back to the
	// plain project-wide one (experiment on the front's leading hypothesis).
	rec := RecommendNextStepForHypothesis(proj, "H-002")
	if rec.Action != ActionExperiment {
		t.Errorf("Action = %q, want %q (plain fallback)", rec.Action, ActionExperiment)
	}
	if rec.Target != "H-005" {
		t.Errorf("Target = %q, want H-005 (the front leader, not the selected hypothesis)", rec.Target)
	}
}

func TestRecommendNextStepForHypothesis_UnknownHypothesisFallsBack(t *testing.T) {
	proj := &ResearchProject{
		ID: "R-001",
		Graph: HypothesisGraph{Nodes: []*HypothesisNode{
			{ID: "H-001", Title: "Only", Status: StatusOpen},
		}},
		Metrics: Metrics{
			Total:       1,
			ByStatus:    map[HypothesisStatus]int{StatusOpen: 1},
			ActiveFront: []string{"H-001"},
		},
	}
	rec := RecommendNextStepForHypothesis(proj, "H-099")
	if rec.Action != ActionExperiment {
		t.Errorf("Action = %q, want %q (plain fallback)", rec.Action, ActionExperiment)
	}
	if rec.Target != "H-001" {
		t.Errorf("Target = %q, want H-001", rec.Target)
	}
}

func TestRecommendNextStepForHypothesis_EmptyIDFallsBack(t *testing.T) {
	proj := &ResearchProject{
		ID:      "R-001",
		Metrics: Metrics{ByStatus: map[HypothesisStatus]int{}},
	}
	rec := RecommendNextStepForHypothesis(proj, "")
	if rec.Action != ActionHypothesize {
		t.Errorf("Action = %q, want %q (plain fallback)", rec.Action, ActionHypothesize)
	}
}

func TestRecommendNextStepForHypothesis_NilProject(t *testing.T) {
	rec := RecommendNextStepForHypothesis(nil, "H-001")
	if rec.Action != ActionInit {
		t.Errorf("Action = %q, want %q", rec.Action, ActionInit)
	}
	if rec.Skill != "research-init" {
		t.Errorf("Skill = %q, want research-init", rec.Skill)
	}
}
