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

func TestInlineStepLifecycle_CompleteStep_UnknownStepNotInPlanIsNoop(t *testing.T) {
	emitter := &mockEmitter{}
	lc := newInlineStepLifecycle(emitter, orchestration.NewMapBlackboard())

	// Complete a step that is not in any declared plan — should be a no-op
	// (no plan panel entry to update, nothing to synthesize).
	lc.completeStep("unknown_step", true, "")

	if len(emitter.planStepCompletes) != 0 {
		t.Errorf("expected 0 PlanStepComplete for unknown step, got %d", len(emitter.planStepCompletes))
	}
	if len(emitter.planStepStarts) != 0 {
		t.Errorf("expected 0 PlanStepStart for unknown step, got %d", len(emitter.planStepStarts))
	}
}

func TestInlineStepLifecycle_CompleteStep_NotStartedInPlanSynthesizesStartAndComplete(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{{ID: "step_4", Summary: "Verify build", Description: "Verify build, tests, lint"}},
	})

	lc := newInlineStepLifecycle(emitter, bb)

	// The Conductor finished the step inline but forgot update_checklist, so
	// PlanStepStart was never inferred. completeStep must synthesize it so
	// the step transitions pending→running→completed instead of being stuck.
	lc.completeStep("step_4", true, "")

	if len(emitter.planStepStarts) != 1 {
		t.Fatalf("expected 1 synthesized PlanStepStart, got %d", len(emitter.planStepStarts))
	}
	if emitter.planStepStarts[0].stepID != "step_4" {
		t.Errorf("expected step_id 'step_4', got %q", emitter.planStepStarts[0].stepID)
	}
	if emitter.planStepStarts[0].description != "Verify build, tests, lint" {
		t.Errorf("expected description from plan, got %q", emitter.planStepStarts[0].description)
	}
	if len(emitter.planStepCompletes) != 1 {
		t.Fatalf("expected 1 PlanStepComplete, got %d", len(emitter.planStepCompletes))
	}
	if emitter.planStepCompletes[0].stepID != "step_4" || !emitter.planStepCompletes[0].success {
		t.Errorf("expected step_4 success=true, got %q success=%v",
			emitter.planStepCompletes[0].stepID, emitter.planStepCompletes[0].success)
	}
	// Start must be emitted before complete so the frontend opens the step
	// container before the terminal status arrives.
	if len(emitter.eventOrder) != 2 ||
		emitter.eventOrder[0] != "plan_step_start" ||
		emitter.eventOrder[1] != "plan_step_complete" {
		t.Errorf("expected [plan_step_start, plan_step_complete], got %v", emitter.eventOrder)
	}
}

func TestInlineStepLifecycle_CompleteStep_NotStartedThenCompleteAllDoesNotDoubleComplete(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{{ID: "step_4", Summary: "Verify", Description: "Verify"}},
	})

	lc := newInlineStepLifecycle(emitter, bb)
	// Synthesized-complete a never-started step, then run the finish fallback.
	lc.completeStep("step_4", true, "")
	lc.completeAll(true, "")

	if len(emitter.planStepCompletes) != 1 {
		t.Fatalf("expected 1 PlanStepComplete (no double-complete), got %d", len(emitter.planStepCompletes))
	}
	if len(emitter.planStepStarts) != 1 {
		t.Fatalf("expected 1 PlanStepStart (no double-start), got %d", len(emitter.planStepStarts))
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

func TestInlineStepLifecycle_CompleteAll_AutoCompletesNeverStartedPlanSteps(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Summary: "A", Description: "Desc A"},
			{ID: "step_2", Summary: "B", Description: "Desc B"},
			{ID: "step_3", Summary: "C", Description: "Desc C"},
		},
	})

	lc := newInlineStepLifecycle(emitter, bb)

	// None of the steps were started (the Conductor forgot update_checklist
	// for all of them and never called declare_step_complete). The finish
	// fallback must synthesize start+complete for every plan step so none
	// are left stuck in "pending".
	lc.completeAll(true, "")

	if len(emitter.planStepCompletes) != 3 {
		t.Fatalf("expected 3 PlanStepComplete (one per plan step), got %d", len(emitter.planStepCompletes))
	}
	if len(emitter.planStepStarts) != 3 {
		t.Fatalf("expected 3 synthesized PlanStepStart, got %d", len(emitter.planStepStarts))
	}
	// Each synthesized start must carry the step's plan description and be
	// immediately followed by its complete.
	for i, s := range emitter.planStepStarts {
		if s.description == "" {
			t.Errorf("planStepStart[%d] (%q): expected description from plan, got empty", i, s.stepID)
		}
	}
	// eventOrder must alternate start→complete for each step.
	for i := 0; i < len(emitter.eventOrder); i += 2 {
		if emitter.eventOrder[i] != "plan_step_start" || emitter.eventOrder[i+1] != "plan_step_complete" {
			t.Errorf("expected plan_step_start→plan_step_complete pair at index %d, got %v", i, emitter.eventOrder)
		}
	}
}

