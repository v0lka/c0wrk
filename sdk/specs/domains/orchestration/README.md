# Orchestration

## Purpose

The orchestration domain turns a single ReAct agent loop into a multi-step, self-correcting execution engine. It provides the shared state container (the **Blackboard**), the plan data model (a DAG of steps), the single-loop task owner (the **Conductor**), request classification (the **Router**), plan generation (the **Planner**), failure analysis (the **Reflector**), and delegated parallel execution (**subagents**). The Conductor is an agent-driven loop: planning, decomposition, interaction, and reflection are tool calls driven by the host application, not pipeline phases baked into the engine.

## Key Files

- `github.com/v0lka/sp4rk/orchestration` — Conductor, ConductorConfig, Blackboard interface + MapBlackboard, Plan/PlanStep data model, ExecutionStatus/ExecutionResult, DAG utilities, Checkpointer persistence
- `github.com/v0lka/sp4rk/agent` — Executor (the ReAct loop primitive), Events, HITLHandler, FinishTool, RunSubAgent/RunSubAgentsParallel, trajectory + store context helpers
- `github.com/v0lka/sp4rk/agent/router` — request classification (domain, complexity, skills)
- `github.com/v0lka/sp4rk/agent/reflector` — failure analysis producing a `Reflection`
- `github.com/v0lka/sp4rk/planner` — DAG plan generation, replanning, continuation
- `github.com/v0lka/sp4rk/llm` — Router (LLM caller), ModelRegistry, token counting
- `github.com/v0lka/sp4rk/memory` — ContextManager / ContextWindow backing each Conductor run
- `github.com/v0lka/sp4rk/prompt` — system-prompt construction with cache-break support

## Core Types

```go
// The single-loop task owner. Wraps an agent.Executor and wires in
// blackboard-backed stores so context-aware tools see shared state.
type Conductor struct {
    cfg ConductorConfig
}

// Every dependency a Conductor needs, populated by the caller before Run.
type ConductorConfig struct {
    LLM                 agent.LLMCaller
    Tools               agent.ToolExecutor
    ToolRegistry        *tools.ToolRegistry
    TokenCounter        llm.TokenCounter
    Model               string
    ModelRegistry       *llm.ModelRegistry
    ContextFactory      ContextManagerFactory   // required
    SystemPrompt        SystemPromptFactory     // required
    MaxSteps            int                     // defaults to 80 when zero
    ToolResultBudget    agent.ToolResultBudget
    CircuitBreaker      agent.CircuitBreakerConfig
    HITLHandler         agent.HITLHandler
    ToolCache           *agent.ToolResultCache
    PerToolTruncation   map[string]agent.ToolTruncationConfig
    ReasoningEffort     string
    PreWarningPercent   int
    NonCacheableTools   []string
    ConversationHistory []llm.Message
}

// Shared, thread-safe container for all task state.
type Blackboard interface {
    GetOriginalRequest() string
    SetOriginalRequest(req string)
    GetPlan() *Plan
    SetPlan(plan *Plan)
    GetStepResult(stepID string) (StepResult, bool)
    GetStepSummary(stepID string) string
    GetAllStepResults() map[string]StepResult
    SetStepResult(stepID, output string, err error, steps []agent.Step)
    GetReflections() []Reflection
    AddReflection(r Reflection)
    GetFinalResult() string
    SetFinalResult(result string)
    Search(query string) []BlackboardEntry
    StoreFact(fact Fact)
    SearchFacts(keywords []string) []Fact
    GetFacts() []Fact
}

// A DAG of execution steps.
type Plan struct {
    Steps              []PlanStep
    ExplorationContext string
}

// Typed terminal outcome — callers consult Status instead of parsing Output.
type ExecutionStatus string // "success" | "partial" | "failed" | "aborted" | "cancelled"

type ExecutionResult struct {
    Output       string
    Plan         *Plan
    Blackboard   Blackboard
    AttemptCount int
    Reflections  []Reflection
    Status       ExecutionStatus
    FailedSteps  int
}
```

## Flow

