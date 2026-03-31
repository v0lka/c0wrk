package core

import (
	"context"
	"strings"
	"testing"

	"github.com/user/agent/internal/llm"
)

func TestReflect_ProducesReflection(t *testing.T) {
	// Setup mock LLM that returns valid JSON reflection
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role: "assistant",
					Content: `{
						"summary": "Task failed due to missing file",
						"failed_criteria": ["ac_1", "ac_2"],
						"hypotheses": ["File was not created", "Wrong path specified"],
						"suggested_action": "retry",
						"reasoning": "The file operation can be retried with correct path",
						"failure_analysis": "The step attempted to read a file that does not exist",
						"root_cause": "Incorrect file path in the action",
						"action_plan": "Verify file path before reading",
						"task_type": "code"
					}`,
				},
				StopReason: "end_turn",
			},
		},
	}

	reflector := NewReflector(mockLLM)

	trajectory := []Step{
		{
			Thought:     "I need to read the config file",
			Action:      llm.ToolCall{Name: "read_file", Input: []byte(`{"path": "/tmp/config.json"}`)},
			Observation: "Error: file not found",
		},
	}

	evalResult := &EvalResult{
		Failed: []EvalDetail{
			{Criterion: AcceptanceCriterion{ID: "ac_1", Description: "Config loaded"}, Diagnostic: "File not found"},
			{Criterion: AcceptanceCriterion{ID: "ac_2", Description: "App runs"}, Diagnostic: "Dependency failure"},
		},
		AllPassed: false,
	}

	reflection, err := reflector.Reflect(context.Background(), trajectory, evalResult, nil, nil)
	if err != nil {
		t.Fatalf("Reflect() error = %v", err)
	}

	// Verify all fields are populated
	if reflection.Summary != "Task failed due to missing file" {
		t.Errorf("Summary = %q, want %q", reflection.Summary, "Task failed due to missing file")
	}
	if len(reflection.FailedCriteria) != 2 {
		t.Errorf("FailedCriteria len = %d, want 2", len(reflection.FailedCriteria))
	}
	if len(reflection.Hypotheses) != 2 {
		t.Errorf("Hypotheses len = %d, want 2", len(reflection.Hypotheses))
	}
	if reflection.SuggestedAction != "retry" {
		t.Errorf("SuggestedAction = %q, want %q", reflection.SuggestedAction, "retry")
	}
	if reflection.Reasoning == "" {
		t.Error("Reasoning should not be empty")
	}
	if reflection.FailureAnalysis == "" {
		t.Error("FailureAnalysis should not be empty")
	}
	if reflection.RootCause == "" {
		t.Error("RootCause should not be empty")
	}
	if reflection.ActionPlan == "" {
		t.Error("ActionPlan should not be empty")
	}
	if reflection.TaskType != "code" {
		t.Errorf("TaskType = %q, want %q", reflection.TaskType, "code")
	}
	if reflection.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
}

