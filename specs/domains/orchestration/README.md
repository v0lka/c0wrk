# Orchestration

## Purpose

The orchestration domain coordinates the full lifecycle of a user request: classifying it, running a single ReAct loop (the Conductor) that owns the task end-to-end, and delegating subtasks to isolated subagents. The Conductor replaces the prior system-driven plan-execute-reflect pipeline with an agent-driven loop where planning, interaction, and reflection are tool calls, not pipeline phases.

## Key Files

- `core/orchestrator.go` — top-level Orchestrator (HandleMessage, Resume, `ResolveVisionOptions` — per-call markitdown vision params for the currently active model, consumed by the backend attachment flow)
- `core/orchestrator_handle.go` — HandleMessage body: router → Conductor launch, `prepareRequestContext` (task-context enrichment incl. the markitdown vision resolver, attached on HandleMessage and Resume; flows through to subagent delegations)
- `core/orchestrator_goal.go` — goal mode: deriveGoal, runGoalLoop, resumeGoalLoop, runGoalTurns, budgets, anti-spin (see [../goal-mode.md](../goal-mode.md))
- `core/conductor.go` — Conductor entry point: builds system prompt, tool set, launches `Executor.Run`
- `core/smallllm/tools_filter.go` — pure tool-set narrowing for the Small-LLM essential-tools variant (see [../small-llm.md](../small-llm.md))
- `core/tools/delegate.go` — `delegate` tool (subagent launch with DAG + async)
- `core/tools/declare_plan.go` — `declare_plan` tool (roadmap publish + approval gate)
- `core/tools/reflect.go` — `reflect` tool (invokes Reflector on trajectory)
- `core/tools/cancel_delegation.go` — `cancel_delegation` tool (async cancellation)
- `core/tools/delegation_registry.go` — Delegation Registry (active/completed delegations per Conductor run)
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
    requestInFlight      atomic.Bool // single-flight guard: one active HandleMessage per *Orchestrator
}
// NOTE: This is an illustrative subset. The full struct also carries
// Conductor-run dependencies (reflector, toolExec, toolCache, perToolTrunc,
// toolResultBudget, circuitBreaker, stepDumpTracker, providerName) and
// goal-loop plumbing (goalProposer, goalTurnRunner,
// activePause). The fields above are the ones consumed by the
// route→Conductor flow.

// Configuration
type OrchestratorConfig struct {
    MaxRedelegationDepth        int     // cap on recursive delegation (default: 2)
    KeepFirst                   int     // context pruning: protected head messages
    KeepLast                    int     // context pruning: protected tail messages
    MaxDependencyContextChars   int     // chars from dependency outputs injected into delegation task
    PreWarningPercent           int     // context-fill notification threshold
    Model                       string
    ReasoningEffort             string
    HITLHandler                 agent.HITLHandler
    InjectionDefenseEnabled     bool
    AgentsMDMaxBytes            int
    AgentsMDSearchPaths         []string // extra AGENTS.md paths (global, c0wrk) read ahead of the workspace file
    ConductorHistoryWindow      int     // recent conversation messages injected into the Conductor context (default: 20)
    GoalLoop                    GoalLoopSettings // goal-loop settings: Verification gates the independent verifier turn ("independent" | "off"); see [../goal-mode.md](../goal-mode.md)
    SmallLLM                    SmallLLMSettings // small-LLM profile (master toggle + essential-tools / system-prompt variants); see [../small-llm.md](../small-llm.md)
}

// Routing result — domain, complexity, skills, and a clarification flag.
// No `mode` (removed): the Conductor decides execution granularity.
// `NeedsClarification` still exists on the sp4rk type but c0wrk IGNORES it
// (core/orchestrator_handle.go: "Router.NeedsClarification is ignored: the
// Conductor decides when to ask") — clarification is a Conductor `ask_user`
// decision, not a routing-driven pipeline branch.
type RoutingDecision struct {
    Domain             string   // "code" | "research" | "general" | "mixed"
    Complexity         int      // 1-5
    NeedsClarification bool     // present on the type; c0wrk does not branch on it
    MatchedSkills      []string
    MatchedTools       []string // semantic tool selection (Small-LLM essential-tools profile)
}

// Handle options
type HandleOptions struct {
    TaskID             string                     // non-empty = continuation of existing task
    UserSkills         []string                   // explicitly requested by user via /skill refs (bypass router)
    UserAgents         []string                   // explicitly requested by user via #agent-name mentions (drives the "Requested Subagents" prompt directive)
    ModelOverride      string                     // non-empty → use this model for all LLM calls; empty → router default
    ReasoningEffort    string                     // non-empty → native reasoning value for all LLM calls; empty → use family default
    SessionPlansDir    string                     // directory for session-scoped plan files (used by declare_plan tool)
    PendingAttachments []orchestration.Attachment // attachments staged by AttachFiles, flushed into the blackboard before execution
    PendingImages      []llm.ContentBlock         // image attachments staged by AttachFiles, passed to the context window as image content blocks
    ReviewMode         bool                       // renders the Code Review prompt section (agent treats review comments as actionable code edits)
    Goal               bool                       // enter the multi-turn goal loop (see ../goal-mode.md)
    GoalBudgetOverride *goal.GoalBudget           // optional per-request budget tightening; any non-zero field overrides the config default
}

