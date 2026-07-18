# Orchestration

## Purpose

The orchestration domain coordinates the full lifecycle of a user request: classifying it, running a single ReAct loop (the Conductor) that owns the task end-to-end, and delegating subtasks to isolated subagents. The Conductor replaces the prior system-driven plan-execute-reflect pipeline with an agent-driven loop where planning, interaction, and reflection are tool calls, not pipeline phases.

## Key Files

- `core/orchestrator.go` — top-level Orchestrator (HandleMessage, Resume)
- `core/orchestrator_handle.go` — HandleMessage body: router → Conductor launch
- `core/orchestrator_goal.go` — goal mode: deriveGoal, runGoalLoop, resumeGoalLoop, runGoalTurns, budgets, anti-spin, pause signal (see [../goal-mode.md](../goal-mode.md))
- `core/conductor.go` — Conductor entry point: builds system prompt, tool set, launches `Executor.Run`
- `core/tools/delegate.go` — `delegate` tool (subagent launch with DAG + async)
- `core/tools/declare_plan.go` — `declare_plan` tool (roadmap publish + approval gate)
- `core/tools/reflect.go` — `reflect` tool (invokes Reflector on trajectory)
- `core/tools/cancel_delegation.go` — `cancel_delegation` tool (async cancellation)
- `core/delegation_registry.go` — Delegation Registry (active/completed delegations per Conductor run)
- `github.com/v0lka/sp4rk/agent/router/router.go` — request classification (Route)
- `core/router_adapter.go` — core adapter wrapping sp4rk router
- `github.com/v0lka/sp4rk/agent/executor.go` — ReAct loop (Conductor and subagents are both `Executor.Run` instances)
- `github.com/v0lka/sp4rk/agent/subagent.go` — `RunSubAgent` / `RunSubAgentsParallel` (isolated executor in goroutine)
- `github.com/v0lka/sp4rk/agent/reflector/reflector.go` — failure analysis (Reflect), invoked via the `reflect` tool
- `github.com/v0lka/sp4rk/orchestration/dag.go` — DAG data structure (used by `delegate` and `declare_plan`)
- `core/plan_serializer.go` — Plan ↔ Markdown serialization (used by `declare_plan`)
- `core/systemprompt.go` — system prompt construction with skill context
- `github.com/v0lka/sp4rk/prompt/builder.go` — fluent prompt construction with cache-break support
- `github.com/v0lka/sp4rk/prompt/sampling.go` — family-aware temperature defaults

## Core Types

```go
// Top-level orchestrator (core layer)
type Orchestrator struct {
    router               *router.Router
    llm                  agent.LLMCaller
    modelSwitcher        *llm.Router
    toolRegistry         *sdktools.ToolRegistry
    coreToolRegistry     *tools.ToolRegistry
    config               OrchestratorConfig
    contextFactory       ContextManagerFactory
    logger               *slog.Logger
    emitter              Emitter
    modelRegistry        *llm.ModelRegistry
    bbFactory            BlackboardFactory
    conversationHistory  []llm.Message
    taskStore            TaskPersistence
    bbRestoreFunc        BlackboardRestoreFunc
    trackingCaller       *llm.TrackingCaller
    tokenCounter         llm.TokenCounter
    vectorSearchFunc     builtins.VectorSearchFunc
    skillManager         *skills.SkillManager
    isNoProject          bool
    currentRequestCtx    atomic.Pointer[context.Context]
    currentRequestSkills atomic.Pointer[[]skills.SkillDescriptor]
}

// Configuration
type OrchestratorConfig struct {
    MaxRedelegationDepth        int     // cap on recursive delegation (default: 2)
    KeepFirst                   int     // context pruning: protected head messages
    KeepLast                    int     // context pruning: protected tail messages
    PlannerHistoryBudgetTokens int     // retained for router history compaction
    PlannerHistoryKeepRecentRatio float64
    MaxDependencyContextChars   int     // chars from dependency outputs injected into delegation task
    PreWarningPercent           int     // context-fill notification threshold
    Model                       string
    ReasoningEffort             string
    HITLHandler                 agent.HITLHandler
    InjectionDefenseEnabled     bool
    AgentsMDMaxBytes            int
}

// Routing result — domain, complexity, skills, model only.
// No mode, no clarification: the Conductor handles both.
type RoutingDecision struct {
    Domain         string   // "code" | "research" | "general" | "mixed"
    Complexity     int      // 1-5
    MatchedSkills  []string
}

// Handle options
type HandleOptions struct {
    TaskID          string   // non-empty = continuation of existing task
    UserSkills      []string // explicitly requested by user via /skill refs
    ModelOverride   string   // non-empty = use this model for the Conductor
    ReasoningEffort string   // native reasoning effort for the model family
    SessionPlansDir string   // directory for session-scoped plan files (used by declare_plan)
    Goal                bool             // first-message-only: enter the multi-turn goal loop (see ../goal-mode.md)
    GoalBudgetOverride  *goal.GoalBudget // optional per-request budget tightening; any non-zero field overrides the config default
}

// Handle result
type HandleResult struct {
    Output          string
    RoutingDecision *RoutingDecision
    Plan            *Plan               // last plan declared via declare_plan, if any
    Blackboard      Blackboard
    Status          orchestration.ExecutionStatus  // success | partial | failed | aborted | cancelled
    Delegations     []DelegationSummary             // active/completed delegations at finish
}
```

