package desktop

import (
	"fmt"
	"log/slog"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/hitlwire"
)

// emitHitlResolved broadcasts one HITL settlement to every surface (webview +
// web-access tabs) so they retire their open card and learn the effective
// first-responder decision — the multi-surface convergence documented for
// hitl_resolved in specs/contracts/event-catalog.md. Skipped at Debug when no
// transport exists (a bare &App{} in unit tests).
func (a *App) emitHitlResolved(sessionID string, kind session.HitlResolvedKind, id, decision string) {
	if a.wailsCtx() == nil && a.wailsEmit == nil {
		a.log().Debug("hitl_resolved skipped: no emit transport", "kind", string(kind), "id", id)
		return
	}
	a.buildUIEmitFunc()(session.Event{
		SessionID: sessionID,
		Type:      "hitl_resolved",
		Data: session.HitlResolvedData{
			Kind:     kind,
			ID:       id,
			Decision: decision,
		},
	})
}

// handleToolConfirmResponse is the body of the EventToolConfirmResponse
// listener (extracted so it is unit-testable without a live Wails runtime).
func (a *App) handleToolConfirmResponse(payload map[string]any, log *slog.Logger) {
	requestID, ok := hitlwire.StringField(payload, "confirm_id", "tool confirmation response", log)
	if !ok {
		return
	}

	resp, ok := hitlwire.ConfirmDecision(payload, log)
	if !ok {
		return
	}

	dataVal, ok := a.pendingConfirmations.Load(requestID)
	if !ok {
		log.Warn("no pending confirmation for confirm_id", "confirm_id", requestID)
		return
	}
	confirmData, ok := dataVal.(*pendingConfirmData)
	if !ok {
		log.Warn("pending confirmation has wrong type", "confirm_id", requestID)
		a.pendingConfirmations.Delete(requestID)
		return
	}

	// Delete from the pending map BEFORE delivering: the channel send
	// handshakes with a waiting consumer (the WS inbound delivery test
	// receives the decision and immediately asserts the entry is gone), so
	// the delete must happen-before the send to keep that observation
	// deterministic. It also strengthens the invariant the resolved event
	// below relies on — no reacting surface can re-answer an entry that is
	// already deleted — and loses nothing: the entry was Load-ed above, so
	// a concurrent duplicate response can no longer deliver twice anyway.
	a.pendingConfirmations.Delete(requestID)

	sent := hitlwire.DeliverResponse(confirmData.ch, resp)
	if !sent {
		log.Warn("confirmation response dropped: channel full",
			"confirm_id", requestID,
			"tool", confirmData.toolName,
			"decision", resp)
	}

	// Mark the persisted tool_confirm message as resolved so it doesn't
	// reappear as pending on session reload.
	if err := a.resolvePendingMessage(confirmData.sessionID, "tool_confirm", "confirm_id", requestID, map[string]any{
		"resolved": true,
		"decision": int(resp),
	}); err != nil {
		log.Warn("failed to resolve persisted tool_confirm message", "confirm_id", requestID, "error", err)
	}

	// Emitted AFTER the channel send and the pending-map delete so a reacting
	// surface can never re-answer a live entry; only a DELIVERED decision is
	// broadcast (the payload carries the effective decision — see
	// deliverResponse).
	if sent {
		a.emitHitlResolved(confirmData.sessionID, session.HitlResolvedToolConfirm, requestID, hitlwire.ConfirmDecisionString(resp))
	}
}

