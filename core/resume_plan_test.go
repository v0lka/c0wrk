package core

// Tests for the continuable-resume plan workflow: a paused task whose approved
// plan still has unreached steps resumes with the plan workflow ACTIVE —
// execute_plan runs without a re-declare, declare_plan soft-hints, and the
// lifecycle never repaints previously-succeeded steps with this run's
// terminal failure. A plan restored from a previous COMPLETED task (neither
// declared in this run nor continuable) stays refused.

import (
	"context"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/orchestration"
)

func TestPlanRunState_IsActive(t *testing.T) {
	cases := []struct {
		name        string
		continuable bool
		declare     bool
		wantActive  bool
	}{
		{"zero value (restored completed plan)", false, false, false},
		{"declared this run", false, true, true},
		{"continuable resume", true, false, true},
		{"both", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := newPlanRunState(tc.continuable)
			if tc.declare {
				ps.markDeclared()
			}
			if got := ps.isActive(); got != tc.wantActive {
				t.Errorf("isActive() = %v, want %v", got, tc.wantActive)
			}
			if got := ps.isContinuable(); got != tc.continuable {
				t.Errorf("isContinuable() = %v, want %v", got, tc.continuable)
			}
		})
	}
}

// TestConductorLauncher_PlanContinuation verifies the capability declare_plan
// consults: true only when the run was seeded as a continuable resume.
func TestConductorLauncher_PlanContinuation(t *testing.T) {
	bb := orchestration.NewMapBlackboard()

	fresh := &conductorLauncher{bb: bb, planState: newPlanRunState(false)}
	if fresh.PlanContinuation() {
		t.Error("fresh run must not report PlanContinuation")
	}

	cont := &conductorLauncher{bb: bb, planState: newPlanRunState(true)}
	if !cont.PlanContinuation() {
		t.Error("continuable run must report PlanContinuation")
	}
	if !cont.HasDeclaredPlan() {
		t.Error("continuable run must report HasDeclaredPlan=true (plan workflow active)")
	}

	// Nil planState (direct test construction) — no continuation, fallback path.
	bare := &conductorLauncher{bb: bb}
	if bare.PlanContinuation() {
		t.Error("nil planState must not report PlanContinuation")
	}
}

// TestHasDeclaredPlan_ContinuableResume locks the delegate-guard behavior: on
// a continuable resume the plan workflow is ACTIVE, so delegate stays disabled
// exactly like after a fresh declare_plan.
func TestHasDeclaredPlan_ContinuableResume(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "s1", Summary: "old"}}})

	l := &conductorLauncher{bb: bb, planState: newPlanRunState(true)}
	if !l.HasDeclaredPlan() {
		t.Error("continuable resume must report HasDeclaredPlan=true")
	}

	// Contrast: a plan merely restored from a completed task does not activate.
	l2 := &conductorLauncher{bb: bb, planState: newPlanRunState(false)}
	if l2.HasDeclaredPlan() {
		t.Error("restored (completed) plan must not report HasDeclaredPlan=true")
	}
}

