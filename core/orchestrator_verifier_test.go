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
	injected := func(_ context.Context, _ *goal.GoalState, _ *goal.Verdict, _, _ string, _ orchestration.Blackboard, _ []sdktools.ToolDescriptor, _ conductorDeps) (*tools.VerificationOutcome, error) {
		called = true
		return &tools.VerificationOutcome{Confirmed: true, Reason: "mock verifier"}, nil
	}
	o.goalVerifier = injected

	got := o.resolveGoalVerifier()
	// The injected verifier is returned verbatim and is callable end-to-end.
	outcome, err := got(context.Background(), &goal.GoalState{}, &goal.Verdict{}, "msg", "", orchestration.NewMapBlackboard(), nil, conductorDeps{})
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

// ----------------------------------------------------------------------------
// Re-derivation mode toolset tests (step_3)
//
// These verify the mode-branching contract: the re_derivation verifier toolset
// is the executable toolset PLUS delegate + read_step_output, while every
// mutating tool and every OTHER goal-control tool remains excluded.
// ----------------------------------------------------------------------------

// verifierFixtureDescriptors is the canonical tool list both modes filter over.
func verifierFixtureDescriptors() []sdktools.ToolDescriptor {
	return []sdktools.ToolDescriptor{
		// Read-only / meta — included in both modes.
		{Name: "read_file"}, {Name: "glob"}, {Name: "ripgrep"}, {Name: "finish"},
		{Name: "store_fact"}, {Name: "search_facts"}, {Name: "ask_user"},
		// Step-output / final-result readers — included in both modes.
		{Name: "read_step_output"}, {Name: "read_final_result"},
		// Shell tool — included in both modes (re-run the verify clause).
		{Name: activeShellToolName()},
		// MCP tool — included in both modes regardless of name.
		{Name: "mcp_linter", SourceCategory: sdktools.SourceCategoryMCP},
		// Verdict channel — included in both modes (the verifier's output).
		{Name: "declare_verification"},
		// delegate — included ONLY in re_derivation mode.
		{Name: "delegate"},
		// Mutating tools — excluded in both modes.
		{Name: "write_file"}, {Name: "edit_file"},
		{Name: "delete_file"}, {Name: "delete_directory"}, {Name: "create_directory"},
		// Goal-control / coordination tools (other than delegate) — excluded in both.
		{Name: "declare_goal_status"}, {Name: "declare_plan"}, {Name: "subagent"},
		{Name: "propose_goal"}, {Name: "reflect"}, {Name: "cancel_delegation"},
		{Name: "declare_step_complete"},
	}
}

// TestVerifierReDerivationToolFilter_AddsDelegateAndStepOutput verifies the
// headline re_derivation acceptance criterion: delegate + read_step_output are
// INCLUDED (re_derivation needs delegate to spin up a fresh read-only run and
// read_step_output to read it), while every mutating tool and every OTHER
// goal-control tool remains excluded.
func TestVerifierReDerivationToolFilter_AddsDelegateAndStepOutput(t *testing.T) {
	got := verifierReDerivationToolFilter(verifierFixtureDescriptors(), nil)
	set := descriptorSet(got)

	for _, want := range []string{"delegate", "read_step_output", "read_final_result"} {
		if !set[want] {
			t.Errorf("re_derivation: expected %q INCLUDED, got %v", want, set)
		}
	}
	// The re_derivation toolset still carries the shared read-only/test/verdict
	// tools so the verifier can corroborate the delegated run's findings.
	for _, want := range []string{"read_file", "glob", "finish", activeShellToolName(), "mcp_linter", "declare_verification"} {
		if !set[want] {
			t.Errorf("re_derivation: expected shared read-only/test tool %q INCLUDED, got %v", want, set)
		}
	}

	// Every mutating tool excluded — the verifier NEVER edits state in either mode.
	for _, unwanted := range []string{
		"write_file", "edit_file", "delete_file", "delete_directory", "create_directory",
	} {
		if set[unwanted] {
			t.Errorf("re_derivation: mutating tool %q must be EXCLUDED", unwanted)
		}
	}
	// Every OTHER goal-control tool excluded — only delegate is granted.
	for _, unwanted := range []string{
		"declare_goal_status", "declare_plan", "subagent", "propose_goal",
		"reflect", "cancel_delegation", "declare_step_complete",
	} {
		if set[unwanted] {
			t.Errorf("re_derivation: goal-control tool %q must be EXCLUDED (only delegate is granted)", unwanted)
		}
	}
}

// TestVerifierToolFilter_ExecutableExcludesDelegate verifies the executable
// (default) mode does NOT grant delegate — the mode-branching delta.
func TestVerifierToolFilter_ExecutableExcludesDelegate(t *testing.T) {
	got := verifierToolFilter(verifierFixtureDescriptors(), nil)
	set := descriptorSet(got)
	if set["delegate"] {
		t.Error("executable mode: delegate must be EXCLUDED (it is granted only in re_derivation mode)")
	}
	// read_step_output is still present (it is a read-only tool, unrelated to
	// the delegate grant).
	if !set["read_step_output"] {
		t.Error("executable mode: read_step_output should still be present (it is a read-only tool)")
	}
}

// TestVerifierReDerivationToolFilter_DelegateDisabledByMode verifies that a
// delegate tool disabled in the current mode (disabledTools) is dropped even in
// re_derivation — the grant is subject to the same mode-disable gate as every
// other tool.
func TestVerifierReDerivationToolFilter_DelegateDisabledByMode(t *testing.T) {
	got := verifierReDerivationToolFilter(verifierFixtureDescriptors(), map[string]bool{"delegate": true})
	set := descriptorSet(got)
	if set["delegate"] {
		t.Error("re_derivation: a mode-disabled delegate must still be dropped")
	}
	// Other shared tools survive.
	if !set["read_file"] {
		t.Error("re_derivation: read_file should survive when only delegate is disabled")
	}
}

// TestVerifierReDerivationExcludedToolNames_OmitsDelegateOnly is a
// completeness guard on the re_derivation exclusion set: it is the executable
// exclusion set MINUS delegate (the one coordination tool granted in that mode),
// and every mutating tool + every other goal-control tool is still present.
func TestVerifierReDerivationExcludedToolNames_OmitsDelegateOnly(t *testing.T) {
	// delegate is NOT in the re_derivation exclusion set (it is granted).
	if _, present := verifierReDerivationExcludedToolNames["delegate"]; present {
		t.Error("re_derivation exclusion set must NOT contain delegate (it is granted in that mode)")
	}
	// Every mutating tool excluded.
	for _, name := range []string{"write_file", "edit_file", "delete_file", "delete_directory", "create_directory"} {
		if _, ok := verifierReDerivationExcludedToolNames[name]; !ok {
			t.Errorf("re_derivation exclusion set missing mutating tool %q", name)
		}
	}
	// Every OTHER goal-control tool excluded (the full executable set minus delegate).
	for _, name := range []string{"declare_goal_status", "declare_plan", "subagent", "propose_goal", "reflect", "cancel_delegation", "declare_step_complete"} {
		if _, ok := verifierReDerivationExcludedToolNames[name]; !ok {
			t.Errorf("re_derivation exclusion set missing goal-control tool %q", name)
		}
	}
}
