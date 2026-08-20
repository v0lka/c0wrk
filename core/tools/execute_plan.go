package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdktools "github.com/v0lka/sp4rk/tools"
)

const toolExecutePlanDescription = `Purpose: execute the approved roadmap — its tasks, in dependency order, with per-task verification.
Use when: the user has approved the plan. Call with no arguments for the first run; call again to RESUME after a failure — already-successful steps are skipped and failed/unstarted steps are re-run.
Inputs: optionally {"steps": ["step_2"]} to force re-running specific steps (plus any steps that depend on them). Omit for a full first run or a resume of all failed steps. The plan itself comes from declare_plan.
Outputs: the execution run — per-task progress events and a completion report (tasks done/failed, checks run).
Example: a plan where task 2 declares depends_on ["task_1"] and runs after it; to retry only task 2 after it failed, call execute_plan with {"steps": ["task_2"]}.
Anti-example: do not call to delegate a single task (delegate does that); not before the roadmap is approved; never implement unapproved additions.`

// PlanStepExecutor builds and runs all steps of a declared plan in DAG order
// with parallelism. The implementation lives in the core layer
// (conductorLauncher.Execute). stepIDs, when non-empty, forces those steps
// (and their transitive dependents) to re-run; otherwise a second call resumes
// by skipping already-successful steps.
type PlanStepExecutor interface {
	Execute(ctx context.Context, stepIDs []string) ([]PlanStepResult, error)
}

// PlanStepResult is the outcome of a single plan-step execution.
type PlanStepResult struct {
	StepID  string
	Summary string
	Status  string // "completed" | "failed" | "paused"
	Output  string
	Error   error
}

// ExecutePlanTool executes the declared plan's steps via a PlanStepExecutor
// injected through context.
type ExecutePlanTool struct {
	*sdktools.BaseTool
}

// NewExecutePlanTool creates the execute_plan tool.
func NewExecutePlanTool() *ExecutePlanTool {
	return &ExecutePlanTool{
		BaseTool: &sdktools.BaseTool{
			ToolGroup:       sdktools.GroupSystem,
			ToolName:        "execute_plan",
			ToolDescription: toolExecutePlanDescription,
			Schema: json.RawMessage(`{
	"type": "object",
	"properties": {
		"steps": {
			"type": "array",
			"items": {"type": "string"},
			"description": "Optional step IDs to force re-run (with their transitive dependents). Omit to run/resume all eligible steps."
		}
	},
	"additionalProperties": false
}`),
			Policy: sdktools.PolicyAlwaysAllow,
		},
	}
}

func (t *ExecutePlanTool) Execute(ctx context.Context, raw json.RawMessage) (sdktools.ToolResult, error) {
	executor := PlanStepExecutorFrom(ctx)
	if executor == nil {
		return sdktools.ErrorResult("execute_plan: no plan step executor in context (not running inside a Conductor)"), nil
	}

	var input executePlanInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			return sdktools.ErrorResult("execute_plan: invalid input: %v", err), nil
		}
	}

	results, err := executor.Execute(ctx, input.Steps)
	if err != nil {
		return sdktools.ErrorResult("execute_plan: %v", err), nil
	}

	return buildExecutePlanResult(results), nil
}

// executePlanInput is the optional input for the execute_plan tool. Steps, when
// non-empty, forces those steps (and their transitive dependents) to re-run.
type executePlanInput struct {
	Steps []string `json:"steps"`
}

func buildExecutePlanResult(results []PlanStepResult) sdktools.ToolResult {
	var completed, failed, paused []string
	summaries := make([]string, 0, len(results))
	for _, r := range results {
		status := r.Status
		if status == "" {
			if r.Error != nil {
				status = "failed"
			} else {
				status = "completed"
			}
		}
		switch status {
		case "completed":
			completed = append(completed, r.StepID)
		case "failed":
			failed = append(failed, r.StepID)
		case "paused":
			paused = append(paused, r.StepID)
		}
		line := fmt.Sprintf("[%s] %s — %s", r.StepID, r.Summary, status)
		if r.Error != nil && status != "paused" {
			line += fmt.Sprintf(": %v", r.Error)
		}
		if r.Output != "" {
			line += "\n" + r.Output
		}
		summaries = append(summaries, line)
	}

	var b string
	switch {
	case len(paused) > 0:
		b = fmt.Sprintf("Plan execution paused — %d succeeded, %d failed, %d paused.\n\n%s\n\nRe-invoke execute_plan to resume the paused steps.",
			len(completed), len(failed), len(paused), joinResults(summaries))
	case len(failed) > 0:
		b = fmt.Sprintf("Plan execution completed with %d succeeded, %d failed.\n\n%s",
			len(completed), len(failed), joinResults(summaries))
	default:
		b = fmt.Sprintf("Plan execution completed — %d step(s) succeeded.\n\n%s",
			len(completed), joinResults(summaries))
	}
	return sdktools.ToolResult{Content: b}
}

func joinResults(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("\n\n")
		b.WriteString(l)
	}
	return b.String()
}

// --- Context plumbing ---

type planStepExecutorKey struct{}

// WithPlanStepExecutor injects the executor into the context so the
// execute_plan tool can call it without importing core.
func WithPlanStepExecutor(ctx context.Context, executor PlanStepExecutor) context.Context {
	return context.WithValue(ctx, planStepExecutorKey{}, executor)
}

// PlanStepExecutorFrom extracts the executor from the context, or returns nil.
func PlanStepExecutorFrom(ctx context.Context) PlanStepExecutor {
	if v, ok := ctx.Value(planStepExecutorKey{}).(PlanStepExecutor); ok {
		return v
	}
	return nil
}
