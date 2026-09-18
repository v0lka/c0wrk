package session

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
func (m *Manager) EmitAutonomyDecision(sessionID string, payload AutonomyDecisionData) {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess != nil {
		sess.mu.Lock()
		emitter := sess.emitter
		sess.mu.Unlock()
		if emitter != nil {
			emitter.AutonomyDecision(payload)
			return
		}
	}

	m.log().Debug("autonomy decision: no live emitter, falling back to raw pipeline",
		"session_id", sessionID, "kind", payload.Kind, "verdict", payload.Verdict)
	m.EmitSessionEvent(sessionID, EventAutonomyDecision, payload)
}
