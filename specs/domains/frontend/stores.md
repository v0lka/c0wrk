# Frontend Stores

## Role

Zustand stores provide normalized, reactive state management. Each store owns one domain of data. Cross-store coordination happens in hooks, not in stores directly.

## Key Files

- `frontend/src/stores/chatStore.ts`
- `frontend/src/stores/planStore.ts`
- `frontend/src/stores/sessionStore.ts`
- `frontend/src/stores/projectStore.ts`
- `frontend/src/stores/fileTreeStore.ts`
- `frontend/src/stores/fileViewerStore.ts`
- `frontend/src/stores/inputModeStore.ts`
- `frontend/src/stores/executionModeStore.ts`
- `frontend/src/stores/blackboardStore.ts`
- `frontend/src/stores/planReviewStore.ts`
- `frontend/src/stores/gitPanelStore.ts`
- `frontend/src/stores/settingsStore.ts`
- `frontend/src/stores/uiStore.ts`
- `frontend/src/stores/vectorIndexStore.ts`

## Store Catalog

> **Note:** `executionModeStore` and `planReviewStore` are affected by ADR-012. Under the Conductor pipeline there is no execution-mode toggle (the Conductor chooses its own granularity) and no system-driven plan-review toggle (approval is a `declare_plan` tool call). These stores are expected to be removed or repurposed during implementation of ADR-012.

| Store                | Responsibility                                                     | Persistence  |
| -------------------- | ------------------------------------------------------------------ | ------------ |
| `chatStore`          | Messages per session, streaming text, activity flags, token counts | No           |
| `planStore`          | DAG items (`planGroups` — single session-reset array, not keyed by sessionId), step status, routing stats (`sessionStats` — keyed by sessionId) | No           |
| `sessionStore`       | Session list (sorted by last_active_at), active session ID, project-switch reset (`resetForProjectSwitch`) | No           |
| `projectStore`       | Project list (sorted by last_active_at, No Project always first), active project ID, lastRealProjectId (for CODE toggle) | No           |
| `fileTreeStore`      | Lazy-loaded directory tree, expanded dirs, search, git status      | No           |
| `fileViewerStore`    | Open files (content/diff/language), tabs, panel width, project-switch file restore (`restoreProjectFiles`) | localStorage |
| `inputModeStore`     | Chat/terminal input mode, panel height, expanded state, selected model override, selected reasoning effort | localStorage |
| `executionModeStore` | Normal/advanced execution mode toggle                              | localStorage |
| `blackboardStore`    | Blackboard facts and metadata for current session                  | No           |
| `planReviewStore`    | Plan review toggle state (enabled/disabled)                        | localStorage |
| `gitPanelStore`      | Git panel UI state (branch selection, commit draft, diff view mode) | No           |
| `settingsStore`      | Settings modal open/close, active tab                              | No           |
| `uiStore`            | Sidebar collapsed state (log level is fetched via `GetLogLevel` RPC, not stored) | localStorage |
| `vectorIndexStore`   | Vector index status, progress, and search mode                     | localStorage (mode only) |
| `workDirsStore`      | Auxiliary work directories (project-scoped + session-scoped lists), modal open/close state | No           |

## Critical Anti-Patterns

### Never allocate in selectors

```typescript
// BAD — creates new array on every render → infinite loop (React #185)
const items = useStore((s) => s.messages.filter((m) => m.type === "user"));

// GOOD — return direct reference, derive in hook
const messages = useStore((s) => s.messages);
const userMessages = useMemo(
  () => messages.filter((m) => m.type === "user"),
  [messages],
);
```

### Never derive objects in selectors

```typescript
// BAD — new object every render
const data = useStore((s) => ({ count: s.items.length, active: s.activeId }));

// GOOD — separate selectors for each primitive
const count = useStore((s) => s.items.length);
const active = useStore((s) => s.activeId);
```

## Store Design Principles

1. **Normalized state**: indexed by ID, updated in-place
2. **Incremental updates**: never rebuild entire tree from flat array
3. **Stable selectors**: return primitives or direct store properties
4. **No cross-store writes**: stores don't import other stores
5. **Declarative persistence**: Zustand `persist` middleware, not manual localStorage
6. **Session-keyed data**: chatStore keys messages by sessionId; planStore uses a single session-reset `planGroups` array (not keyed by sessionId) with `sessionStats` keyed by sessionId

## Invariants

- Each piece of data lives in exactly one store (no dual bookkeeping)
- Store files define stores only — no side effects at import time
- Initialization happens in React lifecycle after runtime readiness confirmed
- Persisted stores use Zustand `persist` middleware exclusively
- Store actions are synchronous (async operations in hooks that call actions)
- Project switch orchestration executes in hooks (`useProjectSwitchState`) with ordered cross-store updates: reset `sessionStore` before destination session load, then restore `fileViewerStore` tabs/files from persisted project state
- Session activation after project switch uses deterministic fallback (saved session ID when valid, otherwise latest session, otherwise newly created session)

## Error Handling

- **Selector errors**: Zustand selectors never throw — they return `undefined` for uninitialized state; hooks must guard before rendering
- **Persistence failures**: Zustand `persist` middleware silently catches `localStorage` write errors (storage full, private browsing); state remains in-memory
- **Missing session data**: stores keyed by `sessionId` return empty collections when the session ID is not yet initialized (no error, just empty)
- **Async initialization**: stores that depend on backend data (sessionList, projectList) use a `loaded` flag; components show loading state until `loaded === true`
- **Cross-store consistency**: hooks that read from multiple stores must handle the case where one store has data and another does not (e.g., event arrives before related entity)
- **Project switch save/restore RPC failures**: `saveProjectSwitchState` and `getProjectSwitchState` are treated as best-effort; hooks continue switch execution and fall back to session list / session creation logic

## Related Specs

- [README.md](README.md) — frontend architecture overview
- [events.md](events.md) — how events update stores
- [rendering.md](rendering.md) — how stores drive rendering
