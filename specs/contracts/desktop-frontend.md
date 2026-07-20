# Contract: Desktop <-> Frontend

## Boundary Rule

Frontend communicates with Go exclusively through Wails IPC. No direct Go imports. Two channels: RPC (request/response) and Events (push notifications).

## Interfaces

| Interface / Type           | Package  | Direction          | Purpose                             |
| -------------------------- | -------- | ------------------ | ----------------------------------- |
| `FrontendAPI`              | backend  | backend → frontend | Wails-bound API (promoted to `App`) |
| `SessionInfo`              | backend  | backend → frontend | Session metadata                    |
| `ProjectInfo`              | backend  | backend → frontend | Project metadata                    |
| `ProjectUIStateRequest`    | backend  | frontend → backend | Persisted project switch UI state write payload |
| `ProjectUIStateResponse`   | backend  | backend → frontend | Persisted project switch UI state read payload  |
| `FileNode`                 | backend  | backend → frontend | File tree entry                     |
| `ChatMessage`              | backend  | backend → frontend | Message history entry               |
| `VectorIndexStatus`        | backend  | backend → frontend | Index progress                      |
| `mcp.ServerStatus`        | github.com/v0lka/sp4rk/tools/mcp | backend → frontend | MCP server state (used by `GetMCPStatus`) |
| `ToolInfo`                 | backend  | backend → frontend | Tool descriptor for UI              |
| `ConfigResponse`           | backend  | backend → frontend | Sanitized config view               |
| `LLMFullConfigRequest`    | frontend | frontend → backend | LLM multi-provider config update |
| `SecuritySettingsResponse` | backend  | backend ↔ frontend | Security policy CRUD                |
| `OptimizePromptResponse`   | backend  | backend → frontend | Prompt optimization result          |
| `SkillDescriptorDTO`       | backend  | backend → frontend | Skill listing                       |
| `TerminalCommand`          | backend  | backend → frontend | Terminal command history            |
| `VectorStoreEntry`         | backend  | backend → frontend | Vector search result                |
| `BlackboardStateResponse`  | backend  | backend → frontend | Task state for resume UI            |
| `AttachmentInfo`           | backend  | backend → frontend | Pending file-attachment metadata (snake_case; markdown content excluded) |

## RPC Surface

All methods on `*desktop.App` (promoted from `*backend.FrontendAPI`) are callable from frontend via `window.go.desktop.App.<MethodName>()`.

**Convention**: Methods that can fail return `(T, error)` in Go. Read-only getters that cannot fail return `T` only (e.g., `GetConfig`, `GetSecuritySettings`, `GetProxySettings`, `GetMCPStatus`, `GetMCPServers`, `GetToolList`, `GetVectorIndexStatus`, `ListSkills`, `GetSessionTokens`). The "Returns" column shows the actual signature; Wails surfaces `error` as a rejected Promise in TypeScript.

### Session (`backend/frontend_api_session.go`)

| Method                 | Parameters                   | Returns                   | Description                                           |
| ---------------------- | ---------------------------- | ------------------------- | ----------------------------------------------------- |
| `CreateSession`        | —                            | (\*SessionInfo, error)    | Create new session (active project)                   |
| `DeleteSession`        | id                           | error                     | Delete session                                        |
| `RenameSession`        | id, name                     | error                     | Rename session                                        |
| `ArchiveSession`       | id                           | error                     | Archive/unarchive session                             |
| `PinSession`           | id                           | error                     | Toggle session pin (affects ordering/filtering)       |
| `ForkSession`          | id                           | (\*SessionInfo, error)    | Deep-copy a session into an independent fork (messages, tasks+steps/facts/attachments/trajectory, terminal commands, work directories, review) with regenerated identifiers in one atomic transaction; runtime counters reset, name "`<src> (fork N)`". Rejected when the session has an unfinished (`in_progress`/`failed`) task. The returned session becomes the active session |
| `ListSessions`         | —                            | ([]SessionInfo, error)    | List active project sessions                          |
| `GetSessionHistory`    | id                           | ([]ChatMessage, error)    | Get message history                                   |
| `GetSessionRuntimeStatus` | id                        | (SessionRuntimeStatus, error) | Live/persisted execution state: `{active, has_unfinished_task, unfinished_task_id?}`. Called after history load to reconcile the UI (running flag, resume banner, stale prompts) instead of defaulting to idle |
| `GetBlackboardState`   | sessionID                    | (\*BlackboardStateResponse, error) | Get blackboard task state                    |
| `SendMessage`          | id, text, mode, activeSkills, modelOverride, reasoningEffort, planReview | error                     | Send user message (async execution)                   |
| `CancelTask`           | id                           | error                     | Cancel running task                                   |
| `ResumeTask`           | id                           | error                     | Resume failed task                                    |
| `CancelUnfinishedTask` | id                           | error                     | Discard a resumable task (no resume prompt next time) |
| `GetSessionTokens`    | sessionID                    | SessionTokensResponse     | Get token usage for session (getter, no error return) |

