# Frontend

## Purpose

React 19 application providing the user interface for c0wrk: chat interaction, plan visualization, file viewing, and workspace management. Communicates with Go backend exclusively via Wails IPC.

## Key Files

- `frontend/src/App.tsx` — root component
- `frontend/src/stores/` — Zustand state management (12 stores)
- `frontend/src/hooks/` — custom React hooks (event handlers, data loading)
- `frontend/src/api/` — backend RPC wrapper layer
- `frontend/src/lib/` — utilities (fuzzyMatch, parseReferences, markdown config)
- `frontend/src/components/` — UI component tree
- `frontend/src/types/` — TypeScript type definitions
- `frontend/src/index.css` — design tokens (Tailwind v4 @theme)

## Stack

- React 19 + TypeScript ~5.7
- Vite 6 (build tool)
- Tailwind CSS v4 (utility-first styling)
- Zustand 5 (state management)
- shadcn/ui + Radix UI (component primitives)
- lucide-react (icons)
- react-markdown 10 + remark/rehype plugins (markdown rendering)
- highlight.js 11 (syntax highlighting)
- Mermaid 11 (lazy-loaded diagrams)

## Layout

Three-column panel layout (no router, single-page app):

```
┌──────────────────────────────────────────────────────────────────┐
│  Sidebar (300px)  │  Main Chat Area  │  File Viewer (500px)      │
│  collapsible→32px │                  │  collapsible→32px         │
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

Chat input supports `/skill` and `@file` autocomplete: typing `/` or `@` (at word boundary) triggers a fuzzy-filtered popup of available skills or workspace entries (files and directories). Fuzzy matching runs against the entry name (last path component), not the full path. Files currently open in the file viewer appear pinned at the top of the list, separated from the rest by a horizontal rule; with an empty query all open tabs are shown. Selection inserts the reference into the textarea (directories with a trailing `/`). On send, skill refs are extracted as `activeSkills[]` and file refs are converted to `fileref://` URIs by the backend preprocessor.

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

## Invariants

- No direct imports from `wailsjs/go/desktop/App` in components — all through `@/api/*`
- No object/array allocation inside Zustand selectors (causes infinite re-render)
- No module-level side effects in store files
- Every selector returns referentially stable value (primitive or direct store property)
- Derived values computed with `useMemo` in custom hooks, not in selectors
- All backend calls are async (never blocking UI thread)

## Related Specs

- [stores.md](stores.md) — Zustand store catalog
- [events.md](events.md) — event handling architecture
- [rendering.md](rendering.md) — message display pipeline
- [../../contracts/desktop-frontend.md](../../contracts/desktop-frontend.md) — RPC surface
- [../../contracts/event-catalog.md](../../contracts/event-catalog.md) — event types
