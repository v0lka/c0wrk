package backend

import (
	"errors"
	"fmt"
)

// ConfirmGoal resolves a pending goal proposal with the user's approval: the
// (possibly user-edited) condition/verify/verificationMode unblocks the derivation
// agent's propose_goal call so the goal loop proceeds. sessionID exists only for the
// Wails RPC binding shape. See specs/domains/goal-mode.md.
func (f *FrontendAPI) ConfirmGoal(sessionID, requestID, condition, verify, verificationMode string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	if requestID == "" {
		return errors.New("request_id is required")
	}
	if !f.app.Manager().ResolveGoalProposal(requestID, "approve", condition, verify, verificationMode) {
		return fmt.Errorf("no pending goal proposal for request_id %q", requestID)
	}
	return nil
}

// CancelGoal resolves a pending goal proposal with a cancellation: propose_goal exits
// the goal loop without committing. See specs/domains/goal-mode.md.
func (f *FrontendAPI) CancelGoal(sessionID, requestID string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	if requestID == "" {
		return errors.New("request_id is required")
	}
	if !f.app.Manager().ResolveGoalProposal(requestID, "cancel", "", "", "") {
		return fmt.Errorf("no pending goal proposal for request_id %q", requestID)
	}
	return nil
}

// AcceptGoalSuggestion unblocks the pending goal-mode suggestion; the task enters the
// goal loop before any work starts. See specs/domains/goal-mode.md.
func (f *FrontendAPI) AcceptGoalSuggestion(sessionID, requestID string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	if requestID == "" {
		return errors.New("request_id is required")
	}
	if !f.app.Manager().ResolveGoalSuggestion(requestID, "accept") {
		return fmt.Errorf("no pending goal suggestion for request_id %q", requestID)
	}
	return nil
}

// DeclineGoalSuggestion continues the plain run and suppresses further suggestions for
// the session. See specs/domains/goal-mode.md.
func (f *FrontendAPI) DeclineGoalSuggestion(sessionID, requestID string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	if requestID == "" {
		return errors.New("request_id is required")
	}
	if !f.app.Manager().ResolveGoalSuggestion(requestID, "decline") {
		return fmt.Errorf("no pending goal suggestion for request_id %q", requestID)
	}
	return nil
}
