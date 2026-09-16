package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// newAbandonHarness wires the production plan plumbing around a fresh run: a
// shared planRunState, a conductorPublisher (the declare_plan sink), and a
// conductorLauncher (the PlanChecker + execute_plan executor). This mirrors the
// wiring in RunConductor closely enough to exercise the abandon path end-to-end.
func newAbandonHarness(t *testing.T) (*conductorPublisher, *conductorLauncher, *planRunState, orchestration.Blackboard) {
	t.Helper()
	bb := orchestration.NewMapBlackboard()
	planState := newPlanRunState(false)
	publisher := &conductorPublisher{
		emitter:   &mockEmitter{},
		bb:        bb,
		plansDir:  t.TempDir(),
		planState: planState,
	}
	launcher := &conductorLauncher{bb: bb, planState: planState}
	return publisher, launcher, planState, bb
}

// runAwaitApprovalDeclare executes declare_plan in await_approval mode against
// the harness, with an approval callback that answers with the given decision.
func runAwaitApprovalDeclare(t *testing.T, publisher *conductorPublisher, launcher *conductorLauncher, decision string) sdktools.ToolResult {
	t.Helper()
	tool := tools.NewDeclarePlanTool(func(_ context.Context, _, _ string) (string, string, error) {
		return decision, "", nil
	})
	ctx := tools.WithPlanPublisher(context.Background(), publisher)
	ctx = tools.WithPlanChecker(ctx, launcher)
	raw, err := json.Marshal(map[string]any{
		"mode":  "await_approval",
		"tasks": []map[string]any{{"id": "step_1", "summary": "A", "description": "Do A"}},
	})
	if err != nil {
		t.Fatalf("marshal declare_plan input: %v", err)
	}
	res, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("declare_plan returned a Go error: %v", err)
	}
	return res
}

// TestAbandon_ReleasesPlanWorkflow is the end-to-end pin for the abandon fix:
// publishing a plan for review marks the run's plan workflow active, so an
// abandoned plan must release it — otherwise the run stays locked (delegate
// disabled, standalone checklist rejected, execute_plan willing to run the
// abandoned draft).
func TestAbandon_ReleasesPlanWorkflow(t *testing.T) {
	publisher, launcher, planState, bb := newAbandonHarness(t)

	res := runAwaitApprovalDeclare(t, publisher, launcher, "abandon")
	if !res.IsError {
		t.Fatalf("abandon must surface as an error result, got: %+v", res)
	}

	// 1. The run is released: HasDeclaredPlan false, planState not declared.
	if launcher.HasDeclaredPlan() {
		t.Error("abandon must release the plan workflow: HasDeclaredPlan must be false")
	}
	if planState.isDeclared() {
		t.Error("abandon must clear planState.declared")
	}
	if planState.isActive() {
		t.Error("abandon must leave the plan workflow inactive")
	}

	// 2. The abandoned plan stays on the blackboard as a historical artifact
	//    (non-goal: it is NOT cleared).
	if bb.GetPlan() == nil {
		t.Error("abandon must NOT clear the blackboard plan (it stays a historical artifact)")
	}

	// 3. A standalone (empty step_id) checklist is allowed again — this is the
	//    exact guard RunConductor wires.
	if msg := newChecklistGuard(planState)(""); msg != "" {
		t.Errorf("a standalone checklist must be allowed after abandon, got: %q", msg)
	}

	// 4. execute_plan refuses the abandoned (now non-active) plan, and the
	//    refusal names the abandon case (not just a "restored plan").
	if _, err := launcher.Execute(context.Background(), nil); err == nil {
		t.Error("execute_plan must refuse the abandoned plan")
	} else if !strings.Contains(err.Error(), "not declared in this run") || !strings.Contains(err.Error(), "abandoned") {
		t.Errorf("refusal should name the non-active (abandoned) plan, got: %v", err)
	}
}

