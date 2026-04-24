# c0wrk Frontend Specification (React)

Detailed specification for a clean reimplementation of the c0wrk desktop frontend.
Covers the full tech stack, architecture, data model, component hierarchy,
backend API contract, and behavioral requirements.

---

## 1. Tech Stack & Tooling

### 1.1 Core

| Layer            | Technology                     | Version constraint |
| ---------------- | ------------------------------ | ------------------ |
| Runtime          | Wails v2 (desktop, Go backend) | —                  |
| UI framework     | React                          | ^19                |
| Language         | TypeScript                     | ~5.7               |
| Build            | Vite                           | ^6                 |
| CSS              | Tailwind CSS v4                | ^4                 |
| State management | Zustand                        | ^5                 |

### 1.2 UI Primitives

- **shadcn/ui** (new-york style, neutral base color, CSS variables enabled).
  Radix UI primitives used: Dialog, DropdownMenu, Collapsible, Tabs, Tooltip.
- **lucide-react** for icons (~1.7).
- **class-variance-authority** + **clsx** + **tailwind-merge** for component variant composition.

### 1.3 Markdown Rendering

| Package                  | Purpose                       |
| ------------------------ | ----------------------------- |
| react-markdown ^10       | Markdown → React              |
| remark-gfm               | GitHub-flavored markdown      |
| remark-emoji             | Emoji shortcodes              |
| remark-breaks            | Soft line breaks → `<br>`     |
| rehype-highlight         | Syntax highlighting in fences |
| rehype-sanitize          | HTML sanitization             |
| rehype-external-links    | `target="_blank"` for links   |
| rehype-slug              | Heading IDs                   |
| rehype-autolink-headings | Clickable heading anchors     |

### 1.4 Syntax Highlighting

- **highlight.js** (core, ^11) with selective language registration.
  Languages registered: `typescript`, `javascript`, `python`, `go`, `rust`, `ruby`,
  `json`, `yaml`, `bash`, `css`, `html/xml`, `sql`, `markdown`, `diff`, `dockerfile`.
- File extension → language detection utility.

### 1.5 Diagrams

- **mermaid** (^11) lazy-loaded for diagram blocks inside markdown.
  Dark theme applied.

### 1.6 File Tree Icons

- **Nerd Fonts** (SauceCodePro NF) embedded as a local font face.
  Seti icon color palette. Icon mapping covers 30+ file extensions
  and 20+ exact filenames. Directories do not display icons.

### 1.7 Other Libraries

| Package   | Purpose                                       |
| --------- | --------------------------------------------- |
| picomatch | Glob-based file tree filtering                |
| diff      | Character-level (word-level) diff computation |

### 1.8 TypeScript Configuration

- Strict mode enabled.
- `noUncheckedIndexedAccess: true`.
- `noUnusedLocals: true`, `noUnusedParameters: true`.
- ES2020 target, `bundler` module resolution.
- Path alias: `@/*` → `./src/*`.

### 1.9 Vite Configuration

- Base path: `./` (relative, required for Wails asset loading).
- Plugins: `@vitejs/plugin-react`, `@tailwindcss/vite`.
- Path alias mirrors `tsconfig.json`.

### 1.10 Linting

- ESLint 9 flat config.
- `react-hooks/exhaustive-deps`: error.
- `@typescript-eslint/no-explicit-any`: warn.

---

## 2. Design System

### 2.1 Color Theme: One Dark

The application uses a single dark theme inspired by Atom One Dark.
All colors are defined as Tailwind CSS v4 `@theme` custom properties.

| Token                                       | Value     | Semantic role                  |
| ------------------------------------------- | --------- | ------------------------------ |
| `background`                                | `#282c34` | App background                 |
| `foreground`                                | `#abb2bf` | Primary text                   |
| `card` / `popover`                          | `#252931` | Panels, sidebar, dropdowns     |
| `primary`                                   | `#528bff` | Accent, links, active elements |
| `primary-foreground`                        | `#282c34` | Text on primary background     |
| `secondary` / `muted`                       | `#1d2025` | Subtle backgrounds, borders    |
| `secondary-foreground` / `muted-foreground` | `#cccccc` | Subdued text                   |
| `accent`                                    | `#528bff` | Same as primary                |
| `destructive`                               | `#e06c75` | Errors, delete actions         |
| `border` / `input`                          | `#1d2025` | All borders                    |
| `ring`                                      | `#528bff` | Focus ring (overridden off)    |
| `success`                                   | `#98c379` | Success states, added lines    |
| `warning`                                   | `#d19a66` | Warnings, confirmations        |
| `info`                                      | `#61afef` | Informational, links           |
| `highlight`                                 | `#e5c07b` | Headings, bold, emphasis       |

Additional hljs token colors (not Tailwind tokens, used in CSS overrides):

| Token            | Value     |
| ---------------- | --------- |
| Comment          | `#5c6370` |
| Keyword          | `#c678dd` |
| Name/Deletion    | `#e06c75` |
| Literal          | `#56b6c2` |
| String/Addition  | `#98c379` |
| Attribute/Number | `#d19a66` |
| Symbol/Link      | `#61afef` |
| Built-in/Class   | `#e5c07b` |

### 2.2 Typography

- Base font size: `14px` (`html { font-size: 14px }`).
- Color scheme: `dark`.
- Monospace font for file tree icons: `SauceCodePro NF, monospace`.
- Tailwind Typography plugin (`@tailwindcss/typography`) with One Dark prose overrides:
  - `--tw-prose-body`: `#abb2bf`
  - `--tw-prose-headings`: `#e5c07b`
  - `--tw-prose-links`: `#61afef`
  - `--tw-prose-bold`: `#e5c07b`
  - `--tw-prose-code`: `#e06c75`
  - `--tw-prose-pre-bg`: `#282c34`
  - Prose bullets, quotes, counters, hr, borders use theme values.
- Inline code (not inside `<pre>`) gets `background: #1d2025`, `border-radius: 0.25rem`, `padding: 0.125rem 0.25rem`.
- Prose links: no underline by default, underline on hover.
- Compact prose variant: `.prose-xs` at `0.75rem` base (for tooltips).

### 2.3 Border Radii

