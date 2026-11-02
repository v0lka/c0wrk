package session

// SetGoalProposalResolver installs the desktop callback delivering user
// decisions to a blocked goal-proposal channel (specs/domains/goal-mode.md).
func (m *Manager) SetGoalProposalResolver(fn func(requestID, decision, condition, verify, verificationMode string) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.goalProposalResolver = fn
}

// ResolveGoalProposal delivers a user decision on a pending goal proposal
// ("approve"/"cancel"; condition/verify/verificationMode carry the possibly
// edited approved values; false when nothing is pending) — specs/domains/goal-mode.md.
func (m *Manager) ResolveGoalProposal(requestID, decision, condition, verify, verificationMode string) bool {
	m.mu.RLock()
	fn := m.goalProposalResolver
	m.mu.RUnlock()
	if fn == nil {
		return false
	}
	return fn(requestID, decision, condition, verify, verificationMode)
}

// SetGoalSuggestionResolver installs the desktop callback delivering user
// decisions to a blocked goal-suggestion channel (specs/domains/goal-mode.md).
func (m *Manager) SetGoalSuggestionResolver(fn func(requestID, decision string) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.goalSuggestionResolver = fn
}

// ResolveGoalSuggestion delivers a user decision on a pending goal-mode
// suggestion ("accept"/"decline"); false when nothing is pending
// (specs/domains/goal-mode.md).
func (m *Manager) ResolveGoalSuggestion(requestID, decision string) bool {
	m.mu.RLock()
	fn := m.goalSuggestionResolver
	m.mu.RUnlock()
	if fn == nil {
		return false
	}
	return fn(requestID, decision)
}
