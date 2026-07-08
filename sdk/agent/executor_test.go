package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
)

// defaultCircuitBreakerConfig provides the standard circuit breaker thresholds for tests.
// Note: Fruitless and SameTool thresholds are set high to avoid triggering in typical test scenarios.
// Production defaults are: FruitlessNudge=5, FruitlessAbort=8, SameToolNudge=8, SameToolAbort=12.
var defaultCircuitBreakerConfig = CircuitBreakerConfig{
	RepeatNudgeThreshold:         3,
	RepeatAbortThreshold:         4,
	TruncationAbortThreshold:     3,
	ParseErrorAbortThreshold:     3,
	FruitlessNudgeThreshold:      50, // high to avoid triggering in tests
	FruitlessAbortThreshold:      60,
	FruitlessMaxResultLen:        32,
	SameToolRepeatNudgeThreshold: 50, // high to avoid triggering in tests
	SameToolRepeatAbortThreshold: 60,
	SameToolResultSizeDelta:      64,
}

// --- NewExecutor tests ---

func TestNewExecutor_NilEmitter(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	if exec.emitter == nil {
		t.Fatal("emitter should not be nil when nil is passed")
	}
	if _, ok := exec.emitter.(*NoopEvents); !ok {
		t.Errorf("emitter should be *NoopEvents, got %T", exec.emitter)
	}
}

func TestSetPlanContext(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetPlanContext("step_3", 3, 5)
	if exec.planStepID != "step_3" {
		t.Errorf("planStepID = %q, want %q", exec.planStepID, "step_3")
	}
	if exec.planStepIndex != 3 {
		t.Errorf("planStepIndex = %d, want 3", exec.planStepIndex)
	}
	if exec.planStepTotal != 5 {
		t.Errorf("planStepTotal = %d, want 5", exec.planStepTotal)
	}
}

// --- Run() tests ---

func TestExecutor_Run_FinishTool(t *testing.T) {
	// LLM returns a finish tool call directly
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseFinish("I know the answer", "The answer is 42"),
		},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	result, err := exec.Run(context.Background(), nil, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}
	if result.Output != "The answer is 42" {
		t.Errorf("Output = %q, want %q", result.Output, "The answer is 42")
	}
	if len(result.Steps) != 1 {
		t.Errorf("len(Steps) = %d, want 1", len(result.Steps))
	}
}

func TestExecutor_Run_ToolCallThenFinish(t *testing.T) {
	toolInput := json.RawMessage(`{"path": "/tmp/test"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("Let me read the file", "read_file", toolInput),
			llmResponseFinish("Got the content", "file content here"),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["read_file"] = tools.ToolResult{Content: "hello world"}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "read_file", Description: "read a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}
	if result.Output != "file content here" {
		t.Errorf("Output = %q", result.Output)
	}
	if len(result.Steps) != 2 {
		t.Errorf("len(Steps) = %d, want 2", len(result.Steps))
	}
}

func TestExecutor_Run_MaxStepsExhausted(t *testing.T) {
	// LLM always returns a tool call that never finishes
	toolInput := json.RawMessage(`{"q":"test"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("searching", "search", toolInput),
			llmResponseWithToolCall("searching more", "search2", json.RawMessage(`{"q":"test2"}`)),
			llmResponseWithToolCall("still searching", "search3", json.RawMessage(`{"q":"test3"}`)),
		},
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 3, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Finished {
		t.Error("expected Finished=false when max steps exhausted")
	}
}

func TestExecutor_Run_TextOnlyEndTurn_ImmediateFinish(t *testing.T) {
	// end_turn without tools → immediate implicit finish (no nudge)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseEndTurn("The answer is yes"),
		},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true (immediate yield)")
	}
	if result.Output != "The answer is yes" {
		t.Errorf("Output = %q, want %q", result.Output, "The answer is yes")
	}
	// Should have 1 step (the implicit finish, no nudge steps)
	if len(result.Steps) != 1 {
		t.Errorf("expected 1 step (immediate finish), got %d", len(result.Steps))
	}
}

func TestExecutor_Run_NudgeOnNoToolsNoEndTurn(t *testing.T) {
	// No tool calls, stop_reason != "end_turn" → nudge (attempt 1)
	// Second call: still no tools, no end_turn → nudge (attempt 2)
	// Third call: end_turn → accept
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message:    llm.Message{Role: "assistant", Content: "hmm"},
				StopReason: "max_tokens",
				Usage:      llm.TokenUsage{InputTokens: 50, OutputTokens: 50},
			},
			{
				Message:    llm.Message{Role: "assistant", Content: "hmm2"},
				StopReason: "max_tokens",
				Usage:      llm.TokenUsage{InputTokens: 50, OutputTokens: 50},
			},
			llmResponseEndTurn("final answer"),
		},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "tool1", Description: "t", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}
}

func TestExecutor_Run_NoToolsImplicitFinish(t *testing.T) {
	// No task tools → end_turn accepted immediately (no nudge)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseEndTurn("done"),
		},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	result, err := exec.Run(context.Background(), nil, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}
}

func TestExecutor_Run_LLMError(t *testing.T) {
	mockLLM := &mockLLMCaller{
		errors: []error{errors.New("api error")},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	_, err := exec.Run(context.Background(), nil, cm)
	if err == nil {
		t.Fatal("expected error from LLM")
	}
}

func TestExecutor_Run_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{llmResponseEndTurn("hi")},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	_, err := exec.Run(ctx, nil, cm)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestExecutor_Run_ToolExecutionError(t *testing.T) {
	toolInput := json.RawMessage(`{}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("trying", "broken_tool", toolInput),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.errors["broken_tool"] = errors.New("infrastructure failure")

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	_, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "broken_tool", Description: "broken", Source: "core"},
	}, cm)
	if err == nil {
		t.Fatal("expected tool execution error")
	}
}

func TestExecutor_Run_ToolNotFound_Recovers(t *testing.T) {
	// When LLM hallucinates a tool name, the registry returns IsError=true.
	// Executor should NOT treat this as an infrastructure error;
	// it should feed the error back to the LLM so it can correct itself.
	toolInput := json.RawMessage(`{}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("trying ghost", "ghost_tool", toolInput),
			llmResponseFinish("fixed", "done"),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["ghost_tool"] = tools.ToolResult{Content: "tool not found: ghost_tool", IsError: true}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "read_file", Description: "read a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after recovery")
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}
	// First step should contain the tool-not-found observation
	if len(result.Steps) < 1 {
		t.Fatalf("expected at least 1 step, got %d", len(result.Steps))
	}
	if result.Steps[0].Observation != "tool not found: ghost_tool" {
		t.Errorf("expected observation %q, got %q", "tool not found: ghost_tool", result.Steps[0].Observation)
	}
}

func TestExecutor_Run_CircuitBreaker_Abort(t *testing.T) {
	// Same tool call repeated ≥ RepeatAbortThreshold (4) times
	sameInput := json.RawMessage(`{"q":"same"}`)
	responses := make([]*llm.ChatResponse, defaultCircuitBreakerConfig.RepeatAbortThreshold+1)
	for i := range responses {
		responses[i] = llmResponseWithToolCall("trying", "search", sameInput)
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Finished {
		t.Error("expected Finished=false on circuit breaker abort")
	}
	if !strings.Contains(result.Output, "Aborted") {
		t.Errorf("expected abort message in output, got %q", result.Output)
	}
}

func TestExecutor_Run_CircuitBreaker_Nudge(t *testing.T) {
	// Same tool call repeated ≥ repeatNudgeThreshold (3) times → nudge, then different tool → finish
	sameInput := json.RawMessage(`{"q":"same"}`)
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("try1", "search", sameInput),
		llmResponseWithToolCall("try2", "search", sameInput),
		llmResponseWithToolCall("try3", "search", sameInput), // triggers nudge
		llmResponseFinish("ok", "done"),                      // after nudge
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after circuit breaker nudge + finish")
	}
}