| Token | Value      |
| ----- | ---------- |
| `sm`  | `0.25rem`  |
| `md`  | `0.375rem` |
| `lg`  | `0.5rem`   |
| `xl`  | `0.75rem`  |

### 2.4 Scrollbars

Custom scrollbar class (`.custom-scrollbar`):

- 8px width/height.
- Transparent track.
- Thumb: `color-mix(in srgb, border 50%, transparent)`, fully rounded, 1px transparent border for padding.

Hidden scrollbar class (`.no-scrollbar`):

- Hides scrollbar across Firefox, WebKit, and IE/Edge while preserving scroll.

### 2.5 Focus Handling

All focus outlines and box-shadows are globally suppressed — including
`:focus-visible` keyboard navigation rings. This is an **intentional
design decision** for this desktop application: the UI conveys interactive
state through color changes, background highlights, and other non-outline
visual feedback. No focus ring should appear on any element under any
circumstance. Component-level `focus:ring-*`, `focus-visible:ring-*`,
and `aria-invalid:ring-*` classes are removed from all shadcn/ui
primitives and variant definitions.

### 2.6 Radix UI Dropdown Overrides

Hover/focus/highlighted states on `[data-radix-dropdown-menu-item]` use
`color-mix(in srgb, muted 50%, transparent)` background, with inherited text color.

---

## 3. Application Architecture

### 3.1 Entry Point

```
main.tsx → StrictMode → ErrorBoundary → App
```

- `App` wraps the entire tree in `<TooltipProvider>`.
- Global banners (CodebaseMemoryBanner, RtkBanner, startup error) render
  in a fixed top overlay (`z-50`, `pointer-events-none` container, individual
  banners are `pointer-events-auto`).
- `App` subscribes to two global (non-session-scoped) events:
  - `startup_error` — displays dismissible error banner.
  - `vector_index:status` — updates vector index store.
- Below the overlay: `<AppLayout />`.

### 3.2 No Router

The app is a single-view application with a panel-based layout.
There is no client-side routing. Navigation is panel selection
(sidebar project/session, file viewer tabs).

### 3.3 Communication with Go Backend

Two mechanisms:

1. **RPC calls** via `window.go.desktop.App.*` methods.
   Async, promise-based. Used for CRUD operations and data fetching.
2. **Real-time events** via `window.runtime.EventsOn` (subscribe) /
   `window.runtime.EventsEmit` (publish).
   Used for streaming updates during task execution.

### 3.4 Event Namespacing

- **Session-scoped**: `session:${sessionId}:${eventType}`.
  All task execution events use this pattern.
- **Global**: bare event names (`startup_error`, `vector_index:status`,
  `projects:loaded`, `sessions:loaded`, `backend:ready`,
  `project:created`, `project:deleted`, `project:renamed`, `project:switched`,
  `workspace:tree_changed`).

### 3.5 State Management

Zustand stores with no middleware. Direct `getState()` calls from event
handlers (outside React render cycle). Store subscriptions inside components
use selector functions for granular re-renders.

**Stores required:**

| Store              | Responsibility                                                                                                                      |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------------------- |
| `chatStore`        | Messages per session, streaming text, thinking flag, activity status, task active flag, per-step context fill, session token counts |
| `planStore`        | Execution plan groups with DAG items, session stats (routing, attempt count)                                                        |
| `sessionStore`     | Session list (sorted by last_active_at), active session ID                                                                          |
| `projectStore`     | Project list (sorted by last_active_at), active project ID                                                                          |
| `fileTreeStore`    | Lazy-loaded directory tree, expanded dirs, recursive entries for search, git status                                                 |
| `fileViewerStore`  | Open files with content/diff/language, tab management, panel width, collapsed state                                                 |
| `ScrollContext`    | React Context + `useScrollContext` hook; holds `scrollToStep` callback for cross-component coordination                             |
| `settingsStore`    | Settings modal open/close state, active tab                                                                                         |
| `uiStore`          | Sidebar collapsed state, log level                                                                                                  |
| `vectorIndexStore` | Vector index status (idle/indexing/ready/reindexing), progress                                                                      |

---

## 4. Data Model

### 4.1 Backend Data Types (from Go)

```typescript
interface ProjectInfo {
  id: string;
  name: string;
  workspace_path: string;
  is_external: boolean;
  created_at: string; // ISO 8601
  last_active_at: string; // ISO 8601
}

interface SessionInfo {
  id: string;
  project_id: string;
  name: string;
  created_at: string;
  last_active_at: string;
  archived: boolean;
  active: boolean; // true when a task is running
  total_input_tokens: number;
  total_output_tokens: number;
  model: string;
  family: string;
}

interface ChatMessage {
  id: number; // DB auto-increment
  session_id: string;
  role: string; // see role-to-type mapping
  content: string;
  metadata: string; // JSON
  created_at: string;
}
```

### 4.2 Frontend Message Model

```typescript
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
  id: string; // semantic ID (not DB ID)
  sessionId: string;
  type: MessageType;
  content: string;
  metadata?: Record<string, unknown>;
  timestamp: number; // epoch ms
}
```

### 4.3 Display Items (Grouped Messages)

The flat `ChatMessageUI[]` array is transformed into a `DisplayItem[]` tree
for rendering. This is a pure function (`groupMessages`) with the following
responsibilities:

1. **Plan step containers**: `plan_step_start` / `plan_step_complete` create
   hierarchical containers that nest child items (tools, thoughts, errors)
   that reference the same `plan_step_id`.
2. **Tool call/result correlation**: `tool_result` messages update their
   matching `tool_call` display item in-place. Matching uses `tool_call_id`
   (preferred) or a composite key `{plan_step_id}:{step}:{call_idx}:{retry_attempt}`.
   Results arriving before their call are buffered.
3. **Thought grouping**: Consecutive `thought` items are collapsed into
   `thought_group` items (single thoughts remain as-is).
4. **Pending action extraction**: Unresolved `tool_confirm`, `ask_user`,
   `step_limit`, and `task_failed_resumable` messages are separated into
   a `pendingActions` array for the sticky actions bar.
5. **Special tool handling**:
   - `subagent` tool calls are skipped (activity visible in plan steps).
   - `finish` tool calls render as compact "Finished step N" markers.
   - Memory tools (`read_evidence`, `read_step_output`, `list_step_outputs`,
     `store_fact`, `search_facts`) render as compact collapsible memory blocks.
