package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/goal"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// memSink is a minimal GoalStatusSink for testing declare_goal_status.
type memSink struct{ verdict *goal.Verdict }

func (s *memSink) Declare(v goal.Verdict) { s.verdict = &v }
func (s *memSink) Last() *goal.Verdict    { return s.verdict }

// --- propose_goal tests ---

// fakeProposer is a configurable GoalProposer for testing.
type fakeProposer struct {
	resp GoalProposalResponse
	err  error
}

func (f *fakeProposer) Propose(_ context.Context, _ GoalProposal) (GoalProposalResponse, error) {
	return f.resp, f.err
}

func proposeCtx(resp GoalProposalResponse) context.Context {
	return WithGoalProposer(context.Background(), &fakeProposer{resp: resp})
}

func TestProposeGoal_Execute_ApproveWithoutEdits(t *testing.T) {
	tool := NewProposeGoalTool()
	ctx := proposeCtx(GoalProposalResponse{Decision: "approve"})
	input, _ := json.Marshal(GoalProposal{Condition: "c", Verify: "v"})

	res, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Errorf("approve should not be an error result, got IsError=true: %q", res.Content)
	}
	if !strings.Contains(res.Content, "Goal approved") {
		t.Errorf("expected 'Goal approved' message, got %q", res.Content)
	}
}

func TestProposeGoal_Execute_ApproveWithEdits(t *testing.T) {
	tool := NewProposeGoalTool()
	ctx := proposeCtx(GoalProposalResponse{Decision: "approve", Condition: "edited cond", Verify: "edited ver"})
	input, _ := json.Marshal(GoalProposal{Condition: "orig", Verify: "orig"})

	res, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Content, "edited cond") {
		t.Errorf("expected edited condition echoed, got %q", res.Content)
	}
}

func TestProposeGoal_Execute_Clarify(t *testing.T) {
	tool := NewProposeGoalTool()
	ctx := proposeCtx(GoalProposalResponse{Decision: "clarify", Clarification: "which scope?"})
	input, _ := json.Marshal(GoalProposal{Condition: "c", Verify: "v"})

	res, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Errorf("clarify should not be IsError, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "which scope?") {
		t.Errorf("expected clarification echoed, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "propose_goal again") {
		t.Errorf("expected re-propose instruction, got %q", res.Content)
	}
}

func TestProposeGoal_Execute_CancelIsError(t *testing.T) {
	tool := NewProposeGoalTool()
	ctx := proposeCtx(GoalProposalResponse{Decision: "cancel"})
	input, _ := json.Marshal(GoalProposal{Condition: "c", Verify: "v"})

	res, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Error("cancel should return IsError=true")
	}
}

func TestProposeGoal_Execute_UnknownDecisionIsError(t *testing.T) {
	tool := NewProposeGoalTool()
	ctx := proposeCtx(GoalProposalResponse{Decision: "bogus"})
	input, _ := json.Marshal(GoalProposal{Condition: "c", Verify: "v"})

	res, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Error("unknown decision should return IsError=true")
	}
}

func TestProposeGoal_Execute_ValidationErrors(t *testing.T) {
	tool := NewProposeGoalTool()
	ctx := proposeCtx(GoalProposalResponse{Decision: "approve"})

	t.Run("empty condition", func(t *testing.T) {
		input, _ := json.Marshal(GoalProposal{Condition: "  ", Verify: "v"})
		res, _ := tool.Execute(ctx, input)
		if !res.IsError || !strings.Contains(res.Content, "condition must not be empty") {
			t.Errorf("expected condition validation error, got %q", res.Content)
		}
	})
	t.Run("empty verify", func(t *testing.T) {
		input, _ := json.Marshal(GoalProposal{Condition: "c", Verify: ""})
		res, _ := tool.Execute(ctx, input)
		if !res.IsError || !strings.Contains(res.Content, "verify must not be empty") {
			t.Errorf("expected verify validation error, got %q", res.Content)
		}
	})
}

