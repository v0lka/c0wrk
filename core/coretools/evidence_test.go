package coretools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/user/agent/core"
	"github.com/user/agent/sdk/llm"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// bbCtx returns a context with a pre-populated blackboard.
func bbCtx(t *testing.T) (context.Context, *core.MapBlackboard) {
	t.Helper()
	bb := core.NewMapBlackboard()
	bb.SetStepResult("step_1", "full output of step 1", nil, []core.Step{
		{
			Thought:     "I need to read the file",
			Action:      llm.ToolCall{Name: "read_file"},
			Observation: "file contents here",
		},
		{
			Thought:     "Now I compile",
			Action:      llm.ToolCall{Name: "bash_exec"},
			Observation: "build succeeded",
		},
	})
	bb.SetStepResult("step_2", "full output of step 2 with keyword foobar", nil, nil)
	bb.SetStepResult("step_err", "", errors.New("exec failed"), nil)
	return WithBlackboard(context.Background(), bb), bb
}

func execEvidence(ctx context.Context, t *testing.T, input any) (string, bool) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	tool := NewEvidenceTool()
	result, execErr := tool.Execute(ctx, raw)
	if execErr != nil {
		t.Fatalf("Execute returned unexpected error: %v", execErr)
	}
	return result.Content, result.IsError
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestEvidence_FetchByStepID(t *testing.T) {
	ctx, _ := bbCtx(t)
	content, isErr := execEvidence(ctx, t, map[string]any{"step_id": "step_1"})
	if isErr {
		t.Fatalf("expected no error, got IsError=true: %s", content)
	}
	// Must contain full output
	if !contains(content, "full output of step 1") {
		t.Errorf("missing full output in result:\n%s", content)
	}
	// Must contain ReAct step info
	if !contains(content, "read_file") {
		t.Errorf("missing tool call 'read_file' in result:\n%s", content)
	}
	if !contains(content, "bash_exec") {
		t.Errorf("missing tool call 'bash_exec' in result:\n%s", content)
	}
}

func TestEvidence_FetchByStepID_NotFound(t *testing.T) {
	ctx, _ := bbCtx(t)
	content, isErr := execEvidence(ctx, t, map[string]any{"step_id": "nonexistent"})
	if isErr {
		t.Fatalf("expected IsError=false for not-found, got true: %s", content)
	}
	if !contains(content, "no result found") {
		t.Errorf("expected informative not-found message, got:\n%s", content)
	}
}

func TestEvidence_SearchByQuery(t *testing.T) {
	ctx, _ := bbCtx(t)
	content, isErr := execEvidence(ctx, t, map[string]any{"query": "foobar"})
	if isErr {
		t.Fatalf("expected no error, got IsError=true: %s", content)
	}
	if !contains(content, "step_2") {
		t.Errorf("expected step_2 in search results:\n%s", content)
	}
}

func TestEvidence_SearchByQuery_NoMatches(t *testing.T) {
	ctx, _ := bbCtx(t)
	content, isErr := execEvidence(ctx, t, map[string]any{"query": "zzz_no_match_zzz"})
	if isErr {
		t.Fatalf("expected IsError=false, got true: %s", content)
	}
	if !contains(content, "no matches found") {
		t.Errorf("expected empty result message, got:\n%s", content)
	}
}

func TestEvidence_ListAll(t *testing.T) {
	ctx, _ := bbCtx(t)
	content, isErr := execEvidence(ctx, t, map[string]any{"list": true})
	if isErr {
		t.Fatalf("expected no error, got IsError=true: %s", content)
	}
	for _, id := range []string{"step_1", "step_2", "step_err"} {
		if !contains(content, id) {
			t.Errorf("missing step %q in list:\n%s", id, content)
		}
	}
	// step_err should show error status
	if !contains(content, "error") {
		t.Errorf("expected error status for step_err:\n%s", content)
	}
}

func TestEvidence_NoBlackboard(t *testing.T) {
	ctx := context.Background()
	content, isErr := execEvidence(ctx, t, map[string]any{"step_id": "step_1"})
	if !isErr {
		t.Fatalf("expected IsError=true when blackboard missing, got false: %s", content)
	}
	if !contains(content, "blackboard not available") {
		t.Errorf("expected blackboard-not-available message, got:\n%s", content)
	}
}

func TestEvidence_InvalidJSON(t *testing.T) {
	ctx, _ := bbCtx(t)
	tool := NewEvidenceTool()
	result, err := tool.Execute(ctx, json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid JSON")
	}
	if !contains(result.Content, "failed to parse input") {
		t.Errorf("expected parse error message, got: %s", result.Content)
	}
}

func TestEvidence_EmptyInput_DefaultsList(t *testing.T) {
	ctx, _ := bbCtx(t)
	content, isErr := execEvidence(ctx, t, map[string]any{})
	if isErr {
		t.Fatalf("expected no error, got IsError=true: %s", content)
	}
	// Should behave like list — show all steps
	if !contains(content, "step_1") || !contains(content, "step_2") {
		t.Errorf("empty input should default to list, got:\n%s", content)
	}
}

func TestEvidence_FetchStepWithError(t *testing.T) {
	ctx, _ := bbCtx(t)
	content, isErr := execEvidence(ctx, t, map[string]any{"step_id": "step_err"})
	if isErr {
		t.Fatalf("expected no error, got IsError=true: %s", content)
	}
	if !contains(content, "exec failed") {
		t.Errorf("expected error info in output, got:\n%s", content)
	}
}

func TestEvidence_Descriptor(t *testing.T) {
	d := EvidenceToolDescriptor()
	if d.Name != "read_evidence" {
		t.Errorf("expected name 'read_evidence', got %q", d.Name)
	}
	if d.Source != "core" {
		t.Errorf("expected source 'core', got %q", d.Source)
	}
	if len(d.InputSchema) == 0 {
		t.Error("expected non-empty InputSchema")
	}
}

func TestEvidence_ContextHelpers(t *testing.T) {
	// nil when not set
	ctx := context.Background()
	if bb := BlackboardFromContext(ctx); bb != nil {
		t.Fatal("expected nil blackboard from empty context")
	}

	// round-trip
	bb := core.NewMapBlackboard()
	ctx = WithBlackboard(ctx, bb)
	got := BlackboardFromContext(ctx)
	if got == nil {
		t.Fatal("expected non-nil blackboard after WithBlackboard")
	}
}

// contains is a test helper for substring matching.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
