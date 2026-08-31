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
	"github.com/v0lka/sp4rk/llm"
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
// re-declare. s1 is skipped via its restored successful StepResult (replayed
// as a success event), s2 runs through the wave dispatcher.
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
	// Production wires the inline lifecycle in RunConductor; Execute needs it
	// to synthesize the skipped-step success pair.
	deps.lifecycle = newInlineStepLifecycle(emitter, bb)
	planState := newPlanRunState(true) // continuable — NOT declared in this run
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
	// The skipped step must have been (re)announced as a SUCCESS to the UI.
	foundS1Success := false
	for _, pc := range emitter.planStepCompletes {
		if pc.stepID == "s1" {
			if !pc.success {
				t.Errorf("skipped step s1 must replay success, got errMsg=%q", pc.errMsg)
			}
			foundS1Success = true
		}
	}
	if !foundS1Success {
		t.Error("expected a synthesized success PlanStepComplete for skipped s1")
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

// TestInlineStepLifecycle_CompleteAll_ContinuableReplaysRestoredSuccess: on a
// continuable resume the finish-fallback sweep must NOT repaint a step that
// already succeeded in a previous run — it replays success — while genuinely
// unreached steps get this run's terminal failure.
func TestInlineStepLifecycle_CompleteAll_ContinuableReplaysRestoredSuccess(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{
		{ID: "s1", Summary: "done earlier", Description: "d1"},
		{ID: "s2", Summary: "never ran", Description: "d2"},
	}})
	bb.SetStepResult("s1", "s1 output", nil, nil)

	emitter := &mockEmitter{}
	lc := newInlineStepLifecycle(emitter, bb)
	lc.planState = newPlanRunState(true) // continuable resume

	lc.completeAll(false, "run failed")

	saw := map[string]struct {
		success bool
		errMsg  string
	}{}
	for _, pc := range emitter.planStepCompletes {
		saw[pc.stepID] = struct {
			success bool
			errMsg  string
		}{pc.success, pc.errMsg}
	}
	s1, ok := saw["s1"]
	if !ok {
		t.Fatal("expected a terminal event for restored-successful s1")
	}
	if !s1.success || s1.errMsg != "" {
		t.Errorf("restored-successful s1 must replay success, got success=%v errMsg=%q", s1.success, s1.errMsg)
	}
	s2, ok := saw["s2"]
	if !ok {
		t.Fatal("expected a terminal event for unreached s2")
	}
	if s2.success || s2.errMsg != "run failed" {
		t.Errorf("unreached s2 must carry the run failure, got success=%v errMsg=%q", s2.success, s2.errMsg)
	}
}

// TestResumeContinuationDirective_Content pins the semantic payload of the
// message-level directive Resume appends on a continuable resume: continue the
// approved plan via execute_plan (never a re-declare), resume paused
// delegations by re-invoking delegate with the same task id, and frame the
// pause as a clean checkpoint — not an error.
func TestResumeContinuationDirective_Content(t *testing.T) {
	for _, want := range []string{
		"execute_plan", // continue the plan via the executor tool…
		"declare_plan", // …never re-declare it
		"delegate",     // resume paused delegations…
		"same task id", // …by their original id (checkpoints picked up)
		"not an error", // a pause is a clean checkpoint
	} {
		if !strings.Contains(resumeContinuationDirective, want) {
			t.Errorf("resumeContinuationDirective must mention %q\ndirective:\n%s", want, resumeContinuationDirective)
		}
	}
}

// TestResume_ContinuationDirective_TogglesWithPlanState proves end-to-end
// (Resume → runConductor → Conductor.SetTask) that the continuation directive
// rides on the resumed Conductor's task message exactly when the plan is
// continuable: present for a paused-with-unreached-steps resume (with the
// original request still intact), absent for plan-less and fully-completed
// resumes, and never leaked into the stored conversation history.
func TestResume_ContinuationDirective_TogglesWithPlanState(t *testing.T) {
	run := func(t *testing.T, bb orchestration.Blackboard) (*seedableRecordingCM, *Orchestrator) {
		t.Helper()
		mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
			executorFinishResponse("resumed output"),
		}}
		var cm *seedableRecordingCM
		orch := newResumeTestOrchestrator(t, mockLLM, &spyEmitter{}, &cm)
		if _, err := orch.Resume(context.Background(), bb, nil, "", nil, nil, ""); err != nil {
			t.Fatalf("Resume failed: %v", err)
		}
		if cm == nil {
			t.Fatal("context manager was never created")
		}
		return cm, orch
	}

	continuableBB := func() orchestration.Blackboard {
		bb := orchestration.NewMapBlackboard()
		bb.SetOriginalRequest("long running task")
		bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{
			{ID: "s1", Summary: "done earlier"},
			{ID: "s2", Summary: "still pending"},
		}})
		// s1 succeeded in the PREVIOUS run; s2 never ran → continuable.
		bb.SetStepResult("s1", "s1 output", nil, nil)
		return bb
	}

	t.Run("continuable resume attaches the directive", func(t *testing.T) {
		cm, orch := run(t, continuableBB())
		if !strings.Contains(cm.taskDefinition, "Resume Continuation") {
			t.Errorf("continuable resume must append the continuation directive, task = %q", cm.taskDefinition)
		}
		if !strings.Contains(cm.taskDefinition, "long running task") {
			t.Errorf("task message must keep the original request, task = %q", cm.taskDefinition)
		}
		// The directive is per-run message decoration, not conversation
		// history: no history message may carry it.
		for _, msg := range orch.historySnapshot() {
			if strings.Contains(msg.Content, "Resume Continuation") {
				t.Errorf("continuation directive leaked into history message (role=%s): %q", msg.Role, msg.Content)
			}
		}
	})

	t.Run("plan-less resume carries no directive", func(t *testing.T) {
		bb := orchestration.NewMapBlackboard()
		bb.SetOriginalRequest("long running task")
		cm, _ := run(t, bb)
		if strings.Contains(cm.taskDefinition, "Resume Continuation") {
			t.Errorf("plan-less resume must not carry the directive, task = %q", cm.taskDefinition)
		}
	})

	t.Run("fully-completed plan carries no directive", func(t *testing.T) {
		bb := continuableBB()
		bb.SetStepResult("s2", "s2 output", nil, nil) // all steps succeeded
		cm, _ := run(t, bb)
		if strings.Contains(cm.taskDefinition, "Resume Continuation") {
			t.Errorf("completed-plan resume must not carry the directive, task = %q", cm.taskDefinition)
		}
	})
}
