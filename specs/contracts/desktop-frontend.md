# Contract: Desktop <-> Frontend

## Boundary Rule

Frontend communicates with Go exclusively through Wails IPC. No direct Go imports. Two channels: RPC (request/response) and Events (push notifications).

## RPC Surface

All methods on `*desktop.App` (promoted from `*backend.FrontendAPI`) are callable from frontend via `window.go.desktop.App.<MethodName>()`.

### Session (`backend/frontend_api_session.go`)

| Method               | Parameters     | Returns                   | Description                         |
| -------------------- | -------------- | ------------------------- | ----------------------------------- |
| `CreateSession`      | —              | SessionInfo               | Create new session (active project) |
| `DeleteSession`      | id             | error                     | Delete session                      |
| `RenameSession`      | id, name       | error                     | Rename session                      |
| `ArchiveSession`     | id             | error                     | Archive/unarchive session           |
| `ListSessions`       | —              | []SessionInfo             | List active project sessions        |
| `GetSessionHistory`  | id             | []ChatMessage             | Get message history                 |
| `GetBlackboardState` | sessionID      | \*BlackboardStateResponse | Get blackboard task state           |
| `SendMessage`        | id, text, mode | error                     | Send user message (async execution) |
| `CancelTask`         | id             | error                     | Cancel running task                 |
| `ResumeTask`         | id             | error                     | Resume failed task                  |

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

| Method              | Parameters               | Returns            | Description                |
| ------------------- | ------------------------ | ------------------ | -------------------------- |
| `SearchVectorStore` | query, topK, filePattern | []VectorStoreEntry | Search/browse vector store |

### Prompt (`backend/frontend_api_prompt.go`)

| Method           | Parameters | Returns                  | Description          |
| ---------------- | ---------- | ------------------------ | -------------------- |
| `OptimizePrompt` | prompt     | \*OptimizePromptResponse | Optimize user prompt |

## Event Protocol

See [event-catalog.md](event-catalog.md) for complete event reference.

### Direction

- **Backend -> Frontend**: lifecycle events during task execution (25+ types)
- **Frontend -> Backend**: confirmation responses (tool_confirm, ask_user, step_limit)

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

- Adding a method to FrontendAPI -> run `wails build` to regenerate bindings -> update `frontend/src/api/` wrapper
- Changing method signature -> regenerate bindings -> update frontend callers
- Adding new event type -> add Go emitter method -> add TS type + type guard -> add handler in relevant hook
- Renaming event -> update both Go emitter AND all frontend subscribers
