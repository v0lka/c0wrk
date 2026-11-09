# Loading States

## Role

Defines the frontend's three data-arrival loading states — the pre-React **boot loader**, the **chat-history spinner**, and the **session-list indicators** — the store markers that drive them, and the invariants (CSP legality, zoom safety, single writer per marker, `null`-vs-loaded semantics) they uphold identically in both transports (Wails webview and the browser web-access sidecar, which serve the same `frontend/dist` tree). It also defines the vector index's non-ready presentation, where `unavailable` is a FAILURE rather than a loading state and the repair action lives.

## Key Files

- `frontend/index.html` — the static boot loader: `#boot-loader` div + inline `<style>` (CSS-only) shipped INSIDE the `#root` React container; also carries the anti-FOUC theme call to the external `public/prepaint-theme.js` script
- `frontend/src/main.tsx` — mounts React into `#root` (the first render clears the static loader and its style) and schedules the idempotent backstop `requestAnimationFrame(() => document.getElementById('boot-loader')?.remove())`
- `frontend/src/App.tsx` — the splash phases the boot loader hands off to: `phase === 'splash'` minimal spinner (no `tool_manager:start` yet) and `ToolInstallSplash` (tool install in progress)
- `frontend/src/components/webaccess/WebAccessGate.tsx` — `TransportProbeSplash`, the spinner the gate holds while `GET /api/transport` settles (web-transport boot barrier, see [../web-access.md](../web-access.md))
- `frontend/src/stores/chatStore.ts` — `historyLoaded: Set<string>` settled marker, `markHistoryLoaded`, and the `useSessionHistoryLoaded(sessionId)` hook selector; per-batch cleanup in `dropSessions`
- `frontend/src/lib/sessionHistoryReload.ts` — `reloadSessionHistory(sessionId, { isCancelled })`, the SINGLE writer of the history-loaded marker (one try/finally in the exported wrapper)
- `frontend/src/components/chat/ChatArea.tsx` — the chat-history spinner in the empty-session branch; the effect that triggers `reloadSessionHistory` on session activation
- `frontend/src/hooks/useShallowResync.ts` — the WS-reconnect/focus resync, routed through the same `reloadSessionHistory`
- `frontend/src/stores/sessionStore.ts` — `sessions: SessionInfo[] | null` (`null` = not yet loaded; `resetForProjectSwitch()` restores `null` on every project switch)
- `frontend/src/hooks/useSessionLoader.ts` — feeds the list: `listSessions()` → `setSessions` (a FAILED fetch leaves the store untouched, i.e. still `null`)
- `frontend/src/components/layout/SessionList.tsx` — CHAT-mode sidebar list: skeleton rows while `sessions === null`
- `frontend/src/components/layout/SessionSelector.tsx` — CODE-mode dropdown: loading row while `sessions === null`
- `frontend/src/components/layout/IndexingStatus.tsx` — the status-bar vector-index pill: honest non-ready labels, no fake progress bar, and the `unavailable` diagnosis (`last_error` + `Kind: <error_kind>`) in its native `title`
- `frontend/src/components/layout/VectorSearchFilters.tsx` — the search panel's inline `last_error` line and the mode-row repair action
- `frontend/src/hooks/useVectorReindex.ts` — the repair action's state machine: busy set, `unavailable` stays enabled, optimistic latch, state-naming titles
- `frontend/src/stores/vectorIndexStore.ts` — the replace-only status snapshot (failure reason is transient; only `mode` is persisted)
- `core/csp` — the production CSP (`script-src 'self'`, `style-src 'self' 'unsafe-inline'`), stamped by the `desktop/csp.go` `CSPMiddleware` into both the Wails asset server and the web-access sidecar

## Behavior

### 1. Boot loader (pre-React, first paint)

```
 HTML paint ──► static #boot-loader visible ──► prepaint-theme applies the
 (no JS yet)    (pure HTML+CSS overlay)         persisted theme (external script)
      ──► React mounts into #root ──► first render CLEARS loader + style
      ──► WebAccessGate ──► App phase='splash' spinner ──► main UI
                             (web transport: TransportProbeSplash first)
```

