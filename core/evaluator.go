package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/user/agent/core/prompts"
	"github.com/user/agent/sdk/llm"
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
func (e *Evaluator) Evaluate(ctx context.Context, result string, criteria []AcceptanceCriterion, steps []Step) (*EvalResult, error) {
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
			detail, err = e.evaluateLLMJudge(ctx, criterion, result, steps)
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

	// Reconsider failed llm_judge criteria when execution evidence is available
	if steps != nil && len(evalResult.Failed) > 0 {
		var reconsidered []EvalDetail
		var stillFailed []EvalDetail
		for _, detail := range evalResult.Failed {
			if detail.Criterion.CheckType == "llm_judge" {
				passed, newDiagnostic, err := e.reconsiderCriterion(ctx, detail.Criterion, result, steps, detail.Diagnostic)
				if err != nil {
					// On error, keep original verdict
					stillFailed = append(stillFailed, detail)
					continue
				}
				if passed {
					reconsidered = append(reconsidered, EvalDetail{
						Criterion:          detail.Criterion,
						Diagnostic:         newDiagnostic,
						Reconsidered:       true,
						OriginalDiagnostic: detail.Diagnostic,
					})
				} else {
					stillFailed = append(stillFailed, detail)
				}
			} else {
				stillFailed = append(stillFailed, detail)
			}
		}
		if len(reconsidered) > 0 {
			evalResult.Passed = append(evalResult.Passed, reconsidered...)
			evalResult.Failed = stillFailed
			evalResult.AllPassed = len(evalResult.Failed) == 0
		}
	}

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
func (e *Evaluator) evaluateLLMJudge(ctx context.Context, criterion AcceptanceCriterion, result string, steps []Step) (EvalDetail, error) {
	// Build evidence section from execution trajectory
	evidenceSection := ""
	if len(steps) > 0 {
		evidenceSection = "Execution Evidence:\n" + buildEvidenceSection(steps)
	}

	// Build evaluation prompt
	prompt := strings.ReplaceAll(prompts.EvaluatorJudge, "CRITERION", criterion.Description)
	prompt = strings.ReplaceAll(prompt, "RESULT", result)
	prompt = strings.ReplaceAll(prompt, "EVIDENCE_SECTION", evidenceSection)

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

// buildEvidenceSection formats execution steps into a readable evidence section.
func buildEvidenceSection(steps []Step) string {
	if len(steps) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, step := range steps {
		fmt.Fprintf(&sb, "### Step %d\n", i+1)
		if step.Thought != "" {
			fmt.Fprintf(&sb, "**Thought:** %s\n", step.Thought)
		}
		if step.Action.Name != "" {
			fmt.Fprintf(&sb, "**Action:** %s\n", step.Action.Name)
			if len(step.Action.Input) > 0 {
				fmt.Fprintf(&sb, "**Input:** %s\n", string(step.Action.Input))
			}
		}
		if step.Observation != "" {
			fmt.Fprintf(&sb, "**Observation:** %s\n", step.Observation)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// reconsiderCriterion re-evaluates a failed criterion using execution evidence.
func (e *Evaluator) reconsiderCriterion(
	ctx context.Context,
	criterion AcceptanceCriterion,
	result string,
	steps []Step,
	originalDiagnostic string,
) (passed bool, diagnostic string, err error) {
	// Build evidence section from steps
	evidence := buildEvidenceSection(steps)

	prompt := strings.ReplaceAll(prompts.EvaluatorReconsider, "CRITERION", criterion.Description)
	prompt = strings.ReplaceAll(prompt, "ORIGINAL_DIAGNOSTIC", originalDiagnostic)
	prompt = strings.ReplaceAll(prompt, "RESULT", result)
	prompt = strings.ReplaceAll(prompt, "EVIDENCE", evidence)

	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
	}

	resp, err := e.llm.Call(ctx, req)
	if err != nil {
		return false, "", fmt.Errorf("reconsideration LLM call: %w", err)
	}

	answer := strings.TrimSpace(resp.Message.Content)
	upper := strings.ToUpper(answer)
	if strings.HasPrefix(upper, "YES") {
		return true, "RECONSIDERED_PASSED: " + answer, nil
	}
	return false, "RECONSIDERED_FAILED: " + answer, nil
}
