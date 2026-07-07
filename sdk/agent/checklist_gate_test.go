package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
)

// checklistToolDescriptor returns a ToolDescriptor for update_checklist, used
// to make the checklist gate believe the tool is available.
func checklistToolDescriptor() tools.ToolDescriptor {
	return tools.ToolDescriptor{Name: "update_checklist", Description: "update checklist", Source: "core"}
}

func TestExecutor_ChecklistGate_FinishWithoutChecklist_NudgesThenAccepts(t *testing.T) {
	// Non-trivial step: 3 read_file calls (> threshold of 2), no update_checklist.
	// First finish → nudge. Second finish → accepted (soft gate).
	readInput := json.RawMessage(`{"path": "/tmp/a.go"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("Reading 1", "read_file", readInput),
			llmResponseWithToolCall("Reading 2", "read_file", readInput),
			llmResponseWithToolCall("Reading 3", "read_file", readInput),
			llmResponseFinish("Done reading", "Summary of findings."),
			llmResponseFinish("Still done", "Final summary."),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["read_file"] = tools.ToolResult{Content: "content"}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "read_file", Description: "read", Source: "core"},
		checklistToolDescriptor(),
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true — checklist gate is a soft nudge, second finish should be accepted")
	}
	// Verify the nudge was injected into steps.
	foundNudge := false
	for _, s := range result.Steps {
		if s.UserNudge == executorChecklistMissingNudge {
			foundNudge = true
			break
		}
	}
	if !foundNudge {
		t.Error("expected checklist missing nudge to be injected into steps")
	}
}

func TestExecutor_ChecklistGate_TrivialStep_AcceptsWithoutChecklist(t *testing.T) {
	// Trivial step: 1 read_file (≤ threshold of 2), no update_checklist.
	// Should accept without nudging.
	readInput := json.RawMessage(`{"path": "/tmp/a.go"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("Reading", "read_file", readInput),
			llmResponseFinish("Done", "Answer."),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["read_file"] = tools.ToolResult{Content: "content"}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "read_file", Description: "read", Source: "core"},
		checklistToolDescriptor(),
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true — trivial step should not trigger checklist gate")
	}
	for _, s := range result.Steps {
		if s.UserNudge == executorChecklistMissingNudge {
			t.Error("trivial step should not receive checklist missing nudge")
		}
	}
}