// handleToolJudgeRequest is the body of the EventToolJudgeRequest listener:
// it looks up the pending confirmation and spawns the judge evaluation
// asynchronously. The Begin marker drops repeat frames —
// first-responder-wins protects only the DELIVERY, so a second frame
// (double-click, scripted client) would fan out a second full-budget LLM
// evaluation that could only lose the response race.
func (a *App) handleToolJudgeRequest(payload map[string]any, uiEmit func(session.Event), log *slog.Logger) {
	confirmID, ok := hitlwire.StringField(payload, "confirm_id", "tool judge request", log)
	if !ok {
		return
	}

	dataVal, ok := a.pendingConfirmations.Load(confirmID)
	if !ok {
		log.Warn("no pending confirmation for judge request", "confirm_id", confirmID)
		return
	}
	pendingData, ok := dataVal.(*pendingConfirmData)
	if !ok {
		log.Warn("pending confirmation has wrong type for judge request", "confirm_id", confirmID)
		return
	}

	if !a.judgeService.Begin(confirmID) {
		log.Debug("judge evaluation already in flight; dropping repeat request",
			"confirm_id", confirmID, "tool", pendingData.toolName)
		return
	}
	a.judgeWG.Add(1)
	go func() {
		defer a.judgeWG.Done()
		defer a.judgeService.Finish(confirmID)
		// The on-demand judge call goes through the transport-neutral
		// backend judge service; the service recovers panics into an error
		// payload, so the goroutine cannot leak a failure into Wails
		// event-loop instability. The pending-confirmation lookup and
		// delivery stay here (HITL plumbing).
		app := a.application()
		if app == nil {
			log.Warn("judge: no application available", "confirm_id", confirmID)
			a.pendingConfirmations.Delete(confirmID)
			uiEmit(session.Event{
				SessionID: pendingData.sessionID,
				Type:      "tool_judge_response",
				Data: session.JudgeResponsePayload{
					ConfirmID: confirmID,
					Error:     "Judge unavailable: application not initialized",
				},
			})
			return
		}

		payload := backend.RunJudgeEvaluation(a.wailsCtx(), &a.judgeService, app, backend.JudgeRequest{
			ConfirmID:   confirmID,
			SessionID:   pendingData.sessionID,
			ToolName:    pendingData.toolName,
			Input:       pendingData.input,
			TaskContext: pendingData.taskContext,
		}, log)
		uiEmit(session.Event{SessionID: pendingData.sessionID, Type: "tool_judge_response", Data: payload})
		log.Debug("judge: response event emitted", "confirm_id", confirmID)
	}()
}

// handleAskUserResponse is the body of the EventAskUserResponse listener.
func (a *App) handleAskUserResponse(payload map[string]any, log *slog.Logger) {
	requestID, ok := hitlwire.StringField(payload, "request_id", "ask_user response", log)
	if !ok {
		return
	}

	resp := hitlwire.AskUserAnswers(payload)

	chVal, ok := a.pendingAskUser.Load(requestID)
	if !ok {
		log.Warn("no pending ask_user for request_id", "request_id", requestID)
		return
	}
	entry, ok := chVal.(*pendingAskUserEntry)
	if !ok {
		log.Warn("pending ask_user channel has wrong type", "request_id", requestID)
		a.pendingAskUser.Delete(requestID)
		return
	}

	// Delete BEFORE delivering — same ordering as handleToolConfirmResponse:
	// the channel send handshakes with the waiting consumer, and any
	// post-receive observation of the pending map must not see the entry
	// (the deleted-alive window is also what the resolved-event invariant
	// below relies on).
	a.pendingAskUser.Delete(requestID)

	sent := hitlwire.DeliverResponse(entry.ch, resp)
	if !sent {
		log.Warn("ask_user response dropped: channel full or receiver gone",
			"request_id", requestID)
	}

	// Mark the persisted ask_user message as resolved so it doesn't reappear
	// as pending on session reload.
	if err := a.resolvePendingMessage(entry.sessionID, "ask_user", "request_id", requestID, map[string]any{
		"resolved": true,
	}); err != nil {
		log.Warn("failed to resolve persisted ask_user message", "request_id", requestID, "error", err)
	}

	// Multi-surface convergence (see emitHitlResolved): answers are
	// agent-owned payload, so the event carries no decision — other surfaces
	// only need to retire the open ask-user form.
	if sent {
		a.emitHitlResolved(entry.sessionID, session.HitlResolvedAskUser, requestID, "")
	}
}

