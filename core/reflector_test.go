package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/orchestration"
)

// TestReflector_Reflect_Success tests successful reflection generation.
func TestReflector_Reflect_Success(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{
					Role: "assistant",
					Content: `{"summary": "Test failed due to syntax error", 
						"hypotheses": ["Missing semicolon"], 
						"suggested_action": "retry",
						"reasoning": "The syntax error is fixable",
						"failure_analysis": "Parse error on line 5",
						"root_cause": "Syntax error",
						"action_plan": "Add missing semicolon"}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	reflector := NewReflector(mockLLM)

	trajectory := []Step{
		{
			Thought:     "I need to run the tests",
			Action:      llm.ToolCall{ID: "call_1", Name: "bash_exec", Input: json.RawMessage(`{"command": "go test"}`)},
			Observation: "FAIL: syntax error",
		},
	}

	plan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Description: "Run tests"},
		},
	}

	reflection, err := reflector.Reflect(context.Background(), trajectory, plan, nil)
	if err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}

	if reflection == nil {
		t.Fatal("expected non-nil reflection")
	}

	if reflection.Summary == "" {
		t.Error("expected non-empty summary")
	}

	if reflection.SuggestedAction != "retry" {
		t.Errorf("expected suggested_action='retry', got '%s'", reflection.SuggestedAction)
	}
}

// TestReflector_Reflect_DefaultAction tests that empty suggested_action defaults to retry.
func TestReflector_Reflect_DefaultAction(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{
					Role: "assistant",
					Content: `{"summary": "Analysis complete", 
						"hypotheses": [], 
						"reasoning": "All good"}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	reflector := NewReflector(mockLLM)

	reflection, err := reflector.Reflect(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}

	if reflection.SuggestedAction != "retry" {
		t.Errorf("expected default suggested_action='retry', got '%s'", reflection.SuggestedAction)
	}
}

// TestReflector_Reflect_ValidActions tests valid suggested actions.
func TestReflector_Reflect_ValidActions(t *testing.T) {
	tests := []struct {
		name           string
		response       string
		expectedAction string
	}{
		{
			name:           "retry action",
			response:       `{"summary": "Test", "suggested_action": "retry"}`,
			expectedAction: "retry",
		},
		{
			name:           "replan action",
			response:       `{"summary": "Test", "suggested_action": "replan"}`,
			expectedAction: "replan",
		},
		{
			name:           "abort action",
			response:       `{"summary": "Test", "suggested_action": "abort"}`,
			expectedAction: "abort",
		},
		{
			name:           "unknown action defaults to retry",
			response:       `{"summary": "Test", "suggested_action": "custom"}`,
			expectedAction: "retry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockLLM := &mockLLMCaller{
				callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: tt.response},
						StopReason: "end_turn",
					}, nil
				},
			}

			reflector := NewReflector(mockLLM)
			reflection, err := reflector.Reflect(context.Background(), nil, nil, nil)
			if err != nil {
				t.Fatalf("Reflect failed: %v", err)
			}

			if reflection.SuggestedAction != tt.expectedAction {
				t.Errorf("expected suggested_action='%s', got '%s'", tt.expectedAction, reflection.SuggestedAction)
			}
		})
	}
}

// TestReflector_Reflect_LLMError tests error handling when LLM call fails.
func TestReflector_Reflect_LLMError(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, errors.New("llm connection failed")
		},
	}

	reflector := NewReflector(mockLLM)

	_, err := reflector.Reflect(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error when LLM fails")
	}

	if !errors.Is(err, context.Canceled) && err.Error() != "reflector LLM call failed: llm connection failed" {
		// Error should wrap the LLM error
		if err.Error() != "reflector LLM call failed: llm connection failed" {
			t.Errorf("unexpected error message: %v", err)
		}
	}
}

// TestReflector_Reflect_InvalidJSON tests error handling for invalid JSON response.
func TestReflector_Reflect_InvalidJSON(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "not valid json"},
				StopReason: "end_turn",
			}, nil
		},
	}

	reflector := NewReflector(mockLLM)

	_, err := reflector.Reflect(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

// TestReflector_Reflect_WithPreviousReflections tests reflection with history.
func TestReflector_Reflect_WithPreviousReflections(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Verify that previous reflections are included in the prompt
			if len(req.Messages) < 2 {
				t.Error("expected at least 2 messages (system + user)")
			}
			userMsg := req.Messages[len(req.Messages)-1]
			if userMsg.Role != "user" {
				t.Error("expected last message to be user message")
			}
			// Check that "Previous Reflections" section is included
			if !strings.Contains(userMsg.Content, "Previous Reflections") {
				t.Error("expected user message to contain 'Previous Reflections' section")
			}

			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: `{"summary": "Analysis", "suggested_action": "retry"}`},
				StopReason: "end_turn",
			}, nil
		},
	}

	reflector := NewReflector(mockLLM)

	prevReflections := []orchestration.Reflection{
		{
			Summary:         "First attempt failed",
			SuggestedAction: "retry",
			RootCause:       "Network error",
		},
	}

	_, err := reflector.Reflect(context.Background(), nil, nil, prevReflections)
	if err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}
}

