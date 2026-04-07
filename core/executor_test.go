package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	tools "github.com/user/agent/sdk/tools"
)

// Tests use shared mock types from testhelpers_test.go:
// - mockLLMCaller: implements LLMCaller
// - mockToolExecutor: implements ToolExecutor
// - mockContextManager: implements ContextManager

func TestExecutor_BasicReActFlow(t *testing.T) {
	// Test: LLM returns a tool call on first call, then a finish call on second.
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "I need to search for the answer",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "search", Input: json.RawMessage(`{"query":"test"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Found the answer",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "finish", Input: json.RawMessage(`{"answer":"The final answer"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 150, OutputTokens: 60},
			},
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"search": {Content: "search result: found it", IsError: false},
		},
	}

	mockCW := &mockContextManager{
		systemPrompt:   "You are a helpful assistant",
		taskDefinition: "Find the answer",
	}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Find the answer",
		Tools: []tools.ToolDescriptor{
			{Name: "search", Description: "Search tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify two steps recorded
	if len(result.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(result.Steps))
	}

	// Verify output matches finish answer
	if result.Output != "The final answer" {
		t.Errorf("expected output 'The final answer', got '%s'", result.Output)
	}

	// Verify Finished = true
	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	// Verify the search tool was called
	if len(mockTools.calls) != 1 || mockTools.calls[0] != "search" {
		t.Errorf("expected search tool to be called once, got %v", mockTools.calls)
	}

	// Verify step was added to context window
	if len(mockCW.steps) != 1 {
		t.Errorf("expected 1 step added to context window, got %d", len(mockCW.steps))
	}
}

func TestExecutor_DirectFinish(t *testing.T) {
	// Test: LLM returns finish tool on first call.
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "I know the answer immediately",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "finish", Input: json.RawMessage(`{"answer":"Direct answer"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 50, OutputTokens: 30},
			},
		},
	}

	mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{Task: "Simple question"}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify single step
	if len(result.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(result.Steps))
	}

	// Verify Finished = true
	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	// Verify output
	if result.Output != "Direct answer" {
		t.Errorf("expected output 'Direct answer', got '%s'", result.Output)
	}

	// Verify no tools were executed (finish is handled specially)
	if len(mockTools.calls) != 0 {
		t.Errorf("expected no tool calls, got %v", mockTools.calls)
	}
}

func TestExecutor_MaxStepsReached(t *testing.T) {
	// Test: LLM always returns a tool call (never finish). Set maxSteps=3.
	// Each call uses distinct input to avoid triggering the circuit breaker.
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Step 1 thought",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "action", Input: json.RawMessage(`{"n":1}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 50, OutputTokens: 30},
			},
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Step 2 thought",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "action", Input: json.RawMessage(`{"n":2}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 60, OutputTokens: 35},
			},
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Step 3 thought",
					ToolCalls: []llm.ToolCall{
						{ID: "call_3", Name: "action", Input: json.RawMessage(`{"n":3}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 70, OutputTokens: 40},
			},
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"action": {Content: "action result", IsError: false},
		},
	}
	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 3, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Never-ending task",
		Tools: []tools.ToolDescriptor{
			{Name: "action", Description: "An action tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify Finished = false
	if result.Finished {
		t.Error("expected Finished to be false after max steps")
	}

	// Verify 3 steps were recorded
	if len(result.Steps) != 3 {
		t.Errorf("expected 3 steps, got %d", len(result.Steps))
	}

	// Verify output is empty
	if result.Output != "" {
		t.Errorf("expected empty output, got '%s'", result.Output)
	}

	// Verify action tool was called 3 times
	if len(mockTools.calls) != 3 {
		t.Errorf("expected 3 tool calls, got %d", len(mockTools.calls))
	}
}

func TestExecutor_ImplicitFinish(t *testing.T) {
	// Test: LLM returns response with no tool calls (just content). Implicit finish.
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:      "assistant",
					Content:   "Here is the answer without any tools",
					ToolCalls: nil,
				},
				StopReason: "end_turn",
				Usage:      llm.TokenUsage{InputTokens: 40, OutputTokens: 25},
			},
		},
	}

	mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{Task: "Simple question"}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify Output = content
	if result.Output != "Here is the answer without any tools" {
		t.Errorf("expected output 'Here is the answer without any tools', got '%s'", result.Output)
	}

	// Verify Finished = true
	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	// Verify single step recorded
	if len(result.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(result.Steps))
	}

	// Verify no tools were called
	if len(mockTools.calls) != 0 {
		t.Errorf("expected no tool calls, got %v", mockTools.calls)
	}
}

func TestExecutor_CompactionTriggered(t *testing.T) {
	// Test that compaction is triggered when NeedsCompaction returns true
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Using a tool",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "sometool", Input: json.RawMessage(`{}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Done",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "finish", Input: json.RawMessage(`{"answer":"compaction test"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"sometool": {Content: "result", IsError: false},
		},
	}
	mockCW := &mockContextManager{needsCompaction: true}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Test compaction",
		Tools: []tools.ToolDescriptor{
			{Name: "sometool", Description: "Some tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify compaction was called
	if !mockCW.compactCalled {
		t.Error("expected Compact to be called")
	}

	// Verify task completed
	if !result.Finished {
		t.Error("expected Finished to be true")
	}
}

func TestExecutor_ToolDefinitionsIncludeFinish(t *testing.T) {
	// Test that tool definitions always include finish tool
	// Note: The nudge mechanism may cause 2 calls when LLM returns no tool calls on step 1
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message:    llm.Message{Role: "assistant", Content: "done"},
				StopReason: "end_turn",
			},
			{
				Message:    llm.Message{Role: "assistant", Content: "done after nudge"},
				StopReason: "end_turn",
			},
		},
	}

	mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Test",
		Tools: []tools.ToolDescriptor{
			{Name: "mytool", Description: "My tool"},
		},
	}

	_, _ = executor.Run(context.Background(), task.Tools, mockCW)

	// Check that at least one request was made and includes both mytool and finish
	if len(mockLLM.calls) < 1 {
		t.Fatalf("expected at least 1 LLM call, got %d", len(mockLLM.calls))
	}

	req := mockLLM.calls[0]
	if len(req.Tools) != 2 {
		t.Errorf("expected 2 tools (mytool + finish), got %d", len(req.Tools))
	}

	hasMyTool := false
	hasFinish := false
	for _, tool := range req.Tools {
		if tool.Name == "mytool" {
			hasMyTool = true
		}
		if tool.Name == "finish" {
			hasFinish = true
		}
	}

	if !hasMyTool {
		t.Error("expected mytool in tool definitions")
	}
	if !hasFinish {
		t.Error("expected finish in tool definitions")
	}
}