- The loader renders on the very first HTML paint — before the JS bundle is even fetched — so the window is never blank during startup. Markup: `#boot-loader` (`role="status" aria-label="Loading c0wrk"`) holding a `.boot-spinner` ring — fixed overlay (`position: fixed; inset: 0; z-index: 50`), flex-centered `2rem` ring (`border: 2px solid var(--color-primary, #abb2bf)`, transparent top border, `border-radius: 50%`, `1s linear infinite` rotation via `@keyframes boot-loader-spin`), background `var(--color-background, #282c34)`.
- `var()` with One Dark fallbacks makes the pre-stylesheet paint match the app shell in both themes; the persisted theme takes over as soon as the stylesheet lands.
- It mirrors App's own splash spinner exactly for a seamless handoff. Because the loader AND its `<style>` live inside `#root`, React's first render clears them as it takes over the container — no blank frame; the rAF removal is an idempotent null-safe backstop that also keeps the spinner on screen if the bundle fails before mounting.
- The anti-FOUC theme application is an EXTERNAL classic script (`public/prepaint-theme.js`) executed during HTML parse — never an inline script (CSP; see Invariants). `main.tsx` re-applies the theme from the rehydrated store; both passes are idempotent.

### 2. Chat history (per-session, marker-driven)

- `chatStore.historyLoaded: Set<string>` holds the sessions whose persisted-history load has **settled** — success OR failure (both mean "no longer in flight"). A session absent from the set is "never loaded" (initial, or a cancelled attempt — see below). `useSessionHistoryLoaded(sessionId)` returns a primitive (`true` for a null session id), so the selector is referentially stable.
- **Single writer**: `lib/sessionHistoryReload.reloadSessionHistory` owns the only try/finally — its `finally` calls `markHistoryLoaded(sessionId)` on resolve AND reject, skipped when `isCancelled()` is true or when the session left the catalog mid-flight (`sessionInCatalog`), so a deletion racing the load cannot resurrect the marker `dropSessions` just cleared. Both trigger paths go through it: ChatArea's activation effect and `useShallowResync` (WS-reconnect/focus), so a resync re-run never flashes the spinner while content is on screen (with content present the spinner branch is unreachable anyway).
- **Cancellation is honest**: a cancelled attempt (the user switched sessions mid-flight) deliberately leaves the marker unset — its snapshot never reached the store, so the next visit to the session spins until its OWN load settles.
- Render: ChatArea's empty branch (`!hasContent`) shows `<Loader2 className="size-8 animate-spin opacity-30" aria-label="Loading session history" />` while the active session has neither content nor the marker; once settled-empty it renders the `MessageCircle` placeholder. A failed load stops the spin (marker set in the `finally`) and the error card explains the empty chat.
- Store hygiene: `markHistoryLoaded` is idempotent — an already-marked session keeps state identity (no gratuitous subscriber sweep); `dropSessions` forgets deleted sessions' markers in the SAME update (one `Set` copy for the whole batch), so a recreated session with a reused id starts as "never loaded" again; a batch with no marked sessions is an identity-preserving no-op.

### 3. Session list (null-vs-loaded sentinel)