// TestExecute_ContinuableResume_ProceedsWithoutRedeclare is the core fix: a
// paused task's approved plan (s1 succeeded, s2 never ran) executes WITHOUT a
// re-declare. s1 is skipped via its restored successful StepResult — silently
// (issue #99: its terminal events were emitted by the run that executed it,
// so the resume wave's Execute emits none) — while s2 still runs through the
// wave dispatcher.
func TestExecute_ContinuableResume_ProceedsWithoutRedeclare(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{
		{ID: "s1", Summary: "done earlier"},
		{ID: "s2", Summary: "still pending"},
	}})
	// s1 succeeded in the PREVIOUS run (restored StepResult, error-free).
	bb.SetStepResult("s1", "s1 output", nil, nil)

	emitter := &mockEmitter{}
	deps := conductorDeps{emitter: emitter}
	// Production wires the inline lifecycle in RunConductor and shares the
	// planRunState between it and the launcher; mirror that wiring here — a
	// nil lifecycle planState would take the direct-construction fallback and
	// wrongly treat the restored plan as declared in this run.
	deps.lifecycle = newInlineStepLifecycle(emitter, bb)
	planState := newPlanRunState(true) // continuable — NOT declared in this run
	deps.lifecycle.planState = planState
	rec := &waveRecorder{}
	l := &conductorLauncher{
		deps:            deps,
		bb:              bb,
		planState:       planState,
		runPlanStepWave: rec.dispatch,
	}

	results, err := l.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected continuable resume to execute without re-declare, got error: %v", err)
	}
	if rec.calls != 1 {
		t.Errorf("expected 1 dispatch call, got %d", rec.calls)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}
	byID := map[string]tools.PlanStepResult{}
	for _, r := range results {
		byID[r.StepID] = r
	}
	if byID["s1"].Status != "completed" || byID["s1"].Output != "s1 output" {
		t.Errorf("s1 should be skipped-replayed as completed, got %+v", byID["s1"])
	}
	if byID["s2"].Status != "completed" {
		t.Errorf("s2 should have run, got %+v", byID["s2"])
	}
	// Issue #99 pin: the resume wave's Execute emits NO events for
	// durable-success steps. The wave stub emits nothing for s2 either, so
	// the emitter must stay empty overall.
	if len(emitter.planStepStarts) != 0 {
		t.Errorf("resume Execute must emit no PlanStepStart for durable-success steps, got %+v", emitter.planStepStarts)
	}
	if len(emitter.planStepCompletes) != 0 {
		t.Errorf("resume Execute must emit no PlanStepComplete for durable-success steps, got %+v", emitter.planStepCompletes)
	}
	// The terminal state is still recorded, just silently.
	if !deps.lifecycle.isCompleted("s1") {
		t.Error("skipped durable-success s1 must be settled silently in the lifecycle")
	}
}

// TestExecute_RestoredFullyCompletedPlan_Refused is the restart-refusal
// counterpart: every step of the restored plan already succeeded, the plan is
// neither declared in this run nor continuable — Execute refuses even though
// the plan looks "fully done" (re-running would duplicate side effects).
func TestExecute_RestoredFullyCompletedPlan_Refused(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "s1", Summary: "old"}}})
	bb.SetStepResult("s1", "s1 output", nil, nil)

	planState := newPlanRunState(false) // restored from a COMPLETED task
	rec := &waveRecorder{}
	l := &conductorLauncher{
		deps:            conductorDeps{emitter: &mockEmitter{}},
		bb:              bb,
		planState:       planState,
		runPlanStepWave: rec.dispatch,
	}

	_, err := l.Execute(context.Background(), nil)
	if err == nil {
		t.Fatal("expected refusal for a restored fully-completed plan")
	}
	if !strings.Contains(err.Error(), "restored from a previous") {
		t.Errorf("expected 'restored from a previous' error, got: %v", err)
	}
	if rec.calls != 0 {
		t.Errorf("expected 0 dispatch calls, got %d", rec.calls)
	}
}

