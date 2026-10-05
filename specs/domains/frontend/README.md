# Frontend

## Purpose

React 19 application providing the user interface for c0wrk: chat interaction, plan visualization, file viewing, and workspace management. Communicates with Go backend exclusively via Wails IPC.

## Key Files

- `frontend/src/App.tsx` — root component
- `frontend/src/stores/` — Zustand state management (34 stores)
- `frontend/src/hooks/` — custom React hooks (event handlers, data loading)
- `frontend/src/api/` — backend RPC wrapper layer
- `frontend/src/lib/` — utilities (fuzzyMatch, parseReferences, markdown config + local image resolution, local file link detection, CodeMirror extensions, the UI-scale geometry helpers `layoutSpace` + `cursorMenuPosition`, and the `@floating-ui/dom` zoom compensation `floatingUiZoom`)
- `frontend/src/components/` — UI component tree
- `frontend/src/types/` — TypeScript type definitions
- `frontend/src/index.css` — design tokens (Tailwind v4 @theme), the `height: 100%` base-sizing chain, and the zoom-corrected `--ui-vh` primitive

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
  pinned: boolean
  active: boolean
  total_input_tokens: number
  total_output_tokens: number
  model: string
  family: string
  has_unfinished_task: boolean
}

// ProjectInfo — project metadata from backend
interface ProjectInfo {
  id: string
  name: string
  workspace_path: string
  is_external: boolean
  is_no_project: boolean
  research_pins: { research: string[] | null; hypotheses: Record<string, string[]> | null }
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

Three-column panel layout (no router, single-page app). The file viewer can be pinned as the classic in-flow third column or unpinned as an overlay above the chat; unpinned floating mode is the default.

```
┌──────────────────────────────────────────────────────────────────┐
│  Sidebar (persisted) │  Main Chat Area  │  File Viewer           │
│  collapsible→40px    │                  │  pinned: docked column │
│                      │                  │  unpinned: chat overlay│
│                      │                  │  (collapses→40px rail) │
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

Resize handles (4px) sit between in-flow panels and on the floating viewer's left edge. Sidebar width/collapse, viewer width/collapse, and viewer pin preference persist through Zustand `persist`/localStorage. The unpinned expanded viewer is a right-aligned absolute overlay over the chat, auto-collapses when the pointer lands outside it or when keyboard focus leaves it (an outside `focusin` collapses only after the viewer itself held focus — Radix portal menus restore focus to their trigger a tick after an item's `onSelect` opened a file, and that focus-restore must not dismiss the freshly opened viewer), and leaves a 40px in-flow rail for reopening; pinning keeps it as a permanently docked, resizable column. A middle-click (`auxclick`, `button === 1`) on a viewer tab closes that tab without activating it — browser-style semantics via the same `closeFile` path as the per-tab close button; the event is `preventDefault`-ed so the webview's autoscroll/selection does not kick in.

The native desktop window separately persists validated width, height, and maximized state in `~/.c0wrk/window_state.json`. Frontend resize events debounce `PersistWindowBounds`; desktop shutdown performs a final best-effort save, and the next process launch uses valid stored dimensions (falling back to defaults for missing, malformed, or below-minimum values).

### Sidebar header

Between the collapse and settings buttons sits a CHAT/CODE segmented toggle:
- **CHAT**: switches to No Project (pseudo-project with semantic_search disabled, per-session workspaces)
- **CODE**: switches to `lastRealProjectId` (the most recent non-No-Project project) or the first available real project
- The toggle is hidden when no projects are loaded yet

Chat input uses a CodeMirror 6 editor in Markdown mode (`@codemirror/lang-markdown`), providing syntax highlighting for Markdown constructs (headings, bold, italic, code, links) and custom token decorations for `/skill` references (warning color) and `@file` references (info color) via a `StateField`. Autocomplete is powered by `@codemirror/autocomplete` with two custom `CompletionSource` functions: typing `/` at a word boundary triggers fuzzy-filtered skill suggestions, and `@` triggers workspace file/directory suggestions. Open file viewer tabs are boosted to the top of file completions. On send, skill refs are extracted as `activeSkills[]` and file refs are converted to `fileref://` URIs by the core preprocessor (see `core/message_preprocess.go`). An `@file` ref accepts an optional line anchor in GitHub-canonical forms — `#L20`, `#L20-36`, `#L20-L36`, or legacy bare-number `#20-36`; the anchor survives into the `fileref://` URI and positions the viewer on open. Paths containing spaces use the single-quoted form `@'my file.go'` — the canonical insert of the `@`-autocomplete and of the file viewer's "Add to chat" action (anchor allowed after the closing quote or inside it); the legacy backslash-escaped form `@my\ file.go` remains recognized.

## Design System

One Dark theme. All colors as Tailwind v4 `@theme` custom properties:

| Token                 | Value   | Usage          |
| --------------------- | ------- | -------------- |
| `--color-background`  | #282c34 | App background |
| `--color-foreground`  | #abb2bf | Default text   |
| `--color-primary`     | #abb2bf | Actions, links (dark theme; `--color-primary-rgb: 82,139,255` / `#528bff` is a separate rgba-helper token) |
| `--color-destructive` | #e06c75 | Errors, delete |
| `--color-success`     | #98c379 | Success states |
| `--color-warning`     | #d19a66 | Warnings       |
| `--color-info`        | #61afef | Information    |
| `--color-highlight`   | #e5c07b | Highlights     |

### Typography

Font sizes are relative: text is sized with the named Tailwind scale (`text-xs`, `text-sm`, `text-base`, `text-lg`, …), whose rem values resolve against the single absolute anchor — `html { font-size: 14px }` in `frontend/src/index.css` (deliberately not 16px — rem-based spacing and radii are tuned for it). Absolute (px) font sizes are forbidden everywhere else.

Invariants:

- Text sizes come from the named scale only; arbitrary `text-[Npx]`/`text-[Nrem]` font sizes are forbidden — only color arbitrary values are legal in the `text-[…]` slot (enforced by `frontend/src/test/typeScaleInvariant.test.ts`).
- CSS carries no `font-size: Npx` outside the `html` root rule — every other CSS font size is rem (enforced by the same guard via brace-matched location of the root rule).
- Inline TS/TSX font sizes are relative too: a bare number or a number/px string (`fontSize: 10`, `fontSize: '13px'`) is forbidden; rem strings (CodeMirror themes) stay legal (enforced by the same guard).
- Deliberate exceptions, bound to an external API or canvas geometry: the xterm constructor option in `Terminal.tsx` (a px number by API) and the SVG labels on the research DAG canvas (`ResearchDagCanvas.tsx`, fontSize 9/11).

Font families are tokenized the same way: `--font-sans`, `--font-mono` and `--font-icon` live in the `@theme` block of `frontend/src/index.css`, with a TS mirror in `frontend/src/lib/fonts.ts` for the non-CSS consumers (CodeMirror themes and the xterm constructor need a literal stack string, not a `var()` reference; CSS is the source of truth — keep both in sync).

- Every text surface rides one of the two text stacks: proportional UI text is `--font-sans` (the `html` base via Tailwind preflight; the opt-in "follow desktop UI font" setting overrides exactly this chain through the `--default-font-family` inline property), monospaced text is `--font-mono`. The same mono stack applies everywhere — file viewer, autocomplete tooltips, terminal, SHAs, IDs — no per-surface variants.
- The icon font `--font-icon` (SauceCodePro NF, embedded via `@font-face`) carries Nerd Font glyphs ONLY — file-tree icons (`.nerd-font-icon`) and completion icons (`.cm-completion-nerd-icon`). Never for text: a text surface that needs a monospaced look uses `--font-mono`.
- The terminal runs on the plain `--font-mono` stack with no icon font — no Nerd Font glyphs in terminal output.
- Hardcoded font-family stacks are forbidden: CSS may only reference `var(--font-*)` (plus the `@font-face` declaration itself); TS may only use the `FONT_*_STACK` constants from `lib/fonts.ts` (or a `var(--font-…)` string) — enforced by `frontend/src/test/typeScaleInvariant.test.ts`.

Dark color-scheme. Focus outlines globally suppressed. Custom scrollbar (8px, semi-transparent thumb).

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
- An unpinned expanded file viewer overlays the chat and auto-collapses on an outside pointerdown, or on focus leaving it once it held focus (a freshly expanded viewer ignores the Radix focus-restore that follows opening a file from a portal menu — e.g. the Research panel's "View artifacts" dropdown); a pinned viewer remains an in-flow resizable column
- Collapsing an unpinned viewer preserves the unpinned preference and renders a 40px in-flow reopen rail
- Persisted desktop window dimensions are accepted only at or above the minimum usable size; invalid state falls back to defaults
- The frontend is **zoom-safe** under the app-wide UI Scale (`zoom` on `<html>`, see [ui-scale.md](ui-scale.md)): the shell and full-height containers size with percentages, viewport-derived sizes use the `--ui-vh` primitive, and pointer-anchored floating panels open at the cursor and fully inside the visible window at any scale — enforced by `frontend/src/test/zoomViewportInvariant.test.ts` plus the per-primitive guards
- Every enabled interactive element shows the pointer cursor and every disabled one shows `not-allowed` — a base-layer cursor policy in `frontend/src/index.css` covers native `button`/`input[type=…]`/`select`/`label`/`summary` and ARIA widget roles (`button`, `menuitem*`, `option`, `tab`, `checkbox`, `radio`, `switch`, `combobox`, `link`, `treeitem`); utility classes (e.g. `cursor-grab` on drag canvases) still override it for intentional exceptions, and `cursor-default` on clickable elements is forbidden outside the allowlist in `frontend/src/test/clickableCursorInvariant.test.ts` (Radix disabled menu items keep `pointer-events-none`, so their cursor stays the UA default)
- Typography is relative-scale-governed: text sizes come from the named Tailwind scale anchored at the 14px `html` root (see Design System); arbitrary `text-[Npx]`/`text-[Nrem]` utilities, px CSS font sizes outside the root rule, and absolute inline `fontSize` values fail the source-scan guard in `frontend/src/test/typeScaleInvariant.test.ts` (API-bound exceptions: the xterm constructor in `Terminal.tsx` and the research DAG canvas SVG labels)

## Configuration

Frontend configuration is derived from backend (no separate frontend config file):

| Source                          | Parameter          | Purpose                    |
| ------------------------------- | ------------------ | -------------------------- |
| `GetConfig()` RPC              | `default_model`    | Default model display      |
| `GetConfig()` RPC              | provider `.models` | Per-provider enabled models|
| `GetLogLevel()` RPC            | log level          | Console/log verbosity      |
| `GetSecuritySettings()` RPC    | `security.groups`  | Tool-group policy UI state (per-group policy + execute blacklist, ADR-024) |
| `ListSkills()` RPC             | skills             | `/skill` autocomplete      |
| `localStorage`                 | sidebar width + collapsed state | Persistent left-panel layout |
| `localStorage`                 | file-viewer width + collapsed + pinned state | Persistent docked/floating right-panel layout |
| `~/.c0wrk/window_state.json`   | window width, height, maximized | Native desktop geometry across process restarts |
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

- [ui-scale.md](ui-scale.md) — UI scale feature and the zoom-safety invariant
- [system-font.md](system-font.md) — opt-in follow-system-font feature (GNOME UI font via gsettings → the inline `--default-font-family` override over Tailwind v4 preflight)
- [stores.md](stores.md) — Zustand store catalog
- [git-operation-console.md](git-operation-console.md) — the footer log of the last git mutation result (button tint, anchored popover, per-project scope, acknowledge semantics)
- [events.md](events.md) — event handling architecture
- [rendering.md](rendering.md) — message display pipeline
- [../../contracts/desktop-frontend.md](../../contracts/desktop-frontend.md) — RPC surface
- [../../contracts/event-catalog.md](../../contracts/event-catalog.md) — event types