### Attachments (`backend/frontend_api_attachment.go`)

| Method              | Parameters               | Returns                       | Description |
| ------------------- | ------------------------ | ----------------------------- | ----------- |
| `AttachFiles`       | sessionID, paths         | ([]AttachmentInfo, error)     | Convert files to markdown via `core/markitdown` and stage them as pending attachments; emits `attachments:changed` (incremental per file + final with per-file failures). Returns the full pending list. System-level errors (session missing, converter init) return `error`; file-level failures (unsupported format, conversion error) are reported via the event payload's `Failed` field, not as `error`, so partial success is preserved |
| `RemoveAttachment`  | sessionID, attachmentID   | error                         | Remove a staged (pending) attachment by ID; no-op if not found. Does not touch attachments already flushed into the blackboard |
| `GetAttachments`    | sessionID                | ([]AttachmentInfo, error)     | Get the session's staged (pending) attachments as metadata-only values |

### Project (`backend/frontend_api_project.go`)

| Method                   | Parameters                  | Returns                         | Description |
| ------------------------ | --------------------------- | ------------------------------- | ----------- |
| `CreateProject`          | name, externalPath          | (\*ProjectInfo, error)         | Create project with external workspace (UI always supplies externalPath; internal workspaces reserved for No Project auto-creation) |
| `DeleteProject`          | id                          | error                           | Delete project |
| `RenameProject`          | id, name                    | error                           | Rename project |
| `ListProjects`           | —                           | ([]ProjectInfo, error)          | List all projects |
| `SaveProjectUIState`     | `ProjectUIStateRequest`     | error                           | Persist per-project UI switch state (saved session + open tabs + active file) |
| `GetProjectUIState`      | projectID                   | (\*ProjectUIStateResponse, error) | Load per-project UI switch state |
| `SaveProjectSwitchState` | `ProjectUIStateRequest`     | error                           | Backward-compatible alias for `SaveProjectUIState` |
| `GetProjectSwitchState`  | projectID                   | (\*ProjectUIStateResponse, error) | Backward-compatible alias for `GetProjectUIState` |
| `SwitchProject`          | id                          | error                           | Set active project and resolve destination session fallback |

### Config (`backend/frontend_api_config.go`)

| Method                   | Parameters               | Returns                           | Description                    |
| ------------------------ | ------------------------ | --------------------------------- | ------------------------------ |
| `GetConfig`              | —                        | ConfigResponse                    | Get current config (sanitized) |
| `UpdateLLMConfig`       | LLMFullConfigRequest    | error                             | Update full LLM multi-provider config |
| `UpdateSearchSettings`   | SearchSettingsRequest    | error                             | Update search config           |
| `GetSecuritySettings`    | —                        | SecuritySettingsResponse          | Get security policies          |
| `UpdateSecuritySettings` | SecuritySettingsResponse | error                             | Update security policies       |
| `GetLogLevel`            | —                        | string                            | Get current log level          |
| `SetLogLevel`            | level                    | error                             | Set log level dynamically      |
| `ListProviderModels`     | provider                 | ([]string, error)                 | List models for a provider     |
| `GetProxySettings`       | —                        | ProxySettingsResponse             | Get proxy configuration        |
| `UpdateProxySettings`    | ProxySettingsRequest     | error                             | Update proxy configuration     |