func TestReflect_IncludesTrajectoryInPrompt(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"summary": "test", "failed_criteria": [], "hypotheses": [], "suggested_action": "retry", "reasoning": "test", "failure_analysis": "test", "root_cause": "test", "action_plan": "test", "task_type": "code"}`,
				},
				StopReason: "end_turn",
			},
		},
	}

	reflector := NewReflector(mockLLM)

	trajectory := []Step{
		{
			Thought:     "First I need to analyze the code",
			Action:      llm.ToolCall{Name: "read_file", Input: []byte(`{"path": "/src/main.go"}`)},
			Observation: "package main\n\nfunc main() {}",
		},
		{
			Thought:     "Now I will modify the code",
			Action:      llm.ToolCall{Name: "write_file", Input: []byte(`{"path": "/src/main.go", "content": "updated"}`)},
			Observation: "File written successfully",
		},
	}

	_, err := reflector.Reflect(context.Background(), trajectory, &EvalResult{}, nil, nil)
	if err != nil {
		t.Fatalf("Reflect() error = %v", err)
	}

	// Check that trajectory appears in the prompt
	lastCall := mockLLM.lastCall()
	if len(lastCall.Messages) < 2 {
		t.Fatal("Expected at least 2 messages (system + user)")
	}

	userMessage := lastCall.Messages[1].Content

	// Verify trajectory steps appear in prompt
	if !strings.Contains(userMessage, "First I need to analyze the code") {
		t.Error("User message should contain first thought")
	}
	if !strings.Contains(userMessage, "Now I will modify the code") {
		t.Error("User message should contain second thought")
	}
	if !strings.Contains(userMessage, "read_file") {
		t.Error("User message should contain action names")
	}
	if !strings.Contains(userMessage, "Execution Trajectory") {
		t.Error("User message should contain trajectory section header")
	}
}

func TestReflect_IncludesEvalResultInPrompt(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"summary": "test", "failed_criteria": ["ac_1"], "hypotheses": [], "suggested_action": "retry", "reasoning": "test", "failure_analysis": "test", "root_cause": "test", "action_plan": "test", "task_type": "code"}`,
				},
				StopReason: "end_turn",
			},
		},
	}

	reflector := NewReflector(mockLLM)

	evalResult := &EvalResult{
		Passed: []EvalDetail{
			{Criterion: AcceptanceCriterion{ID: "ac_passed", Description: "Build succeeds"}, Diagnostic: "Exit code 0"},
		},
		Failed: []EvalDetail{
			{Criterion: AcceptanceCriterion{ID: "ac_failed", Description: "Tests pass"}, Diagnostic: "3 tests failed"},
		},
		AllPassed: false,
	}

	_, err := reflector.Reflect(context.Background(), []Step{}, evalResult, nil, nil)
	if err != nil {
		t.Fatalf("Reflect() error = %v", err)
	}

	lastCall := mockLLM.lastCall()
	userMessage := lastCall.Messages[1].Content

	// Verify eval result appears in prompt
	if !strings.Contains(userMessage, "Failed Criteria") {
		t.Error("User message should contain 'Failed Criteria' section")
	}
	if !strings.Contains(userMessage, "ac_failed") {
		t.Error("User message should contain failed criterion ID")
	}
	if !strings.Contains(userMessage, "Tests pass") {
		t.Error("User message should contain failed criterion description")
	}
	if !strings.Contains(userMessage, "3 tests failed") {
		t.Error("User message should contain diagnostic")
	}
	if !strings.Contains(userMessage, "Passed Criteria") {
		t.Error("User message should contain 'Passed Criteria' section")
	}
	if !strings.Contains(userMessage, "ac_passed") {
		t.Error("User message should contain passed criterion ID")
	}
}

func TestReflect_IncludesPreviousReflections(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"summary": "test", "failed_criteria": [], "hypotheses": [], "suggested_action": "abort", "reasoning": "test", "failure_analysis": "test", "root_cause": "test", "action_plan": "test", "task_type": "code"}`,
				},
				StopReason: "end_turn",
			},
		},
	}

	reflector := NewReflector(mockLLM)

	prevReflections := []Reflection{
		{
			Summary:         "Previous attempt failed due to permissions",
			RootCause:       "Insufficient file permissions",
			ActionPlan:      "Request elevated permissions",
			SuggestedAction: "retry",
		},
		{
			Summary:         "Second attempt also failed",
			RootCause:       "Directory does not exist",
			ActionPlan:      "Create directory first",
			SuggestedAction: "replan",
		},
	}

	_, err := reflector.Reflect(context.Background(), []Step{}, &EvalResult{}, nil, prevReflections)
	if err != nil {
		t.Fatalf("Reflect() error = %v", err)
	}

	lastCall := mockLLM.lastCall()
	userMessage := lastCall.Messages[1].Content

	// Verify previous reflections appear in prompt
	if !strings.Contains(userMessage, "Previous Reflections") {
		t.Error("User message should contain 'Previous Reflections' section")
	}
	if !strings.Contains(userMessage, "Previous attempt failed due to permissions") {
		t.Error("User message should contain first reflection summary")
	}
	if !strings.Contains(userMessage, "Insufficient file permissions") {
		t.Error("User message should contain first reflection root cause")
	}
	if !strings.Contains(userMessage, "Second attempt also failed") {
		t.Error("User message should contain second reflection summary")
	}
	if !strings.Contains(userMessage, "avoid repeating the same mistakes") {
		t.Error("User message should contain instruction about learning from previous reflections")
	}
}

func TestReflect_UsesCorrectRole(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"summary": "test", "failed_criteria": [], "hypotheses": [], "suggested_action": "retry", "reasoning": "test", "failure_analysis": "test", "root_cause": "test", "action_plan": "test", "task_type": "code"}`,
				},
				StopReason: "end_turn",
			},
		},
	}

	reflector := NewReflector(mockLLM)

	_, err := reflector.Reflect(context.Background(), []Step{}, &EvalResult{}, nil, nil)
	if err != nil {
		t.Fatalf("Reflect() error = %v", err)
	}

	// Verify the correct role was used
	if mockLLM.lastRole() != "reflector" {
		t.Errorf("LLM called with role %q, want %q", mockLLM.lastRole(), "reflector")
	}
}

