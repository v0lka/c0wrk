package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdktools "github.com/v0lka/sp4rk/tools"
)

const toolExecutePlanDescription = `Execute all steps of the declared plan in DAG order, with parallelism for independent steps. Each step runs as an isolated executor with its own context, tool set, and checklist. Plan-step executors emit plan_step_start/plan_step_complete events (not subagent events), so the full plan workflow — progress tracking, checklist nesting, plan panel — works correctly. Call this ONCE after declare_plan approval; it runs ALL steps to completion (in dependency-ordered waves) and returns aggregated results. Available only when a plan has been declared via declare_plan. Do NOT use delegate for plan steps — execute_plan is the only execution path for a declared plan.`

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
