package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
)

// denyingHITLHandler denies all tool calls. Used to test that rejected
// mutating tools do not count toward the mutation gate.
type denyingHITLHandler struct{}

func (h *denyingHITLHandler) OnToolCall(_ context.Context, _ string, _ json.RawMessage) (*HITLToolDecision, error) {
	return &HITLToolDecision{Allow: false, Reason: "denied by test"}, nil
}

func (h *denyingHITLHandler) OnStepLimit(_ context.Context, _, _ int, _ string) (StepLimitResponse, error) {
	return StepLimitDeny, nil
}

var _ HITLHandler = (*denyingHITLHandler)(nil)

func TestExecutor_MutationGate_FinishWithoutMutation_NudgesThenRejects(t *testing.T) {
	// LLM: finish immediately (no mutations) → nudge → finish again (still no mutations)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseFinish("I inspected the code.", "No changes needed."),
			llmResponseFinish("Still no changes.", "I could not make changes."),
		},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetMutationRequired(true)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "read_file", Description: "read", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Finished {
		t.Error("expected Finished=false — mutation gate should reject finish without mutations")
	}
	if result.Output == "" {
		t.Error("expected non-empty output (the finish answer)")
	}
}

func TestExecutor_MutationGate_FinishAfterMutation_Accepts(t *testing.T) {
	// LLM: write_file → finish
	writeInput := json.RawMessage(`{"path": "/tmp/test.go", "content": "package main"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("Writing file", "write_file", writeInput),
			llmResponseFinish("Done", "File written successfully"),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["write_file"] = tools.ToolResult{Content: "file written"}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetMutationRequired(true)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "write_file", Description: "write", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true — mutation gate should accept finish after write_file")
	}
}

func TestExecutor_MutationGate_FinishAfterEditFile_Accepts(t *testing.T) {
	editInput := json.RawMessage(`{"path": "/tmp/test.go", "old": "a", "new": "b"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("Editing file", "edit_file", editInput),
			llmResponseFinish("Done", "File edited successfully"),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["edit_file"] = tools.ToolResult{Content: "file edited"}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetMutationRequired(true)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "edit_file", Description: "edit", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true — edit_file counts as mutation")
	}
}

func TestExecutor_MutationGate_FinishAfterReadOnlyOnly_NudgesThenAcceptsMutation(t *testing.T) {
	// LLM: read_file → finish (nudge) → write_file → finish (accepted)
	readInput := json.RawMessage(`{"path": "/tmp/test.go"}`)
	writeInput := json.RawMessage(`{"path": "/tmp/test.go", "content": "new"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("Reading", "read_file", readInput),
			llmResponseFinish("Done reading", "I read the file."),
			llmResponseWithToolCall("Now writing", "write_file", writeInput),
			llmResponseFinish("Done writing", "File written."),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["read_file"] = tools.ToolResult{Content: "old content"}
	mockTools.results["write_file"] = tools.ToolResult{Content: "file written"}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetMutationRequired(true)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "read_file", Description: "read", Source: "core"},
		{Name: "write_file", Description: "write", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true — after nudge, LLM made mutation and finished")
	}
}

func TestExecutor_MutationGate_NotRequired_AcceptsFinishWithoutMutation(t *testing.T) {
	// Without mutation gate, finish without mutations should be accepted (researcher steps etc.)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseFinish("Research complete", "Found the answer."),
		},
	}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	// SetMutationRequired not called — defaults to false

	result, err := exec.Run(context.Background(), nil, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true — mutation gate not enabled")
	}
}

func TestExecutor_MutationGate_RejectedToolDoesNotCount(t *testing.T) {
	// LLM: write_file (rejected by HITL) → finish → finish
	// The rejected write_file should NOT count as a mutation.
	writeInput := json.RawMessage(`{"path": "/tmp/test.go", "content": "bad"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("Writing", "write_file", writeInput),
			llmResponseFinish("Done", "Written."),
			llmResponseFinish("Still done", "Written again."),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["write_file"] = tools.ToolResult{Content: "file written"}

	// HITL that denies write_file
	denyingHITL := &denyingHITLHandler{}

	cm := newMockContextManager()
	exec := NewExecutor(mockLLM, mockTools, 10, WithTokenCounter(&mockTokenCounter{}), WithCircuitBreaker(defaultCircuitBreakerConfig), WithHITL(denyingHITL))
	exec.SetMutationRequired(true)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "write_file", Description: "write", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// First finish should be nudged (rejected write doesn't count).
	// Second finish (after nudge) should be rejected as Finished=false.
	if result.Finished {
		t.Error("expected Finished=false — rejected write_file should not count as mutation")
	}
}

func TestHasMutatingToolExecuted(t *testing.T) {
	exec := &Executor{}

	tests := []struct {
		name  string
		steps []Step
		want  bool
	}{
		{
			name:  "no steps",
			steps: nil,
			want:  false,
		},
		{
			name: "only read-only tools",
			steps: []Step{
				{Action: llm.ToolCall{Name: "read_file"}},
				{Action: llm.ToolCall{Name: "ripgrep"}},
			},
			want: false,
		},
		{
			name: "write_file present",
			steps: []Step{
				{Action: llm.ToolCall{Name: "read_file"}},
				{Action: llm.ToolCall{Name: "write_file"}},
			},
			want: true,
		},
		{
			name: "edit_file present",
			steps: []Step{
				{Action: llm.ToolCall{Name: "edit_file"}},
			},
			want: true,
		},
		{
			name: "create_directory present",
			steps: []Step{
				{Action: llm.ToolCall{Name: "create_directory"}},
			},
			want: true,
		},
		{
			name: "delete_file present",
			steps: []Step{
				{Action: llm.ToolCall{Name: "delete_file"}},
			},
			want: true,
		},
		{
			name: "rejected write_file does not count",
			steps: []Step{
				{Action: llm.ToolCall{Name: "write_file"}, Observation: "[Tool call rejected: denied]"},
			},
			want: false,
		},
		{
			name: "non-rejected write_file with different observation counts",
			steps: []Step{
				{Action: llm.ToolCall{Name: "write_file"}, Observation: "file written successfully"},
			},
			want: true,
		},
		{
			name: "failed write_file (IsError) does not count",
			steps: []Step{
				{Action: llm.ToolCall{Name: "write_file"}, Observation: "error: path is outside workspace", IsError: true},
			},
			want: false,
		},
		{
			name: "failed edit_file (IsError) does not count, successful one does",
			steps: []Step{
				{Action: llm.ToolCall{Name: "edit_file"}, Observation: "error: old string not found", IsError: true},
				{Action: llm.ToolCall{Name: "edit_file"}, Observation: "file edited", IsError: false},
			},
			want: true,
		},
		{
			name: "failed write_file (IsError) does not count",
			steps: []Step{
				{Action: llm.ToolCall{Name: "write_file"}, Observation: "error: permission denied", IsError: true},
			},
			want: false,
		},
		{
			name: "failed then successful write_file counts",
			steps: []Step{
				{Action: llm.ToolCall{Name: "write_file"}, Observation: "error: invalid path", IsError: true},
				{Action: llm.ToolCall{Name: "write_file"}, Observation: "file written"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &runState{allSteps: tt.steps}
			got := exec.hasMutatingToolExecuted(state)
			if got != tt.want {
				t.Errorf("hasMutatingToolExecuted() = %v, want %v", got, tt.want)
			}
		})
	}
}
