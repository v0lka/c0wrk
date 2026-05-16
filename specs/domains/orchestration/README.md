# Orchestration

## Purpose

The orchestration domain coordinates the full lifecycle of a user request: classifying it, generating an execution plan, running the plan's steps, and reflecting on failures to retry or replan. It is the central nervous system of c0wrk.

## Key Files

- `core/orchestrator.go` — top-level Orchestrator (HandleMessage, Resume)
- `core/router.go` — request classification (Route)
- `core/planner.go` — DAG plan generation (Plan, Replan, PlanContinuation)
- `core/reflector.go` — failure analysis (Reflect)
- `core/stepconfig.go` — per-step configuration resolution
- `core/systemprompt.go` — system prompt construction with skill context
- `sdk/orchestration/orchestrator.go` — SDK Plan&Execute engine (Execute, Resume)
- `sdk/orchestration/dag.go` — DAG data structure
- `sdk/agent/executor.go` — ReAct loop (per-step execution)
- `sdk/prompt/builder.go` — fluent prompt construction with cache-break support
- `sdk/prompt/sampling.go` — family-aware temperature defaults (SamplingConfig, DefaultSampling)

## Core Types

```go
// Top-level orchestrator (core layer)
type Orchestrator struct {
    engine               *orchestration.Orchestrator  // SDK P&E engine
    planner              *Planner
    router               *Router
    llm                  LLMCaller
    toolRegistry         *sdktools.ToolRegistry       // SDK registry (basic store)
    coreToolRegistry     *tools.ToolRegistry          // core registry (policy/judge/hooks)
    config               OrchestratorConfig
    contextFactory       ContextManagerFactory
    logger               *slog.Logger
    emitter              Emitter
    modelRegistry        *llm.ModelRegistry
    bbFactory            BlackboardFactory
    conversationHistory  []llm.Message                // retained history for routing context
    taskStore            TaskPersistence              // optional, for ContinueTask BB restoration
    bbRestoreFunc        BlackboardRestoreFunc        // optional, restores BB from store
    trackingCaller       *llm.TrackingCaller          // per-step context tracker wiring
    vectorSearchFunc     tools.VectorSearchFunc       // for vector search hints
    skillManager         *skills.SkillManager         // skill discovery and activation
    currentRequestCtx    atomic.Pointer[context.Context]        // scoped to active HandleMessage
    currentRequestSkills atomic.Pointer[[]skills.SkillDescriptor] // router-matched skills
}

// Configuration
type OrchestratorConfig struct {
    MaxSteps                  int    // default: 30
    KeepFirst                 int    // default: 3
    KeepLast                  int    // default: 10
    MaxRetries                int    // default: 2 (3 total attempts)
    MaxHistoryMessages        int    // default: 20
    MaxDependencyContextChars int    // default: 8000
    Model                     string
    ReasoningEffort           llm.ReasoningEffort
    RoleOverrides             map[string]string
    StepLimitFunc             agent.StepLimitFunc
}

// Routing result
type RoutingDecision struct {
    Domain             string   // "code" | "research" | "general" | "mixed"
    Complexity         int      // 1-5
    NeedsClarification bool
    MatchedSkills      []string
}

// Handle options
type HandleOptions struct {
    TaskID        string   // non-empty = continuation of existing task
    ExecutionMode string   // "normal" = single-step plan, "advanced" = full multi-step DAG
    UserSkills    []string // explicitly requested by user via /skill refs (bypass router)
}

// Handle result
type HandleResult struct {
    Output          string
    RoutingDecision *RoutingDecision
    Plan            *Plan
    Blackboard      Blackboard
    AttemptCount    int
    Reflections     []Reflection
}
```

## Flow

