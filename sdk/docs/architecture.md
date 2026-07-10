# Architecture

This document describes the layered design of the SDK, how the packages relate, how data flows through a task, and the core execution patterns.

## Layered design

The SDK is organized in four layers. Import direction flows downward — upper layers depend on lower layers, never the reverse.

```
┌─────────────────────────────────────────────────────────────┐
│  sdk (root)         Framework, Config, New, Execute          │
│                     Entry point; owns shared infrastructure  │
├─────────────────────────────────────────────────────────────┤
│  orchestration      Conductor, Planner, Reflector            │
│                     Multi-step task coordination             │
├─────────────────────────────────────────────────────────────┤
│  agent              Executor, ReAct loop, RunSubAgent        │
│                     Single-step reasoning & tool use         │
├─────────────────────────────────────────────────────────────┤
│  llm                Router, Providers, ModelRegistry         │
│  tools              ToolRegistry, builtins, mcp              │
│                     Inference and tool execution primitives  │
└─────────────────────────────────────────────────────────────┘
```

| Layer | Package(s) | Responsibility |
| --- | --- | --- |
| **Entry point** | `sp4rk` | The `Framework` owns shared infrastructure (LLM router, tool registry, MCP gateway, tool cache) and creates per-session orchestrators. `Execute` and `NewConductor` are the two ways to run a task. |
| **Orchestration** | `orchestration`, `planner`, `agent/reflector`, `agent/router` | Coordinates multi-step tasks. The `Conductor` runs a single task end-to-end. The `Planner` generates a DAG of steps. The `Reflector` analyzes failures. The `Router` classifies requests by domain and complexity. |
| **Agent** | `agent` | The `Executor` runs the ReAct loop: think, call a tool, observe, repeat. Manages the step budget, circuit breakers, tool-result caching, and sub-agent parallelism. |
| **Primitives** | `llm`, `tools`, `tools/builtins`, `tools/mcp` | The `Router` routes LLM calls to the active provider. The `ToolRegistry` holds and executes tools. Built-in tools cover file, shell, and search operations; the MCP gateway proxies external servers. |

Supporting packages — `memory`, `prompt`, `skills`, `security`, `embedding`, `pathutil`, `strutil` — are consumed across layers as needed.

## Package dependency graph

The arrows show import direction (`A → B` means A imports B).

```
sdk (root)
  ├──→ agent
  ├──→ llm
  ├──→ memory
  ├──→ orchestration
  └──→ tools/mcp

orchestration
  ├──→ agent
  ├──→ llm
  ├──→ skills
  └──→ tools

planner
  ├──→ agent
  ├──→ llm
  ├──→ orchestration
  ├──→ prompt
  ├──→ skills
  └──→ tools

agent
  ├──→ llm
  └──→ tools
agent/reflector
  ├──→ agent
  ├──→ llm
  ├──→ orchestration
  ├──→ prompt
  └──→ tools
agent/router
  ├──→ agent
  ├──→ llm
  ├──→ prompt
  └──→ tools

memory
  ├──→ agent
  ├──→ llm
  ├──→ prompt
  ├──→ security
  └──→ strutil

tools
  ├──→ llm
  ├──→ pathutil
  ├──→ strutil
  └──→ tools/internal/judge_prompts

tools/builtins
  └──→ tools
tools/mcp
  └──→ tools

skills
  ├──→ pathutil
  └──→ tools
```

The `tools` package is a near-leaf dependency — it imports `llm`, `pathutil`, `strutil`, and `tools/internal/judge_prompts` (for the LLM-backed `ToolJudge`). The `llm` package is similarly foundational. This keeps the primitive layers free of higher-level concerns and prevents import cycles.

## The ReAct loop

The `Executor` (in `agent/executor.go`) runs a **Reason → Act → Observe** loop. Each iteration is a `Step`:

```
┌──────────────────────────────────────────────────┐
│  1. Build prompt from ContextManager             │
│  2. Call LLM (via LLMCaller)                     │
│  3. Parse response → Thought + ToolCall(s)       │
│  4. Execute tool(s) (via ToolExecutor)           │
│     └─ HITLHandler intercepts before execution   │
│  5. Record Observation in the Step               │
│  6. AddStep to ContextManager                    │
│  7. Check fill → maybe compact context           │
│  8. Check circuit breakers → maybe nudge/abort   │
└──────────────────────────────────────────────────┘
            │
            ▼
   finish tool called?  ──yes──→  return (Finished=true)
            │ no
            ▼
   step budget left?    ──no──→  HITLHandler.OnStepLimit
            │ yes                      │
            ▼                           ▼
        next iteration           deny → return (Finished=false)
```

A `Step` captures one iteration:

