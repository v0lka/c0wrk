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

// parseConfirmDecision converts the JSON decision field (which may arrive as a
// JSON number → float64, an int, or a string) into a sdktools.ConfirmationResponse.
// Returns ok=false on missing/unrecognized values and logs a warning.
func parseConfirmDecision(payload map[string]any, log *slog.Logger) (sdktools.ConfirmationResponse, bool) {
	decisionVal, ok := payload["decision"]
	if !ok {
		log.Warn("tool confirmation response missing decision field")
		return 0, false
	}

	switch v := decisionVal.(type) {
	case float64:
		return sdktools.ConfirmationResponse(int(v)), true
	case int:
		return sdktools.ConfirmationResponse(v), true
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

// parseStepLimitResponse validates a frontend-supplied step-limit response
// string against the known enum values. Returns ok=false on unrecognized
// values, mirroring parseConfirmDecision's defense-in-depth discipline.
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

// handleToolConfirmResponse is the body of the EventToolConfirmResponse listener.
// Extracted from wireWailsEventListeners (W-23) so it can be unit-tested without
// a live Wails runtime.
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

	select {
	case confirmData.ch <- resp:
	default:
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
}

// handleToolJudgeRequest is the body of the EventToolJudgeRequest listener.
// It looks up the pending confirmation metadata and spawns runJudgeEvaluation
// asynchronously so the event listener returns promptly.
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

	a.judgeWG.Add(1)
	go func() {
		defer a.judgeWG.Done()
		a.runJudgeEvaluation(confirmID, pendingData, uiEmit, log)
	}()
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

	if a.app == nil {
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

	parentCtx := a.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	judgeCtx, judgeCancel := context.WithTimeout(parentCtx, 120*time.Second)
	defer judgeCancel()

	// Inject session dump writer for judge LLM call observability.
	if sess, ok := a.app.Manager().GetSession(pendingData.sessionID); ok {
		if dumpFile := sess.DumpFile(); dumpFile != nil {
			defer func() { _ = dumpFile.Close() }()
			judgeCtx = agent.WithDumpWriter(judgeCtx, dumpFile)
		}
	}

	responsePayload := session.JudgeResponsePayload{ConfirmID: confirmID}

	_, reasoning, err := a.app.EvaluateJudge(judgeCtx, pendingData.toolName, pendingData.input, pendingData.taskContext)
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

	select {
	case entry.ch <- resp:
	default:
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
}

// handlePlanApprovalResponse is the body of the EventPlanApprovalResponse listener.
func (a *App) handlePlanApprovalResponse(payload map[string]any, log *slog.Logger) {
	requestID, ok := stringField(payload, "request_id", "plan_approval response", log)
	if !ok {
		return
	}

	decision, _ := payload["decision"].(string)
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

	select {
	case entry.ch <- planApprovalResponse{Decision: decision, Feedback: feedback}:
	default:
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
}

// resolveGoalProposal delivers a user decision to a blocked goal-proposal
// channel and marks the persisted goal_proposal message resolved. Returns true
// when a pending proposal was found and resolved, false otherwise. Shared by
// the event-based path (handleGoalProposalResponse) and the RPC-based path
// (FrontendAPI.ConfirmGoal/CancelGoal, via the manager resolver).
func (a *App) resolveGoalProposal(requestID, decision, condition, verify, verificationMode, clarification string) bool {
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
		Clarification:    clarification,
	}
	select {
	case entry.ch <- resp:
	default:
		// Channel already resolved or receiver gone — clean up the stale entry.
	}
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
	return true
}

// handleGoalProposalResponse is the body of the EventGoalProposalResponse listener.
// Extracted from wireWailsEventListeners so it can be unit-tested without a live
// Wails runtime.
func (a *App) handleGoalProposalResponse(payload map[string]any, log *slog.Logger) {
	requestID, ok := stringField(payload, "request_id", "goal_proposal response", log)
	if !ok {
		return
	}
	decision, _ := payload["decision"].(string)
	if decision == "" {
		log.Warn("goal proposal response missing decision field", "request_id", requestID)
		return
	}
	condition, _ := payload["condition"].(string)
	verify, _ := payload["verify"].(string)
	verificationMode, _ := payload["verification_mode"].(string)
	clarification, _ := payload["clarification"].(string)

	if !a.resolveGoalProposal(requestID, decision, condition, verify, verificationMode, clarification) {
		log.Warn("no pending goal proposal for request_id", "request_id", requestID)
	}
}

// parseAskUserAnswers extracts the typed answers slice from the untyped JSON
// payload. Missing or malformed entries are silently skipped — the question
// IDs are owned by the orchestrator and unrecognized IDs would be rejected
// upstream anyway.
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

	select {
	case entry.ch <- resp:
	default:
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
}
