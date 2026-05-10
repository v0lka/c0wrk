# Contract: Desktop <-> Frontend

## Boundary Rule

Frontend communicates with Go exclusively through Wails IPC. No direct Go imports. Two channels: RPC (request/response) and Events (push notifications).

## RPC Surface

All methods on `*desktop.App` (promoted from `*backend.FrontendAPI`) are callable from frontend via `window.go.desktop.App.<MethodName>()`.

### Session (`backend/frontend_api_session.go`)

| Method               | Parameters            | Returns       | Description                         |
| -------------------- | --------------------- | ------------- | ----------------------------------- |
| `CreateSession`      | projectID             | SessionInfo   | Create new session                  |
| `DeleteSession`      | sessionID             | error         | Delete session                      |
| `RenameSession`      | sessionID, name       | error         | Rename session                      |
| `ListSessions`       | projectID             | []SessionInfo | List project sessions               |
| `GetSessionMessages` | sessionID             | []ChatMessage | Get message history                 |
| `SendMessage`        | sessionID, text, mode | error         | Send user message (async execution) |
| `CancelTask`         | sessionID             | error         | Cancel running task                 |
| `ResumeTask`         | sessionID             | error         | Resume failed task                  |

### Project (`backend/frontend_api_project.go`)

| Method          | Parameters      | Returns       | Description        |
| --------------- | --------------- | ------------- | ------------------ |
| `CreateProject` | name, path      | ProjectInfo   | Create project     |
| `DeleteProject` | projectID       | error         | Delete project     |
| `ListProjects`  | —               | []ProjectInfo | List all projects  |
| `SwitchProject` | projectID       | error         | Set active project |
| `RenameProject` | projectID, name | error         | Rename project     |

### Config (`backend/frontend_api_config.go`)

| Method               | Parameters | Returns     | Description         |
| -------------------- | ---------- | ----------- | ------------------- |
| `GetConfig`          | —          | ConfigDTO   | Get current config  |
| `SaveConfig`         | ConfigDTO  | error       | Save config changes |
| `ReloadConfig`       | —          | error       | Reload from disk    |
| `GetAvailableModels` | —          | []ModelInfo | List known models   |

### Workspace (`backend/frontend_api_workspace.go`)

| Method         | Parameters  | Returns        | Description               |
| -------------- | ----------- | -------------- | ------------------------- |
| `GetFileTree`  | path, depth | []FileNode     | Get directory tree        |
| `ReadFile`     | path        | FileContent    | Read file contents        |
| `GetFileDiff`  | path        | FileDiff       | Get git diff              |
| `GetGitStatus` | —           | GitStatus      | Get workspace git status  |
| `SearchFiles`  | query       | []SearchResult | Search files in workspace |

### MCP (`backend/frontend_api_mcp.go`)

| Method             | Parameters | Returns           | Description             |
| ------------------ | ---------- | ----------------- | ----------------------- |
| `GetMCPStatus`     | —          | []MCPServerStatus | Get MCP server status   |
| `RestartMCPServer` | name       | error             | Restart specific server |

### Terminal (`backend/frontend_api_terminal.go`)

| Method           | Parameters            | Returns | Description        |
| ---------------- | --------------------- | ------- | ------------------ |
| `StartTerminal`  | sessionID             | error   | Start PTY terminal |
| `WriteTerminal`  | sessionID, data       | error   | Write to terminal  |
| `ResizeTerminal` | sessionID, cols, rows | error   | Resize terminal    |
| `StopTerminal`   | sessionID             | error   | Stop terminal      |

### Vector (`backend/frontend_api_vector.go`)

| Method                 | Parameters | Returns      | Description         |
| ---------------------- | ---------- | ------------ | ------------------- |
| `GetVectorIndexStatus` | —          | VectorStatus | Get indexing status |

### Prompt (`backend/frontend_api_prompt.go`)

| Method           | Parameters | Returns | Description          |
| ---------------- | ---------- | ------- | -------------------- |
| `OptimizePrompt` | text       | string  | Optimize user prompt |

## Event Protocol

See [event-catalog.md](event-catalog.md) for complete event reference.

### Direction

- **Backend → Frontend**: lifecycle events during task execution (25+ types)
- **Frontend → Backend**: confirmation responses (tool_confirm, ask_user, step_limit)

### Naming Convention

- Session-scoped: `session:${sessionId}:${eventType}`
- Global: bare event name (e.g., `backend:ready`)

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

- Adding a method to FrontendAPI → run `wails build` to regenerate bindings → update `frontend/src/api/` wrapper
- Changing method signature → regenerate bindings → update frontend callers
- Adding new event type → add Go emitter method → add TS type + type guard → add handler in relevant hook
- Renaming event → update both Go emitter AND all frontend subscribers
