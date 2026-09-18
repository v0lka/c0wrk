package backend

import (
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// TestAutonomyStepLimitDecisionData pins the auditable record built for an
// autonomously resolved step-limit boundary: the posture (always silent — the
// step-limit gate only resolves without a human there), the sub-policy, the
// verdict, the boundary category, the position, and the deciding
// justification — the fields the frontend surfaces as the non-blocking
// autonomy_decision notice (ASI10).
func TestAutonomyStepLimitDecisionData(t *testing.T) {
	req := sdktools.StepLimitJudgeRequest{
		CurrentStep:   4,
		MaxSteps:      4,
		AbortCategory: sdktools.LoopBoundaryCircuitBreaker,
		AbortReason:   "fruitless detector tripped",
	}
	d := autonomyStepLimitDecisionData(req, agent.StepLimitDeny, "too many failed retries", config.SilentStepLimitAuto)

	if d.Kind != autonomyDecisionKindStepLimit {
		t.Errorf("Kind = %q, want %q", d.Kind, autonomyDecisionKindStepLimit)
	}
	if d.Verdict != string(agent.StepLimitDeny) {
		t.Errorf("Verdict = %q, want %q", d.Verdict, agent.StepLimitDeny)
	}
	if d.Mode != coretools.AutonomyModeSilent {
		t.Errorf("Mode = %q, want %q (the step-limit gate only resolves without a human in silent mode)", d.Mode, coretools.AutonomyModeSilent)
	}
	if d.Policy != config.SilentStepLimitAuto {
		t.Errorf("Policy = %q, want %q", d.Policy, config.SilentStepLimitAuto)
	}
	if d.Category != sdktools.LoopBoundaryCircuitBreaker {
		t.Errorf("Category = %q, want %q", d.Category, sdktools.LoopBoundaryCircuitBreaker)
	}
	if d.Reason != "fruitless detector tripped" {
		t.Errorf("Reason = %q, want the circuit-breaker reason", d.Reason)
	}
	if d.Justification != "too many failed retries" {
		t.Errorf("Justification = %q, want the deciding rationale", d.Justification)
	}
	if d.CurrentStep != 4 || d.MaxSteps != 4 {
		t.Errorf("position = %d/%d, want 4/4", d.CurrentStep, d.MaxSteps)
	}
	if d.Tool != "" {
		t.Errorf("Tool = %q, want empty for a step-limit decision", d.Tool)
	}
}

// TestAutonomyStepLimitDecisionData_BudgetHasNoReason pins that a plain
// step-budget exhaustion (no circuit breaker) carries an empty Reason and the
// "budget" category, so the notice does not invent a trigger.
func TestAutonomyStepLimitDecisionData_BudgetHasNoReason(t *testing.T) {
	req := sdktools.StepLimitJudgeRequest{
		CurrentStep:   20,
		MaxSteps:      20,
		AbortCategory: sdktools.LoopBoundaryBudget,
	}
	d := autonomyStepLimitDecisionData(req, agent.StepLimitAllowMore, "fresh budget granted", config.SilentStepLimitAuto)

	if d.Reason != "" {
		t.Errorf("Reason = %q, want empty for a plain budget exhaustion", d.Reason)
	}
	if d.Category != sdktools.LoopBoundaryBudget {
		t.Errorf("Category = %q, want %q", d.Category, sdktools.LoopBoundaryBudget)
	}
	if d.Verdict != string(agent.StepLimitAllowMore) {
		t.Errorf("Verdict = %q, want %q", d.Verdict, agent.StepLimitAllowMore)
	}
}
