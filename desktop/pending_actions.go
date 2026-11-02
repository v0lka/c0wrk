package desktop

import (
	coretools "github.com/v0lka/c0wrk/core/tools"
)

// PendingActionsResponse lists every HITL prompt currently blocking a
// session's agent goroutine (specs/contracts/desktop-frontend.md).
type PendingActionsResponse struct {
	ToolConfirms    []PendingToolConfirm    `json:"tool_confirms"`
	StepLimits      []PendingStepLimit      `json:"step_limits"`
	PlanApprovals   []PendingPlanApproval   `json:"plan_approvals"`
	AskUser         []PendingAskUser        `json:"ask_user"`
	GoalProposals   []PendingGoalProposal   `json:"goal_proposals"`
	GoalSuggestions []PendingGoalSuggestion `json:"goal_suggestions"`
}

type PendingToolConfirm struct {
	ConfirmID    string `json:"confirm_id"`
	Tool         string `json:"tool"`
	Args         string `json:"args"`
	Reasoning    string `json:"reasoning,omitempty"`
	ToolCallID   string `json:"tool_call_id,omitempty"`
	DisableJudge bool   `json:"disable_judge,omitempty"`
}

type PendingStepLimit struct {
	RequestID   string `json:"request_id"`
	CurrentStep int    `json:"current_step"`
	MaxSteps    int    `json:"max_steps"`
	Reason      string `json:"reason,omitempty"`
}

type PendingPlanApproval struct {
	RequestID   string `json:"request_id"`
	PlanPath    string `json:"plan_path"`
	PlanContent string `json:"plan_content"`
}

type PendingAskUser struct {
	RequestID string                      `json:"request_id"`
	Questions []coretools.AskUserQuestion `json:"questions"`
}

type PendingGoalProposal struct {
	RequestID        string `json:"request_id"`
	Condition        string `json:"condition"`
	Verify           string `json:"verify"`
	VerificationMode string `json:"verification_mode,omitempty"`
}

// PendingGoalSuggestion describes a pending goal-mode activation offer (the
// post-routing suggestion card).
type PendingGoalSuggestion struct {
	RequestID string `json:"request_id"`
	Reason    string `json:"reason"`
}

// GetPendingActions returns all pending HITL prompts for a session; the
// frontend sweeps it on session switch to resurface prompts missed live
// (absent = already resolved). See specs/contracts/desktop-frontend.md.
func (a *App) GetPendingActions(sessionID string) (*PendingActionsResponse, error) {
	// Non-nil: encoding/json marshals nil slices as JSON `null`, which the
	// frontend's isPendingActionsResponse guard rejects — killing session-
	// switch HITL reconciliation.
	resp := &PendingActionsResponse{
		ToolConfirms:    []PendingToolConfirm{},
		StepLimits:      []PendingStepLimit{},
		PlanApprovals:   []PendingPlanApproval{},
		AskUser:         []PendingAskUser{},
		GoalProposals:   []PendingGoalProposal{},
		GoalSuggestions: []PendingGoalSuggestion{},
	}

	a.pendingConfirmations.Range(func(key, value any) bool {
		pd, ok := value.(*pendingConfirmData)
		if !ok || pd.sessionID != sessionID {
			return true
		}
		confirmID, _ := key.(string)
		resp.ToolConfirms = append(resp.ToolConfirms, PendingToolConfirm{
			ConfirmID:    confirmID,
			Tool:         pd.toolName,
			Args:         string(pd.input),
			Reasoning:    pd.reasoning,
			ToolCallID:   pd.toolCallID,
			DisableJudge: pd.disableJudge,
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

	a.pendingGoalProposals.Range(func(_, value any) bool {
		e, ok := value.(*pendingGoalProposalEntry)
		if !ok || e.sessionID != sessionID {
			return true
		}
		resp.GoalProposals = append(resp.GoalProposals, PendingGoalProposal{
			RequestID:        e.payload.RequestID,
			Condition:        e.payload.Condition,
			Verify:           e.payload.Verify,
			VerificationMode: e.payload.VerificationMode,
		})
		return true
	})

	a.pendingGoalSuggestions.Range(func(_, value any) bool {
		e, ok := value.(*pendingGoalSuggestionEntry)
		if !ok || e.sessionID != sessionID {
			return true
		}
		resp.GoalSuggestions = append(resp.GoalSuggestions, PendingGoalSuggestion{
			RequestID: e.payload.RequestID,
			Reason:    e.payload.Reason,
		})
		return true
	})

	return resp, nil
}
