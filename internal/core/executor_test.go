package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/user/agent/internal/llm"
	"github.com/user/agent/internal/tools"
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

	executor := NewExecutor(mockLLM, mockTools, nil, 10, "executor", nil, nil, false)

	task := TaskDefinition{
		Task: "Find the answer",
		Tools: []tools.ToolDescriptor{
			{Name: "search", Description: "Search tool"},
		},
	}

	result, err := executor.Run(context.Background(), task, mockCW)
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

	executor := NewExecutor(mockLLM, mockTools, nil, 10, "executor", nil, nil, false)

	task := TaskDefinition{Task: "Simple question"}

	result, err := executor.Run(context.Background(), task, mockCW)
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
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Step 1 thought",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "action", Input: json.RawMessage(`{}`)},
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
						{ID: "call_2", Name: "action", Input: json.RawMessage(`{}`)},
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
						{ID: "call_3", Name: "action", Input: json.RawMessage(`{}`)},
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

	executor := NewExecutor(mockLLM, mockTools, nil, 3, "executor", nil, nil, false)

	task := TaskDefinition{
		Task: "Never-ending task",
		Tools: []tools.ToolDescriptor{
			{Name: "action", Description: "An action tool"},
		},
	}

	result, err := executor.Run(context.Background(), task, mockCW)
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

	executor := NewExecutor(mockLLM, mockTools, nil, 10, "executor", nil, nil, false)

	task := TaskDefinition{Task: "Simple question"}

	result, err := executor.Run(context.Background(), task, mockCW)
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

	executor := NewExecutor(mockLLM, mockTools, nil, 10, "executor", nil, nil, false)

	task := TaskDefinition{
		Task: "Test compaction",
		Tools: []tools.ToolDescriptor{
			{Name: "sometool", Description: "Some tool"},
		},
	}

	result, err := executor.Run(context.Background(), task, mockCW)
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

func TestExecutor_DefaultRole(t *testing.T) {
	// Test that empty lmRole defaults to "executor"
	executor := NewExecutor(nil, nil, nil, 10, "", nil, nil, false)
	if executor.lmRole != "executor" {
		t.Errorf("expected default lmRole 'executor', got '%s'", executor.lmRole)
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

	executor := NewExecutor(mockLLM, mockTools, nil, 10, "executor", nil, nil, false)

	task := TaskDefinition{
		Task: "Test",
		Tools: []tools.ToolDescriptor{
			{Name: "mytool", Description: "My tool"},
		},
	}

	_, _ = executor.Run(context.Background(), task, mockCW)

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

// === Nudge Mechanism Tests ===

// TestExecutor_NudgeMechanism_RetriesOnNoToolsStep1 tests that when LLM returns no tool calls
// on step 1 with tools available, the executor adds a nudge message and retries.
func TestExecutor_NudgeMechanism_RetriesOnNoToolsStep1(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, role string, req llm.ChatRequest) (*llm.ChatResponse, error) {
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

	executor := NewExecutor(mockLLM, mockTools, nil, 10, "executor", nil, nil, false)

	task := TaskDefinition{
		Task: "Find the answer",
		Tools: []tools.ToolDescriptor{
			{Name: "search", Description: "Search for information"},
		},
	}

	result, err := executor.Run(context.Background(), task, mockCW)
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
		callFn: func(ctx context.Context, role string, req llm.ChatRequest) (*llm.ChatResponse, error) {
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

	executor := NewExecutor(mockLLM, mockTools, nil, 10, "executor", nil, nil, false)

	task := TaskDefinition{
		Task: "Find the answer",
		Tools: []tools.ToolDescriptor{
			{Name: "search", Description: "Search for information"},
		},
	}

	result, err := executor.Run(context.Background(), task, mockCW)
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
		callFn: func(ctx context.Context, role string, req llm.ChatRequest) (*llm.ChatResponse, error) {
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

	executor := NewExecutor(mockLLM, mockTools, nil, 10, "executor", nil, nil, false)

	task := TaskDefinition{
		Task: "Find the answer",
		Tools: []tools.ToolDescriptor{
			{Name: "search", Description: "Search for information"},
		},
	}

	result, err := executor.Run(context.Background(), task, mockCW)
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
		callFn: func(ctx context.Context, role string, req llm.ChatRequest) (*llm.ChatResponse, error) {
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

	executor := NewExecutor(mockLLM, mockTools, nil, 10, "executor", nil, nil, false)

	// Task with NO tools
	task := TaskDefinition{
		Task:  "Simple question",
		Tools: []tools.ToolDescriptor{}, // Empty tools
	}

	result, err := executor.Run(context.Background(), task, mockCW)
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
		callFn: func(ctx context.Context, role string, req llm.ChatRequest) (*llm.ChatResponse, error) {
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

	executor := NewExecutor(mockLLM, mockTools, nil, 10, "executor", nil, nil, false)

	task := TaskDefinition{
		Task: "Find something",
		Tools: []tools.ToolDescriptor{
			{Name: "search", Description: "Search tool"},
		},
	}

	result, err := executor.Run(context.Background(), task, mockCW)
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

// containsIgnoreCase is a helper function that checks if s contains substr (case-insensitive)
func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > 0 && containsIgnoreCaseHelper(s, substr)))
}

func containsIgnoreCaseHelper(s, substr string) bool {
	lowerS := make([]byte, len(s))
	lowerSubstr := make([]byte, len(substr))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c = c + 32
		}
		lowerS[i] = c
	}
	for i := 0; i < len(substr); i++ {
		c := substr[i]
		if c >= 'A' && c <= 'Z' {
			c = c + 32
		}
		lowerSubstr[i] = c
	}
	for i := 0; i <= len(lowerS)-len(lowerSubstr); i++ {
		if string(lowerS[i:i+len(lowerSubstr)]) == string(lowerSubstr) {
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
	executor := NewExecutor(mockLLM, mockTools, nil, 10, "executor", nil, mockEm, true)

	task := TaskDefinition{Task: "Simple question"}

	result, err := executor.Run(context.Background(), task, mockCW)
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
	executor := NewExecutor(mockLLM, mockTools, nil, 10, "executor", nil, mockEm, false)

	task := TaskDefinition{Task: "Simple question"}

	result, err := executor.Run(context.Background(), task, mockCW)
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
