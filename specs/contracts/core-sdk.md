# Contract: Core <-> SDK

> **Status**: Boundary rule superseded by [ADR-008](../decisions/008-backend-sdk-direct-import.md). The interface tables below remain valid — `core/`, `backend/`, and `desktop/` all consume these SDK types directly (no aliases).

## Boundary Rule

`core/`, `backend/`, and `desktop/` all import `sdk/` packages directly. No convenience re-export layers exist. See ADR-008.

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
| `Step`                 | sdk/agent | core/types (direct)              | Single ReAct iteration (incl. IsUntrusted flag) |
| `ExecutorResult`       | sdk/agent | core/types (direct)              | Executor output                       |
| `CircuitBreakerConfig` | sdk/agent | core/orchestrator                | Circuit breaker thresholds            |
| `ToolResultBudget`     | sdk/agent | core/orchestrator                | Tool result truncation (Stage 2)     |
| `ToolResultCache`      | sdk/agent | core/orchestrator, core/builder   | Full result cache keyed by SHA256    |
| `ToolTruncationConfig` | sdk/agent | core/orchestrator config          | Per-tool Stage 1 truncation limits   |
| `ToolCacheMeta`        | sdk/agent | core/executor                     | Cache entry metadata (paths, mtime)  |
| `TodoItem`             | sdk/agent | core/types (adapter)             | Step todo item                        |

### Consumed from `sdk/orchestration`

| Interface / Type   | Package           | Consumed By                                   | Purpose                    |
| ------------------ | ----------------- | --------------------------------------------- | -------------------------- |
| `Planner`          | sdk/orchestration | core (adapted via plannerSDKAdapter)          | Plan generation interface  |
| `Reflector`        | sdk/orchestration | core/reflector                                | Failure analysis interface |
| `Events`           | sdk/orchestration | core/types (adapted via emitterEventsAdapter) | Orchestration lifecycle    |
| `Blackboard`       | sdk/orchestration | core/types (direct)                            | Shared task state          |
| `Orchestrator`     | sdk/orchestration | core/orchestrator (as `engine`)               | DAG execution engine       |
| `Config`           | sdk/orchestration | core/orchestrator (NewOrchestrator)           | Engine configuration (incl. ToolCache, PerToolTruncation, StepDumpTracker) |
| `Plan`, `PlanStep` | sdk/orchestration | core/types (direct use)                        | Plan data structures       |
| `CompletedStep`    | sdk/orchestration | core/types (direct)                            | Step result record         |
| `Reflection`       | sdk/orchestration | core/types (direct)                            | Reflector output           |
| `PruningOverride`  | sdk/orchestration | core/stepconfig                               | Per-step pruning config    |
| `StepDumpTracker`  | sdk/orchestration | core/orchestrator, core/builder, backend/session | Per-step LLM dump file manager |

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
| `ReasoningEffort` | sdk/llm | core/orchestrator config            | Reasoning level (plain `string`, not a custom type) |
| `ModelMetadata`   | sdk/llm | core/stepconfig, systemprompt       | Model capabilities     |

### Consumed from `sdk/tools`

| Interface / Type       | Package   | Consumed By                    | Purpose                                        |
| ---------------------- | --------- | ------------------------------ | ---------------------------------------------- |
| `Tool`                 | sdk/tools | core/tools (embedded registry) | Tool interface (incl. IsUntrusted())          |
| `BaseTool`             | sdk/tools | core/tools/builtins, MCP tools | Base impl with Untrusted field               |
| `IsUntrustedTool()`    | sdk/tools | REMOVED                        | Replaced by ToolExecutor.IsToolUntrusted() — delegates to Tool.IsUntrusted() + MCP source check |
| `ToolRegistry`         | sdk/tools | core/tools (embedded)          | Basic tool store                |
| `ListFiltered`         | sdk/tools | core/orchestrator              | Filtered tool listing (e.g., exclude disabled tools) |
| `ToolDescriptor`       | sdk/tools | core/orchestrator, planner     | Tool metadata                                 |
| `ToolPolicy`           | sdk/tools | core/tools                     | Policy enum                                   |
| `ToolResult`           | sdk/tools | core/tools                     | Execution result                              |
| `ConfirmFunc`          | sdk/tools | core/tools, backend, desktop   | User confirmation callback                    |
| `ConfirmationRequest`  | sdk/tools | core/tools, backend, desktop   | Confirmation request payload                  |
| `ConfirmationResponse` | sdk/tools | core/tools, backend, desktop   | Confirmation response enum (`ConfirmAllowOnce`, `ConfirmDeny`, `ConfirmDenyAndStop`) |
| `ToolJudger`           | sdk/tools | core/tools, core/tools/mcp     | Tool self-judging interface                   |
| `AutoInjectedParamProject` | sdk/tools | core/tools/mcp (schema sanitizer) | Constant for auto-injected parameter name   |
| `FileCoherenceChecker` | sdk/tools | core/tools (re-exported)       | Cross-session file conflict detection          |
| `FileSig`              | sdk/tools | core/tools (re-exported)       | File signature (mtime + size) for coherence    |
| `CoherenceConflict`    | sdk/tools | core/tools (re-exported)       | Conflict record with session and timing detail |