// TestExecutor_NoDuplicateFinishTool tests that finish tool is not duplicated
// when it's already included in the task tools (e.g., from tool registry).
func TestExecutor_NoDuplicateFinishTool(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "I know the answer",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "finish", Input: json.RawMessage(`{"answer":"Done"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 50, OutputTokens: 30},
			},
		},
	}

	mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	// Task includes finish tool (as would happen when toolRegistry.List() includes it)
	task := TaskDefinition{
		Task: "Test",
		Tools: []tools.ToolDescriptor{
			{Name: "mytool", Description: "My tool"},
			{Name: "finish", Description: "Finish tool"},
		},
	}

	_, _ = executor.Run(context.Background(), task.Tools, mockCW)

	// Check that finish tool appears exactly once (not duplicated)
	if len(mockLLM.calls) < 1 {
		t.Fatalf("expected at least 1 LLM call, got %d", len(mockLLM.calls))
	}

	req := mockLLM.calls[0]

	finishCount := 0
	for _, tool := range req.Tools {
		if tool.Name == "finish" {
			finishCount++
		}
	}

	if finishCount != 1 {
		t.Errorf("expected exactly 1 finish tool, got %d", finishCount)
	}

	// Total tools should be 2 (mytool + finish), not 3
	if len(req.Tools) != 2 {
		t.Errorf("expected 2 tools (mytool + finish), got %d: %v", len(req.Tools), req.Tools)
	}
}

// === Nudge Mechanism Tests ===

// TestExecutor_NudgeMechanism_RetriesOnNoToolsStep1 tests that when LLM returns no tool calls
// on step 1 with tools available, the executor adds a nudge message and retries.
func TestExecutor_NudgeMechanism_RetriesOnNoToolsStep1(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				// First call: return no tools (will trigger nudge)
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: "I cannot determine this"},
					StopReason: "end_turn",
					Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
				}, nil
			}
			if callCount == 2 {
				// Second call (after nudge): use tools and then finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Let me search for that",
						ToolCalls: []llm.ToolCall{
							{ID: "call_1", Name: "search", Input: json.RawMessage(`{"query":"test"}`)},
						},
					},
					StopReason: "tool_use",
					Usage:      llm.TokenUsage{InputTokens: 150, OutputTokens: 60},
				}, nil
			}
			// Third call: finish with answer
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Found the answer",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "finish", Input: json.RawMessage(`{"answer":"The answer is 42"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 200, OutputTokens: 70},
			}, nil
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"search": {Content: "Found the answer: 42", IsError: false},
		},
	}
	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Find the answer",
		Tools: []tools.ToolDescriptor{
			{Name: "search", Description: "Search for information"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify LLM was called 3 times (first + nudge retry + after tool)
	if callCount != 3 {
		t.Errorf("expected 3 LLM calls (first + nudge retry + after tool), got %d", callCount)
	}

	// Verify a nudge step was added to context manager
	if len(mockCW.steps) == 0 {
		t.Fatal("expected at least one step to be added to context manager")
	}

	// The first step should be the nudge step with the nudge observation
	nudgeStep := mockCW.steps[0]
	if nudgeStep.Observation == "" {
		t.Error("expected nudge step to have observation")
	}
	if !containsIgnoreCase(nudgeStep.Observation, "tools available") {
		t.Errorf("expected nudge observation to mention tools, got: %s", nudgeStep.Observation)
	}

	// Verify the search tool was executed
	if len(mockTools.calls) != 1 || mockTools.calls[0] != "search" {
		t.Errorf("expected search tool to be called once, got: %v", mockTools.calls)
	}

	// Result should be finished
	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	// Output should be from finish tool
	if result.Output != "The answer is 42" {
		t.Errorf("expected output 'The answer is 42', got '%s'", result.Output)
	}
}

// TestExecutor_NudgeMechanism_AcceptsImplicitFinishAfterRetry tests that if the retry
// after nudge also returns no tool calls, implicit_finish is accepted.
func TestExecutor_NudgeMechanism_AcceptsImplicitFinishAfterRetry(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			// Both calls return no tool calls - second should accept implicit finish
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "I really cannot do this"},
				StopReason: "end_turn",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			}, nil
		},
	}

	mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Find the answer",
		Tools: []tools.ToolDescriptor{
			{Name: "search", Description: "Search for information"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify LLM was called twice (first attempt + retry after nudge)
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls, got %d", callCount)
	}

	// Verify result is finished (implicit finish accepted after nudge retry)
	if !result.Finished {
		t.Error("expected Finished to be true (implicit finish after nudge retry)")
	}

	// Output should be the last response content
	if result.Output != "I really cannot do this" {
		t.Errorf("expected output 'I really cannot do this', got '%s'", result.Output)
	}

	// Should have 2 steps: nudge step + final implicit finish step
	if len(result.Steps) != 2 {
		t.Errorf("expected 2 steps (nudge + implicit finish), got %d", len(result.Steps))
	}
}

