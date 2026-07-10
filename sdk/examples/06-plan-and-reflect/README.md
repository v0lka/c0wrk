# Example 06 — Plan & Reflect Orchestration

The full Plan & Execute orchestration pattern: a **Planner** breaks the task into a DAG of steps, a **Conductor** executes each step as an independent ReAct loop, and a **Reflector** analyzes failures to self-correct with retry or replan.

This is the pattern sp4rk itself uses for complex tasks.

| Variant     | File            | Command                 | When to read                              |
|-------------|-----------------|-------------------------|-------------------------------------------|
| **Fluent**  | `main_fluent.go`| `go run -tags fluent .` | Recommended — `fw.TaskF` collapses the loop to one chain |
| **Classic** | `main.go`       | `go run .`              | Hand-rolled Plan → DAG → retry → Reflect loop (~80 lines) |

### Fluent (recommended)

The entire orchestration loop — plan generation, ready-step scheduling, per-step retry, and reflection — is one `fw.TaskF` chain. The default prompt set and reflector prompt are applied automatically:

```go
result, err := fw.TaskF(ctx, task).
    SystemFactory(stepPromptFactory).
    Workspace(workspaceDir).
    Plan().        // planner + DefaultPromptSet
    Reflect().     // reflector + DefaultReflectorPrompt
    MaxRetries(2). // per-step retry budget
    Execute()      // → *orchestration.ExecutionResult{Plan, Reflections, Output, …}
```

`main_fluent.go` collapses the classic `main.go`'s hand-rolled Plan → DAG → retry → Reflect loop into a single `fw.TaskF` chain for identical behaviour. Read the classic variant to understand *how* the loop works; use the fluent variant in your own code.

## What you will learn

- How to create and configure a `Planner` with a custom `PromptSet`
- How to execute a DAG of steps with `FindReadySteps` + `Conductor.Run`
- How to capture the executor trajectory via `TrajectoryStore`
- How to use the `Reflector` for failure analysis
- How to act on reflection suggestions (retry / replan / abort)

## Architecture

```
User task
    │
    ▼
Planner.Plan()  ──────────────►  Plan { step_1, step_2, step_3 }
    │                                    │
    │                                    ▼
    │                           FindReadySteps(plan, completed)
    │                                    │
    │                              ┌─────┴─────┐
    │                              ▼           ▼
    │                          step_1       step_2 (parallel if no deps)
    │                              │
    │                              ▼
    │                       Conductor.Run(step_1)
    │                              │
    │                         success?
    │                         ┌────┴────┐
    │                        yes        no
    │                         │         │
    │                         │    Reflector.Reflect()
    │                         │         │
    │                         │    ┌────┴────┐
    │                         │  retry    abort
    │                         │    │         │
    │                         │    └─► retry │
    │                         ▼
    │                    completed[step_1] = result
    │                         │
    │                         ▼
    │                    next ready step…
    │
    ▼
AggregateOutput(completed, plan)  ──►  final result
```

## Code walkthrough

### 1. The Planner

```go
plannerCfg := planner.DefaultConfig()
plannerCfg.Prompts = planner.PromptSet{
    BasePrompt: `You are a task planning agent…
        AVAILABLE-TOOLS
        MODE-JSON-EXAMPLE
        MAX-STEPS …`,
    PlanPreamble:      "Break the task into logical steps…",
    MultiStepGuidance: "Prefer fewer, well-defined steps…",
}
plannerCfg.Model = "claude-sonnet-4-5"

pl, err := planner.NewPlanner(fw.LLMRouter(), plannerCfg)
```

The Planner calls the LLM with a system prompt built from the `PromptSet` template. Placeholders like `AVAILABLE-TOOLS`, `MODE-JSON-EXAMPLE`, and `MAX-STEPS` are substituted automatically. The LLM returns a JSON plan that the Planner parses into an `*orchestration.Plan`.

`planner.DefaultConfig()` provides sensible defaults for the context functions (`DomainFromContext`, `ComplexityFromContext`, etc.). When `PlannerToolNames` is empty (the default), the Planner uses **direct planning** — a single LLM call without codebase exploration.

### 2. Generating a plan

```go
plan, err := pl.Plan(ctx, task, availableTools, nil, nil, false, nil)
```

Parameters:
| Parameter            | Description                              |
|----------------------|------------------------------------------|
| `ctx`                | Context (carries workspace path)         |
| `task`               | The user's task description              |
| `availableTools`     | Tool descriptors the steps can use       |
| `reflections`        | Prior reflections (nil for first plan)   |
| `availableSkills`    | Skill descriptors (nil if no skills)     |
| `singleStep`         | `true` = force a single-step plan        |
| `conversationHistory`| Prior messages (nil for first message)   |

The returned `Plan` is a DAG:

```go
type Plan struct {
    Steps []PlanStep  // each has ID, Summary, Description, DependsOn, …
}
```

### 3. Executing the DAG

