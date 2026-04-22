# c0wrk Frontend Specification for Svelte 5 / SvelteKit Migration

> Generated from the React 19 + Vite 6 + Zustand + Tailwind v4 frontend.
> Target stack: **Svelte 5, SvelteKit, Tailwind v4, TypeScript ~5.7**.

---

## Table of Contents

1. [Architecture Overview](#1-architecture-overview)
2. [Wails v2 Integration Contract](#2-wails-v2-integration-contract)
3. [Data Models (TypeScript Types)](#3-data-models-typescript-types)
4. [Backend RPC API Surface](#4-backend-rpc-api-surface)
5. [Event System (Backend → Frontend)](#5-event-system-backend--frontend)
6. [State Management (Stores)](#6-state-management-stores)
7. [Design System & Theming](#7-design-system--theming)
8. [Component Tree & Layout](#8-component-tree--layout)
9. [Component Specifications](#9-component-specifications)
10. [Utility Libraries](#10-utility-libraries)
11. [Third-Party Dependencies](#11-third-party-dependencies)
12. [Build & Dev Configuration](#12-build--dev-configuration)
13. [Migration Notes: React → Svelte 5](#13-migration-notes-react--svelte-5)

---

## 1. Architecture Overview

```
┌──────────────────────────────────────────────────┐
│                  Go Backend (Wails v2)            │
│   desktop/api_*.go  ─  exposed methods on App     │
│   Events: runtime.EventsEmit("name", payload)     │
└───────┬──────────────────────────┬────────────────┘
        │ RPC (Promise-based)      │ Events (pub/sub)
        ▼                          ▼
┌──────────────────────────────────────────────────┐
│               Frontend (Svelte 5 + SvelteKit)     │
│                                                    │
│  wailsjs/go/desktop/App.js   ← generated stubs    │
│  wailsjs/go/models.ts        ← generated types    │
│  wailsjs/runtime/runtime.js  ← event bus + window │
│                                                    │
│  Stores (Svelte 5 runes / writable stores)         │
│  Components (Svelte 5 .svelte files)               │
│  Lib utilities (pure TS, framework-agnostic)       │
└──────────────────────────────────────────────────┘
```

### Key Constraints

- The frontend runs inside a **Wails v2 webview** (WebKit on macOS). There is no server-side rendering; SvelteKit should be used in **SPA mode** (`adapter-static` or equivalent).
- All backend communication goes through `window.go.desktop.App.*` (RPC) and `window.runtime.EventsOn/EventsEmit` (events). There is no HTTP/REST/WebSocket layer.
- The `wailsjs/` directory is **auto-generated** by `wails build` / `wails dev`. Do not hand-edit. The Svelte frontend must consume these same files.

---

## 2. Wails v2 Integration Contract

### 2.1 RPC Calls

All backend methods are exposed as async functions on `window.go.desktop.App`:

```typescript
// Usage pattern (same in Svelte):
const result = await window.go.desktop.App.MethodName(arg1, arg2);
```

The generated stubs in `wailsjs/go/desktop/App.js` re-export these as named functions.

### 2.2 Event Bus

```typescript
// Subscribe (returns unsubscribe function)
const unsub = window.runtime.EventsOn("event_name", (data: unknown) => { ... })

// Emit (frontend → backend)
window.runtime.EventsEmit("event_name", payload)

// Unsubscribe
unsub()  // or: window.runtime.EventsOff("event_name")
```

### 2.3 Runtime Utilities

Available on `window.runtime`:

- `BrowserOpenURL(url)` — opens system browser
- `WindowSetTitle(title)`, `WindowMinimise()`, `WindowMaximise()`, etc.
- `ClipboardGetText()`, `ClipboardSetText(text)`
- `Quit()`, `Hide()`, `Show()`
- `OnFileDrop(callback, useDropTarget)` — drag & drop

### 2.4 Readiness Detection

The Wails runtime injects `window.runtime` and `window.go` at startup. The frontend must check for their existence before using them:

```typescript
function isWailsReady(): boolean {
  return (
    typeof window !== "undefined" &&
    window.runtime !== undefined &&
    window.go?.desktop?.App !== undefined
  );
}
```

---

## 3. Data Models (TypeScript Types)

All types are generated in `wailsjs/go/models.ts`. These are **framework-agnostic** and should be imported as-is.

### 3.1 Namespace `desktop`

| Type                       | Fields                                                                                                                                                                                               |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ConfigResponse`           | `loaded`, `log_level`, `config_migrated`, `config_migration_msg`, `config_errors: string[]`, `llm: ConfigLLMResponse`, `memory: ConfigMemResponse`, `search: ConfigSearchResp`                       |
| `ConfigLLMResponse`        | `active_provider`, `anthropic: ConfigProviderKeyModel`, `gemini: ConfigProviderKeyModel`, `lmstudio: ConfigProviderFull`, `openai_compatible: ConfigProviderFull`, `chatgpt: ConfigProviderKeyModel` |
| `ConfigProviderKeyModel`   | `api_key`, `model`                                                                                                                                                                                   |
| `ConfigProviderFull`       | `base_url`, `api_key`, `model`                                                                                                                                                                       |
| `ConfigMemResponse`        | `database: string`                                                                                                                                                                                   |
| `ConfigSearchResp`         | `provider`, `api_key`                                                                                                                                                                                |
| `LLMSettingsRequest`       | `active_provider`, `api_key`, `base_url`, `model`                                                                                                                                                    |
| `SearchSettingsRequest`    | `provider`, `api_key`                                                                                                                                                                                |
| `SecuritySettingsResponse` | `default_policy`, `tool_policies: Record<string, ToolPolicyResponse>`                                                                                                                                |
| `ToolPolicyResponse`       | `policy`, `blacklist?: string[]`                                                                                                                                                                     |
| `SessionTokensResponse`    | `total_input_tokens`, `total_output_tokens`, `model`, `family`                                                                                                                                       |
| `FileNode`                 | `name`, `path`, `is_dir`                                                                                                                                                                             |
| `ToolInfo`                 | `name`, `description`, `source`, `policy`                                                                                                                                                            |
| `GitStatusEntry`           | `status: string`, `staged: boolean`                                                                                                                                                                  |

### 3.2 Namespace `project`

| Type          | Fields                                                                        |
| ------------- | ----------------------------------------------------------------------------- |
| `ProjectInfo` | `id`, `name`, `workspace_path`, `is_external`, `created_at`, `last_active_at` |

### 3.3 Namespace `session`

| Type          | Fields                                                                                                                                           |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `SessionInfo` | `id`, `project_id`, `name`, `created_at`, `last_active_at`, `archived`, `active`, `total_input_tokens`, `total_output_tokens`, `model`, `family` |
| `ChatMessage` | `id: number`, `session_id`, `role`, `content`, `metadata: number[]` (raw JSON bytes), `created_at`                                               |

### 3.4 Namespace `mcp`

| Type               | Fields                                                                      |
| ------------------ | --------------------------------------------------------------------------- |
| `ServerStatus`     | `name`, `transport`, `connected`, `tool_count`, `tools: string[]`, `error?` |
| `CodeMemoryStatus` | `installed`, `path`                                                         |

### 3.5 Namespace `rtk`

| Type        | Fields                         |
| ----------- | ------------------------------ |
| `RtkStatus` | `installed`, `path`, `version` |

### 3.6 Frontend-Only Types (must be re-created)

```typescript
// Chat message UI type
type MessageType =
  | "user"
  | "assistant"
  | "thinking"
  | "step_done"
  | "tool_call"
  | "tool_result"
  | "tool_confirm"
  | "ask_user"
  | "routing"
  | "reflection"
  | "plan"
  | "error"
  | "thought"
  | "plan_step_start"
  | "plan_step_complete"
  | "retry"
  | "step_retry"
  | "subagent_launch"
  | "subagent_complete"
  | "status"
  | "task_failed_resumable"
  | "task_resumed"
  | "step_limit"
  | "context_compaction";

interface ChatMessageUI {
  id: string;
  sessionId: string;
  type: MessageType;
  content: string;
  metadata?: Record<string, unknown>;
  timestamp: number;
}

// Display item (discriminated union)
type DisplayItemKind =
  | "user"
  | "assistant"
  | "thought"
  | "thought_group"
  | "tool"
  | "tool_confirm"
  | "ask_user"
  | "error"
  | "service"
  | "plan_step"
  | "reflection"
  | "step_finish"
  | "memory_read"
  | "action_placeholder"
  | "resume_action"
  | "step_limit"
  | "context_compaction";

interface DisplayItem {
  kind: DisplayItemKind;
  id: string;
  // ... variant-specific fields per kind
}

// Plan structures
interface PlanItem {
  id: string;
  title: string;
  description?: string;
  summary?: string;
  status: "pending" | "running" | "completed" | "failed";
  duration?: number;
  dependsOn: string[];
}

interface PlanGroup {
  id: number;
  items: PlanItem[];
  progress: number;
  completedCount: number;
  totalCount: number;
}

// Session stats
interface SessionStats {
  routingDomain: string;
  complexity: string;
  attempt: number;
  maxAttempts: number;
}

// Context fill per step
interface ContextFillState {
  fillPercent: number;
  usedTokens: number;
  maxTokens: number;
  status: string;
}

// Vector index status
type VectorIndexStatus = "idle" | "indexing" | "ready" | "reindexing";
```

---

## 4. Backend RPC API Surface

All methods are on `window.go.desktop.App`. Every call returns a `Promise`.

### 4.1 Project Management

| Method          | Signature                                                      | Returns                       |
| --------------- | -------------------------------------------------------------- | ----------------------------- |
| `ListProjects`  | `() => Promise<ProjectInfo[]>`                                 | Sorted by last_active_at desc |
| `CreateProject` | `(name: string, externalPath: string) => Promise<ProjectInfo>` | New project                   |
| `DeleteProject` | `(id: string) => Promise<void>`                                |                               |
| `RenameProject` | `(id: string, name: string) => Promise<void>`                  |                               |
| `SwitchProject` | `(id: string) => Promise<void>`                                |                               |
| `PickDirectory` | `() => Promise<string>`                                        | OS native file picker         |

### 4.2 Session Management

| Method                | Signature                                                         | Returns                       |
| --------------------- | ----------------------------------------------------------------- | ----------------------------- |
| `ListSessions`        | `() => Promise<SessionInfo[]>`                                    | Sorted by last_active_at desc |
| `CreateSession`       | `() => Promise<SessionInfo>`                                      |                               |
| `DeleteSession`       | `(id: string) => Promise<void>`                                   |                               |
| `RenameSession`       | `(id: string, name: string) => Promise<void>`                     |                               |
| `ArchiveSession`      | `(id: string) => Promise<void>`                                   |                               |
| `GetSessionHistory`   | `(id: string) => Promise<ChatMessage[]>`                          | Persisted chat messages       |
| `GetSessionTokens`    | `(id: string) => Promise<SessionTokensResponse>`                  |                               |
| `GetSessionWorkspace` | `(id: string) => Promise<string>`                                 | Workspace path                |
| `UpdateSessionTokens` | `(id, inputTokens, outputTokens, model, family) => Promise<void>` |                               |

### 4.3 Chat / Task Execution

| Method        | Signature                                            | Returns             |
| ------------- | ---------------------------------------------------- | ------------------- |
| `SendMessage` | `(sessionId: string, text: string) => Promise<void>` | Triggers async task |
| `CancelTask`  | `(sessionId: string) => Promise<void>`               |                     |
| `ResumeTask`  | `(sessionId: string) => Promise<void>`               |                     |

### 4.4 Configuration

| Method                 | Signature                                       | Returns     |
| ---------------------- | ----------------------------------------------- | ----------- |
| `GetConfig`            | `() => Promise<ConfigResponse>`                 | Full config |
| `UpdateLLMSettings`    | `(req: LLMSettingsRequest) => Promise<void>`    |             |
| `UpdateSearchSettings` | `(req: SearchSettingsRequest) => Promise<void>` |             |
| `GetLogLevel`          | `() => Promise<string>`                         |             |
| `SetLogLevel`          | `(level: string) => Promise<void>`              |             |
| `ListProviderModels`   | `(provider: string) => Promise<string[]>`       |             |

### 4.5 Security

| Method                   | Signature                                          | Returns |
| ------------------------ | -------------------------------------------------- | ------- |
| `GetSecuritySettings`    | `() => Promise<SecuritySettingsResponse>`          |         |
| `UpdateSecuritySettings` | `(req: SecuritySettingsResponse) => Promise<void>` |         |
| `GetToolList`            | `() => Promise<ToolInfo[]>`                        |         |

### 4.6 MCP Servers

| Method                     | Signature                                                     | Returns |
| -------------------------- | ------------------------------------------------------------- | ------- |
| `GetMCPServers`            | `() => Promise<Record<string, MCPServerConfig>>`              |         |
| `UpdateMCPServers`         | `(servers: Record<string, MCPServerConfig>) => Promise<void>` |         |
| `GetMCPStatus`             | `() => Promise<ServerStatus[]>`                               |         |
| `CheckCodebaseMemoryMCP`   | `() => Promise<CodeMemoryStatus>`                             |         |
| `InstallCodebaseMemoryMCP` | `() => Promise<void>`                                         |         |
| `CheckRtk`                 | `() => Promise<RtkStatus>`                                    |         |
| `InstallRtk`               | `() => Promise<void>`                                         |         |

### 4.7 Workspace / File System

| Method                   | Signature                                                   | Returns            |
| ------------------------ | ----------------------------------------------------------- | ------------------ |
| `ListDirectory`          | `(path: string) => Promise<FileNode[]>`                     |                    |
| `ListDirectoryRecursive` | `(path: string) => Promise<FileNode[]>`                     |                    |
| `WatchDirectory`         | `(path: string) => Promise<void>`                           | Sets up FS watcher |
| `UnwatchDirectory`       | `(path: string) => Promise<void>`                           |                    |
| `ReadFile`               | `(path: string) => Promise<string>`                         | File content       |
| `GetFileDiff`            | `(path: string) => Promise<string>`                         | Unified diff       |
| `GetGitStatus`           | `(path: string) => Promise<Record<string, GitStatusEntry>>` |                    |

---

## 5. Event System (Backend → Frontend)

Events are received via `window.runtime.EventsOn(name, callback)`.

### 5.1 Application-Level Events

| Event Name                    | Payload                                                                 | Description                            |
| ----------------------------- | ----------------------------------------------------------------------- | -------------------------------------- |
| `startup_error`               | `{ message: string, error: string }`                                    | Fatal startup error                    |
| `backend:ready`               | `{ project?: ProjectInfo }`                                             | Backend fully initialized              |
| `projects:loaded`             | `ProjectInfo[]`                                                         | Early project list                     |
| `sessions:loaded`             | `SessionInfo[]`                                                         | Early session list                     |
| `project:created`             | `ProjectInfo`                                                           | New project created                    |
| `project:deleted`             | `{ id: string }`                                                        | Project deleted                        |
| `project:renamed`             | `{ id: string, name: string }`                                          | Project renamed                        |
| `project:switched`            | `{ id: string }`                                                        | Active project changed                 |
| `vector_index:status`         | `{ state, progress, files_indexed, total_files, current_file, branch }` | Vector index progress                  |
| `workspace:tree_changed`      | `undefined`                                                             | File system changed (triggers refresh) |
| `codememory:status`           | `CodeMemoryStatus`                                                      | Codebase memory install status         |
| `codememory:install-progress` | `string`                                                                | Install progress text                  |
| `rtk:status`                  | `RtkStatus`                                                             | RTK install status                     |
| `rtk:install-progress`        | `string`                                                                | Install progress text                  |

### 5.2 Session-Scoped Events

Pattern: `session:{sessionId}:{type}`

| Type Suffix             | Payload Shape                                                                                                              | Description                |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------------- | -------------------------- |
| `routing`               | `{ domain, complexity }`                                                                                                   | Task classification        |
| `step_start`            | `{ step_id }`                                                                                                              | Plan step began            |
| `step_complete`         | `{ step_id, duration_ms }`                                                                                                 | Plan step finished         |
| `step_retry`            | `{ step_id, reason, attempt }`                                                                                             | Step retrying              |
| `thought`               | `{ content, reasoning? }`                                                                                                  | Agent reasoning            |
| `tool_call`             | `{ tool_call_id, tool, args, plan_step_id? }`                                                                              | Tool invocation            |
| `tool_result`           | `{ tool_call_id, tool, result, is_error?, result_len? }`                                                                   | Tool response              |
| `tool_confirm`          | `{ tool, args, confirm_id, reasoning?, tool_msg_id? }`                                                                     | Needs user confirmation    |
| `ask_user`              | `{ questions: AskUserQuestion[] }`                                                                                         | Agent asking user          |
| `step_limit`            | `{ request_id, current_step, max_steps }`                                                                                  | Step limit reached         |
| `reflection`            | `{ summary, suggested_action, root_cause, failure_analysis, action_plan, reasoning, hypotheses[], attempt, max_attempts }` | Agent self-reflection      |
| `plan_generated`        | `{ steps: PlanStep[], progress?, completed_count?, total_count? }`                                                         | New execution plan         |
| `plan_step_start`       | `{ step_id, title?, description?, summary? }`                                                                              | Plan step metadata         |
| `plan_step_complete`    | `{ step_id, status, duration_ms, error? }`                                                                                 | Plan step outcome          |
| `assistant_chunk`       | `{ text }`                                                                                                                 | Streaming text token       |
| `assistant_done`        | `{ content, plan_step_id? }`                                                                                               | Full assistant message     |
| `error`                 | `{ message, plan_step_id? }`                                                                                               | Error occurred             |
| `task_complete`         | `{ summary? }`                                                                                                             | Task finished successfully |
| `task_cancelled`        | `{}`                                                                                                                       | Task cancelled by user     |
| `task_failed_resumable` | `{ message }`                                                                                                              | Task failed but resumable  |
| `task_resumed`          | `{}`                                                                                                                       | Resumed after failure      |
| `retry`                 | `{ attempt, max_attempts, reason }`                                                                                        | Global retry               |
| `service`               | `{ phase, message? }`                                                                                                      | Orchestration phase update |
| `subagent_launch`       | `{ agent_id, role }`                                                                                                       | Sub-agent started          |
| `subagent_complete`     | `{ agent_id }`                                                                                                             | Sub-agent finished         |
| `context_fill`          | `{ step_id, fill_percent, used_tokens, max_tokens, status }`                                                               | Context window usage       |
| `context_compaction`    | `{ step_id, original_tokens, compacted_tokens, ratio }`                                                                    | Memory compaction          |
| `session_tokens`        | `{ total_input_tokens, total_output_tokens, model?, family? }`                                                             | Token count update         |
| `session_renamed`       | `{ name }`                                                                                                                 | Session name auto-updated  |
| `finishing`             | `{}`                                                                                                                       | Task entering finish phase |

### 5.3 Frontend → Backend Events (Emitted)

| Event Name              | Payload                                         | Trigger                    |
| ----------------------- | ----------------------------------------------- | -------------------------- | ---------------------------------- | --------------------------- |
| `tool_confirm_response` | `{ confirm_id, decision: 'allow'                | 'deny' }`                  | User responds to tool confirmation |
| `tool_judge_request`    | `{ confirm_id }`                                | User asks AI to judge tool |
| `ask_user_response`     | `{ answers: Array<{selections, custom_text}> }` | User answers question      |
| `step_limit_response`   | `{ request_id, decision: 'allow_once'           | 'allow_always'             | 'deny' }`                          | User responds to step limit |

---

## 6. State Management (Stores)

The React frontend uses **Zustand** stores. In Svelte 5, these should be implemented as **Svelte stores** (writable/derived) or **runes** (`$state`, `$derived`). Below is the specification for each store with its state shape and actions.

### 6.1 Chat Store

**Purpose:** Manages conversation history, streaming state, and message grouping.

```typescript
// State
messages: Record<string, ChatMessageUI[]>  // sessionId → messages
streamingText: string | null
isThinking: boolean
stepContextFill: Record<string, ContextFillState>
sessionInputTokens: number
sessionOutputTokens: number
sessionModel: string
sessionFamily: string
activityStatus: string | null
isTaskActive: boolean

// Actions
addMessage(sessionId, msg)
updateMessage(sessionId, id, updates)
setMessages(sessionId, msgs)
clearMessages(sessionId)
setStreaming(text)
appendStreamToken(token)
setThinking(thinking)
setStepContextFill(stepId, data)
clearStepContextFill(stepId)
setSessionTokens(input, output, model?, family?)
setActivityStatus(status)
resolveAction(sessionId, messageId, metadataUpdates?)
resolveResumeMessage(sessionId)
setTaskActive(active)
clearSessionUIState()
```

**Critical Logic — `groupMessages(messages: ChatMessageUI[]): GroupedMessages`:**

This pure function transforms a flat message array into a display tree. Key behaviors:

- Consecutive `thought` messages collapse into `thought_group`
- `tool_call` + `tool_result` pair into a single `tool` display item (matched by `tool_call_id`, fallback to composite key `tool:args_hash`)
- Messages with `plan_step_id` nest under `plan_step` containers
- Pending actions (`tool_confirm`, `ask_user`, `task_failed_resumable`, `step_limit`) with `resolved !== true` go into a separate `pendingActions` array
- Memory tools (`read_evidence`, `store_fact`, `search_facts`) render as compact `memory_read` items
- Internal lifecycle messages (`step_done`, `thinking`, `subagent_launch/complete`, `finishing`) are skipped in display

### 6.2 Panel Store

**Purpose:** Manages execution plan visualization.

```typescript
// State
planGroups: PlanGroup[]     // newest first
sessionStats: SessionStats
_planGroupCounter: number

// Actions
addPlanGroup(steps, progress?)
updatePlanItemStatus(stepId, status, duration?)
updateStats(update: Partial<SessionStats>)
resetPanels()
resetPlanStatuses()
rebuildFromEvents(messages: ChatMessageUI[])

// Selectors
selectPlanCompleted(state): number  // from latest group
selectPlanTotal(state): number
```

### 6.3 Project Store

```typescript
// State
projects: ProjectInfo[] | null   // null = not loaded
activeProjectId: string | null

// Actions
setProjects(projects)
addProject(project)
removeProject(id)
setActiveProject(id)
updateProject(id, updates: Partial<ProjectInfo>)
```

Sorting: by `last_active_at` descending.

### 6.4 Session Store

```typescript
// State
sessions: SessionInfo[] | null
activeSessionId: string | null

// Actions
setSessions(sessions)
addSession(session)
removeSession(id)
setActiveSession(id)
updateSession(id, updates: Partial<SessionInfo>)
touchSession(id)   // updates last_active_at to now
```

### 6.5 File Tree Store

```typescript
// State
projectWorkspacePath: string;
rootPath: string;
entries: Record<string, FileNode[]>; // dir path → children
expandedDirs: Set<string>;
loadingDirs: Set<string>;
recursiveEntries: Record<string, FileNode[]>; // full tree (for filtering)
recursiveLoading: boolean;
gitStatus: Record<string, GitStatusEntry>;

// Actions
initForProject(workspacePath);
clearTree();
setEntries(path, nodes);
toggleDir(path);
collapseDir(path);
setLoading(path, loading);
refreshVisibleDirs();
fetchRecursiveTree(rootPath);
clearRecursiveEntries();
fetchGitStatus();
```

**Backend calls:** `ListDirectory`, `ListDirectoryRecursive`, `WatchDirectory`, `UnwatchDirectory`, `GetGitStatus`.

### 6.6 File Viewer Store

```typescript
// State
openFiles: OpenFile[]
activeFilePath: string | null
panelWidth: number          // default: 500, persisted
isCollapsed: boolean        // persisted

interface OpenFile {
  path: string
  name: string
  content: string
  diff: string
  language: string
  isBinary: boolean
  isLoading: boolean
  error: string | null
}

// Actions
openFile(path, name)
closeFile(path)
setActiveFile(path)
closeAllFiles()
setPanelWidth(width)
toggleCollapsed()
refreshFile(path)
refreshAllFiles()
silentRefreshAllFiles()     // no loading spinner
```

**Persistence:** localStorage key `'c0wrk-file-viewer'` stores `{ openFiles (path+name only), activeFilePath, panelWidth, isCollapsed }`. Lazy restoration on load.

### 6.7 UI Store

```typescript
// State
logLevel: "DEBUG" | "INFO" | "WARN" | "ERROR";
sidebarCollapsed: boolean; // persisted

// Actions
setLogLevel(level);
toggleSidebarCollapsed();
setSidebarCollapsed(collapsed);
```

**Persistence:** localStorage key `'c0wrk-sidebar-collapsed'`.

### 6.8 Settings Store

```typescript
// State
open: boolean
activeTab: 'general' | 'llm' | 'search' | 'mcp' | 'security' | 'about'

// Actions
openSettings(tab?)
closeSettings()
setActiveTab(tab)
```

### 6.9 Scroll Store

```typescript
// State
scrollToStep: ((stepId: string) => void) | null

// Actions
setScrollToStep(fn)
```

### 6.10 Vector Index Store

```typescript
// State
status: "idle" | "indexing" | "ready" | "reindexing";
progress: number; // 0–100
filesIndexed: number;
totalFiles: number;
currentFile: string;
branch: string;

// Actions
updateFromEvent(data);
reset();
```

---

## 7. Design System & Theming

### 7.1 Color Palette (One Dark inspired, dark-only)

Defined as Tailwind v4 CSS custom properties in `@theme`:

```css
--color-background: #282c34 --color-foreground: #abb2bf --color-card: #252931
  --color-card-foreground: #abb2bf --color-popover: #252931
  --color-popover-foreground: #abb2bf --color-primary: #528bff
  --color-primary-foreground: #282c34 --color-secondary: #1d2025
  --color-secondary-foreground: #cccccc --color-muted: #1d2025
  --color-muted-foreground: #cccccc --color-accent: #528bff
  --color-accent-foreground: #282c34 --color-destructive: #e06c75
  --color-destructive-foreground: #282c34 --color-border: #1d2025
  --color-input: #1d2025 --color-ring: #528bff --color-success: #98c379
  --color-warning: #d19a66 --color-info: #61afef --color-highlight: #e5c07b;
```

### 7.2 Border Radii

```css
--radius-sm: 0.25rem --radius-md: 0.375rem --radius-lg: 0.5rem
  --radius-xl: 0.75rem;
```

### 7.3 Typography

- Base font size: `14px` (`html { font-size: 14px }`)
- Color scheme: `dark` only
- Nerd Font: `SauceCodePro NF` loaded via `@font-face` from `assets/fonts/SauceCodeProNerdFont-Regular.ttf` — used for file icons (`.nerd-font-icon` class)
- Prose (markdown): custom One Dark variables overriding `@tailwindcss/typography`

### 7.4 Prose / Markdown Theme Overrides

```css
--tw-prose-body: #abb2bf --tw-prose-headings: #e5c07b --tw-prose-links: #61afef
  --tw-prose-bold: #e5c07b --tw-prose-code: #e06c75 --tw-prose-pre-code: #abb2bf
  --tw-prose-pre-bg: #282c34 --tw-prose-bullets: #5c6370 --tw-prose-hr: #1d2025
  --tw-prose-quotes: #5c6370 --tw-prose-quote-borders: #1d2025;
```

### 7.5 Syntax Highlighting

Uses `highlight.js` with Atom One Dark theme. Colors:

- Comment: `#5c6370`
- Keyword: `#c678dd`
- String: `#98c379`
- Number/Type: `#d19a66`
- Function/Link: `#61afef`
- Built-in/Class: `#e5c07b`
- Tag/Deletion: `#e06c75`
- Literal: `#56b6c2`

### 7.6 Custom CSS Classes

| Class                      | Purpose                                          |
| -------------------------- | ------------------------------------------------ |
| `.nerd-font-icon`          | Uses SauceCodePro NF                             |
| `.mermaid-container svg`   | `max-width: 100%; height: auto`                  |
| `.prose-xs`                | 12px base prose size                             |
| `.custom-scrollbar`        | Subtle transparent scrollbar (8px width)         |
| `.no-scrollbar`            | Hidden scrollbar with scroll functionality       |
| `.file-viewer-line`        | Flex row for file viewer lines                   |
| `.file-viewer-line-number` | 3.5rem width, right-aligned, `#5c6370`           |
| `.diff-line-added`         | Green tinted background `rgba(152,195,121,0.15)` |
| `.diff-line-removed`       | Red tinted background `rgba(224,108,117,0.15)`   |
| `.diff-line-modified`      | Light green background `rgba(152,195,121,0.10)`  |
| `.diff-char-added`         | Inline char-level green highlight                |
| `.diff-char-removed`       | Inline char-level red + strikethrough            |

### 7.7 Global Overrides

- All focus rings removed globally (`outline: none !important`)
- Radix dropdown items: custom hover/focus using `color-mix(in srgb, var(--color-muted) 50%, transparent)`
- Default border color: `var(--color-border)` applied to `*`

### 7.8 UI Component Variants (CVA)

**Button variants:** `default`, `destructive`, `outline`, `secondary`, `ghost`, `link`
**Button sizes:** `default` (h-9), `xs` (h-6), `sm` (h-8), `lg` (h-10), `icon` (9x9), `icon-xs` (6x6), `icon-sm` (8x8), `icon-lg` (10x10)

**Badge variants:** `default`, `secondary`, `destructive`, `outline`, `ghost`, `link`

**Tabs list variants:** `default` (bg-muted), `line` (transparent with gap)

---

## 8. Component Tree & Layout

```
App
├── [Banner Layer] (fixed, z-50)
│   ├── CodebaseMemoryBanner
│   ├── RtkBanner
│   └── StartupError (conditional)
│
└── AppLayout
    ├── Sidebar (collapsible, resizable 180–500px, default 300px)
    │   ├── [Header]
    │   │   ├── ProjectDropdown (DropdownMenu)
    │   │   ├── NewProjectButton
    │   │   ├── SettingsButton
    │   │   ├── SessionDropdown (DropdownMenu with search)
    │   │   └── NewSessionButton
    │   ├── [Body]
    │   │   └── WorkspacePanel
    │   │       └── Tabs: Explorer | Git | Semantics
    │   │           └── FileTreePanel (Explorer tab)
    │   │               └── TreeNode (recursive)
    │   │                   └── FileIcon
    │   └── [Footer]
    │       ├── SettingsModal
    │       └── CreateProjectDialog
    │
    ├── ResizeHandle (sidebar ↔ main)
    │
    ├── [Main Content Area] (flex-1)
    │   ├── [no project] → NoProjectEmptyState
    │   ├── [with project]
    │   │   ├── ChatArea
    │   │   │   ├── [Pinned UserMessage] (sticky top)
    │   │   │   └── ChatScrollManager
    │   │   │       └── ChatMessageRenderer
    │   │   │           ├── UserMessage
    │   │   │           ├── AssistantMessage
    │   │   │           ├── ThoughtBlock / ThoughtGroupBlock
    │   │   │           ├── ToolBlock
    │   │   │           ├── PlanStepBlock (recursive children)
    │   │   │           ├── ToolConfirmation
    │   │   │           ├── AskUserPanel
    │   │   │           ├── ResumeActionPanel
    │   │   │           ├── StepLimitPrompt
    │   │   │           ├── ErrorBlock
    │   │   │           ├── ServiceMessage
    │   │   │           ├── ReflectionBlock
    │   │   │           ├── MermaidBlock
    │   │   │           ├── MemoryBlock (inline)
    │   │   │           └── ActivityIndicator
    │   │   ├── PendingActionsBar (sticky bottom)
    │   │   ├── ExecutionPanels (collapsible plan view)
    │   │   │   ├── DAGGraph (SVG)
    │   │   │   └── PlanStepItems
    │   │   └── ChatInput
    │   │
    │   └── ResizeHandle (main ↔ file viewer)
    │
    ├── FileViewerPanel (collapsible, resizable 250–900px, default 500px)
    │   ├── FileViewerTabBar
    │   └── FileViewerContent
    │       ├── SyntaxHighlighted code view
    │       ├── Markdown preview (for .md files)
    │       └── Inline diff overlay
    │
    └── StatusBar
        ├── SessionName + ThinkingSpinner
        ├── RoutingDomain badge
        ├── AttemptCounter
        ├── ContextBadge (model + tokens)
        ├── IndexingStatus
        └── Version label
```

---

## 9. Component Specifications

### 9.1 App (root)

- Wraps everything in a tooltip provider context
- Listens for `startup_error` and `vector_index:status` events
- Renders banner layer + `AppLayout`

### 9.2 AppLayout

- Three-column flex layout: Sidebar | Main | FileViewer
- Sidebar: collapsible (32px collapsed, 300px default), resizable 180–500px
- FileViewer: collapsible (32px collapsed, 500px default), resizable 250–900px
- Both panels use `ResizeHandle` with mouse drag + keyboard arrow keys (10px, Shift+50px)
- Main area shows `NoProjectEmptyState` when no active project

### 9.3 Sidebar

- **Row 1:** Collapse toggle, Project dropdown (with rename/delete context menu), New project btn, Settings btn
- **Row 2:** Session dropdown (with search when >=5 sessions, rename/archive/delete context menu), New session btn
- Active session indicator: green dot when `session.active === true`
- Relative time formatting ("just now", "1m ago", "2h ago", etc.)
- Backend events consumed: `projects:loaded`, `sessions:loaded`, `backend:ready`, `project:created/deleted/renamed/switched`

### 9.4 FileTreePanel

- **Lazy mode (default):** Loads children on directory expand via `ListDirectory`
- **Recursive mode (when filtering):** Loads full tree via `ListDirectoryRecursive`, shows matches + ancestors
- Filter: debounced 300ms, supports glob (picomatch) and regex modes
- Git status badges: staged (info), modified (warning), untracked (success)
- Directory color: highest-priority descendant git status color
- Hidden files (`.` prefix): shown with reduced opacity
- Double-click file: opens in FileViewer
- Listens for `workspace:tree_changed` event to refresh

### 9.5 FileViewerPanel

- Tab bar with scrollable tabs, active tab auto-scrolls into view
- Close button visible on hover (always visible on active tab)
- File dropdown for quick navigation
- Panel collapse toggle button
- Content rendering:
  - Binary files: "Unsupported format" message
  - Markdown: toggle between rendered preview and source
  - Code: syntax highlighting via highlight.js (15 languages registered)
  - Diff overlay: added/removed/modified line backgrounds, char-level diffs
  - Line numbers: 3.5rem column, right-aligned
- **Persistence:** open tabs, active tab, width, collapsed state → localStorage
- Silent refresh on `workspace:tree_changed` (preserves scroll position)

### 9.6 ChatArea

- Fetches session history via `GetSessionHistory` on session change
- Converts history to UI messages via `chatMessageToUI()`, rebuilds panels via `rebuildFromEvents()`
- Pinned last user message at top (max-height: 1/5 container, collapsible with fade overlay)
- Auto-scroll: only if user was at bottom before new content (50px threshold)
- "New activity" banner when scrolled up and new messages arrive

### 9.7 ChatInput

- Auto-resizing textarea (max 6 visible lines)
- Enter to send, Shift+Enter for newline
- Creates session automatically if none active
- Optimistic UI: adds user message before backend confirms
- Cancel button (red square) shown during task execution
- Disabled when task active or no project selected

### 9.8 ChatMessageRenderer

Maps `DisplayItem[]` to components:

| DisplayItem Kind     | Component              | Key Props                                   |
| -------------------- | ---------------------- | ------------------------------------------- |
| `user`               | UserMessage            | content, timestamp                          |
| `assistant`          | AssistantMessage       | content, isStreaming                        |
| `thought`            | ThoughtBlock           | content, reasoning                          |
| `thought_group`      | ThoughtGroupBlock      | thoughts[]                                  |
| `tool`               | ToolBlock              | toolName, args, result, status, source      |
| `tool_confirm`       | ToolConfirmation       | sessionId, metadata                         |
| `ask_user`           | AskUserPanel           | sessionId, metadata                         |
| `resume_action`      | ResumeActionPanel      | sessionId, content, metadata                |
| `step_limit`         | StepLimitPrompt        | sessionId, metadata                         |
| `error`              | ErrorBlock             | content                                     |
| `service`            | ServiceMessage         | variant, content, metadata                  |
| `plan_step`          | PlanStepBlock          | stepId, title, status, children (recursive) |
| `reflection`         | ReflectionBlock        | summary, suggestedAction, rootCause, etc.   |
| `step_finish`        | Compact checkmark line | stepId, duration                            |
| `memory_read`        | MemoryBlock            | tool, content (collapsible)                 |
| `action_placeholder` | ActionPlaceholder      | label (pulsing clock)                       |
| `context_compaction` | Compact indicator      | ratio display                               |

### 9.9 ToolConfirmation

- Shows tool name, formatted args (JSON → key-value)
- Three buttons: Allow Once, Ask Agent (AI judge), Deny
- "Ask Agent" emits `tool_judge_request`, listens for `tool_judge_response` (30s timeout)
- Judge reasoning displayed in warning box
- Resolved state: compact single-line

### 9.10 AskUserPanel

- Renders questions with radio (single-select) or checkbox (multi-select) options
- Star icon for recommended options
- Custom text input per question
- Submit emits `ask_user_response`
- Resolved state: compact answer summary

### 9.11 AssistantMessage

- Toggle between rendered markdown and raw source
- Markdown rendering: GFM, emoji, breaks, slug, autolink headings, external links, sanitize, syntax highlight
- Mermaid diagrams detected by `language-mermaid` code blocks
- Streaming cursor: pulsing 2px inline indicator

### 9.12 PlanStepBlock

- Collapsible container, auto-opens when status=running, auto-closes on completed/failed
- Border color: blue (running), green (completed), red (failed)
- Shows context fill % and token usage when running
- Recursive child rendering via `renderItem` callback

### 9.13 ExecutionPanels

- Collapsible "Execution plan" panel
- Shows DAG graph (SVG) per plan group + step list
- Click step to scroll to it in chat
- Progress: "X/Y completed" in header

### 9.14 DAGGraph

- SVG visualization of plan dependencies
- Constants: LANE_WIDTH=6, ROW_HEIGHT=24, PADDING=4
- Connector types: vertical (straight), fork (right-angle down), merge (right-angle across)
- Uses `computeDAGLayout()` from lib

### 9.15 Settings Modal

- 6 tabs: General, LLM, Search, MCP, Security, About
- General: LogLevelSelector (button group: DEBUG/INFO/WARN/ERROR)
- LLM: ProviderSelector → ProviderConfigForm → ModelSelector, debounced 300ms auto-save
- Search: Provider dropdown (tavily/brave/exa/duckduckgo) + API key (masked), 500ms debounce
- MCP: Server list (collapsible, showing status/tools), Add/Edit/Delete dialog, Codebase Memory + RTK installers
- Security: Tool list grouped by source, per-tool policy buttons (always_allow/always_deny/user_confirm), bash_exec blacklist patterns
- About: Version info, help links

### 9.16 StatusBar

- 8px tall bar at bottom
- Sections: session name + thinking spinner | routing domain | attempt counter | context badge (model + tokens) | indexing status | version "c0wrk v0.1.0"

---

## 10. Utility Libraries

All utilities in `src/lib/` are **pure TypeScript** (no React/framework dependency) unless noted. They can be reused as-is.

### 10.1 `dagLayout.ts` (framework-agnostic)

- `computeDAGLayout(items: DAGItem[]): DAGLayout` — lane assignment + connector computation for DAG visualization
- Exports: `DAGItem`, `DAGNode`, `DAGConnector`, `DAGLayout` types

### 10.2 `formatters.ts` (framework-agnostic)

- `formatDuration(ms: number): string` — "2m30s", "500ms", etc.
- `formatTokenCount(count: number): string` — "1.5M", "150K", "42"

### 10.3 `chatUtils.ts` (framework-agnostic, depends on chatStore types)

- `chatMessageToUI(msg: ChatMessage): ChatMessageUI` — converts persisted backend messages to frontend format
- `roleToType: Record<string, MessageType>` — role→type mapping

### 10.4 `diffParser.ts` (framework-agnostic)

- `parseUnifiedDiff(text): ParseResult` — parses git unified diff
- `classifyLines(total, hunks): LineInfo[]` — maps lines to diff status
- `buildDisplayLines(lines, hunks): DisplayLine[]` — ordered display with char diffs
- `computeCharDiff(old, new): CharDiffPart[]` — word-level diff

### 10.5 `hljsLanguages.ts` (framework-agnostic)

- `registerLanguages()` — registers 15 highlight.js languages
- `detectLanguage(fileName): string` — maps filename to hljs language

### 10.6 `logger.ts` (framework-agnostic)

- `logger.debug/info/warn/error(msg, ...args)`
- `setLogLevel(level: LogLevel)`

### 10.7 `wails.ts` (framework-agnostic, types only)

- All event payload types: `RoutingData`, `ToolCallData`, `ToolResultData`, `ThoughtData`, etc.
- Type guards: `isContextCompactionData()`, `isSessionTokensData()`

### 10.8 `utils.ts` (needs Svelte equivalent)

- `cn(...inputs): string` — `clsx` + `tailwind-merge`. Can be used as-is in Svelte.

### 10.9 `markdownConfig.tsx` (React-specific, must be rewritten)

- `customSchema` (rehype-sanitize schema) — reusable
- `markdownComponents` — React component overrides for react-markdown. Must be replaced with Svelte markdown rendering (e.g., `svelte-markdown` or `mdsvex`).

---

## 11. Third-Party Dependencies

### 11.1 Keep (framework-agnostic)

| Package                   | Usage                                            |
| ------------------------- | ------------------------------------------------ |
| `highlight.js`            | Syntax highlighting in file viewer + code blocks |
| `mermaid`                 | Diagram rendering (lazy-loaded)                  |
| `diff`                    | Word-level diff computation                      |
| `picomatch`               | Glob pattern matching for file tree filter       |
| `clsx`                    | Conditional class composition                    |
| `tailwind-merge`          | Tailwind class conflict resolution               |
| `tailwindcss` v4          | CSS utility framework                            |
| `@tailwindcss/typography` | Prose styling plugin                             |

### 11.2 Replace (React-specific → Svelte equivalents)

| React Package              | Purpose                  | Svelte Equivalent                                 |
| -------------------------- | ------------------------ | ------------------------------------------------- |
| `react`, `react-dom`       | Framework                | `svelte`, `@sveltejs/kit`                         |
| `zustand`                  | State management         | Svelte stores / runes (`$state`, `$derived`)      |
| `radix-ui`                 | Accessible UI primitives | `bits-ui` or `melt-ui`                            |
| `react-markdown`           | Markdown rendering       | `svelte-markdown` or `mdsvex`                     |
| `lucide-react`             | Icons                    | `lucide-svelte`                                   |
| `class-variance-authority` | Variant system           | Can keep CVA or use Svelte-native class switching |
| `@m234/nerd-fonts`         | File type icon glyphs    | Keep (CSS-only, works anywhere)                   |

### 11.3 Remark/Rehype Plugins (keep, framework-agnostic)

- `remark-gfm`, `remark-emoji`, `remark-breaks`
- `rehype-slug`, `rehype-autolink-headings`, `rehype-highlight`, `rehype-external-links`, `rehype-sanitize`

---

## 12. Build & Dev Configuration

### 12.1 Current Vite Config

```typescript
{
  base: './',
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') }
  }
}
```

### 12.2 SvelteKit Equivalent Requirements

- **SPA mode:** Use `@sveltejs/adapter-static` with `fallback: 'index.html'`
- **Base path:** `./` (relative, for Wails webview)
- **Alias:** `$lib` (SvelteKit default) replaces `@/`
- **Tailwind v4:** Use `@tailwindcss/vite` plugin (already Vite-based)
- **wailsjs imports:** Configure as external or alias `wailsjs/` path
- **Output:** `frontend/dist/` (matched in `wails.json`)
- **TypeScript:** ~5.7, strict mode

### 12.3 wails.json Integration

The `wails.json` config references:

- `frontend:dir`: `frontend`
- `frontend:install`: `npm install`
- `frontend:build`: `npm run build`
- `frontend:dev:watcher`: URL for dev server

SvelteKit must produce output compatible with these expectations.

### 12.4 Testing

Current: Vitest with node environment, `src/**/*.test.ts` pattern.

The pure TS utility tests (`dagLayout.test.ts`, `formatters.test.ts`, `diffParser.test.ts`, `chatStore.test.ts`, `chatUtils.test.ts`, `wails.test.ts`) are framework-agnostic and should work with Vitest in SvelteKit as-is.

---

## 13. Migration Notes: React → Svelte 5

### 13.1 State Management

| React (Zustand)                                | Svelte 5                                     |
| ---------------------------------------------- | -------------------------------------------- |
| `const useStore = create((set, get) => {...})` | Module-level `$state()` + exported functions |
| `useStore(selector)`                           | `$derived()` or direct access                |
| `useStore.getState()`                          | Direct module import                         |
| `useStore.subscribe()`                         | `$effect()`                                  |

### 13.2 Component Patterns

| React                             | Svelte 5                                    |
| --------------------------------- | ------------------------------------------- |
| `useState` / `useRef`             | `$state()` / `let` binding                  |
| `useEffect` with deps             | `$effect()` (auto-tracks)                   |
| `useMemo`                         | `$derived()`                                |
| `useCallback`                     | Not needed (no closure identity issues)     |
| `React.memo`                      | Svelte compiles fine-grained reactivity     |
| `useLayoutEffect`                 | `tick()` + `$effect()`                      |
| `ErrorBoundary` (class component) | `<svelte:boundary>` (Svelte 5) or `onError` |
| `children` prop                   | `<slot />` or snippet                       |
| `forwardRef`                      | `bind:this`                                 |
| Context Provider                  | Svelte `setContext` / `getContext`          |

### 13.3 Event Handling

| React                 | Svelte 5                    |
| --------------------- | --------------------------- |
| `onClick`             | `onclick`                   |
| `onChange`            | `oninput` (for text inputs) |
| `onKeyDown`           | `onkeydown`                 |
| `onMouseDown`         | `onmousedown`               |
| `e.stopPropagation()` | Same API                    |

### 13.4 Wails Runtime Integration

The Wails runtime bridge should be a Svelte module store:

```typescript
// src/lib/wails.ts
export function getApi() {
  return window?.go?.desktop?.App;
}

export function getRuntime() {
  return window?.runtime;
}

// Helper for event subscriptions with auto-cleanup
export function onWailsEvent(event: string, handler: (...args: any[]) => void) {
  const runtime = getRuntime();
  if (!runtime) return () => {};
  return runtime.EventsOn(event, handler);
}
```

In components, use `onMount` for subscriptions:

```svelte
<script>
import { onMount } from 'svelte'
import { onWailsEvent } from '$lib/wails'

onMount(() => {
  const unsub = onWailsEvent('event_name', (data) => { ... })
  return unsub
})
</script>
```

### 13.5 Radix UI → Svelte Equivalents

| Radix Component  | Svelte Equivalent (bits-ui) |
| ---------------- | --------------------------- |
| `Dialog`         | `Dialog` from bits-ui       |
| `DropdownMenu`   | `DropdownMenu` from bits-ui |
| `Tooltip`        | `Tooltip` from bits-ui      |
| `Collapsible`    | `Collapsible` from bits-ui  |
| `Tabs`           | `Tabs` from bits-ui         |
| `Separator`      | `Separator` from bits-ui    |
| `Slot` (asChild) | Svelte `<slot>` / snippets  |

### 13.6 Markdown Rendering

Options:

1. **svelte-markdown** — drop-in, supports custom renderers (closest to react-markdown)
2. **mdsvex** — compile-time markdown (better for static content, less suited for dynamic chat)
3. **unified pipeline** — manual: `remark-parse` → `remark-rehype` → `rehype-stringify` → render HTML with `{@html}` + sanitize

Recommended: `svelte-markdown` for chat messages (dynamic), with the same remark/rehype plugin chain.

### 13.7 File Structure Mapping

```
React (current)                    → SvelteKit (target)
────────────────────────────────────────────────────────
src/App.tsx                        → src/routes/+layout.svelte
src/components/layout/AppLayout    → src/routes/+page.svelte (or +layout)
src/components/chat/*.tsx          → src/lib/components/chat/*.svelte
src/components/settings/*.tsx      → src/lib/components/settings/*.svelte
src/components/ui/*.tsx            → src/lib/components/ui/*.svelte
src/stores/*.ts                    → src/lib/stores/*.svelte.ts (or .ts with runes)
src/hooks/*.ts                     → src/lib/actions/*.ts or inline $effect
src/lib/*.ts                       → src/lib/utils/*.ts (keep as-is)
src/constants/*.ts                 → src/lib/constants/*.ts (keep as-is)
src/index.css                      → src/app.css
```

### 13.8 Persistence Pattern

Replace Zustand middleware with direct localStorage calls in `$effect`:

```typescript
// Svelte 5 store with persistence
let sidebarCollapsed = $state(
  JSON.parse(localStorage.getItem("c0wrk-sidebar-collapsed") ?? "false"),
);

$effect(() => {
  localStorage.setItem(
    "c0wrk-sidebar-collapsed",
    JSON.stringify(sidebarCollapsed),
  );
});
```

### 13.9 ResizeObserver / Intersection Observer

Use Svelte `use:` actions:

```svelte
<script>
function resizeObserver(node: HTMLElement, callback: (entry: ResizeObserverEntry) => void) {
  const observer = new ResizeObserver(([entry]) => callback(entry))
  observer.observe(node)
  return { destroy: () => observer.disconnect() }
}
</script>

<div use:resizeObserver={(entry) => height = entry.contentRect.height}>
```

### 13.10 Session Event Hook Migration

The massive `useSessionEvents.ts` hook (26KB) should be split into a Svelte store module that:

1. Exports a function `setupSessionEvents(sessionId: string): () => void` returning cleanup
2. Uses the same event subscription pattern but updates Svelte stores directly
3. Called from `onMount` in the layout component that manages the active session

---

_End of specification._