// TestReflector_Reflect_WithPlan tests reflection includes plan information.
func TestReflector_Reflect_WithPlan(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Verify that plan is included in the prompt
			userMsg := req.Messages[len(req.Messages)-1]
			if !strings.Contains(userMsg.Content, "## Plan") {
				t.Error("expected user message to contain '## Plan' section")
			}
			if !strings.Contains(userMsg.Content, "step_1") {
				t.Error("expected user message to contain step ID")
			}

			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: `{"summary": "Analysis", "suggested_action": "retry"}`},
				StopReason: "end_turn",
			}, nil
		},
	}

	reflector := NewReflector(mockLLM)

	plan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Description: "Run tests", DependsOn: []string{}},
			{ID: "step_2", Description: "Deploy", DependsOn: []string{"step_1"}},
		},
	}

	_, err := reflector.Reflect(context.Background(), nil, plan, nil)
	if err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}
}

// TestReflector_Reflect_WithTrajectory tests reflection includes trajectory.
func TestReflector_Reflect_WithTrajectory(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Verify that trajectory is included in the prompt
			userMsg := req.Messages[len(req.Messages)-1]
			if !strings.Contains(userMsg.Content, "## Execution Trajectory") {
				t.Error("expected user message to contain '## Execution Trajectory' section")
			}
			if !strings.Contains(userMsg.Content, "Step 1") {
				t.Error("expected user message to contain step information")
			}

			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: `{"summary": "Analysis", "suggested_action": "retry"}`},
				StopReason: "end_turn",
			}, nil
		},
	}

	reflector := NewReflector(mockLLM)

	trajectory := []Step{
		{
			Thought:     "I need to test the code",
			Action:      llm.ToolCall{ID: "call_1", Name: "bash_exec", Input: json.RawMessage(`{"command": "go test"}`)},
			Observation: "PASS",
		},
	}

	_, err := reflector.Reflect(context.Background(), trajectory, nil, nil)
	if err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}
}

// TestReflector_Reflect_EmptyTrajectory tests reflection with no trajectory.
func TestReflector_Reflect_EmptyTrajectory(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Verify that empty trajectory is handled
			userMsg := req.Messages[len(req.Messages)-1]
			if !strings.Contains(userMsg.Content, "No steps executed") {
				t.Error("expected user message to indicate no steps executed")
			}

			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: `{"summary": "Analysis", "suggested_action": "retry"}`},
				StopReason: "end_turn",
			}, nil
		},
	}

	reflector := NewReflector(mockLLM)

	_, err := reflector.Reflect(context.Background(), []Step{}, nil, nil)
	if err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}
}

// TestReflector_ImplementsInterface verifies Reflector implements orchestration.Reflector.
func TestReflector_ImplementsInterface(t *testing.T) {
	var _ orchestration.Reflector = (*Reflector)(nil)
}

func TestParseReflectionResponse_Defaults(t *testing.T) {
	r := &Reflector{}

	// Test with minimal JSON (missing optional fields)
	reflection, err := r.parseReflectionResponse(`{"summary":"","suggested_action":"unknown"}`)
	if err != nil {
		t.Fatalf("parseReflectionResponse failed: %v", err)
	}
	if reflection.Summary != "Execution analysis unavailable" {
		t.Errorf("expected default summary, got '%s'", reflection.Summary)
	}
	if reflection.SuggestedAction != "retry" {
		t.Errorf("expected 'retry' for unknown action, got '%s'", reflection.SuggestedAction)
	}
	if reflection.Hypotheses != nil {
		t.Error("expected nil hypotheses slice")
	}
}

func TestReflector_SetsReasoningEffort(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: `{"summary":"ok","suggested_action":"retry"}`},
				StopReason: "end_turn",
			}, nil
		},
	}

	reflector := NewReflector(mock)
	reflector.SetBaseReasoningEffort(llm.ReasoningHigh)

	_, err := reflector.Reflect(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}

	// AgentReasoningMode("reflector", ReasoningHigh) should return ReasoningLow
	got := mock.lastCall().ReasoningEffort
	if got != llm.ReasoningLow {
		t.Errorf("expected ReasoningEffort=%q, got %q", llm.ReasoningLow, got)
	}
}

func TestReflector_NoReasoningEffortWhenBaseEmpty(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: `{"summary":"ok","suggested_action":"retry"}`},
				StopReason: "end_turn",
			}, nil
		},
	}

	reflector := NewReflector(mock)
	// No SetBaseReasoningEffort — base is empty

	_, err := reflector.Reflect(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}

	// AgentReasoningMode("reflector", "") returns ReasoningOff — explicitly disabled
	got := mock.lastCall().ReasoningEffort
	if got != llm.ReasoningOff {
		t.Errorf("expected ReasoningEffort=%q, got %q", llm.ReasoningOff, got)
	}
}
