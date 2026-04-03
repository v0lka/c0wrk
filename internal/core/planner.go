package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/user/agent/internal/core/prompts"
	"github.com/user/agent/internal/llm"
	"github.com/user/agent/internal/tools"
)

// Planner generates DAG execution plans for complex tasks.
type Planner struct {
	llm LLMCaller
}

// NewPlanner creates a new Planner with the given LLM caller.
func NewPlanner(caller LLMCaller) *Planner {
	return &Planner{llm: caller}
}

// Plan generates a DAG execution plan for the given task.
func (p *Planner) Plan(
	ctx context.Context,
	task string,
	criteria []AcceptanceCriterion,
	availableTools []tools.ToolDescriptor,
	reflections []Reflection,
	constitution []string,
) (*Plan, error) {
	systemPrompt := p.buildPlanSystemPrompt(availableTools, criteria, reflections, constitution)

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: task},
	}

	req := llm.ChatRequest{
		Messages: messages,
	}

	resp, err := p.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("planner LLM call failed: %w", err)
	}

	plan, err := p.parsePlanResponse(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse plan response: %w", err)
	}

	return plan, nil
}

// Replan generates an updated plan after a step failure.
func (p *Planner) Replan(
	ctx context.Context,
	originalPlan *Plan,
	completedSteps []CompletedStep,
	failedStep CompletedStep,
	reflection *Reflection,
	criteria []AcceptanceCriterion,
) (*Plan, error) {
	systemPrompt := p.buildReplanSystemPrompt(originalPlan, completedSteps, failedStep, reflection, criteria)

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "Please provide the updated plan."},
	}

	req := llm.ChatRequest{
		Messages: messages,
	}

	resp, err := p.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("planner replan LLM call failed: %w", err)
	}

	plan, err := p.parsePlanResponse(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse replan response: %w", err)
	}

	return plan, nil
}

// buildPlanSystemPrompt constructs the system prompt for initial planning.
func (p *Planner) buildPlanSystemPrompt(
	availableTools []tools.ToolDescriptor,
	criteria []AcceptanceCriterion,
	reflections []Reflection,
	constitution []string,
) string {
	// Build available tools string
	var toolsBuilder strings.Builder
	for _, t := range availableTools {
		fmt.Fprintf(&toolsBuilder, "- %s: %s\n", t.Name, t.Description)
	}
	availableToolsStr := toolsBuilder.String()

	// Build acceptance criteria string
	var criteriaBuilder strings.Builder
	for _, ac := range criteria {
		fmt.Fprintf(&criteriaBuilder, "- %s: %s\n", ac.ID, ac.Description)
	}
	criteriaStr := criteriaBuilder.String()

	// Build reflections string
	var reflectionsStr string
	if len(reflections) > 0 {
		var rb strings.Builder
		rb.WriteString("Reflections from past attempts (learn from them):\n")
		for i, r := range reflections {
			fmt.Fprintf(&rb, "%d. Failure: %s | Root cause: %s | Action plan: %s\n",
				i+1, r.FailureAnalysis, r.RootCause, r.ActionPlan)
		}
		reflectionsStr = rb.String()
	}

	// Build constitution string
	var constitutionStr string
	if len(constitution) > 0 {
		var cb strings.Builder
		cb.WriteString("Constitution principles (follow them):\n")
		for _, principle := range constitution {
			fmt.Fprintf(&cb, "- %s\n", principle)
		}
		constitutionStr = cb.String()
	}

	// Apply template substitutions
	result := prompts.PlannerPlan
	result = strings.ReplaceAll(result, "AVAILABLE-TOOLS", availableToolsStr)
	result = strings.ReplaceAll(result, "ACCEPTANCE-CRITERIA", criteriaStr)
	result = strings.ReplaceAll(result, "REFLECTIONS", reflectionsStr)
	result = strings.ReplaceAll(result, "CONSTITUTION", constitutionStr)

	return result
}

// buildReplanSystemPrompt constructs the system prompt for replanning after failure.
func (p *Planner) buildReplanSystemPrompt(
	originalPlan *Plan,
	completedSteps []CompletedStep,
	failedStep CompletedStep,
	reflection *Reflection,
	criteria []AcceptanceCriterion,
) string {
	// Build original plan string
	planJSON, _ := json.MarshalIndent(originalPlan, "", "  ")
	originalPlanStr := string(planJSON)

	// Build completed steps string
	var completedBuilder strings.Builder
	for _, cs := range completedSteps {
		fmt.Fprintf(&completedBuilder, "- %s: %s\n", cs.StepID, cs.Output)
	}
	completedStepsStr := completedBuilder.String()

	// Build failed step string
	var failedStepBuilder strings.Builder
	failedStepBuilder.WriteString(failedStep.StepID + "\n")
	if failedStep.Error != nil {
		failedStepBuilder.WriteString("Error: " + failedStep.Error.Error() + "\n")
	}
	failedStepBuilder.WriteString("Output: " + failedStep.Output)
	failedStepStr := failedStepBuilder.String()

	// Build reflection string
	var reflectionStr string
	if reflection != nil {
		reflectionStr = fmt.Sprintf(`Reflection on failure:
- Failure analysis: %s
- Root cause: %s
- Action plan: %s
`, reflection.FailureAnalysis, reflection.RootCause, reflection.ActionPlan)
	}

	// Build acceptance criteria string
	var criteriaBuilder strings.Builder
	for _, ac := range criteria {
		fmt.Fprintf(&criteriaBuilder, "- %s: %s\n", ac.ID, ac.Description)
	}
	criteriaStr := criteriaBuilder.String()

	// Apply template substitutions
	result := prompts.PlannerReplan
	result = strings.ReplaceAll(result, "ORIGINAL-PLAN", originalPlanStr)
	result = strings.ReplaceAll(result, "COMPLETED-STEPS", completedStepsStr)
	result = strings.ReplaceAll(result, "FAILED-STEP", failedStepStr)
	result = strings.ReplaceAll(result, "REFLECTION", reflectionStr)
	result = strings.ReplaceAll(result, "ACCEPTANCE-CRITERIA", criteriaStr)

	return result
}

// parsePlanResponse extracts a Plan from the LLM response content.
func (p *Planner) parsePlanResponse(content string) (*Plan, error) {
	// Try to find JSON in the response
	content = strings.TrimSpace(content)

	// Handle markdown code blocks
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		if idx := strings.Index(content, "```"); idx != -1 {
			content = content[:idx]
		}
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		if idx := strings.Index(content, "```"); idx != -1 {
			content = content[:idx]
		}
	}

	content = strings.TrimSpace(content)

	// Find JSON object boundaries
	startIdx := strings.Index(content, "{")
	endIdx := strings.LastIndex(content, "}")

	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		return nil, errors.New("no valid JSON object found in response")
	}

	jsonContent := content[startIdx : endIdx+1]

	var plan Plan
	if err := json.Unmarshal([]byte(jsonContent), &plan); err != nil {
		return nil, fmt.Errorf("failed to unmarshal plan JSON: %w", err)
	}

	return &plan, nil
}