func TestProposeGoal_Execute_NoProposerInContext(t *testing.T) {
	tool := NewProposeGoalTool()
	input, _ := json.Marshal(GoalProposal{Condition: "c", Verify: "v"})
	res, _ := tool.Execute(context.Background(), input)
	if !res.IsError || !strings.Contains(res.Content, "no goal proposer") {
		t.Errorf("expected no-proposer error, got %q", res.Content)
	}
}

func TestProposeGoal_Execute_MalformedInput(t *testing.T) {
	tool := NewProposeGoalTool()
	ctx := proposeCtx(GoalProposalResponse{Decision: "approve"})
	res, err := tool.Execute(ctx, json.RawMessage(`{not json`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	// ParseInputError returns a ToolResult with IsError=true, not a Go error.
	if !res.IsError || !strings.Contains(res.Content, "parse") {
		t.Errorf("expected parse-error ToolResult, got %q (IsError=%v)", res.Content, res.IsError)
	}
}

func TestCoalesce(t *testing.T) {
	if got := coalesce("", "fallback"); got != "fallback" {
		t.Errorf("coalesce(\"\", \"fallback\") = %q, want fallback", got)
	}
	if got := coalesce("first", "second"); got != "first" {
		t.Errorf("coalesce(\"first\", \"second\") = %q, want first", got)
	}
}

func TestProposeGoal_DefaultPolicy(t *testing.T) {
	if got := NewProposeGoalTool().DefaultPolicy(); got != sdktools.PolicyAlwaysAllow {
		t.Errorf("DefaultPolicy = %v, want PolicyAlwaysAllow", got)
	}
}

// --- declare_goal_status tests ---

func TestDeclareGoal_Status_MetWithEvidence(t *testing.T) {
	tool := NewDeclareGoalStatusTool()
	sink := &memSink{}
	ctx := WithGoalStatusSink(context.Background(), sink)
	input, _ := json.Marshal(declareGoalStatusInput{
		Status:   "met",
		Reason:   "all tests pass",
		Evidence: []goal.GoalEvidence{{Type: goal.EvidenceTypeFile, Ref: "main.go", Summary: "impl"}},
	})

	res, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sink.Last() == nil || sink.Last().Status != "met" {
		t.Errorf("sink verdict = %+v, want met", sink.Last())
	}
	if !strings.Contains(res.Content, "MET") {
		t.Errorf("expected MET confirmation, got %q", res.Content)
	}
}

func TestDeclareGoal_Status_NotMet(t *testing.T) {
	tool := NewDeclareGoalStatusTool()
	sink := &memSink{}
	ctx := WithGoalStatusSink(context.Background(), sink)
	input, _ := json.Marshal(declareGoalStatusInput{Status: "not_met", Reason: "wip"})

	res, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sink.Last() == nil || sink.Last().Status != "not_met" {
		t.Errorf("sink verdict = %+v, want not_met", sink.Last())
	}
	if !strings.Contains(res.Content, "NOT YET MET") {
		t.Errorf("expected NOT YET MET confirmation, got %q", res.Content)
	}
}

func TestDeclareGoal_Status_Blocked(t *testing.T) {
	tool := NewDeclareGoalStatusTool()
	sink := &memSink{}
	ctx := WithGoalStatusSink(context.Background(), sink)
	input, _ := json.Marshal(declareGoalStatusInput{Status: "blocked", Reason: "need input"})

	res, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sink.Last() == nil || sink.Last().Status != "blocked" {
		t.Errorf("sink verdict = %+v, want blocked", sink.Last())
	}
	if !strings.Contains(res.Content, "BLOCKED") {
		t.Errorf("expected BLOCKED confirmation, got %q", res.Content)
	}
}

func TestDeclareGoal_Status_InvalidStatus(t *testing.T) {
	tool := NewDeclareGoalStatusTool()
	sink := &memSink{}
	ctx := WithGoalStatusSink(context.Background(), sink)
	input, _ := json.Marshal(declareGoalStatusInput{Status: "done", Reason: "r"})

	res, _ := tool.Execute(ctx, input)
	if !res.IsError || !strings.Contains(res.Content, "validation error") {
		t.Errorf("expected validation error for invalid status, got %q", res.Content)
	}
	if sink.Last() != nil {
		t.Error("sink should not receive a verdict for an invalid status")
	}
}

func TestDeclareGoal_Status_MetRequiresEvidence(t *testing.T) {
	tool := NewDeclareGoalStatusTool()
	sink := &memSink{}
	ctx := WithGoalStatusSink(context.Background(), sink)
	input, _ := json.Marshal(declareGoalStatusInput{Status: "met", Reason: "done"})

	res, _ := tool.Execute(ctx, input)
	if !res.IsError || !strings.Contains(res.Content, "evidence") {
		t.Errorf("expected evidence-mandate error, got %q", res.Content)
	}
	if sink.Last() != nil {
		t.Error("sink should not receive a met verdict without evidence")
	}
}

// TestDeclareGoal_Status_MetRejectsEmptyEvidenceEntry is the regression test for
// the evidence-mandate bypass: the tool executor does not validate against the
// JSON schema, so evidence:[{}] or evidence:[{"ref":""}] must be rejected just
// like empty evidence — otherwise a bare "done" terminates the goal loop with
// no concrete artifact.
func TestDeclareGoal_Status_MetRejectsEmptyEvidenceEntry(t *testing.T) {
	tool := NewDeclareGoalStatusTool()
	sink := &memSink{}
	ctx := WithGoalStatusSink(context.Background(), sink)

	cases := []struct {
		name string
		ev   []goal.GoalEvidence
	}{
		{"empty object", []goal.GoalEvidence{{}}},
		{"empty ref", []goal.GoalEvidence{{Type: "file", Ref: "", Summary: "impl"}}},
		{"whitespace ref", []goal.GoalEvidence{{Type: "file", Ref: "  ", Summary: "impl"}}},
		{"empty type", []goal.GoalEvidence{{Type: "", Ref: "main.go", Summary: "impl"}}},
		{"empty summary", []goal.GoalEvidence{{Type: "file", Ref: "main.go", Summary: ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink.verdict = nil // reset between subtests
			input, _ := json.Marshal(declareGoalStatusInput{Status: "met", Evidence: tc.ev, Reason: "done"})
			res, _ := tool.Execute(ctx, input)
			if !res.IsError {
				t.Errorf("expected validation error for %s, got accepted verdict", tc.name)
			}
			if sink.Last() != nil {
				t.Errorf("sink should not receive a met verdict with an empty evidence entry (%s)", tc.name)
			}
		})
	}
}

func TestDeclareGoal_Status_NoSinkInContext(t *testing.T) {
	tool := NewDeclareGoalStatusTool()
	input, _ := json.Marshal(declareGoalStatusInput{Status: "not_met", Reason: "r"})

	res, _ := tool.Execute(context.Background(), input)
	if !res.IsError || !strings.Contains(res.Content, "no goal status sink") {
		t.Errorf("expected no-sink error, got %q", res.Content)
	}
}

func TestDeclareGoal_Status_MalformedInput(t *testing.T) {
	tool := NewDeclareGoalStatusTool()
	ctx := WithGoalStatusSink(context.Background(), &memSink{})
	res, err := tool.Execute(ctx, json.RawMessage(`{bad`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	// ParseInputError returns a ToolResult with IsError=true, not a Go error.
	if !res.IsError || !strings.Contains(res.Content, "parse") {
		t.Errorf("expected parse-error ToolResult, got %q (IsError=%v)", res.Content, res.IsError)
	}
}

func TestDeclareGoal_DefaultPolicy(t *testing.T) {
	if got := NewDeclareGoalStatusTool().DefaultPolicy(); got != sdktools.PolicyAlwaysAllow {
		t.Errorf("DefaultPolicy = %v, want PolicyAlwaysAllow", got)
	}
}

// TestGoalContextPlumbing verifies the context round-trip helpers.
func TestGoalContextPlumbing(t *testing.T) {
	t.Run("goal proposer", func(t *testing.T) {
		ctx := context.Background()
		if GoalProposerFrom(ctx) != nil {
			t.Error("expected nil proposer from empty context")
		}
		p := &fakeProposer{}
		ctx = WithGoalProposer(ctx, p)
		if GoalProposerFrom(ctx) != p {
			t.Error("WithGoalProposer/GoalProposerFrom round-trip failed")
		}
	})
	t.Run("goal status sink", func(t *testing.T) {
		ctx := context.Background()
		if GoalStatusSinkFrom(ctx) != nil {
			t.Error("expected nil sink from empty context")
		}
		s := &memSink{}
		ctx = WithGoalStatusSink(ctx, s)
		if GoalStatusSinkFrom(ctx) != s {
			t.Error("WithGoalStatusSink/GoalStatusSinkFrom round-trip failed")
		}
	})
}

// --- verification_mode boundary validation ---

// capturingProposer records the GoalProposal it received so a test can assert
// the normalized verification mode was forwarded to the proposer.
type capturingProposer struct {
	resp GoalProposalResponse
	got  GoalProposal
}

func (c *capturingProposer) Propose(_ context.Context, p GoalProposal) (GoalProposalResponse, error) {
	c.got = p
	return c.resp, nil
}

func TestProposeGoal_Execute_VerificationModeDefaultsToExecutable(t *testing.T) {
	proposer := &capturingProposer{resp: GoalProposalResponse{Decision: "approve"}}
	ctx := WithGoalProposer(context.Background(), proposer)
	// Omit verification_mode entirely; expect it to default to executable.
	input, _ := json.Marshal(GoalProposal{Condition: "c", Verify: "v"})

	if _, err := NewProposeGoalTool().Execute(ctx, input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proposer.got.VerificationMode != goal.VerificationModeExecutable {
		t.Errorf("default mode forwarded to proposer = %q, want %q",
			proposer.got.VerificationMode, goal.VerificationModeExecutable)
	}
}

func TestProposeGoal_Execute_VerificationModePassesThroughKnownValue(t *testing.T) {
	proposer := &capturingProposer{resp: GoalProposalResponse{Decision: "approve"}}
	ctx := WithGoalProposer(context.Background(), proposer)
	input, _ := json.Marshal(GoalProposal{
		Condition:        "c",
		Verify:           "v",
		VerificationMode: goal.VerificationModeReDerivation,
	})

	if _, err := NewProposeGoalTool().Execute(ctx, input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proposer.got.VerificationMode != goal.VerificationModeReDerivation {
		t.Errorf("re_derivation mode forwarded to proposer = %q, want %q",
			proposer.got.VerificationMode, goal.VerificationModeReDerivation)
	}
}

func TestProposeGoal_Execute_RejectsUnknownVerificationMode(t *testing.T) {
	proposer := &capturingProposer{resp: GoalProposalResponse{Decision: "approve"}}
	ctx := WithGoalProposer(context.Background(), proposer)
	input, _ := json.Marshal(GoalProposal{
		Condition:        "c",
		Verify:           "v",
		VerificationMode: "bogus",
	})

	res, err := NewProposeGoalTool().Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "validation error") {
		t.Errorf("expected validation error for unknown mode, got %q (IsError=%v)", res.Content, res.IsError)
	}
	// The proposer must NOT have been called for an invalid mode.
	if proposer.got.Condition != "" {
		t.Errorf("proposer was called despite invalid mode; got proposal %+v", proposer.got)
	}
}
