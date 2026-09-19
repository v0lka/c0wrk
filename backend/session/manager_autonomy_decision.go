package session

import (
	"context"

	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
)

// defaultAutonomyPolicy derives the deciding sub-policy for a decision that
// reached the emit boundary without one (audit completeness §6.3: 5 of 8
// TRUE_DENY corpus events carried policy=None — the audit trail lost the
// mechanism that decided). tool_confirm and assisted_deny decisions are taken
// by the strict judge ("judge"); a step_limit boundary resolves under the
// "auto" sub-policy (kind literal mirrors backend's unexported
// autonomyDecisionKindStepLimit — the session package owns the wire view).
func defaultAutonomyPolicy(kind string) string {
	switch kind {
	case "step_limit":
		return "auto"
	default:
		return coretools.SilentToolConfirmJudge
	}
}

// EmitAutonomyDecision reports an automatic (no-human) security decision taken
// under an automatic autonomy posture (assisted or silent). It is the
// non-blocking audit record of a gate a human would otherwise have answered —
// a confirmation-gated tool call the registry resolved without a card, or a
// step-limit boundary resolved without the blocking prompt — so the run's
// trajectory stays reconstructable (OWASP ASI10).
//
// The event is routed through the session's live emitter when one exists (so
// the notice rides the same pipeline as every other session event) and falls
// back to the raw pipeline otherwise. Unlike the strict-judge phase telemetry,
// it IS persisted: the audit trail must survive a reload. Best-effort — a
// missing session only logs at debug level and never blocks the decision.
//
// ctx is the executor context the decision was taken under. It locates the
// notice in the transcript: a subagent executor's context carries its
// delegation/plan-step scope (stamped by agent.RunSubAgent), so the notice
// nests under that subagent's chat block instead of the main stream — exactly
// where the subagent's own tool_call events land. A root executor has no
// context scope; the session emitter's current inline step (SetCurrentStepID)
// is then used as the fallback, and a decision with no scope at all renders in
// the main chat stream.
func (m *Manager) EmitAutonomyDecision(ctx context.Context, sessionID string, payload AutonomyDecisionData) {
	if payload.PlanStepID == "" {
		payload.PlanStepID = agent.StepIDFromContext(ctx)
	}
	// Audit completeness (§6.3): every autonomy_decision names its deciding
	// sub-policy. All current emitters fill Policy; this funnel-level default
	// guarantees the field for any future emission path that forgets, and
	// logs so the gap stays visible instead of being silently masked.
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
			// Root-executor fallback: no subagent context scope, but the
			// Conductor is executing a plan step inline — attribute the notice
			// to that step so it nests with the step's other events.
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
