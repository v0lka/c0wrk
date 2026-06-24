package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

func TestAskUserTool_Name(t *testing.T) {
	tool := NewAskUserTool(nil)
	if tool.Name() != "ask_user" {
		t.Errorf("expected name 'ask_user', got %q", tool.Name())
	}
}

func TestAskUserTool_DefaultPolicy(t *testing.T) {
	tool := NewAskUserTool(nil)
	if tool.DefaultPolicy() != sdktools.PolicyAlwaysAllow {
		t.Errorf("expected PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

func TestAskUserTool_Execute_SingleSelect(t *testing.T) {
	fn := func(_ context.Context, req AskUserRequest) (AskUserResponse, error) {
		return AskUserResponse{Answers: []AskUserAnswer{{ID: "q1", Selected: []string{"opt1"}}}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Pick one",
				"options":  []AskUserOption{{Label: "Option 1", Value: "opt1"}, {Label: "Option 2", Value: "opt2"}},
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
	fn := func(_ context.Context, req AskUserRequest) (AskUserResponse, error) {
		return AskUserResponse{Answers: []AskUserAnswer{{ID: "q1", Selected: []string{"opt1", "opt3"}}}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":           "q1",
				"question":     "Pick several",
				"multi_select": true,
				"options": []AskUserOption{
					{Label: "A", Value: "opt1"},
					{Label: "B", Value: "opt2"},
					{Label: "C", Value: "opt3"},
				},
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
	if !strings.Contains(result.Content, "User selected: opt1, opt3") {
		t.Errorf("expected content to contain 'User selected: opt1, opt3', got %q", result.Content)
	}
}

func TestAskUserTool_Execute_CustomText(t *testing.T) {
	fn := func(_ context.Context, req AskUserRequest) (AskUserResponse, error) {
		return AskUserResponse{Answers: []AskUserAnswer{{ID: "q1", CustomText: "my free text"}}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Explain",
				"options":  []AskUserOption{{Label: "Reason", Value: "r"}},
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
	if !strings.Contains(result.Content, "User answered: my free text") {
		t.Errorf("expected content to contain 'User answered: my free text', got %q", result.Content)
	}
}

func TestAskUserTool_Execute_SelectedAndCustom(t *testing.T) {
	fn := func(_ context.Context, req AskUserRequest) (AskUserResponse, error) {
		return AskUserResponse{Answers: []AskUserAnswer{{ID: "q1", Selected: []string{"opt1"}, CustomText: "also this"}}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Choose",
				"options":  []AskUserOption{{Label: "A", Value: "opt1"}},
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
	if !strings.Contains(result.Content, "User selected: opt1. Additional input: also this") {
		t.Errorf("expected 'User selected: opt1. Additional input: also this', got %q", result.Content)
	}
}

func TestAskUserTool_Execute_NilFunc(t *testing.T) {
	tool := NewAskUserTool(nil)
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Hello?",
				"options":  []AskUserOption{{Label: "A", Value: "a"}},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true when askFunc is nil")
	}
	if !strings.Contains(result.Content, "ask_user is not available") {
		t.Errorf("expected 'ask_user is not available', got %q", result.Content)
	}
}

func TestAskUserTool_Execute_InvalidJSON(t *testing.T) {
	tool := NewAskUserTool(func(_ context.Context, _ AskUserRequest) (AskUserResponse, error) {
		return AskUserResponse{}, nil
	})
	result, err := tool.Execute(context.Background(), []byte("not-json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for invalid JSON")
	}
}

func TestAskUserTool_Execute_EmptyQuestion(t *testing.T) {
	fn := func(_ context.Context, req AskUserRequest) (AskUserResponse, error) {
		return AskUserResponse{}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "   ",
				"options":  []AskUserOption{{Label: "A", Value: "a"}},
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
	if !strings.Contains(result.Content, `question "q1" has empty text`) {
		t.Errorf("expected validation error about empty question, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_NoOptions(t *testing.T) {
	fn := func(_ context.Context, req AskUserRequest) (AskUserResponse, error) {
		return AskUserResponse{}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Hello?",
				"options":  []AskUserOption{},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for no options")
	}
	if !strings.Contains(result.Content, `question "q1" must have at least one option`) {
		t.Errorf("expected validation error about missing options, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_FuncError(t *testing.T) {
	fn := func(_ context.Context, req AskUserRequest) (AskUserResponse, error) {
		return AskUserResponse{}, errors.New("connection lost")
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Hello?",
				"options":  []AskUserOption{{Label: "A", Value: "a"}},
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
	fn := func(_ context.Context, req AskUserRequest) (AskUserResponse, error) {
		return AskUserResponse{Answers: []AskUserAnswer{{ID: "q1"}}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Hello?",
				"options":  []AskUserOption{{Label: "A", Value: "a"}},
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
	fn := func(_ context.Context, req AskUserRequest) (AskUserResponse, error) {
		return AskUserResponse{
			Answers: []AskUserAnswer{
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
				"options":  []AskUserOption{{Label: "A", Value: "opt1"}, {Label: "B", Value: "opt2"}},
			},
			{
				"id":       "q2",
				"question": "Proceed?",
				"options":  []AskUserOption{{Label: "Yes", Value: "yes"}, {Label: "No", Value: "no"}},
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
	fn := func(_ context.Context, req AskUserRequest) (AskUserResponse, error) {
		return AskUserResponse{
			Answers: []AskUserAnswer{
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
				"options":  []AskUserOption{{Label: "A", Value: "opt1"}, {Label: "B", Value: "opt2"}},
			},
			{
				"id":       "q2",
				"question": "Explain why",
				"options":  []AskUserOption{{Label: "Reason", Value: "r"}},
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