6. **Reflection nesting**: Reflections prefer nesting inside their referenced
   plan step, falling back to any open step, then top-level.

Display item kinds (16 total):

| Kind                 | Description                                     |
| -------------------- | ----------------------------------------------- |
| `user`               | User message                                    |
| `assistant`          | Assistant markdown message                      |
| `thought`            | Single reasoning block                          |
| `thought_group`      | Collapsed group of consecutive thoughts         |
| `tool`               | Tool call with optional result                  |
| `tool_confirm`       | Pending tool confirmation action                |
| `ask_user`           | Pending user question action                    |
| `step_limit`         | Pending step limit decision                     |
| `resume_action`      | Pending resume after failure                    |
| `error`              | Error message                                   |
| `service`            | Routing / retry / step_retry / status info      |
| `plan_step`          | Plan step container with children               |
| `reflection`         | Failure reflection with analysis                |
| `step_finish`        | Compact "finished step N" marker                |
| `memory_read`        | Compact memory operation block                  |
| `action_placeholder` | Pulsing placeholder for pending actions in chat |
| `context_compaction` | Context compaction notification                 |

### 4.4 History Conversion

Persisted `ChatMessage` (from `GetSessionHistory`) is converted to
`ChatMessageUI` via:

1. Role → type mapping (e.g., `"tool_call"` → `"tool_call"`,
   `"task_cancelled"` → `"error"`).
2. Content reconstruction from metadata to match live event format
   (e.g., routing messages become `"Domain: X | Complexity: Y"`).
3. Semantic ID generation from metadata fields to enable correct
   cross-referencing in `groupMessages`.

### 4.5 Execution Plan Model

```typescript
interface PlanItem {
  id: string;
  title: string; // short label (summary preferred, description fallback)
  description?: string; // full What-How-Where text
  summary?: string; // 5-7 word label
  status: "pending" | "running" | "completed" | "failed";
  duration?: number; // ms, from plan_step_complete
  dependsOn: string[]; // DAG dependency IDs
}

interface PlanGroup {
  id: string;
  items: PlanItem[];
  progress?: number; // 0.0-1.0
  completedCount?: number;
  totalCount?: number;
}
```

Plan groups are stored newest-first. When a new plan is generated,
it replaces previous groups (only latest plan group is kept active).

---

## 5. Backend API Contract

### 5.1 RPC Methods (`window.go.desktop.App.*`)

#### Projects

| Method                              | Returns         |
| ----------------------------------- | --------------- |
| `CreateProject(name, externalPath)` | `ProjectInfo`   |
| `DeleteProject(id)`                 | `void`          |
| `RenameProject(id, name)`           | `void`          |
| `ListProjects()`                    | `ProjectInfo[]` |
| `SwitchProject(id)`                 | `void`          |

#### Sessions

| Method                    | Returns         |
| ------------------------- | --------------- |
| `CreateSession()`         | `SessionInfo`   |
| `DeleteSession(id)`       | `void`          |
| `ListSessions()`          | `SessionInfo[]` |
| `RenameSession(id, name)` | `void`          |
| `ArchiveSession(id)`      | `void`          |

#### Chat

| Method                         | Returns                                                    |
| ------------------------------ | ---------------------------------------------------------- |
| `SendMessage(sessionId, text)` | `void`                                                     |
| `CancelTask(sessionId)`        | `void`                                                     |
| `GetSessionHistory(sessionId)` | `ChatMessage[]`                                            |
| `GetSessionTokens(sessionId)`  | `{total_input_tokens, total_output_tokens, model, family}` |
| `ResumeTask(sessionId)`        | `void`                                                     |

#### Configuration

| Method                             | Returns                   |
| ---------------------------------- | ------------------------- |
| `GetConfig()`                      | `ConfigResponse`          |
| `GetSecuritySettings()`            | `Record<string, unknown>` |
| `UpdateSecuritySettings(settings)` | `void`                    |

#### Workspace / Files

| Method                           | Returns                                                              |
| -------------------------------- | -------------------------------------------------------------------- |
| `GetSessionWorkspace(sessionId)` | `string` (path)                                                      |
| `ListDirectory(path)`            | `Array<{name, path, is_dir, hidden, gitignored}>`                    |
| `ListDirectoryRecursive(path)`   | `Array<{name, path, is_dir, hidden, gitignored}>`                    |
| `GetGitStatus(path)`             | `Record<string, {status, staged}>` (status: `A`\|`M`\|`R`\|`C`\|`U`) |
| `WatchDirectory(path)`           | `void`                                                               |
| `UnwatchDirectory(path)`         | `void`                                                               |
| `ReadFile(filePath)`             | `string`                                                             |
| `GetFileDiff(filePath)`          | `string` (unified diff)                                              |
| `PickDirectory()`                | `string` (path, OS dialog)                                           |

#### MCP

| Method                       | Returns             |
| ---------------------------- | ------------------- |
| `CheckCodebaseMemoryMCP()`   | `[boolean, string]` |
| `InstallCodebaseMemoryMCP()` | `void`              |

### 5.2 Events: Frontend → Backend

| Event name              | Payload                                                              |
| ----------------------- | -------------------------------------------------------------------- |
| `tool_confirm_response` | `{ confirm_id, decision: 'allow_once' \| 'deny' }`                   |
| `tool_judge_request`    | `{ confirm_id }`                                                     |
| `ask_user_response`     | `{ request_id, answers: [{ id, selected: string[], custom_text }] }` |
| `step_limit_response`   | `{ request_id, response: 'allow_once' \| 'allow_always' \| 'deny' }` |

### 5.3 Events: Backend → Frontend (Global)

| Event name               | Payload shape                                                           |
| ------------------------ | ----------------------------------------------------------------------- |
| `startup_error`          | `{ message: string, error: string }`                                    |
| `vector_index:status`    | `{ state, progress, files_indexed, total_files, current_file, branch }` |
| `projects:loaded`        | `ProjectInfo[]`                                                         |
| `sessions:loaded`        | `SessionInfo[]`                                                         |
| `backend:ready`          | `ProjectInfo[]` (optional pre-emitted data)                             |
| `project:created`        | `ProjectInfo`                                                           |
| `project:deleted`        | `string` (project ID)                                                   |
| `project:renamed`        | `{ id, name }`                                                          |
| `project:switched`       | `ProjectInfo`                                                           |
| `workspace:tree_changed` | (no payload)                                                            |