func TestExecutor_Run_ToolResultBudget(t *testing.T) {
	// Create a very large tool result that exceeds the budget
	largeContent := strings.Repeat("x", 10000) // ~2500 tokens at len/4
	toolInput := json.RawMessage(`{}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("reading", "big_tool", toolInput),
			llmResponseFinish("done", "summary"),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["big_tool"] = tools.ToolResult{Content: largeContent}
	cm := newMockContextManager()
	cm.availableTokens = 5000

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{
		HardCapTokens:   500,
		MaxFillFraction: 0.3,
	}, defaultCircuitBreakerConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "big_tool", Description: "big", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}

	// The tool result in the step should be truncated
	if len(result.Steps) < 1 {
		t.Fatal("expected at least 1 step")
	}
	obs := result.Steps[0].Observation
	if !strings.Contains(obs, "OUTPUT TRUNCATED") {
		t.Error("expected truncation notice in observation")
	}
}

func TestExecutor_Run_EmptyToolResult(t *testing.T) {
	toolInput := json.RawMessage(`{}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("running", "empty_tool", toolInput),
			llmResponseFinish("done", "ok"),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["empty_tool"] = tools.ToolResult{Content: ""}
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "empty_tool", Description: "empty", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty result should be replaced with "(no output)"
	if result.Steps[0].Observation != "(no output)" {
		t.Errorf("expected '(no output)', got %q", result.Steps[0].Observation)
	}
}

func TestExecutor_Run_CompactionTriggered(t *testing.T) {
	toolInput := json.RawMessage(`{}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("thinking", "tool1", toolInput),
			llmResponseFinish("done", "result"),
		},
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()
	cm.fillCheck = FillCheck{Percent: 80, Status: "compact", Used: 80000, Max: 100000}

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "tool1", Description: "t", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}
	if cm.compactCalled == 0 {
		t.Error("expected Compact() to be called")
	}
}

func TestExecutor_Run_EmergencyCompaction(t *testing.T) {
	toolInput := json.RawMessage(`{}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("thinking", "tool1", toolInput),
			llmResponseFinish("done", "result"),
		},
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()
	cm.fillCheck = FillCheck{Percent: 95, Status: "emergency", Used: 95000, Max: 100000}

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "tool1", Description: "t", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}
	if cm.compactCalled == 0 {
		t.Error("expected Compact() to be called for emergency")
	}
}

func TestExecutor_Run_ContextExceededError_ReactiveCompaction(t *testing.T) {
	mockLLM := &mockLLMCaller{
		errors: []error{
			errors.New("context length exceeded"),
			nil,
		},
		responses: []*llm.ChatResponse{
			nil, // error on first
			llmResponseFinish("done", "recovered"),
		},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	result, err := exec.Run(context.Background(), nil, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after reactive compaction")
	}
	if cm.compactCalled == 0 {
		t.Error("expected Compact() to be called for reactive compaction")
	}
}

func TestExecutor_Run_RejectFillStatus(t *testing.T) {
	toolInput := json.RawMessage(`{}`)
	// First call: tool → reject status → compact → retry
	// Second call (retry): tool → reject again → error
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("trying", "tool1", toolInput),
			llmResponseWithToolCall("trying again", "tool1", json.RawMessage(`{"x":"y"}`)),
		},
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()
	cm.fillCheck = FillCheck{Percent: 99, Status: "reject", Used: 99000, Max: 100000}

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	_, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "tool1", Description: "t", Source: "core"},
	}, cm)
	if err == nil {
		t.Fatal("expected error on double reject")
	}
	if !strings.Contains(err.Error(), "context window full") {
		t.Errorf("expected 'context window full' error, got: %v", err)
	}
}

func TestExecutor_Run_SuppressAssistantEvents(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{llmResponseEndTurn("hello")},
	}
	cm := newMockContextManager()
	events := &recordingEvents{}

	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, events, true, ToolResultBudget{}, defaultCircuitBreakerConfig)
	_, err := exec.Run(context.Background(), nil, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, e := range events.events {
		if e == "AssistantChunk" || e == "AssistantDone" {
			t.Errorf("expected assistant events to be suppressed, found: %s", e)
		}
	}
}

func TestExecutor_Run_EmitsAssistantEvents(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{llmResponseEndTurn("hello")},
	}
	cm := newMockContextManager()
	events := &recordingEvents{}

	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, events, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	_, err := exec.Run(context.Background(), nil, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundChunk := false
	foundDone := false
	for _, e := range events.events {
		if e == "AssistantChunk" {
			foundChunk = true
		}
		if e == "AssistantDone" {
			foundDone = true
		}
	}
	if !foundChunk {
		t.Error("expected AssistantChunk event")
	}
	if !foundDone {
		t.Error("expected AssistantDone event")
	}
}

func TestExecutor_Run_FinishInTaskTools(t *testing.T) {
	// When finish tool is already in taskTools, it should not be added twice
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{llmResponseFinish("done", "42")},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	taskTools := []tools.ToolDescriptor{
		{Name: "finish", Description: "custom finish", Source: "core", InputSchema: json.RawMessage(`{}`)},
	}
	result, err := exec.Run(context.Background(), taskTools, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}

	// Verify finish tool wasn't duplicated in the LLM request
	if len(mockLLM.calls) > 0 {
		toolDefs := mockLLM.calls[0].Tools
		finishCount := 0
		for _, td := range toolDefs {
			if td.Name == "finish" {
				finishCount++
			}
		}
		if finishCount != 1 {
			t.Errorf("expected exactly 1 finish tool definition, got %d", finishCount)
		}
	}
}

