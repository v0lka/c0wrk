package backend

import (
	"errors"
	"fmt"
)

// ConfirmGoal resolves a pending goal proposal with the user's approval.
// requestID ties the decision to the pending propose_goal call; condition and
// verify carry the (possibly user-edited) approved values; verificationMode
// carries the (possibly user-edited) per-goal verification mode, which the
// derivation agent chose and the user may override at sign-off. The decision
// is delivered through the manager's goal-proposal resolver, which unblocks
// the derivation agent's propose_goal tool call so the goal loop can proceed.
//
// sessionID is required by the Wails RPC binding signature for session-scoped
// event routing; the requestID alone identifies the pending proposal.
func (f *FrontendAPI) ConfirmGoal(sessionID, requestID, condition, verify, verificationMode string) error {
	if f.app == nil || f.app.Manager() == nil {
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

// CancelGoal resolves a pending goal proposal with a cancellation. The
// derivation agent's propose_goal call receives a cancel decision and exits
// the goal loop cleanly without committing to a goal.
//
// sessionID is required by the Wails RPC binding signature for session-scoped
// event routing; the requestID alone identifies the pending proposal.
func (f *FrontendAPI) CancelGoal(sessionID, requestID string) error {
	if f.app == nil || f.app.Manager() == nil {
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

// PauseGoal signals the running goal loop for a session to pause at the top of
// its next turn. The pause is cooperative: the loop transitions the goal to
// StatusPaused, persists it, and exits so a later ResumeGoal can re-enter.
func (f *FrontendAPI) PauseGoal(sessionID string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().PauseGoal(sessionID)
}

// ResumeGoal re-enters the goal loop for a paused (or still-active) goal. It
// delegates to the resume path, which loads the persisted GoalState and
// dispatches to the orchestrator's resume goal loop. The optional
// modelOverride/reasoningEffort apply the user's current selection to the
// resumed goal. Returns nil if there is nothing to resume.
func (f *FrontendAPI) ResumeGoal(sessionID, modelOverride, reasoningEffort string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().ResumeGoal(f.ctx(), sessionID, modelOverride, reasoningEffort)
}

// ClearGoal abandons the goal for a session: it cancels any in-flight task
// (stopping a running goal loop) and marks the persisted goal cancelled so it
// will not resume on the next app restart.
func (f *FrontendAPI) ClearGoal(sessionID string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().ClearGoal(sessionID)
}
