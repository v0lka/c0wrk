package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/user/agent/core/prompts"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
	tools "github.com/user/agent/sdk/tools"
)

// Plan mode template content.
const (
	planModePreamble = `You are a task planner. Decompose the user's task into a DAG (directed acyclic graph) of execution steps.

Each step should be atomic and executable by a single agent with access to tools.
Steps can depend on other steps (DependsOn) and can be parallelizable.

## Granularity

Prefer fewer, broader steps over many granular ones. Each step should represent meaningful progress, not a single tool call.

- Simple tasks (complexity 1-2): 1-2 steps
- Medium tasks (complexity 3): 2-4 steps
- Complex tasks (complexity 4-5): 3-7 steps

Limit plans to 10 steps maximum. If a task seems to require more, combine related work into broader steps.
`

	planModeDomainAssignment = `
Domain controls how the agent's context window is compacted during long executions:

- "code" → sliding window (keeps recent file edits visible)
- "research" → summarization (condenses findings into key points)
- "general" → sliding window; switches to hierarchical if plan complexity ≥ 4

Choose the domain that matches the **primary activity** of the step, not its subject matter:

- A step that _reads and analyzes_ source code to produce a report is "research" (primary activity: information gathering).
- A step that _modifies_ source files or runs build/test commands is "code" (primary activity: file mutation).
- Use "general" only when a step genuinely mixes activities and cannot be split further.

**Wrong domain → wrong compaction → degraded context quality.** A research step with domain "code" will lose synthesized findings to sliding window eviction. A coding step with domain "research" will lose recent edits to summarization.

For each step:
1. Identify the primary activity (reading/analyzing vs modifying files vs mixed)
2. Match to the domain that fits the primary activity
3. Prefer a specific domain ("code" or "research") over "general" when the activity is clear
`

	planModeAgentProfiles = `
Assign specialized profiles when it adds clear value. Omit profile for simple tasks.
Prefer higher-tier tools over bash_exec in all profiles:

- "researcher": information gathering, analysis (tools: web_search, web_fetch, ripgrep, glob, file_ops)
- "coder": implementation, file operations (tools: file_ops, ripgrep, glob; bash_exec for build/run/test)
- "tester": test execution, verification (tools: bash_exec, ripgrep, glob, file_ops)
- "executor": general purpose (default, all tools — follow tool priority tiers)`

	planModeExtraSections = `
## Output Expectations

- "researcher" / "tester": Pass all results through the finish tool. Write files ONLY for final deliverables.
- "coder": Write code/config files as needed. Summarize what was done through finish.
- "executor": Files only when the file IS the deliverable.

## Parallelization

Steps are parallelizable when they have NO data dependencies — step B can run in parallel with step A only if B does not need A's output. If B needs A's output, B MUST list A in depends_on.

## Fields

- ` + "`estimated_tools`" + `: Informational hint about likely tools. Not a constraint — the executor may use any available tool.
`

	planModeTail = "REFLECTIONS\n"

	planModeJSONExample = `{"steps": [{"id": "step_1", "description": "...", "depends_on": [], "parallelizable": true, "estimated_tools": ["tool1"], "profile": {"role": "coder", "allowed_tools": ["file_ops", "ripgrep", "glob", "bash_exec"], "domain": "code"}}]}`
)

// Continuation mode template content.
const (
	continuationModePreamble = `You are a planning agent that creates continuation plans for follow-up requests.

A task was completed successfully, and the user has sent a follow-up message. Create a plan with ONLY new steps to address the follow-up.

## Context

Original request:
ORIGINAL-REQUEST

Completed plan (step summaries):
COMPLETED-PLAN-SUMMARY

## Instructions

1. Analyze the new user message to understand what additional work is needed.
2. Create ONLY new steps that address the follow-up request.
3. New step IDs MUST be prefixed with ` + "`continuation_`" + ` (e.g., "continuation_1", "continuation_2").
4. New steps MUST reference the terminal steps of the existing plan in their DependsOn field.
5. Keep the same granularity and style as the original plan.
6. Focus ONLY on new steps that address the follow-up request.

## Terminal Steps

The following steps are the terminal (final) steps of the completed plan. New steps should depend on these:
TERMINAL-STEPS
`

	continuationModeDomainAssignment = `
Domain controls how the agent's context window is compacted during long executions:

- "code" → sliding window (keeps recent file edits visible)
- "research" → summarization (condenses findings into key points)
- "general" → sliding window; switches to hierarchical if plan complexity ≥ 4

Choose the domain that matches the **primary activity** of the step.
`

	continuationModeAgentProfiles = `
Assign specialized profiles when it adds clear value. Omit profile for simple tasks.

- "researcher": information gathering, analysis (tools: web_search, web_fetch, ripgrep, glob, file_ops)
- "coder": implementation, file operations (tools: file_ops, ripgrep, glob; bash_exec for build/run/test)
- "tester": test execution, verification (tools: bash_exec, ripgrep, glob, file_ops)
- "executor": general purpose (default, all tools)`

	continuationModeExtraSections = ""
	continuationModeTail          = ""
	continuationModeJSONExample   = `{"steps": [{"id": "continuation_1", "description": "...", "depends_on": ["TERMINAL-STEP-IDS"], "parallelizable": true, "estimated_tools": ["tool1"], "profile": {"role": "coder", "allowed_tools": ["file_ops", "ripgrep", "glob", "bash_exec"], "domain": "code"}}]}`
)

