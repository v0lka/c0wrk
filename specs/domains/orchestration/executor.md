# Executor

## Role

Executes individual plan steps via the ReAct loop (Thought → Action → Observation), with circuit breakers for failure detection and context window management for long-running steps.

## Key Files

- `sdk/agent/executor.go` — Executor struct (Run method, ReAct loop)
- `sdk/orchestration/orchestrator.go` — SDK Orchestrator (drives DAG execution, parallel steps)
- `core/stepconfig.go` — coreStepConfigurator (resolves per-step config)
- `sdk/agent/events.go` — AgentEvents interface (lifecycle hooks)

## Behavior

### SDK Orchestration Engine (DAG Driver)

The SDK `orchestration.Orchestrator` drives the plan:

```
engine.Resume(ctx, bb)
│
├─ Read plan from Blackboard
├─ Loop until all steps complete or max retries exhausted:
│   │
│   ├─ FindReadySteps() — steps with all dependencies satisfied
│   ├─ For each ready step (parallel goroutines):
│   │   ├─ StepConfigurator resolves StepConfig
│   │   ├─ Build step task (description + dependency context)
│   │   ├─ Create ContextManager for step
│   │   ├─ Run Executor.Run(ctx, task, tools, systemPrompt)
│   │   └─ Store result on Blackboard
│   │
│   ├─ If step failed:
│   │   ├─ Reflector.Reflect() → Reflection
│   │   ├─ Planner.Replan() → new Plan
│   │   └─ Retry from step 1
│   │
│   └─ If all steps succeeded: done
│
└─ Return ExecutionResult {Output, Plan, Blackboard, AttemptCount, Reflections}
```

### Per-Step Configuration (StepConfigurator)

`coreStepConfigurator` resolves runtime config for each step:

| Field              | Resolution                                                      |
| ------------------ | --------------------------------------------------------------- |
| MaxSteps           | step.Profile.MaxSteps > 0 ? use it : config.MaxSteps            |
| SystemPrompt       | buildSystemPrompt(ctx) with step-local skill narrowing          |
| CompactionStrategy | step.Profile.Domain mapping (code→sliding, research→summary)    |
| AgentRole          | step.Profile.Role (affects prompt + pruning)                    |
| AllowedTools       | step.Profile.AllowedTools (nil = all)                           |
| ReasoningEffort    | ResolveAgentReasoningMode(role, base, overrides)                |
| KeepLastN          | step.Profile.KeepLastN > 0 ? use it : rolePruningDefaults[role] |
| ProtectedTools     | step.Profile.ProtectedTools ?? rolePruningDefaults[role]        |

Role-based pruning defaults:

- `researcher`: KeepLastN=10
- `coder`, `tester`, `executor`: KeepLastN=5

### Dependency Context Injection

Before a step runs, outputs from its dependencies are injected into the task description:

```
Step task = step.Description + "\n\n## Context from previous steps\n"
  + For each dep in step.DependsOn:
      "### {dep.Summary}\n{truncate(dep.Output, budget)}\n"
```

Budget: `MaxDependencyContextChars` (default: 8000) divided among dependencies.

### ReAct Loop (Executor.Run)

```
Executor.Run(ctx, task, tools, systemPrompt)
│
├─ Initialize: ContextManager, messages, step counter
│
├─ Loop (max MaxSteps iterations):
│   │
│   ├─ 1. Call LLM with current messages
│   │      → Response may contain: text, tool_calls, or finish
│   │
│   ├─ 2. If tool_call:
│   │      ├─ Execute tool via ToolExecutor.Execute(ctx, name, input)
│   │      ├─ Apply ToolResultBudget (truncate if too large)
│   │      ├─ Add observation to messages
│   │      └─ Check context fill → compact if needed
│   │
│   ├─ 3. If finish tool called:
│   │      → Extract output, return success
│   │
│   ├─ 4. Circuit breaker check (see below)
│   │
│   └─ 5. If step limit reached:
│          → Call StepLimitFunc for user decision
│          → AllowOnce: +N steps, AllowAlways: unlimited, Deny: stop
│
└─ Return ExecutorResult {Steps, Output, Finished}
```

### Circuit Breaker

Detects pathological patterns and nudges the LLM:

| Detection    | Trigger                                     | Action                            |
| ------------ | ------------------------------------------- | --------------------------------- |
| Repeat       | Same tool + same args + same error N times  | Nudge: "try a different approach" |
| Repeat abort | Same tool + same args N+2 times             | Hard stop with error              |
| Truncation   | LLM output truncated (tool call incomplete) | Nudge: "split into smaller calls" |
| Parse error  | Invalid tool input N times                  | Nudge: "simplify your input"      |
| Fruitless    | Last N tool results are empty/minimal       | Nudge: "consider wrapping up"     |
| Same tool    | Same tool name N times with similar results | Nudge: "try a different strategy" |

Config: `CircuitBreakerConfig` (thresholds per detection type).

### Critical Always-Allowed Tools

These tools are always available regardless of step's AllowedTools filter:

- `finish` — end step execution
- `store_fact` — save findings to blackboard
- `search_facts` — retrieve stored facts
- `ask_user` — prompt user for information
- `set_step_status` — update step status/checklist
- `read_step_output` — read a specific step's output

The set is enforced in `core/stepconfig.go` `criticalAlwaysAllowedTools` and unioned into the filtered list whenever `AllowedTools` is non-empty.

### Tool Result Budget

Large tool results are truncated to stay within context:

- `HardCapTokens` — absolute maximum tokens for a single result
- `FillFraction` — max percentage of available context for tool results
- Floor: 256 tokens minimum
- Truncation notice appended when result exceeds budget

## Error Handling

- Step failure (executor returns error): result stored with Error field set
- Context cancelled: propagates immediately, no retry
- Step limit reached without finish: treated as incomplete (not failure)

## Invariants

- `finish` tool is ALWAYS available in every step (never filtered out)
- Context window never exceeds model limit (compaction triggers automatically)
- MaxSteps bounds total iterations (StepLimitFunc may extend)
- Each step has its own ContextManager (isolated memory)
- Parallel steps run in separate goroutines with independent contexts
- A step's output is immutable once stored on Blackboard

## Related Specs

- [README.md](README.md) — orchestration overview
- [planner.md](planner.md) — plan generation
- [../memory/compaction.md](../memory/compaction.md) — compaction strategies
- [../tool-system/README.md](../tool-system/README.md) — tool execution pipeline
- [../../architecture/security-model.md](../../architecture/security-model.md) — policy enforcement
