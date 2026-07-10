//go:build fluent

// Example 06 — Plan & Reflect Orchestration (Fluent API)
//
// The same Plan & Execute orchestration as main.go, but the ~80-line hand-rolled
// loop (Planner → DAG → Conductor → retry → Reflect) collapses into a single
// fw.TaskF chain. The default PromptSet and reflector prompt are applied
// automatically; only the task, system prompt, workspace, and retry budget
// need configuring.
//
// Run with: go run -tags fluent .
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/llm"
)

func run() error {
	// Build the Framework: provider + file tools + per-step budget + auto-approve.
	// The finish tool is auto-registered so each step can signal completion.
	fw, err := sp4rk.NewF().
		Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
		FileTools().   // read, write, edit, list, glob, mkdir
		MaxSteps(15).  // per-step ReAct budget
		AutoApprove(). // throwaway workspace — satisfy fail-closed registry
		Build()
	if err != nil {
		return fmt.Errorf("failed to create framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	workspaceDir, err := os.MkdirTemp("", "sp4rk-example-06-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()
	fmt.Println("Workspace:", workspaceDir)

	task := fmt.Sprintf(`Create a small Go project in %s:
1. Create a directory called "myproject"
2. Write a main.go file that prints "Hello from planned agent!"
3. Read the file back to verify it was written correctly`, workspaceDir)

	// A single fw.TaskF chain replaces the entire hand-rolled
	// Plan → FindReadySteps → per-step retry → Reflect loop in main.go.
	// .Plan() enables the planner (with DefaultPromptSet);
	// .Reflect() enables the reflector (with DefaultReflectorPrompt);
	// .MaxRetries(2) is the per-step retry budget.
	result, err := fw.TaskF(context.Background(), task).
		SystemFactory(func(_ context.Context, stepDescription string, _ llm.ModelMetadata) string {
			return fmt.Sprintf(`You are a task execution agent. Complete the following task step.

## Task
%s

## Instructions
- Use the available tools to accomplish the task.
- Verify your work before finishing.
- Call the finish tool with a summary of what you did.`, stepDescription)
		}).
		Workspace(workspaceDir).
		Plan().
		Reflect().
		MaxRetries(2).
		Execute()
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	// The plan, reflections, and aggregated output are all on the result.
	fmt.Println("\n═══════════════════════════════════════════")
	fmt.Println("Plan execution complete")
	fmt.Printf("Steps: %d total, %d failed\n", len(result.Plan.Steps), result.FailedSteps)
	fmt.Printf("Reflections generated: %d\n", len(result.Reflections))
	fmt.Println("\nFinal output:")
	fmt.Println(result.Output)
	fmt.Println("═══════════════════════════════════════════")
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}
