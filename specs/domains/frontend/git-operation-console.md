# Git Operation Console

## Role

The single surface for the outcome of every user-triggered git mutation in the Git panel: a footer log button whose glyph is tinted by the last operation's result, anchored over a popover that replays the captured output. It replaces the scattered per-feature success banners and error toasts with one per-project record so a result survives panel remounts and project switches.

## Key Files

- `frontend/src/lib/gitOperation.ts` — `runGitOperation`, the single wrapper every call site funnels through; also `GitOperationOutcome`, `gitOperationErrorMessage`, `RunGitOperationArgs`
- `frontend/src/stores/gitPanelStore.ts` — `operationByProject`, `GitOperationRecord`, `GitOperationKind`, `EMPTY_GIT_OPERATION`, `recordGitOperation`/`acknowledgeOperation`/`dropProjectOperation`, `selectLastOperation`
- `frontend/src/components/GitPanel/GitPanelFooter.tsx` — hosts the button + popover, owns open state and the outside-click/Escape dismissal, and acknowledges on open (`toggleLog`)
- `frontend/src/components/GitPanel/GitOperationButton.tsx` — the footer affordance (tint + busy spinner + `aria-expanded`)
- `frontend/src/components/GitPanel/GitOperationPopover.tsx` — the anchored log panel (status header + `<pre>` of captured output)
- Event source: `frontend/src/hooks/useGitStatusEvents.ts` — consumes `git:status_changed` so a console-recorded mutation auto-refreshes the store
- Guards: `frontend/src/lib/gitOperation.test.ts`, `frontend/src/stores/gitPanelStore.test.ts`, `frontend/src/components/GitPanel/GitPanelFooter.test.tsx`, `frontend/src/components/GitPanel/GitOperationButton.test.tsx`, `frontend/src/components/GitPanel/GitOperationPopover.test.tsx`

## Behavior

### One funnel for every result

Every user-triggered git mutation runs through `runGitOperation`, which is the only writer of an operation record. It captures the owning `projectId` at call time, awaits `fn`, and:

- on **success** records `{ kind, label, ok: true, output, error: null }` (when `recordSuccess` is true), where `output` is `extractOutput(result)` — or the result itself when it is a string, else `''`;
- on **failure** records `{ kind, label, ok: false, output: '', error }` with `gitOperationErrorMessage(err)`, and logs via `logger.error`;
- **never rethrows** — a rejected `fn` is captured, so a caller's `try/finally` (busy-flag clearing, dialog dismissal) always runs.

Each record sets a fresh `at` timestamp and `acknowledged: false`, so a new result re-arms the tint.

```
call site ──runGitOperation({projectId, kind, label, fn, extractOutput?, recordSuccess?})──▶ gitPanelStore.recordGitOperation
                                                                                              │  operationByProject[projectId]
                                                                                              ▼
GitPanelFooter ──selectLastOperation(state, activeProjectId)──▶ GitOperationButton ──toggle──▶ GitOperationPopover
       ▲                                                                                             (label + output/error)
       └── toggleLog: opening ⇒ acknowledgeOperation(activeProjectId) ⇒ glyph tints neutral
```

### Button

`GitOperationButton` renders a `Terminal` glyph that, while a remote op is in flight, is replaced by a `Loader2` spinner and disabled. Its color is the only signal of an unread result (`operationTone`):

| State | Tone |
| ----- | ---- |
| `busy` (op in flight) | neutral (`text-muted-foreground`) |
| no record | neutral |
| `acknowledged` | neutral |
| `ok` and unread | `text-success` |
| `!ok` and unread | `text-destructive` |

It carries `aria-label="Git operation log"`, `aria-haspopup="dialog"`, and `aria-expanded={open}`.

### Popover

