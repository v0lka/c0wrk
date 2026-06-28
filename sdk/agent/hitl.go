package agent

import (
	"context"
	"encoding/json"
)

// HITLHandler provides hooks for human-in-the-loop interaction during agent execution.
// Implementations can intercept tool calls for confirmation/modification and
// decide what happens when the agent reaches its step budget.
//
// All methods are called synchronously from the executor loop. Implementations
// should return promptly or respect context cancellation to avoid blocking execution.
type HITLHandler interface {
	// OnToolCall is invoked before executing a tool. The handler can:
	//   - Allow the tool call as-is (return Allow=true)
	//   - Reject the tool call (return Allow=false)
	//   - Modify the tool input (return Allow=true with ModifiedInput)
	// Return nil decision to allow the call unchanged (same as Allow=true, no modification).
	OnToolCall(ctx context.Context, toolName string, input json.RawMessage) (*HITLToolDecision, error)

	// OnStepLimit is invoked when the agent exhausts its step budget or a circuit
	// breaker abort threshold is reached. The reason parameter describes why
	// execution was paused (empty string for normal step limit exhaustion).
	// The response determines whether execution continues or stops.
	OnStepLimit(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error)
}

// HITLToolDecision represents the handler's decision about a tool call.
type HITLToolDecision struct {
	// Allow determines whether the tool call should proceed.
	Allow bool
	// ModifiedInput, when non-nil and Allow is true, replaces the original tool input.
	ModifiedInput json.RawMessage
	// Reason is a human-readable explanation for the decision (shown in UI).
	Reason string
}

// NoopHITLHandler is a default HITLHandler that:
//   - Allows all tool calls
//   - Denies step limit extensions (stops at budget)
type NoopHITLHandler struct{}

// allowDecisionSentinel is the canonical allow-all decision, returned by copy
// from OnToolCall to prevent callers from mutating the shared instance.
var allowDecisionSentinel = HITLToolDecision{Allow: true}

// OnToolCall allows all tool calls unchanged.
func (NoopHITLHandler) OnToolCall(_ context.Context, _ string, _ json.RawMessage) (*HITLToolDecision, error) {
	d := allowDecisionSentinel
	return &d, nil
}

// OnStepLimit always denies further steps.
func (NoopHITLHandler) OnStepLimit(_ context.Context, _, _ int, _ string) (StepLimitResponse, error) {
	return StepLimitDeny, nil
}