// TestPlanHasUnreachedSteps exercises the Resume-side computation that seeds
// conductorDeps.resumedWithPlan.
func TestPlanHasUnreachedSteps(t *testing.T) {
	newBBWithPlan := func(steps ...orchestration.PlanStep) *orchestration.MapBlackboard {
		bb := orchestration.NewMapBlackboard()
		bb.SetPlan(&orchestration.Plan{Steps: steps})
		return bb
	}

	t.Run("nil blackboard", func(t *testing.T) {
		if planHasUnreachedSteps(nil) {
			t.Error("nil blackboard must not be continuable")
		}
	})
	t.Run("no plan", func(t *testing.T) {
		if planHasUnreachedSteps(orchestration.NewMapBlackboard()) {
			t.Error("plan-less blackboard must not be continuable")
		}
	})
	t.Run("empty plan", func(t *testing.T) {
		bb := newBBWithPlan()
		if planHasUnreachedSteps(bb) {
			t.Error("empty plan must not be continuable")
		}
	})
	t.Run("all steps succeeded", func(t *testing.T) {
		bb := newBBWithPlan(
			orchestration.PlanStep{ID: "s1"},
			orchestration.PlanStep{ID: "s2"},
		)
		bb.SetStepResult("s1", "out", nil, nil)
		bb.SetStepResult("s2", "out", nil, nil)
		if planHasUnreachedSteps(bb) {
			t.Error("fully-completed plan must not be continuable")
		}
	})
	t.Run("never-run step", func(t *testing.T) {
		bb := newBBWithPlan(
			orchestration.PlanStep{ID: "s1"},
			orchestration.PlanStep{ID: "s2"},
		)
		bb.SetStepResult("s1", "out", nil, nil)
		if !planHasUnreachedSteps(bb) {
			t.Error("plan with a never-run step must be continuable")
		}
	})
	t.Run("failed step", func(t *testing.T) {
		bb := newBBWithPlan(orchestration.PlanStep{ID: "s1"})
		bb.SetStepResult("s1", "", context.DeadlineExceeded, nil)
		if !planHasUnreachedSteps(bb) {
			t.Error("plan with a failed step must be continuable")
		}
	})
}

// TestInlineStepLifecycle_CompleteAll_ContinuableReplaysRestoredSuccess pins
// the finish-fallback sweep's issue #99 contract for a step that already
// succeeded in a previous run: who owns the plan panel state decides.
//   - continuation resume (plan active, NOT declared this run): silence —
//     the terminal events came from the run that executed the step;
//   - plan re-declared in this run: the panel was reset to pending, so the
//     sweep replays the restored success as a Start+Complete(success) pair.
//
// In both cases a genuinely unreached step gets this run's terminal failure.
func TestInlineStepLifecycle_CompleteAll_ContinuableReplaysRestoredSuccess(t *testing.T) {
	newFixture := func() (*mockEmitter, *inlineStepLifecycle) {
		bb := orchestration.NewMapBlackboard()
		bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{
			{ID: "s1", Summary: "done earlier", Description: "d1"},
			{ID: "s2", Summary: "never ran", Description: "d2"},
		}})
		// s1 succeeded in the PREVIOUS run (restored StepResult, error-free).
		bb.SetStepResult("s1", "s1 output", nil, nil)
		emitter := &mockEmitter{}
		return emitter, newInlineStepLifecycle(emitter, bb)
	}

	t.Run("continuable resume: restored success stays silent", func(t *testing.T) {
		emitter, lc := newFixture()
		lc.planState = newPlanRunState(true) // continuable — NOT declared in this run

		lc.completeAll(false, "run failed")

		for _, ps := range emitter.planStepStarts {
			if ps.stepID == "s1" {
				t.Error("restored-successful s1 must not be re-Started on a continuation resume")
			}
		}
		for _, pc := range emitter.planStepCompletes {
			if pc.stepID == "s1" {
				t.Errorf("restored-successful s1 must stay silent on a continuation resume, got Complete(success=%v errMsg=%q)", pc.success, pc.errMsg)
			}
		}
		// The genuinely unreached step still gets this run's terminal failure.
		s2done := false
		for _, pc := range emitter.planStepCompletes {
			if pc.stepID == "s2" {
				s2done = true
				if pc.success || pc.errMsg != "run failed" {
					t.Errorf("unreached s2 must carry the run failure, got success=%v errMsg=%q", pc.success, pc.errMsg)
				}
			}
		}
		if !s2done {
			t.Error("expected a terminal event for unreached s2")
		}
	})

	t.Run("re-declared this run: restored success is replayed", func(t *testing.T) {
		emitter, lc := newFixture()
		planState := newPlanRunState(false)
		planState.markDeclared() // declare_plan in THIS run — panel reset to pending
		lc.planState = planState

		lc.completeAll(false, "run failed")

		starts := 0
		for _, ps := range emitter.planStepStarts {
			if ps.stepID == "s1" {
				starts++
			}
		}
		if starts != 1 {
			t.Errorf("expected exactly 1 replay PlanStepStart for restored-successful s1, got %d", starts)
		}
		completes := 0
		for _, pc := range emitter.planStepCompletes {
			if pc.stepID == "s1" {
				completes++
				if !pc.success || pc.errMsg != "" {
					t.Errorf("re-declared run must replay s1 success, got success=%v errMsg=%q", pc.success, pc.errMsg)
				}
			}
		}
		if completes != 1 {
			t.Errorf("expected exactly 1 replay PlanStepComplete for restored-successful s1, got %d", completes)
		}
		// The genuinely unreached step still gets this run's terminal failure.
		s2done := false
		for _, pc := range emitter.planStepCompletes {
			if pc.stepID == "s2" {
				s2done = true
				if pc.success || pc.errMsg != "run failed" {
					t.Errorf("unreached s2 must carry the run failure, got success=%v errMsg=%q", pc.success, pc.errMsg)
				}
			}
		}
		if !s2done {
			t.Error("expected a terminal event for unreached s2")
		}
	})
}