`sessionStore.sessions: SessionInfo[] | null` — **`null` is the canonical "not yet loaded" sentinel** (the store's initial value AND `resetForProjectSwitch()` on every project switch); `[]` is a genuinely loaded, empty list. The two are never conflated:

| Surface | `sessions === null` (loading) | `[]` (loaded, empty) |
| ------- | ----------------------------- | -------------------- |
| CHAT sidebar (`SessionList`) | three `animate-pulse` skeleton rows matching the row geometry (`h-7`, staggered `w-[85%]`/`w-[70%]`/`w-[55%]`, `bg-muted/60`) inside `<div role="status" aria-label="Loading sessions">` | "No sessions yet" (search: "No matching sessions") |
| CODE dropdown (`SessionSelector`) | loading row: `Loader2 size-3 animate-spin` + "Loading sessions…" with the same `role="status"` signal | renders exactly as before (no loading row) |

- A cold start and a project switch therefore never flash "No sessions yet" before the real list arrives — `null` is the only skeleton trigger.
- The list is fed by `useSessionLoader` (`listSessions()` → `setSessions`, guarded against stale/pushed-over reads); `useShallowResync` re-reads on reconnect/focus. A FAILED fetch leaves the store untouched — still `null`, still showing the loading affordance until a later re-read lands.
- Consumers that must not act on an unloaded model gate on the sentinel: `SessionSelector` skips `pinnedGroupsStore.prunePins` while `sessions === null`, so a not-yet-loaded model can never wipe the destination project's pins.

### 4. Transport behavior (wails / web / none)

The same `frontend/dist` tree is served by the Wails asset server and by the web-access sidecar behind the injected CSP middleware, so all three states look and behave identically. The boot chain differs only in the middle:

- **Wails webview**: the transport resolves synchronously (`'wails'`); `WebAccessGate` passes children through and the static loader hands off directly to App's splash.
- **Browser tab**: the probe (`GET /api/transport`) is an async round trip, and the gate HOLDS the app on `TransportProbeSplash` until it settles — a late-joining tab mounted into an unresolved transport would fire its mount-time RPCs into `'pending'` and strand. Settling `'web'` applies the web-mode adaptations before the app mounts; the desktop-only `backend:ready` event never fires for a late-joining tab, so the splash's safety-net RPC (`listProjects()`) is the only splash-exit path in web mode.
- **`'none'`** (plain dev server / vitest / SSR): no sidecar serves the origin; the same probe path resolves and the gate proceeds — the loading states themselves are transport-agnostic.

### 5. Vector index failure & repair (status-bar pill + search panel)

The vector index streams a status snapshot rather than resolving a promise, but the same discipline applies: a window that is merely not settled yet must never render as a failure, and a failure must say why. The wire shape is `VectorIndexStatus` with the optional pair `last_error` / `error_kind` ([../workspace.md](../workspace.md), [../../decisions/120-vector-store-repair-ladder.md](../../decisions/120-vector-store-repair-ladder.md)).

| Status | Status-bar pill | Search panel |
| ------ | --------------- | ------------ |
| store default `idle` (no status yet) | hidden — the bar renders nothing | normal "Select a project to search" |
| `loading` (kind `loading`) / `loading` (kind `restarting`) | idle dots + "Preparing index…", no progress bar | inputs disabled because searches would wait |
| `preparing` / `migration-rebuild` | "Preparing index…" / "Migrating store…", idle dots and no fake progress; the action stays disabled and spinning | reindex disabled, tooltip names the pre-work |
| `ready` / `indexing` / `reindexing` | legend or honest progress | normal |
| `unavailable` | warning pill labelled "Index unavailable" with idle dots and no bar; its native `title` = the legend + the backend's `last_error`, plus `Kind: <error_kind>` when present | the `last_error` as a plain `text-warning` line under the filters (only when non-empty); the action is ENABLED as `Rebuild index — recreate the broken index from source` |
| `disabled` | idle dots + "Indexing off" (a user choice, not a failure) | action disabled with "Indexing is off — enable it in Settings → Search" |

- **The failure reason is the backend's own words.** The pill keeps its short visible label and puts the diagnosis in its native `title` — one channel, no Radix tooltip and no title-under-trigger combination ([button-tooltips.md](button-tooltips.md)). A missing reason or kind leaves the clean legend (or the bare `Kind:` half) intact: the UI never invents a cause and never renders a dangling separator. The panel copy is plain inline text, not a modal or toast, so the reason is visible without hovering right above the results area the broken index cannot fill.
- **The action is the repair, and its text tracks the state.** Busy states (a pass, the branch-scoped open, the view-attach window, the legacy promotion, or the optimistic latch) disable the action and show a spinner with a tooltip naming the real state; `unavailable` keeps it ENABLED and relabels it as a rebuild (nothing is running, so no spinner); only `disabled` is terminally disabled. A click flips the busy title to `Repairing index…`; the optimistic latch is released by the first `vector_index:status` event, by a flip into `unavailable` (a repair that produced no collection — another attempt stays possible), by the project becoming unavailable, or by a rejected RPC.
- **The reason belongs to one attempt.** `vectorIndexStore` replaces the status object wholesale, so a healthy snapshot (which omits both fields) drops a stale cause and `reset()` clears it on a project/session switch; `partialize` persists only `mode`, never the failure text.

## Error Handling

- **History load rejects**: the wrapper's `finally` still marks the session settled — the spinner never hangs; ChatArea logs and appends a local `error` message ("Failed to load session history…") that renders as the error card. The resync path catches, logs a warning, and waits for the next trigger.
- **Session-list fetch fails**: `useSessionLoader` leaves the store untouched (`null`) — the skeleton/loading row stays honest instead of degrading into a false "No sessions yet"; the next `sessions:changed` push or resync re-read owns the store.
- **Bundle fails before mount**: the static boot loader keeps spinning (it never depended on JS); the rAF backstop is a no-op on the missing node's removal only after a successful mount.
- **Vector index shows a failure, not a loading placeholder**: an `unavailable` snapshot renders the backend's cause on the pill's `title` and in the search panel; a `loading`/`restarting` window renders as loading (never as unavailable); a missing cause leaves the clean legend and no inline line rather than inventing text.

## Invariants

- **No inline scripts in `index.html`, ever.** The production CSP (`core/csp`, stamped by `desktop/csp.go` and reused by the web-access sidecar) allows `script-src 'self'` only; `style-src 'self' 'unsafe-inline'` is what makes the inline `<style>` legal. Anti-FOUC/parse-time logic lives in external scripts under `public/`.
- **The boot loader is pure HTML+CSS** — no JS dependency for its visibility; React mount is the primary removal path and the rAF lookup is an idempotent backstop.
- **Every loading indicator is zoom-safe**: positioned with `inset: 0` / percentages and sized with absolute lengths only — no raw viewport units or `*-screen` utilities anywhere, including `index.html` (the project-wide zoom guard scans it verbatim; see [ui-scale.md](ui-scale.md)).
- **Exactly one writer per marker.** Only `reloadSessionHistory` writes `historyLoaded` (single try/finally); only the session-list loaders (`useSessionLoader`, the resync) write `setSessions`. No component mutates a marker directly.
- **`historyLoaded` means "settled", not "succeeded"** — resolve and reject both stop the spinner; a cancelled attempt and a session that left the catalog mid-flight both leave the marker unset, because neither delivered data the store can render.
- **`sessions === null` means "not yet loaded"; `[]` means "loaded and empty."** A surface renders a loading affordance for `null` and the empty-state for `[]` — never the empty-state for `null`.
- **Loading states are announced**: each carries `role="status"` + an `aria-label` (boot loader, session-list skeleton/loading row) or an explicit `aria-label` (chat-history spinner).
- **Markers are store state, not component state**: cleared with their sessions (`dropSessions`), reset with the project switch (`resetForProjectSwitch`), and updated identity-preserving (idempotent re-marks and no-op batches never copy state).
- **`unavailable` is a failure state, never a loading placeholder**, and a `loading`/`restarting` window never renders as `unavailable` — each vector state's label describes what is actually happening (opening, preparing, migrating, indexing, broken, or deliberately off) and never borrows another state's shape.
- **Every vector failure renders the backend's cause verbatim**: the status-bar pill's native `title` and the search panel's inline line carry `last_error` (and `Kind: <error_kind>` on the pill) as the backend wrote it — a surface never paraphrases a failure it does not understand, invents a cause when the fields are absent, or leaves a dangling separator.
- **The repair action is enabled exactly where it can help**: `unavailable` keeps it enabled as a rebuild (no spinner), busy states disable it with a spinner and a tooltip naming the real pre-work, and only the master-switch `disabled` state disables it terminally; the busy title reads `Repairing index…` during a repair.
- **The vector failure reason is transient store state**: the status snapshot is replaced wholesale, a healthy status drops a stale cause, `reset()` clears it on a project/session switch, and persistence stores only the search `mode`.

## Related Specs

- [ui-scale.md](ui-scale.md) — the zoom-safety invariant every loading overlay obeys
- [../web-access.md](../web-access.md) — the web transport, the probe boot barrier, and the CSP middleware shared by the sidecar
- [stores.md](stores.md) — `chatStore` / `sessionStore` in the store catalog
- [events.md](events.md) — the event-driven resync/re-read paths that re-feed these states
- [../workspace.md](../workspace.md) — the vector index's states, honest failure reason and repair escalation
- [../../decisions/120-vector-store-repair-ladder.md](../../decisions/120-vector-store-repair-ladder.md) — the failure/repair contract this presentation follows
- [README.md](README.md) — frontend architecture overview
