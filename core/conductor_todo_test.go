package core

import (
	"testing"

	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/orchestration"
)

func TestConductorTodoCallback_EmitsStepTodoUpdateAndPlanStepStart(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Summary: "Do thing", Description: "Do the thing well"},
		},
	})

	fn := conductorTodoCallback(emitter, bb)
	if fn == nil {
		t.Fatal("expected non-nil callback")
	}

	fn("step_1", []agent.TodoItem{
		{Text: "First", Checked: false},
		{Text: "Second", Checked: false},
	})

	if len(emitter.stepTodoUpdates) != 1 {
		t.Fatalf("expected 1 StepTodoUpdate, got %d", len(emitter.stepTodoUpdates))
	}
	if emitter.stepTodoUpdates[0].stepID != "step_1" {
		t.Errorf("expected step_id 'step_1', got %q", emitter.stepTodoUpdates[0].stepID)
	}
	if len(emitter.stepTodoUpdates[0].items) != 2 {
		t.Errorf("expected 2 items, got %d", len(emitter.stepTodoUpdates[0].items))
	}

	if len(emitter.planStepStarts) != 1 {
		t.Fatalf("expected 1 PlanStepStart, got %d", len(emitter.planStepStarts))
	}
	if emitter.planStepStarts[0].stepID != "step_1" {
		t.Errorf("expected step_id 'step_1', got %q", emitter.planStepStarts[0].stepID)
	}
	if emitter.planStepStarts[0].description != "Do the thing well" {
		t.Errorf("expected description from plan, got %q", emitter.planStepStarts[0].description)
	}

	if len(emitter.planStepCompletes) != 0 {
		t.Errorf("expected 0 PlanStepComplete (not all checked), got %d", len(emitter.planStepCompletes))
	}
}

func TestConductorTodoCallback_EmitsPlanStepCompleteWhenAllChecked(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_2", Summary: "Finish", Description: "Finish it"},
		},
	})

	fn := conductorTodoCallback(emitter, bb)

	fn("step_2", []agent.TodoItem{
		{Text: "A", Checked: true},
		{Text: "B", Checked: true},
	})

	if len(emitter.planStepCompletes) != 1 {
		t.Fatalf("expected 1 PlanStepComplete, got %d", len(emitter.planStepCompletes))
	}
	if emitter.planStepCompletes[0].stepID != "step_2" {
		t.Errorf("expected step_id 'step_2', got %q", emitter.planStepCompletes[0].stepID)
	}
	if !emitter.planStepCompletes[0].success {
		t.Error("expected success=true")
	}
}

func TestConductorTodoCallback_NilEmitterReturnsNil(t *testing.T) {
	fn := conductorTodoCallback(nil, orchestration.NewMapBlackboard())
	if fn != nil {
		t.Error("expected nil callback for nil emitter")
	}
}

func TestSubagentTodoCallback_EmitsOnlyStepTodoUpdate(t *testing.T) {
	emitter := &mockEmitter{}

	fn := subagentTodoCallback(emitter)
	if fn == nil {
		t.Fatal("expected non-nil callback")
	}

	fn("step_42", []agent.TodoItem{
		{Text: "Task", Checked: false},
	})

	if len(emitter.stepTodoUpdates) != 1 {
		t.Fatalf("expected 1 StepTodoUpdate, got %d", len(emitter.stepTodoUpdates))
	}
	if emitter.stepTodoUpdates[0].stepID != "step_42" {
		t.Errorf("expected step_id 'step_42', got %q", emitter.stepTodoUpdates[0].stepID)
	}

	if len(emitter.planStepStarts) != 0 {
		t.Errorf("subagent callback should NOT emit PlanStepStart, got %d", len(emitter.planStepStarts))
	}
	if len(emitter.planStepCompletes) != 0 {
		t.Errorf("subagent callback should NOT emit PlanStepComplete, got %d", len(emitter.planStepCompletes))
	}
}

func TestEmitPlanStepStart_LooksUpStepFromBlackboard(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "s1", Summary: "Summary A", Description: "Desc A"},
			{ID: "s2", Summary: "Summary B", Description: "Desc B"},
		},
	})

	emitPlanStepStart(emitter, bb, "s2")

	if len(emitter.planStepStarts) != 1 {
		t.Fatalf("expected 1 PlanStepStart, got %d", len(emitter.planStepStarts))
	}
	if emitter.planStepStarts[0].stepID != "s2" {
		t.Errorf("expected step_id 's2', got %q", emitter.planStepStarts[0].stepID)
	}
	if emitter.planStepStarts[0].summary != "Summary B" {
		t.Errorf("expected summary 'Summary B', got %q", emitter.planStepStarts[0].summary)
	}
	if emitter.planStepStarts[0].description != "Desc B" {
		t.Errorf("expected description 'Desc B', got %q", emitter.planStepStarts[0].description)
	}
}

func TestEmitPlanStepStart_NilEmitterIsSafe(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	emitPlanStepStart(nil, bb, "s1")
}

func TestEmitPlanStepStart_UnknownStepEmitsWithEmptyDesc(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "s1"}}})

	emitPlanStepStart(emitter, bb, "unknown_step")

	if len(emitter.planStepStarts) != 1 {
		t.Fatalf("expected 1 PlanStepStart, got %d", len(emitter.planStepStarts))
	}
	if emitter.planStepStarts[0].description != "" {
		t.Errorf("expected empty description for unknown step, got %q", emitter.planStepStarts[0].description)
	}
}
