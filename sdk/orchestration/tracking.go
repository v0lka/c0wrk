package orchestration

import (
	"context"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
)

// tokenTrackingCaller wraps an LLMCaller to report token usage after every call.
// This ensures that service-level LLM calls (Router, Planner, Reflector)
// have their token consumption accumulated in session totals,
// not just the Executor calls.
type tokenTrackingCaller struct {
	inner   agent.LLMCaller
	emitter agent.AgentEvents
}

// NewTokenTrackingCaller wraps an LLMCaller so that every successful Call
// reports token usage via emitter.TokensUsed.
func NewTokenTrackingCaller(inner agent.LLMCaller, emitter agent.AgentEvents) agent.LLMCaller {
	if emitter == nil {
		return inner
	}
	return &tokenTrackingCaller{inner: inner, emitter: emitter}
}

// Call delegates to the wrapped LLMCaller and reports token usage on success.
func (t *tokenTrackingCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	resp, err := t.inner.Call(ctx, req)
	if err == nil && resp != nil {
		t.emitter.TokensUsed(resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Model, resp.Tier)
	}
	return resp, err
}
