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

// TestVerifierToolFilter verifies the verifier toolset is drawn from the
// include GROUPS (system + local_read + remote_read + execute + local_mcp +
// remote_mcp), with every mutating tool (local_write/remote_write groups) and
// every goal-control tool hard-excluded (acceptance: "verifier include =
// system+local_read+remote_read+execute+local_mcp+remote_mcp
// (+declare_verification); hard-exclusion of mutating ones is preserved").
func TestVerifierToolFilter(t *testing.T) {
	descs := []sdktools.ToolDescriptor{
		// local_read — included.
		{Name: "read_file", Group: sdktools.GroupLocalRead},
		{Name: "glob", Group: sdktools.GroupLocalRead},
		{Name: "ripgrep", Group: sdktools.GroupLocalRead},
		// remote_read — included.
		{Name: "web_fetch", Group: sdktools.GroupRemoteRead},
		// system — included (meta tools).
		{Name: "finish", Group: sdktools.GroupSystem},
		{Name: "store_fact", Group: sdktools.GroupSystem},
		{Name: "search_facts", Group: sdktools.GroupSystem},
		{Name: "ask_user", Group: sdktools.GroupSystem},
		{Name: "read_step_output", Group: sdktools.GroupSystem},
		{Name: "batch", Group: sdktools.GroupSystem},
		{Name: "semantic_search", Group: sdktools.GroupSystem},
		// execute — included (re-run the verify clause).
		{Name: activeShellToolName(), Group: sdktools.GroupExecute},
		// MCP groups — included regardless of tool name.
		{Name: "mcp_linter", Group: sdktools.GroupLocalMCP, SourceCategory: sdktools.SourceCategoryMCP},
		{Name: "mcp_remote", Group: sdktools.GroupRemoteMCP, SourceCategory: sdktools.SourceCategoryMCP},
		// Verdict channel — included (the verifier's only output).
		{Name: "declare_verification", Group: sdktools.GroupSystem},
		// local_write / remote_write — EXCLUDED by group.
		{Name: "write_file", Group: sdktools.GroupLocalWrite},
		{Name: "edit_file", Group: sdktools.GroupLocalWrite},
		{Name: "delete_file", Group: sdktools.GroupLocalWrite},
		{Name: "delete_directory", Group: sdktools.GroupLocalWrite},
		{Name: "create_directory", Group: sdktools.GroupLocalWrite},
		{Name: "deploy_hook", Group: sdktools.GroupRemoteWrite},
		// Goal-control / coordination tools — EXCLUDED by name even though
		// their group (system) is included.
		{Name: "declare_goal_status", Group: sdktools.GroupSystem},
		{Name: "declare_plan", Group: sdktools.GroupSystem},
		{Name: "execute_plan", Group: sdktools.GroupSystem},
		{Name: "delegate", Group: sdktools.GroupSystem},
		{Name: "subagent", Group: sdktools.GroupSystem},
		{Name: "propose_goal", Group: sdktools.GroupSystem},
		{Name: "reflect", Group: sdktools.GroupSystem},
		{Name: "cancel_delegation", Group: sdktools.GroupSystem},
		{Name: "declare_step_complete", Group: sdktools.GroupSystem},
		// Untagged tool — matches no include group (fail-closed).
		{Name: "untagged_tool"},
	}

	got := verifierToolFilter(descs, nil)
	set := descriptorSet(got)

	for _, want := range []string{
		"read_file", "glob", "ripgrep", "web_fetch", "finish", "store_fact",
		"search_facts", "ask_user", "read_step_output", "batch", "semantic_search",
		activeShellToolName(), "mcp_linter", "mcp_remote", "declare_verification",
	} {
		if !set[want] {
			t.Errorf("verifierToolFilter: expected %q INCLUDED, got %v", want, set)
		}
	}

	for _, unwanted := range []string{
		// Mutating groups.
		"write_file", "edit_file", "delete_file", "delete_directory", "create_directory", "deploy_hook",
		// Goal-control / coordination tools.
		"declare_goal_status", "declare_plan", "execute_plan", "delegate", "subagent",
		"propose_goal", "reflect", "cancel_delegation", "declare_step_complete",
		// Untagged.
		"untagged_tool",
	} {
		if set[unwanted] {
			t.Errorf("verifierToolFilter: expected %q EXCLUDED, got %v", unwanted, set)
		}
	}
}

// TestVerifierToolFilter_DeclareStepCompleteExcludedEvenThoughSystemGrouped is
// a focused regression: declare_step_complete carries an INCLUDED group
// (system) but the verifier must NEVER have it. The name-based hard-exclude
// must win over the group include.
func TestVerifierToolFilter_DeclareStepCompleteExcludedEvenThoughSystemGrouped(t *testing.T) {
	if _, isExcluded := verifierExcludedToolNames["declare_step_complete"]; !isExcluded {
		t.Fatal("precondition: declare_step_complete should be in verifierExcludedToolNames for this regression to be meaningful")
	}
	got := verifierToolFilter([]sdktools.ToolDescriptor{
		{Name: "declare_step_complete", Group: sdktools.GroupSystem},
	}, nil)
	if len(got) != 0 {
		t.Errorf("expected declare_step_complete excluded, got %d descriptor(s)", len(got))
	}
}

