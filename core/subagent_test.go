package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/llm"
	tools "github.com/v0lka/c0wrk/sdk/tools"
)

// Tests use shared mock types from testhelpers_test.go:
// - mockLLMCaller: implements agent.LLMCaller
// - mockToolExecutor: implements agent.ToolExecutor
// - mockContextManager: implements ContextManager

// defaultCircuitBreakerConfig provides the standard circuit breaker thresholds for tests.
var defaultCircuitBreakerConfig = agent.CircuitBreakerConfig{
	RepeatNudgeThreshold:         3,
	RepeatAbortThreshold:         4,
	TruncationAbortThreshold:     3,
	ParseErrorAbortThreshold:     3,
	FruitlessNudgeThreshold:      4,
	FruitlessAbortThreshold:      6,
	FruitlessMaxResultLen:        32,
	SameToolRepeatNudgeThreshold: 6,
	SameToolRepeatAbortThreshold: 10,
	SameToolResultSizeDelta:      64,
}

func TestRunSubAgent_Successful(t *testing.T) {
	// Create mock LLM that returns finish immediately
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Task completed",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "finish", Input: json.RawMessage(`{"answer":"SubAgent result"}`)},
					},
				},
				StopReason: "tool_use",
				Usage:      llm.TokenUsage{InputTokens: 50, OutputTokens: 30},
			},
		},
	}

	mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
	mockCM := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, false, agent.ToolResultBudget{}, defaultCircuitBreakerConfig, nil)

	task := TaskDefinition{Task: "Test task"}

	// Run SubAgent
	ch := agent.RunSubAgent(context.Background(), "step_1", executor, mockCM, task.Tools, task.Task, nil, nil)

	// Wait for result with timeout
	select {
	case result := <-ch:
		if result.StepID != "step_1" {
			t.Errorf("expected StepID 'step_1', got '%s'", result.StepID)
		}
		if result.Output != "SubAgent result" {
			t.Errorf("expected Output 'SubAgent result', got '%s'", result.Output)
		}
		if result.Error != nil {
			t.Errorf("expected no error, got %v", result.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for SubAgent result")
	}
}

func TestRunSubAgent_ContextCancellation(t *testing.T) {
	// Create a context that's cancelled immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Create mock LLM - it shouldn't be called because context is cancelled
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{},
	}

	mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
	mockCM := &mockContextManager{}

	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, false, agent.ToolResultBudget{}, defaultCircuitBreakerConfig, nil)

	task := TaskDefinition{Task: "Test task"}

	// Run SubAgent with cancelled context
	ch := agent.RunSubAgent(ctx, "step_cancel", executor, mockCM, task.Tools, task.Task, nil, nil)

	// Wait for result with timeout
	select {
	case result := <-ch:
		if result.StepID != "step_cancel" {
			t.Errorf("expected StepID 'step_cancel', got '%s'", result.StepID)
		}
		if result.Error == nil {
			t.Error("expected error due to context cancellation, got nil")
		}
		if !errors.Is(result.Error, context.Canceled) {
			t.Errorf("expected context.Canceled error, got %v", result.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for SubAgent result")
	}
}

func TestRunSubAgentsParallel_MultipleAgents(t *testing.T) {
	// Create 3 mock executors that return different results
	createMockExecutor := func(answer string) *agent.Executor {
		mockLLM := &mockLLMCaller{
			responses: []*llm.ChatResponse{
				{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Done",
						ToolCalls: []llm.ToolCall{
							{ID: "call_1", Name: "finish", Input: json.RawMessage(`{"answer":"` + answer + `"}`)},
						},
					},
					StopReason: "tool_use",
					Usage:      llm.TokenUsage{InputTokens: 50, OutputTokens: 30},
				},
			},
		}
		mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
		return agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, false, agent.ToolResultBudget{}, defaultCircuitBreakerConfig, nil)
	}

	agents := []agent.SubAgentTask{
		{
			StepID:   "step_1",
			Executor: createMockExecutor("Result 1"),
			CM:       &mockContextManager{},
			TaskDesc: "Task 1",
		},
		{
			StepID:   "step_2",
			Executor: createMockExecutor("Result 2"),
			CM:       &mockContextManager{},
			TaskDesc: "Task 2",
		},
		{
			StepID:   "step_3",
			Executor: createMockExecutor("Result 3"),
			CM:       &mockContextManager{},
			TaskDesc: "Task 3",
		},
	}

	// Run all agents in parallel
	results := agent.RunSubAgentsParallel(context.Background(), agents)

	// Verify we got 3 results
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Collect step IDs and outputs
	stepIDs := make(map[string]bool)
	outputs := make(map[string]bool)
	for _, result := range results {
		if result.Error != nil {
			t.Errorf("unexpected error for step %s: %v", result.StepID, result.Error)
		}
		stepIDs[result.StepID] = true
		outputs[result.Output] = true
	}

	// Verify all step IDs are present
	expectedSteps := []string{"step_1", "step_2", "step_3"}
	for _, step := range expectedSteps {
		if !stepIDs[step] {
			t.Errorf("missing step ID: %s", step)
		}
	}

	// Verify all outputs are present
	expectedOutputs := []string{"Result 1", "Result 2", "Result 3"}
	for _, output := range expectedOutputs {
		if !outputs[output] {
			t.Errorf("missing output: %s", output)
		}
	}
}

func TestRunSubAgentsParallel_EmptyInput(t *testing.T) {
	results := agent.RunSubAgentsParallel(context.Background(), []agent.SubAgentTask{})
	if results != nil {
		t.Errorf("expected nil for empty input, got %v", results)
	}
}

func TestNewSubAgent(t *testing.T) {
	mockLLM := &mockLLMCaller{}
	mockTools := &mockToolExecutor{}
	executor := agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, false, agent.ToolResultBudget{}, defaultCircuitBreakerConfig, nil)

	subAgent := agent.NewSubAgent("test_id", executor)

	if subAgent.ID != "test_id" {
		t.Errorf("expected id 'test_id', got '%s'", subAgent.ID)
	}
	if subAgent.Executor != executor {
		t.Error("expected executor to be set")
	}
}

func TestRunSubAgentsParallel_WithContextCancellation(t *testing.T) {
	// Create a context that's cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	createMockExecutor := func() *agent.Executor {
		mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{}}
		mockTools := &mockToolExecutor{results: make(map[string]tools.ToolResult)}
		return agent.NewExecutor(mockLLM, mockTools, nil, 10, nil, false, agent.ToolResultBudget{}, defaultCircuitBreakerConfig, nil)
	}

	agents := []agent.SubAgentTask{
		{
			StepID:   "step_1",
			Executor: createMockExecutor(),
			CM:       &mockContextManager{},
			TaskDesc: "Task 1",
		},
		{
			StepID:   "step_2",
			Executor: createMockExecutor(),
			CM:       &mockContextManager{},
			TaskDesc: "Task 2",
		},
	}

	results := agent.RunSubAgentsParallel(ctx, agents)

	// All results should have errors
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, result := range results {
		if result.Error == nil {
			t.Errorf("expected error for step %s due to context cancellation", result.StepID)
		}
	}
}