### 5.4 Events: Backend → Frontend (Session-Scoped)

All prefixed with `session:${sessionId}:`.

| Event suffix            | Payload interface                    | Description                               |
| ----------------------- | ------------------------------------ | ----------------------------------------- |
| `routing`               | `RoutingData`                        | Domain + complexity classification        |
| `step_start`            | `{ step_num }`                       | New agentic step begins                   |
| `step_complete`         | `{ step_num }`                       | Step finished                             |
| `thought`               | `ThoughtData`                        | Reasoning content                         |
| `tool_call`             | `ToolCallData`                       | Tool invocation with args                 |
| `tool_result`           | `ToolResultData`                     | Tool result (correlates via tool_call_id) |
| `tool_confirm`          | `ToolConfirmData`                    | Security confirmation request             |
| `tool_judge_response`   | `{ confirm_id, reasoning?, error? }` | Judge evaluation result                   |
| `ask_user`              | `AskUserData`                        | Multi-question user prompt                |
| `step_limit`            | `StepLimitData`                      | Step limit reached prompt                 |
| `plan_generated`        | `PlanData`                           | Execution plan with steps + DAG deps      |
| `plan_step_start`       | `PlanStepStartData`                  | Plan step begins execution                |
| `plan_step_complete`    | `PlanStepCompleteData`               | Plan step finishes (success/fail)         |
| `assistant_chunk`       | `AssistantChunkData`                 | Streaming text token/accumulated          |
| `assistant_done`        | (no payload)                         | Streaming complete                        |
| `error`                 | `{ error: string }`                  | Error during execution                    |
| `task_complete`         | `TaskCompleteData`                   | Task finished successfully                |
| `task_cancelled`        | (no payload)                         | Task was cancelled                        |
| `retry`                 | `RetryData`                          | Task-level retry                          |
| `step_retry`            | `StepRetryData`                      | Step-level retry                          |
| `service`               | `{ content, phase? }`                | Orchestration status updates              |
| `subagent_launch`       | `SubAgentData`                       | Sub-agent started                         |
| `subagent_complete`     | `SubAgentData + success`             | Sub-agent finished                        |
| `context_fill`          | `ContextFillData`                    | Context window fill percentage            |
| `context_compaction`    | `ContextCompactionData`              | Context was compacted                     |
| `session_tokens`        | `SessionTokensData`                  | Session cumulative token counts           |
| `task_failed_resumable` | `{ message }`                        | Task failed but can be resumed            |
| `task_resumed`          | (no payload)                         | Resumed after failure                     |
| `finishing`             | (no payload)                         | Task is finishing                         |
| `session_renamed`       | `{ new_name }`                       | Session auto-renamed by backend           |
| `reflection`            | (no payload at event level)          | Reflection phase started                  |

#### Event Payload Types

```typescript
interface RoutingData {
  domain: string; // "general" | "code" | "research" | "mixed"
  complexity: string; // e.g., "simple", "moderate", "complex"
}

interface ToolCallData {
  tool_call_id?: string;
  step: number;
  tool: string;
  args: string;
  parsed_args?: Record<string, unknown>;
  plan_step_id?: string;
  source?: string; // "core" or MCP server name
  call_idx?: number; // 0-based for parallel calls
  retry_attempt?: number;
}

interface ToolResultData {
  tool_call_id?: string;
  step: number;
  result_len: number;
  result: string;
  result_preview?: string;
  plan_step_id?: string;
  call_idx?: number;
  retry_attempt?: number;
}

interface ThoughtData {
  step_num: number;
  content: string;
  reasoning?: string;
  plan_step_id?: string;
}

interface PlanData {
  step_count: number;
  steps?: PlanStepData[];
  progress?: number;
  current_step_index?: number;
  completed_count?: number;
  total_count?: number;
}

interface PlanStepData {
  id?: string;
  description: string;
  summary?: string;
  status: string;
  depends_on?: string[];
}

interface ToolConfirmData {
  confirm_id: string;
  tool: string;
  args: string;
  reasoning?: string;
}

interface AskUserData {
  request_id: string;
  questions: Array<{
    id: string;
    question: string;
    options: Array<{ label: string; value: string }>;
    multi_select?: boolean;
    recommended?: string[];
  }>;
}

interface StepLimitData {
  request_id: string;
  current_step: number;
  max_steps: number;
}

interface ContextFillData {
  fill_percent: number;
  used_tokens: number;
  max_tokens: number;
  status: string; // "ok" | "compact" | "warning" | "emergency" | "reject"
  plan_step_id?: string;
  session_input_tokens: number;
  session_output_tokens: number;
  model: string;
  family: string;
}

interface ContextCompactionData {
  before_percent: number;
  after_percent: number;
  plan_step_id?: string;
}

interface SessionTokensData {
  session_input_tokens: number;
  session_output_tokens: number;
  model: string;
  family: string;
}

interface AssistantChunkData {
  content: string;
  accumulated_content?: string; // full text, preferred when present
}

interface TaskCompleteData {
  session_id: string;
  output: string;
  attempt_count?: number;
  routing_decision?: RoutingData;
}

interface SubAgentData {
  step_id: string;
  description?: string;
  success?: boolean;
  duration?: number;
  plan_step_id?: string;
}
```

---

## 6. Layout

### 6.1 Three-Column Layout

```
┌──────────┬───────────────────────────┬──────────────┐
│ Sidebar  │   Main Content Area       │ File Viewer  │
│          │ ┌───────────────────────┐ │              │
│ Project  │ │ Pinned User Message   │ │ Tab Bar      │
│ Selector │ ├───────────────────────┤ │ File Content │
│          │ │ Chat Scroll Area      │ │ (highlighted │
│ Session  │ │ (messages, tools,     │ │  + diff)     │
│ Selector │ │  plans, etc.)         │ │              │
│          │ ├───────────────────────┤ │              │
│ File     │ │ Pending Actions Bar   │ │              │
│ Tree     │ ├───────────────────────┤ │              │
│          │ │ Execution Panels      │ │              │
│          │ ├───────────────────────┤ │              │
│          │ │ Chat Input            │ │              │
│          │ └───────────────────────┘ │              │
│          │ Status Bar                │              │
└──────────┴───────────────────────────┴──────────────┘
```

