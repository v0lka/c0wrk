package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
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

// TestAskUserTool_Execute_UnavailableContent pins the exact wording the silent
// mode relies on: a tool registered with a nil callback — how silent mode's
// ask_user sub-policy registers it — must return the explicit "not available"
// result and never block the agent.
func TestAskUserTool_Execute_UnavailableContent(t *testing.T) {
	tool := NewAskUserTool(nil)
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Proceed?",
				"options":  []map[string]any{{"label": "Yes", "value": "yes"}},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true, got %+v", result)
	}
	if result.Content != askUserUnavailableContent {
		t.Errorf("expected the unavailable result %q, got %q", askUserUnavailableContent, result.Content)
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

// --- New validation: option values & question IDs ---

// noopAskFunc never returns; validation tests fail before the callback runs.
func noopAskFunc(_ context.Context, _ AskUserRequest) (AskUserResponse, error) {
	return AskUserResponse{}, nil
}

func TestAskUserTool_Execute_EmptyOptionValue(t *testing.T) {
	tool := NewAskUserTool(noopAskFunc)
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Pick one",
				"options": []map[string]any{
					{"label": "A", "value": ""},
					{"label": "B", "value": "b"},
				},
			},
		},
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true for empty option value, got false. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "empty value") {
		t.Errorf("expected 'empty value' in validation error, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_MissingOptionValue(t *testing.T) {
	tool := NewAskUserTool(noopAskFunc)
	// Model omits "value" entirely on every option → Go defaults to "".
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Pick one",
				"options": []map[string]any{
					{"label": "Option A"},
					{"label": "Option B"},
				},
			},
		},
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true for missing option value, got false. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "empty value") {
		t.Errorf("expected 'empty value' in validation error, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_DuplicateOptionValues(t *testing.T) {
	tool := NewAskUserTool(noopAskFunc)
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Pick one",
				"options": []map[string]any{
					{"label": "A", "value": "same"},
					{"label": "B", "value": "same"},
				},
			},
		},
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true for duplicate option values, got false. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "duplicate option value") {
		t.Errorf("expected 'duplicate option value' in validation error, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_DuplicateQuestionIDs(t *testing.T) {
	tool := NewAskUserTool(noopAskFunc)
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "First",
				"options":  []map[string]any{{"label": "A", "value": "a"}},
			},
			{
				"id":       "q1",
				"question": "Second",
				"options":  []map[string]any{{"label": "B", "value": "b"}},
			},
		},
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true for duplicate question id, got false. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "duplicate question id") {
		t.Errorf("expected 'duplicate question id' in validation error, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_EmptyQuestionID(t *testing.T) {
	tool := NewAskUserTool(noopAskFunc)
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "",
				"question": "No id",
				"options":  []map[string]any{{"label": "A", "value": "a"}},
			},
		},
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true for empty question id, got false. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "empty id") {
		t.Errorf("expected 'empty id' in validation error, got %q", result.Content)
	}
}

func TestAskUserTool_Execute_EmptyOptionLabel(t *testing.T) {
	tool := NewAskUserTool(noopAskFunc)
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "Pick one",
				"options": []map[string]any{
					{"label": "", "value": "a"},
					{"label": "B", "value": "b"},
				},
			},
		},
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true for empty option label, got false. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "empty label") {
		t.Errorf("expected 'empty label' in validation error, got %q", result.Content)
	}
}

// --- ADR-133: option-level description hints; recommended stays question-level ---

