package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/goal"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// verifSink is a minimal VerificationSink for testing declare_verification.
// (A separate type from goal_tools_test.go's memSink to keep the two sinks
// independent in the same package.)
type verifSink struct{ outcome *VerificationOutcome }

func (s *verifSink) Declare(v VerificationOutcome) { s.outcome = &v }
func (s *verifSink) Last() *VerificationOutcome    { return s.outcome }

// --- registration / policy ---

func TestDeclareVerification_DefaultPolicy(t *testing.T) {
	if got := NewDeclareVerificationTool().DefaultPolicy(); got != sdktools.PolicyAlwaysAllow {
		t.Errorf("DefaultPolicy = %v, want PolicyAlwaysAllow", got)
	}
}

func TestDeclareVerification_NameAndSchema(t *testing.T) {
	tool := NewDeclareVerificationTool()
	if tool.Name() != "declare_verification" {
		t.Errorf("Name = %q, want declare_verification", tool.Name())
	}
	// Schema must require confirmed + reason and describe an evidence array.
	s := string(tool.InputSchema())
	for _, want := range []string{`"confirmed"`, `"reason"`, `"evidence"`, `"required"`} {
		if !strings.Contains(s, want) {
			t.Errorf("schema missing %q", want)
		}
	}
}

// --- happy path ---

func TestDeclareVerification_ConfirmedWithEvidence(t *testing.T) {
	tool := NewDeclareVerificationTool()
	sink := &verifSink{}
	ctx := WithVerificationSink(context.Background(), sink)
	input, _ := json.Marshal(declareVerificationInput{
		Confirmed: true,
		Reason:    "all tests pass",
		Evidence:  []goal.GoalEvidence{{Type: goal.EvidenceTypeTestOutput, Ref: "go test ./...", Summary: "PASS"}},
	})

	res, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sink.Last() == nil || !sink.Last().Confirmed {
		t.Errorf("sink outcome = %+v, want confirmed", sink.Last())
	}
	if !strings.Contains(res.Content, "CONFIRMED") {
		t.Errorf("expected CONFIRMED confirmation, got %q", res.Content)
	}
}

func TestDeclareVerification_NotConfirmed(t *testing.T) {
	tool := NewDeclareVerificationTool()
	sink := &verifSink{}
	ctx := WithVerificationSink(context.Background(), sink)
	input, _ := json.Marshal(declareVerificationInput{Confirmed: false, Reason: "tests fail"})

	res, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sink.Last() == nil || sink.Last().Confirmed {
		t.Errorf("sink outcome = %+v, want not confirmed", sink.Last())
	}
	if !strings.Contains(res.Content, "NOT CONFIRMED") {
		t.Errorf("expected NOT CONFIRMED confirmation, got %q", res.Content)
	}
}

// --- evidence mandate (mirrors declare_goal_status "met" enforcement) ---

func TestDeclareVerification_ConfirmedWithoutEvidenceRejected(t *testing.T) {
	tool := NewDeclareVerificationTool()
	sink := &verifSink{}
	ctx := WithVerificationSink(context.Background(), sink)
	input, _ := json.Marshal(declareVerificationInput{Confirmed: true, Reason: "done"})

	res, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Errorf("confirmed=true without evidence should be rejected (IsError=true), got %q", res.Content)
	}
	if sink.Last() != nil {
		t.Errorf("rejected verdict must not be recorded in the sink, got %+v", sink.Last())
	}
}

func TestDeclareVerification_ConfirmedWithIncompleteEvidenceRejected(t *testing.T) {
	tool := NewDeclareVerificationTool()
	sink := &verifSink{}
	ctx := WithVerificationSink(context.Background(), sink)

	cases := []struct {
		name string
		ev   goal.GoalEvidence
	}{
		{"empty type", goal.GoalEvidence{Type: "  ", Ref: "main.go", Summary: "impl"}},
		{"empty ref", goal.GoalEvidence{Type: "file", Ref: "  ", Summary: "impl"}},
		{"empty summary", goal.GoalEvidence{Type: "file", Ref: "main.go", Summary: "  "}},
		{"all blank object", goal.GoalEvidence{Type: " ", Ref: " ", Summary: " "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			input, _ := json.Marshal(declareVerificationInput{
				Confirmed: true,
				Reason:    "done",
				Evidence:  []goal.GoalEvidence{c.ev},
			})
			res, _ := tool.Execute(ctx, input)
			if !res.IsError || !strings.Contains(res.Content, "incomplete") {
				t.Errorf("expected incomplete-evidence rejection, got %q (IsError=%v)", res.Content, res.IsError)
			}
		})
	}
	if sink.Last() != nil {
		t.Errorf("no verdict should have been recorded in the sink, got %+v", sink.Last())
	}
}

// --- no sink in context (no-op outside a verification pass) ---

func TestDeclareVerification_NoSinkInContext(t *testing.T) {
	tool := NewDeclareVerificationTool()
	input, _ := json.Marshal(declareVerificationInput{Confirmed: false, Reason: "n/a"})
	res, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "no verification sink") {
		t.Errorf("expected no-sink error, got %q (IsError=%v)", res.Content, res.IsError)
	}
}

// --- malformed input ---

func TestDeclareVerification_MalformedInput(t *testing.T) {
	tool := NewDeclareVerificationTool()
	ctx := WithVerificationSink(context.Background(), &verifSink{})
	res, err := tool.Execute(ctx, json.RawMessage(`{not json`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	// ParseInputError returns a ToolResult with IsError=true, not a Go error.
	if !res.IsError || !strings.Contains(res.Content, "parse") {
		t.Errorf("expected parse-error ToolResult, got %q (IsError=%v)", res.Content, res.IsError)
	}
}

// --- context plumbing round-trip ---

func TestVerificationSinkContextPlumbing(t *testing.T) {
	bg := context.Background()
	if got := VerificationSinkFrom(bg); got != nil {
		t.Errorf("VerificationSinkFrom(empty ctx) = %v, want nil", got)
	}
	s := &verifSink{}
	ctx := WithVerificationSink(bg, s)
	if got := VerificationSinkFrom(ctx); got != s {
		t.Errorf("VerificationSinkFrom did not round-trip the same sink")
	}
}

// --- sink contract: Last returns nil until a declaration ---

func TestVerificationSink_LastIsNilBeforeDeclare(t *testing.T) {
	s := &verifSink{}
	if s.Last() != nil {
		t.Fatal("expected nil before any declaration")
	}
	s.Declare(VerificationOutcome{Confirmed: true, Reason: "ok"})
	if s.Last() == nil || !s.Last().Confirmed {
		t.Errorf("Last = %+v after Declare, want a confirmed outcome", s.Last())
	}
}
