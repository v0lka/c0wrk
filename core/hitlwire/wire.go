// Package hitlwire holds the transport-neutral HITL wire-protocol parsing:
// payload shape checks, decision-string validation, and the buffered
// response delivery shared by every HITL response surface (Wails events
// today, web-access WS frames and ACP mappings next). It owns no transport
// and no pending-state: callers wire parsed decisions into their own
// delivery channels. See specs/contracts/event-catalog.md.
package hitlwire

import (
	"fmt"
	"log/slog"
	"math"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ExtractPayload performs the standard data[0].(map[string]any) shape check
// shared by every HITL event handler. Returns the payload and ok=true on
// success, or logs a warning and returns ok=false on missing/wrong-shape data.
func ExtractPayload(eventName string, data []any, log *slog.Logger) (map[string]any, bool) {
	return extractPayload(eventName, data, log)
}

// extractPayload performs the standard data[0].(map[string]any) shape check
// shared by every HITL event handler. Returns the payload and ok=true on
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

// ConfirmDecision converts the JSON decision field (JSON number →
// float64, an int, or a string; a non-integral number is rejected) into a
// sdktools.ConfirmationResponse; ok=false on missing/unrecognized values,
// with a warning logged.
func ConfirmDecision(payload map[string]any, log *slog.Logger) (sdktools.ConfirmationResponse, bool) {
	return parseConfirmDecision(payload, log)
}

// parseConfirmDecision converts the JSON decision field (JSON number →
// float64, an int, or a string; a non-integral number is rejected) into a
// sdktools.ConfirmationResponse; ok=false on missing/unrecognized values,
// with a warning logged.
func parseConfirmDecision(payload map[string]any, log *slog.Logger) (sdktools.ConfirmationResponse, bool) {
	decisionVal, ok := payload["decision"]
	if !ok {
		log.Warn("tool confirmation response missing decision field")
		return 0, false
	}

	switch v := decisionVal.(type) {
	case float64:
		// A fractional number is a malformed settlement, not a truncation
		// input: int(v) toward zero would silently turn 0.5 into
		// ConfirmAllowOnce (the enum's zero value — the least-safe outcome).
		// Reject it like any other unrecognized value.
		if v != math.Trunc(v) {
			log.Warn("non-integral confirmation decision", "decision", v)
			return 0, false
		}
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

// StepLimitResponse validates a step-limit response string against the
// known enum values; ok=false on unrecognized values.
func StepLimitResponse(v string) (agent.StepLimitResponse, bool) {
	return parseStepLimitResponse(v)
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

// PlanApprovalDecision validates a plan-review decision string against
// the event-catalog values (approve / request_changes / abandon); ok=false on
// missing/unrecognized. An unrecognized decision is a malformed response, not
// an implicit rejection — the pending approval stays alive for a valid retry.
func PlanApprovalDecision(v string) (string, bool) {
	return parsePlanApprovalDecision(v)
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

// GoalProposalDecision validates a goal-proposal decision (approve /
// cancel) under the same discipline: ok=false on unrecognized values leaves
// the pending proposal alive instead of consuming it as an implicit cancel.
func GoalProposalDecision(v string) (string, bool) {
	return parseGoalProposalDecision(v)
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

// AskUserAnswers extracts the typed answers slice from the untyped JSON
// payload; malformed entries are skipped (unrecognized question IDs are
// rejected upstream anyway).
func AskUserAnswers(payload map[string]any) tools.AskUserResponse {
	return parseAskUserAnswers(payload)
}

// parseAskUserAnswers extracts the typed answers slice from the untyped JSON
// payload; malformed entries are skipped (unrecognized question IDs are
// rejected upstream anyway).
func parseAskUserAnswers(payload map[string]any) tools.AskUserResponse {
	var resp tools.AskUserResponse

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
		var answer tools.AskUserAnswer
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

// StringField extracts a string field from a payload, logging a warning if the
// field is missing or has the wrong type.
func StringField(payload map[string]any, key, eventName string, log *slog.Logger) (string, bool) {
	return stringField(payload, key, eventName, log)
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

// ConfirmDecisionString renders a ConfirmationResponse back to the wire
// string the frontend protocol uses ("allow_once" / "deny" /
// "deny_and_stop"), so a hitl_resolved event can carry the effective decision
// as text. Returns "" for out-of-range values (the field is omitempty).
func ConfirmDecisionString(resp sdktools.ConfirmationResponse) string {
	return confirmDecisionString(resp)
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

// DeliverResponse performs the best-effort buffered send shared by every HITL
// response handler and reports whether the decision was DELIVERED: a full
// channel means the receiver is gone or a faster responder won, so the caller
// must not broadcast the dropped response as the effective settlement.
func DeliverResponse[T any](ch chan T, resp T) bool {
	return deliverResponse(ch, resp)
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
