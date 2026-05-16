# Contract: Desktop <-> Frontend

## Boundary Rule

Frontend communicates with Go exclusively through Wails IPC. No direct Go imports. Two channels: RPC (request/response) and Events (push notifications).

## Interfaces

| Interface / Type           | Package  | Direction          | Purpose                             |
| -------------------------- | -------- | ------------------ | ----------------------------------- |
| `FrontendAPI`              | backend  | backend → frontend | Wails-bound API (promoted to `App`) |
| `SessionInfo`              | backend  | backend → frontend | Session metadata                    |
| `ProjectInfo`              | backend  | backend → frontend | Project metadata                    |
| `FileNode`                 | backend  | backend → frontend | File tree entry                     |
| `ChatMessage`              | backend  | backend → frontend | Message history entry               |
| `VectorIndexStatus`        | backend  | backend → frontend | Index progress                      |
| `MCPServerStatus`          | backend  | backend → frontend | MCP server state                    |
| `ToolInfo`                 | backend  | backend → frontend | Tool descriptor for UI              |
| `ConfigResponse`           | backend  | backend → frontend | Sanitized config view               |
| `LLMSettingsRequest`       | frontend | frontend → backend | LLM provider config update          |
| `SecuritySettingsResponse` | backend  | backend ↔ frontend | Security policy CRUD                |
| `OptimizePromptResponse`   | backend  | backend → frontend | Prompt optimization result          |
| `SkillDescriptorDTO`       | backend  | backend → frontend | Skill listing                       |
| `TerminalCommand`          | backend  | backend → frontend | Terminal command history            |
| `VectorStoreEntry`         | backend  | backend → frontend | Vector search result                |
| `BlackboardStateResponse`  | backend  | backend → frontend | Task state for resume UI            |

## RPC Surface

All methods on `*desktop.App` (promoted from `*backend.FrontendAPI`) are callable from frontend via `window.go.desktop.App.<MethodName>()`.

### Session (`backend/frontend_api_session.go`)

| Method               | Parameters                   | Returns                   | Description                         |
| -------------------- | ---------------------------- | ------------------------- | ----------------------------------- |
| `CreateSession`      | —                            | SessionInfo               | Create new session (active project) |
| `DeleteSession`      | id                           | error                     | Delete session                      |
| `RenameSession`      | id, name                     | error                     | Rename session                      |
| `ArchiveSession`     | id                           | error                     | Archive/unarchive session           |
| `ListSessions`       | —                            | []SessionInfo             | List active project sessions        |
| `GetSessionHistory`  | id                           | []ChatMessage             | Get message history                 |
| `GetBlackboardState` | sessionID                    | \*BlackboardStateResponse | Get blackboard task state           |
| `SendMessage`        | id, text, mode, activeSkills | error                     | Send user message (async execution) |
| `CancelTask`         | id                           | error                     | Cancel running task                 |
| `ResumeTask`         | id                           | error                     | Resume failed task                  |

### Project (`backend/frontend_api_project.go`)

| Method          | Parameters         | Returns       | Description        |
| --------------- | ------------------ | ------------- | ------------------ |
| `CreateProject` | name, externalPath | \*ProjectInfo | Create project     |
| `DeleteProject` | id                 | error         | Delete project     |
| `RenameProject` | id, name           | error         | Rename project     |
| `ListProjects`  | —                  | []ProjectInfo | List all projects  |
| `SwitchProject` | id                 | error         | Set active project |

### Config (`backend/frontend_api_config.go`)

| Method                   | Parameters               | Returns                  | Description                    |
| ------------------------ | ------------------------ | ------------------------ | ------------------------------ |
| `GetConfig`              | —                        | ConfigResponse           | Get current config (sanitized) |
| `UpdateLLMSettings`      | LLMSettingsRequest       | error                    | Update LLM provider/model      |
| `UpdateSearchSettings`   | SearchSettingsRequest    | error                    | Update search config           |
| `GetSecuritySettings`    | —                        | SecuritySettingsResponse | Get security policies          |
| `UpdateSecuritySettings` | SecuritySettingsResponse | error                    | Update security policies       |
| `GetLogLevel`            | —                        | string                   | Get current log level          |
| `SetLogLevel`            | level                    | error                    | Set log level dynamically      |
| `ListProviderModels`     | provider                 | []string                 | List models for a provider     |

### Workspace (`backend/frontend_api_workspace.go`)

| Method                | Parameters         | Returns                   | Description                  |
| --------------------- | ------------------ | ------------------------- | ---------------------------- |
| `ListDirectory`       | dirPath, recursive | []FileNode                | List directory contents      |
| `ReadFile`            | filePath           | string                    | Read file contents           |
| `GetFileDiff`         | filePath           | string                    | Get git diff for file        |
| `GetGitStatus`        | dirPath            | map[string]GitStatusEntry | Get git status for directory |
| `GetSessionWorkspace` | sessionID          | string                    | Get session workspace path   |
| `GetFileIcon`         | filePath           | FileIconResponse          | Get devicon for file         |
| `WatchDirectory`      | dirPath            | error                     | Subscribe to dir changes     |
| `UnwatchDirectory`    | dirPath            | error                     | Unsubscribe dir changes      |
| `GetSessionTokens`    | sessionID          | SessionTokensResponse     | Get token usage for session  |

### MCP (`backend/frontend_api_mcp.go`)

| Method             | Parameters                 | Returns                    | Description                |
| ------------------ | -------------------------- | -------------------------- | -------------------------- |
| `GetMCPStatus`     | —                          | []MCPServerStatus          | Get MCP server statuses    |
| `GetMCPServers`    | —                          | map[string]MCPServerConfig | Get MCP server configs     |
| `GetToolList`      | —                          | []ToolInfo                 | List all registered tools  |
| `UpdateMCPServers` | map[string]MCPServerConfig | error                      | Update MCP config + reload |

### Terminal (`backend/frontend_api_terminal.go`)

| Method               | Parameters            | Returns           | Description         |
| -------------------- | --------------------- | ----------------- | ------------------- |
| `StartTerminal`      | sessionID             | error             | Start PTY terminal  |
| `TerminalInput`      | sessionID, data       | error             | Write to terminal   |
| `TerminalResize`     | sessionID, cols, rows | error             | Resize terminal     |
| `StopTerminal`       | sessionID             | error             | Stop terminal       |
| `GetTerminalHistory` | sessionID             | []TerminalCommand | Get command history |

### Vector (`backend/frontend_api_vector.go`)

| Method              | Parameters                                                    | Returns            | Description                                         |
| ------------------- | ------------------------------------------------------------- | ------------------ | --------------------------------------------------- |
| `SearchVectorStore` | `SearchRequest{query, top_k, file_pattern, must_match, mode}` | []VectorStoreEntry | Hybrid search/browse; mode= hybrid\|vector\|lexical |

### Prompt (`backend/frontend_api_prompt.go`)

| Method           | Parameters | Returns                  | Description          |
| ---------------- | ---------- | ------------------------ | -------------------- |
| `OptimizePrompt` | prompt     | \*OptimizePromptResponse | Optimize user prompt |

### Skills (`backend/frontend_api_skills.go`)

| Method       | Parameters | Returns              | Description                              |
| ------------ | ---------- | -------------------- | ---------------------------------------- |
| `ListSkills` | —          | []SkillDescriptorDTO | List available skills (name+description) |

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
- Adding new event type -> add Go emitter method -> add TS type + type guard -> add handler in relevant hook
- Renaming event -> update both Go emitter AND all frontend subscribers