func TestReflect_SuggestsRetryOnPartialFailure(t *testing.T) {
	// LLM response suggesting retry for partial failure
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"summary": "Partial success", "failed_criteria": ["ac_2"], "hypotheses": ["Minor issue"], "suggested_action": "retry", "reasoning": "Some criteria passed, failure seems recoverable", "failure_analysis": "One test failed", "root_cause": "Edge case not handled", "action_plan": "Fix edge case", "task_type": "code"}`,
				},
				StopReason: "end_turn",
			},
		},
	}

	reflector := NewReflector(mockLLM)

	evalResult := &EvalResult{
		Passed: []EvalDetail{
			{Criterion: AcceptanceCriterion{ID: "ac_1", Description: "Build passes"}},
		},
		Failed: []EvalDetail{
			{Criterion: AcceptanceCriterion{ID: "ac_2", Description: "Tests pass"}},
		},
		AllPassed: false,
	}

	reflection, err := reflector.Reflect(context.Background(), []Step{}, evalResult, nil, nil)
	if err != nil {
		t.Fatalf("Reflect() error = %v", err)
	}

	if reflection.SuggestedAction != "retry" {
		t.Errorf("SuggestedAction = %q, want %q for partial failure", reflection.SuggestedAction, "retry")
	}
}

func TestReflect_SuggestsAbortOnFullFailure(t *testing.T) {
	// LLM response suggesting abort for total failure
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"summary": "Complete failure", "failed_criteria": ["ac_1", "ac_2", "ac_3"], "hypotheses": ["Task is impossible"], "suggested_action": "abort", "reasoning": "All criteria failed, no progress made after multiple attempts", "failure_analysis": "Every step failed", "root_cause": "Missing required dependencies", "action_plan": "Cannot proceed without external intervention", "task_type": "code"}`,
				},
				StopReason: "end_turn",
			},
		},
	}

	reflector := NewReflector(mockLLM)

	evalResult := &EvalResult{
		Failed: []EvalDetail{
			{Criterion: AcceptanceCriterion{ID: "ac_1", Description: "Build passes"}, Diagnostic: "Compilation error"},
			{Criterion: AcceptanceCriterion{ID: "ac_2", Description: "Tests pass"}, Diagnostic: "No tests run"},
			{Criterion: AcceptanceCriterion{ID: "ac_3", Description: "Lint passes"}, Diagnostic: "Linter crashed"},
		},
		AllPassed: false,
	}

	// Include previous reflections to indicate repeated failure
	prevReflections := []Reflection{
		{Summary: "First attempt failed", SuggestedAction: "retry"},
		{Summary: "Second attempt failed", SuggestedAction: "retry"},
	}

	reflection, err := reflector.Reflect(context.Background(), []Step{}, evalResult, nil, prevReflections)
	if err != nil {
		t.Fatalf("Reflect() error = %v", err)
	}

	if reflection.SuggestedAction != "abort" {
		t.Errorf("SuggestedAction = %q, want %q for full failure", reflection.SuggestedAction, "abort")
	}
}

func TestReflect_IncludesPlanInPrompt(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"summary": "test", "failed_criteria": [], "hypotheses": [], "suggested_action": "replan", "reasoning": "test", "failure_analysis": "test", "root_cause": "test", "action_plan": "test", "task_type": "code"}`,
				},
				StopReason: "end_turn",
			},
		},
	}

	reflector := NewReflector(mockLLM)

	plan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", Description: "Read the source file", DependsOn: []string{}, RelevantAC: []string{"ac_1"}},
			{ID: "step_2", Description: "Modify the code", DependsOn: []string{"step_1"}, RelevantAC: []string{"ac_2"}},
		},
	}

	_, err := reflector.Reflect(context.Background(), []Step{}, &EvalResult{}, plan, nil)
	if err != nil {
		t.Fatalf("Reflect() error = %v", err)
	}

	lastCall := mockLLM.lastCall()
	userMessage := lastCall.Messages[1].Content

	// Verify plan appears in prompt
	if !strings.Contains(userMessage, "## Plan") {
		t.Error("User message should contain '## Plan' section")
	}
	if !strings.Contains(userMessage, "step_1") {
		t.Error("User message should contain step_1")
	}
	if !strings.Contains(userMessage, "Read the source file") {
		t.Error("User message should contain step description")
	}
	if !strings.Contains(userMessage, "step_2") {
		t.Error("User message should contain step_2")
	}
}

func TestReflect_HandlesMarkdownCodeBlock(t *testing.T) {
	// LLM returns JSON wrapped in markdown code block
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role: "assistant",
					Content: "```json\n" + `{
						"summary": "Wrapped in markdown",
						"failed_criteria": [],
						"hypotheses": [],
						"suggested_action": "retry",
						"reasoning": "test",
						"failure_analysis": "test",
						"root_cause": "test",
						"action_plan": "test",
						"task_type": "code"
					}` + "\n```",
				},
				StopReason: "end_turn",
			},
		},
	}

	reflector := NewReflector(mockLLM)

	reflection, err := reflector.Reflect(context.Background(), []Step{}, &EvalResult{}, nil, nil)
	if err != nil {
		t.Fatalf("Reflect() error = %v, should handle markdown code blocks", err)
	}

	if reflection.Summary != "Wrapped in markdown" {
		t.Errorf("Summary = %q, want %q", reflection.Summary, "Wrapped in markdown")
	}
}

