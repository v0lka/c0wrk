package coretools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/user/agent/core"
)

// execVerdict is a test helper that marshals input and executes the verdict tool.
func execVerdict(ctx context.Context, t *testing.T, input any) (string, bool) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	tool := NewVerdictTool()
	result, execErr := tool.Execute(ctx, raw)
	if execErr != nil {
		t.Fatalf("Execute returned unexpected error: %v", execErr)
	}
	return result.Content, result.IsError
}

func TestVerdictTool_Success(t *testing.T) {
	bb := core.NewMapBlackboard()
	ctx := WithBlackboard(context.Background(), bb)

	content, isErr := execVerdict(ctx, t, map[string]any{
		"criterion_id": "ac_1",
		"verdict":      "YES",
		"explanation":   "code compiles and tests pass",
	})
	if isErr {
		t.Fatalf("expected no error, got IsError=true: %s", content)
	}
	if !contains(content, "Verdict recorded for ac_1: YES") {
		t.Errorf("unexpected content: %s", content)
	}

	verdicts := bb.GetEvalVerdicts()
	if len(verdicts) != 1 {
		t.Fatalf("expected 1 verdict, got %d", len(verdicts))
	}
	v := verdicts["ac_1"]
	if v.CriterionID != "ac_1" {
		t.Errorf("expected criterion_id 'ac_1', got %q", v.CriterionID)
	}
	if v.Verdict != "YES" {
		t.Errorf("expected verdict 'YES', got %q", v.Verdict)
	}
	if v.Explanation != "code compiles and tests pass" {
		t.Errorf("expected explanation 'code compiles and tests pass', got %q", v.Explanation)
	}
}

func TestVerdictTool_NoBlackboard(t *testing.T) {
	ctx := context.Background()
	content, isErr := execVerdict(ctx, t, map[string]any{
		"criterion_id": "ac_1",
		"verdict":      "YES",
		"explanation":   "looks good",
	})
	if !isErr {
		t.Fatalf("expected IsError=true when blackboard missing, got false: %s", content)
	}
	if !contains(content, "blackboard not available") {
		t.Errorf("expected blackboard-not-available message, got:\n%s", content)
	}
}

func TestVerdictTool_MissingParams(t *testing.T) {
	bb := core.NewMapBlackboard()
	ctx := WithBlackboard(context.Background(), bb)

	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name:  "missing criterion_id",
			input: map[string]any{"verdict": "YES", "explanation": "ok"},
			want:  "missing required parameter: criterion_id",
		},
		{
			name:  "missing verdict",
			input: map[string]any{"criterion_id": "ac_1", "explanation": "ok"},
			want:  "missing required parameter: verdict",
		},
		{
			name:  "missing explanation",
			input: map[string]any{"criterion_id": "ac_1", "verdict": "YES"},
			want:  "missing required parameter: explanation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, isErr := execVerdict(ctx, t, tt.input)
			if !isErr {
				t.Fatalf("expected IsError=true, got false: %s", content)
			}
			if !contains(content, tt.want) {
				t.Errorf("expected %q in content, got:\n%s", tt.want, content)
			}
		})
	}
}

func TestVerdictTool_InvalidVerdict(t *testing.T) {
	bb := core.NewMapBlackboard()
	ctx := WithBlackboard(context.Background(), bb)

	content, isErr := execVerdict(ctx, t, map[string]any{
		"criterion_id": "ac_1",
		"verdict":      "MAYBE",
		"explanation":   "not sure",
	})
	if !isErr {
		t.Fatalf("expected IsError=true for invalid verdict, got false: %s", content)
	}
	if !contains(content, "invalid verdict") {
		t.Errorf("expected invalid verdict message, got:\n%s", content)
	}
	if !contains(content, "MAYBE") {
		t.Errorf("expected original value in error, got:\n%s", content)
	}
}

func TestVerdictTool_Descriptor(t *testing.T) {
	d := VerdictToolDescriptor()
	if d.Name != "report_verdict" {
		t.Errorf("expected name 'report_verdict', got %q", d.Name)
	}
	if d.Description != verdictToolDescription {
		t.Errorf("expected description %q, got %q", verdictToolDescription, d.Description)
	}
	if d.Source != "core" {
		t.Errorf("expected source 'core', got %q", d.Source)
	}
	if len(d.InputSchema) == 0 {
		t.Error("expected non-empty InputSchema")
	}
}
