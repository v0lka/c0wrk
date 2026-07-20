# Contract: Backend <-> Core

## Boundary Rule

`backend` imports `core` and sp4rk directly. Desktop imports `backend`, `core`, and sp4rk directly (see ADR-008).

## Interfaces

| Type                  | Package        | Direction      | Purpose                               |
| --------------------- | -------------- | -------------- | ------------------------------------- |
| `OrchestratorBuilder` | core           | core → backend | Factory for per-session orchestrators |
| `Orchestrator`        | core           | core → backend | Per-session orchestration engine      |
| `BuilderConfig`       | core           | backend → core | Configuration transfer object         |
| `HandleResult`        | core           | core → backend | Orchestration output                  |
| `HandleOptions`       | core           | backend → core | Execution mode, plan review, model override, reasoning effort, user skill overrides, session plans dir, pending attachments |
| `Emitter`             | core           | backend → core | Event emission interface              |
| `Blackboard`          | github.com/v0lka/sp4rk/orchestration (direct) | core → backend | Task state (for persistence)          |
| `RoutingDecision`     | github.com/v0lka/sp4rk/agent/router | core → backend | Routing classification                |
| `Plan`, `PlanStep`    | github.com/v0lka/sp4rk/orchestration (direct) | core → backend | Plan structure                        |
| `ToolPolicy`          | github.com/v0lka/sp4rk/tools      | backend → core | Security policy values                |
| `BuiltinToolsConfig`  | core/tools     | backend → core | Tool limits/config (incl. ExtraBashBlacklist). Per-tool truncation lives in `BuilderConfig.ToolLimits.PerToolTruncation`, not `BuiltinToolsConfig`. |
| `StepDumpTracker`     | github.com/v0lka/sp4rk/orchestration (direct) | backend → core | Per-step LLM dump file manager        |
| `Manager`             | core/vectorindex | core → backend | Vector index management (embedding, search, git monitoring) |
| `terminal.Manager`    | core/terminal  | core → backend | PTY/ConPTY lifecycle, shell env, I/O (Unix PTY or Windows ConPTY, selected by build tag)         |
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
    stepDumpTracker *orchestration.StepDumpTracker,
) (*core.Orchestrator, error)
```

The factory captures `*OrchestratorBuilder` and calls `Build()` per session. The `stepDumpTracker` is created by the session manager from the session's dump directory (`~/.c0wrk/projects/<pid>/<sid>/dumps/steps/`) when DEBUG-level logging is enabled. If nil, per-step dumps are a no-op.

## Session Manager Ownership

```
backend.Application
  └─ SessionManager
       ├─ Creates orchestrators (via factory)
       ├─ Routes SendMessage to correct session
       ├─ Manages session lifecycle (create/delete/rename)
       └─ Owns event persistence (SQLite)
```

The session manager never touches core internals — it treats the Orchestrator as a black box with `HandleMessage()`, `Resume()`, `PlanWithFeedback()`, `SemanticValidatePlan()`, and `PlanReviewPhase` signalling on `HandleResult` entry points.

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
| Plan review flag       | backend → core | `HandleOptions.PlanReview`               |
| User-specified skills  | backend → core | `HandleOptions.UserSkills`               |
| Model override         | backend → core | `HandleOptions.ModelOverride`            |
| Reasoning effort       | backend → core | `HandleOptions.ReasoningEffort`          |
| Session plans dir      | backend → core | `HandleOptions.SessionPlansDir`          |
| Task ID (continuation) | backend → core | `HandleOptions.TaskID`                   |
| Pending attachments    | backend → core | `HandleOptions.PendingAttachments` (user-attached files converted to markdown via `core/markitdown`; flushed into the blackboard once before execution — see [../domains/session-lifecycle.md](../domains/session-lifecycle.md)) |
| Available tools config | backend → core | `BuiltinToolsConfig` (incl. ExtraBashBlacklist). Per-tool truncation via `BuilderConfig.ToolLimits.PerToolTruncation`. |
| No Project mode        | backend → core | `Orchestrator.SetNoProjectMode()` (disables code tools, adds bash blacklist) |
| Tool cache config      | backend → core | `BuilderConfig.ToolResultBudget.CacheTTLSeconds` |
| Security policies      | backend → core | `BuilderConfig.Security`                 |
| Execution result       | core → backend | `*HandleResult`                          |
| Plan review path       | core → backend | `HandleResult.PlanReviewPath`            |
| Lifecycle events       | core → backend | `Emitter` method calls                   |
| Blackboard state       | core → backend | `Blackboard` interface (for persistence) |

## Error Propagation

- Core returns `error` from `HandleMessage()` / `Resume()`
- Backend wraps with descriptive context (no `session %s:` prefix idiom — uses general `fmt.Errorf("failed to <action>: %w", err)`)
- Backend decides whether to emit error to frontend or retry

## Breaking Change Checklist

- Adding a field to `BuilderConfig` → update `backend/configadapter.go`
- Adding a new per-tool truncation entry → update `backend/configadapter.go` `convertTruncationMap()` (maps to `BuilderConfig.ToolLimits.PerToolTruncation`, not `BuiltinToolsConfig`)
- Changing `OrchestratorFactory` signature → update factory closure in `backend/application.go` and all test factory mocks
- Changing `HandleResult` fields → update session event emission in backend
- Adding `PlanReview` to `HandleOptions` → update `backend/frontend_api_session.go` + `backend/session/manager_execution.go`
- Adding `PlanReviewPhase`/`PlanReviewPath` to `HandleResult` → update session manager goroutine branching
- Changing `Emitter` interface → update backend emitter implementation
- Adding new `OrchestratorBuilder` method → update `backend/application.go` if exposed to frontend
- Changing tool config types → update `BuiltinToolsConfig` re-exports in `core/tools/builtin_registration.go`
- `vectorindex.ManagerConfig` no longer accepts model-path fields (`ModelPath`, `TokenizerPath`, `LibraryPath`, `MaxSeqLength`, `HiddenDim`); caller is now responsible for embedder lifecycle via `EmbeddingFunc` + `CloseFn`
- `core/tools` AskUser* types are c0wrk-specific; import directly from `core/tools`
- Adding `SessionPlansDir` to `HandleOptions` → update session manager `HandleOptions` construction in `backend/session/manager_execution.go`
- Adding `PendingAttachments` to `HandleOptions` → update `backend/session/manager_execution.go` (snapshot + clear `session.pendingAttachments`, pass through both HandleMessage calls in `SendMessage`); attachments are flushed into the blackboard inside `core` (`Orchestrator.setupBlackboard`)
- Adding `ReviewMode` to `HandleOptions` → thread a `reviewMode bool` through `backend/frontend_api_session.go` (`FrontendAPI.SendMessage`) + `backend/session/manager_execution.go` (`Manager.SendMessage`, both `HandleOptions` construction sites in the send goroutine); in core, `HandleMessage` sets `ReviewModeKey` so `buildSystemPrompt` renders the Code Review section (prompts `CodeReviewMode`). Also add a `, false` arg to all `Manager.SendMessage` test call sites, regenerate Wails bindings (`wails generate module`), and add the param to `frontend/src/api/chat.ts` (`sendMessage`) plus the review submit call site (`useReviewActions.handleSubmit`).
- Removing `LogDir`/`ProjectsDir` from `ApplicationConfig` → update `desktop/startup.go` caller; use `backend/config/paths.go` functions instead
- Changing directories under `~/.c0wrk/` → update `backend/config/paths.go` (single source of truth); verify all callers use path functions, not direct `filepath.Join`
