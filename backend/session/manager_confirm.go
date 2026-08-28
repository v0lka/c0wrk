package session

// EmitToolConfirm reports a tool-confirmation request for a session. The
// desktop confirm callback routes the event through here (instead of the raw
// UI emitter) so the activity tracker — and therefore the runtime-status
// snapshot read on session switches — reflects the "Awaiting confirmation..."
// state for however long the agent goroutine blocks on the user's decision.
// Sessions without a live emitter (e.g. confirmation racing a session restore)
// fall back to the raw event pipeline so the frontend still receives the card.
// Transient: the persister skips this event type. Best-effort: a missing
// session only logs at debug level, never blocks the confirmation.
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
