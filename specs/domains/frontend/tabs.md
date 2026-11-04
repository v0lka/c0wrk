# Workspace Tabs

## Role

The workspace tab layer is the outermost context layer of the app shell: each tab remembers which project and which session its workspace view shows plus a snapshot of the UI slices inside it, while exactly one tab — the active one — mirrors the live global stores. The layer always lives; `uiStore.tabsEnabled` only controls whether the tab bar is visible, whether the tab-cycling hotkeys respond, and how notification clicks route (see [ADR-085](../../decisions/085-workspace-tabs-single-engine.md), [ADR-093](../../decisions/093-sidebar-gear-unconditional.md)).

## Key Files

- `frontend/src/stores/tabStore.ts` — the tab layer state: the `Tab {id, projectId, sessionId, ui}` list plus `activeTabId`; actions `addTab` / `closeTab` / `activateTab` / `updateActiveContext`; the pure `selectActiveTab` selector. TRANSIENT — never persisted.
- `frontend/src/lib/tabEngine.ts` — the single activation engine: `activate` (never rejects), `createAndActivate` (the shared [create → activate] chain link behind the tab bar's «+» and the banner click), `writeBack` (live stores → active tab), `attachWriteBack` (write-back subscriptions). A plain lib module (no React); it never reads the flag.
- `frontend/src/lib/tabSnapshot.ts` — the v2 snapshot slices: `captureTabUI` / `restoreTabUI` / `sanitizeTabUI` (fail-closed validation) and `isSameTabUI` (content equality behind reference-stable no-ops). Pure functions — no store access.
- `frontend/src/hooks/useTabController.ts` — the React seam: one engine instance per mount, `attachWriteBack` mounted in an effect, exposes `{activate, writeBack}`.
- `frontend/src/hooks/useTabHotkeys.ts` — the system-standard tab-cycling shortcuts (host-platform pairs), routed through the app's single TabController; the pure matchers (`tabSwitchDelta`, `selectNeighborTabId`) are exported for tests.
- `frontend/src/components/layout/AppLayout.tsx` — THE single controller mount for the app plus the `{tabsEnabled && <TabBar/>}` render gate.
- `frontend/src/components/layout/TabBar.tsx` — the presentational tab strip; every interaction routes through the `TabController` prop; labels and task-status dots derive LIVE from the project/session/chat stores by id, so renames and lifecycle events repaint without a stale copy on the tab.
- `frontend/src/lib/tabLabel.ts` — `buildTabLabel`, the pure label algebra behind the strip: `Project: Session`, `Project`, `CHAT: Session`, `New Tab` (absent segments are dropped, never rendered as empty text).
- `frontend/src/components/layout/SidebarHeader.tsx` — keeps its Settings gear in BOTH flag states: the gear is unconditional (it opens Settings on the default tab, as it always has); the ADR-085 gear handoff was dropped by [ADR-093](../../decisions/093-sidebar-gear-unconditional.md).
- `frontend/src/components/settings/TabsSettings.tsx` — the Appearance-tab master switch over `uiStore.tabsEnabled`.
- `frontend/src/hooks/useNotificationClicks.ts` — the flag-on banner-click routing branch (one of the flag's two behavioral branches, the other being the `useTabHotkeys` early-return; see [system-notifications.md](system-notifications.md)).
- `frontend/src/hooks/useWindowTitle.ts` — the window title follows the ACTIVE tab's ids, resolved live against the project/session stores.

## Behavior

### Always-live layer, display-and-routing-only flag

The tab layer exists unconditionally. With `uiStore.tabsEnabled` off, the UI never renders the create/close controls, so the layer runs its documented "exactly one tab" special case: that single tab is an invisible live mirror of the workspace, and flipping the flag on merely reveals the tab bar above the already-living tab. There are no flag-driven lifecycle transitions — no state is created, destroyed, or migrated when the flag flips.

| `tabsEnabled` gates | `tabsEnabled` never gates |
| ------------------- | ------------------------- |
| Tab-bar rendering (`AppLayout`) | The tab layer lifecycle (always live) |
| Notification-click routing (`useNotificationClicks`) | The activation engine (it never reads the flag) |
| Tab-cycling hotkeys (`useTabHotkeys` — inert early-return when the flag is off) | |
| | The write-back mirror (runs under any flag value) |
| | The sidebar Settings gear (`SidebarHeader` — unconditional, [ADR-093](../../decisions/093-sidebar-gear-unconditional.md)) |

### Single engine, one activation path

```
                ┌───────────────────────────────────────────────┐
                │              tabStore (transient)             │
                │  tabs: [Tab {id, projectId, sessionId, ui}]   │
                │  activeTabId                                  │
                └──────▲───────────────────────────▲────────────┘
     activate(id)      │                           │  updateActiveContext
     (the single       │                           │  (write-back mirror)
      path)            │                           │
        ┌──────────────┴───────────┐   ┌───────────┴─────────────────┐
        │  tabEngine.activate      │   │  tabEngine.writeBack        │
        │  capture → flip →        │   │  live stores → active tab   │
        │  switch/select →         │   │  (project id, session id,   │
        │  restore → converge      │   │  the UI slices)             │
        └──────────────┬───────────┘   └───────────▲─────────────────┘
                       │                           │ attachWriteBack:
                       ▼                           │ subscriptions on project /
        switchProjectWithState / selectSession ────┘ session/ui/input stores
        (the global stores stay the SINGLE source of truth)
```

`AppLayout` mounts the one controller whose engine owns the write-back subscriptions ("exactly one engine per app"). `useNotificationClicks` instantiates its own BARE engine (no `attachWriteBack`), so a banner click can never install a second write-back. All engine instances enqueue on ONE module-level activation chain (`lib/tabEngine`), so a banner-click activation can never interleave its store writes with an in-flight tab-click activation — the shared project switcher alone could not provide this, since a same-project activation makes no switch call at all.

### Activation sequence (`runActivation`)

1. **Capture** — `writeBack()` mirrors the live context into the outgoing tab while it is still active.
2. **Optimistic flip** — `activateTab(tabId)` paints the incoming tab immediately; the incoming tab's remembered `ui` snapshot is copied BEFORE any await, so a mid-switch write-back (the incoming tab is now active and the subscriptions fire) cannot overwrite what the tab remembered.
3. **Context move** — different project: `switchProjectWithState(dest)` first lands the workspace on the project's SHARED saved-session pointer (saved session → latest → fresh), with `null` resolving to the No Project pseudo-project entry; the engine then restores the tab's OWN remembered session over that landed pointer (same contract as the same-project branch: already-landed is a no-op, a session missing from the destination list is dropped, a live one is `selectSession`-ed under the resolved project). The shared pointer is one value per project — and the CHAT pseudo-project is one bucket for every CHAT tab — so without this re-selection a round trip through another mode would converge same-project (or CHAT) tabs onto one session. Same project: `selectSession(sessionId)` when the session differs.
4. **Restore** — the remembered slices are applied to the live stores: the per-project maps merge entry-by-entry (this tab's remembered entries layer over the live map; other projects' entries survive), and the scalar shell slices (sidebar, viewer geometry, input panel, ratio, stats) are written through their store setters, the viewer under the empty-floating invariant.
5. **Converge** — a final `writeBack()` mirrors whatever the context move landed on (restored session included) into the now-active tab; content-equal against the remembered snapshot, so a clean activation is a reference-stable no-op.

Activations serialize through the engine's module-level chain, shared by every engine instance: each activation's full body completes before the next one starts — including activations from a DIFFERENT engine instance (the notification hook's bare engine) — so rapid tab clicks and a banner click landing mid-switch never interleave their store writes.

### Tab creation (`createAndActivate`) — creation rides the same chain

Every tab creation goes through `TabEngine.createAndActivate(seed?)`: the [create → activate] pair is ONE chain link, so an in-flight activation always completes (its restore + write-back land on the tab that was active throughout) BEFORE the fresh tab exists — a half-created tab can never be overtaken by a resuming activation and silently grafted with a foreign context. The link resolves the seed and captures the live UI when it RUNS (not at call time): seed-less (the tab bar's «+» button) means the fresh tab inherits the LIVE workspace of that moment — same project, same session, the slices the user is looking at — so its activation is the clean same-project/same-session path. The banner click passes both ids explicitly (the clicked session). Internally the link is: `addTab(seed + captureLiveUI)` → synchronous step-back to the outgoing tab → the full `runActivation` for the new tab (addTab auto-activates, and the step-back keeps the engine's already-active guard from skipping the materialization). Like `activate`, `createAndActivate` never rejects.

### Write-back: the active tab is a live mirror

`writeBack()` copies the live workspace context (`activeProjectId`, `activeSessionId`, and all eight snapshot slices: the uiStore per-project maps and scalar shell slices, the input-panel slice, the viewer geometry, the git-panel map) into the ACTIVE tab via `updateActiveContext`. `attachWriteBack()` adopts the context synchronously on attach, then keeps the mirror converged through subscriptions on the project, session, ui, input, file-viewer and git-panel stores. Content-equal writes keep the stored references, so unrelated store churn costs nothing.

### Snapshot slices (v2 composition)

| Slice | Source | Content |
| ----- | ------ | ------- |
| `workspaceTabByProject` | `uiStore` | `'explorer'` \| `'git'` \| `'semantics'` \| `'research'` per project id |
| `researchSegmentByProject` | `uiStore` | `'dashboard'` \| `'papers'` per project id |
| `inputMode` | `inputModeStore` | `{mode, height, collapsedHeight, isExpanded}` — exactly the panel's persisted fields; the transient `pending*` fields are excluded |
| `sidebar` | `uiStore` | `{collapsed, width}` — the left sidebar's geometry (v2, [ADR-085](../../decisions/085-workspace-tabs-single-engine.md)) |
| `viewer` | `fileViewerStore` | `{pinned, collapsed, width}` — the right panel's geometry (v2); its OPEN FILE TABS are deliberately excluded, and so are the per-file reading positions (`scrollPositions`) — those are session-scoped in-memory state that never enters a tab snapshot or localStorage |
| `gitPanelTabByProject` | `gitPanelStore` | `'files'` \| `'changes'` \| `'history'` per project id (v2) |
| `chatSessionListRatio` | `uiStore` | the CHAT session-list/workspace split ratio (v2) |
| `showSessionStats` | `uiStore` | the session-stats row visibility toggle (v2) |

Restore semantics follow the slice's shape: the per-project maps merge entry-by-entry (this tab's remembered entries layer over the live map; other projects' entries survive), while every scalar slice — sidebar, viewer, input panel, ratio, stats — restores as an OVERWRITE through its owning store's setters (the setters' clamps stay in force); overwriting the live value is exactly what makes the scalar shell state per-tab ([ADR-085](../../decisions/085-workspace-tabs-single-engine.md)). The viewer restore additionally respects the empty-floating auto-collapse invariant: an unpinned viewer with zero open tabs recedes to the docked rail regardless of what the snapshot remembers.

Every payload crosses `sanitizeTabUI` (the same fail-closed contract as `mergeUIStore`): per-project maps rebuild from valid entries only, scalar fields fall back to the caller-supplied live fallback (widths/heights/ratio must be finite numbers, modes must be known modes, booleans must be booleans), results are freshly allocated so later store mutations never leak into a stored snapshot. `captureTabUI` captures the live slices; `restoreTabUI` validates a remembered snapshot against the live fallback; `isSameTabUI` powers the content-equality no-ops.

The file viewer's open tabs and active file are deliberately NOT a slice: they remain per-project state, restored by the project-switch persistence (`ProjectSwitchState`), never captured per workspace tab — two tabs on the same project intentionally share the project's viewer ([ADR-085](../../decisions/085-workspace-tabs-single-engine.md) D3).

### Banner-click routing (the flag's routing branch)

With the flag ON, a `notification_clicked` payload routes through the tab engine: prefer the exact `{projectId, sessionId}` tab, else any tab already showing the session (session ids are globally unique), and `activate` it; no match creates a tab through the shared `createAndActivate` primitive with the clicked ids as the fixed seed and a live UI capture — the activation's restore step then re-applies what the workspace already shows, so the input panel is left untouched exactly like a «+»-created tab. With the flag OFF, the pre-tab radar pattern runs (switch-when-needed → `selectSession`) and the tab layer is never read or written. See [system-notifications.md](system-notifications.md) § Click routing.

A banner-CREATED tab survives a FAILED activation: `activate` never rejects, and a failure rolls back only the active pointer — the created tab stays in the strip with its clicked context (the seeded live capture matches what the workspace shows, so nothing stale is attributed to it). This is deliberate: the user explicitly asked for that session's tab, and a re-click finds it through the exact-match lookup above and retries the activation.

### Keyboard cycling (the host platform's tab shortcuts)

`AppLayout` mounts `useTabHotkeys` right next to THE single TabController mount: a window-level `keydown` listener whose every activation routes through that controller, so hotkey cycling travels the exact same single-engine path — and the same module-level serialization chain — as tab-bar clicks. The shortcuts are the HOST PLATFORM's system conventions, not one hardcoded web combo:

| Host | Next tab | Previous tab |
| ---- | -------- | ------------ |
| macOS | `Cmd+Shift+]` | `Cmd+Shift+[` |
| macOS (alias) | `Ctrl+Tab` | `Ctrl+Shift+Tab` |
| Windows / Linux | `Ctrl+Tab` / `Ctrl+PageDown` | `Ctrl+Shift+Tab` / `Ctrl+PageUp` |

The macOS pair is matched on the physical key (`code` `BracketRight`/`BracketLeft`), so any keyboard layout works, and `Cmd+]` / `Cmd+[` stay untouched (the web-history pair). Cycling wraps around over the CURRENT tab order, read via `getState()` at event time. With the flag off, or with fewer than two tabs, the keypress is left completely untouched — no activation and no `preventDefault` — so the pre-tab shell keeps its byte-identical keyboard behavior. The same full yield applies while the EMBEDDED TERMINAL holds focus: xterm attaches its own handlers (a TUI can legitimately bind the cycling combos), so a keydown originating inside the terminal's `data-terminal-root` container (xterm's hidden helper textarea) is left unprevented and unhandled — the check is on the event's own target (`isTerminalKeyEvent`), not on panel visibility, so a visible-but-unfocused terminal keeps cycling working. OS auto-repeat (a held shortcut firing repeated keydowns) is ignored — one press is one wrap-around step: a held cross-project cycle must not enqueue an unbounded activation backlog on the shared chain (each project switch costs several backend RPCs plus a persisted switch-state write, and a deep backlog keeps flipping the workspace long after the key is released). Repeat keydowns are dropped before `preventDefault` and stay unprevented, like every other non-activating keydown.

### Tab-strip keyboard interaction (APG tabs, manual activation)

The rendered strip is itself a keyboard surface: the rows are `role="tab"` inside a `role="tablist"`, only the ACTIVE row is tabbable (roving `tabindex`, each row carries `data-tab-id`), and `←`/`→` move DOM FOCUS to the neighbor row with wrap-around over the current order; `Home`/`End` jump to the first/last row. Activation stays MANUAL — focus-only arrows never switch tabs; `Enter`/`Space` on the focused row routes through the same single-engine controller as clicks (the row is a `div`, so the keys are handled explicitly and preventDefaulted, which also stops Space's page scroll). Row keys apply only while the row ITSELF has focus: the nested close button is a real button and keeps its native semantics. The whole surface exists only with the flag on — off renders no bar at all.

### Edge handling

| Situation | Behavior |
| --------- | -------- |
| Tab's project was deleted | The tab retargets to the CHAT pseudo-project (session cleared) and completes activation there |
| Tab's project is `null` (CHAT) | Switches to the No Project pseudo-project entry |
| Tab's session was deleted | The dead id is dropped from the tab; the live session stays put — in BOTH the same-project branch and the cross-project branch (checked against the destination list after the switch) |
| `activate` on the already-active id | No-op (nothing captured, nothing restored) |
| Tab closed while its activation sat in the queue | No-op |
| Closing the ACTIVE tab when the neighbor's activation FAILS | The tab stays open: the failed activation rolled `activeTabId` back onto it, and closing onto a neighbor whose remembered context never got applied would let the next write-back clobber that memory (the engine's `logger.warn` is the signal); the close proceeds only when the neighbor is live |
| No Project entry unavailable | The activation fails and rolls back (see Error Handling) |
| Cycling shortcut with the flag off or a single tab | No-op; the keydown is left unprevented (the pre-tab keyboard behavior) |
| Cycling shortcut while the embedded terminal holds focus | Full yield: the keydown is left unprevented for the terminal (a TUI can bind the combos) |
| Cycling shortcut held down (OS auto-repeat) | Ignored — one press is one wrap-around step; repeat keydowns are dropped before `preventDefault` and stay unprevented |
| Banner-created tab whose activation fails | The tab STAYS in the strip with its clicked context (only the active pointer rolls back); a re-click finds it via the exact-match lookup and retries |

## Error Handling

- **Activation never rejects.** Every failure inside `runActivation` is caught: `activeTabId` rolls back to the outgoing tab, the failure is reported through `logger.warn`, and the promise resolves — fire-and-forget callers cannot produce an unhandled rejection. The live context may have partially moved (the switch flow writes stores as it goes); the post-rollback `writeBack()` re-mirrors whatever live ended up as into the refocused tab, keeping the active-tab-mirrors-live property intact.
- **Fail-closed snapshots.** A malformed tab-UI payload (a hand-edited store, a future wire source) never reaches the stores: invalid entries and wrong-typed scalars fall back per `sanitizeTabUI`.
- **Store-level no-ops.** `activateTab` on an unknown id, `closeTab` on an unknown id, and `updateActiveContext` with an unchanged context all return the same state object.
- **Project-switch failures** surface their own toast from the switch flow; the activation rolls back on top of them.

## Invariants

- The tab layer always holds at least one tab; closing the last tab replaces it with a fresh default tab.
- `activeTabId` always references an element of `tabs`.
- The ACTIVE tab always mirrors the live workspace context — project id, session id, and the eight snapshot slices of the v2 composition — kept converged by the write-back subscriptions under any `tabsEnabled` value.
- Every tab activation travels the single engine path, every tab creation rides the same module-level chain (`createAndActivate`), and the engine stays flag-agnostic — it reads only the tab layer and the live stores.
- Keyboard cycling acts only while the tab bar is enabled and at least two tabs exist, it travels the single engine path, and it yields entirely while the embedded terminal holds focus; any gated-out or non-matching keypress stays unprevented.
- Tab-strip keys (`←`/`→`/`Home`/`End`/`Enter`/`Space`) work only when a tab row itself is focused; arrows move focus without switching (manual activation), and the nested close button keeps native button semantics.
- Tab actions that change nothing return the same state object (reference-stable no-ops).
- Tab arrangements stay app-lifetime: `tabStore` keeps its state in memory only, and a restart always starts from a single default tab.
- The `tabsEnabled` flag persists across restarts (`c0wrk-sidebar-collapsed`, uiStore persist version 7, default off).
- A snapshot payload is validated fail-closed both before storage and before restore, so the stores receive validated values only.
- A snapshot restore always merges the per-project maps entry-by-entry and preserves other projects' entries; scalar slices restore as an overwrite, and the viewer restore never floats an empty unpinned panel (the empty-floating invariant outranks the snapshot).
- Exactly one engine with write-back subscriptions exists per app (the `AppLayout` controller mount); every other engine instance runs bare, without `attachWriteBack`.
- The file viewer's open tabs stay per-project: viewer tabs and the active file ride the project-switch persistence, outside every workspace tab's snapshot; only the viewer panel's geometry (pinned/collapsed/width) is a snapshot slice.
- With the flag off, the shell layout stays byte-identical to the pre-tab UI: no bar, no wrapper artifacts, and the single tab remains the invisible live mirror.

## Related Specs

- [stores.md](stores.md) — `tabStore` in the store catalog; `uiStore` owns the persisted flag (v7) and the per-project maps the snapshot slices capture
- [system-notifications.md](system-notifications.md) — the flag-on banner-click branch of `useNotificationClicks`
- [README.md](README.md) — the app shell the tab bar sits above and the project-switch flow the engine drives
- [ADR-085](../../decisions/085-workspace-tabs-single-engine.md) — the single-engine / disabled-mode-is-one-tab / snapshot-slices decision
- [ADR-093](../../decisions/093-sidebar-gear-unconditional.md) — the sidebar Settings gear is unconditional; the flag's gate narrowed to tab-bar rendering + hotkey gating + banner-click routing