```
HandleRequest(ctx, message, bb, availableTools)
│
├─ 1. Classify: router.Route(ctx, message, tools, history, skills)
│      → RoutingDecision { Domain, Complexity, MatchedSkills }
│
├─ 2. Select compaction strategy from Domain (code→sliding, research→summarization,
│      general/mixed→sliding, hierarchical when complexity >= 4)
│
├─ 3. Build a Conductor from ConductorConfig (LLM caller, tool executor,
│      context factory, system-prompt factory, MaxSteps, circuit breakers, HITL)
│
├─ 4. conductor.Run(ctx, message, bb, availableTools, events, strategy)
│      │
│      │  A single ReAct loop owns the task end-to-end. The host application
│      │  supplies tools — including, optionally, host-defined delegation,
│      │  planning, interaction, and reflection tools built atop these primitives.
│      │
│      ├─ reads state from the blackboard (facts, step outputs, plan)
│      ├─ calls the Executor's tools (filesystem, search, web, …)
│      └─ finish ends the task with a final answer
│
└─ 5. Return ExecutionResult { Status, Output, Blackboard }
```

The Conductor is the only top-level execution entry point this domain exposes. Delegation (running isolated ReAct loops in goroutines) is a host concern that builds on `agent.RunSubAgent`/`RunSubAgentsParallel`; the engine itself is agnostic to whether a host chooses to delegate.

## Invariants

- Exactly one Conductor `Executor.Run` instance owns a given task from start to finish.
- `Conductor.Run` injects `StepOutputStore`, `FactStore`, and `FinalResultStore` adapters derived from the blackboard into `ctx`, so blackboard-backed tools read shared state.
- Routing always produces a valid domain from `{"code", "research", "general", "mixed"}` after validation.
- Complexity is always clamped to `[1, 5]`.
- `ExecutionResult.Status` is the typed success contract: callers consult it instead of parsing `Output`.
- The Conductor's `finish`-join guard rejects `finish` while a `PendingDelegations` registry (injected via `WithDelegationRegistry`) reports pending async work.
- The blackboard is created once per first request and restored for continuations via a `Checkpointer`.
- The `finish` tool is always available in every run (appended automatically if absent).
- A `ContextManager` returned by the factory that implements `TaskAware`/`ConversationAware`/`TrackerProvider` gets the corresponding capabilities wired; otherwise they are safely skipped.

## Configuration

`ConductorConfig` is the single source of Conductor tuning. Notable defaults:

| Field | Default | Description |
| ----- | ------- | ----------- |
| `MaxSteps` | `80` (when zero) | Per-run ReAct step budget. |
| `ReasoningEffort` | `""` | Reasoning effort passed to reasoning-capable models. |
| `PreWarningPercent` | `0` (disabled) | Context-fill percentage that triggers a pre-compaction `store_fact` nudge. |
| `CircuitBreaker` | `DefaultCircuitBreakerConfig()` | Loop-protection thresholds (see [executor.md](executor.md)). |
| `ContextFactory` / `SystemPrompt` | required | Both must be non-nil or `Run` returns an error. |
| `ModelRegistry` | optional | Resolves model metadata; an unknown model falls back to a usable metadata (`ContextWindow=128000`, `OutputLimit=4096`). |

## Extension Points

- **Custom `ContextManagerFactory`** for alternative memory/compaction strategies (see [../memory/README.md](../memory/README.md)).
- **Custom `SystemPromptFactory`** to assemble the system prompt from the step description and model metadata.
- **Custom `Planner` / `Reflector`** — implement the `orchestration.Planner` / `orchestration.Reflector` interfaces (the SDK provides reference implementations in `planner` / `agent/reflector`).
- **Custom `Blackboard`** persistence via the `Checkpointer` interface and `CheckpointedBlackboard`.
- **Custom `Events`** — embed `orchestration.NoopEvents` and override only the hooks you need (plan/step/reflection lifecycle).
- **Delegation tools** — the host wires `delegate`/`declare_plan`/`reflect`/`cancel_delegation` as ordinary tools; the engine exposes the primitives (`RunSubAgent`, `PendingDelegations`, `Planner`, `Reflector`) they are built on.

## Related Specs

- [conductor.md](conductor.md) — the single-loop task owner
- [executor.md](executor.md) — the ReAct loop primitive the Conductor wraps
- [router.md](router.md) — request classification
- [planner.md](planner.md) — DAG plan generation
- [reflector.md](reflector.md) — failure analysis and self-correction
- [subagents.md](subagents.md) — delegated parallel execution primitive
- [../memory/README.md](../memory/README.md) — context management backing each run
- [../memory/blackboard.md](../memory/blackboard.md) — shared task state container