// TestExecutor_NudgeMechanism_ProducesToolCallsOnRetry tests that when the retry
// after nudge produces tool calls, they are executed normally.
func TestExecutor_NudgeMechanism_ProducesToolCallsOnRetry(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				// First call: return no tools
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: "Thinking..."},
					StopReason: "end_turn",
					Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
				}, nil
			}
			if callCount == 2 {
				// Second call (after nudge): use the search tool
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Searching for answer",
						ToolCalls: []llm.ToolCall{
							{ID: "call_1", Name: "search", Input: json.RawMessage(`{"query":"answer"}`)},
						},
					},
					StopReason: "tool_use",
					Usage:      llm.TokenUsage{InputTokens: 150, OutputTokens: 60},
				}, nil
			}
			// Third call: finish with answer
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Found it!",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "finish", Input: json.RawMessage(`{"answer":"The answer is 42"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 200, OutputTokens: 70},
			}, nil
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"search": {Content: "Found: 42", IsError: false},
		},
	}
	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Find the answer",
		Tools: []tools.ToolDescriptor{
			{Name: "search", Description: "Search for information"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify LLM was called 3 times (first + nudge retry + after tool)
	if callCount != 3 {
		t.Errorf("expected 3 LLM calls, got %d", callCount)
	}

	// Verify search tool was called
	if len(mockTools.calls) != 1 || mockTools.calls[0] != "search" {
		t.Errorf("expected search tool to be called once, got: %v", mockTools.calls)
	}

	// Verify result is finished
	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	// Output should be from finish tool
	if result.Output != "The answer is 42" {
		t.Errorf("expected output 'The answer is 42', got '%s'", result.Output)
	}

	// Should have 3 steps: nudge + search + finish
	if len(result.Steps) != 3 {
		t.Errorf("expected 3 steps, got %d", len(result.Steps))
	}
}

// TestExecutor_NudgeMechanism_NoNudgeWithoutTools tests that nudge is NOT triggered
// when there are no tools available (empty tools list).
func TestExecutor_NudgeMechanism_NoNudgeWithoutTools(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "Direct answer"},
				StopReason: "end_turn",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			}, nil
		},
	}

	mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	// Task with NO tools
	task := TaskDefinition{
		Task:  "Simple question",
		Tools: []tools.ToolDescriptor{}, // Empty tools
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify LLM was called only once (no nudge retry)
	if callCount != 1 {
		t.Errorf("expected 1 LLM call (no nudge when no tools), got %d", callCount)
	}

	// Verify result is finished
	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	// Single step
	if len(result.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(result.Steps))
	}
}