// TestVerifierToolFilter_ExecutePlanExcludedEvenThoughSystemGrouped guards the
// group-migration delta: execute_plan is a system-group tool that the OLD
// include set (name-based) never granted; it launches plan-step subagents with
// full toolsets and must stay excluded under the group-based include too.
func TestVerifierToolFilter_ExecutePlanExcludedEvenThoughSystemGrouped(t *testing.T) {
	got := verifierToolFilter([]sdktools.ToolDescriptor{
		{Name: "execute_plan", Group: sdktools.GroupSystem},
	}, nil)
	if len(got) != 0 {
		t.Errorf("expected execute_plan excluded, got %d descriptor(s)", len(got))
	}
}

// TestVerifierToolFilter_MisTaggedMutatingBuiltinExcluded guards the
// belt-and-braces NAME backstop (verifierMutatingToolNames): a classic
// mutating builtin mis-tagged into an INCLUDED group (e.g. write_file
// accidentally tagged GroupSystem) passes both group checks — the name
// exclusion must still strip it so a tagging accident can never hand the
// verifier a mutating tool.
func TestVerifierToolFilter_MisTaggedMutatingBuiltinExcluded(t *testing.T) {
	for _, name := range []string{
		ToolWriteFile, ToolEditFile, "delete_file", "delete_directory", "create_directory",
	} {
		got := verifierToolFilter([]sdktools.ToolDescriptor{
			{Name: name, Group: sdktools.GroupSystem}, // mis-tagged into an included group
		}, nil)
		if len(got) != 0 {
			t.Errorf("expected mis-tagged mutating builtin %q excluded, got %d descriptor(s)", name, len(got))
		}
	}
}