func TestReflect_DefaultsToRetryWhenActionMissing(t *testing.T) {
	// LLM returns JSON without suggested_action
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role: "assistant",
					Content: `{
						"summary": "test",
						"failed_criteria": [],
						"hypotheses": [],
						"reasoning": "test",
						"failure_analysis": "test",
						"root_cause": "test",
						"action_plan": "test",
						"task_type": "code"
					}`,
				},
				StopReason: "end_turn",
			},
		},
	}

	reflector := NewReflector(mockLLM)

	reflection, err := reflector.Reflect(context.Background(), []Step{}, &EvalResult{}, nil, nil)
	if err != nil {
		t.Fatalf("Reflect() error = %v", err)
	}

	// Should default to "retry" when suggested_action is missing
	if reflection.SuggestedAction != "retry" {
		t.Errorf("SuggestedAction = %q, want %q (default)", reflection.SuggestedAction, "retry")
	}
}

func TestReflect_LLMError(t *testing.T) {
	mockLLM := &mockLLMCaller{
		err: context.DeadlineExceeded,
	}

	reflector := NewReflector(mockLLM)

	_, err := reflector.Reflect(context.Background(), []Step{}, &EvalResult{}, nil, nil)
	if err == nil {
		t.Fatal("Reflect() should return error when LLM fails")
	}

	if !strings.Contains(err.Error(), "reflector LLM call failed") {
		t.Errorf("Error message = %q, should contain 'reflector LLM call failed'", err.Error())
	}
}

func TestReflect_InvalidJSON(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "This is not valid JSON at all",
				},
				StopReason: "end_turn",
			},
		},
	}

	reflector := NewReflector(mockLLM)

	_, err := reflector.Reflect(context.Background(), []Step{}, &EvalResult{}, nil, nil)
	if err == nil {
		t.Fatal("Reflect() should return error for invalid JSON")
	}

	if !strings.Contains(err.Error(), "failed to parse reflection response") {
		t.Errorf("Error message = %q, should contain 'failed to parse reflection response'", err.Error())
	}
}

// TestReflector_ParsesEscalateAction tests that the reflector correctly parses "escalate" as a valid SuggestedAction.
func TestReflector_ParsesEscalateAction(t *testing.T) {
	// Mock returns a reflection JSON with "escalate" action
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role: "assistant",
					Content: `{
						"summary": "Task too complex for react mode",
						"failed_criteria": ["ac_1"],
						"hypotheses": ["Need structured planning"],
						"suggested_action": "escalate",
						"reasoning": "React mode cannot handle this complexity",
						"failure_analysis": "Multiple dependent steps needed",
						"root_cause": "Task requires DAG execution",
						"action_plan": "Escalate to plan_execute mode",
						"task_type": "code"
					}`,
				},
				StopReason: "end_turn",
			},
		},
	}

	reflector := NewReflector(mockLLM)

	reflection, err := reflector.Reflect(context.Background(), []Step{}, &EvalResult{}, nil, nil)
	if err != nil {
		t.Fatalf("Reflect() error = %v", err)
	}

	// Verify escalate action is parsed correctly
	if reflection.SuggestedAction != "escalate" {
		t.Errorf("SuggestedAction = %q, want %q", reflection.SuggestedAction, "escalate")
	}

	// Verify other fields are populated correctly
	if reflection.Summary != "Task too complex for react mode" {
		t.Errorf("Summary = %q, want %q", reflection.Summary, "Task too complex for react mode")
	}

	if len(reflection.FailedCriteria) != 1 || reflection.FailedCriteria[0] != "ac_1" {
		t.Errorf("FailedCriteria = %v, want [ac_1]", reflection.FailedCriteria)
	}

	if reflection.Reasoning == "" {
		t.Error("Reasoning should not be empty")
	}
}