// handlePlanApprovalResponse is the body of the EventPlanApprovalResponse listener.
func (a *App) handlePlanApprovalResponse(payload map[string]any, log *slog.Logger) {
	requestID, ok := hitlwire.StringField(payload, "request_id", "plan_approval response", log)
	if !ok {
		return
	}

	rawDecision, _ := payload["decision"].(string)
	decision, ok := hitlwire.PlanApprovalDecision(rawDecision)
	if !ok {
		log.Warn("plan_approval response has unknown or missing decision",
			"decision", rawDecision, "request_id", requestID)
		return
	}
	feedback, _ := payload["feedback"].(string)

	chVal, ok := a.pendingPlanApprovals.Load(requestID)
	if !ok {
		log.Warn("no pending plan approval for request_id", "request_id", requestID)
		return
	}
	entry, ok := chVal.(*pendingPlanApprovalEntry)
	if !ok {
		log.Warn("pending plan approval channel has wrong type", "request_id", requestID)
		a.pendingPlanApprovals.Delete(requestID)
		return
	}

	// Delete BEFORE delivering — same ordering as handleToolConfirmResponse
	// (the delete must happen-before the channel send so a waiting consumer
	// never observes a deleted-alive entry).
	a.pendingPlanApprovals.Delete(requestID)

	sent := hitlwire.DeliverResponse(entry.ch, planApprovalResponse{Decision: decision, Feedback: feedback})
	if !sent {
		log.Warn("plan approval response dropped: channel full or receiver gone",
			"request_id", requestID)
	}

	// Mark the persisted plan_review message as resolved so it doesn't
	// reappear as pending on session reload.
	if err := a.resolvePendingMessage(entry.sessionID, "plan_review", "request_id", requestID, map[string]any{
		"resolved": true,
		"decision": decision,
	}); err != nil {
		log.Warn("failed to resolve persisted plan_review message", "request_id", requestID, "error", err)
	}

	// Multi-surface convergence (see emitHitlResolved). Only a delivered
	// decision is broadcast — the payload carries the effective decision.
	if sent {
		a.emitHitlResolved(entry.sessionID, session.HitlResolvedPlanReview, requestID, decision)
	}
}

// resolveGoalProposal delivers a user decision to a blocked goal-proposal
// channel and marks the persisted goal_proposal message resolved; true when a
// pending proposal was found. One funnel for the event path
// (handleGoalProposalResponse) and the RPC path (ConfirmGoal/CancelGoal).
func (a *App) resolveGoalProposal(requestID, decision, condition, verify, verificationMode string) bool {
	if requestID == "" {
		return false
	}
	dataVal, ok := a.pendingGoalProposals.Load(requestID)
	if !ok {
		return false
	}
	entry, ok := dataVal.(*pendingGoalProposalEntry)
	if !ok {
		a.pendingGoalProposals.Delete(requestID)
		return false
	}

	resp := goalProposalResponse{
		Decision:         decision,
		Condition:        condition,
		Verify:           verify,
		VerificationMode: verificationMode,
	}
	// Delete BEFORE delivering — same ordering as handleToolConfirmResponse
	// (happen-before the channel send; a waiting consumer must never observe
	// a deleted-alive entry).
	a.pendingGoalProposals.Delete(requestID)
	sent := hitlwire.DeliverResponse(entry.ch, resp)

	// Mark the persisted goal_proposal message as resolved so it doesn't
	// reappear as pending on session reload.
	if err := a.resolvePendingMessage(entry.sessionID, "goal_proposal", "request_id", requestID, map[string]any{
		"resolved":          true,
		"decision":          decision,
		"condition":         condition,
		"verify":            verify,
		"verification_mode": verificationMode,
	}); err != nil {
		a.log().Warn("failed to resolve persisted goal_proposal message", "request_id", requestID, "error", err)
	}

	// Emitted HERE — in the shared resolver — so both the event and RPC paths
	// broadcast the settlement (see emitHitlResolved); only a delivered
	// decision is broadcast.
	if sent {
		a.emitHitlResolved(entry.sessionID, session.HitlResolvedGoalProposal, requestID, decision)
	}
	return true
}

// handleGoalProposalResponse is the body of the EventGoalProposalResponse
// listener (extracted so it is unit-testable without a live Wails runtime).
func (a *App) handleGoalProposalResponse(payload map[string]any, log *slog.Logger) {
	requestID, ok := hitlwire.StringField(payload, "request_id", "goal_proposal response", log)
	if !ok {
		return
	}
	rawDecision, _ := payload["decision"].(string)
	decision, ok := hitlwire.GoalProposalDecision(rawDecision)
	if !ok {
		log.Warn("goal proposal response has unknown or missing decision",
			"decision", rawDecision, "request_id", requestID)
		return
	}
	condition, _ := payload["condition"].(string)
	verify, _ := payload["verify"].(string)
	verificationMode, _ := payload["verification_mode"].(string)

	if !a.resolveGoalProposal(requestID, decision, condition, verify, verificationMode) {
		log.Warn("no pending goal proposal for request_id", "request_id", requestID)
	}
}

