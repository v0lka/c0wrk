package backend

import (
	"errors"
	"fmt"
)

// ConfirmGoal resolves a pending goal proposal with the user's approval.
// requestID ties the decision to the pending propose_goal call; condition and
// verify carry the (possibly user-edited) approved values. The decision is
// delivered through the manager's goal-proposal resolver, which unblocks the
// derivation agent's propose_goal tool call so the goal loop can proceed.
func (f *FrontendAPI) ConfirmGoal(sessionID, requestID, condition, verify string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	if requestID == "" {
		return errors.New("request_id is required")
	}
	if !f.app.Manager().ResolveGoalProposal(requestID, "approve", condition, verify, "") {
		return fmt.Errorf("no pending goal proposal for request_id %q", requestID)
	}
	return nil
}

// CancelGoal resolves a pending goal proposal with a cancellation. The
// derivation agent's propose_goal call receives a cancel decision and exits
// the goal loop cleanly without committing to a goal.
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

// ClarifyGoal resolves a pending goal proposal with a clarification. The user
// has provided an answer (either to the agent's clarification question or
// their own refinement request) and wants the agent to revise the goal and
// re-propose. The clarification string is forwarded to the propose_goal tool,
// which instructs the agent to call propose_goal again with updated values.
// This completes the clarification round-trip that needs_clarification mode
// requires; without it, the approve path is the only resolvable action.
func (f *FrontendAPI) ClarifyGoal(sessionID, requestID, clarification string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	if requestID == "" {
		return errors.New("request_id is required")
	}
	if !f.app.Manager().ResolveGoalProposal(requestID, "clarify", "", "", clarification) {
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
// dispatches to the orchestrator's resume goal loop. Returns nil if there is
// nothing to resume.
func (f *FrontendAPI) ResumeGoal(sessionID string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().ResumeGoal(f.ctx(), sessionID)
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
