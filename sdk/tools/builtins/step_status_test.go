package builtins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/user/agent/sdk/agent"
)

// ---------------------------------------------------------------------------
// parseAndValidateTodoList
// ---------------------------------------------------------------------------

func TestParseAndValidateTodoList_Valid(t *testing.T) {
	input := "- [ ] Task one\n- [x] Task two\n- [ ] Task three"
	result := parseAndValidateTodoList(input)
	if !result.Valid {
		t.Fatalf("expected valid, got error: %s", result.Error)
	}
	if len(result.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result.Items))
	}
	if result.Items[0].Text != "Task one" || result.Items[0].Checked {
		t.Errorf("item 0 mismatch: %+v", result.Items[0])
	}
	if result.Items[1].Text != "Task two" || !result.Items[1].Checked {
		t.Errorf("item 1 mismatch: %+v", result.Items[1])
	}
	if result.Items[2].Text != "Task three" || result.Items[2].Checked {
		t.Errorf("item 2 mismatch: %+v", result.Items[2])
	}
}

func TestParseAndValidateTodoList_Empty(t *testing.T) {
	result := parseAndValidateTodoList("")
	if result.Valid {
		t.Fatal("expected invalid for empty input")
	}
	if !strings.Contains(result.Error, "empty") {
		t.Errorf("expected empty error, got %q", result.Error)
	}
}

func TestParseAndValidateTodoList_Nested(t *testing.T) {
	input := "- [ ] Parent\n  - [ ] Child"
	result := parseAndValidateTodoList(input)
	if result.Valid {
		t.Fatal("expected invalid for nested list")
	}
	if !strings.Contains(result.Error, "nested") {
		t.Errorf("expected nested error, got %q", result.Error)
	}
}

func TestParseAndValidateTodoList_InvalidCheckbox(t *testing.T) {
	input := "- [ ] Valid\n- [*] Invalid\n- plain bullet"
	result := parseAndValidateTodoList(input)
	if result.Valid {
		t.Fatal("expected invalid for bad checkbox formats")
	}
	if !strings.Contains(result.Error, "invalid checkbox format") {
		t.Errorf("expected checkbox format error, got %q", result.Error)
	}
}

func TestParseAndValidateTodoList_UnicodeCheckbox(t *testing.T) {
	input := "- [ ] Valid\n- ☑ Unicode"
	result := parseAndValidateTodoList(input)
	if result.Valid {
		t.Fatal("expected invalid for unicode checkbox")
	}
}

func TestParseAndValidateTodoList_BlankLinesIgnored(t *testing.T) {
	input := "- [ ] Task one\n\n- [x] Task two\n"
	result := parseAndValidateTodoList(input)
	if !result.Valid {
		t.Fatalf("expected valid, got error: %s", result.Error)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Items))
	}
}

// ---------------------------------------------------------------------------
// SetStepStatusTool.Execute
// ---------------------------------------------------------------------------

func TestSetStepStatusTool_ExecuteValid(t *testing.T) {
	var capturedStepID string
	var capturedItems []agent.TodoItem

	ctx := context.Background()
	ctx = agent.WithStepID(ctx, "step_42")
	ctx = agent.WithStepTodoUpdateFunc(ctx, func(stepID string, items []agent.TodoItem) {
		capturedStepID = stepID
		capturedItems = items
	})

	tool := NewSetStepStatusTool()
	input, _ := json.Marshal(SetStepStatusInput{TodoList: "- [ ] A\n- [x] B"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if capturedStepID != "step_42" {
		t.Errorf("expected step_id step_42, got %q", capturedStepID)
	}
	if len(capturedItems) != 2 {
		t.Fatalf("expected 2 items, got %d", len(capturedItems))
	}
	if !strings.Contains(result.Content, "step_42") {
		t.Errorf("result should mention step_id, got %q", result.Content)
	}
}

func TestSetStepStatusTool_ExecuteNoStepID(t *testing.T) {
	ctx := context.Background()
	tool := NewSetStepStatusTool()
	input, _ := json.Marshal(SetStepStatusInput{TodoList: "- [ ] A\n- [x] B"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, "no active plan step") {
		t.Errorf("expected no-active-step message, got %q", result.Content)
	}
}

func TestSetStepStatusTool_ExecuteNoUpdateFunc(t *testing.T) {
	ctx := context.Background()
	ctx = agent.WithStepID(ctx, "step_1")
	tool := NewSetStepStatusTool()
	input, _ := json.Marshal(SetStepStatusInput{TodoList: "- [ ] A"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, "step_1") {
		t.Errorf("result should mention step_id, got %q", result.Content)
	}
}

func TestSetStepStatusTool_ExecuteInvalidFormat(t *testing.T) {
	ctx := context.Background()
	ctx = agent.WithStepID(ctx, "step_1")
	tool := NewSetStepStatusTool()
	input, _ := json.Marshal(SetStepStatusInput{TodoList: "bad format"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid format")
	}
	if !strings.Contains(result.Content, "Invalid to-do list format") {
		t.Errorf("expected format error message, got %q", result.Content)
	}
}

func TestSetStepStatusTool_ExecuteInvalidJSON(t *testing.T) {
	tool := NewSetStepStatusTool()
	result, err := tool.Execute(context.Background(), []byte(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid JSON")
	}
}
