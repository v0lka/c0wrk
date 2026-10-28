package desktop

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// extractPayload performs the standard data[0].(map[string]any) shape check
// shared by every Wails event handler. Returns the payload and ok=true on
// success, or logs a warning and returns ok=false on missing/wrong-shape data.
func extractPayload(eventName string, data []any, log *slog.Logger) (map[string]any, bool) {
	if len(data) == 0 {
		log.Warn(eventName + ": missing payload")
		return nil, false
	}
	payload, ok := data[0].(map[string]any)
	if !ok {
		log.Warn(eventName+": unexpected payload type", "data", data)
		return nil, false
	}
	return payload, true
}

// parseConfirmDecision converts the JSON decision field (JSON number →
// float64, an int, or a string) into a sdktools.ConfirmationResponse;
// ok=false on missing/unrecognized values, with a warning logged.
func parseConfirmDecision(payload map[string]any, log *slog.Logger) (sdktools.ConfirmationResponse, bool) {
	decisionVal, ok := payload["decision"]
	if !ok {
		log.Warn("tool confirmation response missing decision field")
		return 0, false
	}

	switch v := decisionVal.(type) {
	case float64:
		return numericConfirmDecision(int(v), log)
	case int:
		return numericConfirmDecision(v, log)
	case string:
		switch v {
		case "allow_once":
			return sdktools.ConfirmAllowOnce, true
		case "deny":
			return sdktools.ConfirmDeny, true
		case "stop", "deny_and_stop":
			return sdktools.ConfirmDenyAndStop, true
		default:
			log.Warn("unknown string confirmation decision", "decision", v)
			return 0, false
		}
	default:
		log.Warn("tool confirmation decision has unsupported type", "type", fmt.Sprintf("%T", decisionVal))
		return 0, false
	}
}

// numericConfirmDecision converts an integer decision and rejects values
// outside the ConfirmationResponse enum range.
func numericConfirmDecision(v int, log *slog.Logger) (sdktools.ConfirmationResponse, bool) {
	d := sdktools.ConfirmationResponse(v)
	if d < sdktools.ConfirmAllowOnce || d > sdktools.ConfirmDenyAndStop {
		log.Warn("numeric confirmation decision out of range", "decision", v)
		return 0, false
	}
	return d, true
}

// parseStepLimitResponse validates a step-limit response string against the
// known enum values; ok=false on unrecognized values.
func parseStepLimitResponse(v string) (agent.StepLimitResponse, bool) {
	switch v {
	case string(agent.StepLimitAllowOnce):
		return agent.StepLimitAllowOnce, true
	case string(agent.StepLimitAllowMore):
		return agent.StepLimitAllowMore, true
	case string(agent.StepLimitAllowAlways):
		return agent.StepLimitAllowAlways, true
	case string(agent.StepLimitDeny):
		return agent.StepLimitDeny, true
	default:
		return "", false
	}
}

// parsePlanApprovalDecision validates a plan-review decision string against
// the event-catalog values (approve / request_changes / abandon); ok=false on
// missing/unrecognized. An unrecognized decision is a malformed response, not
// an implicit rejection — the pending approval stays alive for a valid retry.
func parsePlanApprovalDecision(v string) (string, bool) {
	switch v {
	case "approve", "request_changes", "abandon":
		return v, true
	default:
		return "", false
	}
}

// parseGoalProposalDecision validates a goal-proposal decision (approve /
// cancel) under the same discipline: ok=false on unrecognized values leaves
// the pending proposal alive instead of consuming it as an implicit cancel.
func parseGoalProposalDecision(v string) (string, bool) {
	switch v {
	case "approve", "cancel":
		return v, true
	default:
		return "", false
	}
}

// stringField extracts a string field from a payload, logging a warning if the
// field is missing or has the wrong type.
func stringField(payload map[string]any, key, eventName string, log *slog.Logger) (string, bool) {
	val, ok := payload[key]
	if !ok {
		log.Warn(eventName+": missing "+key, "key", key)
		return "", false
	}
	s, ok := val.(string)
	if !ok {
		log.Warn(eventName+": "+key+" is not string", "type", fmt.Sprintf("%T", val))
		return "", false
	}
	return s, true
}