### 6.2 Panel Dimensions & Resize

| Panel       | Default | Min   | Max   | Collapsed                     |
| ----------- | ------- | ----- | ----- | ----------------------------- |
| Sidebar     | 300px   | 180px | 500px | 32px strip with expand button |
| File Viewer | 500px   | 250px | 900px | 32px strip with expand button |

- Resize handles between panels are 4px wide vertical separators.
- Resize handles support mouse drag and keyboard (arrow keys ±10px, ±50px with Shift).
- Hover/active visual states on resize handles.
- File viewer only appears when files are open.
- Sidebar collapsed state persists via `localStorage`.
- File viewer width and collapsed state persist via `localStorage`.

### 6.3 Empty States

- **No project**: Full-area empty state with icon, description, and "Create Project" button.
- **No session / no messages**: Centered icon + prompt text.
- **No projects in sidebar**: Sidebar body shows icon + "New Project" button.
- **Projects loading**: Empty sidebar body (no spinner).

---

## 7. Sidebar

### 7.1 Structure

Two header rows + body area:

1. **Row 1**: Collapse button | Project selector dropdown | New Project button | Settings button.
2. **Row 2** (visible when project selected): Session selector dropdown | New Session button.
3. **Body**: Workspace panel (file tree).

### 7.2 Project Selector

- Dropdown listing all projects, sorted by `last_active_at`.
- Active project shows a checkmark.
- Each project has a context menu (nested dropdown) with: Rename, Delete.
- Bottom item: "New Project..." opens the create project dialog.
- Switching projects calls `SwitchProject` RPC, reloads sessions.
- Deleting the active project auto-selects the next available project.

### 7.3 Session Selector

- Dropdown listing active sessions (non-archived), then archived sessions
  in a separate section with "Archived" header.
- Active session shows a checkmark.
- Sessions with `active: true` show a green dot indicator.
- Each session has a context menu: Rename, Archive/Unarchive, Delete.
- **Search**: When total session count >= 5, a search input appears at the
  top of the dropdown. Filters both active and archived sessions by name
  (case-insensitive substring). Search state resets on dropdown close.
- Sessions sorted by `last_active_at` (most recent first).
- Bottom item: "New Session..." creates a new session.

### 7.4 Inline Rename

- Triggered from context menu. Replaces the header area with an auto-focused
  text input.
- Enter commits, Escape cancels, blur commits.
- Works for both projects and sessions.

### 7.5 Project and Session Data Loading

- On mount, attempt to load projects immediately.
- Subscribe to `backend:ready` event (for late startup). If data is
  pre-emitted with the event, use it directly. Otherwise fetch.
- Subscribe to `projects:loaded` and `sessions:loaded` for early data
  delivery before full backend initialization.
- Subscribe to `project:created`, `project:deleted`, `project:renamed`,
  `project:switched` for real-time sync.
- When active project changes, reload sessions list.
- Stale response handling: use a fetch counter ref to discard
  out-of-order responses.

### 7.6 Create Project Dialog

- Modal dialog with:
  - Project name input (required).
  - Workspace type toggle: internal (default) vs external.
  - External: shows a directory picker (calls `PickDirectory` RPC) with path display.
- Submit calls `CreateProject`, adds to store, switches to new project.

---

## 8. Workspace Panel (File Tree)

### 8.1 Structure

- Tabbed panel with icon-only tab triggers and tooltips:
  - **Explorer** tab (active, implemented): File tree.
  - **Git** tab (placeholder/TBD).
  - **Semantics** tab (placeholder/TBD).

### 8.2 File Tree

- **Lazy loading**: Directories load children on expand via `ListDirectory` RPC.
- **Root**: Determined from the active project's `workspace_path` (`ProjectInfo.workspace_path`).
  The tree loads when `activeProjectId` changes, independent of session selection.
- **Empty state**: When no project is selected, shows "Select a project to browse files".
  When a project is selected but the tree is still loading, shows "Loading workspace...".
- **Directory watching**: `WatchDirectory` on root path; cleanup via `UnwatchDirectory`.
  `workspace:tree_changed` event triggers tree refresh.
- **Hidden and gitignored entries**: Files and directories whose names start with `.` (or have the OS hidden attribute) and entries matched by `.gitignore` are **not excluded** from the tree. They are displayed with a subdued comment text color (`text-hljs-comment`).
  Git status coloring takes precedence over the subdued color when both apply.

### 8.3 Filter Bar

- Text input at the top of the file tree.
- Two filter modes (toggle button):
  - **Glob** (default): Uses picomatch for pattern matching.
  - **Regex**: Standard regex matching.
- Debounced at 300ms.
- When filter is active:
  - Fetches recursive directory listing via `ListDirectoryRecursive`.
  - Displays flat filtered results (no tree nesting).
- When filter is empty: shows normal lazy-loaded tree.

### 8.4 Tree Node Rendering

- Each node shows: expand/collapse chevron (dirs only) | file icon (Nerd Font, files only) | name.
- **Directories**: Click to expand/collapse. Lazy-loads children.
- **Files**: Double-click opens in file viewer.
- Nesting indicated by left padding per depth level.

### 8.5 Git Status Integration

- Git status fetched via `GetGitStatus` RPC on tree root.
- Supported statuses: `A` (added/untracked), `M` (modified), `R` (renamed),
  `C` (copied), `U` (unmerged/conflict). For renames and copies the backend
  extracts the destination path from the porcelain `orig -> dest` format.
- Status coloring on file names:
  - Staged (any status): `info` color.
  - Modified / Renamed / Copied / Unmerged (unstaged): `warning` color.
  - New/untracked: `success` color.
- **Inline status indicator**: Each file with a git status displays a bold
  status letter (`A`, `M`, `R`, `C`, or `U`) right-aligned in the tree row
  with 24px (`pr-6`) right padding, colored the same as the file name.
