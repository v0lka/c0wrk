package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// newHistoryTestOrchestrator builds an orchestrator with the standard test
// wiring used by conversation-history tests.
func newHistoryTestOrchestrator(mockLLM *mockLLMCaller) *Orchestrator {
	registry := createTestRegistry()
	return NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
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

	_, err := orchestrator.HandleMessage(context.Background(), "build the feature", "session-1", HandleOptions{})
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

	_, err := orchestrator.HandleMessage(ctx, "cancelled request", "session-1", HandleOptions{})
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

	result, err := orchestrator.Resume(context.Background(), bb, nil, "", nil, nil, "")
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

// TestResume_InjectsPriorConversationHistory verifies that resuming a failed
// task preserves the session's prior dialogue (previous tasks) in the resumed
// Conductor's context. Regression: Resume previously passed nil conversation
// history to the Conductor, so a resumed failed task lost all context from
// earlier tasks in the chat session.
func TestResume_InjectsPriorConversationHistory(t *testing.T) {
	var capturedCM *mockContextManager
	captureFactory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, _ ...orchestration.PruningOverride) ContextManager {
		cm := &mockContextManager{systemPrompt: systemPrompt}
		capturedCM = cm
		return cm
	}

	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
		executorFinishResponse("Resumed and finished"),
	}}
	registry := createTestRegistry()
	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: captureFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	// Prior tasks in the session, followed by the interrupted task's user
	// message that Resume treats as the current task.
	prior := []llm.Message{
		{Role: "user", Content: "first task"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second task"},
		{Role: "assistant", Content: "second answer"},
		{Role: "user", Content: "failed task"},
	}
	orchestrator.SetConversationHistory(prior)

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("failed task")

	if _, err := orchestrator.Resume(context.Background(), bb, nil, "", nil, nil, ""); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	if capturedCM == nil {
		t.Fatal("context factory was not invoked")
	}
	if len(capturedCM.priorConversation) == 0 {
		t.Fatal("resumed conductor did not receive prior conversation history")
	}

	// The earlier tasks' dialogue must be present in the injected history.
	var got strings.Builder
	for _, msg := range capturedCM.priorConversation {
		got.WriteString(msg.Content)
		got.WriteString("\n")
	}
	for _, want := range []string{"first task", "first answer", "second task", "second answer"} {
		if !strings.Contains(got.String(), want) {
			t.Errorf("prior conversation history missing %q; got:\n%s", want, got.String())
		}
	}
}

// TestConversationHistory_RetryAfterFailureNotDuplicated verifies that when a
// failed attempt is retried with the same message (the session manager's
// continuation fallback), the failed pair is replaced by the retry's outcome
// instead of duplicating the user message.
func TestConversationHistory_RetryAfterFailureNotDuplicated(t *testing.T) {
	callIdx := 0
	failFirstAttempt := true
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // Router — failing attempt
				return routerResponse(false), nil
			case 2: // Conductor — fail once
				if failFirstAttempt {
					failFirstAttempt = false
					return nil, errors.New("conductor LLM unavailable")
				}
				return executorFinishResponse("Done after retry"), nil
			case 3: // Router — retry
				return routerResponse(false), nil
			default: // Conductor — retry succeeds with finish tool
				return executorFinishResponse("Done after retry"), nil
			}
		},
	}
	orchestrator := newHistoryTestOrchestrator(mockLLM)

	// First attempt fails.
	if _, err := orchestrator.HandleMessage(context.Background(), "retry me", "session-1", HandleOptions{}); err == nil {
		t.Fatal("expected first attempt to fail")
	}
	// Retry with the same message (as the session manager's fallback does).
	if _, err := orchestrator.HandleMessage(context.Background(), "retry me", "session-1", HandleOptions{}); err != nil {
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

// TestTruncateHistory verifies that truncateHistory keeps the most recent
// messages and returns the slice unchanged when within the window.
func TestTruncateHistory(t *testing.T) {
	history := []llm.Message{
		{Role: "user", Content: "msg1"},
		{Role: "assistant", Content: "resp1"},
		{Role: "user", Content: "msg2"},
		{Role: "assistant", Content: "resp2"},
		{Role: "user", Content: "msg3"},
		{Role: "assistant", Content: "resp3"},
	}

	// window >= len: return as-is
	got := truncateHistory(history, 10)
	if len(got) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(got))
	}

	// window = 2: keep last 2
	got = truncateHistory(history, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Content != "msg3" || got[1].Content != "resp3" {
		t.Errorf("expected last 2 messages, got %s, %s", got[0].Content, got[1].Content)
	}

	// window = 0: return as-is (disabled)
	got = truncateHistory(history, 0)
	if len(got) != 6 {
		t.Fatalf("expected 6 messages (window=0 = disabled), got %d", len(got))
	}

	// empty history: return empty
	got = truncateHistory(nil, 20)
	if len(got) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(got))
	}
}

