package session

// SetDisplayContextWindowForModel pushes a runtime context-window correction
// for `model` into every live session emitter — the session-manager half of a
// runtime metadata correction that must reach ALREADY-OPEN sessions.
//
// Motivating caller: the embedded LLM context read-back
// (backend.persistEmbeddedContext). When the model is reloaded under a
// different tuning context, the measured window lands in the tier-1
// llm.models override and the builder pushes it into every live per-session
// model REGISTRY — but an emitter caches its display basis (the context window
// the status bar and the "Compacted from X% to Y%" cards are presented
// against) at HandleMessage start and only re-resolves it on the NEXT message.
// A session sitting idle — or the tail of a task that started before the
// correction — would keep displaying fill percentages scaled to the stale
// window: observed as a status bar showing the model's old (larger) window and
// compaction cards whose percentages are halved relative to the real basis.
//
// The emitter's own SetDisplayContextWindowForModel is model-scoped — it drops
// the correction when the session has since switched to another model — and
// re-broadcasts a corrected context_fill when the session is idle, so the
// status bar heals immediately without waiting for the next message. Mid-task
// the basis simply swaps; the next executor ContextFill re-derives both cached
// halves of the compaction-percentage scale from the new basis, so the pair
// stays consistent.
//
// Best-effort by contract, mirroring EmitToolConfirm: sessions without a live
// emitter are skipped, a missing manager state never blocks the caller, and
// the push is safe while a session's task is running (the emitter method is
// atomic; it is invoked outside m.mu and s.mu).
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