func TestExecutor_ChecklistGate_FinishWithUncheckedChecklist_NudgesThenAccepts(t *testing.T) {
	// update_checklist with 1/3 done (2 unchecked) → finish → nudge → finish → accept.
	checklistInput := json.RawMessage(`{"todo_list": "- [x] A\n- [ ] B\n- [ ] C"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("Checklist", "update_checklist", checklistInput),
			llmResponseFinish("Done", "Finished with some items left."),
			llmResponseFinish("Still done", "Final answer."),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["update_checklist"] = tools.ToolResult{Content: "Checklist updated: 1/3 done."}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		checklistToolDescriptor(),
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true — unchecked nudge is soft, second finish accepted")
	}
	foundUncheckedNudge := false
	for _, s := range result.Steps {
		if s.UserNudge != "" && s.UserNudge != executorChecklistMissingNudge && s.UserNudge != executorMutationNudge {
			foundUncheckedNudge = true
		}
	}
	if !foundUncheckedNudge {
		t.Error("expected unchecked checklist nudge to be injected")
	}
}

func TestExecutor_ChecklistGate_FinishWithAllCheckedChecklist_Accepts(t *testing.T) {
	// update_checklist with 2/2 done (all checked) → finish → accepted, no nudge.
	checklistInput := json.RawMessage(`{"todo_list": "- [x] A\n- [x] B"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("Checklist", "update_checklist", checklistInput),
			llmResponseFinish("Done", "All items complete."),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["update_checklist"] = tools.ToolResult{Content: "Checklist updated: 2/2 done."}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		checklistToolDescriptor(),
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true — all-checked checklist should not trigger nudge")
	}
	for _, s := range result.Steps {
		if s.UserNudge != "" {
			t.Errorf("expected no nudge for all-checked checklist, got %q", s.UserNudge)
		}
	}
}

func TestExecutor_ChecklistGate_Disabled_AcceptsWithoutChecklist(t *testing.T) {
	// Gate disabled: non-trivial step without checklist → accepted, no nudge.
	readInput := json.RawMessage(`{"path": "/tmp/a.go"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("Reading 1", "read_file", readInput),
			llmResponseWithToolCall("Reading 2", "read_file", readInput),
			llmResponseWithToolCall("Reading 3", "read_file", readInput),
			llmResponseFinish("Done", "Summary."),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["read_file"] = tools.ToolResult{Content: "content"}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetChecklistGateEnabled(false)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "read_file", Description: "read", Source: "core"},
		checklistToolDescriptor(),
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true — gate disabled")
	}
	for _, s := range result.Steps {
		if s.UserNudge == executorChecklistMissingNudge {
			t.Error("gate disabled should not inject checklist nudge")
		}
	}
}

func TestExecutor_ChecklistGate_ToolUnavailable_AcceptsWithoutChecklist(t *testing.T) {
	// update_checklist not in taskTools: gate should not activate even on
	// a non-trivial step.
	readInput := json.RawMessage(`{"path": "/tmp/a.go"}`)
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			llmResponseWithToolCall("Reading 1", "read_file", readInput),
			llmResponseWithToolCall("Reading 2", "read_file", readInput),
			llmResponseWithToolCall("Reading 3", "read_file", readInput),
			llmResponseFinish("Done", "Summary."),
		},
	}
	mockTools := newMockToolExecutor()
	mockTools.results["read_file"] = tools.ToolResult{Content: "content"}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	// No update_checklist in taskTools.
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "read_file", Description: "read", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true — checklist tool unavailable, gate should not activate")
	}
	for _, s := range result.Steps {
		if s.UserNudge == executorChecklistMissingNudge {
			t.Error("tool unavailable should not trigger checklist nudge")
		}
	}
}

func TestHasChecklistUpdate(t *testing.T) {
	exec := &Executor{}
	tests := []struct {
		name  string
		steps []Step
		want  bool
	}{
		{name: "no steps", steps: nil, want: false},
		{name: "only read_file", steps: []Step{{Action: llm.ToolCall{Name: "read_file"}}}, want: false},
		{name: "update_checklist present", steps: []Step{{Action: llm.ToolCall{Name: "update_checklist"}}}, want: true},
		{name: "failed update_checklist does not count", steps: []Step{{Action: llm.ToolCall{Name: "update_checklist"}, IsError: true}}, want: false},
		{name: "failed then successful update_checklist counts", steps: []Step{
			{Action: llm.ToolCall{Name: "update_checklist"}, IsError: true},
			{Action: llm.ToolCall{Name: "update_checklist"}, IsError: false},
		}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &runState{allSteps: tt.steps}
			if got := exec.hasChecklistUpdate(state); got != tt.want {
				t.Errorf("hasChecklistUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCountProductiveToolCalls(t *testing.T) {
	exec := &Executor{}
	tests := []struct {
		name  string
		steps []Step
		want  int
	}{
		{name: "no steps", steps: nil, want: 0},
		{name: "only finish", steps: []Step{{Action: llm.ToolCall{Name: "finish"}}}, want: 0},
		{name: "nudge step without action", steps: []Step{{UserNudge: "nudge"}}, want: 0},
		{name: "three read_file calls", steps: []Step{
			{Action: llm.ToolCall{Name: "read_file"}},
			{Action: llm.ToolCall{Name: "read_file"}},
			{Action: llm.ToolCall{Name: "read_file"}},
		}, want: 3},
		{name: "mix including finish and nudge", steps: []Step{
			{Action: llm.ToolCall{Name: "read_file"}},
			{UserNudge: "nudge"},
			{Action: llm.ToolCall{Name: "update_checklist"}},
			{Action: llm.ToolCall{Name: "finish"}},
		}, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &runState{allSteps: tt.steps}
			if got := exec.countProductiveToolCalls(state); got != tt.want {
				t.Errorf("countProductiveToolCalls() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestLastChecklistUnchecked(t *testing.T) {
	exec := &Executor{}
	tests := []struct {
		name  string
		steps []Step
		want  int
	}{
		{name: "no checklist", steps: nil, want: 0},
		{name: "only read_file", steps: []Step{{Action: llm.ToolCall{Name: "read_file"}, Observation: "content"}}, want: 0},
		{name: "all checked 2/2", steps: []Step{
			{Action: llm.ToolCall{Name: "update_checklist"}, Observation: "Checklist updated: 2/2 done."},
		}, want: 0},
		{name: "two unchecked 1/3", steps: []Step{
			{Action: llm.ToolCall{Name: "update_checklist"}, Observation: "Checklist updated: 1/3 done."},
		}, want: 2},
		{name: "step-scoped format", steps: []Step{
			{Action: llm.ToolCall{Name: "update_checklist"}, Observation: "Checklist updated for step_3: 0/5 done."},
		}, want: 5},
		{name: "last checklist wins", steps: []Step{
			{Action: llm.ToolCall{Name: "update_checklist"}, Observation: "Checklist updated: 1/3 done."},
			{Action: llm.ToolCall{Name: "read_file"}, Observation: "content"},
			{Action: llm.ToolCall{Name: "update_checklist"}, Observation: "Checklist updated: 3/3 done."},
		}, want: 0},
		{name: "failed checklist ignored", steps: []Step{
			{Action: llm.ToolCall{Name: "update_checklist"}, Observation: "invalid format", IsError: true},
		}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &runState{allSteps: tt.steps}
			if got := exec.lastChecklistUnchecked(state); got != tt.want {
				t.Errorf("lastChecklistUnchecked() = %d, want %d", got, tt.want)
			}
		})
	}
}