// TestDropFailedExchangeTail verifies the injection-time mirror of
// appendHistory's retry collapse ([45]): a trailing failed exchange of the
// given request is dropped, while cancelled notes, non-matching requests, and
// short histories are preserved.
func TestDropFailedExchangeTail(t *testing.T) {
	failedTail := []llm.Message{
		{Role: "user", Content: "earlier question"},
		{Role: "assistant", Content: "earlier answer"},
		{Role: "user", Content: "retry me"},
		{Role: "assistant", Content: HistoryNoteFailed("llm unavailable")},
	}

	// Matching failed tail → dropped.
	got := dropFailedExchangeTail(failedTail, "retry me")
	if len(got) != 2 {
		t.Fatalf("expected 2 messages after dropping the failed tail, got %d: %+v", len(got), got)
	}
	if got[1].Content != "earlier answer" {
		t.Errorf("unexpected tail after drop: %+v", got[1])
	}

	// Cancelled tail → kept (appendHistory does not collapse cancellations).
	cancelled := []llm.Message{
		{Role: "user", Content: "retry me"},
		{Role: "assistant", Content: HistoryNoteCancelled},
	}
	if got := dropFailedExchangeTail(cancelled, "retry me"); len(got) != 2 {
		t.Errorf("cancelled tail must be kept, got %d messages", len(got))
	}

	// Different request → kept.
	if got := dropFailedExchangeTail(failedTail, "another message"); len(got) != 4 {
		t.Errorf("non-matching failed tail must be kept, got %d messages", len(got))
	}

	// Empty request / short history → unchanged.
	if got := dropFailedExchangeTail(failedTail, ""); len(got) != 4 {
		t.Errorf("empty request must keep history, got %d messages", len(got))
	}
	short := []llm.Message{{Role: "user", Content: "retry me"}}
	if got := dropFailedExchangeTail(short, "retry me"); len(got) != 1 {
		t.Errorf("short history must be kept, got %d messages", len(got))
	}
}

// TestResume_InjectedHistoryDropsFailedExchangeTail verifies the [45] wiring:
// Resume injects the conversation history alongside a taskMessage that repeats
// the original request; when the history tail is the recorded failure of that
// exact request, the tail must be dropped from the injected history so the
// model does not see the same request twice with a failure marker in between.
// Earlier dialogue must survive the drop.
func TestResume_InjectedHistoryDropsFailedExchangeTail(t *testing.T) {
	var capturedCM *mockContextManager
	captureFactory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, _ ...orchestration.PruningOverride) ContextManager {
		cm := &mockContextManager{systemPrompt: systemPrompt}
		capturedCM = cm
		return cm
	}

	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
		executorFinishResponse("Resumed and finished"),
	}}
	registry := createTestRegistry()
	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: captureFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	prior := []llm.Message{
		{Role: "user", Content: "first task"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "failed task"},
		{Role: "assistant", Content: HistoryNoteFailed("conductor LLM unavailable")},
	}
	orchestrator.SetConversationHistory(prior)

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("failed task")

	if _, err := orchestrator.Resume(context.Background(), bb, nil, "", nil, nil, ""); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	if capturedCM == nil {
		t.Fatal("context factory was not invoked")
	}
	injected := capturedCM.priorConversation
	if len(injected) != 2 {
		t.Fatalf("expected 2 injected messages (failed tail dropped), got %d: %+v", len(injected), injected)
	}
	var got strings.Builder
	for _, msg := range injected {
		got.WriteString(msg.Content)
		got.WriteString("\n")
	}
	if !strings.Contains(got.String(), "first task") || !strings.Contains(got.String(), "first answer") {
		t.Errorf("prior dialogue lost by the failed-tail drop; got:\n%s", got.String())
	}
	if strings.Contains(got.String(), "Task failed before completion") {
		t.Errorf("failed-exchange tail leaked into the injected history; got:\n%s", got.String())
	}
}