- Status propagates to parent directories via `propagateGitStatus`.
  Propagated directories display a bold bullet (`\u2022`) instead of a
  status letter, using the same color. A `Set<string>` of propagated paths
  is returned alongside the augmented status map so the renderer can
  distinguish real file statuses from inherited directory indicators.

### 8.6 Hidden and Gitignored Entries

- **Hidden**: Files and directories whose names start with `.` (or carry the OS hidden attribute) are flagged as `hidden` by the backend.
- **Gitignored**: Entries matched by `.gitignore` are flagged as `gitignored` by the backend. These entries are **included** in both flat and recursive listings (the `.git` directory itself remains excluded).
- **Visual treatment**: Both `hidden` and `gitignored` entries render their file names with the comment theme color (`text-hljs-comment`).
- **Precedence**: Git status coloring (staged, modified, untracked) takes precedence over the subdued color. A hidden or gitignored file that also has a git status is colored according to its git status.

### 8.7 File Icons

- Nerd Font icon mapping via a backend resolver (files only; directories have no icon).
- Each icon has a seti-palette color.

---

## 9. Chat Area

### 9.1 History Loading

- On session change, call `GetSessionHistory` to load persisted messages.
- Convert via `chatMessageToUI` (role mapping, content reconstruction, semantic ID generation).
- Rebuild panel store from history via `rebuildFromEvents`.
- Show error banner on history load failure (non-blocking).

### 9.2 Pinned User Message

- The **last** user message in the conversation is pinned at the top of
  the chat area in a sticky header with blur backdrop.
- Max height: 1/5 of the chat container height.
- If content overflows max height:
  - Show a gradient fade overlay at the bottom.
  - Click expands to show full content.
  - When expanded, click collapses back.
- Container height tracked via `ResizeObserver`.
- The pinned message is **excluded** from the main message list to avoid duplication.

### 9.3 Smart Auto-Scroll

- Auto-scroll is only active when the user was at the bottom of the chat
  (within 50px threshold) before new content arrived.
- Scroll state tracked using **previous** scroll measurements
  (before new DOM content rendered).
- Scroll-to-bottom performed synchronously in `useLayoutEffect` (before paint).
- When user has scrolled up and new content arrives:
  - A "New activity" banner appears as a sticky pill at the bottom of the viewport.
  - Clicking the banner scrolls to bottom and dismisses itself.
  - Banner auto-dismisses when user manually scrolls to bottom.

### 9.4 Scroll-to-Step

- The execution panel registers a `scrollToStep` callback via `scrollStore`.
- Clicking a plan step in the execution panel scrolls the chat to the
  corresponding `[data-step-id]` element (last match, for retries).
- Uses `scrollIntoView({ behavior: 'smooth', block: 'start' })`.

### 9.5 Message Rendering

- Each display item is wrapped in an `ErrorBoundary` with a compact fallback.
- Streaming assistant text is rendered as a separate `AssistantMessage`
  below the main item list with `isStreaming` flag.
- An `ActivityIndicator` renders at the very bottom of the message list.

### 9.6 Activity Indicator

- Shows when `activityStatus` is non-null in chatStore.
- Animated pulsing dot + status text (e.g., "Thinking...", "Running tool: X...",
  "Generating response...").

---

## 10. Chat Input

### 10.1 Textarea Behavior

- Auto-resizing textarea with max visible height of 6 lines.
- Overflow switches from `hidden` to `auto` when line count exceeds max.
- `Enter` sends message, `Shift+Enter` inserts newline.
- Reset height after send.

### 10.2 Send Flow

1. If no active session, auto-create one via `CreateSession`.
2. Optimistically add user message to chat store.
3. Touch session to move it to top of list.
4. Mark task as active (disables input).
5. Call `SendMessage` RPC.
6. On RPC error: show error in chat, restore input.
7. On session creation error: restore text for retry.

### 10.3 States

- **No project**: Input disabled, placeholder "Select or create a project to start".
- **Task active**: Input disabled, placeholder "Session is processing...".
- **Cancel button**: Shown when task is active (thinking or processing).
  Calls `CancelTask` RPC. Styled with destructive color.
- **Send button**: Styled with `success` color. Icon: Play.
  Disabled when input empty or input disabled.

---

## 11. Assistant Messages

### 11.1 Markdown Rendering

Full markdown pipeline:

- remark: gfm, emoji, breaks.
- rehype: slug, autolink-headings (wrap behavior), highlight,
  external-links (target blank, noopener noreferrer), sanitize (custom schema).

### 11.2 Custom Sanitize Schema

Extends default schema to allow:

- `className` on `code`, `pre`, `span`, `div`.
- `style` on `div` (for mermaid).
- `input` elements with `type`, `checked`, `disabled` (for task lists).

### 11.3 Code Blocks

- Language label displayed in top-right corner of code blocks.
- Language extracted from `className` on `<code>` element (pattern: `language-*`).

### 11.4 Mermaid Diagrams

- Code blocks with language `mermaid` render as diagrams.
- Mermaid is lazy-loaded (dynamic import) on first use.
- Dark theme configuration.
- Rendered in a container with `max-width: 100%; height: auto`.

### 11.5 Streaming Cursor

- When `isStreaming` is true, a blinking cursor character is appended after content.

### 11.6 Raw/Rendered Toggle

Not present on assistant messages in chat — only on file viewer for markdown files.

---

## 12. Tool Blocks

### 12.1 Display

- Collapsible block with a header row showing:
  - Status icon:
    - Running: spinner (animate-spin).
    - Success: checkmark (success color).
    - Error: X icon (destructive color).
    - Awaiting confirmation: alert triangle (warning color).
  - Tool name in monospace.
  - MCP badge: if `source` is present and not `"core"`, show the server name in a badge.
- Collapsed by default. Expand to see args and result.

### 12.2 Args/Result Display

- Arguments shown as monospace pre-formatted text.
  If `parsedArgs` available, show as formatted key-value pairs.
  Otherwise show raw JSON string.
- Result shown similarly when available.
- If content exceeds 200 characters, truncated with "Show more" / "Show less" toggle.
- Result length badge shown when > 500 chars.

---

## 13. Thought Blocks

### 13.1 Single Thought