// compile-time check: Planner implements orchestration.Planner.
var _ orchestration.Planner = (*Planner)(nil)

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
	availableTools []tools.ToolDescriptor,
	reflections []Reflection,
) (*Plan, error) {
	systemPrompt := p.buildPlanSystemPrompt(ctx, availableTools, reflections)

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
	sessionReflections []Reflection,
) (*Plan, error) {
	systemPrompt := p.buildReplanSystemPrompt(ctx, originalPlan, completedSteps, failedStep, reflection, sessionReflections)

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

// PlanContinuation generates a continuation plan for follow-up requests after task completion.
func (p *Planner) PlanContinuation(
	ctx context.Context,
	originalRequest string,
	existingPlan *Plan,
	completedSteps []CompletedStep,
	newMessage string,
	availableTools []tools.ToolDescriptor,
) (*Plan, error) {
	systemPrompt := p.buildContinuationSystemPrompt(ctx, originalRequest, existingPlan, completedSteps, availableTools)

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: newMessage},
	}

	req := llm.ChatRequest{
		Messages: messages,
	}

	resp, err := p.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("planner continuation LLM call failed: %w", err)
	}

	plan, err := p.parsePlanResponse(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse continuation plan: %w", err)
	}

	return plan, nil
}

// buildPlanSystemPrompt constructs the system prompt for initial planning.
func (p *Planner) buildPlanSystemPrompt(
	ctx context.Context,
	availableTools []tools.ToolDescriptor,
	reflections []Reflection,
) string {
	// Build available tools string (grouped by priority tier)
	availableToolsStr := agent.BuildGroupedToolList(availableTools)

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

	// Apply template substitutions using base template
	result := prompts.PlannerBase
	result = strings.ReplaceAll(result, "MODE-PREAMBLE", planModePreamble)
	result = strings.ReplaceAll(result, "DOMAIN-ASSIGNMENT", planModeDomainAssignment)
	result = strings.ReplaceAll(result, "AGENT-PROFILES", planModeAgentProfiles)
	result = strings.ReplaceAll(result, "MODE-EXTRA-SECTIONS", planModeExtraSections)
	result = strings.ReplaceAll(result, "MODE-TAIL", planModeTail)
	result = strings.ReplaceAll(result, "MODE-JSON-EXAMPLE", planModeJSONExample)
	result = strings.ReplaceAll(result, "AVAILABLE-TOOLS", availableToolsStr)
	result = strings.ReplaceAll(result, "REFLECTIONS", reflectionsStr)
	result = strings.ReplaceAll(result, "WORKSPACE-PATH", formatWorkspacePath(ctx))

	// Append environment context if available.
	if envBlock := tools.FormatFullEnvBlock(tools.EnvInfoFrom(ctx)); envBlock != "" {
		result += "\n\n" + envBlock
	}

	return result
}

// buildReplanSystemPrompt constructs the system prompt for replanning after failure.
func (p *Planner) buildReplanSystemPrompt(
	ctx context.Context,
	originalPlan *Plan,
	completedSteps []CompletedStep,
	failedStep CompletedStep,
	reflection *Reflection,
	sessionReflections []Reflection,
) string {
	// Build original plan string
	var originalPlanStr string
	planJSON, err := json.MarshalIndent(originalPlan, "", "  ")
	if err != nil {
		// Fallback to Go's default formatting if JSON marshaling fails
		originalPlanStr = fmt.Sprintf("%+v", originalPlan)
	} else {
		originalPlanStr = string(planJSON)
	}

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

	// Build previous session reflections string (cross-attempt pattern visibility)
	var prevReflectionsStr string
	if len(sessionReflections) > 0 {
		var prb strings.Builder
		prb.WriteString("Previous session reflections (showing cross-attempt failure patterns):\n")
		for i, r := range sessionReflections {
			fmt.Fprintf(&prb, "%d. Summary: %s | Root cause: %s | Action plan: %s | Suggested: %s\n",
				i+1, r.Summary, r.RootCause, r.ActionPlan, r.SuggestedAction)
		}
		prevReflectionsStr = prb.String()
	}

	// Apply template substitutions.
	// PREVIOUS-SESSION-REFLECTIONS must be replaced before CURRENT-REFLECTION
	// to avoid substring collision.
	result := prompts.PlannerReplan
	result = strings.ReplaceAll(result, "ORIGINAL-PLAN", originalPlanStr)
	result = strings.ReplaceAll(result, "COMPLETED-STEPS", completedStepsStr)
	result = strings.ReplaceAll(result, "FAILED-STEP", failedStepStr)
	result = strings.ReplaceAll(result, "PREVIOUS-SESSION-REFLECTIONS", prevReflectionsStr)
	result = strings.ReplaceAll(result, "CURRENT-REFLECTION", reflectionStr)
	result = strings.ReplaceAll(result, "WORKSPACE-PATH", formatWorkspacePath(ctx))

	// Append environment context if available.
	if envBlock := tools.FormatFullEnvBlock(tools.EnvInfoFrom(ctx)); envBlock != "" {
		result += "\n\n" + envBlock
	}

	return result
}

