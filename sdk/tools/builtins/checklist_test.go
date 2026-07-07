package builtins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/agent"
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
// UpdateChecklistTool.Execute
// ---------------------------------------------------------------------------

func TestUpdateChecklistTool_ExecuteValid(t *testing.T) {
	var capturedStepID string
	var capturedItems []agent.TodoItem

	ctx := context.Background()
	ctx = agent.WithStepID(ctx, "step_42")
	ctx = agent.WithStepTodoUpdateFunc(ctx, func(stepID string, items []agent.TodoItem) {
		capturedStepID = stepID
		capturedItems = items
	})

	tool := NewUpdateChecklistTool()
	input, _ := json.Marshal(UpdateChecklistInput{TodoList: "- [ ] A\n- [x] B"})

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

func TestUpdateChecklistTool_ExecuteStandaloneNoStepID(t *testing.T) {
	var capturedStepID string
	var capturedItems []agent.TodoItem

	ctx := context.Background()
	ctx = agent.WithStepTodoUpdateFunc(ctx, func(stepID string, items []agent.TodoItem) {
		capturedStepID = stepID
		capturedItems = items
	})

	tool := NewUpdateChecklistTool()
	input, _ := json.Marshal(UpdateChecklistInput{TodoList: "- [ ] A\n- [x] B"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	// Standalone: stepID is empty, but the callback is still invoked so the
	// UI can render a standalone checklist card.
	if capturedStepID != "" {
		t.Errorf("expected empty step_id for standalone, got %q", capturedStepID)
	}
	if len(capturedItems) != 2 {
		t.Fatalf("expected 2 items, got %d", len(capturedItems))
	}
	if strings.Contains(result.Content, "step") {
		t.Errorf("standalone result should not mention step, got %q", result.Content)
	}
}

func TestUpdateChecklistTool_ExecuteWithExplicitStepID(t *testing.T) {
	var capturedStepID string
	var capturedItems []agent.TodoItem

	// No step ID in context — the explicit step_id parameter should be used instead.
	ctx := context.Background()
	ctx = agent.WithStepTodoUpdateFunc(ctx, func(stepID string, items []agent.TodoItem) {
		capturedStepID = stepID
		capturedItems = items
	})

	tool := NewUpdateChecklistTool()
	input, _ := json.Marshal(UpdateChecklistInput{TodoList: "- [ ] A\n- [x] B", StepID: "step_99"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if capturedStepID != "step_99" {
		t.Errorf("expected step_id 'step_99', got %q", capturedStepID)
	}
	if len(capturedItems) != 2 {
		t.Fatalf("expected 2 items, got %d", len(capturedItems))
	}
	if !strings.Contains(result.Content, "step_99") {
		t.Errorf("result should mention step_id, got %q", result.Content)
	}
}

func TestUpdateChecklistTool_ExecuteExplicitStepIDOverridesContext(t *testing.T) {
	var capturedStepID string

	// Context has step_42, but explicit step_id parameter should win.
	ctx := context.Background()
	ctx = agent.WithStepID(ctx, "step_42")
	ctx = agent.WithStepTodoUpdateFunc(ctx, func(stepID string, items []agent.TodoItem) {
		capturedStepID = stepID
	})

	tool := NewUpdateChecklistTool()
	input, _ := json.Marshal(UpdateChecklistInput{TodoList: "- [ ] A", StepID: "step_override"})

	_, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedStepID != "step_override" {
		t.Errorf("expected step_id 'step_override' to override context, got %q", capturedStepID)
	}
}

func TestUpdateChecklistTool_ExecuteNoUpdateFunc(t *testing.T) {
	ctx := context.Background()
	ctx = agent.WithStepID(ctx, "step_1")
	tool := NewUpdateChecklistTool()
	input, _ := json.Marshal(UpdateChecklistInput{TodoList: "- [ ] A"})

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

func TestUpdateChecklistTool_ExecuteStandaloneNoUpdateFunc(t *testing.T) {
	ctx := context.Background()
	tool := NewUpdateChecklistTool()
	input, _ := json.Marshal(UpdateChecklistInput{TodoList: "- [ ] A\n- [x] B"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	// No update func, no step ID — should still succeed (standalone, headless).
	if !strings.Contains(result.Content, "Checklist updated") {
		t.Errorf("result should mention checklist updated, got %q", result.Content)
	}
}

func TestUpdateChecklistTool_ExecuteInvalidFormat(t *testing.T) {
	ctx := context.Background()
	ctx = agent.WithStepID(ctx, "step_1")
	tool := NewUpdateChecklistTool()
	input, _ := json.Marshal(UpdateChecklistInput{TodoList: "bad format"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid format")
	}
	if !strings.Contains(result.Content, "Invalid checklist format") {
		t.Errorf("expected format error message, got %q", result.Content)
	}
}

func TestUpdateChecklistTool_ExecuteInvalidJSON(t *testing.T) {
	tool := NewUpdateChecklistTool()
	result, err := tool.Execute(context.Background(), []byte(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid JSON")
	}
}

func TestUpdateChecklistTool_GuardRejectsStandaloneWhenPlanDeclared(t *testing.T) {
	ctx := context.Background()
	// Guard simulates "plan declared, standalone checklist rejected".
	ctx = agent.WithChecklistGuard(ctx, func(stepID string) string {
		if stepID == "" {
			return "a plan has been declared; pass step_id"
		}
		return ""
	})
	tool := NewUpdateChecklistTool()
	input, _ := json.Marshal(UpdateChecklistInput{TodoList: "- [ ] A\n- [ ] B"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result — guard should reject standalone checklist")
	}
	if !strings.Contains(result.Content, "step_id") {
		t.Errorf("guard rejection message should mention step_id, got %q", result.Content)
	}
}

func TestUpdateChecklistTool_GuardAllowsStepScopedWhenPlanDeclared(t *testing.T) {
	ctx := context.Background()
	ctx = agent.WithChecklistGuard(ctx, func(stepID string) string {
		if stepID == "" {
			return "a plan has been declared; pass step_id"
		}
		return ""
	})
	ctx = agent.WithStepID(ctx, "step_1")
	tool := NewUpdateChecklistTool()
	input, _ := json.Marshal(UpdateChecklistInput{TodoList: "- [ ] A\n- [ ] B"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("guard should allow step-scoped checklist, got error: %s", result.Content)
	}
}

func TestUpdateChecklistTool_GuardNotSet_AcceptsStandalone(t *testing.T) {
	// No guard in context — standalone checklist should be accepted.
	ctx := context.Background()
	tool := NewUpdateChecklistTool()
	input, _ := json.Marshal(UpdateChecklistInput{TodoList: "- [ ] A"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success without guard, got: %s", result.Content)
	}
}

func TestUpdateChecklistTool_ResultContainsReminderWhenUnchecked(t *testing.T) {
	ctx := context.Background()
	tool := NewUpdateChecklistTool()
	input, _ := json.Marshal(UpdateChecklistInput{TodoList: "- [x] A\n- [ ] B"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "1/2 done") {
		t.Errorf("result should show 1/2 done, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "Remember to call update_checklist again") {
		t.Errorf("result should contain incremental-update reminder, got %q", result.Content)
	}
}

func TestUpdateChecklistTool_ResultNoReminderWhenAllChecked(t *testing.T) {
	ctx := context.Background()
	tool := NewUpdateChecklistTool()
	input, _ := json.Marshal(UpdateChecklistInput{TodoList: "- [x] A\n- [x] B"})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if strings.Contains(result.Content, "Remember to call") {
		t.Errorf("all-checked checklist should not contain reminder, got %q", result.Content)
	}
}
