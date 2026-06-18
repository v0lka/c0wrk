# Contract: Backend <-> Core

## Boundary Rule

`backend` imports `core` and `sdk` directly. Desktop imports `backend`, `core`, and `sdk` directly (see ADR-008).

## Interfaces

| Type                  | Package        | Direction      | Purpose                               |
| --------------------- | -------------- | -------------- | ------------------------------------- |
| `OrchestratorBuilder` | core           | core → backend | Factory for per-session orchestrators |
| `Orchestrator`        | core           | core → backend | Per-session orchestration engine      |
| `BuilderConfig`       | core           | backend → core | Configuration transfer object         |
| `HandleResult`        | core           | core → backend | Orchestration output                  |
| `HandleOptions`       | core           | backend → core | Execution mode, model override, reasoning effort, user skill overrides |
| `Emitter`             | core           | backend → core | Event emission interface              |
| `Blackboard`          | sdk/orchestration (direct) | core → backend | Task state (for persistence)          |
| `RoutingDecision`     | core           | core → backend | Routing classification                |
| `Plan`, `PlanStep`    | sdk/orchestration (direct) | core → backend | Plan structure                        |
| `ToolPolicy`          | sdk/tools      | backend → core | Security policy values                |
| `BuiltinToolsConfig`  | core/tools     | backend → core | Tool limits/config (incl. perToolTruncation) |
| `Manager`             | sdk/vectorindex | core → backend | Vector index management (embedding, search, git monitoring) |
| `PTYManager`          | core/terminal  | core → backend | PTY lifecycle, shell env, I/O         |
| `Watcher`             | core/workspace | core → backend | Filesystem event watcher with debouncing |
| `FileNode`            | core/workspace | core → backend | File tree node (type alias in backend) |
| `GitStatusEntry`      | core/workspace | core → backend | Git porcelain status (type alias in backend) |

## Workspace Services (core/workspace)

Backend calls these stateless functions directly. Caching of `IsGitRepo` results
is a backend ViewModel concern (see `FrontendAPI.isGitRepo`).

| Function | Signature | Purpose |
| -------- | --------- | ------- |
| `IsGitRepo` | `(ctx context.Context, dir string) bool` | Check if dir is inside a git work tree |
| `IsGitTracked` | `(ctx context.Context, dir, relPath string) bool` | Check if a file is tracked by git |
| `GitStatus` | `(ctx context.Context, repoPath string) (map[string]GitStatusEntry, error)` | Parse `git status --porcelain` output |
| `GetFileDiff` | `(ctx context.Context, repoPath, relPath string) (string, error)` | Convenience wrapper: auto-detects repo and delegates |
| `GetFileDiffInRepo` | `(ctx context.Context, repoPath, relPath string) (string, error)` | Diff for git repos (caller guarantees repo) |
| `GetFileDiffNoRepo` | `(ctx context.Context, repoPath, relPath string) (string, error)` | Diff for non-repo workspaces (caller guarantees non-repo) |
| `GitIgnoredPaths` | `(ctx context.Context, dir string) (map[string]bool, error)` | Set of git-ignored absolute paths |
| `ListDirFlat` | `(absDir string, ignoredPaths map[string]bool, opts ...ListDirOption) ([]FileNode, error)` | Immediate children listing |
| `ListDirRecursive` | `(absDir string, ignoredPaths map[string]bool, opts ...ListDirOption) ([]FileNode, error)` | Recursive flat listing |

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
// backend/session/manager.go
type OrchestratorFactory func(
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
| User-specified skills  | backend → core | `HandleOptions.UserSkills`               |
| Model override         | backend → core | `HandleOptions.ModelOverride`            |
| Reasoning effort       | backend → core | `HandleOptions.ReasoningEffort`          |
| Task ID (continuation) | backend → core | `HandleOptions.TaskID`                   |
| Available tools config | backend → core | `BuiltinToolsConfig` (incl. perToolTruncation) |
| Tool cache config      | backend → core | `BuilderConfig.ToolResultBudget.CacheTTLSeconds` |
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
- Adding a new per-tool truncation entry → update `backend/configadapter.go` `convertTruncationMap()`
- Changing `HandleResult` fields → update session event emission in backend
- Changing `Emitter` interface → update backend emitter implementation
- Adding new `OrchestratorBuilder` method → update `backend/application.go` if exposed to frontend
- Changing tool config types → update `BuiltinToolsConfig` re-exports in `core/tools/builtin_registration.go`
- `vectorindex.ManagerConfig` no longer accepts model-path fields (`ModelPath`, `TokenizerPath`, `LibraryPath`, `MaxSeqLength`, `HiddenDim`); caller is now responsible for embedder lifecycle via `EmbeddingFunc` + `CloseFn`
- `core/tools` AskUser* and builtins limit/vector type aliases removed per ADR-008; import directly from `sdk/tools` / `sdk/tools/builtins`
