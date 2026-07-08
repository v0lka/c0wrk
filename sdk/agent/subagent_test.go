package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
)

func TestRunSubAgent_Success(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseFinish("done", "agent output"),
		},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	ch := RunSubAgent(context.Background(), "step_1", exec, cm, nil, "test task", nil, nil)
	result := <-ch
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.StepID != "step_1" {
		t.Errorf("StepID = %q, want %q", result.StepID, "step_1")
	}
	if result.Output != "agent output" {
		t.Errorf("Output = %q, want %q", result.Output, "agent output")
	}
}

func TestRunSubAgent_WithEmitter(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseFinish("done", "output"),
		},
	}
	cm := newMockContextManager()
	events := &recordingEvents{}
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	ch := RunSubAgent(context.Background(), "step_1", exec, cm, nil, "task desc", events, nil)
	result := <-ch
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Check emitter received launch and complete events
	foundLaunch := false
	foundComplete := false
	for _, e := range events.events {
		if e == "SubAgentLaunch:step_1" {
			foundLaunch = true
		}
		if e == "SubAgentComplete:step_1:true" {
			foundComplete = true
		}
	}
	if !foundLaunch {
		t.Error("expected SubAgentLaunch event")
	}
	if !foundComplete {
		t.Error("expected SubAgentComplete event with success=true")
	}
}

func TestRunSubAgent_LLMError(t *testing.T) {
	mockLLM := &mockLLMCaller{
		errors: []error{errors.New("llm failed")},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	ch := RunSubAgent(context.Background(), "step_1", exec, cm, nil, "test", nil, nil)
	result := <-ch
	if result.Error == nil {
		t.Fatal("expected error")
	}
	if result.StepID != "step_1" {
		t.Errorf("StepID = %q, want %q", result.StepID, "step_1")
	}
}

func TestRunSubAgent_MaxStepsExhausted(t *testing.T) {
	// LLM always returns tool calls, never finishes
	toolInput := json.RawMessage(`{}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("t1", "tool1", toolInput),
			llmResponseWithToolCall("t2", "tool2", json.RawMessage(`{"x":"1"}`)),
		},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 2, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	ch := RunSubAgent(context.Background(), "step_1", exec, cm, []tools.ToolDescriptor{
		{Name: "tool1", Description: "t", Source: "core"},
	}, "test", nil, nil)
	result := <-ch
	if result.Error == nil {
		t.Fatal("expected error for max steps exhaustion")
	}
	if result.StepID != "step_1" {
		t.Errorf("StepID = %q, want %q", result.StepID, "step_1")
	}
}

func TestRunSubAgentsParallel_Empty(t *testing.T) {
	results := RunSubAgentsParallel(context.Background(), nil)
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}

	results = RunSubAgentsParallel(context.Background(), []SubAgentTask{})
	if results != nil {
		t.Errorf("expected nil results for empty slice, got %v", results)
	}
}

func TestRunSubAgentsParallel_MultipleAgents(t *testing.T) {
	agents := make([]SubAgentTask, 3)
	for i := 0; i < 3; i++ {
		mockLLM := &mockLLMCaller{
			responses: []*llm.ChatResponse{
				llmResponseFinish("done", "output_"+string(rune('A'+i))),
			},
		}
		agents[i] = SubAgentTask{
			StepID:   "step_" + string(rune('1'+i)),
			Executor: newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig),
			CM:       newMockContextManager(),
			TaskDesc: "task " + string(rune('A'+i)),
		}
	}

	results := RunSubAgentsParallel(context.Background(), agents)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// All should succeed
	for _, r := range results {
		if r.Error != nil {
			t.Errorf("unexpected error for %s: %v", r.StepID, r.Error)
		}
	}
}

func TestRunSubAgentsParallel_MixedResults(t *testing.T) {
	// One succeeds, one fails
	successLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseFinish("ok", "success"),
		},
	}
	failLLM := &mockLLMCaller{
		errors: []error{errors.New("fail")},
	}

	agents := []SubAgentTask{
		{
			StepID:   "step_1",
			Executor: newExecutorDefaultHITL(successLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig),
			CM:       newMockContextManager(),
			TaskDesc: "good task",
		},
		{
			StepID:   "step_2",
			Executor: newExecutorDefaultHITL(failLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig),
			CM:       newMockContextManager(),
			TaskDesc: "bad task",
		},
	}

	results := RunSubAgentsParallel(context.Background(), agents)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	successCount := 0
	errorCount := 0
	for _, r := range results {
		if r.Error == nil {
			successCount++
		} else {
			errorCount++
		}
	}
	if successCount != 1 || errorCount != 1 {
		t.Errorf("expected 1 success and 1 error, got %d success, %d error", successCount, errorCount)
	}
}