```go
completed := make(map[string]orchestration.CompletedStep)

for {
    readySteps := orchestration.FindReadySteps(plan, completed)
    if len(readySteps) == 0 {
        break
    }

    for _, step := range readySteps {
        result, err := conductor.Run(ctx, step.Description, bb, availableTools, events, "sliding_window")
        // …
    }
}
```

`FindReadySteps` returns steps whose dependencies are all completed successfully. This respects the DAG topology — a step only runs after its `DependsOn` steps are done. For parallel execution, launch each ready step in a goroutine (see `agent.RunSubAgentsParallel`).

### 4. Capturing the trajectory

The Reflector needs the executor's step history (thoughts, tool calls, observations) to analyze failures. We inject a `TrajectoryStore` into the context — the executor syncs to it at every ReAct iteration:

```go
type trajectoryStore struct { … }

store := &trajectoryStore{}
stepCtx := agent.WithTrajectoryStore(ctx, store)

result, err := conductor.Run(stepCtx, step.Description, bb, tools, events, "sliding_window")

trajectory := store.Steps()  // full step history for reflection
```

### 5. The Reflector

```go
rf := reflector.New(fw.LLMRouter(), reflector.Config{
    SystemPrompt: "You are a reflection agent. Analyze the failed execution…",
})

reflection, err := rf.Reflect(ctx, trajectory, plan, prevReflections)
```

The Reflector sends the trajectory, plan, and prior reflections to the LLM and parses the response into an `*orchestration.Reflection`:

```go
type Reflection struct {
    Summary         string    // what happened
    RootCause       string    // primary reason for failure
    SuggestedAction string    // "retry" | "replan" | "abort"
    ActionPlan      string    // concrete steps to fix
    Hypotheses      []string  // possible causes
    Reasoning       string    // analysis reasoning
    FailureAnalysis string    // detailed failure analysis
    Timestamp       time.Time // when the reflection was produced
}
```

### 6. Acting on reflections

```go
switch reflection.SuggestedAction {
case "retry":
    // Re-execute the step (optionally with the action plan as guidance)
case "replan":
    // Call pl.Replan() to generate a revised plan
case "abort":
    // Stop execution — the failure is unrecoverable
}
```

This example implements the retry loop. A full implementation would also call `pl.Replan()` when the reflection suggests it:

```go
newPlan, err := pl.Replan(ctx, plan, completedList, failedStep, reflection, reflections, nil)
```

### 7. Aggregating output

```go
finalOutput := orchestration.AggregateOutput(completed, plan, nil)
```

`AggregateOutput` collects outputs from terminal steps (steps that no other step depends on). If all steps succeeded, this is the final deliverable.

## The three SDK primitives

| Primitive  | Package                | Role                                      |
|------------|------------------------|-------------------------------------------|
| Planner    | `planner`              | Generates a DAG plan from a task          |
| Conductor  | `orchestration`        | Executes one step as a ReAct loop         |
| Reflector  | `agent/reflector`      | Analyzes failures, suggests corrections   |

The Framework (`sp4rk.Framework`) wires them together: `fw.NewConductor()` creates a Conductor with the Framework's LLM router, tool registry, and context-window factory. `fw.LLMRouter()` provides the `agent.LLMCaller` needed by the Planner and Reflector. `fw.ContextFactory()` provides the `orchestration.ContextManagerFactory` needed by the Planner for exploration mode.

## Prerequisites

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

## Run

### Fluent (recommended)

```bash
cd sdk/examples/06-plan-and-reflect
go run -tags fluent .
```

### Classic API (advanced control)

```bash
cd sdk/examples/06-plan-and-reflect
go run .
```

## Expected output

```
Workspace: /tmp/sp4rk-example-06-123456

📋 Planning...
Plan generated with 3 steps:
  • step_1: Create project directory (depends on: none)
  • step_2: Write main.go file (depends on: [step_1])
  • step_3: Verify file contents (depends on: [step_2])

▶ Executing step_1: Create project directory
  ✅ step_1 completed

▶ Executing step_2: Write main.go file
  ✅ step_2 completed

▶ Executing step_3: Verify file contents
  ✅ step_3 completed

═══════════════════════════════════════════
Plan execution complete
Steps completed: 3/3
Reflections generated: 0

Final output:
Created the directory myproject, wrote main.go with "Hello from planned agent!",
and verified the file contents by reading it back.
═══════════════════════════════════════════
```

If a step fails, you'll see the reflection in action:

```
▶ Executing step_2: Write main.go file
  ❌ step_2 failed (attempt 1): step execution did not complete…
  🔍 Reflecting on failure...
  💡 Reflection: The agent tried to write to a non-existent directory
     Root cause: directory not created before write
     Suggested action: retry
     Action plan: create the directory first, then write the file
  ↻ Retry 1/2 for step_2
  ✅ step_2 completed
```

## Next

→ **11-full-power** — combine every SDK subsystem into one agent: multi-provider LLM, custom + built-in + MCP tools, HITL, events, planner, reflector, skills, and fact memory.

Or dive into a focused subsystem first: **07-multi-provider-routing**, **08-parallel-subagents**, **09-context-memory**, **10-security-and-safety**.
