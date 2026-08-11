# Contract: Core <-> sp4rk

> **Status**: Boundary rule superseded by [ADR-008](../decisions/008-backend-sp4rk-direct-import.md). The interface tables below remain valid — `core/`, `backend/`, and `desktop/` all consume these sp4rk types directly (no aliases).

## Boundary Rule

`core/`, `backend/`, and `desktop/` all import sp4rk packages directly. No convenience re-export layers exist. See ADR-008.

sp4rk (module `github.com/v0lka/sp4rk`) lives in its [own repository](https://github.com/v0lka/sp4rk) and is a separate Go module (per ADR-015). The root module (`github.com/v0lka/c0wrk`) depends on it as a normal external dependency (`require github.com/v0lka/sp4rk`, no `replace` directive). The module boundary provides compile-time enforcement of the import prohibition: the sp4rk module cannot import `core/`, `backend/`, or `desktop/`. External consumers can import `github.com/v0lka/sp4rk` independently.

## Interfaces

### Consumed from `github.com/v0lka/sp4rk/agent`

| Interface / Type       | Package                          | Consumed By                                  | Purpose                               |
| ---------------------- | -------------------------------- | -------------------------------------------- | ------------------------------------- |
| `LLMCaller`            | github.com/v0lka/sp4rk/agent     | core/orchestrator, github.com/v0lka/sp4rk/planner | LLM call interface                    |
| `ToolExecutor`         | github.com/v0lka/sp4rk/agent     | core (via github.com/v0lka/sp4rk/orchestration) | Tool execution interface              |
| `CompactionStrategy`   | github.com/v0lka/sp4rk/agent     | core/stepconfig                              | Memory compaction                     |
| `Events`               | github.com/v0lka/sp4rk/agent     | core/types (embedded in Emitter)             | Lifecycle event hooks                 |
| `ContextManager`       | github.com/v0lka/sp4rk/agent     | core/types (extended)                        | Context window management             |
| `HITLHandler`          | github.com/v0lka/sp4rk/agent     | core/orchestrator config                     | Human-in-the-loop handler (OnStepLimit + OnToolCall) |
| `Step`                 | github.com/v0lka/sp4rk/agent     | core/types (direct)                          | Single ReAct iteration (incl. IsUntrusted flag) |
| `ExecutorResult`       | github.com/v0lka/sp4rk/agent     | core/types (direct)                          | Executor output                       |
| `CircuitBreakerConfig` | github.com/v0lka/sp4rk/agent     | core/orchestrator                            | Circuit breaker thresholds            |
| `ToolResultBudget`     | github.com/v0lka/sp4rk/agent     | core/orchestrator                            | Tool result truncation (Stage 2)     |
| `ToolResultCache`      | github.com/v0lka/sp4rk/agent     | core/orchestrator, core/builder              | Full result cache keyed by short hash |
| `ToolTruncationConfig` | github.com/v0lka/sp4rk/agent     | core/orchestrator config                     | Per-tool Stage 1 truncation limits   |
| `ToolCacheMeta`        | github.com/v0lka/sp4rk/agent     | github.com/v0lka/sp4rk/agent (executor)      | Cache entry metadata (paths, mtime)  |
| `TodoItem`             | github.com/v0lka/sp4rk/agent     | core/types (adapter)                         | Step todo item                        |
| `Executor.AddNonCacheableTools` | github.com/v0lka/sp4rk/agent | core/conductor                          | Extends the non-cacheable tool set with consumer-specific meta-tools (delegate, declare_plan, reflect, etc.) |
| `WithResumeSteps` (Option) | github.com/v0lka/sp4rk/agent     | core/orchestrator (Resume)                   | Seeds prior ReAct steps into the Executor so the step counter resumes from `len(steps)+1` and the full trajectory syncs to the TrajectoryStore |
| `Step.UserNudge`           | github.com/v0lka/sp4rk/agent     | core/session (tryContinueInterruptedTask)    | User message appended to a resumed trajectory; rendered as a `{role:user}` turn |
| `ConductorConfig.NonCacheableTools` | github.com/v0lka/sp4rk/orchestration | core/conductor                     | Lists consumer-specific non-cacheable tool names passed to the sp4rk Conductor's executor |

### Consumed from `github.com/v0lka/sp4rk/orchestration`

| Interface / Type   | Package                              | Consumed By                                   | Purpose                    |
| ------------------ | ------------------------------------ | --------------------------------------------- | -------------------------- |
| `Reflector`        | github.com/v0lka/sp4rk/orchestration | core (via github.com/v0lka/sp4rk/orchestration engine) | Failure analysis interface |
| `Blackboard`       | github.com/v0lka/sp4rk/orchestration | core/types (direct)                           | Shared task state          |
| `Conductor`       | github.com/v0lka/sp4rk/orchestration | core/conductor (`RunConductor`)               | DAG execution engine (built via `orchestration.NewConductor(cfg ConductorConfig)`) |
| `StepSeedable`     | github.com/v0lka/sp4rk/orchestration | core/conductor                                | Optional `ContextManager` capability (`SeedSteps`) used to resume an executor from a checkpoint |
| `ConductorConfig.ResumeSteps` | github.com/v0lka/sp4rk/orchestration | core/orchestrator (`runConductor`)    | Prior ReAct steps seeded into the ContextManager + Executor on resume |
| `ConductorConfig`  | github.com/v0lka/sp4rk/orchestration | core/conductor (`RunConductor`)           | Engine configuration (incl. `ToolCache`, `PerToolTruncation`, `NonCacheableTools`, `ResumeSteps`) |
| `Plan`, `PlanStep` | github.com/v0lka/sp4rk/orchestration | core/types (direct use)                       | Plan data structures       |
| `CompletedStep`    | github.com/v0lka/sp4rk/orchestration | core/types (direct)                           | Step result record         |
| `Reflection`       | github.com/v0lka/sp4rk/orchestration | core/types (direct)                           | Reflector output           |
| `PruningOverride`  | github.com/v0lka/sp4rk/orchestration | core/stepconfig                               | Per-step pruning config    |
| `StepDumpTracker`  | github.com/v0lka/sp4rk/orchestration | core/orchestrator, core/builder, backend/session | Per-step LLM dump file manager |

### Consumed from `github.com/v0lka/sp4rk/llm`

| Interface / Type  | Package                       | Consumed By                         | Purpose                |
| ----------------- | ----------------------------- | ----------------------------------- | ---------------------- |
| `Provider`        | github.com/v0lka/sp4rk/llm    | core/builder (via Router)           | LLM provider interface |
| `Router`          | github.com/v0lka/sp4rk/llm    | core/builder                        | Multi-provider routing |
| `ModelRegistry`   | github.com/v0lka/sp4rk/llm    | core/builder, orchestrator, planner | Model metadata         |
| `TokenCounter`    | github.com/v0lka/sp4rk/llm    | core/builder (passed to sp4rk)      | Token counting         |
| `TrackingCaller`  | github.com/v0lka/sp4rk/llm    | core/orchestrator                   | Usage tracking wrapper |
| `Message`         | github.com/v0lka/sp4rk/llm    | core/router, planner, orchestrator  | LLM message type       |
| `ChatRequest`     | github.com/v0lka/sp4rk/llm    | core/router, planner                | LLM request            |
| `ReasoningEffort` | github.com/v0lka/sp4rk/llm    | core/orchestrator config            | Reasoning level (plain `string`, not a custom type) |
| `ModelMetadata`   | github.com/v0lka/sp4rk/llm    | core/stepconfig, systemprompt       | Model capabilities     |

### Consumed from `github.com/v0lka/sp4rk/tools`

| Interface / Type       | Package                        | Consumed By                    | Purpose                                        |
| ---------------------- | ------------------------------ | ------------------------------ | ---------------------------------------------- |
| `Tool`                 | github.com/v0lka/sp4rk/tools   | core/tools (embedded registry) | Tool interface (incl. IsUntrusted())          |
| `BaseTool`             | github.com/v0lka/sp4rk/tools   | core/tools/builtins, MCP tools | Base impl with Untrusted field               |
| `ContentBackedReader`  | github.com/v0lka/sp4rk/tools   | core/tools (`read_file_doc.go`) | Optional interface; `IsContentBacked` opts document-format `read_file` reads into content-backed caching |
| `ToolRegistry`         | github.com/v0lka/sp4rk/tools   | core/tools (embedded)          | Basic tool store                |
| `ListFiltered`         | github.com/v0lka/sp4rk/tools   | core/orchestrator              | Filtered tool listing (e.g., exclude disabled tools) |
| `ToolDescriptor`       | github.com/v0lka/sp4rk/tools   | core/orchestrator, planner     | Tool metadata                                 |
| `ToolPolicy`           | github.com/v0lka/sp4rk/tools   | core/tools                     | Policy enum                                   |
| `ToolResult`           | github.com/v0lka/sp4rk/tools   | core/tools                     | Execution result                              |
| `ConfirmFunc`          | github.com/v0lka/sp4rk/tools   | core/tools, backend, desktop   | User confirmation callback                    |
| `ConfirmationRequest`  | github.com/v0lka/sp4rk/tools   | core/tools, backend, desktop   | Confirmation request payload                  |
| `ConfirmationResponse` | github.com/v0lka/sp4rk/tools   | core/tools, backend, desktop   | Confirmation response enum (`ConfirmAllowOnce`, `ConfirmDeny`, `ConfirmDenyAndStop`) |
| `ToolJudger`           | github.com/v0lka/sp4rk/tools   | core/tools, github.com/v0lka/sp4rk/tools/mcp | Tool self-judging interface                   |
| `StripParamsFromSchema` | github.com/v0lka/sp4rk/tools  | github.com/v0lka/sp4rk/tools/mcp | Strips auto-injected params (e.g. `project`) from MCP tool input schemas |
| `FileCoherenceChecker` | github.com/v0lka/sp4rk/tools   | core/tools (re-exported)       | Cross-session file conflict detection          |
| `FileSig`              | github.com/v0lka/sp4rk/tools   | core/tools (re-exported)       | File signature (mtime + size) for coherence    |
| `CoherenceConflict`    | github.com/v0lka/sp4rk/tools   | core/tools (re-exported)       | Conflict record with session and timing detail |

AskUser types (`AskUserFunc`, `AskUserRequest`, `AskUserResponse`, `AskUserQuestion`, `AskUserAnswer`, `AskUserOption`) moved to `core/tools/` as c0wrk-specific UI types. The `ask_user` tool implementation also lives in `core/tools/askuser.go`.

### Consumed from `core/proxy`

Proxy configuration moved from sp4rk to core/ per architectural extraction (c0wrk-specific infrastructure).

| Interface / Type | Package    | Consumed By                                      | Purpose                              |
| ---------------- | ---------- | ------------------------------------------------ | ------------------------------------ |
| `Config`         | core/proxy | core/builderconfig, backend/configadapter        | HTTP proxy settings (URL, bypass, TLS certs, env vars) |
| `BuildTransport` | core/proxy | core/builder                                     | Build proxy-configured `*http.Transport`     |
| `BuildClient`    | core/proxy | core/builder                                     | Build proxy-configured `*http.Client`         |
| `SetEnvVars`     | core/proxy | core/builder (on SetGlobalEnv opt-in)            | Export `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`/`SSL_CERT_DIR` for child processes |
| `ClearEnvVars`   | core/proxy | core/builder (on proxy disable)                  | Remove proxy environment variables            |
| `MaskURL`        | core/proxy | backend/frontend_api_config                      | Mask proxy URL password for UI display        |

### Consumed from `github.com/v0lka/sp4rk/memory`

| Interface / Type | Package                         | Consumed By                      | Purpose                        |
| ---------------- | ------------------------------- | -------------------------------- | ------------------------------ |
| `ContextWindow`  | github.com/v0lka/sp4rk/memory   | core (via ContextManagerFactory) | Token tracking + compaction (implements `StepSeedable.SeedSteps` for resume)    |
| Strategy impls   | github.com/v0lka/sp4rk/memory   | core/builder (wired in factory)  | Sliding, summary, hierarchical |

## Imports Pattern

All layers above sp4rk import source packages directly, e.g.:

```go
// desktop/startup_phases.go
import "github.com/v0lka/sp4rk/tools"  // → sdktools.ConfirmFunc, ...
import "github.com/v0lka/c0wrk/core/tools" // → coretools.AskUserFunc, coretools.WithNoProject
import "github.com/v0lka/sp4rk/agent"   // → agent.HITLHandler, agent.StepLimitDeny, ...

// backend/configadapter.go
import "github.com/v0lka/c0wrk/core/proxy"      // → proxy.Config, proxy.MaskURL
// backend/frontend_api_config.go
import "github.com/v0lka/c0wrk/core/proxy"      // → proxy.MaskURL (for UI display)
// backend/application.go
import "github.com/v0lka/sp4rk/tools"      // → sdktools.ConfirmFunc, sdktools.EnvInfo, ...
import "github.com/v0lka/sp4rk/tools/mcp"  // → mcp.ServerStatus
import "github.com/v0lka/sp4rk/agent"       // → agent.HITLHandler
import "github.com/v0lka/c0wrk/core/tools"       // → coretools.AskUserFunc
```

## Initialization

`core/builder.go` → `NewOrchestratorBuilder`:

1. Creates a `github.com/v0lka/sp4rk/tools.ToolRegistry` (`tools.NewToolRegistry()`); MCP tool schemas are sanitized later by the gateway via `sdktools.StripParamsFromSchema` (strips auto-injected params such as `project`), and OpenAI-compatible schemas by the provider via `llm.SanitizeSchemaForOpenAI`
2. Registers built-in tools from `github.com/v0lka/sp4rk/tools/builtins`
3. Starts MCP gateway (async)
4. Creates `ToolResultCache` with TTL and passes per-tool truncation to orchestrator config
5. Creates a `github.com/v0lka/sp4rk/llm.Router` with providers (async)
6. `Build()` creates per-session `Orchestrator` which internally builds a `github.com/v0lka/sp4rk/orchestration.Conductor` (via `core/conductor.go` `RunConductor` → `orchestration.NewConductor(cfg)`)

## Adapter Pattern

Core bridges c0wrk-specific configuration into sp4rk engine components via small factory/adapter functions:

- `newCoreRouter` (`core/router_adapter.go`) — builds a `github.com/v0lka/sp4rk/agent/router.Router` wired with the c0wrk routing system prompt and AGENTS.md context sections.
- `newCoreReflector` (`core/reflector_adapter.go`) — builds a `github.com/v0lka/sp4rk/agent/reflector.Reflector` wired with the c0wrk reflection system prompt.
- `adaptContextFactory` (`core/conductor.go`) — adapts core's `ContextManagerFactory` (returns a core `ContextManager`) to `github.com/v0lka/sp4rk/orchestration.ContextManagerFactory` (returns an `agent.ContextManager`).
- `core.Emitter` needs **no adapter**: it embeds `agent.Events` directly and is passed to the engine Conductor (which accepts `agent.Events`).

> The plan-execute-reflect pipeline — including `Planner.Plan()`, the `plannerSp4rkAdapter`, and the `emitterEventsAdapter` — was removed in [ADR-012](../decisions/012-conductor-orchestration-pipeline.md). The Conductor now plans via the LLM + `declare_plan`; `Plan`/`PlanStep` survive only as DAG library types for `delegate`/`declare_plan`.

## Data Flow Across Boundary

| Data                    | Direction     | Form                                                |
| ----------------------- | ------------- | --------------------------------------------------- |
| LLM request             | core → sp4rk  | `llm.ChatRequest` via `Router.Call()`               |
| LLM response            | sp4rk → core  | `llm.ChatResponse`                                  |
| Tool execution request  | core → sp4rk  | `tools.Tool.Execute()`                              |
| Tool result             | sp4rk → core  | `tools.ToolResult`                                  |
| Tool cache store        | sp4rk → sp4rk | `ToolResultCache.Store(name, content, meta)` — returns a short hash (git-style abbreviated SHA256 prefix, unique per session) |
| Tool cache lookup       | sp4rk ← sp4rk | `tool_result_read(hash, start_line, num_lines)` — fragment (hash is the short hash, full hash, or any unique prefix) |
| File coherence state    | sp4rk ↔ core  | `FileCoherenceChecker` (injected via context)       |
| Compaction trigger      | core → sp4rk  | `CompactionStrategy.Compact()`                      |
| Compacted messages      | sp4rk → core  | `[]llm.Message`                                     |
| Plan structure          | sp4rk → core  | `orchestration.Plan` (direct)                       |
| Orchestration lifecycle | core → sp4rk  | `core.Emitter` (embeds `agent.Events`; passed directly to the engine) |
| Blackboard state        | sp4rk ↔ core  | `orchestration.Blackboard` (direct, shared)         |
| Context window status   | sp4rk → core  | `ContextManager.FillPercent()` / `AvailableTokens()` / `MaxTokens()` / `UsedTokens()` |
| CompactionResult        | sp4rk → core  | `CompactionResult{BeforePercent, AfterPercent}` (no `Strategy` field) |

## Error Propagation

- sp4rk errors bubble up through core as-is (no re-wrapping at this boundary)
- Core adds context when the error's origin is ambiguous: `fmt.Errorf("routing failed: %w", err)`
- sp4rk never returns core-specific error types

## Breaking Change Checklist

- If you modify a `github.com/v0lka/sp4rk/orchestration` interface → update adapter in `core/orchestrator.go`
- If you add a field to `github.com/v0lka/sp4rk/orchestration.ConductorConfig` → update `RunConductor` in `core/conductor.go`
- If you change `CallerForStep` signature (github.com/v0lka/sp4rk/planner) → update all closures in `core/builder.go`, plus all call sites in `github.com/v0lka/sp4rk/planner/planner.go` (note: core no longer uses the planner pipeline after ADR-012; `CallerForStep` is not referenced from `core/`)
- If you modify `github.com/v0lka/sp4rk/agent.Events` → update `core.Emitter` interface AND `noopEmitter`
- If you change the `github.com/v0lka/sp4rk/tools.Tool` interface → update `github.com/v0lka/sp4rk/tools/mcp/mcptool.go`, ALL builtins, AND all test mocks implementing `Tool`
- If you add a new sp4rk type that backend or desktop needs → import directly from the source package
- If you modify `github.com/v0lka/sp4rk/tools.FileCoherenceChecker` → update `backend/session/file_coherence.go` implementation
- If you change path-containment context helpers (`SessionRoots`, `WithAllowedRoots`/`AllowedRootsFrom`, `AllPathsInSessionRoots`, `WithWorkspacePath`, `WithTempDir`) → update `core/tools/registry.go` (auto-approval), `core/tools/registry_symlink.go` (symlink roots), `backend/session/manager_execution.go` (context injection), and the security-model spec
- If you change `IsUntrusted()` semantics → update ALL built-in tool registrations (`github.com/v0lka/sp4rk/tools/builtins/*.go`) AND `github.com/v0lka/sp4rk/security/wrap.go`

## Related Specs

- [sp4rk agent-execution contract](https://github.com/v0lka/sp4rk/blob/main/specs/contracts/agent-execution.md) - canonical contract for what an embedding application provides (events, HITL, ToolExecutor)
- [sp4rk tools contract](https://github.com/v0lka/sp4rk/blob/main/specs/contracts/tools.md) - canonical `Tool`/`ToolRegistry`/`ToolPolicy`/`ToolJudger` definitions
- [sp4rk llm-providers contract](https://github.com/v0lka/sp4rk/blob/main/specs/contracts/llm-providers.md) - canonical `Provider`/`Router`/`ModelRegistry` definitions
- [ADR-008](../decisions/008-backend-sp4rk-direct-import.md) - backend may import sp4rk directly
- [ADR-015](../decisions/015-sp4rk-external-module-dependency.md) - sp4rk as an external module dependency
