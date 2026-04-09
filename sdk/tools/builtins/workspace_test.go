package builtins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/tools"
)

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
	tool := NewReadStepOutputTool()
	ws := agent.NewSharedWorkspace()
	ws.Store("step_1/output", "full output content from step 1", "step_1")

	ctx := agent.WithSharedWorkspace(context.Background(), ws)
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
	tool := NewReadStepOutputTool()
	ws := agent.NewSharedWorkspace()
	ws.Store("step_1/output", "content", "step_1")

	ctx := agent.WithSharedWorkspace(context.Background(), ws)
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

func TestReadStepOutputTool_WorkspaceNotInContext(t *testing.T) {
	tool := NewReadStepOutputTool()
	// No workspace in context
	input, _ := json.Marshal(ReadStepOutputInput{StepID: "step_1"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true when workspace not in context")
	}
	if result.Content != "Workspace not available" {
		t.Errorf("expected 'Workspace not available', got: %s", result.Content)
	}
}

func TestReadStepOutputTool_InvalidJSON(t *testing.T) {
	tool := NewReadStepOutputTool()
	ws := agent.NewSharedWorkspace()
	ctx := agent.WithSharedWorkspace(context.Background(), ws)

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

func TestListStepOutputsTool_HappyPath(t *testing.T) {
	tool := NewListStepOutputsTool()
	ws := agent.NewSharedWorkspace()
	ws.Store("step_1/output", "output from step 1", "step_1")
	ws.Store("step_2/output", "output from step 2", "step_2")

	ctx := agent.WithSharedWorkspace(context.Background(), ws)
	input, _ := json.Marshal(ListStepOutputsInput{})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "step_1:") {
		t.Errorf("expected result to contain step_1, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "step_2:") {
		t.Errorf("expected result to contain step_2, got: %s", result.Content)
	}
}

func TestListStepOutputsTool_EmptyWorkspace(t *testing.T) {
	tool := NewListStepOutputsTool()
	ws := agent.NewSharedWorkspace()

	ctx := agent.WithSharedWorkspace(context.Background(), ws)
	input, _ := json.Marshal(ListStepOutputsInput{})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error for empty workspace, got: %s", result.Content)
	}
	if result.Content != "No step outputs available yet" {
		t.Errorf("expected 'No step outputs available yet', got: %s", result.Content)
	}
}

func TestListStepOutputsTool_WorkspaceNotInContext(t *testing.T) {
	tool := NewListStepOutputsTool()
	// No workspace in context
	input, _ := json.Marshal(ListStepOutputsInput{})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true when workspace not in context")
	}
	if result.Content != "Workspace not available" {
		t.Errorf("expected 'Workspace not available', got: %s", result.Content)
	}
}

func TestListStepOutputsTool_PreviewTruncation(t *testing.T) {
	tool := NewListStepOutputsTool()
	ws := agent.NewSharedWorkspace()
	longContent := strings.Repeat("a", 300)
	ws.Store("step_1/output", longContent, "step_1")

	ctx := agent.WithSharedWorkspace(context.Background(), ws)
	input, _ := json.Marshal(ListStepOutputsInput{})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "...") {
		t.Errorf("expected preview to be truncated with ..., got: %s", result.Content)
	}
	// Should be around 200 chars + "..."
	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	// Check that the line contains the step ID and preview
	if !strings.HasPrefix(lines[0], "- step_1:") {
		t.Errorf("expected line to start with '- step_1:', got: %s", lines[0])
	}
}

func TestListStepOutputsTool_NewlinesReplaced(t *testing.T) {
	tool := NewListStepOutputsTool()
	ws := agent.NewSharedWorkspace()
	ws.Store("step_1/output", "line 1\nline 2\nline 3", "step_1")

	ctx := agent.WithSharedWorkspace(context.Background(), ws)
	input, _ := json.Marshal(ListStepOutputsInput{})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}
	// Newlines should be replaced with spaces
	if strings.Contains(result.Content, "\n") && !strings.HasSuffix(result.Content, "\n") {
		// The content itself should not have newlines (except possibly trailing)
		contentPart := strings.TrimPrefix(result.Content, "- step_1: ")
		contentPart = strings.TrimSpace(contentPart)
		if strings.Contains(contentPart, "\n") {
			t.Errorf("expected newlines to be replaced with spaces, got: %s", result.Content)
		}
	}
}

func TestListStepOutputsTool_InvalidJSON(t *testing.T) {
	tool := NewListStepOutputsTool()
	ws := agent.NewSharedWorkspace()
	ctx := agent.WithSharedWorkspace(context.Background(), ws)

	result, err := tool.Execute(ctx, json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for invalid JSON")
	}
}