### Workspace (`backend/frontend_api_workspace.go`)

| Method                | Parameters         | Returns                              | Description                  |
| --------------------- | ------------------ | ------------------------------------ | ---------------------------- |
| `ListDirectory`       | dirPath, recursive | ([]FileNode, error)                  | List directory contents (workspace-contained) |
| `WriteFile`           | sessionID, path, content | error                          | Write content to a file (workspace-contained; structural/write RPC, resolves via `resolveWorkspacePath`) |
| `ReadFile`            | filePath           | (string, error)                      | Read file contents (any absolute path; not constrained to workspace — the viewer surfaces paths the agent cites, including out-of-workspace files like SDK sources). A trailing `#L<n>` / `#L<n>-L<m>` line anchor is stripped before resolution |
| `GetFileDiff`         | filePath           | (string, error)                      | Get git diff for file (any absolute path; returns `("", nil)` for files outside the active project root or a non-git path — no baseline to diff against) |
| `GetGitStatus`        | dirPath            | (map[string]GitStatusEntry, error)   | Get git status for directory |
| `GetSessionWorkspace` | sessionID          | (string, error)                      | Get session workspace path   |
| `GetFileIcon`         | filePath           | (FileIconResponse, error)            | Get devicon for file (any absolute path; not constrained to workspace) |
| `WatchDirectory`      | dirPath            | error                                | Subscribe to dir changes     |
| `UnwatchDirectory`    | dirPath            | error                                | Unsubscribe dir changes      |

> **Path-containment boundary**: read-path RPCs (`ReadFile`, `GetFileIcon`, `GetFileDiff`) resolve via `resolveReadablePath` and are **not** workspace-contained — the file viewer must display any path the agent surfaces in chat. Structural/write RPCs (`WriteFile`, `ListDirectory`) resolve via `resolveWorkspacePath` and **retain** containment (reject paths outside the active project workspace). This is a UI-display affordance only and does **not** affect the agent's tool surface: the `read_file` agent tool enforces its own session-root containment independently (see [../architecture/security-model.md](../architecture/security-model.md)).

### MCP (`backend/frontend_api_mcp.go`)

| Method             | Parameters                 | Returns                           | Description                |
| ------------------ | -------------------------- | --------------------------------- | -------------------------- |
| `GetMCPStatus`     | —                          | []mcp.ServerStatus               | Get MCP server statuses    |
| `GetMCPServers`    | —                          | map[string]MCPServerConfig       | Get MCP server configs   |
| `GetToolList`      | —                          | []ToolInfo                       | List all registered tools  |
| `UpdateMCPServers` | map[string]MCPServerConfig | error                             | Update MCP config + reload |

### Terminal (`backend/frontend_api_terminal.go`)

| Method               | Parameters            | Returns                      | Description         |
| -------------------- | --------------------- | ---------------------------- | ------------------- |
| `StartTerminal`      | sessionID             | error                        | Start PTY terminal  |
| `TerminalInput`      | sessionID, data       | error                        | Write to terminal   |
| `TerminalResize`     | sessionID, cols, rows | error                        | Resize terminal     |
| `StopTerminal`       | sessionID             | error                        | Stop terminal       |
| `GetTerminalHistory` | sessionID             | ([]TerminalCommand, error)   | Get command history |

### Vector (`backend/frontend_api_vector.go`)

| Method                | Parameters                                                    | Returns                       | Description                                         |
| --------------------- | ------------------------------------------------------------- | ----------------------------- | --------------------------------------------------- |
| `SearchVectorStore`   | `SearchRequest{query, top_k, file_pattern, must_match, mode}` | ([]VectorStoreEntry, error)   | Hybrid search/browse; mode= hybrid\|vector\|lexical |
| `GetVectorIndexStatus`| —                                                             | VectorIndexStatus             | Get vector index state/progress (getter, no error)  |

### Git (`backend/frontend_api_git.go`)

