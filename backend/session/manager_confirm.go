package session

// EmitToolConfirm routes a confirmation through the session emitter so the
// runtime status reports "Awaiting confirmation..." while the agent blocks; a
// session with no live emitter falls back to the raw event pipeline. Transient
// (persister skips it): specs/contracts/event-catalog.md, tool_confirm.
func (m *Manager) EmitToolConfirm(sessionID string, payload ToolConfirmPayload) {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess != nil {
		sess.mu.Lock()
		emitter := sess.emitter
		sess.mu.Unlock()
		if emitter != nil {
			emitter.ToolConfirm(payload)
			return
		}
	}

	m.log().Debug("tool confirm: no live emitter, falling back to raw pipeline",
		"session_id", sessionID, "tool", payload.Tool)
	m.EmitSessionEvent(sessionID, "tool_confirm", payload)
}