```go
type Step struct {
    Thought          string         // the model's reasoning
    ReasoningContent string         // chain-of-thought from reasoning models
    ReasoningItems   []llm.ReasoningItem // structured reasoning items (Responses API)
    Action           llm.ToolCall   // the tool it chose to call
    Observation      string         // the tool's result
    IsError          bool           // whether the tool returned an error
    IsUntrusted      bool           // whether the observation is external data
    TokensUsed       int            // token consumption for this step
    UserNudge        string         // optional injected user message (e.g. nudges)
    ResponseGroup    int64          // links steps from the same multi-tool-call response
    CacheHash        string         // hash for tool-result cache retrieval
}
```

The loop terminates in one of three ways:

1. **Finish** — the agent calls the `finish` tool. `ExecutorResult.Finished` is `true`; `ExecutionResult.Status` is `"success"`.
2. **Budget exhausted** — `MaxSteps` iterations complete without a finish call. The `HITLHandler.OnStepLimit` hook decides whether to grant more steps or stop. If denied, `Finished` is `false` and `Status` is `"partial"`.
3. **Circuit breaker** — consecutive identical calls, parse errors, truncation, or fruitless results trip an abort threshold, stopping the loop early.

## Plan & Execute pattern

For complex tasks, the SDK composes three orchestration primitives:

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

1. **Planner** — `Planner.Plan` generates a `Plan`: a DAG of `PlanStep` values, each with a summary, description, dependencies (`DependsOn`), a parallelism hint, estimated tools, and an optional `AgentProfile` (role, allowed tools, domain, step budget).
2. **Conductor** — for each step, the `Conductor` creates an `Executor` and runs a ReAct loop. Steps with no unmet dependencies can run in parallel via `RunSubAgent` goroutines. Results are written to the `Blackboard`.
3. **Reflector** — when a step fails, `Reflector.Reflect` examines the execution trajectory and prior reflections, then returns a `Reflection` with a root cause, hypotheses, and a suggested action: `"retry"`, `"replan"`, or `"abort"`.
4. **Retry/replan** — the orchestrator consults the reflection. A retry re-runs the step (up to `ExecutionConfig.MaxRetries`). A replan asks the `Planner` to generate a new plan accounting for completed steps and the failure. An abort stops execution with `Status == "aborted"`.

## Request classification (agent/router)

The `agent/router` package provides a `Router` that classifies user requests before planning. It uses a single LLM call to determine:

- **Domain** — `"code"` (source modification, build/test), `"research"` (information gathering), `"general"` (default), or `"mixed"`.
- **Complexity** — an integer from 1 (trivial) to 5 (highly complex).
- **Matched skills** — skill names selected from the available skill pool.
- **Needs clarification** — whether the request is ambiguous and requires user input before proceeding.

The routing decision drives two downstream behaviours:

1. **Compaction strategy** — the host application maps the domain/complexity pair to a context compaction strategy name:

   | Domain | Complexity | Strategy |
   | --- | --- | --- |
   | `code` | any | `sliding_window` |
   | `research` | any | `summarization` |
   | `general` / `mixed` | < 4 | `sliding_window` |
   | `general` / `mixed` | ≥ 4 | `hierarchical` |

2. **Planner mode** — the `Planner` reads domain and complexity from the context (via `Config.DomainFromContext` / `Config.ComplexityFromContext`) to decide between direct planning (no exploration) and informed-exploration planning (the planner runs a short ReAct loop to investigate the codebase before generating the plan).

```go
import (
    "github.com/v0lka/sp4rk/agent/router"
    "github.com/v0lka/sp4rk/llm"
)

rt := router.New(llmCaller, router.Config{
    SystemPrompt:  "Classify the user's request by domain and complexity. ...",
    HistoryWindow: 10, // number of recent messages to include
})

decision, err := rt.Route(ctx, userMessage, availableTools, history, skillDescriptors)
// decision.Domain, decision.Complexity, decision.MatchedSkills, decision.NeedsClarification
// The host maps decision.Domain/decision.Complexity to a compaction strategy
// ("sliding_window" | "summarization" | "hierarchical") per the table above.
```

## Key interfaces

The SDK is built around small, focused interfaces that let you swap implementations.

| Interface | Package | Purpose |
| --- | --- | --- |
| `LLMCaller` | `agent` | What the `Executor` needs from the LLM layer: `Call(ctx, ChatRequest) (*ChatResponse, error)`. The `llm.Router` satisfies it. |
| `ToolExecutor` | `agent` | What the `Executor` needs from the tools layer: `Execute`, `GetToolSource`, `IsToolUntrusted`. The `tools.ToolRegistry` satisfies it. |
| `ContextManager` | `agent` | Manages the LLM context window: `BuildPrompt`, `AddStep`, `Compact`, `CheckFill`, `FillPercent`, `AvailableTokens`. The `memory.ContextWindow` satisfies it. |
| `CompactionStrategy` | `agent` | Compresses step history: `Compact(ctx, steps, budgetTokens) []llm.Message`. Implementations: sliding, summary, hierarchical. |
| `HITLHandler` | `agent` | Human-in-the-loop hooks: `OnToolCall` (confirm/modify/reject) and `OnStepLimit` (grant or deny more steps). |
| `Events` | `agent` | Streams lifecycle events: `StepStart`, `Thought`, `ToolCall`, `ToolResult`, `AssistantChunk`, `ContextFill`, `ContextCompaction`, `SubAgentLaunch`, `Finishing`, etc. `NoopEvents` is the no-op default. |
| `Blackboard` | `orchestration` | Structured access to shared task state: plan, step results, reflections, facts, final result. `MapBlackboard` is the in-memory implementation. |
| `Planner` | `orchestration` | Generates and regenerates plans: `Plan`, `Replan`, `PlanContinuation`. |
| `Reflector` | `orchestration` | Analyzes failures: `Reflect(ctx, trajectory, plan, prevReflections) (*Reflection, error)`. |
| `Checkpointer` | `orchestration` | Persists blackboard state: `SaveCheckpoint`, `LoadCheckpoint`, `DeleteCheckpoint`. |