| Method                 | Parameters              | Returns                       | Description |
| ---------------------- | ----------------------- | ----------------------------- | ----------- |
| `StageFile`            | path                    | error                         | Stage a single file |
| `UnstageFile`          | path                    | error                         | Unstage a single file |
| `StageAll`             | —                       | error                         | Stage all changes |
| `UnstageAll`           | —                       | error                         | Unstage all changes |
| `StageHunks`           | path, hunks []HunkRange | error                         | Stage selected hunks |
| `GetDiffStat`          | path                    | (*DiffStat, error)            | Diff stat for a file |
| `GetDiffStats`         | —                       | (map[string]DiffStat, error)  | Diff stats for all changed files |
| `Commit`               | message                 | (string, error)               | Create a commit |
| `GetBranches`          | —                       | ([]Branch, error)             | List branches |
| `GetCurrentBranch`     | —                       | (BranchInfo, error)           | Get current branch |
| `CheckoutBranch`       | name                    | error                         | Checkout a branch |
| `CreateBranch`         | name                    | error                         | Create a new branch |
| `GenerateCommitMessage`| diff                    | (string, error)               | AI-generate a commit message from diff |
| `Pull`                 | remote, flags []string  | (string, error)               | Pull from remote (flags: --ff-only, --rebase, --rebase --autostash) |
| `Push`                 | remote, flags []string  | (string, error)               | Push to remote (flags: --force, --force-with-lease, --no-verify) |
| `Fetch`                | remote, flags []string  | (string, error)               | Fetch from remote (flags: --tags, --prune) |
| `GetCommitLog`         | limit, skip             | ([]CommitInfo, error)         | Paginated commit log |
| `GetCommitFiles`       | sha                     | ([]CommitFile, error)         | Files changed in a commit |
| `StashCreate`          | message                 | error                         | Create a stash entry |
| `StashPop`             | index                   | error                         | Pop a stash entry |
| `StashDrop`            | index                   | error                         | Drop a stash entry |
| `StashList`            | —                       | ([]StashEntry, error)         | List stash entries |
| `DiscardChanges`       | path                    | error                         | Discard working-tree changes for a file |
| `AppendToGitignore`    | pattern                 | error                         | Append a pattern to .gitignore |
| `Merge`                | branch                  | error                         | Merge a branch |
| `Rebase`               | branch                  | error                         | Rebase onto a branch |
| `AbortMerge`           | —                       | error                         | Abort an in-progress merge |
| `AbortRebase`          | —                       | error                         | Abort an in-progress rebase |
| `GetRebaseMergeState`  | —                       | (MergeRebaseState, error)     | Get in-progress merge/rebase state |
| `GetGitGraph`          | limit, skip             | ([]GraphCommit, error)        | Paginated git graph (DAG) |

### Lifecycle (`backend/frontend_api.go`)

| Method             | Parameters       | Returns              | Description |
| ------------------ | ---------------- | -------------------- | ----------- |
| `Lifecycle`        | —                | *FrontendAPILifecycle | Returns lifecycle sub-API (config load state, vector manager, cleanup) |

### Desktop (`desktop/app.go` — methods on `*App`, not promoted from `FrontendAPI`)

| Method           | Parameters | Returns       | Description |
| ---------------- | ---------- | ------------- | ----------- |
| `GetPendingActions` | sessionID  | (*PendingActionsResponse, error) | Unresolved pending actions for a session (tool confirmations, ask-user forms, step-limit/resume prompts, goal proposals) |
| `PickDirectory`  | —          | (string, error) | Native directory picker dialog |
| `PickAttachmentFiles` | —     | ([]string, error) | Native multi-select file picker restricted to markitdown-supported document formats (filter built from `core/markitdown.SupportedExtensions()`). Returns `([]string{}, nil)` on cancel. Must remain on `App` — it requires the Wails context like `PickDirectory` |
| `SetWailsLogger` | wl         | —             | Binding artifact: stores Wails log adapter (called internally, not from frontend) |

### Prompt (`backend/frontend_api_prompt.go`)

| Method           | Parameters | Returns                             | Description          |
| ---------------- | ---------- | ----------------------------------- | -------------------- |
| `OptimizePrompt` | prompt     | (\*OptimizePromptResponse, error)   | Optimize user prompt |

