package desktop

import (
	coretools "github.com/v0lka/c0wrk/core/tools"
)

// PendingActionsResponse is the return type for GetPendingActions. It lists
// every HITL prompt currently blocking a session's agent goroutine, keyed by
// kind. Each entry carries the same payload the original event delivered, so
// the frontend can reconstruct the pending-action UI without relying on the
// event having been caught live.
type PendingActionsResponse struct {
	ToolConfirms  []PendingToolConfirm  `json:"tool_confirms"`
	StepLimits    []PendingStepLimit    `json:"step_limits"`
	PlanApprovals []PendingPlanApproval `json:"plan_approvals"`
	AskUser       []PendingAskUser      `json:"ask_user"`
}

// PendingToolConfirm describes a pending tool confirmation.
type PendingToolConfirm struct {
	ConfirmID string `json:"confirm_id"`
	Tool      string `json:"tool"`
	Args      string `json:"args"`
	Reasoning string `json:"reasoning,omitempty"`
}

// PendingStepLimit describes a pending step-limit prompt.
type PendingStepLimit struct {
	RequestID   string `json:"request_id"`
	CurrentStep int    `json:"current_step"`
	MaxSteps    int    `json:"max_steps"`
	Reason      string `json:"reason,omitempty"`
}

// PendingPlanApproval describes a pending plan-approval prompt.
type PendingPlanApproval struct {
	RequestID   string `json:"request_id"`
	PlanPath    string `json:"plan_path"`
	PlanContent string `json:"plan_content"`
}

// PendingAskUser describes a pending ask-user prompt.
type PendingAskUser struct {
	RequestID string                      `json:"request_id"`
	Questions []coretools.AskUserQuestion `json:"questions"`
}

// GetPendingActions returns all pending HITL prompts for a session — tool
// confirmations, step-limit prompts, plan approvals, and ask-user questions
// that are currently blocking the session's agent goroutine. The frontend
// calls this on session switch to resurface prompts whose events were missed
// while the session was in the background (no live event listener caught
// them) and to reconcile stale persisted prompts (a prompt in the DB that is
// NOT in this response has already been resolved).
func (a *App) GetPendingActions(sessionID string) (*PendingActionsResponse, error) {
	// Initialize every slice as non-nil/empty. Go's encoding/json marshals a
	// nil slice to JSON `null` (not `[]`), and the frontend's shape guard
	// (isPendingActionsResponse) rejects the response when any kind is null —
	// which would silently disable HITL reconciliation on session switch.
	// A session almost never has all four kinds pending at once, so without
	// this the response was effectively always rejected.
	resp := &PendingActionsResponse{
		ToolConfirms:  []PendingToolConfirm{},
		StepLimits:    []PendingStepLimit{},
		PlanApprovals: []PendingPlanApproval{},
		AskUser:       []PendingAskUser{},
	}

	a.pendingConfirmations.Range(func(key, value any) bool {
		pd, ok := value.(*pendingConfirmData)
		if !ok || pd.sessionID != sessionID {
			return true
		}
		confirmID, _ := key.(string)
		resp.ToolConfirms = append(resp.ToolConfirms, PendingToolConfirm{
			ConfirmID: confirmID,
			Tool:      pd.toolName,
			Args:      string(pd.input),
		})
		return true
	})

	a.pendingStepLimit.Range(func(_, value any) bool {
		e, ok := value.(*pendingStepLimitEntry)
		if !ok || e.sessionID != sessionID {
			return true
		}
		resp.StepLimits = append(resp.StepLimits, PendingStepLimit{
			RequestID:   e.payload.RequestID,
			CurrentStep: e.payload.CurrentStep,
			MaxSteps:    e.payload.MaxSteps,
			Reason:      e.payload.Reason,
		})
		return true
	})

	a.pendingPlanApprovals.Range(func(_, value any) bool {
		e, ok := value.(*pendingPlanApprovalEntry)
		if !ok || e.sessionID != sessionID {
			return true
		}
		resp.PlanApprovals = append(resp.PlanApprovals, PendingPlanApproval{
			RequestID:   e.payload.RequestID,
			PlanPath:    e.payload.PlanPath,
			PlanContent: e.payload.PlanContent,
		})
		return true
	})

	a.pendingAskUser.Range(func(_, value any) bool {
		e, ok := value.(*pendingAskUserEntry)
		if !ok || e.sessionID != sessionID {
			return true
		}
		resp.AskUser = append(resp.AskUser, PendingAskUser{
			RequestID: e.payload.RequestID,
			Questions: e.payload.Questions,
		})
		return true
	})

	return resp, nil
}
