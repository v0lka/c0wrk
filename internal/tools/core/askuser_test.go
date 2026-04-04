package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/user/agent/internal/tools"
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
		return tools.AskUserResponse{Selected: []string{"opt1"}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"question": "Pick one",
		"options":  []tools.AskUserOption{{Label: "Option 1", Value: "opt1"}, {Label: "Option 2", Value: "opt2"}},
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
		return tools.AskUserResponse{Selected: []string{"opt1", "opt2"}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"question":     "Pick many",
		"options":      []tools.AskUserOption{{Label: "A", Value: "opt1"}, {Label: "B", Value: "opt2"}},
		"multi_select": true,
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
		return tools.AskUserResponse{CustomText: "my custom answer"}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"question": "What do you think?",
		"options":  []tools.AskUserOption{{Label: "A", Value: "a"}},
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
		return tools.AskUserResponse{Selected: []string{"opt1"}, CustomText: "extra info"}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"question": "Pick and explain",
		"options":  []tools.AskUserOption{{Label: "A", Value: "opt1"}},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected IsError=false, got true. Content: %s", result.Content)
	}
	expected := "User selected: opt1. Additional input: extra info"
	if result.Content != expected {
		t.Errorf("expected %q, got %q", expected, result.Content)
	}
}

func TestAskUserTool_Execute_NilFunc(t *testing.T) {
	tool := NewAskUserTool(nil)

	input, _ := json.Marshal(map[string]any{
		"question": "Hello?",
		"options":  []tools.AskUserOption{{Label: "A", Value: "a"}},
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
		"question": "",
		"options":  []tools.AskUserOption{{Label: "A", Value: "a"}},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for empty question")
	}
	if !strings.Contains(result.Content, "question must not be empty") {
		t.Errorf("expected validation error about question, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_EmptyOptions(t *testing.T) {
	tool := NewAskUserTool(nil)

	input, _ := json.Marshal(map[string]any{
		"question": "Hello?",
		"options":  []tools.AskUserOption{},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for empty options")
	}
	if !strings.Contains(result.Content, "options must have at least one entry") {
		t.Errorf("expected validation error about options, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_FuncError(t *testing.T) {
	fn := func(_ context.Context, req tools.AskUserRequest) (tools.AskUserResponse, error) {
		return tools.AskUserResponse{}, errors.New("connection lost")
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"question": "Hello?",
		"options":  []tools.AskUserOption{{Label: "A", Value: "a"}},
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
		return tools.AskUserResponse{}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"question": "Hello?",
		"options":  []tools.AskUserOption{{Label: "A", Value: "a"}},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected IsError=false, got true. Content: %s", result.Content)
	}
	if result.Content != "User provided no answer" {
		t.Errorf("expected 'User provided no answer', got %q", result.Content)
	}
}
