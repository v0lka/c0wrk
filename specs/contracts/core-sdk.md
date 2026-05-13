# Contract: Core <-> SDK

## Boundary Rule

Only `core/` imports `sdk/`. No other layer (backend, desktop) may import sdk packages directly. Backend accesses sdk types through type aliases defined in `core/types.go`.

## Interfaces

### Consumed from `sdk/agent`

| Interface / Type       | Package   | Consumed By                      | Purpose                               |
| ---------------------- | --------- | -------------------------------- | ------------------------------------- |
| `LLMCaller`            | sdk/agent | core/orchestrator, core/planner  | LLM call interface                    |
| `ToolExecutor`         | sdk/agent | core (via sdk/orchestration)     | Tool execution interface              |
| `CompactionStrategy`   | sdk/agent | core/stepconfig                  | Memory compaction                     |
| `AgentEvents`          | sdk/agent | core/types (embedded in Emitter) | Lifecycle event hooks                 |
| `ContextManager`       | sdk/agent | core/types (extended)            | Context window management             |
| `StepLimitFunc`        | sdk/agent | core/orchestrator config         | Step limit / circuit breaker callback |
| `Step`                 | sdk/agent | core/types (alias)               | Single ReAct iteration                |
| `ExecutorResult`       | sdk/agent | core/types (alias)               | Executor output                       |
| `CircuitBreakerConfig` | sdk/agent | core/orchestrator                | Circuit breaker thresholds            |
| `ToolResultBudget`     | sdk/agent | core/orchestrator                | Tool result truncation                |
| `TodoItem`             | sdk/agent | core/types (adapter)             | Step todo item                        |

### Consumed from `sdk/orchestration`

| Interface / Type   | Package           | Consumed By                                   | Purpose                    |
| ------------------ | ----------------- | --------------------------------------------- | -------------------------- |
| `Planner`          | sdk/orchestration | core (adapted via plannerSDKAdapter)          | Plan generation interface  |
| `Reflector`        | sdk/orchestration | core/reflector                                | Failure analysis interface |
| `Events`           | sdk/orchestration | core/types (adapted via emitterEventsAdapter) | Orchestration lifecycle    |
| `Blackboard`       | sdk/orchestration | core/types (alias)                            | Shared task state          |
| `Orchestrator`     | sdk/orchestration | core/orchestrator (as `engine`)               | DAG execution engine       |
| `Config`           | sdk/orchestration | core/orchestrator (NewOrchestrator)           | Engine configuration       |
| `Plan`, `PlanStep` | sdk/orchestration | core/types (aliases)                          | Plan data structures       |
| `CompletedStep`    | sdk/orchestration | core/types (alias)                            | Step result record         |
| `Reflection`       | sdk/orchestration | core/types (alias)                            | Reflector output           |
| `PruningOverride`  | sdk/orchestration | core/stepconfig                               | Per-step pruning config    |

### Consumed from `sdk/llm`

| Interface / Type  | Package | Consumed By                         | Purpose                |
| ----------------- | ------- | ----------------------------------- | ---------------------- |
| `Provider`        | sdk/llm | core/builder (via Router)           | LLM provider interface |
| `Router`          | sdk/llm | core/builder                        | Multi-provider routing |
| `ModelRegistry`   | sdk/llm | core/builder, orchestrator, planner | Model metadata         |
| `TokenCounter`    | sdk/llm | core/builder (passed to SDK)        | Token counting         |
| `TrackingCaller`  | sdk/llm | core/orchestrator                   | Usage tracking wrapper |
| `Message`         | sdk/llm | core/router, planner, orchestrator  | LLM message type       |
| `ChatRequest`     | sdk/llm | core/router, planner                | LLM request            |
| `ReasoningEffort` | sdk/llm | core/orchestrator config            | Reasoning level        |
| `ModelMetadata`   | sdk/llm | core/stepconfig, systemprompt       | Model capabilities     |

### Consumed from `sdk/tools`