func TestInlineStepLifecycle_CompleteAll_MixedStartedAndNeverStartedSteps(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Summary: "A", Description: "A"},
			{ID: "step_2", Summary: "B", Description: "B"},
			{ID: "step_3", Summary: "C", Description: "C"},
		},
	})

	lc := newInlineStepLifecycle(emitter, bb)

	// step_1 was started via checklist; step_2 and step_3 were never touched.
	lc.onChecklistUpdate("step_1", []agent.TodoItem{{Text: "A", Checked: false}})
	lc.completeAll(true, "")

	// 3 completes total: step_1 (from startedPending) + step_2, step_3 (synthesized).
	if len(emitter.planStepCompletes) != 3 {
		t.Fatalf("expected 3 PlanStepComplete, got %d", len(emitter.planStepCompletes))
	}
	// 3 starts total: step_1 (from checklist) + step_2, step_3 (synthesized).
	// completeAll must NOT synthesize a start for step_1 — it was already started.
	if len(emitter.planStepStarts) != 3 {
		t.Fatalf("expected 3 PlanStepStart (1 checklist + 2 synthesized), got %d", len(emitter.planStepStarts))
	}
	// The first start is step_1's, emitted before its step_todo_update (from
	// the checklist), proving it is not a completeAll synthesis.
	if emitter.planStepStarts[0].stepID != "step_1" {
		t.Errorf("expected first start to be step_1 (from checklist), got %q", emitter.planStepStarts[0].stepID)
	}
	if len(emitter.eventOrder) < 2 || emitter.eventOrder[0] != "plan_step_start" || emitter.eventOrder[1] != "step_todo_update" {
		t.Errorf("expected step_1 start before step_todo_update, got %v", emitter.eventOrder)
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

func TestInlineStepLifecycle_OnChecklistUpdate_AfterCompleteDoesNotRestart(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{{ID: "step_1", Summary: "S", Description: "D"}},
	})

	lc := newInlineStepLifecycle(emitter, bb)
	lc.onChecklistUpdate("step_1", []agent.TodoItem{{Text: "A", Checked: false}})
	lc.completeStep("step_1", true, "")
	// A late checklist update arriving after completion must not re-Start
	// the step (which would otherwise leave it in started and cause
	// completeAll to double-complete it).
	lc.onChecklistUpdate("step_1", []agent.TodoItem{{Text: "A", Checked: true}})

	if len(emitter.planStepStarts) != 1 {
		t.Errorf("expected 1 PlanStepStart (no re-start after complete), got %d", len(emitter.planStepStarts))
	}
	if len(emitter.planStepCompletes) != 1 {
		t.Errorf("expected 1 PlanStepComplete, got %d", len(emitter.planStepCompletes))
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