// Handle result
type HandleResult struct {
    Output          string
    RoutingDecision *RoutingDecision
    Plan            *Plan               // last plan declared via declare_plan, if any
    Blackboard      Blackboard
    Reflections     []orchestration.Reflection           // reflect-tool outputs, if any
    Status          orchestration.ExecutionStatus  // success | partial | failed | aborted | cancelled
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
├─ GOAL MODE: when opts.Goal, dispatch to runGoalLoop instead of the
│     route→Conductor flow below. Goal mode runs on BOTH a fresh task
│     (opts.TaskID == "") and a continuation (opts.TaskID != ""); on a
│     continuation the prior task's blackboard is restored first.
│     The goal loop derives a {condition, verify} goal with user sign-off,
│     then iterates the Conductor turn-by-turn until the agent declares the
│     goal met (with evidence), the budget is exhausted, the agent goes idle
│     (anti-spin), or the task is paused (a session-level control). Each turn
│     is a fresh Executor.Run; the loop holds the single-flight guard for its
│     whole run. See [../goal-mode.md](../goal-mode.md).
│
├─ 3. ROUTE (or continuation fast-path):
│     ├─ Continuation fast-path: if opts.TaskID != "" AND the restored BB
│     │   has BOTH an existing plan AND a routing decision → reuse the
│     │   routing decision, reactivate skills, skip the router LLM call.
│     │   The plan gate prevents a plan-less continuation from mis-routing.
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
│     → Emit SkillsActivated event
│       (skills narrow the available toolset only — policy comes from
│        security.groups, ADR-024; there is no skill policy layer)
│
├─ 4a. Small-LLM essential-tools filter (non-goal path only):
│     → When small_llm.enabled AND essential_tools.enabled, narrow the
│       available tool set via smallllm.SelectTools (router-matched +
│       always-present + protected base + every MCP tool, capped by
│       max_tools). Emit tools_assigned. No-op when the profile is off.
│     → Goal mode returns before this point and is never narrowed.
│
├─ 5. Build Conductor:
│     ├─ System prompt = orchestrator core + family overlay + verification
│     │   mandate + workspace + env + active skills (verbatim) +
│     │   Conductor tool guidance (delegate, declare_plan, execute_plan,
│     │   reflect, ask_user)
│     ├─ Tool set = file ops + search + system-group tools (ask_user, finish,
│     │   store_fact, search_facts, update_checklist, declare_step_complete,
│     │   read_step_output, semantic_search) + Conductor tools (delegate,
│     │   declare_plan, execute_plan, reflect, cancel_delegation)
│     ├─ ContextManager via contextFactory
│     └─ Delegation Registry injected into context
│
├─ 6. Launch Conductor = executor.Run(ctx, conductorTools, cm)
│     └─ The Conductor owns the task until it calls finish.
│        Planning, decomposition, interaction, reflection all happen
│        as tool calls inside this loop.
│
├─ 7. Persist task outcome on BB per typed status (persistTaskOutcome):
│     success → completed; partial/cancelled → left in_progress (resumable);
│     failed/aborted → failed (resumable)
│
└─ 8. Return HandleResult (carries Status + Reflections).
      A recordConversationOutcome defer updates conversationHistory for EVERY
      terminal outcome (success, failure, cancel).
```

## Execution Surface

| Surface | When | Behaviour |
| ------- | ---- | --------- |
| Simple task | Conductor never calls `delegate` | One ReAct loop, no subagents. Replaces the former "normal" single-step mode without planner overhead. |
| Complex task | Conductor calls `delegate` with one or more tasks | Subagents run isolated ReAct loops; Conductor sees only summaries. Replaces the former "advanced" multi-step DAG mode. |
| Interactive skill | Conductor calls `ask_user` / `declare_plan` mid-loop | Skill instructions are executable because the tools are available inside the loop. No pipeline-level gate. |
| Goal mode | Any message with `opts.Goal` (fresh task or continuation) | `runGoalLoop` replaces the single route→Conductor pass: derives a {condition, verify} goal with user sign-off, then iterates the Conductor turn-by-turn (each a fresh `Executor.Run`) until the agent declares the goal met, the budget is exhausted, the agent goes idle, or the task is paused (session-level). See [../goal-mode.md](../goal-mode.md). |

There is no `executionMode` toggle. The Conductor chooses its own granularity based on task complexity and its system-prompt guidance.

## Invariants

- Every FIRST user message passes through Route; continuation messages (opts.TaskID != "") with a restored plan AND routing decision take the continuation fast-path and skip the router.
- Routing always produces a valid domain from {"code", "research", "general", "mixed"}.
- Complexity is always in range [1, 5].
- Exactly one Conductor `Executor.Run` instance owns a given task from start to finish.
- **Goal mode is a turn-of-Conductors, not one long-lived executor.** When `opts.Goal`, `runGoalLoop` iterates: each turn launches a fresh `Executor.Run` via `RunConductor`, reusing the normal continuation-trajectory mechanism so dialogue context persists across the turn boundary. Goal mode runs on BOTH a fresh task (`opts.TaskID == ""`) and a continuation (`opts.TaskID != ""`); on a continuation the prior task's blackboard is restored and the agent derives a fresh goal against the inherited facts/history. Routing is decided once at the top of `runGoalLoop` (before derivation) and inherited unchanged by every turn; no turn re-routes. The loop holds the single-flight guard for its whole run; `PauseSession` releases it by stopping the in-flight conductor at the next step boundary (the task is persisted as paused; the goal stays `active`). See [../goal-mode.md](../goal-mode.md).
- The Conductor always has `ask_user`, `declare_plan`, `execute_plan`, `reflect`, `delegate`, `cancel_delegation`, `finish` available (they are `system`-group tools — bypass policy; ADR-024).
- `finish` with pending async delegations requires either a prior `cancel_delegation` for each, or an implicit join (the Conductor waits for all pending delegations before finishing).
- `ExecutionResult.Status` is the typed success contract: success | partial | failed | aborted | cancelled. Callers consult it instead of parsing Output.
- Blackboard is created once per first message and restored for continuations.
- Vector search hints are non-blocking (2s timeout, failure is acceptable).
- Skills are activated task-wide and rendered verbatim in the Conductor system prompt (no truncation).
- requestInFlight (atomic.Bool) enforces the "one active request per `*Orchestrator`" invariant: HandleMessage CompareAndSwap's it to true on entry and stores false on defer; a concurrent caller is refused with `ErrRequestInFlight`.
- conversationHistory is updated for every terminal outcome of HandleMessage and Resume.
- When the assistant output contains tool-call syntax printed as text (failure-mode detected by `agent.DetectToolCallSyntaxInContent`), the history records a `HistoryNoteFailed(...)` note instead of the hallucinated text.
- isNoProject: routing domain "code" is overridden to "general" after classification.
- SetNoProjectMode(): disables code tools and adds extended bash command blacklist on the core tool registry.
- Small-LLM essential-tools filter: when active it runs exactly once per task on the non-goal path (before the ReAct loop) and never in goal mode; `finish` and the fact-memory / human-interaction tools always survive. The profile is strictly additive — every variant is inert when its master/sub-toggle is off. See [../small-llm.md](../small-llm.md).

## Configuration

From `config.yaml` (via BuilderConfig → OrchestratorConfig):

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| `orchestration.maxDependencyContextChars` | 8000 | Max chars from dependency outputs injected into a delegation task |
| `orchestration.maxRedelegationDepth` | 2 | Maximum recursive delegation depth when `allow_redelegate` is true (ASI07-R6). 0 = use default (2) |
| `executor.compaction.sliding_window.keep_first` | 3 | Protected head messages during sliding-window compaction |
| `executor.compaction.sliding_window.keep_last` | 10 | Protected tail messages during sliding-window compaction |
| `executor.compaction.thresholds.pre_warning_percent` | 75 | Context-fill % that triggers the pre-compaction store_fact nudge |
| `security.agents_md_max_bytes` | 65536 | Cap on AGENTS.md content injected into prompts (0 = default; -1 = unlimited). Applies to the combined content of all AGENTS.md sources. |

The small-LLM profile (`small_llm.*`) tunes the Conductor for small/local models (tool-set narrowing, system-prompt Lite swap, sampling override, loop hardening). It is strictly additive and defaults to off. See [../small-llm.md](../small-llm.md).

Not wired from `config.yaml` (hardcoded defaults in code):
- `OrchestratorConfig.ConductorHistoryWindow` — default 20 (set in `NewOrchestrator`; not exposed as a config key).
- `OrchestratorConfig.AgentsMDSearchPaths` — resolved by the backend (`backend/configadapter.go`) from the user home directory. AGENTS.md is read from multiple sources and concatenated in priority order: (1) `~/.agents/AGENTS.md` (global, shared across all agents), (2) `~/.c0wrk/.agents/AGENTS.md` (c0wrk-specific), (3) `<workspace>/AGENTS.md` (project-specific). Missing files are silently skipped. The combined content is capped by `security.agents_md_max_bytes`. This applies in both CHAT (No Project) and CODE modes — in CHAT mode only the global and c0wrk files are read (no workspace).

Note: yaml key casing is mixed within and across config sections (see the struct tags in `backend/config/config.go`). `orchestration.*` uses `camelCase` (`maxDependencyContextChars`). `executor.compaction.sliding_window.*` uses `snake_case` (`keep_first`/`keep_last`), while sibling `executor.*` subsections use `camelCase` (`toolOutputPruning`, `historyMutation`, `circuitBreaker`). There is no top-level `conductor:` config section — `ConductorHistoryWindow` is not user-tunable via config (`MaxRedelegationDepth` is exposed as `orchestration.maxRedelegationDepth`).

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