// TestExecutor_NudgeMechanism_NudgeOnLaterSteps tests that nudge IS triggered
// on later steps when tools are available but not used.
func TestExecutor_NudgeMechanism_NudgeOnLaterSteps(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				// First call: use a tool
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Using tool",
						ToolCalls: []llm.ToolCall{
							{ID: "call_1", Name: "search", Input: json.RawMessage(`{"query":"test"}`)},
						},
					},
					StopReason: "tool_use",
					Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
				}, nil
			}
			// Calls 2 and 3: return no tools
			// Call 2 will trigger nudge, call 3 will be implicit finish
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "No more tools needed"},
				StopReason: "end_turn",
				Usage:      llm.TokenUsage{InputTokens: 150, OutputTokens: 60},
			}, nil
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"search": {Content: "search result", IsError: false},
		},
	}
	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Find something",
		Tools: []tools.ToolDescriptor{
			{Name: "search", Description: "Search tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify LLM was called 3 times (nudge on step 2)
	if callCount != 3 {
		t.Errorf("expected 3 LLM calls (nudge on step 2), got %d", callCount)
	}

	// Verify result is finished
	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	// Should have 3 steps: tool call + nudge + implicit finish
	if len(result.Steps) != 3 {
		t.Errorf("expected 3 steps, got %d", len(result.Steps))
	}
}

// === Reactive Compaction Tests ===

// TestExecutor_ReactiveCompaction_RejectTriggersCompact tests that when CheckFill returns "reject",
// the executor triggers reactive compaction and retries instead of erroring.
func TestExecutor_ReactiveCompaction_RejectTriggersCompact(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				// First call: use a regular tool (not finish) so we reach CheckFill
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Using a tool",
						ToolCalls: []llm.ToolCall{
							{ID: "call_1", Name: "sometool", Input: json.RawMessage(`{}`)},
						},
					},
					StopReason: "tool_use",
					Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
				}, nil
			}
			// Second call (after compaction retry): finish
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Task completed",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "finish", Input: json.RawMessage(`{"answer":"Success after compaction"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			}, nil
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"sometool": {Content: "tool result", IsError: false},
		},
	}

	// Context manager that returns "reject" on first CheckFill, then "ok" after Compact
	checkFillCallCount := 0
	mockCW := &mockContextManager{
		checkFillFn: func() FillCheck {
			checkFillCallCount++
			if checkFillCallCount == 1 {
				return FillCheck{Percent: 105, Status: "reject", Used: 105000, Max: 100000}
			}
			return FillCheck{Percent: 50, Status: "ok", Used: 50000, Max: 100000}
		},
	}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Test reactive compaction on reject",
		Tools: []tools.ToolDescriptor{
			{Name: "sometool", Description: "Some tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify compaction was called
	if !mockCW.compactCalled {
		t.Error("expected Compact to be called for reactive compaction")
	}

	// Verify the task completed successfully after compaction
	if !result.Finished {
		t.Error("expected Finished to be true after reactive compaction and retry")
	}

	if result.Output != "Success after compaction" {
		t.Errorf("expected output 'Success after compaction', got '%s'", result.Output)
	}

	// Verify CheckFill was called once (after step 1, which triggered reactive compaction)
	// Note: CheckFill is not called after step 2 because the finish tool returns early
	if checkFillCallCount != 1 {
		t.Errorf("expected CheckFill to be called once, got %d", checkFillCallCount)
	}
}

// TestExecutor_ReactiveCompaction_APIContextExceeded tests that when the API returns
// a "context length exceeded" error, the executor compacts and retries.
func TestExecutor_ReactiveCompaction_APIContextExceeded(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				// First call: return context length exceeded error
				return nil, errors.New("context length exceeded: maximum context length is 128000 tokens")
			}
			// Second call: succeed after compaction
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Task completed after compaction",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "finish", Input: json.RawMessage(`{"answer":"Success after API error recovery"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			}, nil
		},
	}

	mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Test reactive compaction on API error",
		Tools: []tools.ToolDescriptor{
			{Name: "sometool", Description: "Some tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify compaction was called
	if !mockCW.compactCalled {
		t.Error("expected Compact to be called for reactive compaction on API error")
	}

	// Verify LLM was called twice (first error, then success)
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls (error + retry), got %d", callCount)
	}

	// Verify the task completed successfully
	if !result.Finished {
		t.Error("expected Finished to be true after reactive compaction and retry")
	}

	if result.Output != "Success after API error recovery" {
		t.Errorf("expected output 'Success after API error recovery', got '%s'", result.Output)
	}
}

