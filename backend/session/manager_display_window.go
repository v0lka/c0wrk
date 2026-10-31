package session

// SetDisplayContextWindowForModel pushes a runtime context-window correction
// for `model` into every live session emitter — the session-manager half of a
// correction that must reach ALREADY-OPEN sessions (the embedded-LLM context
// read-back: an emitter caches its display basis at HandleMessage start, so
// an idle session would otherwise keep showing fill/compaction percentages
// against the stale window — specs/domains/embedded-llm.md). The emitter
// method is model-scoped and re-broadcasts a corrected context_fill for an
// idle session; mid-task the basis simply swaps.
//
// Best-effort, mirroring EmitToolConfirm: sessions without a live emitter are
// skipped; safe while a task runs (atomic emitter method, invoked outside
// m.mu and s.mu).
func (m *Manager) SetDisplayContextWindowForModel(model string, window int) {
	if model == "" || window <= 0 {
		return
	}
	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.RUnlock()

	pushed := 0
	for _, s := range sessions {
		s.mu.RLock()
		emitter := s.emitter
		s.mu.RUnlock()
		if emitter == nil {
			continue
		}
		emitter.SetDisplayContextWindowForModel(model, window)
		pushed++
	}
	if pushed > 0 {
		m.log().Debug("display context window correction pushed to live sessions",
			"model", model, "window", window, "sessions", pushed)
	}
}
