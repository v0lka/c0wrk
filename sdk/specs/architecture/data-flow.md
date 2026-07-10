# Data Flow

## Context

Understanding how data moves through sp4rk is essential for safely modifying any layer. This spec traces the major flows inside the engine — from a task entering via `Framework.Execute` through the ReAct loop, the Plan&Execute cycle, tool execution, blackboard state, and event emission. sp4rk is engine-only: a host application supplies the user message, the callbacks (`Events`, `ConfirmFunc`, `HITLHandler`), and the shared infrastructure; the flows below assume those are wired via `Config`.

## Request Lifecycle

A task enters the engine and flows downward through the layers:

```
host application: calls Framework.Execute(ctx, systemPrompt, events, userMessage)
         │
         ▼
sp4rk Framework.Execute
  ├─ creates a Conductor (wired with shared router/registry/cache)
  └─ creates a MapBlackboard for the task
         │
         ▼
orchestration.Conductor.Run(ctx, message, blackboard, tools, events, strategy)
  │
  ├─ 1. (optional) CLASSIFY: agent/router.Router.Route(ctx, msg, tools, history, skills)
  │      → RoutingDecision {Domain, Complexity, MatchedSkills, NeedsClarification}
  │      → host maps Domain/Complexity → compaction strategy
  │        (code → sliding_window, research → summarization,
  │         general/mixed <4 → sliding_window, ≥4 → hierarchical)
  │      → events: routing decision surfaced via Events
  │
  ├─ 2. PLAN:
  │      ├─ single-step mode → Planner produces exactly one PlanStep
  │      └─ multi-step mode  → Planner.Plan → Plan (DAG of PlanSteps)
  │      → events: PlanGenerated(stepCount, steps)
  │
  ├─ 3. EXECUTE: for each ready step (parallel where the DAG allows):
  │      └─ Executor.Run(ctx, stepTask, tools, systemPrompt)
  │           └─ ReAct loop: LLM → ToolCall → Observe → repeat
  │           → events: AssistantChunk, ToolCall, ToolResult, Thought, ...
  │      → step results written to the Blackboard
  │
  ├─ 4. REFLECT (on failure):
  │      → Reflector.Reflect(ctx, trajectory, plan, prevReflections)
  │      → events: Reflection(reflection, attempt, maxAttempts)
  │      → retry | replan (back to step 2) | abort
  │
  └─ 5. RETURN: ExecutionResult {Output, Blackboard, Status, Reflections}
         │
         ▼
host application: reads ExecutionResult, persists state, updates UI
```

## Entry Points

There are two ways to run a task:

- **`Framework.Execute`** — the convenience path. The `Framework` creates a `Conductor` and a `MapBlackboard`, runs the task, and returns an `ExecutionResult`. Best for one-off tasks.
- **`Framework.NewConductor`** — returns a reusable `Conductor` already wired with the framework's shared infrastructure (LLM router, tool registry, tool cache). The host then calls `Conductor.Run` directly with its own `Blackboard`. Best for repeated or session-scoped use.

`RunF` provides a lower-level run helper for cases that compose the engine primitives more directly.

## The ReAct Loop

The `Executor` (in `github.com/v0lka/sp4rk/agent`) runs a Reason → Act → Observe loop. Each iteration is a `Step`:

```
┌──────────────────────────────────────────────────┐
│  1. ContextManager.BuildPrompt()                 │
│  2. LLMCaller.Call(ctx, ChatRequest)             │
│      └─ llm.Router → active provider/model       │
│  3. Parse response → Thought + ToolCall(s)       │
│  4. HITLHandler.OnToolCall() (if configured)     │
│      └─ may confirm / modify / reject            │
│  5. ToolExecutor.Execute() → tools.ToolRegistry  │
│  6. Record Observation in the Step               │
│     └─ set Step.IsUntrusted from Tool.IsUntrusted│
│  7. ContextManager.AddStep()                     │
│  8. CheckFill() → maybe Compact()                │
│  9. Check circuit breakers → maybe nudge/abort   │
└──────────────────────────────────────────────────┘
            │
            ▼
   finish tool called?  ──yes──→  return (ExecutorResult.Finished = true)
            │ no
            ▼
   step budget left?    ──no──→  HITLHandler.OnStepLimit
            │ yes                     │ (grant or deny)
            ▼                          ▼
        next iteration            deny → return (Finished = false)
```

A `Step` captures one iteration:

```go
type Step struct {
    Thought          string
    ReasoningContent string
    ReasoningItems   []llm.ReasoningItem
    Action           llm.ToolCall
    Observation      string
    IsError          bool
    IsUntrusted      bool
    TokensUsed       int
    UserNudge        string
    ResponseGroup    int64
    CacheHash        string
}
```

