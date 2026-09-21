package session

import (
	"context"
	"testing"
	"time"

	coretools "github.com/v0lka/c0wrk/core/tools"
)

// TestEmitAutonomyDecision_PolicyGuaranteed pins the audit-completeness
// contract (§6.3): every autonomy_decision event names its deciding
// sub-policy. The corpus audit found 5 of 8 TRUE_DENY events with
// policy=None — the audit trail lost the mechanism that decided. All current
// emitters fill Policy; the emit funnel must guarantee the field for any
// future emission path that forgets, defaulting it from the decision kind.
func TestEmitAutonomyDecision_PolicyGuaranteed(t *testing.T) {
	eventChan := make(chan Event, 64)
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "done"}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	t.Cleanup(mgr.Shutdown)

	info, err := mgr.CreateSession(testProjectID, runtimeTempDir(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	emit := func(payload coretools.AutonomyDecision) AutonomyDecisionData {
		t.Helper()
		mgr.EmitAutonomyDecision(context.Background(), info.ID, payload)
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

	// A tool-gate decision (either kind) that reaches the boundary without a
	// policy defaults to "judge" — the strict judge is the only decider for
	// tool gates when no sub-policy is named.
	for _, kind := range []string{"tool_confirm", "assisted_deny"} {
		got := emit(coretools.AutonomyDecision{Kind: kind, Mode: coretools.AutonomyModeSilent, Verdict: "deny", Tool: "bash_exec"})
		if got.Policy != coretools.SilentToolConfirmJudge {
			t.Errorf("kind %s: policy = %q, want the judge default %q", kind, got.Policy, coretools.SilentToolConfirmJudge)
		}
	}

	// A step-limit boundary defaults to the auto sub-policy.
	got := emit(coretools.AutonomyDecision{Kind: "step_limit", Mode: coretools.AutonomyModeSilent, Verdict: "allow_more"})
	if got.Policy != "auto" {
		t.Errorf("step_limit: policy = %q, want %q", got.Policy, "auto")
	}

	// An explicit policy passes through untouched.
	got = emit(coretools.AutonomyDecision{
		Kind:    "tool_confirm",
		Mode:    coretools.AutonomyModeSilent,
		Policy:  coretools.SilentToolConfirmAllow,
		Verdict: "allow",
		Tool:    "bash_exec",
	})
	if got.Policy != coretools.SilentToolConfirmAllow {
		t.Errorf("explicit policy must pass through: policy = %q, want %q", got.Policy, coretools.SilentToolConfirmAllow)
	}
}