// TestVerifierToolFilter_DisabledToolsDropped verifies mode-disabled tools are
// dropped even when their group is included.
func TestVerifierToolFilter_DisabledToolsDropped(t *testing.T) {
	descs := []sdktools.ToolDescriptor{
		{Name: "read_file", Group: sdktools.GroupLocalRead},
		{Name: "glob", Group: sdktools.GroupLocalRead},
		{Name: activeShellToolName(), Group: sdktools.GroupExecute},
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
// channel is present even when it is the only included tool provided.
func TestVerifierToolFilter_DeclareVerificationAlwaysIncluded(t *testing.T) {
	got := verifierToolFilter([]sdktools.ToolDescriptor{
		{Name: "declare_verification", Group: sdktools.GroupSystem},
		{Name: "write_file", Group: sdktools.GroupLocalWrite}, // excluded
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
// collapse to a single entry (LLM providers reject duplicate tool names).
func TestVerifierToolFilter_DeduplicatesByName(t *testing.T) {
	got := verifierToolFilter([]sdktools.ToolDescriptor{
		{Name: "read_file", Group: sdktools.GroupLocalRead},
		{Name: "read_file", Group: sdktools.GroupLocalRead},
		{Name: "finish", Group: sdktools.GroupSystem},
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

// TestVerifierExclusions_ContainRequiredControlToolsAndMutatingGroups is a
// completeness guard: every goal-control tool named in the spec MUST appear in
// the name-based exclude set, and both mutating groups MUST appear in the
// group-based exclude set, so none can ever leak into the verifier's toolset.
func TestVerifierExclusions_ContainRequiredControlToolsAndMutatingGroups(t *testing.T) {
	requiredNames := []string{
		// Goal-control
		"declare_goal_status", "declare_plan", "execute_plan", "delegate", "subagent",
		"propose_goal", "reflect", "declare_step_complete", "cancel_delegation",
	}
	for _, name := range requiredNames {
		if _, ok := verifierExcludedToolNames[name]; !ok {
			t.Errorf("verifierExcludedToolNames missing required entry %q", name)
		}
	}
	for _, g := range []sdktools.ToolGroup{sdktools.GroupLocalWrite, sdktools.GroupRemoteWrite} {
		if _, ok := verifierExcludedGroups[g]; !ok {
			t.Errorf("verifierExcludedGroups missing mutating group %q", g)
		}
		if _, included := verifierIncludeGroups[g]; included {
			t.Errorf("mutating group %q must NOT be in verifierIncludeGroups", g)
		}
	}
}

// ----------------------------------------------------------------------------
// Re-derivation mode toolset tests
//
// These verify the mode-branching contract: the re_derivation verifier toolset
// is the executable toolset PLUS delegate, while every mutating tool and every
// OTHER goal-control tool remains excluded.
// ----------------------------------------------------------------------------

// verifierFixtureDescriptors is the canonical tool list both modes filter over.
func verifierFixtureDescriptors() []sdktools.ToolDescriptor {
	return []sdktools.ToolDescriptor{
		// local_read / remote_read / system — included in both modes.
		{Name: "read_file", Group: sdktools.GroupLocalRead},
		{Name: "glob", Group: sdktools.GroupLocalRead},
		{Name: "ripgrep", Group: sdktools.GroupLocalRead},
		{Name: "web_fetch", Group: sdktools.GroupRemoteRead},
		{Name: "finish", Group: sdktools.GroupSystem},
		{Name: "store_fact", Group: sdktools.GroupSystem},
		{Name: "search_facts", Group: sdktools.GroupSystem},
		{Name: "ask_user", Group: sdktools.GroupSystem},
		// Step-output / final-result readers (system) — included in both modes.
		{Name: "read_step_output", Group: sdktools.GroupSystem},
		{Name: "read_final_result", Group: sdktools.GroupSystem},
		// Shell tool (execute) — included in both modes (re-run the verify clause).
		{Name: activeShellToolName(), Group: sdktools.GroupExecute},
		// MCP tools — included in both modes regardless of name.
		{Name: "mcp_linter", Group: sdktools.GroupLocalMCP, SourceCategory: sdktools.SourceCategoryMCP},
		// Verdict channel (system) — included in both modes.
		{Name: "declare_verification", Group: sdktools.GroupSystem},
		// delegate (system) — included ONLY in re_derivation mode.
		{Name: "delegate", Group: sdktools.GroupSystem},
		// Mutating tools — excluded in both modes (by group).
		{Name: "write_file", Group: sdktools.GroupLocalWrite},
		{Name: "edit_file", Group: sdktools.GroupLocalWrite},
		{Name: "delete_file", Group: sdktools.GroupLocalWrite},
		{Name: "delete_directory", Group: sdktools.GroupLocalWrite},
		{Name: "create_directory", Group: sdktools.GroupLocalWrite},
		// Goal-control / coordination tools (other than delegate) — excluded in both.
		{Name: "declare_goal_status", Group: sdktools.GroupSystem},
		{Name: "declare_plan", Group: sdktools.GroupSystem},
		{Name: "execute_plan", Group: sdktools.GroupSystem},
		{Name: "subagent", Group: sdktools.GroupSystem},
		{Name: "propose_goal", Group: sdktools.GroupSystem},
		{Name: "reflect", Group: sdktools.GroupSystem},
		{Name: "cancel_delegation", Group: sdktools.GroupSystem},
		{Name: "declare_step_complete", Group: sdktools.GroupSystem},
	}
}

// TestVerifierReDerivationToolFilter_AddsDelegate verifies the headline
// re_derivation acceptance criterion: delegate is INCLUDED (re_derivation needs
// it to spin up a fresh read-only run; read_step_output reads that run's
// result), while every mutating tool and every OTHER goal-control tool remains
// excluded.
func TestVerifierReDerivationToolFilter_AddsDelegate(t *testing.T) {
	got := verifierReDerivationToolFilter(verifierFixtureDescriptors(), nil)
	set := descriptorSet(got)

	for _, want := range []string{"delegate", "read_step_output", "read_final_result"} {
		if !set[want] {
			t.Errorf("re_derivation: expected %q INCLUDED, got %v", want, set)
		}
	}
	// The re_derivation toolset still carries the shared read/test/verdict
	// tools so the verifier can corroborate the delegated run's findings.
	for _, want := range []string{"read_file", "glob", "web_fetch", "finish", activeShellToolName(), "mcp_linter", "declare_verification"} {
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
		"declare_goal_status", "declare_plan", "execute_plan", "subagent", "propose_goal",
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
	// read_step_output is still present (it is a system tool, unrelated to
	// the delegate grant).
	if !set["read_step_output"] {
		t.Error("executable mode: read_step_output should still be present (it is a system tool)")
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
// exclusion set MINUS delegate (the one coordination tool granted in that
// mode), and every other goal-control tool is still present.
func TestVerifierReDerivationExcludedToolNames_OmitsDelegateOnly(t *testing.T) {
	// delegate is NOT in the re_derivation exclusion set (it is granted).
	if _, present := verifierReDerivationExcludedToolNames["delegate"]; present {
		t.Error("re_derivation exclusion set must NOT contain delegate (it is granted in that mode)")
	}
	// Every OTHER goal-control tool excluded (the full executable set minus delegate).
	for _, name := range []string{"declare_goal_status", "declare_plan", "execute_plan", "subagent", "propose_goal", "reflect", "cancel_delegation", "declare_step_complete"} {
		if _, ok := verifierReDerivationExcludedToolNames[name]; !ok {
			t.Errorf("re_derivation exclusion set missing goal-control tool %q", name)
		}
	}
}
