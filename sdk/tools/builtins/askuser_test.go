package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/sdk/tools"
)

func TestAskUserTool_Name(t *testing.T) {
	tool := NewAskUserTool(nil)
	if tool.Name() != "ask_user" {
		t.Errorf("expected name 'ask_user', got %q", tool.Name())
	}
}

func TestAskUserTool_DefaultPolicy(t *testing.T) {
	tool := NewAskUserTool(nil)
	if tool.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

func TestAskUserTool_Execute_SingleSelect(t *testing.T) {
	fn := func(_ context.Context, req tools.AskUserRequest) (tools.AskUserResponse, error) {
		return tools.AskUserResponse{Answers: []tools.AskUserAnswer{{ID: "q1", Selected: []string{"opt1"}}}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Pick one",
				"options":  []tools.AskUserOption{{Label: "Option 1", Value: "opt1"}, {Label: "Option 2", Value: "opt2"}},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected IsError=false, got true. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "User selected: opt1") {
		t.Errorf("expected content to contain 'User selected: opt1', got %q", result.Content)
	}
}

func TestAskUserTool_Execute_MultiSelect(t *testing.T) {
	fn := func(_ context.Context, req tools.AskUserRequest) (tools.AskUserResponse, error) {
		return tools.AskUserResponse{Answers: []tools.AskUserAnswer{{ID: "q1", Selected: []string{"opt1", "opt2"}}}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":           "q1",
				"question":     "Pick many",
				"options":      []tools.AskUserOption{{Label: "A", Value: "opt1"}, {Label: "B", Value: "opt2"}},
				"multi_select": true,
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected IsError=false, got true. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "User selected: opt1, opt2") {
		t.Errorf("expected content to contain 'User selected: opt1, opt2', got %q", result.Content)
	}
}

func TestAskUserTool_Execute_CustomText(t *testing.T) {
	fn := func(_ context.Context, req tools.AskUserRequest) (tools.AskUserResponse, error) {
		return tools.AskUserResponse{Answers: []tools.AskUserAnswer{{ID: "q1", CustomText: "my custom answer"}}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "What do you think?",
				"options":  []tools.AskUserOption{{Label: "A", Value: "a"}},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected IsError=false, got true. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "User answered: my custom answer") {
		t.Errorf("expected content to contain 'User answered: my custom answer', got %q", result.Content)
	}
}

func TestAskUserTool_Execute_SelectedAndCustom(t *testing.T) {
	fn := func(_ context.Context, req tools.AskUserRequest) (tools.AskUserResponse, error) {
		return tools.AskUserResponse{Answers: []tools.AskUserAnswer{{ID: "q1", Selected: []string{"opt1"}, CustomText: "extra info"}}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Pick and explain",
				"options":  []tools.AskUserOption{{Label: "A", Value: "opt1"}},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected IsError=false, got true. Content: %s", result.Content)
	}
	expected := "Q \"Pick and explain\" → User selected: opt1. Additional input: extra info"
	if result.Content != expected {
		t.Errorf("expected %q, got %q", expected, result.Content)
	}
}

func TestAskUserTool_Execute_NilFunc(t *testing.T) {
	tool := NewAskUserTool(nil)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Hello?",
				"options":  []tools.AskUserOption{{Label: "A", Value: "a"}},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for nil askFunc")
	}
	if !strings.Contains(result.Content, "not available") {
		t.Errorf("expected error about not available, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_InvalidInput(t *testing.T) {
	tool := NewAskUserTool(nil)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{invalid json`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for invalid JSON")
	}
	if !strings.Contains(result.Content, "failed to parse input") {
		t.Errorf("expected parse error message, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_EmptyQuestion(t *testing.T) {
	tool := NewAskUserTool(nil)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "",
				"options":  []tools.AskUserOption{{Label: "A", Value: "a"}},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for empty question")
	}
	if !strings.Contains(result.Content, "has empty text") {
		t.Errorf("expected validation error about question, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_EmptyOptions(t *testing.T) {
	tool := NewAskUserTool(nil)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Hello?",
				"options":  []tools.AskUserOption{},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for empty options")
	}
	if !strings.Contains(result.Content, "must have at least one option") {
		t.Errorf("expected validation error about options, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_FuncError(t *testing.T) {
	fn := func(_ context.Context, req tools.AskUserRequest) (tools.AskUserResponse, error) {
		return tools.AskUserResponse{}, errors.New("connection lost")
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Hello?",
				"options":  []tools.AskUserOption{{Label: "A", Value: "a"}},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true when askFunc returns error")
	}
	if !strings.Contains(result.Content, "connection lost") {
		t.Errorf("expected error content to contain 'connection lost', got %q", result.Content)
	}
}

func TestAskUserTool_Execute_NoAnswer(t *testing.T) {
	fn := func(_ context.Context, req tools.AskUserRequest) (tools.AskUserResponse, error) {
		return tools.AskUserResponse{Answers: []tools.AskUserAnswer{{ID: "q1"}}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Hello?",
				"options":  []tools.AskUserOption{{Label: "A", Value: "a"}},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected IsError=false, got true. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "User provided no answer") {
		t.Errorf("expected content to contain 'User provided no answer', got %q", result.Content)
	}
}

func TestAskUserTool_Execute_EmptyQuestionsArray(t *testing.T) {
	tool := NewAskUserTool(nil)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for empty questions array")
	}
	if !strings.Contains(result.Content, "questions array must not be empty") {
		t.Errorf("expected validation error about empty questions array, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_MultipleQuestions(t *testing.T) {
	fn := func(_ context.Context, req tools.AskUserRequest) (tools.AskUserResponse, error) {
		return tools.AskUserResponse{
			Answers: []tools.AskUserAnswer{
				{ID: "q1", Selected: []string{"opt1"}},
				{ID: "q2", Selected: []string{"yes"}},
			},
		}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Pick one",
				"options":  []tools.AskUserOption{{Label: "A", Value: "opt1"}, {Label: "B", Value: "opt2"}},
			},
			{
				"id":       "q2",
				"question": "Proceed?",
				"options":  []tools.AskUserOption{{Label: "Yes", Value: "yes"}, {Label: "No", Value: "no"}},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected IsError=false, got true. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "User selected: opt1") {
		t.Errorf("expected content to contain 'User selected: opt1', got %q", result.Content)
	}
	if !strings.Contains(result.Content, "User selected: yes") {
		t.Errorf("expected content to contain 'User selected: yes', got %q", result.Content)
	}
}

func TestAskUserTool_Execute_MixedResponses(t *testing.T) {
	fn := func(_ context.Context, req tools.AskUserRequest) (tools.AskUserResponse, error) {
		return tools.AskUserResponse{
			Answers: []tools.AskUserAnswer{
				{ID: "q1", Selected: []string{"opt1"}},
				{ID: "q2", CustomText: "my custom answer"},
			},
		}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Pick one",
				"options":  []tools.AskUserOption{{Label: "A", Value: "opt1"}, {Label: "B", Value: "opt2"}},
			},
			{
				"id":       "q2",
				"question": "Explain why",
				"options":  []tools.AskUserOption{{Label: "Reason", Value: "r"}},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected IsError=false, got true. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "User selected: opt1") {
		t.Errorf("expected content to contain 'User selected: opt1', got %q", result.Content)
	}
	if !strings.Contains(result.Content, "User answered: my custom answer") {
		t.Errorf("expected content to contain 'User answered: my custom answer', got %q", result.Content)
	}
}
