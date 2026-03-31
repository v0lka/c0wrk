package core

import (
	"context"
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

	evalResult, err := evaluator.Evaluate(context.Background(), "", criteria)
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

	evalResult, err := evaluator.Evaluate(context.Background(), "", criteria)
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

	evalResult, err := evaluator.Evaluate(context.Background(), "func doSomething() error { return nil }", criteria)
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

	evalResult, err := evaluator.Evaluate(context.Background(), "func doSomething() { panic(\"oops\") }", criteria)
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

	evalResult, err := evaluator.Evaluate(context.Background(), "func foo() {}", criteria)
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

	evalResult, err := evaluator.Evaluate(context.Background(), "func wellWritten() error { return nil }", criteria)
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

	evalResult, err := evaluator.Evaluate(context.Background(), "some code", criteria)
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