func TestExecutor_Run_PlanContextLogging(t *testing.T) {
	toolInput := json.RawMessage(`{}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("step", "tool1", toolInput),
			llmResponseFinish("done", "ok"),
		},
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetPlanContext("step_2", 2, 5)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "tool1", Description: "t", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}
}

// --- isContextExceededError tests ---

func TestIsContextExceededError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"unrelated error", errors.New("something else"), false},
		{"context length exceeded", errors.New("context length exceeded"), true},
		{"maximum context length", errors.New("maximum context length reached"), true},
		{"context_length_exceeded", errors.New("error: context_length_exceeded"), true},
		{"too many tokens", errors.New("too many tokens"), true},
		{"request too large", errors.New("request too large for model"), true},
		{"input is too long", errors.New("input is too long"), true},
		{"prompt is too long", errors.New("Prompt is too long"), true},
		{"case insensitive", errors.New("CONTEXT LENGTH EXCEEDED"), true},
		{"sentinel ErrContextWindowExceeded", llm.ErrContextWindowExceeded, true},
		{"wrapped ErrContextWindowExceeded", fmt.Errorf("outer: %w", llm.ErrContextWindowExceeded), true},
		{"NewContextWindowError", llm.NewContextWindowError("test-model", 200000, 128000, 200000, 72000), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isContextExceededError(tt.err)
			if got != tt.expected {
				t.Errorf("isContextExceededError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

// --- applyToolResultBudget tests ---

func TestApplyToolResultBudget_NoBudget(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	cm := newMockContextManager()
	result := exec.applyToolResultBudget("hello world", cm, "some_tool", "")
	if result != "hello world" {
		t.Errorf("expected unchanged result, got %q", result)
	}
}

func TestApplyToolResultBudget_UnderBudget(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{
		HardCapTokens:   1000,
		MaxFillFraction: 0.5,
	}, defaultCircuitBreakerConfig)
	cm := newMockContextManager()
	cm.availableTokens = 10000

	result := exec.applyToolResultBudget("short result", cm, "some_tool", "")
	if result != "short result" {
		t.Errorf("expected unchanged result, got %q", result)
	}
}

func TestApplyToolResultBudget_Truncated(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{
		HardCapTokens:   100,
		MaxFillFraction: 0.5,
	}, defaultCircuitBreakerConfig)
	cm := newMockContextManager()
	cm.availableTokens = 200 // adaptive cap = 100

	longContent := strings.Repeat("x", 2000) // ~500 tokens at len/4
	result := exec.applyToolResultBudget(longContent, cm, "some_tool", "")
	if !strings.Contains(result, "OUTPUT TRUNCATED") {
		t.Error("expected truncation notice")
	}
	if len(result) >= len(longContent) {
		t.Error("expected result to be shorter than original")
	}
}

func TestApplyToolResultBudget_MinFloor(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{
		HardCapTokens:   10, // very small
		MaxFillFraction: 0.01,
	}, defaultCircuitBreakerConfig)
	cm := newMockContextManager()
	cm.availableTokens = 100 // adaptive cap = 1, but floor is 256

	// Content that would be under 256 tokens (floor) → no truncation
	shortContent := strings.Repeat("x", 500) // ~125 tokens
	result := exec.applyToolResultBudget(shortContent, cm, "some_tool", "")
	if strings.Contains(result, "OUTPUT TRUNCATED") {
		t.Error("floor should prevent truncation of small content")
	}
}

func TestApplyToolResultBudget_ReadFileHint(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{
		HardCapTokens:   100,
		MaxFillFraction: 0.5,
	}, defaultCircuitBreakerConfig)
	cm := newMockContextManager()
	cm.availableTokens = 200

	longContent := strings.Repeat("x", 2000)
	result := exec.applyToolResultBudget(longContent, cm, "read_file", "")
	if !strings.Contains(result, "OUTPUT TRUNCATED") {
		t.Error("expected truncation notice")
	}
	if !strings.Contains(result, "start_line/end_line") {
		t.Error("expected read_file hint about start_line/end_line")
	}
}

func TestApplyToolResultBudget_RipgrepHint(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{
		HardCapTokens:   100,
		MaxFillFraction: 0.5,
	}, defaultCircuitBreakerConfig)
	cm := newMockContextManager()
	cm.availableTokens = 200

	longContent := strings.Repeat("x", 2000)
	result := exec.applyToolResultBudget(longContent, cm, "ripgrep", "")
	if !strings.Contains(result, "OUTPUT TRUNCATED") {
		t.Error("expected truncation notice")
	}
	if !strings.Contains(result, "Narrow your search pattern") {
		t.Error("expected ripgrep hint about narrowing search pattern")
	}
}

func TestApplyToolResultBudget_GrepHint(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{
		HardCapTokens:   100,
		MaxFillFraction: 0.5,
	}, defaultCircuitBreakerConfig)
	cm := newMockContextManager()
	cm.availableTokens = 200

	longContent := strings.Repeat("x", 2000)
	result := exec.applyToolResultBudget(longContent, cm, "grep", "")
	if !strings.Contains(result, "OUTPUT TRUNCATED") {
		t.Error("expected truncation notice")
	}
	if !strings.Contains(result, "Narrow your search pattern") {
		t.Error("expected grep hint about narrowing search pattern")
	}
}

func TestApplyToolResultBudget_GlobHint(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{
		HardCapTokens:   100,
		MaxFillFraction: 0.5,
	}, defaultCircuitBreakerConfig)
	cm := newMockContextManager()
	cm.availableTokens = 200

	longContent := strings.Repeat("x", 2000)
	result := exec.applyToolResultBudget(longContent, cm, "glob", "")
	if !strings.Contains(result, "OUTPUT TRUNCATED") {
		t.Error("expected truncation notice")
	}
	if !strings.Contains(result, "more specific glob pattern") {
		t.Error("expected glob hint about more specific pattern")
	}
}

func TestApplyToolResultBudget_DefaultHint(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{
		HardCapTokens:   100,
		MaxFillFraction: 0.5,
	}, defaultCircuitBreakerConfig)
	cm := newMockContextManager()
	cm.availableTokens = 200

	longContent := strings.Repeat("x", 2000)
	result := exec.applyToolResultBudget(longContent, cm, "unknown_tool", "")
	if !strings.Contains(result, "OUTPUT TRUNCATED") {
		t.Error("expected truncation notice")
	}
	if !strings.Contains(result, "Break into smaller operations") {
		t.Error("expected default hint about breaking into smaller operations")
	}
}

func TestApplyToolResultBudget_WithHash(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{
		HardCapTokens:   100,
		MaxFillFraction: 0.5,
	}, defaultCircuitBreakerConfig)
	cm := newMockContextManager()
	cm.availableTokens = 200

	longContent := strings.Repeat("x", 2000)
	result := exec.applyToolResultBudget(longContent, cm, "read_file", "abc123hash")
	if !strings.Contains(result, "OUTPUT TRUNCATED") {
		t.Error("expected truncation notice")
	}
	if !strings.Contains(result, "Hash: abc123hash") {
		t.Error("expected cache hash in truncation message")
	}
	if !strings.Contains(result, "tool_result_read") {
		t.Error("expected tool_result_read reference in truncation message")
	}
}

// --- buildToolDefinitions tests ---

func TestBuildToolDefinitions_AddsFinish(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	defs := exec.buildToolDefinitions([]tools.ToolDescriptor{
		{Name: "search", Description: "search"},
	})

	// Should have search + finish
	if len(defs) != 2 {
		t.Errorf("expected 2 definitions, got %d", len(defs))
	}
	hasFinish := false
	for _, d := range defs {
		if d.Name == "finish" {
			hasFinish = true
		}
	}
	if !hasFinish {
		t.Error("expected finish tool to be added")
	}
}

func TestBuildToolDefinitions_NoDoubleFinish(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	defs := exec.buildToolDefinitions([]tools.ToolDescriptor{
		{Name: "finish", Description: "custom finish"},
	})

	finishCount := 0
	for _, d := range defs {
		if d.Name == "finish" {
			finishCount++
		}
	}
	if finishCount != 1 {
		t.Errorf("expected 1 finish tool, got %d", finishCount)
	}
}

func TestBuildToolDefinitions_EmptyInput(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	defs := exec.buildToolDefinitions(nil)
	if len(defs) != 1 {
		t.Errorf("expected 1 definition (finish only), got %d", len(defs))
	}
	if defs[0].Name != "finish" {
		t.Errorf("expected finish tool, got %q", defs[0].Name)
	}
}

func TestBuildToolDefinitions_DeduplicatesByName(t *testing.T) {
	// Guards against upstream sources of duplicate tool names (e.g. the
	// delegate tool's filterToolsByName). DeepSeek rejects requests whose
	// tool names are not unique with HTTP 400 "Tool names must be unique."
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	defs := exec.buildToolDefinitions([]tools.ToolDescriptor{
		{Name: "semantic_search", Description: "first"},
		{Name: "bash_exec", Description: "bash"},
		{Name: "semantic_search", Description: "second"},
		{Name: "tool_result_read", Description: "trr"},
		{Name: "bash_exec", Description: "dup bash"},
	})

	seen := make(map[string]int, len(defs))
	for _, d := range defs {
		seen[d.Name]++
	}
	if seen["semantic_search"] != 1 {
		t.Errorf("semantic_search should appear once, got %d", seen["semantic_search"])
	}
	if seen["bash_exec"] != 1 {
		t.Errorf("bash_exec should appear once, got %d", seen["bash_exec"])
	}
	if seen["tool_result_read"] != 1 {
		t.Errorf("tool_result_read should appear once, got %d", seen["tool_result_read"])
	}
	if seen["finish"] != 1 {
		t.Errorf("finish should be injected once, got %d", seen["finish"])
	}
	for _, d := range defs {
		if d.Name == "semantic_search" && d.Description == "second" {
			t.Error("dedup should keep the first occurrence, not the duplicate")
		}
	}
}

func TestExecutor_Run_CircuitBreaker_JSONNormalization(t *testing.T) {
	// Tool calls with semantically identical JSON but different whitespace
	// should be detected as identical by the circuit breaker.
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("try1", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseWithToolCall("try2", "search", json.RawMessage(`{"q": "same"}`)),
		llmResponseWithToolCall("try3", "search", json.RawMessage(`{ "q" : "same" }`)),
		llmResponseWithToolCall("try4", "search", json.RawMessage(`{  "q"  :  "same"  }`)),
		llmResponseWithToolCall("try5", "search", json.RawMessage(`{"q":"same"}`)), // extra, should not be reached
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Finished {
		t.Error("expected Finished=false on circuit breaker abort")
	}
	if !strings.Contains(result.Output, "Aborted") {
		t.Errorf("expected abort message in output, got %q", result.Output)
	}
}

func TestExecutor_Run_CircuitBreaker_ErrorAwareAbort(t *testing.T) {
	// When repeated identical calls produce errors (IsError=true),
	// the abort threshold should be 3 instead of 4.
	sameInput := json.RawMessage(`{"q":"same"}`)
	responses := make([]*llm.ChatResponse, 5)
	for i := range responses {
		responses[i] = llmResponseWithToolCall("trying", "search", sameInput)
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	// Return error results for the tool
	mockTools.results["search"] = tools.ToolResult{Content: "not found", IsError: true}
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Finished {
		t.Error("expected Finished=false on circuit breaker abort")
	}
	if !strings.Contains(result.Output, "Aborted") {
		t.Errorf("expected abort message in output, got %q", result.Output)
	}
	// With error-aware thresholds: call 1 executes (count=1), call 2 executes (count=2, nudge),
	// call 3 aborts (count=3). So only 2 tool executions should happen.
	toolCalls := 0
	for _, c := range mockTools.calls {
		if c.Name == "search" {
			toolCalls++
		}
	}
	// First call: count=1, executes. Second call: count=2, nudge (no execute).
	// Third call: count=3, abort (no execute). So 1 tool execution.
	if toolCalls != 1 {
		t.Errorf("expected 1 tool execution with error-aware abort (threshold=3), got %d", toolCalls)
	}
}

func TestExecutor_Run_CircuitBreaker_ErrorAwareNudge(t *testing.T) {
	// When repeated error calls occur, the nudge should happen at count 2
	// with the error-specific message.
	sameInput := json.RawMessage(`{"q":"same"}`)
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("try1", "search", sameInput),
		llmResponseWithToolCall("try2", "search", sameInput), // triggers nudge at count=2
		llmResponseFinish("ok", "done"),                      // after nudge
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: "not found", IsError: true}
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after error-aware nudge + finish")
	}

	// Verify the nudge step contains the error-specific message
	foundErrorNudge := false
	for _, s := range cm.steps {
		if strings.Contains(s.Observation, "returned an error") {
			foundErrorNudge = true
			break
		}
	}
	if !foundErrorNudge {
		t.Error("expected error-specific nudge message in steps")
	}
}

// --- Truncation protection tests ---

func TestExecutor_Run_TruncatedToolCall_SkipsExecution(t *testing.T) {
	// LLM returns a max_tokens response with a truncated tool call, then a normal finish.
	// The tool should NOT be executed, and a truncation system message should appear.
	truncatedInput := json.RawMessage(`{"content": "hello worl`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Let me write the file",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "write_file", Input: truncatedInput},
					},
				},
				StopReason: "max_tokens",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 4096},
			},
			llmResponseFinish("done", "completed"),
		},
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "write_file", Description: "write a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Tool should NOT have been executed
	for _, c := range mockTools.calls {
		if c.Name == "write_file" {
			t.Error("write_file tool should NOT have been executed on truncated response")
		}
	}

	// Executor should not abort (only 1 truncation, threshold is 3)
	if !result.Finished {
		t.Error("expected Finished=true (only 1 truncation, should not abort)")
	}

	// The truncation system message should appear in steps
	foundTruncMsg := false
	for _, s := range result.Steps {
		if strings.Contains(s.Observation, "was NOT executed") && strings.Contains(s.Observation, "write_file") {
			foundTruncMsg = true
			break
		}
	}
	if !foundTruncMsg {
		t.Error("expected truncation system message in step observation")
	}
}