## Data flow

A single task flows through the layers as follows:

```
user message
    │
    ▼
Framework.Execute(ctx, systemPrompt, events, userMessage)
    │  creates a Conductor and a MapBlackboard
    ▼
Conductor.Run(ctx, message, blackboard, tools, events, strategy)
    │  resolves model metadata, builds the system prompt,
    │  creates a ContextManager, creates an Executor
    ▼
Executor.Run(ctx, availableTools, contextManager)
    │  ┌─── ReAct loop ───────────────────────────┐
    │  │  ContextManager.BuildPrompt()             │
    │  │  LLMCaller.Call()  →  Router → Provider   │
    │  │  parse response → Thought + ToolCall      │
    │  │  HITLHandler.OnToolCall() (if configured) │
    │  │  ToolExecutor.Execute()  →  ToolRegistry  │
    │  │  ContextManager.AddStep()                 │
    │  │  ContextManager.CheckFill() → Compact?    │
    │  └───────────────────────────────────────────┘
    │  returns ExecutorResult{Output, Steps, Finished}
    ▼
ExecutionResult{Output, Blackboard, Status, Reflections}
```

`Framework.Execute` is the convenience path. For repeated use, `Framework.NewConductor` creates a reusable conductor wired with the framework's shared infrastructure; you then call `Conductor.Run` directly with your own `Blackboard`.

## The Blackboard pattern

The `Blackboard` is a thread-safe shared-state container that holds everything an orchestration run accumulates:

| State | Method | Description |
| --- | --- | --- |
| Original request | `GetOriginalRequest` / `SetOriginalRequest` | The user's initial task. |
| Plan | `GetPlan` / `SetPlan` | The DAG of steps. |
| Step results | `GetStepResult` / `SetStepResult` | Per-step output, summary, error, and trajectory. |
| Reflections | `GetReflections` / `AddReflection` | Failure analyses from the reflector. |
| Facts | `GetFacts` / `StoreFact` / `SearchFacts` | Keyword-tagged facts for inter-step communication. |
| Final result | `GetFinalResult` / `SetFinalResult` | The terminal outcome. |

Steps communicate through the blackboard rather than direct references. A step can `StoreFact` with keywords, and a later step can `SearchFacts` to retrieve it. The `OnBlackboardChanged` callback on `Config` notifies the host application after every successful write, enabling live UI updates.

`MapBlackboard` is the default in-memory implementation. For persistence, provide a `Checkpointer` and use `RestoreBlackboard` to reload state across restarts.

## Concurrency model

| Component | Thread-safe? | Notes |
| --- | --- | --- |
| `llm.Router` | **Yes** | Protected by a `sync.RWMutex`. `SetModel` takes a write lock; `Call` snapshots the active provider and model under a read lock, then releases it before the retry loop so `SetModel` is not blocked by backoff sleeps. Safe to share across goroutines. |
| `tools.ToolRegistry` | **Yes** | Protected by a `sync.RWMutex`. Register, unregister, get, list, and execute are safe for concurrent use. |
| `orchestration.MapBlackboard` | **Yes** | Protected by a `sync.RWMutex`. All read and write methods are safe for concurrent use. |
| `embedding.Embedder` | **Yes** | Protected by a `sync.Mutex`. Safe for concurrent embedding calls. |
| `skills.SkillManager` | **Yes** | Protected by a `sync.RWMutex`. Scan and lookups are safe for concurrent use. |
| `agent.Executor` | **No** | An executor owns a single ReAct loop. For parallel steps, create separate executors and run them via `RunSubAgent` goroutines. |

The `Conductor` runs one task at a time. Parallelism within a plan is achieved by launching multiple subagent goroutines (`RunSubAgent`), each with its own `Executor` and `ContextManager`, writing results back to the shared `Blackboard`.

## Next steps

- [agent-executor.md](agent-executor.md) dives into the `Executor` and circuit breakers.
- [orchestration.md](orchestration.md) covers the `Conductor` and blackboard in depth.
- [planner.md](planner.md) and [reflector.md](reflector.md) detail plan generation and failure analysis.
- [memory.md](memory.md) explains context-window management and compaction.