## Flow

```
HandleMessage(ctx, message, sessionID, opts)
│
├─ 0. Inject vector search hints (non-blocking, 2s timeout)
├─ 0a. Emit initial context_fill (0%)
│
├─ 1. Blackboard lifecycle:
│     ├─ opts.TaskID == "": create new BB via bbFactory
│     └─ opts.TaskID != "": restore BB from persistence
│
├─ 2. Load available tools from registry (filtered via ListFiltered
│     to exclude disabled tools in No Project mode)
│
├─ GOAL MODE (first message only): when opts.Goal && opts.TaskID == "",
│     dispatch to runGoalLoop instead of the route→Conductor flow below.
│     The goal loop derives a {condition, verify} goal with user sign-off,
│     then iterates the Conductor turn-by-turn until the agent declares the
│     goal met (with evidence), the budget is exhausted, the agent goes idle
│     (anti-spin), or the goal is paused. Each turn is a fresh Executor.Run;
│     the loop holds the single-flight guard for its whole run. See
│     [../goal-mode.md](../goal-mode.md).
│
├─ 3. ROUTE (or continuation fast-path):
│     ├─ Continuation fast-path: if opts.TaskID != "" AND restored BB
│     │   has an existing routing decision → reuse it, reactivate skills,
│     │   skip the router LLM call.
│     └─ Default: router.Route(ctx, routingMessage, tools, history, skills)
│         → When opts.UserSkills is non-empty, routingMessage is augmented
│           with skill descriptions via buildSkillAugmentedRoutingMessage.
│         → RoutingDecision { Domain, Complexity, MatchedSkills }
│         → Emit Routing event
│
├─ 3a. No Project: override routing.Domain from "code" to "general"
│
├─ 4. Activate matched skills:
│     → Merge router-matched skills with opts.UserSkills (deduplicated union)
│     → Set ActiveSkills in context
│     → Apply skill policy overrides to tool registry
│     → Emit SkillsActivated event
│
├─ 5. Build Conductor:
│     ├─ System prompt = orchestrator core + family overlay + verification
│     │   mandate + workspace + env + active skills (verbatim) +
│     │   Conductor tool guidance (delegate, declare_plan, reflect, ask_user)
│     ├─ Tool set = file ops + search + internal tools (ask_user, finish,
│     │   store_fact, search_facts, update_checklist, declare_step_complete,
│     │   read_step_output, semantic_search) + Conductor tools (delegate,
│     │   declare_plan, reflect, cancel_delegation)
│     ├─ ContextManager via contextFactory
│     └─ Delegation Registry injected into context
│
├─ 6. Launch Conductor = executor.Run(ctx, conductorTools, cm)
│     └─ The Conductor owns the task until it calls finish.
│        Planning, decomposition, interaction, reflection all happen
│        as tool calls inside this loop.
│
├─ 7. Persist task outcome on BB per typed status (persistTaskOutcome):
│     success → completed; partial → left in_progress (resumable);
│     failed/aborted → failed (resumable)
│
└─ 8. Return HandleResult (carries Status + Delegations summary).
      A recordConversationOutcome defer updates conversationHistory for EVERY
      terminal outcome (success, failure, cancel).
```

## Execution Surface

| Surface | When | Behaviour |
| ------- | ---- | --------- |
| Simple task | Conductor never calls `delegate` | One ReAct loop, no subagents. Replaces the former "normal" single-step mode without planner overhead. |
| Complex task | Conductor calls `delegate` with one or more tasks | Subagents run isolated ReAct loops; Conductor sees only summaries. Replaces the former "advanced" multi-step DAG mode. |
| Interactive skill | Conductor calls `ask_user` / `declare_plan` mid-loop | Skill instructions are executable because the tools are available inside the loop. No pipeline-level gate. |
| Goal mode | First message carries `/goal` (`opts.Goal`) | `runGoalLoop` replaces the single route→Conductor pass: derives a {condition, verify} goal with user sign-off, then iterates the Conductor turn-by-turn (each a fresh `Executor.Run`) until the agent declares the goal met, the budget is exhausted, the agent goes idle, or the goal is paused. See [../goal-mode.md](../goal-mode.md). |