The loop terminates in one of three ways:

1. **Finish** — the agent calls the `finish` tool. `ExecutorResult.Finished` is `true`; `ExecutionResult.Status` is `"success"`.
2. **Budget exhausted** — `MaxSteps` iterations complete without a finish call. `HITLHandler.OnStepLimit` decides whether to grant more steps or stop. If denied, `Finished` is `false` and `Status` is `"partial"`.
3. **Circuit breaker** — consecutive identical calls, parse errors, truncation, or fruitless results trip an abort threshold, stopping the loop early.

## Plan & Execute Flow

For complex tasks, the engine composes three orchestration primitives:

```
   user message
        │
        ▼
   ┌─────────┐    Plan()     ┌──────────┐
   │ Planner │ ────────────→ │  Plan    │  (DAG of PlanSteps)
   └─────────┘               │  (DAG)   │
                             └────┬─────┘
                                  │ execute steps
                                  ▼
                          ┌───────────────┐
                          │  Conductor    │  runs each step as a ReAct loop
                          │  (per step)   │  via Executor
                          └───────┬───────┘
                                  │ step failed?
                                  ▼
                          ┌───────────────┐
                          │  Reflector    │  analyzes trajectory
                          └───────┬───────┘
                                  │ Reflection
                                  ▼
                       retry  /  replan  /  abort
```

1. **Planner** — `Planner.Plan` generates a `Plan`: a DAG of `PlanStep` values, each with a summary, description, `DependsOn` dependencies, a parallelism hint, estimated tools, and an optional `AgentProfile`. The planner may run a short ReAct exploration loop first (informed-exploration mode) based on the router's domain/complexity decision.
2. **Conductor** — for each step, the `Conductor` creates an `Executor` and runs a ReAct loop. Steps with no unmet dependencies can run in parallel via `RunSubAgent` goroutines. Results are written to the `Blackboard`.
3. **Reflector** — when a step fails, `Reflector.Reflect` examines the trajectory and prior reflections, then returns a `Reflection` with a root cause, hypotheses, and a suggested action: `"retry"`, `"replan"`, or `"abort"`.
4. **Retry/replan** — the conductor consults the reflection. A retry re-runs the step (up to `ExecutionConfig.MaxRetries`). A replan asks the `Planner` to generate a new plan accounting for completed steps and the failure. An abort stops execution with `Status == "aborted"`.

## Subagent Delegation Flow

Parallelism within a plan is achieved by launching subagents rather than sharing a single executor:

```
Conductor picks up a batch of ready steps (no unmet DependsOn)
         │
         ▼
for each step → agent.RunSubAgent(ctx, stepTask, tools, systemPrompt, events)
         │  (separate goroutine, own Executor + own ContextManager)
         ▼
events: SubAgentLaunch(step)   ← streamed to host
         │
         ▼
Executor.Run ReAct loop (independent of sibling subagents)
         │
         ▼
result written to shared Blackboard: bb.SetStepResult(stepID, output, err, steps)
         │
         ▼
events: SubAgentComplete(step)
```

The `Executor` is **not** thread-safe for concurrent loops on the same instance. Parallel steps always get separate executors and context managers, coordinated through the shared `Blackboard` rather than direct references.

## Event Flow

The `Events` interface (in `github.com/v0lka/sp4rk/agent`) is the engine's only channel for streaming lifecycle updates. The host application supplies an implementation; `NoopEvents` is the no-op default.

```
Executor / Conductor / SubAgent (via Events interface)
         │  StepStart, Thought, ToolCall, ToolResult, AssistantChunk,
         │  ContextFill, ContextCompaction, SubAgentLaunch, SubAgentComplete,
         │  Routing, PlanGenerated, Reflection, Finishing, ...
         ▼
host-supplied Events implementation
         │
         ▼
host decides what to do: persist to storage, push to a UI, log, drop, ...
```

Event emission is **decoupled from persistence**. The engine emits events; it does not write task state to a database. If the host needs durable state, it persists on the `OnBlackboardChanged` callback (fired after every successful blackboard write) or by reading the `ExecutionResult` / using a `Checkpointer`. This keeps sp4rk free of storage assumptions.

## Tool Execution Flow

