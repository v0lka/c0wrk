# Contract: Backend <-> Core

## Boundary Rule

`backend` imports `core`. `backend` NEVER imports `sdk`. Desktop imports backend (not core directly for business logic).

## Interfaces

| Type                  | Package        | Direction      | Purpose                               |
| --------------------- | -------------- | -------------- | ------------------------------------- |
| `OrchestratorBuilder` | core           | core → backend | Factory for per-session orchestrators |
| `Orchestrator`        | core           | core → backend | Per-session orchestration engine      |
| `BuilderConfig`       | core           | backend → core | Configuration transfer object         |
| `HandleResult`        | core           | core → backend | Orchestration output                  |
| `HandleOptions`       | core           | backend → core | Execution mode control                |
| `Emitter`             | core           | backend → core | Event emission interface              |
| `Blackboard`          | core (alias)   | core → backend | Task state (for persistence)          |
| `RoutingDecision`     | core           | core → backend | Routing classification                |
| `Plan`, `PlanStep`    | core (aliases) | core → backend | Plan structure                        |
| `ToolPolicy`          | core/tools     | backend → core | Security policy values                |
| `BuiltinToolsConfig`  | core/tools     | backend → core | Tool limits/config                    |

## Config Adapter

Single conversion point: `backend/configadapter.go`

```
config.Config (backend/config package)
         │
         ▼
ToBuilderConfig(cfg) → core.BuilderConfig
         │
         ▼
core.NewOrchestratorBuilder(builderCfg, askUserFunc, logger)
```

All config field mapping happens in this one function. When adding config fields:

1. Add to `backend/config` struct (YAML parsing)
2. Add to `core.BuilderConfig` if it needs to reach core
3. Map in `backend/configadapter.go`

## Factory Pattern

Backend creates orchestrators through a closure factory:

```go
// backend/application.go
type orchestratorFactory func(
    emitter core.Emitter,
    logger *slog.Logger,
    workspacePath string,
    bbFactory core.BlackboardFactory,
    dumpWriter io.Writer,
) (*core.Orchestrator, error)
```

The factory captures `*OrchestratorBuilder` and calls `Build()` per session.

## Session Manager Ownership

```
backend.Application
  └─ SessionManager
       ├─ Creates orchestrators (via factory)
       ├─ Routes SendMessage to correct session
       ├─ Manages session lifecycle (create/delete/rename)
       └─ Owns event persistence (SQLite)
```

The session manager never touches core internals — it treats the Orchestrator as a black box with `HandleMessage()` and `Resume()` entry points.

## Event Emission

Backend implements `core.Emitter`:

1. Receives lifecycle events from core during execution
2. Persists events to SQLite (`EventPersister`)
3. Emits to frontend via Wails `runtime.EventsEmit()`

The emitter implementation lives in `backend/session/` (not in core).

## Data Flow Across Boundary

| Data                   | Direction      | Form                                     |
| ---------------------- | -------------- | ---------------------------------------- |
| User message           | backend → core | `string` via `HandleMessage()`           |
| Execution mode         | backend → core | `HandleOptions.ExecutionMode`            |
| Task ID (continuation) | backend → core | `HandleOptions.TaskID`                   |
| Available tools config | backend → core | `BuiltinToolsConfig`                     |
| Security policies      | backend → core | `BuilderConfig.Security`                 |
| Execution result       | core → backend | `*HandleResult`                          |
| Lifecycle events       | core → backend | `Emitter` method calls                   |
| Blackboard state       | core → backend | `Blackboard` interface (for persistence) |

## Error Propagation

- Core returns `error` from `HandleMessage()` / `Resume()`
- Backend wraps with session context: `fmt.Errorf("session %s: %w", id, err)`
- Backend decides whether to emit error to frontend or retry

## Breaking Change Checklist

- Adding a field to `BuilderConfig` → update `backend/configadapter.go`
- Changing `HandleResult` fields → update session event emission in backend
- Changing `Emitter` interface → update backend emitter implementation
- Adding new `OrchestratorBuilder` method → update `backend/application.go` if exposed to frontend
- Changing tool config types → update `BuiltinToolsConfig` re-exports in `core/tools/builtin_registration.go`