There is no `executionMode` toggle. The Conductor chooses its own granularity based on task complexity and its system-prompt guidance.

## Invariants

- Every FIRST user message passes through Route; continuation messages (opts.TaskID != "") with a restored routing decision take the continuation fast-path and skip the router.
- Routing always produces a valid domain from {"code", "research", "general", "mixed"}.
- Complexity is always in range [1, 5].
- Exactly one Conductor `Executor.Run` instance owns a given task from start to finish.
- **Goal mode is a turn-of-Conductors, not one long-lived executor.** When `opts.Goal && opts.TaskID == ""`, `runGoalLoop` iterates: each turn launches a fresh `Executor.Run` via `RunConductor`, reusing the normal continuation-trajectory mechanism so dialogue context persists across the turn boundary. Routing is decided once at the top of `runGoalLoop` (before derivation) and inherited unchanged by every turn; no turn re-routes. The loop holds the single-flight guard for its whole run; `PauseGoal` releases it by transitioning to `paused` and breaking out. See [../goal-mode.md](../goal-mode.md).
- The Conductor always has `ask_user`, `declare_plan`, `reflect`, `delegate`, `cancel_delegation`, `finish` available regardless of skill or tool-policy overrides (they are internal tools, bypass policy).
- `finish` with pending async delegations requires either a prior `cancel_delegation` for each, or an implicit join (the Conductor waits for all pending delegations before finishing).
- `ExecutionResult.Status` is the typed success contract: success | partial | failed | aborted | cancelled. Callers consult it instead of parsing Output.
- Blackboard is created once per first message and restored for continuations.
- Vector search hints are non-blocking (2s timeout, failure is acceptable).
- Skills are activated task-wide and rendered verbatim in the Conductor system prompt (no truncation).
- currentRequestCtx and currentRequestSkills are cleared at end of HandleMessage.
- conversationHistory is updated for every terminal outcome of HandleMessage and Resume.
- When the assistant output contains tool-call syntax printed as text (failure-mode detected by `agent.DetectToolCallSyntaxInContent`), the history records a `HistoryNoteFailed(...)` note instead of the hallucinated text.
- isNoProject: routing domain "code" is overridden to "general" after classification.
- SetNoProjectMode(): disables code tools and adds extended bash command blacklist on the core tool registry.

## Configuration

From `config.yaml` (via BuilderConfig → OrchestratorConfig):

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| `conductor.maxRedelegationDepth` | 2 | Cap on recursive delegation when `allow_redelegate` is true |
| `conductor.maxDependencyContextChars` | 8000 | Max chars from dependency outputs injected into a delegation task |
| `executor.keepFirst` | 3 | Protected head messages during compaction |
| `executor.keepLast` | 10 | Protected tail messages during compaction |
| `orchestration.plannerHistoryBudgetTokens` | 4000 | Max tokens for conversation history sent to router (triggers summarisation compaction when exceeded) |
| `orchestration.plannerHistoryKeepRecentRatio` | 0.75 | Fraction of budget reserved for recent messages during compaction |

Note: yaml key casing is mixed across config sections — `executor.*` keys use `snake_case`, while `orchestration.*` and `conductor.*` keys use `camelCase`. This matches the struct tags in `backend/config/config.go`.

## Extension Points

- Custom `ContextManagerFactory` for alternative memory strategies.
- Custom `BlackboardFactory` for alternative persistence.
- Custom `HITLHandler` for step limit and circuit breaker handling (applies to both Conductor and subagents).
- System prompt customization via the Conductor prompt builder.
- New Conductor tools: implement `tools.Tool`, register in `core/tools/builtin_registration.go`, add to the Conductor tool set in `core/conductor.go`. See [../../contracts/conductor-tools.md](../../contracts/conductor-tools.md).

## Related Specs

- [sp4rk orchestration overview](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/README.md) — canonical engine orchestration specs (Conductor, Executor, Router, Planner, Reflector, Subagents)
- [conductor.md](conductor.md) — Conductor component detail
- [delegation.md](delegation.md) — delegate tool and async delegation registry
- [router.md](router.md) — request classification
- [executor.md](executor.md) — ReAct loop (shared by Conductor and subagents)
- [../goal-mode.md](../goal-mode.md) — goal mode (multi-turn objective loop, reuses the Conductor per turn)
- [../memory/README.md](../memory/README.md) — context management
- [../../contracts/conductor-tools.md](../../contracts/conductor-tools.md) — Conductor tool surface contract
- [../../decisions/012-conductor-orchestration-pipeline.md](../../decisions/012-conductor-orchestration-pipeline.md) — architectural decision
