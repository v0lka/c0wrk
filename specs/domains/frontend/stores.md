# Frontend Stores

## Role

Zustand stores provide normalized, reactive state management. Each store owns one domain of data. Cross-store coordination happens in hooks, not in stores directly.

## Key Files

- `frontend/src/stores/chatStore.ts`
- `frontend/src/stores/chatInputStore.ts`
- `frontend/src/stores/planStore.ts`
- `frontend/src/stores/sessionStore.ts`
- `frontend/src/stores/projectStore.ts`
- `frontend/src/stores/fileTreeStore.ts`
- `frontend/src/stores/fileViewerStore.ts`
- `frontend/src/stores/inputModeStore.ts`
- `frontend/src/stores/blackboardStore.ts`
- `frontend/src/stores/gitPanelStore.ts`
- `frontend/src/stores/settingsStore.ts`
- `frontend/src/stores/uiStore.ts`
- `frontend/src/stores/vectorIndexStore.ts`
- `frontend/src/stores/attachmentsStore.ts`
- `frontend/src/stores/workDirsStore.ts`
- `frontend/src/stores/goalStore.ts`
- `frontend/src/stores/reviewStore.ts`
- `frontend/src/stores/themeStore.ts`
- `frontend/src/stores/soundStore.ts`
- `frontend/src/stores/updateStore.ts`
- `frontend/src/stores/experimentalStore.ts`
- `frontend/src/stores/researchStore.ts`
- `frontend/src/stores/terminalRegistryStore.ts`

## Store Catalog

| Store                | Responsibility                                                     | Persistence  |
| -------------------- | ------------------------------------------------------------------ | ------------ |
| `chatStore`          | Messages per session, streaming text, activity flags (`taskActive`), token counts, a per-session `paused` map (absent-key = not paused; `setPaused` action drives it from `session_paused`/`session_resumed` events), and a per-session `compacting` map (absent-key = not compacting; `setCompacting` drives it from `compaction_started`/`compaction_finished` and the runtime-status reconcile — locks the input area and swaps the status-bar compact button for cancel while set) | No           |
| `chatInputStore`     | Chat-input state **keyed per session** (`inputs`): message draft, optimize-in-flight flag, optimize error, send error. Async results (optimize/send) are written to the session captured at action time, so a round-trip completing after a session switch lands in the origin session's slice — never in another session's editor. A `NULL_SESSION_KEY` sentinel slot holds the draft/errors written while no session is active (survives session deletion — it is not a session). Slices of deleted sessions are dropped via `dropSessions` | No           |
| `planStore`          | DAG items (`planGroups` — single session-reset array, not keyed by sessionId), step status, routing stats (`sessionStats` — keyed by sessionId) | No           |
| `sessionStore`       | Session list (sorted by last_active_at), active session ID, project-switch reset (`resetForProjectSwitch`) | No           |
| `projectStore`       | Project list (sorted by last_active_at, No Project always first), active project ID, lastRealProjectId (for CODE toggle), createDialogOpen (Create Project dialog visibility) | No           |
| `fileTreeStore`      | Lazy-loaded directory tree, expanded dirs, search, git status      | No           |
| `fileViewerStore`    | Open files (content/diff/language), tabs, panel width, collapsed state, and `pinned` dock-vs-floating preference; unpinned is the default and auto-collapses on outside focus | localStorage |
| `inputModeStore`     | Chat/terminal input mode, panel height, expanded state, selected model override, selected reasoning effort, pending text insertion (`pendingInsertion`), pending terminal directory (`pendingTerminalDir`), and goal-mode fields (`goalEnabled`, `goalBudget` — in-memory only, not persisted) | localStorage |
| `blackboardStore`    | Blackboard facts and metadata for current session                  | No           |
| `gitPanelStore`      | Git panel UI state (branch info ahead/behind, merge/rebase state, sort/filter) plus a transient per-project commit-box slice `commitByProject` (draft message, AI-generate/commit in-flight flags, error, success SHA) — keyed by project id so it survives project switches and GitPanel unmounts, with async writes keyed to the project captured at click time; dropped via `dropProjectCommitState` | localStorage (view/sort/group prefs only) |
| `settingsStore`      | Settings modal open/close, active tab                              | No           |
| `uiStore`            | Sidebar collapsed state and clamped width, active workspace tab (`workspaceTab`), chat session-list/workspace split ratio (`chatSessionListRatio`), and session-stats row visibility (`showSessionStats`) (log level is fetched via `GetLogLevel` RPC, not stored) | localStorage |
| `vectorIndexStore`   | Vector index status, progress, and search mode                     | localStorage (mode only) |
| `workDirsStore`      | Auxiliary work directories (project-scoped + session-scoped lists), modal open/close state | No           |
| `goalStore`          | Goal-mode state per session (lifecycle status, turn/budget, active goal condition+verify, pending proposal, verdict reason+evidence, independent verifier outcome `verification`/`verificationReason`/`verificationEvidence`, per-goal `verificationMode`); reconciled from `goal_status`/`goal_progress` service-phase events. The status-bar indicator (`GoalStatusIndicator`) is a **read-only** badge (icon + turn + budget) that reads `useGoalStatus` (primitive string) + `useActiveGoal` (direct ref) — it offers no Pause/Resume/Clear controls (pause/resume is session-level, driven from `chatStore.paused`). | No           |
| `reviewStore`        | Code-review buffer per session (general comment, hunk comments keyed by `filePath::hunkId`, review status `active`/`submitted`/`approved`), review-page open state, review-loop flags, and prompt-shown tracking; restored on session activation via `useReviewRestore`. | localStorage |
| `attachmentsStore`   | Pending file attachments **keyed per session** (`attachmentsBySession`; chips/banner render the active session's slice, and an upload that completes after a session switch lands in the originating session's key — no clear-on-switch); `namesById` is a global accumulating id→name cache for `read_attachment` tool cards; carries a per-session transient `imageErrorBySession` banner (set when the user attaches images to a non-vision model; keyed under `NULL_SESSION_KEY` when no session exists; cleared on dismiss/attach, and the sentinel entry retires once a real session becomes active). Slices of deleted sessions are dropped via `dropSessions` | No           |
| `themeStore`         | Active UI theme (`dark` / `light`); `setTheme` writes `<html data-theme>` instantly so the palette applies without a restart. Re-read pre-paint in `main.tsx` to avoid FOUC. | localStorage |
| `soundStore`         | Master toggle for sound notifications; tones are synthesized in the webview via the Web Audio API (`lib/sound.ts`), so the `enabled` preference is the only persisted state | localStorage |
| `updateStore`        | Self-update UI state machine (`phase`, release `info`, `currentVersion`, download `progress`, `errorMessage`, `isChecking`, `isDownloading`); transitions driven by `useUpdateChecker` from global `update:*` events; exposes per-primitive selector hooks (`useUpdatePhase`, `useUpdateProgress`, …). Transient — not persisted. | No           |
| `experimentalStore`  | Effective Experimental Features gate from runtime config (master `enabled` switch loaded from `GetConfig`, updated in place from Settings) | No           |
| `researchStore`      | Experimental research status, hypothesis graph, metrics, and report for the active project (guarded by `projectId` against stale fetches) | No           |
| `terminalRegistryStore` | App-lifetime per-session terminal instances (insertion-ordered session IDs + readiness set; removed only on explicit session/project deletion) | No           |

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
