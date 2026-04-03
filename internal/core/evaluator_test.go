package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/user/agent/internal/llm"
	"github.com/user/agent/internal/tools"
)

// Tests use shared mock types from testhelpers_test.go:
// - mockLLMCaller: implements LLMCaller
// - mockToolExecutor: implements ToolExecutor

func TestEvaluator_ProgrammaticPasses(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {
				Content: "all tests passed",
				IsError: false,
			},
		},
	}

	evaluator := NewEvaluator(mockTools, nil)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_1",
			Description: "Tests must pass",
			CheckType:   "programmatic",
			CheckCmd:    "go test ./...",
		},
	}

	evalResult, err := evaluator.Evaluate(context.Background(), "", criteria, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d", len(evalResult.Failed))
	}
	if evalResult.Passed[0].Criterion.ID != "ac_1" {
		t.Errorf("expected criterion ID ac_1, got %s", evalResult.Passed[0].Criterion.ID)
	}
}

func TestEvaluator_ProgrammaticFails(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {
				Content: "FAIL: TestSomething",
				IsError: true,
			},
		},
	}

	evaluator := NewEvaluator(mockTools, nil)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_1",
			Description: "Tests must pass",
			CheckType:   "programmatic",
			CheckCmd:    "go test ./...",
		},
	}

	evalResult, err := evaluator.Evaluate(context.Background(), "", criteria, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(evalResult.Failed))
	}
	if len(evalResult.Passed) != 0 {
		t.Errorf("expected 0 passed, got %d", len(evalResult.Passed))
	}
	if evalResult.Failed[0].Criterion.ID != "ac_1" {
		t.Errorf("expected criterion ID ac_1, got %s", evalResult.Failed[0].Criterion.ID)
	}
	if evalResult.AllPassed {
		t.Error("expected AllPassed to be false")
	}
}

func TestEvaluator_LLMJudgePasses(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: "YES, the criterion is met because the code implements proper error handling.",
			},
		}},
	}

	evaluator := NewEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_2",
			Description: "Code must have proper error handling",
			CheckType:   "llm_judge",
		},
	}

	evalResult, err := evaluator.Evaluate(context.Background(), "func doSomething() error { return nil }", criteria, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d", len(evalResult.Failed))
	}
	if evalResult.Passed[0].Criterion.ID != "ac_2" {
		t.Errorf("expected criterion ID ac_2, got %s", evalResult.Passed[0].Criterion.ID)
	}
}

func TestEvaluator_LLMJudgeFails(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: "NO, the criterion is not met because there is no error handling.",
			},
		}},
	}

	evaluator := NewEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_2",
			Description: "Code must have proper error handling",
			CheckType:   "llm_judge",
		},
	}

	evalResult, err := evaluator.Evaluate(context.Background(), "func doSomething() { panic(\"oops\") }", criteria, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(evalResult.Failed))
	}
	if len(evalResult.Passed) != 0 {
		t.Errorf("expected 0 passed, got %d", len(evalResult.Passed))
	}
	if evalResult.Failed[0].Criterion.ID != "ac_2" {
		t.Errorf("expected criterion ID ac_2, got %s", evalResult.Failed[0].Criterion.ID)
	}
	if evalResult.AllPassed {
		t.Error("expected AllPassed to be false")
	}
}

func TestEvaluator_MixedCriteria(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {
				Content: "all tests passed",
				IsError: false, // passes
			},
		},
	}

	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: "NO, the documentation is incomplete.",
			},
		}},
	}

	evaluator := NewEvaluator(mockTools, mockLLM)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_1",
			Description: "Tests must pass",
			CheckType:   "programmatic",
			CheckCmd:    "go test ./...",
		},
		{
			ID:          "ac_2",
			Description: "Code must be well documented",
			CheckType:   "llm_judge",
		},
	}

	evalResult, err := evaluator.Evaluate(context.Background(), "func foo() {}", criteria, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(evalResult.Failed))
	}
	if evalResult.AllPassed {
		t.Error("expected AllPassed to be false when one criterion fails")
	}

	// Verify which criterion passed and which failed
	if evalResult.Passed[0].Criterion.ID != "ac_1" {
		t.Errorf("expected ac_1 to pass, got %s", evalResult.Passed[0].Criterion.ID)
	}
	if evalResult.Failed[0].Criterion.ID != "ac_2" {
		t.Errorf("expected ac_2 to fail, got %s", evalResult.Failed[0].Criterion.ID)
	}
}

