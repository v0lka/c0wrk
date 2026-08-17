package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdktools "github.com/v0lka/sp4rk/tools"
)

const toolExecutePlanDescription = `Purpose: start executing the approved roadmap — its tasks, in dependency order, with per-task verification.
Use when: the user has approved the plan; call ONCE per plan. Work tasks in the specified order (or in explicit parallel groups), verify each against its acceptance criteria before moving on, and report progress after each task.
Inputs: none — it executes the plan already declared via declare_plan; the steps, their order, and per-step agents come from that declaration.
Outputs: the execution run — per-task progress events and a completion report (tasks done/failed, checks run).
Example: a plan where task 2 declares depends_on ["task_1"] and runs after it.
Anti-example: do not call to delegate a single task (delegate does that); not before the roadmap is approved; never implement unapproved additions.`

// PlanStepExecutor builds and runs all steps of a declared plan in DAG order
// with parallelism. The implementation lives in the core layer
// (conductorLauncher.ExecutePlan).
type PlanStepExecutor interface {
	Execute(ctx context.Context) ([]PlanStepResult, error)
}

// PlanStepResult is the outcome of a single plan-step execution.
type PlanStepResult struct {
	StepID  string
	Summary string
	Status  string // "completed" | "failed"
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
	"properties": {},
	"additionalProperties": false
}`),
			Policy: sdktools.PolicyAlwaysAllow,
		},
	}
}

func (t *ExecutePlanTool) Execute(ctx context.Context, _ json.RawMessage) (sdktools.ToolResult, error) {
	executor := PlanStepExecutorFrom(ctx)
	if executor == nil {
		return sdktools.ErrorResult("execute_plan: no plan step executor in context (not running inside a Conductor)"), nil
	}

	results, err := executor.Execute(ctx)
	if err != nil {
		return sdktools.ErrorResult("execute_plan: %v", err), nil
	}

	return buildExecutePlanResult(results), nil
}

func buildExecutePlanResult(results []PlanStepResult) sdktools.ToolResult {
	var completed, failed []string
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
		}
		line := fmt.Sprintf("[%s] %s — %s", r.StepID, r.Summary, status)
		if r.Error != nil {
			line += fmt.Sprintf(": %v", r.Error)
		}
		if r.Output != "" {
			line += "\n" + r.Output
		}
		summaries = append(summaries, line)
	}

	var b string
	if len(failed) > 0 {
		b = fmt.Sprintf("Plan execution completed with %d succeeded, %d failed.\n\n%s",
			len(completed), len(failed), joinResults(summaries))
	} else {
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
