package core

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRoutingDecision_JSONRoundTrip(t *testing.T) {
	original := RoutingDecision{
		Mode:               "plan_execute",
		Domain:             "code",
		Complexity:         4,
		CompactionStrategy: "summarization",
		SuggestedTools:     []string{"read_file", "write_file", "run_tests"},
		NeedsClarification: false,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal RoutingDecision: %v", err)
	}

	var decoded RoutingDecision
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal RoutingDecision: %v", err)
	}

	if decoded.Mode != original.Mode {
		t.Errorf("Mode mismatch: got %q, want %q", decoded.Mode, original.Mode)
	}
	if decoded.Domain != original.Domain {
		t.Errorf("Domain mismatch: got %q, want %q", decoded.Domain, original.Domain)
	}
	if decoded.Complexity != original.Complexity {
		t.Errorf("Complexity mismatch: got %d, want %d", decoded.Complexity, original.Complexity)
	}
	if decoded.CompactionStrategy != original.CompactionStrategy {
		t.Errorf("CompactionStrategy mismatch: got %q, want %q", decoded.CompactionStrategy, original.CompactionStrategy)
	}
	if len(decoded.SuggestedTools) != len(original.SuggestedTools) {
		t.Errorf("SuggestedTools length mismatch: got %d, want %d", len(decoded.SuggestedTools), len(original.SuggestedTools))
	}
	for i, tool := range decoded.SuggestedTools {
		if tool != original.SuggestedTools[i] {
			t.Errorf("SuggestedTools[%d] mismatch: got %q, want %q", i, tool, original.SuggestedTools[i])
		}
	}
	if decoded.NeedsClarification != original.NeedsClarification {
		t.Errorf("NeedsClarification mismatch: got %v, want %v", decoded.NeedsClarification, original.NeedsClarification)
	}
}

func TestPlan_JSONRoundTrip(t *testing.T) {
	original := Plan{
		Steps: []PlanStep{
			{
				ID:             "step_1",
				Description:    "Read and analyze requirements",
				DependsOn:      nil,
				Parallelizable: false,
				EstimatedTools: []string{"read_file"},
				RelevantAC:     []string{"ac_1"},
			},
			{
				ID:             "step_2a",
				Description:    "Implement core logic",
				DependsOn:      []string{"step_1"},
				Parallelizable: true,
				EstimatedTools: []string{"read_file", "write_file"},
				RelevantAC:     []string{"ac_1", "ac_2"},
			},
			{
				ID:             "step_2b",
				Description:    "Write tests",
				DependsOn:      []string{"step_1"},
				Parallelizable: true,
				EstimatedTools: []string{"write_file"},
				RelevantAC:     []string{"ac_3"},
			},
			{
				ID:             "step_3",
				Description:    "Run and verify tests",
				DependsOn:      []string{"step_2a", "step_2b"},
				Parallelizable: false,
				EstimatedTools: []string{"run_tests"},
				RelevantAC:     []string{"ac_1", "ac_2", "ac_3"},
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal Plan: %v", err)
	}

	var decoded Plan
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal Plan: %v", err)
	}

	if len(decoded.Steps) != len(original.Steps) {
		t.Fatalf("Steps length mismatch: got %d, want %d", len(decoded.Steps), len(original.Steps))
	}

	for i, step := range decoded.Steps {
		orig := original.Steps[i]
		if step.ID != orig.ID {
			t.Errorf("Steps[%d].ID mismatch: got %q, want %q", i, step.ID, orig.ID)
		}
		if step.Description != orig.Description {
			t.Errorf("Steps[%d].Description mismatch: got %q, want %q", i, step.Description, orig.Description)
		}
		if len(step.DependsOn) != len(orig.DependsOn) {
			t.Errorf("Steps[%d].DependsOn length mismatch: got %d, want %d", i, len(step.DependsOn), len(orig.DependsOn))
		}
		for j, dep := range step.DependsOn {
			if dep != orig.DependsOn[j] {
				t.Errorf("Steps[%d].DependsOn[%d] mismatch: got %q, want %q", i, j, dep, orig.DependsOn[j])
			}
		}
		if step.Parallelizable != orig.Parallelizable {
			t.Errorf("Steps[%d].Parallelizable mismatch: got %v, want %v", i, step.Parallelizable, orig.Parallelizable)
		}
	}
}

func TestAcceptanceCriterion_JSONRoundTrip(t *testing.T) {
	original := AcceptanceCriterion{
		ID:          "ac_1",
		Description: "All unit tests pass",
		CheckType:   "programmatic",
		CheckCmd:    "go test ./...",
		StepHint:    "Run tests after implementation",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal AcceptanceCriterion: %v", err)
	}

	var decoded AcceptanceCriterion
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal AcceptanceCriterion: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID mismatch: got %q, want %q", decoded.ID, original.ID)
	}
	if decoded.Description != original.Description {
		t.Errorf("Description mismatch: got %q, want %q", decoded.Description, original.Description)
	}
	if decoded.CheckType != original.CheckType {
		t.Errorf("CheckType mismatch: got %q, want %q", decoded.CheckType, original.CheckType)
	}
	if decoded.CheckCmd != original.CheckCmd {
		t.Errorf("CheckCmd mismatch: got %q, want %q", decoded.CheckCmd, original.CheckCmd)
	}
	if decoded.StepHint != original.StepHint {
		t.Errorf("StepHint mismatch: got %q, want %q", decoded.StepHint, original.StepHint)
	}
}

func TestReflection_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second) // Truncate for JSON precision
	original := Reflection{
		FailureAnalysis: "Test failed due to nil pointer dereference",
		RootCause:       "Missing nil check before accessing struct field",
		ActionPlan:      "Add nil check in function X before accessing field Y",
		Timestamp:       now,
		TaskType:        "code_fix",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal Reflection: %v", err)
	}

	var decoded Reflection
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal Reflection: %v", err)
	}

	if decoded.FailureAnalysis != original.FailureAnalysis {
		t.Errorf("FailureAnalysis mismatch: got %q, want %q", decoded.FailureAnalysis, original.FailureAnalysis)
	}
	if decoded.RootCause != original.RootCause {
		t.Errorf("RootCause mismatch: got %q, want %q", decoded.RootCause, original.RootCause)
	}
	if decoded.ActionPlan != original.ActionPlan {
		t.Errorf("ActionPlan mismatch: got %q, want %q", decoded.ActionPlan, original.ActionPlan)
	}
	if !decoded.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp mismatch: got %v, want %v", decoded.Timestamp, original.Timestamp)
	}
	if decoded.TaskType != original.TaskType {
		t.Errorf("TaskType mismatch: got %q, want %q", decoded.TaskType, original.TaskType)
	}
}