// resolveGoalSuggestion delivers a user decision to a blocked goal-suggestion
// channel and marks the persisted goal_suggestion message resolved; true when
// a pending suggestion was found. One funnel for the event path
// (handleGoalSuggestionResponse) and the RPC path
// (AcceptGoalSuggestion/DeclineGoalSuggestion).
func (a *App) resolveGoalSuggestion(requestID, decision string) bool {
	if requestID == "" {
		return false
	}
	dataVal, ok := a.pendingGoalSuggestions.Load(requestID)
	if !ok {
		return false
	}
	entry, ok := dataVal.(*pendingGoalSuggestionEntry)
	if !ok {
		a.pendingGoalSuggestions.Delete(requestID)
		return false
	}

	sent := hitlwire.DeliverResponse(entry.ch, goalSuggestionResponse{Decision: decision})
	a.pendingGoalSuggestions.Delete(requestID)

	// Mark the persisted goal_suggestion message as resolved so it doesn't
	// reappear as pending on session reload.
	if err := a.resolvePendingMessage(entry.sessionID, "goal_suggestion", "request_id", requestID, map[string]any{
		"resolved": true,
		"decision": decision,
	}); err != nil {
		a.log().Warn("failed to resolve persisted goal_suggestion message", "request_id", requestID, "error", err)
	}

	// Emitted HERE — in the shared resolver — so both the event and RPC paths
	// broadcast the settlement (see emitHitlResolved); only a delivered
	// decision is broadcast.
	if sent {
		a.emitHitlResolved(entry.sessionID, session.HitlResolvedGoalSuggestion, requestID, decision)
	}
	return true
}

// handleGoalSuggestionResponse is the body of the EventGoalSuggestionResponse
// listener (extracted so it is unit-testable without a live Wails runtime).
func (a *App) handleGoalSuggestionResponse(payload map[string]any, log *slog.Logger) {
	requestID, ok := hitlwire.StringField(payload, "request_id", "goal_suggestion response", log)
	if !ok {
		return
	}
	decision, _ := payload["decision"].(string)
	if decision != "accept" && decision != "decline" {
		log.Warn("goal suggestion response has unrecognized decision", "request_id", requestID, "decision", decision)
		return
	}

	if !a.resolveGoalSuggestion(requestID, decision) {
		log.Warn("no pending goal suggestion for request_id", "request_id", requestID)
	}
}

// handleStepLimitResponse is the body of the EventStepLimitResponse listener.
func (a *App) handleStepLimitResponse(payload map[string]any, log *slog.Logger) {
	requestID, ok := hitlwire.StringField(payload, "request_id", "step_limit response", log)
	if !ok {
		return
	}

	responseVal, ok := payload["response"]
	if !ok {
		log.Warn("step_limit response missing response field")
		return
	}

	respStr, ok := responseVal.(string)
	if !ok {
		log.Warn("step_limit response has unsupported type", "type", fmt.Sprintf("%T", responseVal))
		return
	}
	resp, ok := hitlwire.StepLimitResponse(respStr)
	if !ok {
		log.Warn("unknown step_limit response value", "response", respStr)
		return
	}

	chVal, ok := a.pendingStepLimit.Load(requestID)
	if !ok {
		log.Warn("no pending step_limit for request_id", "request_id", requestID)
		return
	}
	entry, ok := chVal.(*pendingStepLimitEntry)
	if !ok {
		log.Warn("pending step_limit channel has wrong type", "request_id", requestID)
		a.pendingStepLimit.Delete(requestID)
		return
	}

	sent := hitlwire.DeliverResponse(entry.ch, resp)
	if !sent {
		log.Warn("step_limit response dropped: channel full or receiver gone",
			"request_id", requestID)
	}
	a.pendingStepLimit.Delete(requestID)

	// Mark the persisted step_limit message as resolved so it doesn't
	// reappear as pending on session reload.
	if err := a.resolvePendingMessage(entry.sessionID, "step_limit", "request_id", requestID, map[string]any{
		"resolved": true,
		"response": respStr,
	}); err != nil {
		log.Warn("failed to resolve persisted step_limit message", "request_id", requestID, "error", err)
	}

	// Multi-surface convergence (see emitHitlResolved). Only a delivered
	// decision is broadcast — the payload carries the effective decision.
	if sent {
		a.emitHitlResolved(entry.sessionID, session.HitlResolvedStepLimit, requestID, respStr)
	}
}
