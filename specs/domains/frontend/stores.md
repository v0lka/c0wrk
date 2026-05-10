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
- `frontend/src/stores/settingsStore.ts`
- `frontend/src/stores/uiStore.ts`
- `frontend/src/stores/vectorIndexStore.ts`

## Store Catalog

| Store                | Responsibility                                                     | Persistence  |
| -------------------- | ------------------------------------------------------------------ | ------------ |
| `chatStore`          | Messages per session, streaming text, activity flags, token counts | No           |
| `planStore`          | DAG items per session, step status, routing stats                  | No           |
| `sessionStore`       | Session list (sorted by last_active_at), active session ID         | No           |
| `projectStore`       | Project list (sorted by last_active_at), active project ID         | No           |
| `fileTreeStore`      | Lazy-loaded directory tree, expanded dirs, search, git status      | No           |
| `fileViewerStore`    | Open files (content/diff/language), tabs, panel width              | localStorage |
| `inputModeStore`     | Chat/terminal input mode, panel height, expanded state             | localStorage |
| `executionModeStore` | Normal/advanced execution mode toggle                              | localStorage |
| `blackboardStore`    | Blackboard facts and metadata for current session                  | No           |
| `settingsStore`      | Settings modal open/close, active tab                              | No           |
| `uiStore`            | Sidebar collapsed state, log level                                 | localStorage |
| `vectorIndexStore`   | Vector index status and progress                                   | No           |

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
6. **Session-keyed data**: chatStore and planStore key data by sessionId

## Invariants

- Each piece of data lives in exactly one store (no dual bookkeeping)
- Store files define stores only — no side effects at import time
- Initialization happens in React lifecycle after runtime readiness confirmed
- Persisted stores use Zustand `persist` middleware exclusively
- Store actions are synchronous (async operations in hooks that call actions)

## Related Specs

- [README.md](README.md) — frontend architecture overview
- [events.md](events.md) — how events update stores
- [rendering.md](rendering.md) — how stores drive rendering
