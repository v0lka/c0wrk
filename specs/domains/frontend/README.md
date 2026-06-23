# Frontend

## Purpose

React 19 application providing the user interface for c0wrk: chat interaction, plan visualization, file viewing, and workspace management. Communicates with Go backend exclusively via Wails IPC.

## Key Files

- `frontend/src/App.tsx` — root component
- `frontend/src/stores/` — Zustand state management (12 stores)
- `frontend/src/hooks/` — custom React hooks (event handlers, data loading)
- `frontend/src/api/` — backend RPC wrapper layer
- `frontend/src/lib/` — utilities (fuzzyMatch, parseReferences, markdown config, local file link detection, CodeMirror extensions)
- `frontend/src/components/` — UI component tree
- `frontend/src/types/` — TypeScript type definitions
- `frontend/src/index.css` — design tokens (Tailwind v4 @theme)

## Core Types

```typescript
// SessionInfo — session metadata from backend
interface SessionInfo {
  id: string
  project_id: string
  name: string
  created_at: string
  last_active_at: string
  archived: boolean
  active: boolean
  total_input_tokens: number
  total_output_tokens: number
  model: string
  family: string
}

// ProjectInfo — project metadata from backend
interface ProjectInfo {
  id: string
  name: string
  workspace_path: string
  is_external: boolean
  is_no_project: boolean
  created_at: string
  last_active_at: string
}

// ProjectSwitchState — persisted per-project UI switch state
interface ProjectSwitchState {
  project_id: string
  saved_session_id: string
  open_tabs: string[]
  active_file: string
  updated_at: string
}

// FileEntry — file tree entry from backend
interface FileEntry {
  name: string
  path: string
  is_dir: boolean
  icon?: string
  icon_color?: string
  hidden?: boolean
  gitignored?: boolean
}
```

Note: Wails auto-generates TypeScript types from Go structs using snake_case JSON field names. Git status is stored separately in `fileTreeStore.gitStatus` (a `Record<string, GitStatusEntry>` keyed by absolute path), not as a field on each file entry.

## Stack

- React 19 + TypeScript ~5.7
- Vite 6 (build tool)
- Tailwind CSS v4 (utility-first styling)
- Zustand 5 (state management)
- shadcn/ui + Radix UI (component primitives)
- lucide-react (icons)
- CodeMirror 6 (file viewer + chat input editor, with Markdown mode and custom extensions)
- react-markdown 10 + remark/rehype plugins (markdown rendering)
- highlight.js 11 (syntax highlighting in rendered messages)
- Mermaid 11 (lazy-loaded diagrams)

## Layout

Three-column panel layout (no router, single-page app):

```
┌──────────────────────────────────────────────────────────────────┐
│  Sidebar (300px)  │  Main Chat Area  │  File Viewer (500px)      │
│  collapsible→40px │                  │  collapsible→40px         │
│                   │                  │  (only when files open)   │
│  ┌─────────────┐  │  ┌────────────┐  │  ┌─────────────────────┐ │
│  │ Project sel │  │  │ Pinned msg │  │  │ Tab bar             │ │
│  │ Session sel │  │  │ Message    │  │  │ File content        │ │
│  │ File tree   │  │  │ list       │  │  │ (syntax highlight   │ │
│  │             │  │  │            │  │  │  or diff view)      │ │
│  │             │  │  │ Activity   │  │  │                     │ │
│  │             │  │  │ indicator  │  │  │                     │ │
│  │             │  │  ├────────────┤  │  │                     │ │
│  │             │  │  │ Chat input │  │  │                     │ │
│  └─────────────┘  │  └────────────┘  │  └─────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

Resize handles (4px) between panels. Panel states persisted via localStorage.

### Sidebar header

Between the collapse and settings buttons sits a CHAT/CODE segmented toggle:
- **CHAT**: switches to No Project (pseudo-project with code tools disabled, bash blacklist, per-session workspaces)
- **CODE**: switches to `lastRealProjectId` (the most recent non-No-Project project) or the first available real project
- The toggle is hidden when no projects are loaded yet

Chat input uses a CodeMirror 6 editor in Markdown mode (`@codemirror/lang-markdown`), providing syntax highlighting for Markdown constructs (headings, bold, italic, code, links) and custom token decorations for `/skill` references (warning color) and `@file` references (info color) via a `StateField`. Autocomplete is powered by `@codemirror/autocomplete` with two custom `CompletionSource` functions: typing `/` at a word boundary triggers fuzzy-filtered skill suggestions, and `@` triggers workspace file/directory suggestions. Open file viewer tabs are boosted to the top of file completions. On send, skill refs are extracted as `activeSkills[]` and file refs are converted to `fileref://` URIs by the core preprocessor (see `core/message_preprocess.go`).

## Design System

One Dark theme. All colors as Tailwind v4 `@theme` custom properties:

| Token                 | Value   | Usage          |
| --------------------- | ------- | -------------- |
| `--color-background`  | #282c34 | App background |
| `--color-foreground`  | #abb2bf | Default text   |
| `--color-primary`     | #528bff | Actions, links |
| `--color-destructive` | #e06c75 | Errors, delete |
| `--color-success`     | #98c379 | Success states |
| `--color-warning`     | #d19a66 | Warnings       |
| `--color-info`        | #61afef | Information    |
| `--color-highlight`   | #e5c07b | Highlights     |

Base font: 14px. Dark color-scheme. Focus outlines globally suppressed. Custom scrollbar (8px, semi-transparent thumb).

## Communication Pattern

```
┌──────────────────────────────────────────────────────────┐
│                    Frontend                               │
│                                                          │
│  src/api/*.ts  ──RPC──→  window.go.desktop.App.*        │
│       ↑                        │                        │
│  src/stores/*  ←──Events──  window.runtime.EventsOn()   │
│       ↑                        │                        │
│  src/hooks/*   ←──handlers──   │                        │
│       ↑                                                 │
│  src/components/* ←── React renders ←── store selectors │
└──────────────────────────────────────────────────────────┘
```

Project switching is orchestrated by `useProjectSwitchState`: it saves source-project UI state through project RPC wrappers (`saveProjectSwitchState`), calls `switchProject`, restores destination tabs/files from `getProjectSwitchState`, and then applies session fallback (saved session → latest session → create session).

## Invariants

- No direct imports from `wailsjs/go/desktop/App` in components — all through `@/api/*`
- No object/array allocation inside Zustand selectors (causes infinite re-render)
- No module-level side effects in store files
- Every selector returns referentially stable value (primitive or direct store property)
- Derived values computed with `useMemo` in custom hooks, not in selectors
- All backend calls are async (never blocking UI thread)
- Project-switch persistence and restoration is hook-driven (`useProjectSwitchState`) and uses best-effort source-state save plus deterministic destination session fallback
- Project switch flow preserves order: save source UI state before `switchProject`, then restore destination file/session state after switch
- Session restore fallback during project switch is deterministic: saved session for destination project, otherwise latest destination session, otherwise a new session
- `lastRealProjectId` always tracks the most recent non-No-Project project activated (updated in `setActiveProjectId` when switching to a real project; preserved when switching to No Project)
- CHAT/CODE toggle switches projects via `switchProject()`; CHAT selects No Project, CODE selects `lastRealProjectId` (or first real project if the last one was deleted)
- All projects created through the CreateProjectDialog always require an external workspace directory; internal workspaces are reserved for No Project auto-creation

## Configuration

Frontend configuration is derived from backend (no separate frontend config file):

| Source                          | Parameter          | Purpose                    |
| ------------------------------- | ------------------ | -------------------------- |
| `GetConfig()` RPC              | `default_model`    | Default model display      |
| `GetConfig()` RPC              | provider `.models` | Per-provider enabled models|
| `GetLogLevel()` RPC            | log level          | Console/log verbosity      |
| `GetSecuritySettings()` RPC    | `default_policy`   | Tool confirmation UI state |
| `ListSkills()` RPC             | skills             | `/skill` autocomplete      |
| `localStorage`                 | panel widths       | Persistent layout          |
| `localStorage`                 | collapsed states   | Sidebar/file viewer state  |
| `localStorage`                 | execution mode     | Normal/advanced toggle     |
| `localStorage`                 | selected model     | Per-message model override |

## Extension Points

- **New RPC wrapper**: add module in `frontend/src/api/` when backend exposes a new method
- **Project-switch persistence wrapper updates**: when project switch-state RPC names or payload fields change, update method probing (`Save/GetProjectSwitchState`, `Save/GetProjectUIState`) and guards in `frontend/src/api/projects.ts` + `frontend/src/types/guards.ts`
- **New store**: create in `frontend/src/stores/`, register in the store initialization sequence
- **New event handler hook**: add to `frontend/src/hooks/` with type guard and store update logic
- **Project-switch orchestration changes**: extend `frontend/src/hooks/useProjectSwitchState.ts` to keep save-before-switch and restore-after-switch ordering stable
- **New display item type**: extend `groupMessages()` in `frontend/src/lib/chatUtils.ts` and add renderer in `ChatMessageRenderer.tsx`
- **New tool card**: add a `CardConfig` entry in `frontend/src/components/chat/toolCards/toolCardRegistry.ts` and (optionally) a body component in `toolCards/bodies/`
- **Custom autocomplete**: add a new `CompletionSource` in `frontend/src/lib/cmChatAutocomplete.ts` and register it in the `autocompletion({ override: [...] })` array

## Related Specs

- [stores.md](stores.md) — Zustand store catalog
- [events.md](events.md) — event handling architecture
- [rendering.md](rendering.md) — message display pipeline
- [../../contracts/desktop-frontend.md](../../contracts/desktop-frontend.md) — RPC surface
- [../../contracts/event-catalog.md](../../contracts/event-catalog.md) — event types
