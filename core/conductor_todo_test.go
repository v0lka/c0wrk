package core

import (
	"testing"

	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/orchestration"
)

func TestInlineStepLifecycle_OnChecklistUpdate_EmitsStepTodoUpdateAndInferredPlanStepStart(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Summary: "Do thing", Description: "Do the thing well"},
		},
	})

	lc := newInlineStepLifecycle(emitter, bb)

	lc.onChecklistUpdate("step_1", []agent.TodoItem{
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
		t.Fatalf("expected 1 inferred PlanStepStart, got %d", len(emitter.planStepStarts))
	}
	if emitter.planStepStarts[0].stepID != "step_1" {
		t.Errorf("expected step_id 'step_1', got %q", emitter.planStepStarts[0].stepID)
	}
	if emitter.planStepStarts[0].description != "Do the thing well" {
		t.Errorf("expected description from plan, got %q", emitter.planStepStarts[0].description)
	}

	// Checklist update must NOT emit PlanStepComplete (lifecycle is decoupled).
	if len(emitter.planStepCompletes) != 0 {
		t.Errorf("expected 0 PlanStepComplete from checklist update, got %d", len(emitter.planStepCompletes))
	}

	// PlanStepStart must be emitted before StepTodoUpdate so the frontend
	// opens the step container before the checklist arrives (nesting).
	if len(emitter.eventOrder) != 2 ||
		emitter.eventOrder[0] != "plan_step_start" ||
		emitter.eventOrder[1] != "step_todo_update" {
		t.Errorf("expected [plan_step_start, step_todo_update], got %v", emitter.eventOrder)
	}
}

func TestInlineStepLifecycle_OnChecklistUpdate_PlanStepStartEmittedOncePerStep(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{{ID: "step_1", Summary: "S", Description: "D"}},
	})

	lc := newInlineStepLifecycle(emitter, bb)

	// First update → inferred start.
	lc.onChecklistUpdate("step_1", []agent.TodoItem{{Text: "A", Checked: false}})
	// Second update (item checked off) → no duplicate start.
	lc.onChecklistUpdate("step_1", []agent.TodoItem{{Text: "A", Checked: true}})

	if len(emitter.planStepStarts) != 1 {
		t.Fatalf("expected 1 PlanStepStart (deduped), got %d", len(emitter.planStepStarts))
	}
	if len(emitter.stepTodoUpdates) != 2 {
		t.Fatalf("expected 2 StepTodoUpdate, got %d", len(emitter.stepTodoUpdates))
	}
}

func TestInlineStepLifecycle_OnChecklistUpdate_StandaloneEmitsOnlyStepTodoUpdate(t *testing.T) {
	emitter := &mockEmitter{}
	lc := newInlineStepLifecycle(emitter, orchestration.NewMapBlackboard())

	// Empty stepID = standalone checklist (Conductor without a plan).
	lc.onChecklistUpdate("", []agent.TodoItem{
		{Text: "Standalone task", Checked: false},
	})

	if len(emitter.stepTodoUpdates) != 1 {
		t.Fatalf("expected 1 StepTodoUpdate, got %d", len(emitter.stepTodoUpdates))
	}
	if emitter.stepTodoUpdates[0].stepID != "" {
		t.Errorf("expected empty step_id for standalone, got %q", emitter.stepTodoUpdates[0].stepID)
	}
	if len(emitter.planStepStarts) != 0 {
		t.Errorf("standalone checklist should NOT emit PlanStepStart, got %d", len(emitter.planStepStarts))
	}
	if len(emitter.planStepCompletes) != 0 {
		t.Errorf("standalone checklist should NOT emit PlanStepComplete, got %d", len(emitter.planStepCompletes))
	}
	if len(emitter.eventOrder) != 1 || emitter.eventOrder[0] != "step_todo_update" {
		t.Errorf("standalone should emit only [step_todo_update], got %v", emitter.eventOrder)
	}
}

func TestInlineStepLifecycle_CompleteStep_EmitsPlanStepComplete(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{{ID: "step_2", Summary: "Finish", Description: "Finish it"}},
	})

	lc := newInlineStepLifecycle(emitter, bb)

	// Start the step via checklist update.
	lc.onChecklistUpdate("step_2", []agent.TodoItem{{Text: "A", Checked: true}})
	// Explicitly complete it.
	lc.completeStep("step_2", true, "")

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

func TestInlineStepLifecycle_CompleteStep_NotStartedIsNoop(t *testing.T) {
	emitter := &mockEmitter{}
	lc := newInlineStepLifecycle(emitter, orchestration.NewMapBlackboard())

	// Complete a step that was never started — should be a no-op.
	lc.completeStep("unknown_step", true, "")

	if len(emitter.planStepCompletes) != 0 {
		t.Errorf("expected 0 PlanStepComplete for unstarted step, got %d", len(emitter.planStepCompletes))
	}
}

func TestInlineStepLifecycle_CompleteAll_AutoCompletesRemainingSteps(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Summary: "A", Description: "A"},
			{ID: "step_2", Summary: "B", Description: "B"},
		},
	})

	lc := newInlineStepLifecycle(emitter, bb)

	// Start both steps.
	lc.onChecklistUpdate("step_1", []agent.TodoItem{{Text: "A", Checked: false}})
	lc.onChecklistUpdate("step_2", []agent.TodoItem{{Text: "B", Checked: false}})
	// Explicitly complete step_1.
	lc.completeStep("step_1", true, "")
	// Finish fallback — step_2 is still running.
	lc.completeAll(true, "")

	if len(emitter.planStepCompletes) != 2 {
		t.Fatalf("expected 2 PlanStepComplete (1 explicit + 1 fallback), got %d", len(emitter.planStepCompletes))
	}
}

func TestInlineStepLifecycle_CompleteAll_AfterExplicitCompleteIsNoop(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{{ID: "step_1", Summary: "A", Description: "A"}},
	})

	lc := newInlineStepLifecycle(emitter, bb)
	lc.onChecklistUpdate("step_1", []agent.TodoItem{{Text: "A", Checked: true}})
	lc.completeStep("step_1", true, "")
	lc.completeAll(true, "") // should not double-complete

	if len(emitter.planStepCompletes) != 1 {
		t.Fatalf("expected 1 PlanStepComplete (no double-complete), got %d", len(emitter.planStepCompletes))
	}
}

func TestInlineStepLifecycle_CompleteAll_PropagatesErrorMessageOnFailure(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{{ID: "step_1", Summary: "A", Description: "A"}},
	})

	lc := newInlineStepLifecycle(emitter, bb)
	lc.onChecklistUpdate("step_1", []agent.TodoItem{{Text: "A", Checked: false}})
	// Finish fallback on failure — error message must propagate.
	lc.completeAll(false, "context canceled")

	if len(emitter.planStepCompletes) != 1 {
		t.Fatalf("expected 1 PlanStepComplete, got %d", len(emitter.planStepCompletes))
	}
	if emitter.planStepCompletes[0].success {
		t.Errorf("expected success=false, got true")
	}
	if emitter.planStepCompletes[0].errMsg != "context canceled" {
		t.Errorf("expected errMsg 'context canceled', got %q", emitter.planStepCompletes[0].errMsg)
	}
}

func TestInlineStepLifecycle_NilEmitterIsSafe(t *testing.T) {
	lc := newInlineStepLifecycle(nil, orchestration.NewMapBlackboard())
	lc.onChecklistUpdate("step_1", []agent.TodoItem{{Text: "A"}})
	lc.completeStep("step_1", true, "")
	lc.completeAll(true, "")
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