// TestAbandon_SettlesPlanStepsAsTerminal pins the UI-hygiene half of the abandon
// fix: because an abandoned run is no longer plan-active, the finish-fallback
// completeAll (gated on planDeclaredInRun) no longer sweeps the abandoned plan's
// steps — so AbandonPlan must settle them itself with one terminal
// PlanStepComplete per step (not-completed, reason "plan abandoned"), or they
// would hang "pending" in the plan panel forever. Approval precedes execution,
// so no step was started: no PlanStepStart may be emitted.
func TestAbandon_SettlesPlanStepsAsTerminal(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	planState := newPlanRunState(false)
	publisher := &conductorPublisher{
		emitter:   emitter,
		bb:        bb,
		plansDir:  t.TempDir(),
		planState: planState,
	}

	if _, err := publisher.Publish(context.Background(), []tools.PlanTaskInput{
		{ID: "step_1", Summary: "A", Description: "Do A"},
		{ID: "step_2", Summary: "B", Description: "Do B"},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !planState.isActive() {
		t.Fatal("publishing must mark the plan workflow active")
	}

	publisher.AbandonPlan()

	if len(emitter.planStepCompletes) != len(bb.GetPlan().Steps) {
		t.Fatalf("AbandonPlan must settle every plan step, got %d completes for %d steps",
			len(emitter.planStepCompletes), len(bb.GetPlan().Steps))
	}
	for _, c := range emitter.planStepCompletes {
		if c.success {
			t.Errorf("step %q must be settled NOT-completed, got success=true", c.stepID)
		}
		if c.errMsg != "plan abandoned" {
			t.Errorf("step %q: expected reason %q, got %q", c.stepID, "plan abandoned", c.errMsg)
		}
	}
	if len(emitter.planStepStarts) != 0 {
		t.Errorf("abandon must NOT emit PlanStepStart (no step ran), got %d", len(emitter.planStepStarts))
	}
}

// TestAbandonPlan_NilSafety: AbandonPlan must be safe without a plan state or an
// emitter (direct test construction) — both halves degrade to no-ops.
func TestAbandonPlan_NilSafety(t *testing.T) {
	p := &conductorPublisher{}
	p.AbandonPlan() // nil planState + nil emitter: must not panic
}

// TestApproveAndRequestChanges_KeepPlanWorkflowActive: approve and
// request_changes leave the run locked into the plan workflow (delegate
// disabled, standalone checklist rejected, execute_plan available).
func TestApproveAndRequestChanges_KeepPlanWorkflowActive(t *testing.T) {
	for _, decision := range []string{"approve", "request_changes"} {
		t.Run(decision, func(t *testing.T) {
			publisher, launcher, planState, _ := newAbandonHarness(t)

			res := runAwaitApprovalDeclare(t, publisher, launcher, decision)
			if res.IsError {
				t.Fatalf("%s must be a non-error result, got: %+v", decision, res)
			}
			if !launcher.HasDeclaredPlan() {
				t.Errorf("%s must keep the plan workflow active (HasDeclaredPlan=true)", decision)
			}
			if !planState.isDeclared() {
				t.Errorf("%s must keep planState.declared=true", decision)
			}
			if msg := newChecklistGuard(planState)(""); msg == "" {
				t.Errorf("%s must keep rejecting a standalone checklist", decision)
			}
		})
	}
}

// TestPresent_KeepsPlanWorkflowActive: present mode returns before the approval
// callback and leaves the workflow active (Publish marks it).
func TestPresent_KeepsPlanWorkflowActive(t *testing.T) {
	publisher, launcher, planState, _ := newAbandonHarness(t)

	tool := tools.NewDeclarePlanTool(nil)
	ctx := tools.WithPlanPublisher(context.Background(), publisher)
	ctx = tools.WithPlanChecker(ctx, launcher)
	raw, err := json.Marshal(map[string]any{
		"mode":  "present",
		"tasks": []map[string]any{{"id": "step_1", "summary": "A", "description": "Do A"}},
	})
	if err != nil {
		t.Fatalf("marshal declare_plan input: %v", err)
	}
	res, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("present must be a non-error result, got: %+v", res)
	}
	if !launcher.HasDeclaredPlan() {
		t.Error("present must leave the plan workflow active (HasDeclaredPlan=true)")
	}
	if !planState.isDeclared() {
		t.Error("present must leave planState.declared=true")
	}
}
