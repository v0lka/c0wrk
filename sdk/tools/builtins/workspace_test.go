package builtins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/tools"
)

// mockStepOutputStore implements agent.StepOutputStore for testing.
type mockStepOutputStore struct {
	entries []agent.StepOutputEntry
}

func (m *mockStepOutputStore) GetStepOutput(stepID string) (string, bool) {
	for _, e := range m.entries {
		if e.StepID == stepID {
			return e.FullOutput, true
		}
	}
	return "", false
}

func (m *mockStepOutputStore) ListStepOutputs() []agent.StepOutputEntry {
	return m.entries
}

func ctxWithStepOutputStore(store agent.StepOutputStore) context.Context {
	return agent.WithStepOutputStore(context.Background(), store)
}

func TestReadStepOutputTool_Name(t *testing.T) {
	tool := NewReadStepOutputTool()
	if tool.Name() != "read_step_output" {
		t.Errorf("expected Name() = %q, got %q", "read_step_output", tool.Name())
	}
}

func TestReadStepOutputTool_DefaultPolicy(t *testing.T) {
	tool := NewReadStepOutputTool()
	if tool.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected DefaultPolicy() = PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

func TestReadStepOutputTool_HappyPath(t *testing.T) {
	store := &mockStepOutputStore{
		entries: []agent.StepOutputEntry{
			{StepID: "step_1", FullOutput: "full output content from step 1"},
		},
	}
	ctx := ctxWithStepOutputStore(store)
	tool := NewReadStepOutputTool()
	input, _ := json.Marshal(ReadStepOutputInput{StepID: "step_1"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}
	if result.Content != "full output content from step 1" {
		t.Errorf("expected content %q, got %q", "full output content from step 1", result.Content)
	}
}

func TestReadStepOutputTool_StepNotFound(t *testing.T) {
	store := &mockStepOutputStore{
		entries: []agent.StepOutputEntry{
			{StepID: "step_1", FullOutput: "content"},
		},
	}
	ctx := ctxWithStepOutputStore(store)
	tool := NewReadStepOutputTool()
	input, _ := json.Marshal(ReadStepOutputInput{StepID: "step_2"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for non-existent step")
	}
	if !strings.Contains(result.Content, "No output found for step: step_2") {
		t.Errorf("expected error message about step not found, got: %s", result.Content)
	}
}

func TestReadStepOutputTool_StoreNotInContext(t *testing.T) {
	tool := NewReadStepOutputTool()
	input, _ := json.Marshal(ReadStepOutputInput{StepID: "step_1"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true when store not in context")
	}
	if !strings.Contains(result.Content, "not available") {
		t.Errorf("expected 'not available' message, got: %s", result.Content)
	}
}

func TestReadStepOutputTool_InvalidJSON(t *testing.T) {
	store := &mockStepOutputStore{}
	ctx := ctxWithStepOutputStore(store)
	tool := NewReadStepOutputTool()

	result, err := tool.Execute(ctx, json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for invalid JSON")
	}
}

func TestListStepOutputsTool_Name(t *testing.T) {
	tool := NewListStepOutputsTool()
	if tool.Name() != "list_step_outputs" {
		t.Errorf("expected Name() = %q, got %q", "list_step_outputs", tool.Name())
	}
}

func TestListStepOutputsTool_DefaultPolicy(t *testing.T) {
	tool := NewListStepOutputsTool()
	if tool.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected DefaultPolicy() = PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

func TestListStepOutputsTool_EmptyStore(t *testing.T) {
	store := &mockStepOutputStore{}
	ctx := ctxWithStepOutputStore(store)
	tool := NewListStepOutputsTool()
	input, _ := json.Marshal(ListStepOutputsInput{})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "No step outputs available yet") {
		t.Errorf("expected 'No step outputs available yet', got: %s", result.Content)
	}
}

func TestListStepOutputsTool_WithEntries(t *testing.T) {
	store := &mockStepOutputStore{
		entries: []agent.StepOutputEntry{
			{StepID: "step_1", FullOutput: "output from step one"},
			{StepID: "step_2", FullOutput: "output from step two"},
		},
	}
	ctx := ctxWithStepOutputStore(store)
	tool := NewListStepOutputsTool()
	input, _ := json.Marshal(ListStepOutputsInput{})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "step_1") || !strings.Contains(result.Content, "step_2") {
		t.Errorf("expected both step IDs in output, got: %s", result.Content)
	}
}

func TestListStepOutputsTool_StoreNotInContext(t *testing.T) {
	tool := NewListStepOutputsTool()
	input, _ := json.Marshal(ListStepOutputsInput{})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true when store not in context")
	}
	if !strings.Contains(result.Content, "not available") {
		t.Errorf("expected 'not available' message, got: %s", result.Content)
	}
}

func TestListStepOutputsTool_InvalidJSON(t *testing.T) {
	store := &mockStepOutputStore{}
	ctx := ctxWithStepOutputStore(store)
	tool := NewListStepOutputsTool()

	result, err := tool.Execute(ctx, json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for invalid JSON")
	}
}