// TestExecutor_ReactiveCompaction_DoubleRejectFails tests that when CheckFill returns
// "reject" even after compaction, the executor returns an error.
func TestExecutor_ReactiveCompaction_DoubleRejectFails(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			// Always return a regular tool call (not finish) so we reach CheckFill
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Using a tool",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "sometool", Input: json.RawMessage(`{}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			}, nil
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"sometool": {Content: "tool result", IsError: false},
		},
	}

	// Context manager that always returns "reject" even after Compact
	mockCW := &mockContextManager{
		checkFillFn: func() FillCheck {
			return FillCheck{Percent: 105, Status: "reject", Used: 105000, Max: 100000}
		},
	}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Test double reject failure",
		Tools: []tools.ToolDescriptor{
			{Name: "sometool", Description: "Some tool"},
		},
	}

	_, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err == nil {
		t.Fatal("expected error when CheckFill returns reject even after compaction")
	}

	// Verify the error message indicates reactive compaction was attempted
	if !strings.Contains(err.Error(), "after reactive compaction") {
		t.Errorf("expected error message to contain 'after reactive compaction', got: %v", err)
	}

	// Verify compaction was called
	if !mockCW.compactCalled {
		t.Error("expected Compact to be called")
	}

	// Verify LLM was called twice (step 1 + step 2 before error on second CheckFill)
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls, got %d", callCount)
	}
}

// TestExecutor_ReactiveCompaction_NonContextErrorNotIntercepted tests that non-context
// errors (e.g., connection refused) propagate without triggering compaction.
func TestExecutor_ReactiveCompaction_NonContextErrorNotIntercepted(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			// Always return a non-context error
			return nil, errors.New("connection refused: unable to reach API endpoint")
		},
	}

	mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Test non-context error propagation",
		Tools: []tools.ToolDescriptor{
			{Name: "sometool", Description: "Some tool"},
		},
	}

	_, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err == nil {
		t.Fatal("expected error for non-context API error")
	}

	// Verify the error is the original connection error
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected error to contain 'connection refused', got: %v", err)
	}

	// Verify compaction was NOT called (error should propagate immediately)
	if mockCW.compactCalled {
		t.Error("expected Compact NOT to be called for non-context errors")
	}

	// Verify LLM was called only once (no retry)
	if callCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", callCount)
	}
}

// containsIgnoreCase is a helper function that checks if s contains substr (case-insensitive)
func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(s != "" && containsIgnoreCaseHelper(s, substr)))
}

func containsIgnoreCaseHelper(s, substr string) bool {
	lowerS := make([]byte, len(s))
	lowerSubstr := make([]byte, len(substr))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		lowerS[i] = c
	}
	for i := 0; i < len(substr); i++ {
		c := substr[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		lowerSubstr[i] = c
	}
	for i := 0; i <= len(lowerS)-len(lowerSubstr); i++ {
		if bytes.Equal(lowerS[i:i+len(lowerSubstr)], lowerSubstr) {
			return true
		}
	}
	return false
}

// === SuppressAssistantEvents Tests ===

// TestExecutor_SuppressAssistantEvents_True verifies that when suppressAssistantEvents is true,
// the executor does NOT call AssistantChunk/AssistantDone on the emitter when finishing.
func TestExecutor_SuppressAssistantEvents_True(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Direct answer without tools",
				},
				StopReason: "end_turn",
				Usage:      llm.TokenUsage{InputTokens: 50, OutputTokens: 30},
			},
		},
	}

	mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
	mockCW := &mockContextManager{}
	mockEm := &mockEmitter{}

	// Create executor with suppressAssistantEvents = true
	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, mockEm, true, ToolResultBudget{})

	task := TaskDefinition{Task: "Simple question"}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify result completed
	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	// Verify AssistantChunk and AssistantDone were NOT called
	if len(mockEm.assistantChunks) != 0 {
		t.Errorf("expected 0 AssistantChunk calls, got %d", len(mockEm.assistantChunks))
	}
	if len(mockEm.assistantDones) != 0 {
		t.Errorf("expected 0 AssistantDone calls, got %d", len(mockEm.assistantDones))
	}
}

// TestExecutor_SuppressAssistantEvents_False verifies that when suppressAssistantEvents is false,
// the executor DOES call AssistantChunk/AssistantDone on the emitter when finishing.
func TestExecutor_SuppressAssistantEvents_False(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Direct answer with events",
				},
				StopReason: "end_turn",
				Usage:      llm.TokenUsage{InputTokens: 50, OutputTokens: 30},
			},
		},
	}

	mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
	mockCW := &mockContextManager{}
	mockEm := &mockEmitter{}

	// Create executor with suppressAssistantEvents = false
	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, mockEm, false, ToolResultBudget{})

	task := TaskDefinition{Task: "Simple question"}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify result completed
	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	// Verify AssistantChunk and AssistantDone WERE called
	if len(mockEm.assistantChunks) != 1 {
		t.Errorf("expected 1 AssistantChunk call, got %d", len(mockEm.assistantChunks))
	}
	if len(mockEm.assistantDones) != 1 {
		t.Errorf("expected 1 AssistantDone call, got %d", len(mockEm.assistantDones))
	}

	// Verify the content is correct
	if len(mockEm.assistantChunks) > 0 && mockEm.assistantChunks[0] != "Direct answer with events" {
		t.Errorf("expected AssistantChunk content 'Direct answer with events', got %q", mockEm.assistantChunks[0])
	}
	if len(mockEm.assistantDones) > 0 && mockEm.assistantDones[0].content != "Direct answer with events" {
		t.Errorf("expected AssistantDone content 'Direct answer with events', got %q", mockEm.assistantDones[0].content)
	}
}