### Skills (`backend/frontend_api_skills.go`)

| Method       | Parameters | Returns              | Description                              |
| ------------ | ---------- | -------------------- | ---------------------------------------- |
| `ListSkills` | —          | []SkillDescriptorDTO | List available skills (name+description) |

### Goal (`backend/frontend_api_goal.go`)

| Method          | Parameters                              | Returns | Description                                                                                          |
| --------------- | --------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------- |
| `ConfirmGoal`   | sessionID, requestID, condition, verify | error   | Approve a proposed goal (optionally with edits). Resolves the pending `goal_proposal` action          |
| `CancelGoal`    | sessionID, requestID                    | error   | Cancel a proposed goal                                                                               |
| `ClarifyGoal`   | sessionID, requestID, clarification     | error   | Ask the derivation agent for clarification on a proposed goal                                        |
| `PauseGoal`     | sessionID                               | error   | Pause an active goal loop                                                                            |
| `ResumeGoal`    | sessionID                               | error   | Resume a paused goal loop                                                                            |
| `ClearGoal`     | sessionID                               | error   | Clear the active goal for a session                                                                  |

### Work Directories (`backend/frontend_api_workdirs.go`)

| Method                          | Parameters                                  | Returns                           | Description |
| ------------------------------- | ------------------------------------------- | --------------------------------- | ----------- |
| `ListProjectWorkDirectories`    | projectID                                   | ([]WorkDirectoryRecord, error)    | Project-scoped auxiliary directories |
| `ListSessionWorkDirectories`    | sessionID                                   | ([]WorkDirectoryRecord, error)    | Session-scoped auxiliary directories |
| `AddWorkDirectory`              | scope, ownerID, path, description           | error                             | Add directory (validates existence + non-empty description; rejects project scope for No Project); emits `workdirs:changed` |
| `UpdateWorkDirectoryDescription`| scope, id, description                      | error                             | Update a directory's description; emits `workdirs:changed` |
| `DeleteWorkDirectory`           | scope, id                                   | error                             | Delete a directory; emits `workdirs:changed` |

`scope` is `"project"` or `"session"`; `ownerID` is the corresponding project/session ID. `WorkDirectoryRecord` is `project.WorkDirectoryRecord{ID, Path, Description, CreatedAt}`. The `workdirs:changed` event triggers a UI reload; directories are loaded into the execution context on the next message (via `tools.WithAllowedRoots`), and — together with the workspace path — feed a multi-root ignore checker (`tools.WithIgnoreChecker`) so `glob`/`ripgrep` honour each root's own `.gitignore` + `.aiignore` ([ADR-016](../decisions/016-aiignore.md)).

## Event Protocol

See [event-catalog.md](event-catalog.md) for complete event reference.

### Direction

- **Backend -> Frontend**: lifecycle events during task execution (25+ types)
- **Frontend -> Backend**: confirmation responses (tool_confirm, ask_user, step_limit)

### Naming Convention

- Session-scoped: `session:${sessionId}:${eventType}`
- Global: bare event name (e.g., `backend:ready`)

## Data Flow Across Boundary

```
┌──────────────────┐                    ┌──────────────────┐
│   Desktop App    │                    │  Backend (Go)    │
│  (TypeScript)    │                    │                  │
│                  │   Wails Binding    │                  │
│  Wails API calls ├────────────────────►  App struct      │
│  (async Go fn)   │                    │  (methods)       │
│                  │                    │                  │
│  Event handlers  │◄───────────────────┤  EventsEmit()    │
│  (Go events)     │   Wails Events     │                  │
│                  │                    │                  │
│  state → store   │                    │  persistence     │
│  update (Zustand)│                    │  (SQLite)        │
└──────────────────┘                    └──────────────────┘
```