### Consumed from `sdk/proxy`

| Interface / Type | Package    | Consumed By                                      | Purpose                              |
| ---------------- | ---------- | ------------------------------------------------ | ------------------------------------ |
| `Config`         | sdk/proxy  | core/builderconfig, backend/configadapter        | HTTP proxy settings (URL, bypass, TLS certs, env vars) |
| `BuildTransport` | sdk/proxy  | core/builder                                     | Build proxy-configured `*http.Transport`     |
| `BuildClient`    | sdk/proxy  | core/builder                                     | Build proxy-configured `*http.Client`         |
| `SetEnvVars`     | sdk/proxy  | core/builder (on SetGlobalEnv opt-in)            | Export `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`/`SSL_CERT_DIR` for child processes |
| `ClearEnvVars`   | sdk/proxy  | core/builder (on proxy disable)                  | Remove proxy environment variables            |
| `MaskURL`        | sdk/proxy  | backend/frontend_api_config                      | Mask proxy URL password for UI display        |

### Consumed from `sdk/memory`

| Interface / Type | Package    | Consumed By                      | Purpose                        |
| ---------------- | ---------- | -------------------------------- | ------------------------------ |
| `ContextWindow`  | sdk/memory | core (via ContextManagerFactory) | Token tracking + compaction    |
| Strategy impls   | sdk/memory | core/builder (wired in factory)  | Sliding, summary, hierarchical |

## Imports Pattern

All layers above sdk import source packages directly, e.g.:

```go
// desktop/startup_phases.go
import "github.com/v0lka/c0wrk/sdk/tools"  // → sdktools.ConfirmFunc, sdktools.AskUserFunc, ...
import "github.com/v0lka/c0wrk/sdk/agent"   // → agent.StepLimitFunc, agent.StepLimitDeny, ...

// backend/application.go
import "github.com/v0lka/c0wrk/sdk/tools"      // → sdktools.ConfirmFunc, sdktools.EnvInfo, ...
import "github.com/v0lka/c0wrk/core/tools/mcp"  // → mcp.ServerStatus
import "github.com/v0lka/c0wrk/sdk/agent"       // → agent.StepLimitFunc
import "github.com/v0lka/c0wrk/sdk/proxy"       // → proxy.MaskURL, proxy.Config
```

## Initialization

`core/builder.go` → `NewOrchestratorBuilder`:

1. Creates `sdk/tools.ToolRegistry`
2. Registers built-in tools from `sdk/tools/builtins`
3. Starts MCP gateway (async)
4. Creates `ToolResultCache` with TTL and passes per-tool truncation to orchestrator config
5. Creates `sdk/llm.Router` with providers (async)
6. `Build()` creates per-session `Orchestrator` which internally creates `sdk/orchestration.Orchestrator`

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
| Tool cache store        | sdk → sdk | `ToolResultCache.Store(name, content, meta)` — SHA256 key |
| Tool cache lookup       | sdk ← sdk | `tool_result_read(hash, start_line, num_lines)` — fragment |
| File coherence state    | sdk ↔ core | `FileCoherenceChecker` (injected via context)       |
| Compaction trigger      | core → sdk | `CompactionStrategy.Compact()`                      |
| Compacted messages      | sdk → core | `[]llm.Message`                                     |
| Plan generation request | core → sdk | `Planner.Plan()` via adapter                        |
| Plan structure          | sdk → core | `orchestration.Plan` (direct)                       |
| Orchestration lifecycle | core → sdk | `orchestration.Events` (adapted Emitter)            |
| Blackboard state        | sdk ↔ core | `orchestration.Blackboard` (direct, shared)         |
| Context window status   | sdk → core | `ContextManager.State()`                            |
| CompactionResult        | sdk → core | `CompactionResult{BeforeFill, AfterFill, Strategy}` |

## Error Propagation

- SDK errors bubble up through core as-is (no re-wrapping at this boundary)
- Core adds context when the error's origin is ambiguous: `fmt.Errorf("routing failed: %w", err)`
- SDK never returns core-specific error types

## Breaking Change Checklist

- If you modify an `sdk/orchestration` interface → update adapter in `core/orchestrator.go`
- If you add a field to `sdk/orchestration.Config` → update `NewOrchestrator` in `core/orchestrator.go`
- If you change `CallerForStep` signature (sdk/orchestration or sdk/planner) → update all closures in `core/orchestrator.go` and `core/builder.go`, plus all call sites in `sdk/orchestration/orchestrator.go` and `sdk/planner/planner.go`
- If you modify `sdk/agent.AgentEvents` → update `core.Emitter` interface AND `noopEmitter`
- If you change `sdk/tools.Tool` interface → update `core/tools/mcp/mcptool.go`, ALL builtins, AND all test mocks implementing `Tool`
- If you add a new sdk type that backend or desktop needs → import directly from the source package
- If you modify `sdk/tools.FileCoherenceChecker` → update `backend/session/file_coherence.go` implementation
- If you change `IsUntrusted()` semantics → update ALL built-in tool registrations (`sdk/tools/builtins/*.go`) AND `sdk/security/wrap.go`
