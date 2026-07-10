# Reflector

The `reflector` package provides structured self-correction for agent execution. After a step fails, a `Reflector` analyses the execution trajectory — the sequence of thoughts, actions, and observations the agent produced — and returns a `Reflection` containing a root-cause analysis and a suggested recovery action.

The reflector implements the `orchestration.Reflector` interface, so it drops into any orchestrator that supports reflection-driven retry loops.

```go
import "github.com/v0lka/sp4rk/agent/reflector"
```

---

## Table of contents

- [Reflector](#reflector-1)
  - [New](#new)
  - [SetReasoningEffort](#setreasoningeffort)
- [Config](#config)
- [Reflect](#reflect)
  - [How the trajectory is built](#how-the-trajectory-is-built)
  - [Previous reflections](#previous-reflections)
  - [Response parsing](#response-parsing)
- [Reflection type](#reflection-type)
  - [SuggestedAction values](#suggestedaction-values)
- [Integration with the Blackboard](#integration-with-the-blackboard)
- [Complete retry + reflect example](#complete-retry--reflect-example)

---

## Reflector

```go
type Reflector struct {
    // has unexported fields
}
```

`Reflector` analyses an execution trajectory to produce structured self-correction insights. It is a stateless wrapper around an LLM caller: each call to `Reflect` sends the trajectory, the plan, and any prior reflections to the LLM and parses the JSON response into an `orchestration.Reflection`.

The reflector satisfies `orchestration.Reflector` at compile time:

```go
var _ orchestration.Reflector = (*Reflector)(nil)
```

### New

```go
func New(caller agent.LLMCaller, cfg Config) *Reflector
```

Creates a new Reflector with the given LLM caller and configuration. If `cfg.AnalyzeFooter` is empty, a standard analysis request is used as the footer appended to the user message.

```go
rf := reflector.New(router, reflector.Config{
    SystemPrompt: `You are a reflection agent. Analyze the failed execution.

Consider:
- Was the approach wrong?
- Were the wrong tools used?
- Was the task description ambiguous?
- Were there environmental issues (missing files, permissions)?

Return a JSON object with:
- "summary": brief description of what happened
- "root_cause": the primary reason for failure
- "suggested_action": "retry", "replan", or "abort"
- "action_plan": concrete steps to fix the issue`,
})
```

### SetReasoningEffort

```go
func (r *Reflector) SetReasoningEffort(effort string)
```

Sets the reasoning effort for the reflector's LLM calls. Use this with reasoning-capable models to control how much reasoning budget the reflector spends analysing a failure. Common values are `"low"`, `"medium"`, and `"high"`.

```go
rf.SetReasoningEffort("high")
```

---

## Config

```go
type Config struct {
    // SystemPrompt is the reflection system prompt.
    SystemPrompt string
    // AnalyzeFooter is appended to the user message. Defaults to a standard analysis request.
    AnalyzeFooter string
}
```

| Field | Description |
| --- | --- |
| `SystemPrompt` | The reflection system prompt. Instructs the LLM on how to analyse failures and what JSON to return. |
| `AnalyzeFooter` | Text appended to the user message after the trajectory, plan, and prior reflections. When empty, defaults to `"Please analyze this execution and provide a structured reflection."` |

The system prompt should instruct the model to return a JSON object matching the `Reflection` schema. The reflector extracts JSON from the response (allowing surrounding prose) and unmarshals it.

---

## Reflect

```go
func (r *Reflector) Reflect(
    ctx context.Context,
    trajectory []agent.Step,
    plan *orchestration.Plan,
    prevReflections []orchestration.Reflection,
) (*orchestration.Reflection, error)
```

Analyses an execution trajectory and returns a structured reflection.

| Parameter | Description |
| --- | --- |
| `ctx` | Context. Carries workspace/environment info that is appended to the system prompt as a compact environment block. |
| `trajectory` | The executor's step history — the sequence of `agent.Step` values (thought → action → observation) produced during the failed run. |
| `plan` | The plan being executed. Provides step descriptions and dependencies so the reflector understands the broader context. May be `nil`. |
| `prevReflections` | Reflections from earlier failures in the same task. The reflector learns from these to avoid repeating the same mistakes. |

**Returns** an `*orchestration.Reflection` with `Timestamp` set to the current time. Returns an error if the LLM call fails, returns nil, or produces unparseable JSON.

```go
reflection, err := rf.Reflect(ctx, trajectory, plan, reflections)
if err != nil {
    return err
}
fmt.Printf("Root cause: %s\n", reflection.RootCause)
fmt.Printf("Suggested action: %s\n", reflection.SuggestedAction)
```

### How the trajectory is built

The reflector constructs a user message that presents the trajectory in a step-by-step **Thought / Action / Observation** format:

```markdown
## Execution Trajectory

### Step 1
**Thought:** I need to read the file first.
**Action:** read_file
**Input:** {"path": "main.go"}

**Observation:** Error: file not found

### Step 2
**Thought:** The file doesn't exist, so I'll create it.
**Action:** write_file
**Input:** {"path": "main.go", "content": "..."}

**Observation:** Error: permission denied
```

When the plan is provided, it is appended as a section listing each step's ID, description, and dependencies:

```markdown
## Plan

- step_1: Create the project directory
- step_2: Write main.go
  Depends on: [step_1]
```

The trajectory is captured by injecting an `agent.TrajectoryStore` into the context before calling the executor. The executor syncs its step history to the store at each ReAct iteration, so the full trajectory is available after the run:

```go
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

// Before running the executor:
store := &trajectoryStore{}
stepCtx := agent.WithTrajectoryStore(ctx, store)
result, err := conductor.Run(stepCtx, step.Description, bb, tools, events, "sliding_window")
trajectory := store.Steps() // pass this to rf.Reflect on failure
```

### Previous reflections

When `prevReflections` is non-empty, the reflector includes them in the user message under a `## Previous Reflections` section, prefixed with the note "(Learn from these to avoid repeating the same mistakes)". Each prior reflection's summary, root cause, action plan, and suggested action are listed.

This gives the LLM memory of past failures so it can identify recurring patterns and recommend a different recovery action rather than retrying the same doomed approach.

### Response parsing

The reflector extracts JSON from the LLM response (tolerating surrounding prose or markdown fences) and unmarshals it into an `orchestration.Reflection`. It then validates `SuggestedAction`:

- `"retry"`, `"replan"`, and `"abort"` are accepted as-is.
- An empty or unrecognised value defaults to `"retry"`.

If `Summary` is empty, it is set to `"Execution analysis unavailable"`. The `Timestamp` is always set to `time.Now()`.

---

## Reflection type

The reflector returns an `orchestration.Reflection` (defined in the `orchestration` package):

```go
type Reflection struct {
    Summary         string    `json:"summary"`
    Hypotheses      []string  `json:"hypotheses"`
    SuggestedAction string    `json:"suggested_action"` // "retry" | "replan" | "abort"
    Reasoning       string    `json:"reasoning"`
    FailureAnalysis string    `json:"failure_analysis"`
    RootCause       string    `json:"root_cause"`
    ActionPlan      string    `json:"action_plan"`
    Timestamp       time.Time `json:"timestamp"`
}
```

| Field | Description |
| --- | --- |
| `Summary` | Brief description of what happened during the failed execution. |
| `Hypotheses` | Candidate explanations for the failure. |
| `SuggestedAction` | The recommended recovery action — see below. |
| `Reasoning` | The reflector's reasoning process. |
| `FailureAnalysis` | Detailed analysis of why the execution failed. |
| `RootCause` | The primary reason for the failure. |
| `ActionPlan` | Concrete steps to fix the issue. |
| `Timestamp` | When the reflection was produced (set by the reflector). |

### SuggestedAction values

`SuggestedAction` drives the orchestrator's recovery decision. It is a string with three valid values:

| Value | Meaning | Orchestrator response |
| --- | --- | --- |
| `"retry"` | Try the step again with adjustments informed by the reflection. | Re-run the step (optionally injecting the reflection's `ActionPlan` into the next attempt). |
| `"replan"` | The plan itself is wrong — the step decomposition or approach is flawed. | Call `Planner.Replan` to generate a corrected plan, then resume execution. |
| `"abort"` | The failure is unrecoverable (e.g. missing dependencies, permission issues that cannot be fixed). | Stop execution immediately. |

When the LLM omits or returns an unrecognised action, the reflector defaults to `"retry"` — the safest recovery attempt.

---

## Integration with the Blackboard

Reflections are stored on the `orchestration.Blackboard` so they persist across the retry loop and are available to the planner during replanning.

- `Blackboard.AddReflection(r Reflection)` appends a reflection.
- `Blackboard.GetReflections() []Reflection` returns all reflections in insertion order.

A typical retry loop records each reflection on the blackboard and passes the accumulated list to the next `Reflect` call:

```go
var reflections []orchestration.Reflection

for attempt := 1; attempt <= maxRetries+1; attempt++ {
    result, err := conductor.Run(stepCtx, step.Description, bb, tools, events, "sliding_window")
    trajectory := store.Steps()

    if err == nil && result.Status == orchestration.ExecutionStatusSuccess {
        bb.SetStepResult(step.ID, result.Output, nil, trajectory)
        break
    }

    if attempt <= maxRetries {
        reflection, reflectErr := rf.Reflect(stepCtx, trajectory, plan, reflections)
        if reflectErr == nil && reflection != nil {
            bb.AddReflection(*reflection)            // persist for replanning
            reflections = append(reflections, *reflection)

            switch reflection.SuggestedAction {
            case "abort":
                // stop immediately
                return
            case "replan":
                // generate a corrected plan
                newPlan, _ := pl.Replan(ctx, plan, completedList,
                    orchestration.CompletedStep{StepID: step.ID, Error: err},
                    reflection, reflections, skills)
                plan = newPlan
                // restart execution with the new plan
            case "retry":
                // loop continues — next attempt sees the reflection
            }
        }
    }
}
```

The `ExecutionResult` returned by an orchestrator also carries the accumulated reflections in its `Reflections` field, so callers can inspect what went wrong even after the task completes.

---

## Complete retry + reflect example

This example (adapted from the SDK's example 06) shows the full retry-and-reflect loop: a Conductor executes a plan step, and on failure the Reflector analyses the trajectory and recommends a recovery action.

```go
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

// trajectoryStore captures the executor's step history for reflection.
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
		Execution: sp4rk.ExecutionConfig{MaxSteps: 15, MaxRetries: 2},
	})
	if err != nil {
		return err
	}
	defer func() { _ = fw.Shutdown() }()

	registry := fw.ToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewEditFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(builtins.NewCreateDirectoryTool())
	registry.Register(agent.NewFinishTool())

	workspaceDir, _ := os.MkdirTemp("", "example-*")
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	// --- Planner ---
	plannerCfg := planner.DefaultConfig()
	plannerCfg.Prompts = planner.PromptSet{
		BasePrompt: `You are a task planning agent. Break the task into steps.

Available tools:
AVAILABLE-TOOLS

Create at most MAX-STEPS steps. Use "depends_on" for ordering.

MODE-PREAMBLE

Output ONLY valid JSON:
MODE-JSON-EXAMPLE`,
		PlanPreamble:      "Break the task into sequential steps.",
		MultiStepGuidance: "Each step should produce a verifiable artifact.",
	}
	plannerCfg.Model = "claude-sonnet-4-5"
	pl, err := planner.NewPlanner(fw.LLMRouter(), plannerCfg)
	if err != nil {
		return err
	}

	// --- Reflector ---
	rf := reflector.New(fw.LLMRouter(), reflector.Config{
		SystemPrompt: `You are a reflection agent. Analyze the failed execution.

Consider:
- Was the approach wrong?
- Were the wrong tools used?
- Was the task description ambiguous?
- Were there environmental issues (missing files, permissions)?

Return JSON with "summary", "root_cause", "suggested_action" (retry/replan/abort), and "action_plan".`,
	})

	// --- Conductor ---
	systemPromptFactory := func(_ context.Context, stepDescription string, _ llm.ModelMetadata) string {
		return fmt.Sprintf(`You are a task execution agent.

## Task
%s

Use the available tools. Verify your work before calling finish.`, stepDescription)
	}
	conductor, err := fw.NewConductor(systemPromptFactory)
	if err != nil {
		return err
	}
	defer conductor.Cleanup()

	task := fmt.Sprintf(`Create a small Go project in %s:
1. Create a directory "myproject"
2. Write main.go that prints "Hello from planned agent!"
3. Read the file back to verify`, workspaceDir)

	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)
	availableTools := registry.List()

	// --- Plan ---
	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest(task)
	plan, err := pl.Plan(ctx, task, availableTools, nil, nil, false, nil)
	if err != nil {
		return err
	}

	// --- Execute with retry + reflect ---
	completed := make(map[string]orchestration.CompletedStep)
	var reflections []orchestration.Reflection
	maxRetries := 2

	for {
		ready := orchestration.FindReadySteps(plan, completed)
		if len(ready) == 0 {
			break
		}
		for _, step := range ready {
			success := false
			for attempt := 1; attempt <= maxRetries+1; attempt++ {
				store := &trajectoryStore{}
				stepCtx := agent.WithTrajectoryStore(ctx, store)

				result, runErr := conductor.Run(
					stepCtx, step.Description, bb, availableTools,
					&agent.NoopEvents{}, "sliding_window",
				)
				trajectory := store.Steps()

				if runErr == nil && result.Status == orchestration.ExecutionStatusSuccess {
					completed[step.ID] = orchestration.CompletedStep{
						StepID: step.ID, Output: result.Output, Steps: trajectory,
					}
					bb.SetStepResult(step.ID, result.Output, nil, trajectory)
					success = true
					break
				}

				// Failure — reflect and decide.
				if attempt <= maxRetries {
					reflection, reflectErr := rf.Reflect(stepCtx, trajectory, plan, reflections)
					if reflectErr == nil && reflection != nil {
						bb.AddReflection(*reflection)
						reflections = append(reflections, *reflection)

						fmt.Printf("  💡 Reflection: %s\n", reflection.Summary)
						fmt.Printf("     Root cause: %s\n", reflection.RootCause)
						fmt.Printf("     Suggested action: %s\n", reflection.SuggestedAction)
						fmt.Printf("     Action plan: %s\n", reflection.ActionPlan)

						if reflection.SuggestedAction == "abort" {
							fmt.Println("  🛑 Aborting as suggested by reflection")
							break
						}
						// For "replan", a full implementation calls pl.Replan(...)
						// and restarts with the new plan. For "retry", the loop
						// continues and the next attempt sees the reflection.
					}
				}
			}
			if !success {
				completed[step.ID] = orchestration.CompletedStep{
					StepID: step.ID,
					Error:  fmt.Errorf("step failed after %d attempts", maxRetries+1),
				}
			}
		}
	}

	finalOutput := orchestration.AggregateOutput(completed, plan, nil)
	fmt.Printf("\nCompleted %d/%d steps, %d reflections\n",
		len(completed), len(plan.Steps), len(reflections))
	fmt.Println(finalOutput)
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}
```

The flow is:

1. **Execute** — `Conductor.Run` runs the step's ReAct loop. A `TrajectoryStore` captures every thought/action/observation.
2. **Reflect** — on failure, `Reflector.Reflect` receives the trajectory, the plan, and prior reflections, then returns a `Reflection` with a root cause and a suggested action.
3. **Recover** — the orchestrator acts on `SuggestedAction`: retry the step, replan via `Planner.Replan`, or abort. Each reflection is stored on the blackboard so the next `Reflect` call can learn from past mistakes.
