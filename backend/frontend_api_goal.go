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
