package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/orchestration"
)

// newHistoryTestOrchestrator builds an orchestrator with the standard test
// wiring used by conversation-history tests.
func newHistoryTestOrchestrator(mockLLM *mockLLMCaller) *Orchestrator {
	registry := createTestRegistry()
	pl, err := newCorePlanner(mockLLM, coretools.NewToolRegistry())
	if err != nil {
		panic(err)
	}
	return NewOrchestrator(OrchestratorConfig{
		MaxSteps: 10,
	}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		Planner:        pl,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
}

// routerResponse returns a canned routing decision response.
func routerResponse(needsClarification bool) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: `{"domain": "code", "complexity": 3, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": ` + boolString(needsClarification) + `}`,
		},
		StopReason: "end_turn",
	}
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// plannerResponse returns a canned single-step plan response.
func plannerResponse() *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: `{"steps": [{"id": "step_1", "summary": "Test", "description": "What: test\nHow: test\nWhere: test\nAcceptance Criteria: pass", "depends_on": [], "parallelizable": false, "estimated_tools": []}]}`,
		},
		StopReason: "end_turn",
	}
}

// executorFinishResponse returns a canned executor response that finishes with
// the given answer.
func executorFinishResponse(answer string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:      "assistant",
			Content:   "Task completed",
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "` + answer + `"}`)}},
		},
		StopReason: "tool_use",
	}
}

// TestConversationHistory_ClarificationRecorded verifies that a clarification
// short-circuit records both the user message and the clarifying question in
// the conversation history.
func TestConversationHistory_ClarificationRecorded(t *testing.T) {
	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{routerResponse(true)}}
	orchestrator := newHistoryTestOrchestrator(mockLLM)

	result, err := orchestrator.HandleMessage(context.Background(), "do something vague", "session-1", HandleOptions{ExecutionMode: "advanced"})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}
	if result == nil || result.RoutingDecision == nil || !result.RoutingDecision.NeedsClarification {
		t.Fatal("expected clarification result")
	}

	history := orchestrator.ConversationHistory()
	if len(history) != 2 {
		t.Fatalf("expected 2 history messages (user + clarification), got %d: %+v", len(history), history)
	}
	if history[0].Role != "user" || history[0].Content != "do something vague" {
		t.Errorf("unexpected user message: %+v", history[0])
	}
	if history[1].Role != "assistant" || !strings.Contains(history[1].Content, "clarify") {
		t.Errorf("expected clarification text in assistant message, got %+v", history[1])
	}
}

// TestConversationHistory_PlanningFailureRecorded verifies that a hard
// planning failure records the user message plus a failure note so the
// rejected request stays visible to future routing and planning.
func TestConversationHistory_PlanningFailureRecorded(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx == 1 {
				return routerResponse(false), nil
			}
			return nil, errors.New("planner LLM unavailable")
		},
	}
	orchestrator := newHistoryTestOrchestrator(mockLLM)

	_, err := orchestrator.HandleMessage(context.Background(), "build the feature", "session-1", HandleOptions{ExecutionMode: "advanced"})
	if err == nil {
		t.Fatal("expected planning failure")
	}

	history := orchestrator.ConversationHistory()
	if len(history) != 2 {
		t.Fatalf("expected 2 history messages (user + failure note), got %d: %+v", len(history), history)
	}
	if history[0].Role != "user" || history[0].Content != "build the feature" {
		t.Errorf("unexpected user message: %+v", history[0])
	}
	if history[1].Role != "assistant" || !strings.Contains(history[1].Content, "[Task failed before completion:") {
		t.Errorf("expected failure note in assistant message, got %+v", history[1])
	}
}

// TestConversationHistory_EmptyPlanRecorded verifies that the empty-plan guard
// still results in the rejected request being recorded (via the centralized
// recordConversationOutcome defer).
func TestConversationHistory_EmptyPlanRecorded(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx == 1 {
				return routerResponse(false), nil
			}
			// Planner returns invalid JSON on every attempt, exhausting retries.
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "not json at all"},
				StopReason: "end_turn",
			}, nil
		},
	}
	orchestrator := newHistoryTestOrchestrator(mockLLM)

	_, err := orchestrator.HandleMessage(context.Background(), "impossible request", "session-1", HandleOptions{ExecutionMode: "advanced"})
	if err == nil {
		t.Fatal("expected planning failure")
	}

	history := orchestrator.ConversationHistory()
	if len(history) != 2 {
		t.Fatalf("expected 2 history messages, got %d: %+v", len(history), history)
	}
	if history[0].Content != "impossible request" {
		t.Errorf("unexpected user message: %+v", history[0])
	}
	if !strings.Contains(history[1].Content, "[Task failed before completion:") {
		t.Errorf("expected failure note, got %+v", history[1])
	}
}

// TestConversationHistory_CancellationRecorded verifies that a cancelled
// request records the user message plus the cancellation note.
func TestConversationHistory_CancellationRecorded(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, ctx.Err()
		},
	}
	orchestrator := newHistoryTestOrchestrator(mockLLM)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := orchestrator.HandleMessage(ctx, "cancelled request", "session-1", HandleOptions{ExecutionMode: "advanced"})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	history := orchestrator.ConversationHistory()
	if len(history) != 2 {
		t.Fatalf("expected 2 history messages, got %d: %+v", len(history), history)
	}
	if history[0].Role != "user" || history[0].Content != "cancelled request" {
		t.Errorf("unexpected user message: %+v", history[0])
	}
	if history[1].Content != HistoryNoteCancelled {
		t.Errorf("expected cancellation note %q, got %q", HistoryNoteCancelled, history[1].Content)
	}
}

// TestConversationHistory_PlanReviewAndApproval verifies the plan review flow:
// the user message is recorded when the plan is generated for review, and the
// assistant output is recorded when the approved plan is executed via Resume.
func TestConversationHistory_PlanReviewAndApproval(t *testing.T) {
	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
		routerResponse(false),
		plannerResponse(),
		executorFinishResponse("Executed after approval"),
	}}
	orchestrator := newHistoryTestOrchestrator(mockLLM)

	result, err := orchestrator.HandleMessage(context.Background(), "review this plan", "session-1", HandleOptions{
		ExecutionMode:   "advanced",
		PlanReview:      true,
		SessionPlansDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}
	if result.PlanReviewPhase != "awaiting_accept" {
		t.Fatalf("expected awaiting_accept phase, got %q", result.PlanReviewPhase)
	}

	// After plan review initiation only the user message must be recorded —
	// the exchange is not complete until the plan is approved and executed.
	history := orchestrator.ConversationHistory()
	if len(history) != 1 {
		t.Fatalf("expected 1 history message (user only), got %d: %+v", len(history), history)
	}
	if history[0].Role != "user" || history[0].Content != "review this plan" {
		t.Errorf("unexpected user message: %+v", history[0])
	}

	// Approve: resume execution with the plan stored on the blackboard.
	resumeResult, err := orchestrator.Resume(context.Background(), result.Blackboard, result.RoutingDecision)
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if resumeResult.Output != "Executed after approval" {
		t.Fatalf("unexpected resume output: %q", resumeResult.Output)
	}

	history = orchestrator.ConversationHistory()
	if len(history) != 2 {
		t.Fatalf("expected 2 history messages (user + assistant), got %d: %+v", len(history), history)
	}
	if history[1].Role != "assistant" || history[1].Content != "Executed after approval" {
		t.Errorf("expected resumed output in assistant message, got %+v", history[1])
	}
}

// TestConversationHistory_ResumeAppendsAssistant verifies that resuming an
// interrupted task (e.g. after an app restart) records the assistant output
// in the conversation history.
func TestConversationHistory_ResumeAppendsAssistant(t *testing.T) {
	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
		executorFinishResponse("Resumed and finished"),
	}}
	orchestrator := newHistoryTestOrchestrator(mockLLM)

	// Simulate the restored history: the user message that spawned the task
	// was restored from the message store.
	orchestrator.SetConversationHistory([]llm.Message{
		{Role: "user", Content: "long running task"},
	})

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("long running task")
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Summary: "Do work", Description: "What: work\nHow: work\nWhere: here\nAcceptance Criteria: done"},
		},
	})

	result, err := orchestrator.Resume(context.Background(), bb, nil)
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if result.Output != "Resumed and finished" {
		t.Fatalf("unexpected output: %q", result.Output)
	}

	history := orchestrator.ConversationHistory()
	if len(history) != 2 {
		t.Fatalf("expected 2 history messages (user + assistant), got %d: %+v", len(history), history)
	}
	if history[1].Role != "assistant" || history[1].Content != "Resumed and finished" {
		t.Errorf("expected resumed output in history, got %+v", history[1])
	}
}

// TestConversationHistory_ResumeFailureRecordsNote verifies that a failed
// resume records a failure note so the exchange stays visible in history.
func TestConversationHistory_ResumeFailureRecordsNote(t *testing.T) {
	mockLLM := &mockLLMCaller{}
	orchestrator := newHistoryTestOrchestrator(mockLLM)

	// A blackboard without a plan makes Resume fail immediately.
	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("broken task")

	_, err := orchestrator.Resume(context.Background(), bb, nil)
	if err == nil {
		t.Fatal("expected Resume error for missing plan")
	}

	history := orchestrator.ConversationHistory()
	if len(history) != 1 {
		t.Fatalf("expected 1 history message (failure note), got %d: %+v", len(history), history)
	}
	if history[0].Role != "assistant" || !strings.Contains(history[0].Content, "[Task failed before completion:") {
		t.Errorf("expected failure note, got %+v", history[0])
	}
}

// TestPlanner_FirstMessageReceivesHistory verifies that the first-message
// planning path (Planner.Plan) receives the conversation history — the
// scenario after a backend restart when the continuation anchor is
// unavailable and a fresh plan is generated for a follow-up message.
func TestPlanner_FirstMessageReceivesHistory(t *testing.T) {
	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
		routerResponse(false),
		plannerResponse(),
		executorFinishResponse("Done"),
	}}
	orchestrator := newHistoryTestOrchestrator(mockLLM)

	// Simulate history restored from the message store after a restart.
	orchestrator.SetConversationHistory([]llm.Message{
		{Role: "user", Content: "previous question about auth middleware"},
		{Role: "assistant", Content: "implemented the auth middleware"},
	})

	_, err := orchestrator.HandleMessage(context.Background(), "now add tests for it", "session-1", HandleOptions{ExecutionMode: "advanced"})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	// Call 2 is the planner (call 1 is the router).
	if len(mockLLM.calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(mockLLM.calls))
	}
	plannerReq := mockLLM.calls[1]
	var systemPrompt strings.Builder
	for _, msg := range plannerReq.Messages {
		if msg.Role == "system" {
			systemPrompt.WriteString(msg.Content)
		}
	}
	if !strings.Contains(systemPrompt.String(), "previous question about auth middleware") {
		t.Error("planner system prompt should contain the prior user message from conversation history")
	}
	if !strings.Contains(systemPrompt.String(), "implemented the auth middleware") {
		t.Error("planner system prompt should contain the prior assistant message from conversation history")
	}
}

// TestConversationHistory_RetryAfterFailureNotDuplicated verifies that when a
// failed attempt is retried with the same message (the session manager's
// continuation fallback), the failed pair is replaced by the retry's outcome
// instead of duplicating the user message.
func TestConversationHistory_RetryAfterFailureNotDuplicated(t *testing.T) {
	callIdx := 0
	failFirstPlanning := true
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // Router — failing attempt
				return routerResponse(false), nil
			case 2: // Planner — fail once
				if failFirstPlanning {
					failFirstPlanning = false
					return nil, errors.New("planner LLM unavailable")
				}
				return plannerResponse(), nil
			case 3: // Router — retry
				return routerResponse(false), nil
			case 4: // Planner — retry succeeds
				return plannerResponse(), nil
			default: // Executor — finish
				return executorFinishResponse("Done after retry"), nil
			}
		},
	}
	orchestrator := newHistoryTestOrchestrator(mockLLM)

	// First attempt fails at planning.
	if _, err := orchestrator.HandleMessage(context.Background(), "retry me", "session-1", HandleOptions{ExecutionMode: "advanced"}); err == nil {
		t.Fatal("expected first attempt to fail")
	}
	// Retry with the same message (as the session manager's fallback does).
	if _, err := orchestrator.HandleMessage(context.Background(), "retry me", "session-1", HandleOptions{ExecutionMode: "advanced"}); err != nil {
		t.Fatalf("retry failed: %v", err)
	}

	history := orchestrator.ConversationHistory()
	if len(history) != 2 {
		t.Fatalf("expected 2 history messages (failed pair replaced), got %d: %+v", len(history), history)
	}
	if history[0].Role != "user" || history[0].Content != "retry me" {
		t.Errorf("unexpected user message: %+v", history[0])
	}
	if history[1].Content != "Done after retry" {
		t.Errorf("expected retry output, got %q", history[1].Content)
	}
}