func TestExecutor_Run_ConsecutiveTruncation_Aborts(t *testing.T) {
	// 3 consecutive max_tokens responses with tool calls → abort
	responses := make([]*llm.ChatResponse, 3)
	for i := range responses {
		responses[i] = &llm.ChatResponse{
			Message: llm.Message{
				Role:    "assistant",
				Content: fmt.Sprintf("attempt %d", i+1),
				ToolCalls: []llm.ToolCall{
					{ID: fmt.Sprintf("call_%d", i), Name: "write_file", Input: json.RawMessage(`{"content": "trunca`)},
				},
			},
			StopReason: "max_tokens",
			Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 4096},
		}
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "write_file", Description: "write a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Finished {
		t.Error("expected Finished=false on truncation abort")
	}
	if !strings.Contains(result.Output, "truncated") {
		t.Errorf("expected truncation abort message, got %q", result.Output)
	}
}

func TestExecutor_Run_ConsecutiveParseErrors_Aborts(t *testing.T) {
	// 3 tool calls to the same tool, each with invalid JSON that causes parse errors.
	// The tool executor returns IsError=true with "failed to parse input" content.
	responses := make([]*llm.ChatResponse, 3)
	badInputs := []string{
		`{"path": 123}`,
		`{"path": null, "extra": true}`,
		`{"wrong_field": "value"}`,
	}
	for i := range responses {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("attempt %d", i+1),
			"create_file",
			json.RawMessage(badInputs[i]),
		)
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["create_file"] = tools.ToolResult{
		Content: "failed to parse input: invalid field type",
		IsError: true,
	}
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "create_file", Description: "create a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Finished {
		t.Error("expected Finished=false on parse error abort")
	}
	if !strings.Contains(result.Output, "failed to parse input") {
		t.Errorf("expected parse error abort message, got %q", result.Output)
	}
}

func TestExecutor_Run_MaxTokens_SetFromOutputLimit(t *testing.T) {
	// Verify that ChatRequest.MaxTokens is set from cw.OutputLimit()
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseFinish("done", "42"),
		},
	}
	cm := newMockContextManager()
	// OutputLimit() returns 8192 by default in the mock

	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	_, err := exec.Run(context.Background(), nil, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mockLLM.calls) == 0 {
		t.Fatal("expected at least 1 LLM call")
	}
	if mockLLM.calls[0].MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d, want 8192", mockLLM.calls[0].MaxTokens)
	}
}

func TestExecutor_Run_TruncationCounterResets(t *testing.T) {
	// truncated → normal tool call (success) → truncated → normal finish
	// Counter should reset after the successful call, so no abort.
	responses := []*llm.ChatResponse{
		// 1st: truncated
		{
			Message: llm.Message{
				Role:    "assistant",
				Content: "try big write",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "write_file", Input: json.RawMessage(`{"content": "trunc`)},
				},
			},
			StopReason: "max_tokens",
			Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 4096},
		},
		// 2nd: normal tool call (not truncated)
		llmResponseWithToolCall("smaller write", "write_file", json.RawMessage(`{"content": "ok"}`)),
		// 3rd: truncated again
		{
			Message: llm.Message{
				Role:    "assistant",
				Content: "try big write again",
				ToolCalls: []llm.ToolCall{
					{ID: "call_3", Name: "write_file", Input: json.RawMessage(`{"content": "trunc2`)},
				},
			},
			StopReason: "max_tokens",
			Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 4096},
		},
		// 4th: finish
		llmResponseFinish("done", "all good"),
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["write_file"] = tools.ToolResult{Content: "written"}
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "write_file", Description: "write a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT abort — counter resets after successful call
	if !result.Finished {
		t.Error("expected Finished=true (counter should reset after successful call)")
	}
	if result.Output != "all good" {
		t.Errorf("Output = %q, want %q", result.Output, "all good")
	}
}

func TestExecutor_WrapUpNudge(t *testing.T) {
	// maxSteps=6, LLM always returns tool calls (never finishes).
	// Wrap-up nudge should fire at stepNum=3 (which is maxSteps-3=3).
	maxSteps := 6
	responses := make([]*llm.ChatResponse, maxSteps)
	for i := range responses {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("working step %d", i+1),
			fmt.Sprintf("tool_%d", i+1),
			json.RawMessage(fmt.Sprintf(`{"i":%d}`, i+1)),
		)
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, maxSteps, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "tool_1", Description: "t", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Finished {
		t.Error("expected Finished=false (max steps exhausted)")
	}

	// Find the wrap-up nudge step
	foundNudge := false
	for _, s := range result.Steps {
		if strings.Contains(s.UserNudge, "running low on tool call iterations") {
			foundNudge = true
			break
		}
	}
	if !foundNudge {
		t.Error("expected wrap-up nudge user nudge containing 'running low on tool call iterations'")
	}
}

func TestExecutor_WrapUpNudge_OnlyOnce(t *testing.T) {
	// maxSteps=8, LLM always returns tool calls (never finishes).
	// Nudge should fire once at stepNum=5 (maxSteps-3) and NOT repeat on steps 6, 7, 8.
	maxSteps := 8
	responses := make([]*llm.ChatResponse, maxSteps)
	for i := range responses {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("working step %d", i+1),
			fmt.Sprintf("tool_%d", i+1),
			json.RawMessage(fmt.Sprintf(`{"i":%d}`, i+1)),
		)
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, maxSteps, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "tool_1", Description: "t", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Count nudge occurrences
	nudgeCount := 0
	for _, s := range result.Steps {
		if strings.Contains(s.UserNudge, "running low on tool call iterations") {
			nudgeCount++
		}
	}
	if nudgeCount != 1 {
		t.Errorf("expected exactly 1 wrap-up nudge, got %d", nudgeCount)
	}
}

func TestExecutor_WrapUpNudge_ActiveProgress_UsesActiveNudge(t *testing.T) {
	// maxSteps=6, nudge fires at stepNum=3 (maxSteps-3). A successful write_file
	// in step 1 falls within the lookback window, so the continuation-oriented
	// "active progress" nudge should fire instead of the default wrap-up nudge.
	maxSteps := 6
	writeInput := json.RawMessage(`{"path": "/tmp/a.go", "content": "x"}`)
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("writing", "write_file", writeInput),
		llmResponseWithToolCall("reading 1", "read_file", json.RawMessage(`{"path": "/tmp/1"}`)),
		llmResponseWithToolCall("reading 2", "read_file", json.RawMessage(`{"path": "/tmp/2"}`)),
		llmResponseWithToolCall("reading 3", "read_file", json.RawMessage(`{"path": "/tmp/3"}`)),
		llmResponseWithToolCall("reading 4", "read_file", json.RawMessage(`{"path": "/tmp/4"}`)),
		llmResponseWithToolCall("reading 5", "read_file", json.RawMessage(`{"path": "/tmp/5"}`)),
		llmResponseWithToolCall("reading 6", "read_file", json.RawMessage(`{"path": "/tmp/6"}`)), // buffer for step-limit boundary
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["write_file"] = tools.ToolResult{Content: "file written"}
	mockTools.results["read_file"] = tools.ToolResult{Content: "content"}
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, maxSteps, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "write_file", Description: "write", Source: "core"},
		{Name: "read_file", Description: "read", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the wrap-up nudge and assert it is the active-progress variant.
	foundActive := false
	for _, s := range result.Steps {
		if strings.Contains(s.UserNudge, "running low on tool call iterations") {
			if !strings.Contains(s.UserNudge, "active progress") {
				t.Errorf("expected active-progress nudge, got: %s", s.UserNudge)
			}
			if !strings.Contains(s.UserNudge, "Continue completing the work in progress") {
				t.Errorf("active nudge should encourage continuation, got: %s", s.UserNudge)
			}
			foundActive = true
		}
	}
	if !foundActive {
		t.Error("expected wrap-up nudge user nudge containing 'running low on tool call iterations'")
	}
}

