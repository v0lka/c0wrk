//go:build !fluent

// Example 06 — Plan & Reflect Orchestration (Classic API)
//
// The full Plan & Execute orchestration pattern, written by hand against the
// classic sp4rk API (Planner → DAG → Conductor → Reflector, with a manual retry
// loop). For the concise recommended path — which collapses this ~80-line loop
// into a single fw.TaskF chain — see main_fluent.go (run with `-tags fluent`).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/planner"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// trajectoryStore implements agent.TrajectoryStore. The executor syncs its
// step history to this store at each ReAct iteration, so we can retrieve
// the full trajectory after a step completes — the Reflector needs it to
// analyze what went wrong.
type trajectoryStore struct {
	mu    sync.Mutex
	steps []agent.Step
}

func (s *trajectoryStore) Sync(steps []agent.Step) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = steps
}

func (s *trajectoryStore) Steps() []agent.Step {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.steps
}

func run() error {
	fw, err := sp4rk.New(sp4rk.Config{
		LLM: sp4rk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
		},
		Execution: sp4rk.ExecutionConfig{
			MaxSteps:   15, // per-step ReAct budget
			MaxRetries: 2,  // retries per plan step on failure
		},
		// The registry is FAIL-CLOSED for PolicyUserConfirm tools
		// (write_file, edit_file, create_directory). This example runs in a
		// throwaway temp workspace, so we auto-approve mutations.
		ConfirmFunc: func(_ context.Context, req tools.ConfirmationRequest) (tools.ConfirmationResponse, error) {
			fmt.Printf("  [auto-approving %s]\n", req.ToolName)
			return tools.ConfirmAllowOnce, nil
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	// --- Register tools ---
	registry := fw.ToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewEditFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(builtins.NewGlobTool())
	registry.Register(builtins.NewCreateDirectoryTool())
	registry.Register(agent.NewFinishTool())

	workspaceDir, err := os.MkdirTemp("", "sp4rk-example-06-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	fmt.Println("Workspace:", workspaceDir)

	// --- Create the Planner ---
	// The Planner needs a PromptSet with a BasePrompt template. The template
	// uses placeholders (AVAILABLE-TOOLS, MODE-JSON-EXAMPLE, etc.) that the
	// planner substitutes at call time.
	plannerCfg := planner.DefaultConfig()
	plannerCfg.Prompts = planner.PromptSet{
		BasePrompt: `You are a task planning agent. Break down the user's task into a sequence of concrete steps.

## Available Tools
AVAILABLE-TOOLS

## Instructions
- Create at most MAX-STEPS steps.
- Each step must be self-contained with clear acceptance criteria.
- Use "depends_on" to chain steps that must run in order.
- Use "estimated_tools" to list the tools each step will likely need.

MODE-PREAMBLE

## Output Format
Return ONLY a valid JSON object with this structure:
MODE-JSON-EXAMPLE`,
		PlanPreamble:      "Break the task into logical, sequential steps. Each step should produce a verifiable artifact.",
		MultiStepGuidance: "Prefer fewer, well-defined steps. Include acceptance criteria for each step.",
	}
	plannerCfg.Model = "claude-sonnet-4-5"

	pl, err := planner.NewPlanner(fw.LLMRouter(), plannerCfg)
	if err != nil {
		return fmt.Errorf("failed to create planner: %w", err)
	}

	// --- Create the Reflector ---
	// The Reflector analyzes a failed step's trajectory and produces a
	// Reflection with a suggested action: "retry", "replan", or "abort".
	rf := reflector.New(fw.LLMRouter(), reflector.Config{
		SystemPrompt: `You are a reflection agent. Analyze the failed execution and determine why it failed.

Consider:
- Was the approach wrong?
- Were the wrong tools used?
- Was the task description ambiguous?
- Were there environmental issues (missing files, permissions)?

Return a JSON object with:
- "summary": brief description of what happened
- "root_cause": the primary reason for failure
- "suggested_action": "retry" (try again with adjustments), "replan" (the plan itself is wrong), or "abort" (unrecoverable)
- "action_plan": concrete steps to fix the issue`,
	})

	// --- Create a Conductor for step execution ---
	// One Conductor is reused for all steps. The system prompt factory
	// receives the step description at Run time, so it adapts per step.
	systemPromptFactory := func(_ context.Context, stepDescription string, _ llm.ModelMetadata) string {
		return fmt.Sprintf(`You are a task execution agent. Complete the following task step.

## Task
%s

## Instructions
- Use the available tools to accomplish the task.
- Verify your work before finishing.
- Call the finish tool with a summary of what you did.`, stepDescription)
	}
	conductor, err := fw.NewConductor(systemPromptFactory)
	if err != nil {
		return fmt.Errorf("failed to create conductor: %w", err)
	}
	defer conductor.Cleanup()

	// --- The task ---
	task := fmt.Sprintf(`Create a small Go project in %s:
1. Create a directory called "myproject"
2. Write a main.go file that prints "Hello from planned agent!"
3. Read the file back to verify it was written correctly`, workspaceDir)

	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)
	availableTools := registry.List()

	// --- Step 1: Plan ---
	fmt.Println("\n📋 Planning...")
	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest(task)

	plan, err := pl.Plan(ctx, task, availableTools, nil, nil, false, nil)
	if err != nil {
		return fmt.Errorf("planning failed: %w", err)
	}

	fmt.Printf("Plan generated with %d steps:\n", len(plan.Steps))
	for _, step := range plan.Steps {
		deps := "none"
		if len(step.DependsOn) > 0 {
			deps = fmt.Sprintf("%v", step.DependsOn)
		}
		fmt.Printf("  • %s: %s (depends on: %s)\n", step.ID, step.Summary, deps)
	}

	// --- Step 2: Execute the plan ---
	completed := make(map[string]orchestration.CompletedStep)
	var reflections []orchestration.Reflection
	maxRetries := 2

	for {
		// Find steps whose dependencies are all satisfied
		readySteps := orchestration.FindReadySteps(plan, completed)
		if len(readySteps) == 0 {
			break // all steps done (or blocked by failures)
		}

		for _, step := range readySteps {
			fmt.Printf("\n▶ Executing %s: %s\n", step.ID, step.Summary)

			success := false
			for attempt := 1; attempt <= maxRetries+1; attempt++ {
				if attempt > 1 {
					fmt.Printf("  ↻ Retry %d/%d for %s\n", attempt-1, maxRetries, step.ID)
				}

				// Inject a TrajectoryStore so we can capture the executor's
				// steps for reflection on failure.
				store := &trajectoryStore{}
				stepCtx := agent.WithTrajectoryStore(ctx, store)

				result, runErr := conductor.Run(
					stepCtx,
					step.Description,
					bb,
					availableTools,
					&agent.NoopEvents{},
					"sliding_window",
				)

				trajectory := store.Steps()

				if runErr == nil && result.Status == orchestration.ExecutionStatusSuccess {
					// Success — record the result
					completed[step.ID] = orchestration.CompletedStep{
						StepID: step.ID,
						Output: result.Output,
						Steps:  trajectory,
					}
					bb.SetStepResult(step.ID, result.Output, nil, trajectory)
					fmt.Printf("  ✅ %s completed\n", step.ID)
					success = true
					break
				}

				// Failure — reflect and decide what to do
				errMsg := "execution failed"
				if runErr != nil {
					errMsg = runErr.Error()
				} else if result != nil {
					errMsg = result.Output
				}
				fmt.Printf("  ❌ %s failed (attempt %d): %s\n", step.ID, attempt, errMsg)

				if attempt <= maxRetries {
					// Reflect on the failure
					fmt.Printf("  🔍 Reflecting on failure...\n")
					reflection, reflectErr := rf.Reflect(stepCtx, trajectory, plan, reflections)
					if reflectErr == nil && reflection != nil {
						bb.AddReflection(*reflection)
						reflections = append(reflections, *reflection)
						fmt.Printf("  💡 Reflection: %s\n", reflection.Summary)
						fmt.Printf("     Root cause: %s\n", reflection.RootCause)
						fmt.Printf("     Suggested action: %s\n", reflection.SuggestedAction)
						fmt.Printf("     Action plan: %s\n", reflection.ActionPlan)

						if reflection.SuggestedAction == "abort" {
							fmt.Printf("  🛑 Aborting as suggested by reflection\n")
							break
						}
						// For "retry" and "replan", we retry the step.
						// A full implementation would call pl.Replan() for "replan".
					}
				}
			}

			if !success {
				fmt.Printf("  🛑 %s failed after %d attempts — stopping\n", step.ID, maxRetries+1)
				completed[step.ID] = orchestration.CompletedStep{
					StepID: step.ID,
					Error:  fmt.Errorf("step failed after %d attempts", maxRetries+1),
				}
			}
		}
	}

	// --- Step 3: Aggregate results ---
	finalOutput := orchestration.AggregateOutput(completed, plan, nil)

	fmt.Println("\n═══════════════════════════════════════════")
	fmt.Println("Plan execution complete")
	fmt.Printf("Steps completed: %d/%d\n", len(completed), len(plan.Steps))
	fmt.Printf("Reflections generated: %d\n", len(reflections))
	fmt.Println("\nFinal output:")
	fmt.Println(finalOutput)
	fmt.Println("═══════════════════════════════════════════")
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}