```
HandleMessage(ctx, message, sessionID, opts)
│
├─ 0. Set PlanModeKey in context
├─ 0a. Inject vector search hints (non-blocking, 2s timeout)
├─ 0b. Emit initial context_fill (0%)
│
├─ 1. Blackboard lifecycle:
│     ├─ opts.TaskID == "": create new BB via bbFactory
│     └─ opts.TaskID != "": restore BB from persistence
│
├─ 2. Load available tools from registry
│
├─ 3. ROUTE: router.Route(ctx, routingMessage, tools, history, skills)
│     → When opts.UserSkills is non-empty, routingMessage is augmented with
│       skill descriptions via buildSkillAugmentedRoutingMessage so the router
│       can classify domain/complexity based on the actual task semantics
│     → RoutingDecision
│     → Emit Routing event
│
├─ 4. Activate matched skills:
│     → Merge router-matched skills with opts.UserSkills (deduplicated union)
│     → Set ActiveSkills in context
│     → Apply skill policy overrides to tool registry
│     → Emit SkillsActivated event
│
├─ 5. NeedsClarification? → return early with clarification message
│     (suppressed when opts.UserSkills is non-empty — explicit /skill invocation
│      means user intent is clear regardless of how the stripped message reads)
│
├─ 6. Branch:
│     ├─ First message (TaskID == ""):
│     │     ├─ opts.ExecutionMode == "normal" → Plan(singleStep=true) → 1 step
│     │     └─ otherwise → Plan(singleStep=false) → full DAG
│     │     → Set plan on BB → engine.Resume(ctx, bb)
│     │
│     └─ Continuation (TaskID != ""):
│           ├─ opts.ExecutionMode == "normal" → PlanContinuation(singleStep=true)
│           └─ otherwise → PlanContinuation(singleStep=false)
│           → Merge into existing plan → engine.Resume(ctx, bb)
│
├─ 7. SDK engine executes (Plan&Execute loop with retry/replan)
│
├─ 8. Persist task completion on BB
│
└─ 9. Return HandleResult
```

## Execution Modes

| Mode     | Condition                              | Behavior                                                  |
| -------- | -------------------------------------- | --------------------------------------------------------- |
| Normal   | `opts.ExecutionMode == "normal"`       | Plan(singleStep=true) → exactly 1 step with full ToT     |
| Advanced | any other value (including "advanced") | Plan(singleStep=false) → full DAG, parallel execution     |

## Invariants

- Every user message passes through Route (even in normal mode)
- Routing always produces a valid domain from {"code", "research", "general", "mixed"}
- Complexity is always in range [1, 5]
- MaxRetries bounds total attempts at MaxRetries + 1
- Blackboard is created once per first message and restored for continuations
- Vector search hints are non-blocking (2s timeout, failure is acceptable)
- Skills are activated task-wide but rendered per-step by StepConfigurator
- NeedsClarification is suppressed when UserSkills is non-empty (explicit `/skill` invocation implies clear intent)
- currentRequestCtx and currentRequestSkills are cleared at end of HandleMessage

## Configuration

From `config.yaml` (via BuilderConfig → OrchestratorConfig):

| Parameter                                 | Default | Description                       |
| ----------------------------------------- | ------- | --------------------------------- |
| `executor.max_react_steps`                | 50      | Max ReAct iterations per step     |
| `executor.max_retries`                    | 2       | Max retry attempts (3 total)      |
| `orchestration.maxHistoryMessages`        | 20      | Conversation history window       |
| `orchestration.maxDependencyContextChars` | 8000    | Max chars from dependency outputs |
| `reasoning.base_effort`                   | "high"  | Base reasoning effort             |
| `reasoning.role_overrides`                | {}      | Per-role effort overrides         |

Note: yaml key casing is mixed across config sections — `executor.*` and `reasoning.*` keys use `snake_case`, while `orchestration.*` and `toolLimits.*` keys use `camelCase`. This matches the struct tags in `backend/config/config.go`.

## Extension Points

- Custom `ContextManagerFactory` for alternative memory strategies
- Custom `BlackboardFactory` for alternative persistence
- Custom `StepLimitFunc` for step limit and circuit breaker handling
- System prompt customization via `buildSystemPrompt` function

## Related Specs

- [router.md](router.md) — request classification
- [planner.md](planner.md) — plan generation
- [executor.md](executor.md) — step execution
- [../memory/README.md](../memory/README.md) — context management
- [../../contracts/core-sdk.md](../../contracts/core-sdk.md) — SDK interface wiring
