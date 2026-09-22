# Git Operation Console

## Role

The single surface for the outcome of every *funneled* user-triggered git mutation in the Git panel — branch creation is the one deliberate exception (see [What stays inline](#what-stays-inline-and-does-not-feed-the-console)): a footer log button whose glyph is tinted by the last operation's result, anchored over a popover that replays the captured output. It replaces the scattered per-feature success banners and error toasts with one per-project record so a result survives panel remounts and project switches.

## Key Files

- `frontend/src/lib/gitOperation.ts` — `runGitOperation`, the single wrapper every call site funnels through; also `GitOperationOutcome`, `gitOperationErrorMessage`, `RunGitOperationArgs`
- `frontend/src/stores/gitPanelStore.ts` — `operationByProject`, `GitOperationRecord`, `GitOperationKind`, `EMPTY_GIT_OPERATION`, `recordGitOperation`/`acknowledgeOperation`/`dropProjectOperation`, `selectLastOperation`
- `frontend/src/components/GitPanel/GitPanelFooter.tsx` — hosts the button + popover, owns open state and the outside-click/Escape dismissal, and acknowledges on open (`toggleLog`)
- `frontend/src/components/GitPanel/GitOperationButton.tsx` — the footer affordance (tint + busy spinner + `aria-expanded`)
- `frontend/src/components/GitPanel/GitOperationPopover.tsx` — the portaled log panel (status header + `<pre>` of captured output), positioned from the trigger's rect
- `frontend/src/lib/dropdownPosition.ts` / `frontend/src/lib/layoutSpace.ts` — the zoom-safe trigger-anchored placement helpers the popover reuses (`computeDropdownPosition`, `toLayoutTriggerRect`, `getLayoutViewport`)
- Event source: `frontend/src/hooks/useGitStatusEvents.ts` — consumes `git:status_changed` so a console-recorded mutation auto-refreshes the store
- Guards: `frontend/src/lib/gitOperation.test.ts`, `frontend/src/stores/gitPanelStore.test.ts`, `frontend/src/components/GitPanel/GitPanelFooter.test.tsx`, `frontend/src/components/GitPanel/GitOperationButton.test.tsx`, `frontend/src/components/GitPanel/GitOperationPopover.test.tsx`

## Behavior

### One funnel for every result

Every user-triggered git mutation runs through `runGitOperation`, which is the only writer of an operation record. It captures the owning `projectId` at call time, awaits `fn`, and:

- on **success** records `{ kind, label, ok: true, output, error: null }` (when `recordSuccess` is true), where `output` is `extractOutput(result)` — or the result itself when it is a string, else `''`;
- on **failure** records `{ kind, label, ok: false, output: '', error }` with `gitOperationErrorMessage(err)`, and logs it at the call site's explicit `logLevel` — `logger.error` by default, `logger.warn` for the low-level ops that pass `logLevel: 'warn'` (per-file staging/unstaging, discard, and gitignore failures are routine user-initiated outcomes rather than faults; the bulk `stage-all`/`unstage-all` toolbar actions keep the default ERROR, as a failed batch reads like a fault). Severity is deliberately independent of `recordSuccess`, which only governs whether a *success* is recorded;
- **never rethrows** — a rejected `fn` is captured, so a caller's `try/finally` (busy-flag clearing, dialog dismissal) always runs.

Each record sets a fresh `at` timestamp and `acknowledged: false`, so a new result re-arms the tint.

```
call site ──runGitOperation({projectId, kind, label, fn, extractOutput?, recordSuccess?, logLevel?})──▶ gitPanelStore.recordGitOperation
                                                                                              │  operationByProject[projectId]
                                                                                              ▼
GitPanelFooter ──selectLastOperation(state, activeProjectId)──▶ GitOperationButton ──toggle──▶ GitOperationPopover
       ▲                                                                                             (label + output/error)
       └── toggleLog: opening ⇒ acknowledgeOperation(activeProjectId) ⇒ glyph tints neutral
```

### Button

`GitOperationButton` renders a `Terminal` glyph that, while a remote op is in flight, is replaced by a `Loader2` spinner. It stays clickable during an operation so the previous result's log remains readable. Its color is the only signal of an unread result (`operationTone`):

| State | Tone |
| ----- | ---- |
| `busy` (op in flight) | neutral (`text-muted-foreground`) |
| no record | neutral |
| `acknowledged` | neutral |
| `ok` and unread | `text-success` |
| `!ok` and unread | `text-destructive` |

It carries `aria-label="Git operation log"`, `aria-haspopup="dialog"`, `aria-expanded={open}`, and `aria-controls` pointing at the popover's stable id.

### Popover

`GitOperationPopover` is purely presentational — the footer owns open state and dismissal. It renders a dialog (`role="dialog"`, `aria-modal="false"`, `aria-label="Git operation log"`, a stable `id` the trigger points at via `aria-controls`) and moves focus into itself once positioned, with a status header (a `Terminal` / `CheckCircle2` / `XCircle` icon plus the operation `label`, falling back to `kind`, and `No git operations yet` when there is no record) above a scrollable, pre-wrapped `<pre>`:

```
header: [icon] <label>                      ← label.trim() || kind; tinted success/destructive/muted
body:   captured output                     ← output.trim() || error || 'No output'
```

The body prefers the captured `output`; when the operation produced none (a failed spawn records an empty `output` and the message in `error`), it falls back to `error`, then to `No output`.

The panel is **portaled to `document.body`** and `position: fixed`, measured and placed from the trigger button's `getBoundingClientRect()` (see [Zoom-safe sizing](#zoom-safe-sizing)). This is what keeps it outside the Git panel's `overflow-hidden` ancestor: an in-place panel wider than the resizable sidebar (180–500px) overflowed that clipped ancestor, and the panel's `focus()` then scrolled the container — shifting the entire Git panel sideways. Escaping to a body portal removes both the clipping and the scroll.

### Per-project scope

The record lives in the **transient, non-persisted** `operationByProject` map in `gitPanelStore`, keyed by project id. Consequences:

- A result survives GitPanel unmounts (CHAT↔CODE switches) and project switches — switch away and back and the last result is still there.
- A slow operation that completes after a project switch lands in the project captured at call time, never the newly active one.
- Nothing is rehydrated from `localStorage`: a result is live feedback for the running session, not persisted state. `partializeGitPanel` deliberately omits `operationByProject`.
- The record is dropped only by `dropProjectOperation` (project deleted) or a store `reset`.

The active project's record is read via `selectLastOperation(state, activeProjectId)`, which returns the stored record **by reference** (or `undefined` for a null/undefined/unknown project). The popover is rendered on every tab because `GitPanelFooter` is the shared footer for the whole panel.

### Acknowledge semantics

Opening the log is the acknowledgement. `GitPanelFooter.toggleLog` calls `acknowledgeOperation(activeProjectId)` as it opens the popover (except while a remote op is in flight — the log stays readable mid-operation, and the pending result re-arms the tint when it lands), flipping `acknowledged: true`, so the glyph drops to neutral and an open log reads as "seen" rather than "unread result". Closing and reopening does not re-acknowledge; `acknowledgeOperation` is a reference-stable no-op when the project has no record or the flag is already set (it never fabricates an entry just to flip a flag). A newly recorded operation resets `acknowledged` to false, re-arming the tint.

### Which operations feed the console

Every `GitOperationKind` is written by `runGitOperation`; call sites and labels:

| Kind | Trigger (call site) | Label example | Success recorded? |
| ---- | ------------------- | ------------- | ----------------- |
| `pull`, `push`, `fetch` | Remote-op split buttons + flag dropdowns (`GitPanel/GitPanelFooter.tsx`) | `Pull` / `Push` / `Fetch` | Yes |
| `commit` | Commit button and the Trust/continue flow (`GitPanel/CommitSection.tsx`) | `Committed <sha7>`; `Commit failed` on failure | Yes |
| `stage-all`, `unstage-all` | Changes toolbar (`hooks/useGitToolbarActions.ts`) | `Staged all changes` / `Unstaged all changes` | Yes |
| `merge-abort`, `rebase-abort` | Changes toolbar abort (`hooks/useGitToolbarActions.ts`) | `Aborted merge` / `Aborted rebase` | Yes |
| `stash-create`, `stash-pop`, `stash-drop` | Stash buttons + per-entry list (`GitPanel/GitStashButtons.tsx`) | `Stashed changes`, `Popped latest stash`, `Popped/Dropped stash@{N}` | Yes |
| `checkout`, `branch-rename`, `branch-delete`, `merge`, `rebase`, `branch-push`, `branch-checkout-remote`, `branch-delete-remote` | Branch lists (`hooks/useBranchActions.ts`) | `Checked out <name>`, `Renamed <old> to <new>`, `Merged <name> into current`, … | Yes |
| `reset`, `tag-create`, `tag-push`, `tag-delete`, `tag-delete-remote` | History commit context menu (`GitPanel/GitHistoryContextMenu.tsx`) | `Reset <branch> to <sha7> (soft\|mixed\|hard)`, `Created tag <t>`, … | Yes |
| `stage`, `unstage` | Changes list rows + file context menu (`GitPanel/index.tsx`, `GitPanel/GitFileContextMenu.tsx`) | `Staged <path>` / `Unstaged <path>` | **No** — errors only |
| `discard` | File context menu (`GitPanel/GitFileContextMenu.tsx`) | `Discarded changes in <path>` | **No** — errors only |
| `gitignore` | File + file-tree context menus (`GitPanel/GitFileContextMenu.tsx`, `layout/FileTreeContextMenu.tsx`) | `Added <path> to .gitignore` | **No** — errors only |
| `unknown` | sentinel only — never passed by a real caller | — | n/a |

**Silent successes.** The per-file staging/discard/gitignore operations pass `recordSuccess: false`: success is already visible through the changed git state (a row moving between the staged/unstaged sections, an entry leaving the tree), so a success entry would only be console noise. Failures are always recorded, regardless of the flag. These same operations also pass `logLevel: 'warn'` so their routine failures log below ERROR; the flag and the level are chosen independently at each call site.

**Recorded output.** A failing spawn records an empty `output` and the message in `error`. Remote ops and commits pass an `extractOutput` (e.g. the commit replays `result.output`, remote ops substitute `<Op> completed.` for an empty string); branch push/delete-remote return the backend's combined stdout+stderr, so `git` progress text (written to stderr) shows up in the popover body.

**A withheld commit is not a result.** A commit whose hooks/signing are suppressed returns `result.suppressed` — a decision request, not a commit — so it opens the Trust/continue dialog and **never** reaches the console. Only a commit that actually ran (success or failure) is recorded, after the flow replays its outcome.

### What stays inline (and does NOT feed the console)

The console owns operation *results* only. These non-result surfaces deliberately remain local:

- The git-status **load** error row in `GitPanel/index.tsx` (fed only by `useGitStatusEvents`).
- The **stash-list load** error inside the stash popover — a read, not a mutation.
- **Branch-list load** and **create-branch** errors in `BranchPicker` (the create-branch section is out of the funnel).
- The per-project commit box's inline error, reserved for DRAFT/GENERATE/VALIDATION failures (e.g. a failed AI generation); a commit *outcome* is a git operation result and goes to the console.

**Modal-embedded errors.** A mutation that runs from inside a blocking `Dialog` keeps a minimal inline error so the failure is visible while the dialog holds the screen (the footer console is behind the modal overlay and unreachable): the Create-tag dialog's `tagError` and the Hard-reset confirmation dialog's `resetError` in `GitHistoryContextMenu.tsx`. The operation is **still** recorded to the console (`tag-create` / `reset`) — the inline text is the modal-scoped echo, not the primary surface. Likewise `BranchPicker` closes on every branch operation *settle* — success **and** failure — so its own overlay never traps the result; the picker's residual local `error` covers only the load/create reads above.

### Zoom-safe sizing

The popover is trigger-anchored, not pointer-tracked, and is rendered through a React portal to `document.body` with `position: fixed` — so the Git panel's `overflow-hidden` ancestor can neither clip it nor scroll under it. Placement mirrors `BudgetCombobox`: the trigger's `getBoundingClientRect()` (VISUAL px) is converted to LAYOUT px with `toLayoutTriggerRect`, the window extent comes from `getLayoutViewport()` (also layout px), and `computeDropdownPosition` returns the fixed rect — opening **upward** (the footer sits at the bottom of the panel, so there is no room below it) and **left-aligned to the trigger**, i.e. up and to the right, then clamped horizontally so the panel always stays inside the window at any UI scale. No pointer coordinate lands in `style.left/top` (`position.left`/`position.top` come from `lib/dropdownPosition`, already layout px), so the zoom cannot displace it.

Its height is capped in absolute layout px — `min(MAX_PANEL_HEIGHT, room above the trigger, window height)` — measured from the panel's own `offsetHeight` and applied inline as `maxHeight`, so the panel never grows past the window or past the trigger. The body is `min-h-0 flex-1 overflow-auto custom-scrollbar` so the cap scrolls rather than overflowing. See [ui-scale.md](ui-scale.md) for the coordinate model.

## Error Handling

- **`fn` rejects**: caught, never rethrown; the message is normalized by `gitOperationErrorMessage` (`err instanceof Error ? err.message : String(err)`), recorded with `ok: false`, and logged. The caller keeps its control flow and its `finally` always runs.
- **No active project**: call sites guard before calling (`if (!projectId) return`), since without one there is nothing to key the record to. The one exception is the file-tree `.gitignore` action (`FileTreeContextMenu.handleAddToGitignore`): with no active project there is no console to key the record to, so it still runs the write and logs its own failure at ERROR.
- **Acknowledging without a record**: `acknowledgeOperation` is a no-op (reference-stable) when the project has no record or is already acknowledged — it never creates a phantom entry.
- **Stale completion**: the project id is captured at call time, so a late completion cannot land in the wrong project's record.
- **Non-string / non-Error shapes**: `defaultExtractOutput` yields `''` for a non-string result; a non-Error rejection still yields a displayable string.

## Invariants

- `runGitOperation` is the **only** writer of an operation record; no UI component mutates `operationByProject` directly.
- A recorded operation result lives in `operationByProject`, keyed by project id (transient, never persisted) — never in component-local state.
- Every record carries a concrete `kind` from the closed `GitOperationKind` union (`unknown` is a sentinel, never passed by a real caller), so the record can never carry an arbitrary op string.
- Failures are **always** recorded; successes are recorded unless the call site opted out with `recordSuccess: false`.
- `runGitOperation` never throws — the outcome is returned as a discriminated `GitOperationOutcome`, never propagated as a rejection.
- Opening the log acknowledges the active project's record; the glyph is neutral while `busy`, when there is no record, or once `acknowledged`.
- A record is dropped only by project deletion (`dropProjectOperation`), never by a panel remount or project switch.
- `selectLastOperation` returns the stored record by reference (or `undefined`) — it allocates nothing, so it is safe as a Zustand selector.
- The popover is portaled to `document.body` and `position: fixed`, placed from the trigger's rect converted to layout px (`toLayoutTriggerRect` + `getLayoutViewport` + `computeDropdownPosition`) and height-capped in layout px — never clipped or scrolled by the panel's `overflow-hidden` ancestor, and correct under the app-wide UI scale.

## Related Specs

- [stores.md](stores.md) — `gitPanelStore` catalog entry (`operationByProject`, `commitByProject`)
- [ui-scale.md](ui-scale.md) — zoom-safety invariant (anchored placement + `--ui-vh` cap)
- [README.md](README.md) — frontend architecture overview
- [events.md](events.md) — how `git:status_changed` reaches the store
- [../../contracts/desktop-frontend.md](../../contracts/desktop-frontend.md) — the git RPC surface the operations call
- [../../contracts/event-catalog.md](../../contracts/event-catalog.md) — `git:status_changed`
- [../workspace.md](../workspace.md) — git subprocess hardening behind every git operation