// TestInlineStepLifecycle_SeedCompletedFromBlackboard pins the resume-path
// seeding (issue #99): seedCompletedFromBlackboard silently settles every
// plan step carrying an error-free restored StepResult — no events — so a
// late checklist update cannot re-Start the step and neither the launcher's
// skip branch nor the finish-fallback sweep can re-announce it.
func TestInlineStepLifecycle_SeedCompletedFromBlackboard(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{
		{ID: "s1", Summary: "done earlier", Description: "d1"},
		{ID: "s2", Summary: "never ran", Description: "d2"},
		{ID: "s3", Summary: "failed earlier", Description: "d3"},
	}})
	bb.SetStepResult("s1", "s1 output", nil, nil)
	bb.SetStepResult("s3", "", context.DeadlineExceeded, nil)

	emitter := &mockEmitter{}
	lc := newInlineStepLifecycle(emitter, bb)

	lc.seedCompletedFromBlackboard()

	// Seeding itself is silent.
	if len(emitter.planStepStarts) != 0 || len(emitter.planStepCompletes) != 0 {
		t.Errorf("seeding must not emit events, got starts=%d completes=%d", len(emitter.planStepStarts), len(emitter.planStepCompletes))
	}
	// Only the restored-successful step is settled.
	if !lc.isCompleted("s1") {
		t.Error("restored-successful s1 must be seeded as completed")
	}
	if lc.isCompleted("s2") {
		t.Error("never-run s2 must not be seeded as completed")
	}
	if lc.isCompleted("s3") {
		t.Error("failed s3 must not be seeded as completed")
	}

	// A late checklist update for the seeded step must NOT re-emit
	// PlanStepStart (the onChecklistUpdate re-Start protection).
	lc.onChecklistUpdate("s1", []agent.TodoItem{{Text: "leftover item"}})
	if len(emitter.planStepStarts) != 0 {
		t.Errorf("seeded step must not be re-Started by a late checklist update, got %+v", emitter.planStepStarts)
	}
	if len(emitter.stepTodoUpdates) != 1 || emitter.stepTodoUpdates[0].stepID != "s1" {
		t.Errorf("checklist update itself must still flow through, got %+v", emitter.stepTodoUpdates)
	}

	// Contrast: an unseeded step still infers PlanStepStart from its first
	// checklist update — the suppression comes from the seeding, not from a
	// global mute of the lifecycle.
	lc.onChecklistUpdate("s2", []agent.TodoItem{{Text: "fresh item"}})
	if len(emitter.planStepStarts) != 1 || emitter.planStepStarts[0].stepID != "s2" {
		t.Errorf("unseeded s2 must infer PlanStepStart from its first checklist update, got %+v", emitter.planStepStarts)
	}
}

// TestResumeContinuationDirective_Content and
// TestResume_ContinuationDirective_TogglesWithPlanState were removed together
// with resumeContinuationDirective: resumption is now system-driven — the
// Resume auto-resume wave settles paused work before the first LLM call and
// appends a factual summary (covered by the wave tests) instead of instructing
// the model to re-invoke delegate/execute_plan.