// === Tool Result Budget Tests ===

// mockContextManagerWithAvailableTokens is a mock that allows setting available tokens for budget tests.
type mockContextManagerWithAvailableTokens struct {
	mockContextManager
	availableTokens int
}

func (m *mockContextManagerWithAvailableTokens) AvailableTokens() int {
	return m.availableTokens
}

func TestExecutor_ToolResultBudget_HardCap(t *testing.T) {
	// Setup: Large observation (e.g., 50000 chars = ~12500 tokens)
	// Budget: HardCapTokens=2000, MaxFillFraction=0.3
	// AvailableTokens: 100000 (large, so hard cap wins)
	// Verify: observation is truncated to ~8000 chars (2000*4)
	// Verify: truncation notice is appended

	largeObservation := strings.Repeat("a", 50000)

	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Using tool",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "sometool", Input: json.RawMessage(`{}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Done",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "finish", Input: json.RawMessage(`{"answer":"done"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"sometool": {Content: largeObservation, IsError: false},
		},
	}

	mockCW := &mockContextManagerWithAvailableTokens{
		availableTokens: 100000, // large, so hard cap wins
	}

	budget := ToolResultBudget{
		HardCapTokens:   2000,
		MaxFillFraction: 0.3,
	}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, budget)

	task := TaskDefinition{
		Task: "Test budget",
		Tools: []tools.ToolDescriptor{
			{Name: "sometool", Description: "Some tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	// Verify the step observation was truncated
	if len(mockCW.steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(mockCW.steps))
	}

	observation := mockCW.steps[0].Observation

	// Should be truncated to ~8000 chars (2000*4) plus notice
	if len(observation) > 9000 {
		t.Errorf("expected observation to be truncated to ~8000 chars, got %d", len(observation))
	}

	// Should contain truncation notice
	if !strings.Contains(observation, "[OUTPUT TRUNCATED:") {
		t.Errorf("expected truncation notice in observation, got: %s", observation[:min(len(observation), 200)])
	}
}

func TestExecutor_ToolResultBudget_AdaptiveCap(t *testing.T) {
	// Setup: Large observation
	// Budget: HardCapTokens=8192, MaxFillFraction=0.3
	// AvailableTokens: 1000 (small, so adaptive cap = 300 tokens wins)
	// But floor is 256, so cap = max(300, 256) = 300
	// Verify: observation truncated to ~1200 chars (300*4)

	largeObservation := strings.Repeat("b", 20000)

	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Using tool",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "sometool", Input: json.RawMessage(`{}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Done",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "finish", Input: json.RawMessage(`{"answer":"done"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"sometool": {Content: largeObservation, IsError: false},
		},
	}

	mockCW := &mockContextManagerWithAvailableTokens{
		availableTokens: 1000, // small, so adaptive cap wins
	}

	budget := ToolResultBudget{
		HardCapTokens:   8192,
		MaxFillFraction: 0.3,
	}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, budget)

	task := TaskDefinition{
		Task: "Test budget",
		Tools: []tools.ToolDescriptor{
			{Name: "sometool", Description: "Some tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	if len(mockCW.steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(mockCW.steps))
	}

	observation := mockCW.steps[0].Observation

	// Should be truncated to ~1200 chars (300*4) plus notice
	// Allow some tolerance for the notice
	if len(observation) > 1500 {
		t.Errorf("expected observation to be truncated to ~1200 chars, got %d", len(observation))
	}

	if !strings.Contains(observation, "[OUTPUT TRUNCATED:") {
		t.Errorf("expected truncation notice in observation")
	}
}

func TestExecutor_ToolResultBudget_SmallResultPassesThrough(t *testing.T) {
	// Setup: Small observation (100 chars)
	// Budget: HardCapTokens=8192
	// Verify: observation unchanged

	smallObservation := "small result"

	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Using tool",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "sometool", Input: json.RawMessage(`{}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Done",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "finish", Input: json.RawMessage(`{"answer":"done"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"sometool": {Content: smallObservation, IsError: false},
		},
	}

	mockCW := &mockContextManagerWithAvailableTokens{
		availableTokens: 100000,
	}

	budget := ToolResultBudget{
		HardCapTokens:   8192,
		MaxFillFraction: 0.3,
	}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, budget)

	task := TaskDefinition{
		Task: "Test budget",
		Tools: []tools.ToolDescriptor{
			{Name: "sometool", Description: "Some tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	if len(mockCW.steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(mockCW.steps))
	}

	observation := mockCW.steps[0].Observation

	// Should be unchanged
	if observation != smallObservation {
		t.Errorf("expected observation unchanged, got: %s", observation)
	}

	// Should NOT contain truncation notice
	if strings.Contains(observation, "[OUTPUT TRUNCATED:") {
		t.Errorf("did not expect truncation notice for small result")
	}
}

func TestExecutor_ToolResultBudget_FloorPreventsZeroCap(t *testing.T) {
	// Setup: Large observation
	// Budget: HardCapTokens=8192, MaxFillFraction=0.3
	// AvailableTokens: 100 (very small, adaptive = 30 tokens)
	// Verify: floor of 256 is used, not 30

	largeObservation := strings.Repeat("c", 5000)

	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Using tool",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "sometool", Input: json.RawMessage(`{}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Done",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "finish", Input: json.RawMessage(`{"answer":"done"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"sometool": {Content: largeObservation, IsError: false},
		},
	}

	mockCW := &mockContextManagerWithAvailableTokens{
		availableTokens: 100, // very small, would give 30 tokens without floor
	}

	budget := ToolResultBudget{
		HardCapTokens:   8192,
		MaxFillFraction: 0.3,
	}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, budget)

	task := TaskDefinition{
		Task: "Test budget",
		Tools: []tools.ToolDescriptor{
			{Name: "sometool", Description: "Some tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	if len(mockCW.steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(mockCW.steps))
	}

	observation := mockCW.steps[0].Observation

	// Should be truncated to ~1024 chars (256*4) plus notice
	// Without floor, it would be ~120 chars (30*4)
	if len(observation) < 1000 {
		t.Errorf("expected floor of 256 tokens to be used, observation too short: %d chars", len(observation))
	}

	if !strings.Contains(observation, "[OUTPUT TRUNCATED:") {
		t.Errorf("expected truncation notice in observation")
	}
}

func TestExecutor_ToolResultBudget_TruncationNotice(t *testing.T) {
	// Setup: observation that will be truncated
	// Verify: notice contains "[OUTPUT TRUNCATED:" and token counts

	largeObservation := strings.Repeat("d", 10000)

	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Using tool",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "sometool", Input: json.RawMessage(`{}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Done",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "finish", Input: json.RawMessage(`{"answer":"done"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"sometool": {Content: largeObservation, IsError: false},
		},
	}

	mockCW := &mockContextManagerWithAvailableTokens{
		availableTokens: 100000,
	}

	budget := ToolResultBudget{
		HardCapTokens:   1000,
		MaxFillFraction: 0.3,
	}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, budget)

	task := TaskDefinition{
		Task: "Test budget",
		Tools: []tools.ToolDescriptor{
			{Name: "sometool", Description: "Some tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	if len(mockCW.steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(mockCW.steps))
	}

	observation := mockCW.steps[0].Observation

	// Verify truncation notice format
	if !strings.Contains(observation, "[OUTPUT TRUNCATED:") {
		t.Errorf("expected truncation notice to contain '[OUTPUT TRUNCATED:'")
	}

	if !strings.Contains(observation, "tokens") {
		t.Errorf("expected truncation notice to mention 'tokens'")
	}

	if !strings.Contains(observation, "of") {
		t.Errorf("expected truncation notice to contain 'of' for token counts")
	}

	// Should contain percentage
	if !strings.Contains(observation, "%") {
		t.Errorf("expected truncation notice to contain percentage")
	}
}

func TestExecutor_ToolResultBudget_Disabled(t *testing.T) {
	// Setup: Large observation
	// Budget: zero value (disabled)
	// Verify: observation returned unchanged

	largeObservation := strings.Repeat("e", 50000)

	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Using tool",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "sometool", Input: json.RawMessage(`{}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Done",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "finish", Input: json.RawMessage(`{"answer":"done"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			},
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"sometool": {Content: largeObservation, IsError: false},
		},
	}

	mockCW := &mockContextManagerWithAvailableTokens{
		availableTokens: 100000,
	}

	// Zero value budget = disabled
	budget := ToolResultBudget{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, budget)

	task := TaskDefinition{
		Task: "Test budget",
		Tools: []tools.ToolDescriptor{
			{Name: "sometool", Description: "Some tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	if len(mockCW.steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(mockCW.steps))
	}

	observation := mockCW.steps[0].Observation

	// Should be unchanged (full length)
	if observation != largeObservation {
		t.Errorf("expected observation unchanged (length %d), got length %d", len(largeObservation), len(observation))
	}

	// Should NOT contain truncation notice
	if strings.Contains(observation, "[OUTPUT TRUNCATED:") {
		t.Errorf("did not expect truncation notice when budget is disabled")
	}
}

// === Circuit Breaker Tests ===

func TestExecutor_RepeatedToolCallCircuitBreaker(t *testing.T) {
	// Test that the circuit breaker aborts when the LLM keeps calling the same tool with the same args.
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Let me try again",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "file_ops", Input: json.RawMessage(`{"action":"write_file","path":"/tmp/test.txt","content":"hello"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			}, nil
		},
	}

	mockTools := &mockToolExecutor{
		executeFn: func(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
			return tools.ToolResult{Content: "Error: permission denied", IsError: true}, nil
		},
	}

	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Write a file",
		Tools: []tools.ToolDescriptor{
			{Name: "file_ops", Description: "File operations"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.Finished {
		t.Error("expected Finished to be false (circuit breaker abort)")
	}

	if !strings.Contains(result.Output, "Aborted") {
		t.Errorf("expected output to contain 'Aborted', got '%s'", result.Output)
	}

	if !strings.Contains(result.Output, "file_ops") {
		t.Errorf("expected output to contain 'file_ops', got '%s'", result.Output)
	}

	if len(result.Steps) >= 10 {
		t.Errorf("expected circuit breaker to kick in early (less than 10 steps), got %d", len(result.Steps))
	}

	// Exactly 2 steps: 1 normal execution + 1 nudge step (error-aware thresholds: nudge at repeat 2, abort at repeat 3)
	if len(result.Steps) != 2 {
		t.Errorf("expected exactly 2 steps (1 normal + 1 nudge), got %d", len(result.Steps))
	}
}

func TestExecutor_RepeatedToolCallResets(t *testing.T) {
	// Test that the circuit breaker counter resets when the tool call changes.
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			switch callCount {
			case 1, 2:
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Searching",
						ToolCalls: []llm.ToolCall{
							{ID: "call_1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
						},
					},
					StopReason: "tool_use",
					Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
				}, nil
			case 3:
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Reading file",
						ToolCalls: []llm.ToolCall{
							{ID: "call_1", Name: "file_ops", Input: json.RawMessage(`{"action":"read"}`)},
						},
					},
					StopReason: "tool_use",
					Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
				}, nil
			case 4, 5:
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Searching again",
						ToolCalls: []llm.ToolCall{
							{ID: "call_1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
						},
					},
					StopReason: "tool_use",
					Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
				}, nil
			default:
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Finishing",
						ToolCalls: []llm.ToolCall{
							{ID: "call_1", Name: "finish", Input: json.RawMessage(`{"answer":"done"}`)},
						},
					},
					StopReason: "tool_use",
					Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
				}, nil
			}
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"search":   {Content: "search result", IsError: false},
			"file_ops": {Content: "file content", IsError: false},
		},
	}

	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, nil, false, ToolResultBudget{})

	task := TaskDefinition{
		Task: "Mixed tool calls",
		Tools: []tools.ToolDescriptor{
			{Name: "search", Description: "Search tool"},
			{Name: "file_ops", Description: "File operations"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if !result.Finished {
		t.Error("expected Finished to be true (no circuit breaker abort)")
	}

	if result.Output != "done" {
		t.Errorf("expected output 'done', got '%s'", result.Output)
	}
}

// === Plan Context Logging Tests ===

func TestExecutor_PlanContextInLogs(t *testing.T) {
	// Test that SetPlanContext causes log lines to include plan step info.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Searching",
						ToolCalls: []llm.ToolCall{
							{ID: "call_1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
						},
					},
					StopReason: "tool_use",
					Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
				}, nil
			}
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Done",
					ToolCalls: []llm.ToolCall{
						{ID: "call_2", Name: "finish", Input: json.RawMessage(`{"answer":"ok"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
			}, nil
		},
	}

	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"search": {Content: "search result", IsError: false},
		},
	}

	mockCW := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, logger, nil, false, ToolResultBudget{})
	executor.SetPlanContext("step_3", 3, 10)

	task := TaskDefinition{
		Task: "Test plan context logging",
		Tools: []tools.ToolDescriptor{
			{Name: "search", Description: "Search tool"},
		},
	}

	result, err := executor.Run(context.Background(), task.Tools, mockCW)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if !result.Finished {
		t.Error("expected Finished to be true")
	}

	logOutput := buf.String()

	if !strings.Contains(logOutput, "plan_step") {
		t.Errorf("expected log output to contain 'plan_step', got:\n%s", logOutput)
	}

	if !strings.Contains(logOutput, "step_3") {
		t.Errorf("expected log output to contain 'step_3', got:\n%s", logOutput)
	}

	if !strings.Contains(logOutput, "3/10") {
		t.Errorf("expected log output to contain '3/10', got:\n%s", logOutput)
	}
}