func TestAskUserTool_Execute_OptionDescriptionAccepted(t *testing.T) {
	var got AskUserRequest
	fn := func(_ context.Context, req AskUserRequest) (AskUserResponse, error) {
		got = req
		return AskUserResponse{Answers: []AskUserAnswer{{ID: "q1", Selected: []string{"rest"}}}}, nil
	}
	tool := NewAskUserTool(fn)

	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "REST or GraphQL?",
				"options": []map[string]any{
					{"label": "REST", "value": "rest", "description": "Simpler tooling, wide adoption"},
					{"label": "GraphQL", "value": "graphql"},
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
	if len(got.Questions) != 1 || len(got.Questions[0].Options) != 2 {
		t.Fatalf("expected one question with two options, got %+v", got)
	}
	if desc := got.Questions[0].Options[0].Description; desc != "Simpler tooling, wide adoption" {
		t.Errorf("expected description to reach the ask callback, got %q", desc)
	}
	if desc := got.Questions[0].Options[1].Description; desc != "" {
		t.Errorf("expected empty description on the second option, got %q", desc)
	}
}

func TestAskUserTool_Execute_OptionRecommendedRejected(t *testing.T) {
	tool := NewAskUserTool(noopAskFunc)
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "REST or GraphQL?",
				"options": []map[string]any{
					{"label": "REST", "value": "rest", "recommended": []string{"rest"}},
					{"label": "GraphQL", "value": "graphql"},
				},
			},
		},
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true for option-level recommended, got false. Content: %s", result.Content)
	}
	// The rejection must name the question-level placement, not just refuse.
	if !strings.Contains(result.Content, "not accepted inside an option") {
		t.Errorf("expected 'not accepted inside an option' in validation error, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "put recommended at the question level") {
		t.Errorf("expected the question-level placement hint in validation error, got %q", result.Content)
	}
	if !strings.Contains(result.Content, `question "q1"`) {
		t.Errorf("expected the offending question id in validation error, got %q", result.Content)
	}
}

// TestAskUserTool_Schema_OptionDescriptionAdmitted pins the schema side of
// ADR-133: the closed-set pre-dispatch validator (Gate 1) must admit the
// optional option-level description so the key reaches Execute and the UI.
func TestAskUserTool_Schema_OptionDescriptionAdmitted(t *testing.T) {
	tool := NewAskUserTool(nil)
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "REST or GraphQL?",
				"options": []map[string]any{
					{"label": "REST", "value": "rest", "description": "hint"},
				},
			},
		},
	})
	if verr := sdktools.ValidateToolInput("ask_user", tool.InputSchema(), input); verr != nil {
		t.Fatalf("Gate 1 must admit the option-level description, got: %v", verr)
	}
}

// TestAskUserTool_Schema_OptionRecommendedRoutedToTool pins the deliberate
// schema declaration of option-level `recommended` (ADR-133): the key is
// admitted past Gate 1 — the generic unknown-parameter rejection cannot name
// the question-level placement — so Execute itself produces the actionable
// error (covered by TestAskUserTool_Execute_OptionRecommendedRejected).
func TestAskUserTool_Schema_OptionRecommendedRoutedToTool(t *testing.T) {
	tool := NewAskUserTool(nil)
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "REST or GraphQL?",
				"options": []map[string]any{
					{"label": "REST", "value": "rest", "recommended": []string{"rest"}},
				},
			},
		},
	})
	if verr := sdktools.ValidateToolInput("ask_user", tool.InputSchema(), input); verr != nil {
		t.Fatalf("option-level recommended must pass Gate 1 and reach Execute's specific rejection, got: %v", verr)
	}
}

// TestAskUserTool_Schema_UnknownOptionKeyRejected pins the other half of the
// contract: description is a targeted relaxation only — any other undeclared
// option key is still rejected by the closed-set validator.
func TestAskUserTool_Schema_UnknownOptionKeyRejected(t *testing.T) {
	tool := NewAskUserTool(nil)
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"id":       "q1",
				"question": "REST or GraphQL?",
				"options": []map[string]any{
					{"label": "REST", "value": "rest", "title": "REST"},
				},
			},
		},
	})
	verr := sdktools.ValidateToolInput("ask_user", tool.InputSchema(), input)
	if verr == nil {
		t.Fatal("expected the unknown option key to be rejected by Gate 1, got nil")
	}
	var inve *sdktools.InputValidationError
	if !errors.As(verr, &inve) {
		t.Fatalf("expected *sdktools.InputValidationError, got %T", verr)
	}
	if msg := verr.Error(); !strings.Contains(msg, `unknown parameter "title"`) {
		t.Errorf("expected 'unknown parameter' in validation error, got %q", msg)
	}
	if msg := verr.Error(); !strings.Contains(msg, "questions[0].options[0]") {
		t.Errorf("expected the option path in validation error, got %q", msg)
	}
}
