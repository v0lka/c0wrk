package core

import (
	"context"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// descriptorSet converts a descriptor list to a name set for membership checks.
func descriptorSet(ds []sdktools.ToolDescriptor) map[string]bool {
	out := make(map[string]bool, len(ds))
	for _, d := range ds {
		out[d.Name] = true
	}
	return out
}

// TestVerifierToolFilter verifies the verifier toolset is exactly: read-only
// tools + the shell tool + all MCP tools + declare_verification, with every
// mutating tool and every goal-control tool hard-excluded (acceptance: "excludes
// every mutating tool and every goal-control tool; includes read-only +
// bash_exec/posh_exec + all MCP tools").
func TestVerifierToolFilter(t *testing.T) {
	descs := []sdktools.ToolDescriptor{
		// Read-only / meta — must be included.
		{Name: "read_file"}, {Name: "glob"}, {Name: "ripgrep"}, {Name: "finish"},
		{Name: "store_fact"}, {Name: "search_facts"}, {Name: "ask_user"},
		// Shell tool — must be included (re-run the verify clause).
		{Name: activeShellToolName()},
		// MCP tool — must be included regardless of name.
		{Name: "mcp_linter", SourceCategory: sdktools.SourceCategoryMCP},
		// Verdict channel — must be included (the verifier's only output).
		{Name: "declare_verification"},
		// Mutating tools — must be EXCLUDED.
		{Name: "write_file"}, {Name: "edit_file"},
		{Name: "delete_file"}, {Name: "delete_directory"}, {Name: "create_directory"},
		// Goal-control / coordination tools — must be EXCLUDED.
		{Name: "declare_goal_status"}, {Name: "declare_plan"}, {Name: "delegate"},
		{Name: "subagent"}, {Name: "propose_goal"}, {Name: "reflect"},
		{Name: "cancel_delegation"},
		// declare_step_complete is in subagentReadOnlyToolNames but MUST be
		// excluded here (the verifier doesn't run plan steps).
		{Name: "declare_step_complete"},
		// An arbitrary non-included tool — excluded by the include criteria.
		{Name: "some_random_mutation_tool"},
	}

	got := verifierToolFilter(descs, nil)
	set := descriptorSet(got)

	for _, want := range []string{"read_file", "glob", "ripgrep", "finish", "store_fact", "search_facts", "ask_user", activeShellToolName(), "mcp_linter", "declare_verification"} {
		if !set[want] {
			t.Errorf("verifierToolFilter: expected %q INCLUDED, got %v", want, set)
		}
	}

	for _, unwanted := range []string{
		"write_file", "edit_file", "delete_file", "delete_directory", "create_directory",
		"declare_goal_status", "declare_plan", "delegate", "subagent", "propose_goal",
		"reflect", "cancel_delegation", "declare_step_complete", "some_random_mutation_tool",
	} {
		if set[unwanted] {
			t.Errorf("verifierToolFilter: expected %q EXCLUDED, got %v", unwanted, set)
		}
	}
}

// TestVerifierToolFilter_DeclareStepCompleteExcludedEvenThoughReadOnly is a
// focused regression: declare_step_complete lives in subagentReadOnlyToolNames
// (an include criterion) but the verifier must NEVER have it. The hard-exclude
// set must win over the include criteria.
func TestVerifierToolFilter_DeclareStepCompleteExcludedEvenThoughReadOnly(t *testing.T) {
	_, isReadOnly := subagentReadOnlyToolNames["declare_step_complete"]
	if !isReadOnly {
		t.Fatal("precondition: declare_step_complete should be in subagentReadOnlyToolNames for this regression to be meaningful")
	}
	got := verifierToolFilter([]sdktools.ToolDescriptor{{Name: "declare_step_complete"}}, nil)
	if len(got) != 0 {
		t.Errorf("expected declare_step_complete excluded, got %d descriptor(s)", len(got))
	}
}

// TestVerifierToolFilter_DisabledToolsDropped verifies mode-disabled tools are
// dropped even when they match an include criterion.
func TestVerifierToolFilter_DisabledToolsDropped(t *testing.T) {
	descs := []sdktools.ToolDescriptor{
		{Name: "read_file"}, {Name: "glob"}, {Name: activeShellToolName()},
	}
	// read_file and glob disabled (e.g. CHAT / No-Project mode disables glob).
	got := verifierToolFilter(descs, map[string]bool{"read_file": true, "glob": true})
	set := descriptorSet(got)
	if len(got) != 1 {
		t.Fatalf("expected only the shell tool to survive, got %d (%v)", len(got), set)
	}
	if !set[activeShellToolName()] {
		t.Errorf("expected shell tool %q present", activeShellToolName())
	}
}

// TestVerifierToolFilter_DeclareVerificationAlwaysIncluded verifies the verdict
// channel is present even when the input list provides it standalone.
func TestVerifierToolFilter_DeclareVerificationAlwaysIncluded(t *testing.T) {
	got := verifierToolFilter([]sdktools.ToolDescriptor{
		{Name: "declare_verification"},
		{Name: "write_file"}, // excluded
	}, nil)
	set := descriptorSet(got)
	if !set["declare_verification"] {
		t.Error("declare_verification must be included — it is the verifier's verdict channel")
	}
	if set["write_file"] {
		t.Error("write_file must be excluded")
	}
}

// TestVerifierToolFilter_DeduplicatesByName verifies duplicate tool names
// collapse to a single entry (matches mandatorySubagentTools semantics).
func TestVerifierToolFilter_DeduplicatesByName(t *testing.T) {
	got := verifierToolFilter([]sdktools.ToolDescriptor{
		{Name: "read_file"}, {Name: "read_file"}, {Name: "finish"},
	}, nil)
	if len(got) != 2 {
		t.Errorf("expected dedup to 2 tools, got %d", len(got))
	}
}

// TestRenderReportedEvidence verifies the agent's self-reported verdict is
// rendered as UNVERIFIED claims for the directive placeholder.
func TestRenderReportedEvidence(t *testing.T) {
	t.Run("nil verdict yields a placeholder note", func(t *testing.T) {
		s := renderReportedEvidence(nil)
		if s == "" {
			t.Fatal("expected non-empty placeholder for nil verdict")
		}
		if !strings.Contains(s, "no self-evaluation") {
			t.Errorf("nil verdict note should flag the absence; got %q", s)
		}
	})

	t.Run("verdict with evidence lists claims as unverified", func(t *testing.T) {
		v := &goal.Verdict{
			Status: "met",
			Reason: "tests pass",
			Evidence: []goal.GoalEvidence{
				{Type: "test_output", Ref: "go test ./...", Summary: "all green"},
				{Type: "file", Ref: "core/conductor.go", Summary: "added the override"},
			},
		}
		s := renderReportedEvidence(v)
		if !strings.Contains(s, "tests pass") {
			t.Errorf("expected the agent's reason in output; got %q", s)
		}
		if !strings.Contains(s, "UNVERIFIED") {
			t.Errorf("evidence must be flagged as UNVERIFIED claims; got %q", s)
		}
		if !strings.Contains(s, "go test ./...") || !strings.Contains(s, "core/conductor.go") {
			t.Errorf("expected both evidence refs in output; got %q", s)
		}
	})

	t.Run("verdict with no evidence flags the absence", func(t *testing.T) {
		v := &goal.Verdict{Status: "met", Reason: "trust me"}
		s := renderReportedEvidence(v)
		if !strings.Contains(s, "NO concrete evidence") {
			t.Errorf("expected an explicit no-evidence note; got %q", s)
		}
	})
}

// TestResolveGoalVerifier_Default verifies a nil goalVerifier field resolves to
// the production defaultGoalVerifier (the nil→default seam contract).
func TestResolveGoalVerifier_Default(t *testing.T) {
	o := &Orchestrator{}
	got := o.resolveGoalVerifier()
	if got == nil {
		t.Fatal("nil goalVerifier should resolve to a non-nil defaultGoalVerifier")
	}
}

// TestResolveGoalVerifier_Injectable verifies the seam is injectable for tests
// (mirrors the goalTurnRunner injection pattern: set the field, get it back).
// This is the acceptance criterion: "The goalVerifier seam is injectable for
// tests (mirrors goalTurnRunner)."
func TestResolveGoalVerifier_Injectable(t *testing.T) {
	o := &Orchestrator{}

	called := false
	injected := func(_ context.Context, _ *goal.GoalState, _ *goal.Verdict, _ string, _ orchestration.Blackboard, _ []sdktools.ToolDescriptor, _ conductorDeps) (*tools.VerificationOutcome, error) {
		called = true
		return &tools.VerificationOutcome{Confirmed: true, Reason: "mock verifier"}, nil
	}
	o.goalVerifier = injected

	got := o.resolveGoalVerifier()
	// The injected verifier is returned verbatim and is callable end-to-end.
	outcome, err := got(context.Background(), &goal.GoalState{}, &goal.Verdict{}, "msg", orchestration.NewMapBlackboard(), nil, conductorDeps{})
	if err != nil {
		t.Fatalf("injected verifier returned error: %v", err)
	}
	if outcome == nil || !outcome.Confirmed || outcome.Reason != "mock verifier" {
		t.Errorf("injected verifier not honored: %+v", outcome)
	}
	if !called {
		t.Error("injected verifier was not invoked")
	}
}

// TestVerifierExcludedToolNames_ContainsRequiredMutatingAndControlTools is a
// completeness guard: every mutating tool and every goal-control tool named in
// the spec MUST appear in the exclude set, so none can ever leak into the
// verifier's toolset by accident.
func TestVerifierExcludedToolNames_ContainsRequiredMutatingAndControlTools(t *testing.T) {
	required := []string{
		// Mutating
		"write_file", "edit_file", "delete_file", "delete_directory", "create_directory",
		// Goal-control
		"declare_goal_status", "declare_plan", "delegate", "subagent",
		"propose_goal", "reflect", "declare_step_complete", "cancel_delegation",
	}
	for _, name := range required {
		if _, ok := verifierExcludedToolNames[name]; !ok {
			t.Errorf("verifierExcludedToolNames missing required entry %q", name)
		}
	}
}