func TestEvaluator_AllPassed(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {
				Content: "all tests passed",
				IsError: false,
			},
		},
	}

	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: "YES, the code quality is excellent.",
			},
		}},
	}

	evaluator := NewEvaluator(mockTools, mockLLM)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_1",
			Description: "Tests must pass",
			CheckType:   "programmatic",
			CheckCmd:    "go test ./...",
		},
		{
			ID:          "ac_2",
			Description: "Code quality is good",
			CheckType:   "llm_judge",
		},
	}

	evalResult, err := evaluator.Evaluate(context.Background(), "func wellWritten() error { return nil }", criteria, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 2 {
		t.Errorf("expected 2 passed, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d", len(evalResult.Failed))
	}
	if len(evalResult.Unclear) != 0 {
		t.Errorf("expected 0 unclear, got %d", len(evalResult.Unclear))
	}
	if !evalResult.AllPassed {
		t.Error("expected AllPassed to be true when all criteria pass")
	}
}

func TestEvaluator_LLMJudgeUnclear(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: "I cannot determine if this criterion is met without more context.",
			},
		}},
	}

	evaluator := NewEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_1",
			Description: "Code must be performant",
			CheckType:   "llm_judge",
		},
	}

	evalResult, err := evaluator.Evaluate(context.Background(), "some code", criteria, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Unclear) != 1 {
		t.Errorf("expected 1 unclear, got %d", len(evalResult.Unclear))
	}
	if len(evalResult.Passed) != 0 {
		t.Errorf("expected 0 passed, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d", len(evalResult.Failed))
	}
	if !evalResult.AllPassed {
		t.Error("expected AllPassed to be true when there are UNCLEAR results but zero FAILED results")
	}
}

// TestEvaluator_LLMJudgeWithTrajectory verifies that execution steps are included
// in the LLM prompt as evidence when provided.
func TestEvaluator_LLMJudgeWithTrajectory(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: "YES, the criterion is met based on the execution evidence.",
			},
		}},
	}

	evaluator := NewEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{{
		ID:          "ac_trajectory",
		Description: "Code must compile without errors",
		CheckType:   "llm_judge",
	}}

	steps := []Step{{
		Thought:     "I need to compile the code",
		Action:      llm.ToolCall{Name: "bash_exec", Input: json.RawMessage(`{"command":"go build"}`)},
		Observation: "Build successful",
	}}

	evalResult, err := evaluator.Evaluate(context.Background(), "compilation output", criteria, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed, got %d", len(evalResult.Passed))
	}

	// Verify the LLM prompt contained execution evidence
	if len(mockLLM.calls) == 0 {
		t.Fatal("expected at least one LLM call")
	}
	lastCallContent := mockLLM.calls[0].Messages[0].Content
	if !strings.Contains(lastCallContent, "Step 1") {
		t.Error("expected prompt to contain 'Step 1' from execution evidence")
	}
	if !strings.Contains(lastCallContent, "bash_exec") {
		t.Error("expected prompt to contain tool name 'bash_exec' from execution evidence")
	}
}

// TestEvaluator_ReconsiderationFlipsFailed verifies that a failed llm_judge
// criterion can be flipped to passed after reconsideration with execution evidence.
func TestEvaluator_ReconsiderationFlipsFailed(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				// Initial evaluation returns NO
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "NO, the criterion is not met based on the result alone.",
					},
				}, nil
			}
			// Reconsideration returns YES
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "YES, upon reviewing the execution evidence, the criterion is met.",
				},
			}, nil
		},
	}

	evaluator := NewEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{{
		ID:          "ac_reconsider",
		Description: "Tests must pass",
		CheckType:   "llm_judge",
	}}

	steps := []Step{{
		Thought:     "Running tests",
		Action:      llm.ToolCall{Name: "bash_exec", Input: json.RawMessage(`{"command":"go test ./..."}`)},
		Observation: "ok  all tests passed",
	}}

	evalResult, err := evaluator.Evaluate(context.Background(), "test output", criteria, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have made 2 calls: initial + reconsideration
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls (initial + reconsideration), got %d", callCount)
	}

	// Criterion should now be in Passed (flipped from Failed)
	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed after reconsideration, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 0 {
		t.Errorf("expected 0 failed after reconsideration, got %d", len(evalResult.Failed))
	}

	// Check reconsidered flag and original diagnostic
	passedDetail := evalResult.Passed[0]
	if !passedDetail.Reconsidered {
		t.Error("expected Reconsidered to be true")
	}
	if passedDetail.OriginalDiagnostic == "" {
		t.Error("expected OriginalDiagnostic to be populated")
	}
	if !strings.Contains(passedDetail.OriginalDiagnostic, "FAILED") {
		t.Errorf("expected OriginalDiagnostic to contain 'FAILED', got %q", passedDetail.OriginalDiagnostic)
	}
	if !evalResult.AllPassed {
		t.Error("expected AllPassed to be true after successful reconsideration")
	}
}