func TestExecutor_WrapUpNudge_NoMutation_UsesDefaultNudge(t *testing.T) {
	// maxSteps=6, only read-only tools. No recent mutation → default wrap-up
	// nudge, which still offers the alternative to continue working instead of
	// finishing, so the path to OnStepLimit is never excluded. Distinct tool
	// names/args per step avoid the same-tool-repeat circuit breaker.
	maxSteps := 6
	responses := make([]*llm.ChatResponse, 0, maxSteps+1)
	for i := 0; i < maxSteps+1; i++ {
		responses = append(responses, llmResponseWithToolCall(
			fmt.Sprintf("reading %d", i+1),
			fmt.Sprintf("search_%d", i+1),
			json.RawMessage(fmt.Sprintf(`{"q":"q%d"}`, i+1)),
		))
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, maxSteps, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundDefault := false
	for _, s := range result.Steps {
		if strings.Contains(s.UserNudge, "running low on tool call iterations") {
			if strings.Contains(s.UserNudge, "active progress") {
				t.Errorf("did not expect active-progress nudge for read-only task, got: %s", s.UserNudge)
			}
			// The default nudge should still offer the continue alternative so
			// the path to OnStepLimit is not excluded.
			if !strings.Contains(s.UserNudge, "you may continue working instead of finishing") {
				t.Errorf("default nudge should offer the continue alternative, got: %s", s.UserNudge)
			}
			foundDefault = true
		}
	}
	if !foundDefault {
		t.Error("expected wrap-up nudge user nudge containing 'running low on tool call iterations'")
	}
}

func TestExecutor_RecentSuccessfulMutation(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	// No steps at all.
	if exec.recentSuccessfulMutation(&runState{}, 5) {
		t.Error("expected false for empty steps")
	}

	// write_file succeeds, followed by two read-only calls.
	state := &runState{allSteps: []Step{
		{Action: llm.ToolCall{Name: "write_file"}},
		{Action: llm.ToolCall{Name: "read_file"}},
		{Action: llm.ToolCall{Name: "read_file"}},
	}}
	// lookback=3 reaches write_file → true.
	if !exec.recentSuccessfulMutation(state, 3) {
		t.Error("expected true: write_file within lookback=3")
	}
	// lookback=2 only sees the two read_file calls → false (write_file outside window).
	if exec.recentSuccessfulMutation(state, 2) {
		t.Error("expected false: write_file outside lookback=2")
	}

	// Failed write_file does not count.
	failed := &runState{allSteps: []Step{
		{Action: llm.ToolCall{Name: "write_file"}, IsError: true},
	}}
	if exec.recentSuccessfulMutation(failed, 5) {
		t.Error("expected false: failed mutation not counted")
	}

	// Rejected write_file does not count.
	rejected := &runState{allSteps: []Step{
		{Action: llm.ToolCall{Name: "write_file"}, Observation: "[Tool call rejected by HITL]"},
	}}
	if exec.recentSuccessfulMutation(rejected, 5) {
		t.Error("expected false: rejected mutation not counted")
	}

	// Nudge-only steps are skipped (do not consume the lookback window).
	withNudges := &runState{allSteps: []Step{
		{Action: llm.ToolCall{Name: "write_file"}},
		{UserNudge: "nudge 1"},
		{UserNudge: "nudge 2"},
	}}
	if !exec.recentSuccessfulMutation(withNudges, 1) {
		t.Error("expected true: nudge steps skipped, write_file is 1 real call back")
	}
}

// --- StepLimit tests ---

func TestStepLimit_NilCallback(t *testing.T) {
	// Create executor with maxSteps=2, NO HITLHandler set (uses NoopHITLHandler)
	// Mock LLM to always request a tool call
	toolInput := json.RawMessage(`{"q":"test"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("searching 1", "search", toolInput),
			llmResponseWithToolCall("searching 2", "search", toolInput),
			llmResponseWithToolCall("searching 3", "search", toolInput),
		},
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 2, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	// NoopHITLHandler (default) - testing silent exit at step limit

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assert: result.Finished == false (exhausted without prompting)
	if result.Finished {
		t.Error("expected Finished=false when max steps exhausted with nil callback")
	}

	// Assert: exactly 2 steps were executed
	if len(result.Steps) != 2 {
		t.Errorf("expected exactly 2 steps, got %d", len(result.Steps))
	}
}

func TestStepLimit_Deny(t *testing.T) {
	// Create executor with maxSteps=2
	// Set HITLHandler.OnStepLimit that returns StepLimitDeny
	// Mock LLM to always request a tool call
	toolInput := json.RawMessage(`{"q":"test"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("searching 1", "search", toolInput),
			llmResponseWithToolCall("searching 2", "search", toolInput),
			llmResponseWithToolCall("searching 3", "search", toolInput),
		},
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	callbackCallCount := 0
	var receivedCurrentStep, receivedMaxSteps int

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 2, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callbackCallCount++
		receivedCurrentStep = currentStep
		receivedMaxSteps = maxSteps
		return StepLimitDeny, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assert: result.Finished == false
	if result.Finished {
		t.Error("expected Finished=false when step limit callback returns deny")
	}

	// Assert: the callback was called exactly once
	if callbackCallCount != 1 {
		t.Errorf("expected callback to be called exactly once, got %d", callbackCallCount)
	}

	// Assert: callback received correct currentStep and maxSteps args
	// currentStep should be 3 (we're at step 3 when limit is reached with maxSteps=2)
	if receivedCurrentStep != 3 {
		t.Errorf("expected currentStep=3, got %d", receivedCurrentStep)
	}
	if receivedMaxSteps != 2 {
		t.Errorf("expected maxSteps=2, got %d", receivedMaxSteps)
	}
}

func TestStepLimit_AllowOnce(t *testing.T) {
	// Create executor with maxSteps=2
	// Set HITLHandler.OnStepLimit that returns StepLimitAllowOnce on first call, then StepLimitDeny on second
	// Mock LLM to always request a tool call
	toolInput := json.RawMessage(`{"q":"test"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("searching 1", "search", toolInput),
			llmResponseWithToolCall("searching 2", "search", toolInput),
			llmResponseWithToolCall("searching 3", "search", toolInput),
			llmResponseWithToolCall("searching 4", "search", toolInput),
		},
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	callbackCallCount := 0

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 2, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callbackCallCount++
		if callbackCallCount == 1 {
			return StepLimitAllowOnce, nil
		}
		return StepLimitDeny, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assert: the callback was called exactly twice (once at step 3, once at step 4)
	if callbackCallCount != 2 {
		t.Errorf("expected callback to be called exactly twice, got %d", callbackCallCount)
	}

	// Assert: result.Finished == false (denied on second call)
	if result.Finished {
		t.Error("expected Finished=false when step limit callback returns deny on second call")
	}

	// Assert: 3 total steps executed (2 original + 1 extension)
	// The steps include the 2 tool calls + 1 nudge step for allow_once
	toolCallSteps := 0
	for _, s := range result.Steps {
		if s.Action.Name != "" {
			toolCallSteps++
		}
	}
	if toolCallSteps != 3 {
		t.Errorf("expected 3 tool call steps, got %d", toolCallSteps)
	}

	// Verify the allow_once nudge was injected
	foundNudge := false
	for _, s := range result.Steps {
		if strings.Contains(s.UserNudge, "ONE additional tool call iteration") {
			foundNudge = true
			break
		}
	}
	if !foundNudge {
		t.Error("expected allow_once nudge to be injected")
	}
}

func TestStepLimit_AllowAlways(t *testing.T) {
	// Create executor with maxSteps=2
	// Set HITLHandler.OnStepLimit that returns StepLimitAllowAlways
	// Mock LLM to request tool calls for 5 iterations, then return a final text response (finish)
	// Use different tool inputs to avoid triggering the circuit breaker
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("searching 1", "search", json.RawMessage(`{"q":"test1"}`)),
			llmResponseWithToolCall("searching 2", "search", json.RawMessage(`{"q":"test2"}`)),
			llmResponseWithToolCall("searching 3", "search", json.RawMessage(`{"q":"test3"}`)),
			llmResponseWithToolCall("searching 4", "search", json.RawMessage(`{"q":"test4"}`)),
			llmResponseWithToolCall("searching 5", "search", json.RawMessage(`{"q":"test5"}`)),
			llmResponseEndTurn("final answer"),
		},
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	callbackCallCount := 0

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 2, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callbackCallCount++
		return StepLimitAllowAlways, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assert: the callback was called exactly once
	if callbackCallCount != 1 {
		t.Errorf("expected callback to be called exactly once, got %d", callbackCallCount)
	}

	// Assert: result.Finished == true
	if !result.Finished {
		t.Error("expected Finished=true when execution completes after allow_always")
	}

	// Assert: all steps executed without re-prompting (5 tool calls + 1 finish)
	// Plus 1 nudge step for allow_always
	toolCallSteps := 0
	for _, s := range result.Steps {
		if s.Action.Name != "" && s.Action.Name != "finish" {
			toolCallSteps++
		}
	}
	if toolCallSteps != 5 {
		t.Errorf("expected 5 tool call steps, got %d", toolCallSteps)
	}

	// Verify the allow_always nudge was injected
	foundNudge := false
	for _, s := range result.Steps {
		if strings.Contains(s.UserNudge, "unlimited tool call iterations") {
			foundNudge = true
			break
		}
	}
	if !foundNudge {
		t.Error("expected allow_always nudge to be injected")
	}
}

// --- Fruitless Result Detector tests ---

func TestExecutor_Run_FruitlessDetector_Nudge(t *testing.T) {
	// 5 consecutive tool calls returning minimal results (<= 32 chars) trigger a nudge.
	// Use custom config with lower thresholds.
	fruitlessConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      5,
		FruitlessAbortThreshold:      8,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}

	// 5 tool calls with minimal results, then finish
	responses := make([]*llm.ChatResponse, 6)
	for i := 0; i < 5; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("search %d", i+1),
			"search",
			json.RawMessage(fmt.Sprintf(`{"q":"test%d"}`, i+1)),
		)
	}
	responses[5] = llmResponseFinish("done", "completed")

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	// Return minimal results (empty string)
	mockTools.results["search"] = tools.ToolResult{Content: "", IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, fruitlessConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished=true after fruitless nudge + finish")
	}

	// Verify the nudge step appears
	foundNudge := false
	for _, s := range result.Steps {
		if strings.Contains(s.UserNudge, "tool calls returned empty or minimal results") {
			foundNudge = true
			break
		}
	}
	if !foundNudge {
		t.Error("expected fruitless nudge message in steps")
	}
}

func TestExecutor_Run_FruitlessDetector_Abort(t *testing.T) {
	// 8 consecutive minimal-result calls trigger abort.
	fruitlessConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      5,
		FruitlessAbortThreshold:      8,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}

	// 8 tool calls with minimal results (should abort at 8)
	responses := make([]*llm.ChatResponse, 10)
	for i := 0; i < 10; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("search %d", i+1),
			"search",
			json.RawMessage(fmt.Sprintf(`{"q":"test%d"}`, i+1)),
		)
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	// Return short results (within 32 char limit)
	mockTools.results["search"] = tools.ToolResult{Content: "not found", IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, fruitlessConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Finished {
		t.Error("expected Finished=false on fruitless abort")
	}
	if !strings.Contains(result.Output, "Aborted") {
		t.Errorf("expected abort message in output, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "empty or minimal results") {
		t.Errorf("expected fruitless abort message, got %q", result.Output)
	}
}

func TestExecutor_Run_FruitlessDetector_Reset(t *testing.T) {
	// After some minimal results, a substantial result (> 32 chars) resets the counter.
	fruitlessConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      5,
		FruitlessAbortThreshold:      8,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}

	// 3 minimal results -> 1 substantial result -> 4 more minimal results -> finish
	// Should NOT trigger nudge (counter resets at substantial result)
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("search 1", "search", json.RawMessage(`{"q":"test1"}`)),
		llmResponseWithToolCall("search 2", "search", json.RawMessage(`{"q":"test2"}`)),
		llmResponseWithToolCall("search 3", "search", json.RawMessage(`{"q":"test3"}`)),
		llmResponseWithToolCall("search 4", "search", json.RawMessage(`{"q":"test4"}`)),
		llmResponseWithToolCall("search 5", "search", json.RawMessage(`{"q":"test5"}`)),
		llmResponseWithToolCall("search 6", "search", json.RawMessage(`{"q":"test6"}`)),
		llmResponseWithToolCall("search 7", "search", json.RawMessage(`{"q":"test7"}`)),
		llmResponseFinish("done", "completed"),
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := &countingToolExecutor{
		results: map[int]tools.ToolResult{
			1: {Content: "no", IsError: false},                     // minimal
			2: {Content: "no", IsError: false},                     // minimal
			3: {Content: "no", IsError: false},                     // minimal
			4: {Content: strings.Repeat("x", 100), IsError: false}, // substantial - resets counter
			5: {Content: "no", IsError: false},                     // minimal
			6: {Content: "no", IsError: false},                     // minimal
			7: {Content: "no", IsError: false},                     // minimal
		},
	}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, fruitlessConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished=true (counter should have reset)")
	}

	// Should NOT have triggered nudge (only 4 consecutive minimal results after reset)
	for _, s := range result.Steps {
		if strings.Contains(s.Observation, "tool calls returned empty or minimal results") {
			t.Error("should NOT have fruitless nudge (counter reset at substantial result)")
			break
		}
	}
}

func TestExecutor_Run_FruitlessDetector_IgnoresErrors(t *testing.T) {
	// Error results (IsError=true) should NOT count toward the fruitless counter.
	fruitlessConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      5,
		FruitlessAbortThreshold:      8,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}

	// 6 error results -> 4 minimal non-error results -> finish
	// Should NOT trigger nudge (errors don't count, only 4 minimal results)
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("search 1", "search", json.RawMessage(`{"q":"test1"}`)),
		llmResponseWithToolCall("search 2", "search", json.RawMessage(`{"q":"test2"}`)),
		llmResponseWithToolCall("search 3", "search", json.RawMessage(`{"q":"test3"}`)),
		llmResponseWithToolCall("search 4", "search", json.RawMessage(`{"q":"test4"}`)),
		llmResponseWithToolCall("search 5", "search", json.RawMessage(`{"q":"test5"}`)),
		llmResponseWithToolCall("search 6", "search", json.RawMessage(`{"q":"test6"}`)),
		llmResponseWithToolCall("search 7", "search", json.RawMessage(`{"q":"test7"}`)),
		llmResponseWithToolCall("search 8", "search", json.RawMessage(`{"q":"test8"}`)),
		llmResponseWithToolCall("search 9", "search", json.RawMessage(`{"q":"test9"}`)),
		llmResponseWithToolCall("search 10", "search", json.RawMessage(`{"q":"test10"}`)),
		llmResponseFinish("done", "completed"),
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := &countingToolExecutor{
		results: map[int]tools.ToolResult{
			1:  {Content: "error", IsError: true},
			2:  {Content: "error", IsError: true},
			3:  {Content: "error", IsError: true},
			4:  {Content: "error", IsError: true},
			5:  {Content: "error", IsError: true},
			6:  {Content: "error", IsError: true},
			7:  {Content: "no", IsError: false}, // minimal non-error
			8:  {Content: "no", IsError: false}, // minimal non-error
			9:  {Content: "no", IsError: false}, // minimal non-error
			10: {Content: "no", IsError: false}, // minimal non-error
		},
	}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, fruitlessConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished=true (errors should not count toward fruitless)")
	}

	// Should NOT have triggered nudge (only 4 consecutive non-error minimal results)
	for _, s := range result.Steps {
		if strings.Contains(s.Observation, "tool calls returned empty or minimal results") {
			t.Error("should NOT have fruitless nudge (errors don't count)")
			break
		}
	}
}

// --- Same-Tool Repetition Detector tests ---

func TestExecutor_Run_SameToolRepeat_Nudge(t *testing.T) {
	// 8 calls to the same tool with different arguments but similar result sizes trigger nudge.
	sameToolConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 8,
		SameToolRepeatAbortThreshold: 12,
		SameToolResultSizeDelta:      64,
	}

	// 8 tool calls with different args but similar result sizes, then finish
	responses := make([]*llm.ChatResponse, 9)
	for i := 0; i < 8; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("search %d", i+1),
			"search",
			json.RawMessage(fmt.Sprintf(`{"q":"query%d"}`, i+1)),
		)
	}
	responses[8] = llmResponseFinish("done", "completed")

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	// Return results with similar sizes (all around 50 chars, within 64 delta)
	mockTools.results["search"] = tools.ToolResult{Content: strings.Repeat("x", 50), IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, sameToolConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished=true after same-tool nudge + finish")
	}

	// Verify the nudge step appears
	foundNudge := false
	for _, s := range result.Steps {
		if strings.Contains(s.UserNudge, "consistently similar results") {
			foundNudge = true
			break
		}
	}
	if !foundNudge {
		t.Error("expected same-tool repeat nudge message in steps")
	}
}

func TestExecutor_Run_SameToolRepeat_Abort(t *testing.T) {
	// 12 calls to the same tool with different arguments but similar result sizes trigger abort.
	sameToolConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 8,
		SameToolRepeatAbortThreshold: 12,
		SameToolResultSizeDelta:      64,
	}

	// 12+ tool calls with different args but similar result sizes
	responses := make([]*llm.ChatResponse, 15)
	for i := 0; i < 15; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("search %d", i+1),
			"search",
			json.RawMessage(fmt.Sprintf(`{"q":"query%d"}`, i+1)),
		)
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	// Return results with similar sizes
	mockTools.results["search"] = tools.ToolResult{Content: strings.Repeat("x", 50), IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, sameToolConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Finished {
		t.Error("expected Finished=false on same-tool repeat abort")
	}
	if !strings.Contains(result.Output, "Aborted") {
		t.Errorf("expected abort message in output, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "similar results") {
		t.Errorf("expected same-tool abort message, got %q", result.Output)
	}
}

func TestExecutor_Run_SameToolRepeat_ResetOnToolChange(t *testing.T) {
	// Switching to a different tool resets the counter.
	sameToolConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 8,
		SameToolRepeatAbortThreshold: 12,
		SameToolResultSizeDelta:      64,
	}

	// 5 calls to search -> 1 call to other_tool -> 5 more calls to search -> finish
	// Should NOT trigger nudge (counter resets when tool changes)
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("search 1", "search", json.RawMessage(`{"q":"test1"}`)),
		llmResponseWithToolCall("search 2", "search", json.RawMessage(`{"q":"test2"}`)),
		llmResponseWithToolCall("search 3", "search", json.RawMessage(`{"q":"test3"}`)),
		llmResponseWithToolCall("search 4", "search", json.RawMessage(`{"q":"test4"}`)),
		llmResponseWithToolCall("search 5", "search", json.RawMessage(`{"q":"test5"}`)),
		llmResponseWithToolCall("other", "other_tool", json.RawMessage(`{"x":"y"}`)),
		llmResponseWithToolCall("search 6", "search", json.RawMessage(`{"q":"test6"}`)),
		llmResponseWithToolCall("search 7", "search", json.RawMessage(`{"q":"test7"}`)),
		llmResponseWithToolCall("search 8", "search", json.RawMessage(`{"q":"test8"}`)),
		llmResponseWithToolCall("search 9", "search", json.RawMessage(`{"q":"test9"}`)),
		llmResponseWithToolCall("search 10", "search", json.RawMessage(`{"q":"test10"}`)),
		llmResponseFinish("done", "completed"),
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: strings.Repeat("x", 50), IsError: false}
	mockTools.results["other_tool"] = tools.ToolResult{Content: strings.Repeat("y", 50), IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, sameToolConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
		{Name: "other_tool", Description: "other", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished=true (counter should reset on tool change)")
	}

	// Should NOT have triggered nudge
	for _, s := range result.Steps {
		if strings.Contains(s.Observation, "consistently similar results") {
			t.Error("should NOT have same-tool repeat nudge (counter reset on tool change)")
			break
		}
	}
}

func TestExecutor_Run_SameToolRepeat_ResetOnSizeChange(t *testing.T) {
	// A result with significantly different size (> SameToolResultSizeDelta) resets the counter.
	sameToolConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 8,
		SameToolRepeatAbortThreshold: 12,
		SameToolResultSizeDelta:      64,
	}

	// 5 calls with ~50 char results -> 1 call with 200 char result -> 5 more calls with ~50 char results -> finish
	// Should NOT trigger nudge (counter resets when size differs significantly)
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("search 1", "search", json.RawMessage(`{"q":"test1"}`)),
		llmResponseWithToolCall("search 2", "search", json.RawMessage(`{"q":"test2"}`)),
		llmResponseWithToolCall("search 3", "search", json.RawMessage(`{"q":"test3"}`)),
		llmResponseWithToolCall("search 4", "search", json.RawMessage(`{"q":"test4"}`)),
		llmResponseWithToolCall("search 5", "search", json.RawMessage(`{"q":"test5"}`)),
		llmResponseWithToolCall("search 6", "search", json.RawMessage(`{"q":"test6"}`)),
		llmResponseWithToolCall("search 7", "search", json.RawMessage(`{"q":"test7"}`)),
		llmResponseWithToolCall("search 8", "search", json.RawMessage(`{"q":"test8"}`)),
		llmResponseWithToolCall("search 9", "search", json.RawMessage(`{"q":"test9"}`)),
		llmResponseWithToolCall("search 10", "search", json.RawMessage(`{"q":"test10"}`)),
		llmResponseWithToolCall("search 11", "search", json.RawMessage(`{"q":"test11"}`)),
		llmResponseFinish("done", "completed"),
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := &countingToolExecutor{
		results: map[int]tools.ToolResult{
			1:  {Content: strings.Repeat("x", 50), IsError: false},  // ~50 chars
			2:  {Content: strings.Repeat("x", 50), IsError: false},  // ~50 chars
			3:  {Content: strings.Repeat("x", 50), IsError: false},  // ~50 chars
			4:  {Content: strings.Repeat("x", 50), IsError: false},  // ~50 chars
			5:  {Content: strings.Repeat("x", 50), IsError: false},  // ~50 chars
			6:  {Content: strings.Repeat("x", 200), IsError: false}, // 200 chars - resets counter (delta > 64)
			7:  {Content: strings.Repeat("x", 50), IsError: false},  // ~50 chars
			8:  {Content: strings.Repeat("x", 50), IsError: false},  // ~50 chars
			9:  {Content: strings.Repeat("x", 50), IsError: false},  // ~50 chars
			10: {Content: strings.Repeat("x", 50), IsError: false},  // ~50 chars
			11: {Content: strings.Repeat("x", 50), IsError: false},  // ~50 chars
		},
	}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, sameToolConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished=true (counter should reset on size change)")
	}

	// Should NOT have triggered nudge
	for _, s := range result.Steps {
		if strings.Contains(s.Observation, "consistently similar results") {
			t.Error("should NOT have same-tool repeat nudge (counter reset on size change)")
			break
		}
	}
}

func TestExecutor_Run_SameToolRepeat_StoreFactExcluded(t *testing.T) {
	// store_fact should NOT trigger the same-tool repetition detector even when
	// called many times in a row with similar-sized confirmation results.
	sameToolConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 3,
		SameToolRepeatAbortThreshold: 5,
		SameToolResultSizeDelta:      64,
	}

	// 6 calls to store_fact (would normally trigger abort) followed by finish
	responses := make([]*llm.ChatResponse, 7)
	for i := 0; i < 6; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("store fact %d", i+1),
			"store_fact",
			json.RawMessage(fmt.Sprintf(`{"fact":"fact %d"}`, i+1)),
		)
	}
	responses[6] = llmResponseFinish("done", "completed")

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	// store_fact returns similar-sized confirmations
	mockTools.results["store_fact"] = tools.ToolResult{Content: "Fact stored.", IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, sameToolConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "store_fact", Description: "store a fact", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished=true (store_fact should not trigger same-tool abort)")
	}

	// Should NOT have triggered nudge or abort
	for _, s := range result.Steps {
		if strings.Contains(s.Observation, "consistently similar results") || strings.Contains(s.Observation, "Aborted") {
			t.Error("store_fact should NOT trigger same-tool repeat nudge or abort")
			break
		}
	}
}

// --- Multi-Tool-Call tests ---

func TestExecutor_Run_MultiToolCall_AllExecuted(t *testing.T) {
	// LLM returns 3 tool calls in a single response, then finishes.
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithMultipleToolCalls("I'll read all three files", []llm.ToolCall{
				{ID: "call_a", Name: "read_file", Input: json.RawMessage(`{"path":"/a"}`)},
				{ID: "call_b", Name: "read_file", Input: json.RawMessage(`{"path":"/b"}`)},
				{ID: "call_c", Name: "read_file", Input: json.RawMessage(`{"path":"/c"}`)},
			}),
			llmResponseFinish("done", "all read"),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["read_file"] = tools.ToolResult{Content: "file content"}
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "read_file", Description: "read a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}

	// All 3 tool calls should be executed
	toolCalls := 0
	for _, c := range mockTools.calls {
		if c.Name == "read_file" {
			toolCalls++
		}
	}
	if toolCalls != 3 {
		t.Errorf("expected 3 tool executions, got %d", toolCalls)
	}

	// 3 steps from the multi-call + 1 finish step = 4
	if len(result.Steps) != 4 {
		t.Errorf("expected 4 steps, got %d", len(result.Steps))
	}

	// Verify ResponseGroup is set on the first 3 steps
	if result.Steps[0].ResponseGroup == 0 {
		t.Error("expected non-zero ResponseGroup for multi-call steps")
	}
	group := result.Steps[0].ResponseGroup
	for i := 0; i < 3; i++ {
		if result.Steps[i].ResponseGroup != group {
			t.Errorf("step %d ResponseGroup = %d, want %d", i, result.Steps[i].ResponseGroup, group)
		}
	}

	// Only first step should carry the thought
	if result.Steps[0].Thought != "I'll read all three files" {
		t.Errorf("first step Thought = %q", result.Steps[0].Thought)
	}
	for i := 1; i < 3; i++ {
		if result.Steps[i].Thought != "" {
			t.Errorf("step %d should have empty Thought, got %q", i, result.Steps[i].Thought)
		}
	}
}

func TestExecutor_Run_MultiToolCall_SingleCallStandalone(t *testing.T) {
	// Single tool call should have ResponseGroup == 0 (standalone step)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("reading", "read_file", json.RawMessage(`{"path":"/a"}`)),
			llmResponseFinish("done", "ok"),
		},
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "read_file", Description: "read a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}

	// Single-call steps should have ResponseGroup == 0
	if result.Steps[0].ResponseGroup != 0 {
		t.Errorf("expected ResponseGroup=0 for single-call, got %d", result.Steps[0].ResponseGroup)
	}
}

func TestExecutor_Run_MultiToolCall_FinishInGroup(t *testing.T) {
	// LLM returns 2 tool calls where the second is finish. The first should execute, finish should be handled.
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithMultipleToolCalls("reading and finishing", []llm.ToolCall{
				{ID: "call_a", Name: "read_file", Input: json.RawMessage(`{"path":"/a"}`)},
				{ID: "call_finish", Name: "finish", Input: json.RawMessage(`{"answer":"done"}`)},
			}),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["read_file"] = tools.ToolResult{Content: "content"}
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "read_file", Description: "read a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}

	// read_file should have been executed
	executed := false
	for _, c := range mockTools.calls {
		if c.Name == "read_file" {
			executed = true
		}
	}
	if !executed {
		t.Error("expected read_file to be executed before finish")
	}
}

func TestExecutor_Run_MultiToolCall_EmptyResultHandled(t *testing.T) {
	// Multi-tool call with empty results should get "(no output)" replacement
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithMultipleToolCalls("running tools", []llm.ToolCall{
				{ID: "call_a", Name: "tool1", Input: json.RawMessage(`{}`)},
				{ID: "call_b", Name: "tool2", Input: json.RawMessage(`{}`)},
			}),
			llmResponseFinish("done", "ok"),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["tool1"] = tools.ToolResult{Content: ""}
	mockTools.results["tool2"] = tools.ToolResult{Content: "result2"}
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "tool1", Description: "t", Source: "core"},
		{Name: "tool2", Description: "t", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}

	// Step 0 (tool1) should have "(no output)" observation
	if result.Steps[0].Observation != "(no output)" {
		t.Errorf("expected '(no output)' for empty tool result, got %q", result.Steps[0].Observation)
	}
	// Step 1 (tool2) should have actual result
	if result.Steps[1].Observation != "result2" {
		t.Errorf("expected 'result2', got %q", result.Steps[1].Observation)
	}
}

// --- compactJSON tests ---

func TestCompactJSON_ValidJSON(t *testing.T) {
	input := json.RawMessage(`{"a":  1,  "b":  [2,  3]}`)
	result := compactJSON(input)
	if strings.Contains(result, "  ") {
		t.Error("compactJSON should remove extra whitespace")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(result), &m); err != nil {
		t.Errorf("compactJSON result should be valid JSON: %v", err)
	}
}

func TestCompactJSON_InvalidJSON(t *testing.T) {
	input := json.RawMessage(`not json`)
	result := compactJSON(input)
	if result != "not json" {
		t.Errorf("compactJSON with invalid JSON = %q, want raw fallback", result)
	}
}

func TestCompactJSON_EmptyObject(t *testing.T) {
	input := json.RawMessage(`{}`)
	result := compactJSON(input)
	if result != "{}" {
		t.Errorf("compactJSON({}) = %q, want {}", result)
	}
}

func TestCompactJSON_Empty(t *testing.T) {
	input := json.RawMessage(``)
	result := compactJSON(input)
	if result != "" {
		t.Errorf("compactJSON(empty) = %q, want empty", result)
	}
}

// --- formatFragmentationNudge test ---

func TestFormatFragmentationNudge(t *testing.T) {
	result := formatFragmentationNudge("abc123hash", "read_file", 100)
	if result == "" {
		t.Fatal("formatFragmentationNudge returned empty string")
	}
	if !strings.Contains(result, "truncated to 100 lines") {
		t.Error("expected 'truncated to 100 lines' in nudge")
	}
	if !strings.Contains(result, "read_file") {
		t.Error("expected tool name in nudge")
	}
	if !strings.Contains(result, "abc123hash") {
		t.Error("expected hash in nudge")
	}
	if !strings.Contains(result, "tool_result_read") {
		t.Error("expected tool_result_read reference in nudge")
	}
}

func TestFormatFragmentationNudge_ByteOnly(t *testing.T) {
	result := formatFragmentationNudge("abc123hash", "web_fetch", 0)
	if result == "" {
		t.Fatal("formatFragmentationNudge with maxSliceHint=0 returned empty string")
	}
	if strings.Contains(result, "truncated to 0 lines") {
		t.Error("byte-only truncation must not say '0 lines'")
	}
	if !strings.Contains(result, "byte limit") {
		t.Error("expected 'byte limit' in byte-only nudge")
	}
	if !strings.Contains(result, "abc123hash") {
		t.Error("expected hash in nudge")
	}
	if !strings.Contains(result, "tool_result_read") {
		t.Error("expected tool_result_read reference in nudge")
	}
}

// --- formatPreCompactionNudge tests ---

func TestFormatPreCompactionNudge(t *testing.T) {
	vulnerable := []VulnerableOutput{
		{ToolName: "read_file", InputHint: "/path/to/file.go"},
		{ToolName: "ripgrep", InputHint: "pattern"},
		{ToolName: "glob", InputHint: ""},
	}
	result := formatPreCompactionNudge(75.5, vulnerable)

	if result == "" {
		t.Fatal("formatPreCompactionNudge returned empty string")
	}
	if !strings.Contains(result, "CONTEXT PRESSURE WARNING") {
		t.Error("expected warning header in nudge")
	}
	if !strings.Contains(result, "read_file") {
		t.Error("expected read_file in nudge")
	}
	if !strings.Contains(result, "/path/to/file.go") {
		t.Error("expected file hint in nudge")
	}
	if !strings.Contains(result, "ripgrep") {
		t.Error("expected ripgrep in nudge")
	}
	if !strings.Contains(result, "glob") {
		t.Error("expected glob in nudge")
	}
	if !strings.Contains(result, "store_fact") {
		t.Error("expected store_fact reference in nudge")
	}
}

func TestFormatPreCompactionNudge_Empty(t *testing.T) {
	result := formatPreCompactionNudge(0.0, nil)
	if result == "" {
		t.Fatal("formatPreCompactionNudge with empty input returned empty string")
	}
	if !strings.Contains(result, "CONTEXT PRESSURE WARNING") {
		t.Error("expected warning header even with empty vulnerable list")
	}
}

// --- isPathWithinWorkspace tests ---

func TestIsPathWithinWorkspace_Positive(t *testing.T) {
	if !isPathWithinWorkspace("/home/user/project/src/main.go", "/home/user/project") {
		t.Error("path within workspace should return true")
	}
	if !isPathWithinWorkspace("/home/user/project", "/home/user/project") {
		t.Error("exact workspace root should return true")
	}
	if !isPathWithinWorkspace("/home/user/project/sub/deep/file.txt", "/home/user/project") {
		t.Error("deep path within workspace should return true")
	}
}

func TestIsPathWithinWorkspace_Negative(t *testing.T) {
	if isPathWithinWorkspace("/etc/passwd", "/home/user/project") {
		t.Error("path outside workspace should return false")
	}
	if isPathWithinWorkspace("/home/user/other", "/home/user/project") {
		t.Error("sibling directory should return false")
	}
	if isPathWithinWorkspace("/home/user/projectsuffix", "/home/user/project") {
		t.Error("path with shared prefix but not within workspace should return false")
	}
}

func TestIsPathWithinWorkspace_DirtyPaths(t *testing.T) {
	if !isPathWithinWorkspace("/home/user/project/src/../lib/main.go", "/home/user/project") {
		t.Error("cleaned path within workspace should return true")
	}
	if isPathWithinWorkspace("/home/user/project/../../etc/passwd", "/home/user/project") {
		t.Error("cleaned path escaping workspace should return false")
	}
}

// TestCheckRepeatIdenticalTool_ZeroThresholds verifies that zero-valued
// RepeatNudgeThreshold and RepeatAbortThreshold do not cause integer underflow
// when the previous tool call produced an error.
func TestCheckRepeatIdenticalTool_ZeroThresholds(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:     0,
		RepeatAbortThreshold:     0,
		TruncationAbortThreshold: 3,
		ParseErrorAbortThreshold: 3,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)

	// Simulate two consecutive identical tool calls where the first produced an error.
	action := llm.ToolCall{Name: "bad_tool", Input: json.RawMessage(`{"x":1}`)}
	ctx := context.Background()
	cw := newMockContextManager()

	// First call: set up as if previous call was the same and produced an error.
	exec.lastToolKey = "bad_tool:" + compactJSON(action.Input)
	exec.lastToolResultIsError = true
	exec.consecutiveRepeatCount = 2

	loopAct, result, err := exec.checkRepeatIdenticalTool(ctx, action, 1, "thought", nil, &runState{}, cw)
	// Should not panic. With zero thresholds, the count (2) >= 0 (abortThreshold),
	// so the circuit breaker should abort.
	if err != nil {
		t.Logf("checkRepeatIdenticalTool returned error (expected abort): %v", err)
	}
	_ = loopAct
	_ = result
}