// confirmDecisionString renders a ConfirmationResponse back to the wire
// string the frontend protocol uses ("allow_once" / "deny" /
// "deny_and_stop"), so a hitl_resolved event can carry the effective decision
// as text. Returns "" for out-of-range values (the field is omitempty).
func confirmDecisionString(resp sdktools.ConfirmationResponse) string {
	switch resp {
	case sdktools.ConfirmAllowOnce:
		return "allow_once"
	case sdktools.ConfirmDeny:
		return "deny"
	case sdktools.ConfirmDenyAndStop:
		return "deny_and_stop"
	default:
		return ""
	}
}

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

// deliverResponse performs the best-effort buffered send shared by every HITL
// response handler and reports whether the decision was DELIVERED: a full
// channel means the receiver is gone or a faster responder won, so the caller
// must not broadcast the dropped response as the effective settlement.
func deliverResponse[T any](ch chan T, resp T) bool {
	select {
	case ch <- resp:
		return true
	default:
		return false
	}
}

// handleToolConfirmResponse is the body of the EventToolConfirmResponse
// listener (extracted so it is unit-testable without a live Wails runtime).
func (a *App) handleToolConfirmResponse(payload map[string]any, log *slog.Logger) {
	requestID, ok := stringField(payload, "confirm_id", "tool confirmation response", log)
	if !ok {
		return
	}

	resp, ok := parseConfirmDecision(payload, log)
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

	sent := deliverResponse(confirmData.ch, resp)
	if !sent {
		log.Warn("confirmation response dropped: channel full",
			"confirm_id", requestID,
			"tool", confirmData.toolName,
			"decision", resp)
	}

	a.pendingConfirmations.Delete(requestID)

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
		a.emitHitlResolved(confirmData.sessionID, session.HitlResolvedToolConfirm, requestID, confirmDecisionString(resp))
	}
}

// handleToolJudgeRequest is the body of the EventToolJudgeRequest listener:
// it looks up the pending confirmation and spawns runJudgeEvaluation
// asynchronously. The beginJudgeEvaluation marker drops repeat frames —
// first-responder-wins protects only the DELIVERY, so a second frame
// (double-click, scripted client) would fan out a second full-budget LLM
// evaluation that could only lose the response race.
func (a *App) handleToolJudgeRequest(payload map[string]any, uiEmit func(session.Event), log *slog.Logger) {
	confirmID, ok := stringField(payload, "confirm_id", "tool judge request", log)
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

	if !a.beginJudgeEvaluation(confirmID) {
		log.Debug("judge evaluation already in flight; dropping repeat request",
			"confirm_id", confirmID, "tool", pendingData.toolName)
		return
	}
	a.judgeWG.Add(1)
	go func() {
		defer a.judgeWG.Done()
		defer a.finishJudgeEvaluation(confirmID)
		a.runJudgeEvaluation(confirmID, pendingData, uiEmit, log)
	}()
}

// beginJudgeEvaluation claims the confirm_id's in-flight slot (true = caller
// owns it and must release via finishJudgeEvaluation; false = already
// running, repeat dropped).
func (a *App) beginJudgeEvaluation(confirmID string) bool {
	_, loaded := a.judgeInFlight.LoadOrStore(confirmID, struct{}{})
	return !loaded
}

// finishJudgeEvaluation releases the claim taken by beginJudgeEvaluation.
func (a *App) finishJudgeEvaluation(confirmID string) {
	a.judgeInFlight.Delete(confirmID)
}