// TestEvaluator_ReconsiderationKeepsFailed verifies that a failed criterion
// stays failed when reconsideration also returns NO.
func TestEvaluator_ReconsiderationKeepsFailed(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "NO, the criterion is not met.",
					},
				}, nil
			}
			// Reconsideration also returns NO
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "NO, even with execution evidence, the criterion is not met.",
				},
			}, nil
		},
	}

	evaluator := NewEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{{
		ID:          "ac_stays_failed",
		Description: "Tests must pass",
		CheckType:   "llm_judge",
	}}

	steps := []Step{{
		Thought:     "Running tests",
		Action:      llm.ToolCall{Name: "bash_exec"},
		Observation: "FAIL: TestSomething",
	}}

	evalResult, err := evaluator.Evaluate(context.Background(), "test output", criteria, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have made 2 calls
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls, got %d", callCount)
	}

	// Criterion should still be in Failed
	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(evalResult.Failed))
	}
	if len(evalResult.Passed) != 0 {
		t.Errorf("expected 0 passed, got %d", len(evalResult.Passed))
	}
	if evalResult.AllPassed {
		t.Error("expected AllPassed to be false")
	}
}

// TestEvaluator_NoReconsiderationForProgrammatic verifies that programmatic
// criteria are never reconsidered (they are ground truth).
func TestEvaluator_NoReconsiderationForProgrammatic(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {
				Content: "FAIL: build failed",
				IsError: true,
			},
		},
	}

	// This LLM should NOT be called for programmatic checks
	llmCallCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			llmCallCount++
			return &llm.ChatResponse{
				Message: llm.Message{Role: "assistant", Content: "YES"},
			}, nil
		},
	}

	evaluator := NewEvaluator(mockTools, mockLLM)

	criteria := []AcceptanceCriterion{{
		ID:          "ac_programmatic",
		Description: "Build must succeed",
		CheckType:   "programmatic",
		CheckCmd:    "go build ./...",
	}}

	// Even with steps, programmatic should not trigger reconsideration
	steps := []Step{{
		Thought:     "Building",
		Action:      llm.ToolCall{Name: "bash_exec"},
		Observation: "build ok",
	}}

	evalResult, err := evaluator.Evaluate(context.Background(), "", criteria, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// LLM should not have been called at all (programmatic doesn't use LLM)
	if llmCallCount != 0 {
		t.Errorf("expected 0 LLM calls for programmatic check, got %d", llmCallCount)
	}

	// Programmatic fail should stay failed
	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(evalResult.Failed))
	}

	// Reconsidered should be false
	if evalResult.Failed[0].Reconsidered {
		t.Error("programmatic criterion should not be reconsidered")
	}
}

// TestEvaluator_NoReconsiderationWithoutSteps verifies that reconsideration
// is skipped when steps is nil.
func TestEvaluator_NoReconsiderationWithoutSteps(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			// Always return NO
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "NO, the criterion is not met.",
				},
			}, nil
		},
	}

	evaluator := NewEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{{
		ID:          "ac_no_steps",
		Description: "Code must be well documented",
		CheckType:   "llm_judge",
	}}

	// Pass nil for steps - should skip reconsideration
	evalResult, err := evaluator.Evaluate(context.Background(), "some code", criteria, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only have 1 call (no reconsideration)
	if callCount != 1 {
		t.Errorf("expected 1 LLM call (no reconsideration without steps), got %d", callCount)
	}

	// Criterion should be failed
	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(evalResult.Failed))
	}
	if evalResult.AllPassed {
		t.Error("expected AllPassed to be false")
	}
}