- **Synchronous**: Wails RPC method calls from frontend are async (TypeScript `Promise`) but the Go handler may block
- **Asynchronous**: real-time event stream is push-only; frontend listens with `EventsOn` and publishes to stores
- **Project switch state flow**:
  1. Frontend snapshots source project UI state (`open_tabs`, `active_file`, `saved_session_id`) and calls `SaveProjectSwitchState`/`SaveProjectUIState` (best-effort)
  2. Frontend calls `SwitchProject(id)`
  3. Backend persists/normalizes source project state and switches active project context
  4. Backend resolves destination session fallback deterministically: saved session if valid for destination project → latest project session by activity (`ListSessionsByProject`) → backend creates a new project-scoped session via `SessionManager.CreateSession(projectID, workspacePath)` when no sessions exist
  5. Frontend calls `GetProjectSwitchState`/`GetProjectUIState`, restores file tabs/active file, refreshes session list, and activates the resolved session
- **Startup**: backend exposes RPC methods after `Startup()` completes; frontend waits for `backend:ready` event. Vector search methods may return empty results until background ONNX init completes (~1-2s after startup).
- **Teardown**: `Shutdown()` triggers backend cleanup; frontend stops polling and unregisters event listeners

## Error Propagation

- **RPC errors**: Go methods return `error`; Wails serializes as `Error` thrown in the TypeScript `Promise` rejection
- **Event errors**: sent as `frontend:event:error` type with `{message, code}` payload; frontend `useWailsEvent` hook catches and displays toast
- **Startup failures**: if backend `Startup()` panics, Wails shows a native error dialog; if startup completes but services fail, `GetConfig()` returns an error which frontend uses to display a "Backend unavailable" banner
- **Streaming failures**: SSE/event stream disconnects bubble to `frontend:event:error`; `chatStore.flushStreamingToMessage()` preserves partial content
- **Panic recovery**: Wails runtime catches Go panics and returns them as RPC errors; backend uses `recover()` middleware in handler chain
- **Fallback**: methods invoked before backend ready return "backend not initialized" error
- **Vector not ready**: vector search methods invoked before embedder initialization completes return empty results with no error (graceful degradation)

## Initialization

```go
// desktop/startup.go — phased startup (critical path < 500ms)
// Phase 1: shell_env + logger
// Phase 2: config + deps_check (parallel)
// Phase 3: database + terminal (parallel)
// Phase 4: stores + project/session preload
// Phase 5: application + FrontendAPI
// → EventBackendReady emitted here ←
// Background: ONNX embedder + vector index manager (non-blocking)
```

```typescript
// frontend/src/main.tsx — mount sequence
// 1. React renders App shell (header, sidebar placeholders)
// 2. useWailsEvent registers for all streaming events
// 3. useProjectLoader calls listProjects() immediately; falls back to backend:ready event
// 4. On project selected: useSessionLoader fetches sessions, FileTreePanel loads directory
// 5. UI transitions from loading state as stores populate
// 6. Vector search becomes available when vector_index:status reports ready (~1-2s)
```

- Frontend must handle the case where backend RPCs return "not initialized" during startup race conditions
- All stores initialize to empty/loading state; no implicit default data

## Type Mapping

Go structs are auto-generated as TypeScript interfaces at:

- `frontend/wailsjs/go/desktop/App.js` — method stubs
- `frontend/wailsjs/go/desktop/App.d.ts` — type declarations

Frontend wraps these in `frontend/src/api/` modules (never imports wailsjs directly from components).

## Wails Binding Regeneration

Bindings are regenerated by:

- `wails build` (production)
- `wails dev` (development with hot-reload)

Adding/removing/renaming a method on `desktop.App` or `backend.FrontendAPI` requires regeneration.

## Breaking Change Checklist

- Adding a method to FrontendAPI -> run `wails build` to regenerate bindings -> update `frontend/src/api/` wrapper
- Changing method signature -> regenerate bindings -> update frontend callers
- Changing `ProjectUIStateRequest`/`ProjectUIStateResponse` fields or renaming `SaveProjectUIState` / `GetProjectUIState` aliases -> regenerate bindings -> update `frontend/src/api/projects.ts` RPC probing and `frontend/src/types/guards.ts` shape validators
- Adding new event type -> add Go emitter method -> add TS type + type guard -> add handler in relevant hook
- Renaming event -> update both Go emitter AND all frontend subscribers