```
Executor decides to call a tool
         │
         ▼
ToolExecutor.Execute(ctx, name, input)
         │
         ▼
tools.ToolRegistry.Execute(ctx, name, input)
  │
  ├─ 1. Lookup tool by name
  ├─ 2. (host-level gates — symlink gate, auto-approval, etc. — run HERE
  │       when the host has wired them; sp4rk provides the primitives)
  ├─ 3. Resolve effective policy (per-tool override > registry default > tool default)
  ├─ 4. Apply policy:
  │      ├─ PolicyAlwaysAllow → execute (unless ToolJudger flags the call)
  │      ├─ PolicyAlwaysDeny  → return error ToolResult
  │      └─ PolicyUserConfirm → ConfirmFunc(ctx, ConfirmationRequest) blocks
  │                              until the host responds
  │             │ (on ConfirmAllowOnce)
  │             ▼
  │      tool.Execute(ctx, input) → ToolResult{Content, IsError}
  │
  └─ returns ToolResult
         │
         ▼ (back in Executor)
cache + two-stage truncation
  │
  ├─ Skip if tool is non-cacheable
  ├─ Store full result in ToolResultCache (key = shortest unique SHA256 prefix)
  ├─ Stage 1: per-tool line/byte truncation (fragmentation nudge with hash)
  └─ Stage 2: token-based ToolResultBudget hard cap
```

The registry is **fail-closed**: a `PolicyUserConfirm` tool is denied when no `ConfirmFunc` is configured, so an injected instruction cannot trigger a silent mutation in a default-configured engine. See [security-model.md](security-model.md).

## Blackboard Flow

The `Blackboard` is the thread-safe shared state for a Plan&Execute run:

```
Conductor.Run
  │
  ├─ Receives/creates Blackboard (MapBlackboard, or PersistentBlackboard
  │   wrapping a store + Checkpointer)
  │
  ├─ Stores the Plan on the Blackboard
  │
  ├─ Each step executor:
  │   ├─ Reads dependency outputs:  bb.GetStepResult(depID)
  │   ├─ Writes own result:        bb.SetStepResult(stepID, output, err, steps)
  │   └─ Stores facts:             bb.StoreFact(fact)
  │
  ├─ Reflector reads: bb.GetAllStepResults(), bb.GetReflections()
  │
  └─ Final: bb.SetFinalResult(output)
       → OnBlackboardChanged callback notifies the host after every write
```

Steps communicate through the blackboard: one step can `StoreFact` with keywords, and a later step can `SearchFacts` to retrieve it. The `OnBlackboardChanged` callback on `Config` fires after every successful write, enabling the host to react (live UI updates, persistence).

## Invariants

- Every task enters the engine through `Framework.Execute`, `Framework.NewConductor`, or `RunF` — there is no back door into the `Executor`.
- The `Executor` consumes `llm` and `tools` only through the `LLMCaller`, `ToolExecutor`, and `ContextManager` interfaces.
- Event emission is the host's responsibility; the engine never writes task state to a database.
- A `Blackboard` is per-task, lifecycle-tied to a single run; it is never shared across tasks.
- Parallel steps run in separate subagent goroutines with their own `Executor` and `ContextManager`, coordinated only through the shared `Blackboard`.
- Tool execution is fail-closed for `PolicyUserConfirm` tools when no `ConfirmFunc` is configured.

## Anti-Patterns

### ❌ Bypassing the engine entry points

All tasks must go through `Framework.Execute` / `Framework.NewConductor` / `RunF`. Constructing an `Executor` directly and skipping the `Conductor` loses event emission, the blackboard, and Plan&Execute coordination.

### ❌ Sharing a single Executor across parallel steps

The `Executor` owns one ReAct loop and is not safe for concurrent iteration. Parallel steps require separate executors launched via `RunSubAgent`, coordinated through the `Blackboard`.

### ❌ Treating event emission as persistence

The engine emits `Events` but does not durably store state. A host that relies on events alone will lose state on restart; it must persist via `OnBlackboardChanged`, a `Checkpointer`, or the returned `ExecutionResult`.

### ❌ Sharing a Blackboard across tasks

The `Blackboard` is per-task. Reusing it across tasks corrupts step dependency resolution and reflection data.

### ❌ Calling tools outside the registry

Tools must execute through `tools.ToolRegistry.Execute` so policy resolution, the judge gate, the confirmation flow, caching, and truncation all apply. Calling `tool.Execute` directly bypasses every safety check.

## Related Specs

- [layers.md](layers.md) - Package layers the data flows through
- [security-model.md](security-model.md) - Tool execution gating and prompt-injection defense
- [../domains/orchestration/README.md](../domains/orchestration/README.md) - Conductor pipeline detail
- [../contracts/agent-execution.md](../contracts/agent-execution.md) - Host callbacks (Events, ConfirmFunc, HITL)