| Interface / Type | Package   | Consumed By                    | Purpose          |
| ---------------- | --------- | ------------------------------ | ---------------- |
| `Tool`           | sdk/tools | core/tools (embedded registry) | Tool interface   |
| `ToolRegistry`   | sdk/tools | core/tools (embedded)          | Basic tool store |
| `ToolDescriptor` | sdk/tools | core/orchestrator, planner     | Tool metadata    |
| `ToolPolicy`     | sdk/tools | core/tools                     | Policy enum      |
| `ToolResult`     | sdk/tools | core/tools                     | Execution result |

### Consumed from `sdk/memory`

| Interface / Type | Package    | Consumed By                      | Purpose                        |
| ---------------- | ---------- | -------------------------------- | ------------------------------ |
| `ContextWindow`  | sdk/memory | core (via ContextManagerFactory) | Token tracking + compaction    |
| Strategy impls   | sdk/memory | core/builder (wired in factory)  | Sliding, summary, hierarchical |

## Type Aliasing Bridge

`core/types.go` re-exports sdk types so backend can reference them:

```go
// core/types.go
type Step = agent.Step
type Plan = orchestration.Plan
type Blackboard = orchestration.Blackboard
type Reflection = orchestration.Reflection
// ... ~20 more aliases
```

Backend imports: `"github.com/user/agent/core"` → `core.Step`, `core.Plan`, etc.

## Initialization

`core/builder.go` → `NewOrchestratorBuilder`:

1. Creates `sdk/tools.ToolRegistry`
2. Registers built-in tools from `sdk/tools/builtins`
3. Starts MCP gateway (async)
4. Creates `sdk/llm.Router` with providers (async)
5. `Build()` creates per-session `Orchestrator` which internally creates `sdk/orchestration.Orchestrator`

## Adapter Pattern

Core uses adapters to bridge its interfaces with SDK interfaces:

- `emitterEventsAdapter` — wraps `core.Emitter` to implement `orchestration.Events`
- `plannerSDKAdapter` — wraps `core.Planner` to implement `orchestration.Planner` (adds skill threading)
- `ContextManagerFactory` closure — wraps `core.ContextManager` creation to return `agent.ContextManager`

## Data Flow Across Boundary

| Data                    | Direction  | Form                                                |
| ----------------------- | ---------- | --------------------------------------------------- |
| LLM request             | core → sdk | `llm.ChatRequest` via `Router.Call()`               |
| LLM response            | sdk → core | `llm.ChatResponse`                                  |
| Tool execution request  | core → sdk | `tools.Tool.Execute()`                              |
| Tool result             | sdk → core | `tools.ToolResult`                                  |
| Compaction trigger      | core → sdk | `CompactionStrategy.Compact()`                      |
| Compacted messages      | sdk → core | `[]llm.Message`                                     |
| Plan generation request | core → sdk | `Planner.Plan()` via adapter                        |
| Plan structure          | sdk → core | `orchestration.Plan` (aliased)                      |
| Orchestration lifecycle | core → sdk | `orchestration.Events` (adapted Emitter)            |
| Blackboard state        | sdk ↔ core | `orchestration.Blackboard` (aliased, shared)        |
| Context window status   | sdk → core | `ContextManager.State()`                            |
| CompactionResult        | sdk → core | `CompactionResult{BeforeFill, AfterFill, Strategy}` |

## Error Propagation

- SDK errors bubble up through core as-is (no re-wrapping at this boundary)
- Core adds context when the error's origin is ambiguous: `fmt.Errorf("routing failed: %w", err)`
- SDK never returns core-specific error types

## Breaking Change Checklist

- If you modify an `sdk/orchestration` interface → update `core/types.go` alias AND adapter in `core/orchestrator.go`
- If you add a field to `sdk/orchestration.Config` → update `NewOrchestrator` in `core/orchestrator.go`
- If you modify `sdk/agent.AgentEvents` → update `core.Emitter` interface AND `noopEmitter`
- If you change `sdk/tools.Tool` interface → update `core/tools/mcp/mcptool.go` AND all builtins
- If you add a new sdk type that backend needs → add alias in `core/types.go`