- Collapsible block with 500-character preview.
- Header: "Reasoning" label.
- If `reasoning` field is present, shown separately from `content`.

### 13.2 Thought Groups

- When consecutive thoughts occur, they collapse into a single block
  labeled "Reasoning (N)" where N is the count.
- Expandable to show all individual thoughts.

---

## 14. Plan Step Blocks (Inline)

### 14.1 Container Behavior

- Auto-opens when status becomes `running`.
- Auto-closes when status becomes `completed` or `failed`.
- Shows step number, title, status icon, duration (when completed).
- If `isRetry` flag is set, shows a retry indicator.
- Error message displayed when step fails with an error.
- Context fill percentage shown per step (from `stepContextFill` in chatStore).

### 14.2 Children

- Nested display items rendered recursively inside the plan step container.
- Same rendering logic as top-level items.

### 14.3 Data Attribute

- Each plan step block sets `data-step-id` attribute for scroll-to-step navigation.

---

## 15. Execution Panels

### 15.1 Collapsible Panel

- Renders below the chat area, above the input.
- Only visible when a plan exists and a session is active.
- Collapsible header: "Execution plan" with completed/total counter.
- Chevron toggle (show on hover).

### 15.2 Plan Content

- DAG graph (SVG) on the left.
- Plan items list on the right.
- Each item shows status icon + title.
- Tooltip with full description on hover (only if description differs from title).
- Click on an item triggers scroll-to-step in the chat area.

### 15.3 DAG Graph

- SVG visualization using a greedy lane-allocation algorithm.
- Layout constants: lane width 6px, row height 24px, padding 4px.
- Node indicators: circles (r=2) filled with muted-foreground.
- Connector types:
  - **Vertical**: straight line between nodes in the same lane.
  - **Fork**: L-shaped path (horizontal from parent, curve, vertical to child).
  - **Merge**: L-shaped path (vertical from parent, curve, horizontal to child).
- Curves use quadratic Bezier with radius = lane_width / 2.

---

## 16. Pending Actions Bar

### 16.1 Purpose

Sticky bar between the chat scroll area and the input.
Shows unresolved interactive prompts that require user response.

### 16.2 Action Types

#### Tool Confirmation

- Warning-styled panel (warning border + background).
- Shows: tool name, formatted JSON args.
- Three buttons: "Allow Once", "Ask Agent", "Deny".
- "Ask Agent" triggers a judge evaluation:
  - Emits `tool_judge_request` event.
  - Shows loading spinner.
  - Listens for `tool_judge_response` on session channel.
  - 30-second timeout with error message.
  - Judge reasoning displayed in amber warning box.
  - "Ask Agent" button disabled after evaluation.
- On resolve:
  - Emits `tool_confirm_response`.
  - Updates linked `tool_call` message to remove `awaiting_confirmation`.
  - Marks confirmation message as resolved.
  - Shows compact confirmed/denied line in place of panel.

#### Ask User (Multi-Question)

- Primary-styled panel (primary border + background).
- Renders each question with:
  - Question text.
  - Option list with radio (single-select) or checkbox (multi-select) indicators.
  - Recommended options marked with a star icon.
  - Custom text input ("Or type your own answer...").
- Enter key in custom input submits if any answer selected.
- Submit button enabled when at least one question has a selection or custom text.
- On submit:
  - Emits `ask_user_response` with structured answers.
  - Marks message as resolved.
  - Shows compact "Answered: ..." summary line.

#### Step Limit Prompt

- Shows current step / max steps.
- Three buttons: "Allow Once", "Allow Always", "Deny".
- Emits `step_limit_response`.

#### Resume Action

- Shows failure message.
- Resume button calls `ResumeTask` RPC.
- Resolves the `task_failed_resumable` message.

### 16.3 Lightweight Extraction

Pending actions are extracted via a dedicated lightweight function
(`extractPendingActions`) that scans messages for unresolved action types
without running the full `groupMessages` pipeline.

---

## 17. Reflection Block

- Displays failure analysis from the orchestrator's reflection system.
- Shows:
  - Summary (always visible).
  - Suggested action badge: "retry" (info), "replan" (warning), "abort" (destructive).
  - Collapsible details section:
    - Root cause.
    - Failure analysis.
    - Action plan.
    - Hypotheses (bulleted list).
    - Reasoning.
  - Attempt counter: "Attempt N/M".

---

## 18. Service Messages

- Compact inline messages for routing, retry, step_retry, and status events.
- Routing: Shows domain label and complexity star rating.
- Retry/step_retry: Shows attempt count.
- Status: Shows orchestration phase text.
- Domain labels: `general`, `code`, `research`, `mixed`.
- Complexity shown as star rating (e.g., 1-5 stars).

---

## 19. File Viewer

### 19.1 Panel Structure

- Tab bar at top + content area below.
- Only renders when files are open and panel is not collapsed.

### 19.2 Tab Bar

- Horizontally scrollable tab strip.
- Each tab shows: file icon | file name (truncated to 120px) | close button.
- Close button: visible on hover (inactive tabs) or always visible with reduced opacity (active tab).
- Active tab has distinct background.
- Full file path shown in tooltip.
- When > 1 file open: dropdown button to list all open files.
- Collapse button (PanelRightClose icon) at the right end.
- Tab click activates file and scrolls tab into view.
- Closing the active tab activates the adjacent tab (right preferred, then left).

### 19.3 File Content Display

- **Loading**: Centered spinner.
- **Error**: Centered error message in destructive color.
- **Binary**: "Unsupported file format" message.
- **Code files**: Syntax-highlighted via highlight.js.
  - Full content highlighted, then HTML split into per-line segments
    (preserving tag balance across lines).
  - Line numbers in a fixed-width column (3.5rem), right-aligned, non-selectable.
  - Content in a `white-space: pre` column.
- **Markdown files**: Toggle between rendered preview and raw source view.
  - Preview uses same ReactMarkdown pipeline as assistant messages.
  - Source uses syntax-highlighted code view.
  - Toggle button in a toolbar above the content.

### 19.4 Diff Highlighting

When `GetFileDiff` returns a non-empty diff:

