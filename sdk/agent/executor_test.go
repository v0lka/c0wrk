package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/tools"
)

// --- NewExecutor tests ---

func TestNewExecutor_NilEmitter(t *testing.T) {
	exec := NewExecutor(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
	if exec.emitter == nil {
		t.Fatal("emitter should not be nil when nil is passed")
	}
	if _, ok := exec.emitter.(*NoopEvents); !ok {
		t.Errorf("emitter should be *NoopEvents, got %T", exec.emitter)
	}
}

func TestSetPlanContext(t *testing.T) {
	exec := NewExecutor(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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
	exec := NewExecutor(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})

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
	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})

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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 3, nil, nil, false, ToolResultBudget{})
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

func TestExecutor_Run_NudgeOnImplicitFinish(t *testing.T) {
	// First call: end_turn without tools → nudge
	// Second call: end_turn again → accept
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseEndTurn("I think I know"),
			llmResponseEndTurn("The answer is yes"),
		},
	}
	cm := newMockContextManager()
	exec := NewExecutor(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after nudge")
	}
	if result.Output != "The answer is yes" {
		t.Errorf("Output = %q, want %q", result.Output, "The answer is yes")
	}
	// Should have nudge step + final step
	if len(result.Steps) < 2 {
		t.Errorf("expected at least 2 steps (nudge + final), got %d", len(result.Steps))
	}
}

func TestExecutor_Run_NudgeOnNoToolsNoEndTurn(t *testing.T) {
	// No tool calls, stop_reason != "end_turn" → nudge, then finish
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message:    llm.Message{Role: "assistant", Content: "hmm"},
				StopReason: "max_tokens",
				Usage:      llm.TokenUsage{InputTokens: 50, OutputTokens: 50},
			},
			llmResponseEndTurn("final answer"),
		},
	}
	cm := newMockContextManager()
	exec := NewExecutor(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})

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
	exec := NewExecutor(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})

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
	exec := NewExecutor(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})

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
	exec := NewExecutor(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})

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
	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})

	_, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "broken_tool", Description: "broken", Source: "core"},
	}, cm)
	if err == nil {
		t.Fatal("expected tool execution error")
	}
}

func TestExecutor_Run_CircuitBreaker_Abort(t *testing.T) {
	// Same tool call repeated ≥ repeatAbortThreshold (4) times
	sameInput := json.RawMessage(`{"q":"same"}`)
	responses := make([]*llm.ChatResponse, repeatAbortThreshold+1)
	for i := range responses {
		responses[i] = llmResponseWithToolCall("trying", "search", sameInput)
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, nil, false, ToolResultBudget{})
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
		llmResponseFinish("ok", "done"),                       // after nudge
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, nil, false, ToolResultBudget{})
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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{
		HardCapTokens:   500,
		MaxFillFraction: 0.3,
	})

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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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
	exec := NewExecutor(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})

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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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

	exec := NewExecutor(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, events, true, ToolResultBudget{})
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

	exec := NewExecutor(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, events, false, ToolResultBudget{})
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
	exec := NewExecutor(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})

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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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
	exec := NewExecutor(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
	cm := newMockContextManager()
	result := exec.applyToolResultBudget("hello world", cm)
	if result != "hello world" {
		t.Errorf("expected unchanged result, got %q", result)
	}
}

func TestApplyToolResultBudget_UnderBudget(t *testing.T) {
	exec := NewExecutor(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{
		HardCapTokens:   1000,
		MaxFillFraction: 0.5,
	})
	cm := newMockContextManager()
	cm.availableTokens = 10000

	result := exec.applyToolResultBudget("short result", cm)
	if result != "short result" {
		t.Errorf("expected unchanged result, got %q", result)
	}
}

func TestApplyToolResultBudget_Truncated(t *testing.T) {
	exec := NewExecutor(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{
		HardCapTokens:   100,
		MaxFillFraction: 0.5,
	})
	cm := newMockContextManager()
	cm.availableTokens = 200 // adaptive cap = 100

	longContent := strings.Repeat("x", 2000) // ~500 tokens at len/4
	result := exec.applyToolResultBudget(longContent, cm)
	if !strings.Contains(result, "OUTPUT TRUNCATED") {
		t.Error("expected truncation notice")
	}
	if len(result) >= len(longContent) {
		t.Error("expected result to be shorter than original")
	}
}

func TestApplyToolResultBudget_MinFloor(t *testing.T) {
	exec := NewExecutor(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{
		HardCapTokens:   10, // very small
		MaxFillFraction: 0.01,
	})
	cm := newMockContextManager()
	cm.availableTokens = 100 // adaptive cap = 1, but floor is 256

	// Content that would be under 256 tokens (floor) → no truncation
	shortContent := strings.Repeat("x", 500) // ~125 tokens
	result := exec.applyToolResultBudget(shortContent, cm)
	if strings.Contains(result, "OUTPUT TRUNCATED") {
		t.Error("floor should prevent truncation of small content")
	}
}

// --- buildToolDefinitions tests ---

func TestBuildToolDefinitions_AddsFinish(t *testing.T) {
	exec := NewExecutor(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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
	exec := NewExecutor(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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
	exec := NewExecutor(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
	defs := exec.buildToolDefinitions(nil)
	if len(defs) != 1 {
		t.Errorf("expected 1 definition (finish only), got %d", len(defs))
	}
	if defs[0].Name != "finish" {
		t.Errorf("expected finish tool, got %q", defs[0].Name)
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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, nil, false, ToolResultBudget{})
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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, nil, false, ToolResultBudget{})
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
		llmResponseFinish("ok", "done"),                       // after nudge
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: "not found", IsError: true}
	cm := newMockContextManager()

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, nil, false, ToolResultBudget{})
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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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

	exec := NewExecutor(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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

	exec := NewExecutor(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, nil, false, ToolResultBudget{})
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
