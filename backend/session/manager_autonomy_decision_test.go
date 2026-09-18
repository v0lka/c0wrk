package session

import (
	"context"
	"testing"
	"time"

	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
)

// TestEmitAutonomyDecision_StepScoping pins the transcript placement of an
// automatic (no-human) security decision: the notice must carry the
// delegation/plan-step scope of the executor that took it, so the frontend
// nests it under that executor's chat block instead of the main stream.
//
//   - a subagent executor's context carries its scope (agent.RunSubAgent
//     stamps it for every subagent launch — delegated, async and plan-step
//     waves alike);
//   - a root executor has no context scope, but while the Conductor runs a
//     plan step inline the session emitter's current step is the fallback;
//   - the context scope wins over the inline fallback (a delegated subagent
//     deciding while the root Conductor is itself inside an inline step must
//     nest under the subagent, not the step);
//   - no scope at all ⇒ empty plan_step_id ⇒ the notice renders in the main
//     chat stream (a root-level decision).
func TestEmitAutonomyDecision_StepScoping(t *testing.T) {
	eventChan := make(chan Event, 64)
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "done"}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	t.Cleanup(mgr.Shutdown)

	info, err := mgr.CreateSession(testProjectID, runtimeTempDir(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// The live session's emitter, for driving the inline-step fallback.
	mgr.mu.RLock()
	sess := mgr.sessions[info.ID]
	mgr.mu.RUnlock()
	if sess == nil {
		t.Fatal("session not registered after CreateSession")
	}

	payload := coretools.AutonomyDecision{
		Kind:    "tool_confirm",
		Mode:    coretools.AutonomyModeSilent,
		Policy:  "allow",
		Verdict: "allow",
		Tool:    "bash_exec",
	}

	emitted := func(t *testing.T) AutonomyDecisionData {
		t.Helper()
		evt, ok := waitForEvent(eventChan, EventAutonomyDecision, time.Second)
		if !ok {
			t.Fatal("autonomy_decision event was not emitted")
		}
		data, ok := evt.Data.(AutonomyDecisionData)
		if !ok {
			t.Fatalf("event Data = %T, want AutonomyDecisionData", evt.Data)
		}
		return data
	}

	// 1. Subagent executor: the delegation/plan-step context scope nests the
	// notice under the subagent's block.
	mgr.EmitAutonomyDecision(agent.WithStepID(context.Background(), "d7"), info.ID, payload)
	if got := emitted(t); got.PlanStepID != "d7" {
		t.Errorf("subagent decision PlanStepID = %q, want %q", got.PlanStepID, "d7")
	} else if got.Verdict != payload.Verdict || got.Tool != payload.Tool {
		t.Errorf("scoping must not disturb the payload: got %+v, want verdict %q tool %q", got, payload.Verdict, payload.Tool)
	}

	// 2. Root executor inside an inline step: no context scope, so the session
	// emitter's current step is the fallback.
	sess.emitter.SetCurrentStepID("step_2")
	mgr.EmitAutonomyDecision(context.Background(), info.ID, payload)
	if got := emitted(t); got.PlanStepID != "step_2" {
		t.Errorf("inline root decision PlanStepID = %q, want %q", got.PlanStepID, "step_2")
	}

	// 3. Precedence: a subagent context scope beats the inline fallback even
	// while the root Conductor is inside an inline step.
	mgr.EmitAutonomyDecision(agent.WithStepID(context.Background(), "d9"), info.ID, payload)
	if got := emitted(t); got.PlanStepID != "d9" {
		t.Errorf("ctx scope must win over the inline fallback: PlanStepID = %q, want %q", got.PlanStepID, "d9")
	}

	// 4. No scope at all: a root-level decision stays in the main stream.
	sess.emitter.SetCurrentStepID("")
	mgr.EmitAutonomyDecision(context.Background(), info.ID, payload)
	if got := emitted(t); got.PlanStepID != "" {
		t.Errorf("root decision PlanStepID = %q, want empty", got.PlanStepID)
	}
}