1. Parse unified diff format into structured hunks.
2. Build display lines including removed lines and character-level diffs.
3. Line types and their visual treatment:
   - **Normal**: No background change.
   - **Added**: Green background (rgba success 15%), green line number.
   - **Removed**: Red background (rgba destructive 15%), red line number, no line number value.
   - **Modified**: Light green background (rgba success 10%), green line number,
     inline character-level diff:
     - Added characters: stronger green background (rgba success 35%), rounded.
     - Removed characters: red background (rgba destructive 35%), strike-through, rounded.

### 19.5 Auto-Refresh

- Listens for `workspace:tree_changed` event.
- Silently refreshes all open files (content + diff) without loading spinners.
- Preserves scroll position during refresh.

### 19.6 Persistence

Via `localStorage` (key: `c0wrk-file-viewer`):

- Open file paths and names (for tab restoration).
- Active file path.
- Panel width.
- Collapsed state.

On startup, persisted tabs are lazily re-opened (each loads content async).

### 19.7 Binary Detection

File content checked for null bytes in the first 8KB.
If found, marked as binary and content not displayed.

### 19.8 Language Detection

File extension mapped to highlight.js language identifier.
Falls back to `highlightAuto` if explicit language highlighting fails.

---

## 20. Status Bar

Fixed 32px bar at the bottom of the main content area.
Displays (left to right):

1. **Thinking indicator**: Spinner when agent is thinking.
2. **Session name**: Truncated, muted text.
3. **Routing domain badge**: Shows current domain (general/code/research/mixed/idle).
4. **Attempt counter**: "Attempt N/M".
5. **Context badge**: Model name, family, input/output token counts.
   Fetches configured model on mount.
6. **Vector index status**: See section 21.
7. **Spacer**.
8. **Version string**: "c0wrk v0.1.0".

Items separated by vertical separators.

---

## 21. Vector Index Status

### 21.1 States

| State        | Visual                                        |
| ------------ | --------------------------------------------- |
| `idle`       | Not shown                                     |
| `indexing`   | Animated ping icon + "Indexing..." + progress |
| `reindexing` | Spinner icon + "Updating..." + progress.      |
| `ready`      | Checkmark icon + "Index ready"                |

### 21.2 Progress

- Progress bar showing percentage.
- File count: "N/M files".

---

## 22. Warning Banners

### 22.1 Codebase Memory Banner

- Checks via `CheckCodebaseMemoryMCP` on mount.
- If not installed: yellow warning bar with "Install" action button.
- Install calls `InstallCodebaseMemoryMCP`.
- Dismissible per browser session (`sessionStorage`).

### 22.2 RTK Banner

- Similar pattern for RTK (Runtime Toolkit) availability check.
- Dismissible per browser session (`sessionStorage`).

### 22.3 Startup Error Banner

- Red error bar displayed when `startup_error` event received.
- Shows message + error detail in monospace.
- Dismissible (component state).

---

## 23. Settings Modal

- Modal dialog with tabbed interface.
- Tabs: General, LLM, Search, MCP, Security, About.
- Opened via settings button in sidebar or keyboard shortcut.
- State managed by `settingsStore` (active tab preserved).

---

## 24. Error Handling

### 24.1 Error Boundary

- Class component error boundary wrapping individual display items
  and markdown renderers.
- Configurable fallback prop.
- Prevents single component crash from breaking the entire UI.

### 24.2 Event Data Validation

- All event handlers use type guard functions to validate incoming data
  before processing.
- Pattern: `function isXData(data: unknown): data is XType` checking
  for required fields.
- Invalid data silently ignored (no crash, logged at debug level).

### 24.3 Stale Response Handling

- Async operations that depend on active session/project use counter refs
  to detect and discard stale responses.
- Pattern: increment counter before fetch, check counter matches after
  response arrives.

---

## 25. Session Event Handler

### 25.1 Lifecycle

- Subscribes to all session-scoped events when `sessionId` changes.
- Unsubscribes on cleanup (effect teardown).
- Uses a `mounted` flag to prevent updates after unmount.
- Checks `isActiveSession()` before updating global UI state
  (streaming text, thinking flag, activity status).

### 25.2 Session Reset

On session change:

1. Clear previous session's UI state (streaming, thinking, activity, task active,
   context fill, token counts).
2. Reset panel data.
3. Load persisted session token totals from `GetSessionTokens`.

### 25.3 Event Handler Pattern

Each handler:

1. Validates data with type guard.
2. Optionally updates activity status (if active session).
3. Adds/updates message in chat store.
4. Updates panel store where applicable.

### 25.4 Streaming Text

- `assistant_chunk`: If `accumulated_content` is present, set streaming text
  directly (preferred). Otherwise append delta to existing streaming text.
- `assistant_done`: Flush streaming text into a permanent assistant message,
  then clear streaming state.

---

## 26. Utilities

### 26.1 Formatters

- `formatDuration(ms)`: Converts milliseconds to human-readable
  (e.g., "1.2s", "3m 45s").
- `formatTokenCount(count)`: Formats token counts with K/M suffixes.
- `formatRelativeTime(dateStr)`: Relative time display
  ("just now", "5m ago", "3h ago", "2d ago", then date).

### 26.2 Logger

- Level-based logger: `debug`, `info`, `warn`, `error`, `silent`.
- Log level configurable via `uiStore`.

### 26.3 cn() Utility

Standard `clsx` + `tailwind-merge` composition utility.

---

## 27. Persistence Summary

| What                   | Storage          | Key                       |
| ---------------------- | ---------------- | ------------------------- |
| File viewer state      | `localStorage`   | `c0wrk-file-viewer`       |
| Sidebar collapsed      | `localStorage`   | `c0wrk-sidebar-collapsed` |
| Codebase Memory banner | `sessionStorage` | per-session dismissal     |
| RTK banner             | `sessionStorage` | per-session dismissal     |

---

## 28. Resize Handle

- 4px wide vertical separator between panels.
- Visual states: default (transparent), hover (border highlight), active (primary color).
- Mouse drag handling with `mousemove`/`mouseup` on `document`.
- Keyboard support: Left/Right arrow keys adjust by ±10px, ±50px with Shift.
- Cleanup on unmount.
- Accepts `onMouseDown` callback (for drag) and `onResize` callback (for keyboard).
- Min/max constraints enforced by the parent layout.
