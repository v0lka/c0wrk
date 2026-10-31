package session

import (
	"context"

	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
)

// defaultAutonomyPolicy derives the deciding sub-policy for a decision that
// reached the emit boundary without one (audit §6.3 — see
// specs/contracts/event-catalog.md, autonomy_decision). An unrecognized kind
// returns the explicit "unknown" sentinel rather than a plausible-looking
// decider, so the audit gap is visible in the data itself.
func defaultAutonomyPolicy(kind string) string {
	switch kind {
	case "step_limit":
		return "auto"
	case "tool_confirm", "assisted_deny":
		return coretools.SilentToolConfirmJudge
	default:
		return "unknown"
	}
}

// EmitAutonomyDecision reports an automatic (no-human) security decision —
// the persisted, non-blocking audit record of a gate a human would otherwise
// have answered (OWASP ASI10). Routing, persistence and plan_step_id scoping
// (RunSubAgent context stamp, CurrentStepID root fallback) are specified in
// specs/decisions/053-silent-mode.md (D6) and specs/contracts/event-catalog.md
// (autonomy_decision). Best-effort: a missing session logs at debug, never
// blocks the decision.
func (m *Manager) EmitAutonomyDecision(ctx context.Context, sessionID string, payload AutonomyDecisionData) {
	if payload.PlanStepID == "" {
		payload.PlanStepID = agent.StepIDFromContext(ctx)
	}
	if payload.Policy == "" {
		payload.Policy = defaultAutonomyPolicy(payload.Kind)
		m.log().Warn("autonomy decision carried no policy; defaulted at the emit boundary",
			"session_id", sessionID, "kind", payload.Kind, "policy", payload.Policy)
	}
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess != nil {
		sess.mu.Lock()
		emitter := sess.emitter
		sess.mu.Unlock()
		if emitter != nil {
			if payload.PlanStepID == "" {
				payload.PlanStepID = emitter.CurrentStepID()
			}
			emitter.AutonomyDecision(payload)
			return
		}
	}

	m.log().Debug("autonomy decision: no live emitter, falling back to raw pipeline",
		"session_id", sessionID, "kind", payload.Kind, "verdict", payload.Verdict)
	m.EmitSessionEvent(sessionID, EventAutonomyDecision, payload)
}
