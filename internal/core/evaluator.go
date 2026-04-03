package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/user/agent/internal/core/prompts"
	"github.com/user/agent/internal/llm"
)

// Evaluator checks execution results against acceptance criteria.
type Evaluator struct {
	tools ToolExecutor // for programmatic checks (bash_exec)
	llm   LLMCaller    // for llm_judge checks
}

// NewEvaluator creates a new Evaluator.
func NewEvaluator(tools ToolExecutor, caller LLMCaller) *Evaluator {
	return &Evaluator{
		tools: tools,
		llm:   caller,
	}
}

// Evaluate checks the result against all acceptance criteria.
func (e *Evaluator) Evaluate(ctx context.Context, result string, criteria []AcceptanceCriterion) (*EvalResult, error) {
	evalResult := &EvalResult{
		Passed:  []EvalDetail{},
		Failed:  []EvalDetail{},
		Unclear: []EvalDetail{},
	}

	for _, criterion := range criteria {
		var detail EvalDetail
		var err error

		switch criterion.CheckType {
		case "programmatic":
			detail, err = e.evaluateProgrammatic(ctx, criterion)
		case "llm_judge":
			detail, err = e.evaluateLLMJudge(ctx, criterion, result)
		default:
			// Unknown check type - mark as unclear
			detail = EvalDetail{
				Criterion:  criterion,
				Diagnostic: "unknown check type: " + criterion.CheckType,
			}
			evalResult.Unclear = append(evalResult.Unclear, detail)
			continue
		}

		if err != nil {
			return nil, err
		}

		// Categorize based on diagnostic prefix convention
		// The evaluation methods set Diagnostic to indicate pass/fail
		switch {
		case detail.Diagnostic != "" && strings.HasPrefix(detail.Diagnostic, "PASSED:"):
			evalResult.Passed = append(evalResult.Passed, detail)
		case detail.Diagnostic != "" && strings.HasPrefix(detail.Diagnostic, "FAILED:"):
			evalResult.Failed = append(evalResult.Failed, detail)
		default:
			// Default to unclear if no prefix or UNCLEAR prefix
			evalResult.Unclear = append(evalResult.Unclear, detail)
		}
	}

	// AllPassed = no failures (unclear is a judge quality issue, not execution failure)
	evalResult.AllPassed = len(evalResult.Failed) == 0

	return evalResult, nil
}

// evaluateProgrammatic runs a programmatic check via bash_exec.
func (e *Evaluator) evaluateProgrammatic(ctx context.Context, criterion AcceptanceCriterion) (EvalDetail, error) {
	// Create JSON input for bash_exec
	input := map[string]string{"command": criterion.CheckCmd}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return EvalDetail{}, fmt.Errorf("failed to marshal bash_exec input: %w", err)
	}

	// Execute the command
	toolResult, err := e.tools.Execute(ctx, "bash_exec", inputJSON)
	if err != nil {
		return EvalDetail{}, fmt.Errorf("bash_exec failed: %w", err)
	}

	detail := EvalDetail{
		Criterion: criterion,
	}

	if toolResult.IsError {
		// Command failed (non-zero exit code)
		detail.Diagnostic = "FAILED:" + toolResult.Content
	} else {
		// Command succeeded (exit code 0)
		detail.Diagnostic = "PASSED:" + toolResult.Content
	}

	return detail, nil
}

// evaluateLLMJudge uses LLM to evaluate whether a criterion is met.
func (e *Evaluator) evaluateLLMJudge(ctx context.Context, criterion AcceptanceCriterion, result string) (EvalDetail, error) {
	// Build evaluation prompt
	prompt := strings.ReplaceAll(prompts.EvaluatorJudge, "CRITERION", criterion.Description)
	prompt = strings.ReplaceAll(prompt, "RESULT", result)

	// Create chat request
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	// Call LLM
	resp, err := e.llm.Call(ctx, req)
	if err != nil {
		return EvalDetail{}, fmt.Errorf("LLM judge call failed: %w", err)
	}

	detail := EvalDetail{
		Criterion: criterion,
	}

	// Parse response
	responseText := strings.TrimSpace(resp.Message.Content)
	upperResponse := strings.ToUpper(responseText)

	switch {
	case strings.HasPrefix(upperResponse, "YES"):
		detail.Diagnostic = "PASSED:" + responseText
	case strings.HasPrefix(upperResponse, "NO"):
		detail.Diagnostic = "FAILED:" + responseText
	default:
		detail.Diagnostic = "UNCLEAR:" + responseText
	}

	return detail, nil
}
