package reflector

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// mockLLMCaller is a minimal mock for testing.
type mockLLMCaller struct {
	calls  []llm.ChatRequest
	callFn func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
	err    error
}

func (m *mockLLMCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	m.calls = append(m.calls, req)
	if m.callFn != nil {
		return m.callFn(ctx, req)
	}
	if m.err != nil {
		return nil, m.err
	}
	return &llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: `{"summary":"ok","suggested_action":"retry"}`}}, nil
}

func (m *mockLLMCaller) lastCall() llm.ChatRequest {
	if len(m.calls) == 0 {
		return llm.ChatRequest{}
	}
	return m.calls[len(m.calls)-1]
}

func newTestReflector(mock *mockLLMCaller) *Reflector {
	return New(mock, Config{SystemPrompt: "You are a reflector."})
}

func TestReflector_Reflect_Success(t *testing.T) {
	mock := &mockLLMCaller{
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

	r := newTestReflector(mock)

	trajectory := []agent.Step{
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

	reflection, err := r.Reflect(context.Background(), trajectory, plan, nil)
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

func TestReflector_Reflect_DefaultAction(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"summary": "Analysis complete", "hypotheses": [], "reasoning": "All good"}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	r := newTestReflector(mock)
	reflection, err := r.Reflect(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}
	if reflection.SuggestedAction != "retry" {
		t.Errorf("expected default suggested_action='retry', got '%s'", reflection.SuggestedAction)
	}
}

func TestReflector_Reflect_ValidActions(t *testing.T) {
	tests := []struct {
		name           string
		response       string
		expectedAction string
	}{
		{"retry action", `{"summary": "Test", "suggested_action": "retry"}`, "retry"},
		{"replan action", `{"summary": "Test", "suggested_action": "replan"}`, "replan"},
		{"abort action", `{"summary": "Test", "suggested_action": "abort"}`, "abort"},
		{"unknown action defaults to retry", `{"summary": "Test", "suggested_action": "custom"}`, "retry"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockLLMCaller{
				callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: tt.response},
						StopReason: "end_turn",
					}, nil
				},
			}
			r := newTestReflector(mock)
			reflection, err := r.Reflect(context.Background(), nil, nil, nil)
			if err != nil {
				t.Fatalf("Reflect failed: %v", err)
			}
			if reflection.SuggestedAction != tt.expectedAction {
				t.Errorf("expected suggested_action='%s', got '%s'", tt.expectedAction, reflection.SuggestedAction)
			}
		})
	}
}

func TestReflector_Reflect_LLMError(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, errors.New("llm connection failed")
		},
	}

	r := newTestReflector(mock)
	_, err := r.Reflect(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error when LLM fails")
	}
	if err.Error() != "reflector LLM call failed: llm connection failed" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestReflector_Reflect_InvalidJSON(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "not valid json"},
				StopReason: "end_turn",
			}, nil
		},
	}

	r := newTestReflector(mock)
	_, err := r.Reflect(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestReflector_Reflect_WithPreviousReflections(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if len(req.Messages) < 2 {
				t.Error("expected at least 2 messages (system + user)")
			}
			userMsg := req.Messages[len(req.Messages)-1]
			if userMsg.Role != "user" {
				t.Error("expected last message to be user message")
			}
			if !strings.Contains(userMsg.Content, "Previous Reflections") {
				t.Error("expected user message to contain 'Previous Reflections' section")
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: `{"summary": "Analysis", "suggested_action": "retry"}`},
				StopReason: "end_turn",
			}, nil
		},
	}

	r := newTestReflector(mock)
	prevReflections := []orchestration.Reflection{
		{Summary: "First attempt failed", SuggestedAction: "retry", RootCause: "Network error"},
	}
	_, err := r.Reflect(context.Background(), nil, nil, prevReflections)
	if err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}
}

func TestReflector_Reflect_WithPlan(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
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

	r := newTestReflector(mock)
	plan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Description: "Run tests", DependsOn: []string{}},
			{ID: "step_2", Description: "Deploy", DependsOn: []string{"step_1"}},
		},
	}
	_, err := r.Reflect(context.Background(), nil, plan, nil)
	if err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}
}

func TestReflector_Reflect_WithTrajectory(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
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

	r := newTestReflector(mock)
	trajectory := []agent.Step{
		{
			Thought:     "I need to test the code",
			Action:      llm.ToolCall{ID: "call_1", Name: "bash_exec", Input: json.RawMessage(`{"command": "go test"}`)},
			Observation: "PASS",
		},
	}
	_, err := r.Reflect(context.Background(), trajectory, nil, nil)
	if err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}
}

func TestReflector_Reflect_EmptyTrajectory(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
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

	r := newTestReflector(mock)
	_, err := r.Reflect(context.Background(), []agent.Step{}, nil, nil)
	if err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}
}

func TestReflector_ImplementsInterface(t *testing.T) {
	var _ orchestration.Reflector = (*Reflector)(nil)
}

func TestParseReflectionResponse_Defaults(t *testing.T) {
	r := &Reflector{analyzeFooter: defaultAnalyzeFooter}

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

	r := newTestReflector(mock)
	r.SetReasoningEffort("high")

	_, err := r.Reflect(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}

	got := mock.lastCall().ReasoningEffort
	if got != "high" {
		t.Errorf("expected ReasoningEffort=%q, got %q", "high", got)
	}
}

func TestReflector_NoReasoningEffortWhenEmpty(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: `{"summary":"ok","suggested_action":"retry"}`},
				StopReason: "end_turn",
			}, nil
		},
	}

	r := newTestReflector(mock)

	_, err := r.Reflect(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}

	got := mock.lastCall().ReasoningEffort
	if got != "" {
		t.Errorf("expected empty ReasoningEffort, got %q", got)
	}
}

func TestReflector_NilResponse(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, nil
		},
	}
	r := newTestReflector(mock)
	_, err := r.Reflect(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil response")
	}
	if !strings.Contains(err.Error(), "nil response") {
		t.Errorf("expected 'nil response' in error, got: %v", err)
	}
}