// runJudgeEvaluation performs the on-demand judge call and emits the response
// event. Always recovers from panics so the handler does not leak a goroutine
// failure into Wails event-loop instability.
func (a *App) runJudgeEvaluation(confirmID string, pendingData *pendingConfirmData, uiEmit func(session.Event), log *slog.Logger) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("judge: goroutine panicked", "confirm_id", confirmID, "panic", r)
			uiEmit(session.Event{
				SessionID: pendingData.sessionID,
				Type:      "tool_judge_response",
				Data: session.JudgeResponsePayload{
					ConfirmID: confirmID,
					Error:     fmt.Sprintf("Internal error during judge evaluation: %v", r),
				},
			})
		}
	}()

	log.Debug("judge: goroutine started", "confirm_id", confirmID, "tool", pendingData.toolName)

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

	parentCtx := a.wailsCtx()
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	judgeCtx, judgeCancel := context.WithTimeout(parentCtx, 120*time.Second)
	defer judgeCancel()

	// Inject the session dump writer (judge-call observability), then enrich
	// the judge context with the session's security scope so the judge LLM
	// sees the session's directory scope (explicit + implicit work dirs).
	sess, sessOK := app.Manager().GetSession(pendingData.sessionID)
	if sessOK {
		if dumpFile := sess.DumpFile(); dumpFile != nil {
			defer func() { _ = dumpFile.Close() }()
			judgeCtx = agent.WithDumpWriter(judgeCtx, dumpFile)
		}
	}
	judgeCtx = app.Manager().JudgeContext(judgeCtx, pendingData.sessionID)

	responsePayload := session.JudgeResponsePayload{ConfirmID: confirmID}

	// Session-pinned judge evaluation (ADR-028): the manual "Judge" action
	// evaluates on the session's own provider/model — the same judge
	// automatic escalations use. See specs/decisions/028-session-pinned-judge.md.
	_, reasoning, err := app.EvaluateJudgeForSession(judgeCtx, pendingData.sessionID, pendingData.toolName, pendingData.input, pendingData.taskContext)
	if err != nil {
		log.Warn("judge: evaluation failed", "confirm_id", confirmID, "tool", pendingData.toolName, "error", err)
		responsePayload.Error = fmt.Sprintf("Judge evaluation failed: %v", err)
		responsePayload.Reasoning = reasoning
	} else {
		log.Debug("judge: evaluation completed", "confirm_id", confirmID, "tool", pendingData.toolName, "reasoning", reasoning)
		responsePayload.Reasoning = reasoning
	}

	uiEmit(session.Event{SessionID: pendingData.sessionID, Type: "tool_judge_response", Data: responsePayload})
	log.Debug("judge: response event emitted", "confirm_id", confirmID)
}

// handleAskUserResponse is the body of the EventAskUserResponse listener.
func (a *App) handleAskUserResponse(payload map[string]any, log *slog.Logger) {
	requestID, ok := stringField(payload, "request_id", "ask_user response", log)
	if !ok {
		return
	}

	resp := parseAskUserAnswers(payload)

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

	sent := deliverResponse(entry.ch, resp)
	if !sent {
		log.Warn("ask_user response dropped: channel full or receiver gone",
			"request_id", requestID)
	}
	a.pendingAskUser.Delete(requestID)

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
	requestID, ok := stringField(payload, "request_id", "plan_approval response", log)
	if !ok {
		return
	}

	rawDecision, _ := payload["decision"].(string)
	decision, ok := parsePlanApprovalDecision(rawDecision)
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

	sent := deliverResponse(entry.ch, planApprovalResponse{Decision: decision, Feedback: feedback})
	if !sent {
		log.Warn("plan approval response dropped: channel full or receiver gone",
			"request_id", requestID)
	}
	a.pendingPlanApprovals.Delete(requestID)

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
	sent := deliverResponse(entry.ch, resp)
	a.pendingGoalProposals.Delete(requestID)

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
	requestID, ok := stringField(payload, "request_id", "goal_proposal response", log)
	if !ok {
		return
	}
	rawDecision, _ := payload["decision"].(string)
	decision, ok := parseGoalProposalDecision(rawDecision)
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

	sent := deliverResponse(entry.ch, goalSuggestionResponse{Decision: decision})
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
	requestID, ok := stringField(payload, "request_id", "goal_suggestion response", log)
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

// parseAskUserAnswers extracts the typed answers slice from the untyped JSON
// payload; malformed entries are skipped (unrecognized question IDs are
// rejected upstream anyway).
func parseAskUserAnswers(payload map[string]any) coretools.AskUserResponse {
	var resp coretools.AskUserResponse

	answersVal, ok := payload["answers"]
	if !ok {
		return resp
	}
	answersArr, ok := answersVal.([]any)
	if !ok {
		return resp
	}

	for _, item := range answersArr {
		answerMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var answer coretools.AskUserAnswer
		if id, ok := answerMap["id"].(string); ok {
			answer.ID = id
		}
		if selectedVal, ok := answerMap["selected"]; ok {
			if selectedArr, ok := selectedVal.([]any); ok {
				for _, v := range selectedArr {
					if s, ok := v.(string); ok {
						answer.Selected = append(answer.Selected, s)
					}
				}
			}
		}
		if ct, ok := answerMap["custom_text"].(string); ok {
			answer.CustomText = ct
		}
		resp.Answers = append(resp.Answers, answer)
	}
	return resp
}

// handleStepLimitResponse is the body of the EventStepLimitResponse listener.
func (a *App) handleStepLimitResponse(payload map[string]any, log *slog.Logger) {
	requestID, ok := stringField(payload, "request_id", "step_limit response", log)
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
	resp, ok := parseStepLimitResponse(respStr)
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

	sent := deliverResponse(entry.ch, resp)
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