// buildContinuationSystemPrompt constructs the system prompt for continuation planning.
func (p *Planner) buildContinuationSystemPrompt(
	ctx context.Context,
	originalRequest string,
	existingPlan *Plan,
	completedSteps []CompletedStep,
	availableTools []tools.ToolDescriptor,
) string {
	// Build completed plan summary (step IDs + descriptions + summaries)
	var planSummaryBuilder strings.Builder
	for _, step := range existingPlan.Steps {
		// Find the completed step result for this step
		var summary string
		for _, cs := range completedSteps {
			if cs.StepID == step.ID {
				if cs.Output != "" {
					summary = cs.Output
				}
				break
			}
		}
		fmt.Fprintf(&planSummaryBuilder, "- %s: %s", step.ID, step.Description)
		if summary != "" {
			fmt.Fprintf(&planSummaryBuilder, " → %s", summary)
		}
		planSummaryBuilder.WriteString("\n")
	}
	completedPlanSummary := planSummaryBuilder.String()

	// Find terminal steps (steps that have no dependents)
	terminalSteps := findTerminalSteps(existingPlan)
	terminalStepsStr := strings.Join(terminalSteps, ", ")

	// Build available tools string (grouped by priority tier)
	availableToolsStr := agent.BuildGroupedToolList(availableTools)

	// Apply template substitutions using base template
	result := prompts.PlannerBase
	result = strings.ReplaceAll(result, "MODE-PREAMBLE", continuationModePreamble)
	result = strings.ReplaceAll(result, "DOMAIN-ASSIGNMENT", continuationModeDomainAssignment)
	result = strings.ReplaceAll(result, "AGENT-PROFILES", continuationModeAgentProfiles)
	result = strings.ReplaceAll(result, "MODE-EXTRA-SECTIONS", continuationModeExtraSections)
	result = strings.ReplaceAll(result, "MODE-TAIL", continuationModeTail)
	result = strings.ReplaceAll(result, "MODE-JSON-EXAMPLE", continuationModeJSONExample)
	result = strings.ReplaceAll(result, "ORIGINAL-REQUEST", originalRequest)
	result = strings.ReplaceAll(result, "COMPLETED-PLAN-SUMMARY", completedPlanSummary)
	result = strings.ReplaceAll(result, "TERMINAL-STEPS", terminalStepsStr)
	result = strings.ReplaceAll(result, "AVAILABLE-TOOLS", availableToolsStr)
	result = strings.ReplaceAll(result, "WORKSPACE-PATH", formatWorkspacePath(ctx))

	// Append environment context if available.
	if envBlock := tools.FormatFullEnvBlock(tools.EnvInfoFrom(ctx)); envBlock != "" {
		result += "\n\n" + envBlock
	}

	return result
}

// findTerminalSteps returns the IDs of steps that have no dependents (terminal steps in the DAG).
func findTerminalSteps(plan *Plan) []string {
	// Track which steps are depended on
	dependedOn := make(map[string]bool)
	for _, step := range plan.Steps {
		for _, dep := range step.DependsOn {
			dependedOn[dep] = true
		}
	}

	// Terminal steps are those not depended on by any other step
	var terminal []string
	for _, step := range plan.Steps {
		if !dependedOn[step.ID] {
			terminal = append(terminal, step.ID)
		}
	}
	return terminal
}

// formatWorkspacePath returns the workspace instruction block if a workspace path is set.
func formatWorkspacePath(ctx context.Context) string {
	wp := tools.WorkspacePathFrom(ctx)
	if wp == "" {
		return ""
	}
	return fmt.Sprintf("Session workspace: %s\nWhen steps produce file artifacts, they must be created inside this workspace unless the task explicitly specifies an external location.", wp)
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