`GitOperationPopover` is purely presentational — the footer owns open state and dismissal. It renders a dialog (`role="dialog"`, `aria-label="Git operation log"`) with a status header (a `Terminal` / `CheckCircle2` / `XCircle` icon plus the operation `label`, falling back to `kind`, and `No git operations yet` when there is no record) above a scrollable, pre-wrapped `<pre>`:

```
header: [icon] <label>                      ← label.trim() || kind; tinted success/destructive/muted
body:   captured output                     ← output.trim() || error || 'No output'
```

The body prefers the captured `output`; when the operation produced none (a failed spawn records an empty `output` and the message in `error`), it falls back to `error`, then to `No output`.

### Per-project scope

The record lives in the **transient, non-persisted** `operationByProject` map in `gitPanelStore`, keyed by project id. Consequences:

- A result survives GitPanel unmounts (CHAT↔CODE switches) and project switches — switch away and back and the last result is still there.
- A slow operation that completes after a project switch lands in the project captured at call time, never the newly active one.
- Nothing is rehydrated from `localStorage`: a result is live feedback for the running session, not persisted state. `partializeGitPanel` deliberately omits `operationByProject`.
- The record is dropped only by `dropProjectOperation` (project deleted) or a store `reset`.

The active project's record is read via `selectLastOperation(state, activeProjectId)`, which returns the stored record **by reference** (or `undefined` for a null/undefined/unknown project). The popover is rendered on every tab because `GitPanelFooter` is the shared footer for the whole panel.

### Acknowledge semantics

Opening the log is the acknowledgement. `GitPanelFooter.toggleLog` calls `acknowledgeOperation(activeProjectId)` as it opens the popover, flipping `acknowledged: true`, so the glyph drops to neutral and an open log reads as "seen" rather than "unread result". Closing and reopening does not re-acknowledge; `acknowledgeOperation` is a reference-stable no-op when the project has no record or the flag is already set (it never fabricates an entry just to flip a flag). A newly recorded operation resets `acknowledged` to false, re-arming the tint.

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

**Silent successes.** The low-level staging/gitignore operations pass `recordSuccess: false`: success is already visible through the changed git state (a row moving between the staged/unstaged sections, an entry leaving the tree), so a success entry would only be console noise. Failures are always recorded, regardless of the flag.

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

The popover is anchored, not pointer-tracked: it is absolutely positioned by its relatively-positioned footer wrapper (`absolute bottom-full right-0`), so no pointer coordinate is written into `style.left/top` and the UI-scale zoom cannot displace it. Its height is capped through the zoom-corrected `--ui-vh` primitive — `max-h-[calc(var(--ui-vh)*0.5)]` — so it stays inside the visible window at any UI scale (a raw `50vh` would be magnified past the window). The body is `min-h-0 flex-1 overflow-auto` so the cap scrolls rather than overflowing. See [ui-scale.md](ui-scale.md) for the coordinate model.

## Error Handling

- **`fn` rejects**: caught, never rethrown; the message is normalized by `gitOperationErrorMessage` (`err instanceof Error ? err.message : String(err)`), recorded with `ok: false`, and logged. The caller keeps its control flow and its `finally` always runs.
- **No active project**: every call site guards before calling (`if (!projectId) return`); without one there is nothing to key the record to.
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
- The popover is anchored to the footer wrapper (no pointer coordinate) and capped in `--ui-vh`, so it is correct under the app-wide UI scale.

## Related Specs

- [stores.md](stores.md) — `gitPanelStore` catalog entry (`operationByProject`, `commitByProject`)
- [ui-scale.md](ui-scale.md) — zoom-safety invariant (anchored placement + `--ui-vh` cap)
- [README.md](README.md) — frontend architecture overview
- [events.md](events.md) — how `git:status_changed` reaches the store
- [../../contracts/desktop-frontend.md](../../contracts/desktop-frontend.md) — the git RPC surface the operations call
- [../../contracts/event-catalog.md](../../contracts/event-catalog.md) — `git:status_changed`
- [../workspace.md](../workspace.md) — git subprocess hardening behind every git operation
