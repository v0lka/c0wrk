# Row Action Overlay (`ItemAction` / `ItemActions`)

## Role

Shared hover-overlay primitives for list-row actions: `ItemAction` is one icon button, `ItemActions` is the absolutely-positioned gradient panel that slides over the right edge of a row and reveals its buttons on hover/focus. One implementation keeps every list surface visually and behaviorally in sync.

## Key Files

- `frontend/src/components/layout/ItemAction.tsx` — both primitives; no other file defines row-action buttons
- `frontend/src/components/layout/ProjectSelector.tsx` — project dropdown rows (Rename, Delete)
- `frontend/src/components/layout/SessionListItem.tsx` — session rows for both surfaces: `SessionSelector` (dropdown, CODE mode) and `SessionList` (flat, CHAT mode); the `variant` prop selects the outer element (`dropdown` → `DropdownMenuItem`, `flat` → `div[role=button]`); actions: Promote to project (only when the row's `project_id` is `__no_project__` AND the caller wired `onPromote` — CODE-mode selectors pass no callback, busy-gated with a promote-specific `disabledReason`), Pin/Unpin, Fork (busy-gated), Rename, Archive/Unarchive, Delete
- `frontend/src/components/layout/SessionGroupedList.tsx` — worktree branch rows of the CODE-mode grouped session selector's Worktrees section: the overlay's children are four nested **registered** `DropdownMenuItem`s (see the nested-items shape under Hover overlay mechanics); actions: New session in this worktree (+), Pin/Unpin group (the GROUP-level pin — `pinnedGroupsStore`, persisted per project; a pinned branch floats above the unpinned groups of its section via `buildSessionGroups`' `pinnedBranches` ordering, member sessions' own pins untouched; the pinned row also carries an always-visible `Pin` badge before the branch name, mirroring the per-session pinned-row badge), Archive group, Delete group
- `frontend/src/components/layout/SessionSelector.tsx` — archived branch rows of the same selector's Archived section: the same nested-items shape with a THREE-action overlay — Pin/Unpin group (the same GROUP-level pin, keyed by the branch name in both sections), Restore (unarchive) group, Delete group
- `frontend/src/components/GitPanel/LocalBranchRow.tsx` — local branch rows (Push, Merge, Rebase, Rename, Delete); the session-pinned branch (managed worktree, ADR-080) renders NO Delete action at all and a disabled Rename — see [session-worktrees.md](../session-worktrees.md)
- `frontend/src/components/GitPanel/RemoteBranchRow.tsx` — remote branch rows (Delete on remote)
- `frontend/src/components/settings/ThemeMenuItem.tsx` — theme menu rows (Delete theme; predefined themes render no actions)
- `frontend/src/components/ui/ModelPickerMenu.tsx` — model picker menu rows (Copy model name). The action lives in a per-row internal component, `CopyModelNameAction`, that owns its own `useCopyFlash` instance — so copying one row never flashes a sibling — and copies the row's full composite `provider/name` id; the row's native `title={entry.id}` restores what the row's `truncate` clipping hides (see [button-tooltips.md](button-tooltips.md)); the Default row carries neither title nor actions
- `frontend/src/components/chat/BookmarksPanel.tsx` — bookmark rows (Rename, Delete)
- `frontend/src/components/research/ResearchProjectPicker.tsx` — research-project rows (Pin/Unpin, Delete)
- `frontend/src/components/research/ResearchHypothesisPicker.tsx` — hypothesis menu rows (Pin/Unpin)
- `frontend/src/components/layout/ItemAction.test.tsx` — pins the button feedback classes, the click/disabled behavior, and the title contract

## Behavior

### Tooltip contract

The **native `title` attribute is the only tooltip mechanism** for row-action buttons — no Radix Tooltip, no portal, no animation, no `TooltipProvider` requirement (tests render `ItemAction` bare). An enabled button carries `title = label`; the label text comes from the calling site (`label` prop).

A disabled button (`pointer-events-none`) never receives pointer events, so its own `title` would never show. The `disabledReason` is therefore mirrored as the `title` of the focusable wrapper `span` (keyboard users focus the span via its `tabIndex=0`; hovering the span shows the reason). The reason is also mirrored onto the button's own `title` for assistive tech. Because the wrapper span is the tab stop while disabled and its disabled button contents are excluded from accessible-name computation, the span also carries `aria-label = "label: reason"` — the action keeps an accessible name exactly while it is disabled. `disabled` without `disabledReason` leaves the wrapper's title and aria-label unset and the button's `title` at `label`.

### Consumers

| Surface | File | Actions |
| ------- | ---- | ------- |
| Project selector rows | `ProjectSelector.tsx` | Rename, Delete |
| Session rows (dropdown + flat) | `SessionListItem.tsx` | Promote to project (No Project rows only, busy-gated), Pin/Unpin, Fork (busy-gated), Rename, Archive/Unarchive, Delete |
| Worktree branch rows (grouped session selector) | `SessionGroupedList.tsx` | New session in this worktree (+), Pin/Unpin group, Archive group, Delete group |
| Archived branch rows (Archived section) | `SessionSelector.tsx` | Pin/Unpin group, Restore group (no confirmation), Delete group (always confirms) |
| Local branch rows | `LocalBranchRow.tsx` | Push, Merge, Rebase, Rename, Delete (Delete absent and Rename disabled on the session-pinned branch — managed worktree pin, ADR-080) |
| Remote branch rows | `RemoteBranchRow.tsx` | Delete on remote |
| Theme menu rows | `ThemeMenuItem.tsx` | Delete theme |
| Model picker menu rows | `ModelPickerMenu.tsx` | Copy model name (per-row `useCopyFlash` flash) |
| Bookmark rows | `BookmarksPanel.tsx` | Rename, Delete |
| Research-project rows | `ResearchProjectPicker.tsx` | Pin/Unpin, Delete |
| Hypothesis menu rows | `ResearchHypothesisPicker.tsx` | Pin/Unpin |

### Hover overlay mechanics

`ItemActions` is `absolute inset-y-0 right-0` with a leftward `from-popover` gradient so the underlying relative-time text stays readable; `opacity-0` → `group-hover/item:opacity-100` / `group-focus-within/item:opacity-100`; the parent row must be `relative` and carry the `group/item` class. Pointer events are contained (`stopPropagation` on pointerdown/pointerup/click) so clicking an action never selects the row — this stopPropagation is load-bearing for Radix menu rows (deleting a theme or a branch must not also select the row). Keyboard activation is contained too: `ItemActions` stops `keydown` bubbling for Enter/Space, because Radix menu items fire on bubbled events — without the stop, activating an overlay action would also re-fire the parent row's `onSelect` (e.g. re-toggling the branch group); arrow keys still bubble so the menu's roving focus keeps working.

Two overlay shapes exist:

- **Plain buttons** (the default) — `ProjectSelector`, `SessionListItem`, the Git panel rows, `ThemeMenuItem`, `BookmarksPanel`, the research pickers.
- **Nested registered Radix menu items** (`SessionGroupedList` branch rows) — the overlay sits inside a `DropdownMenuItem` and its children are `DropdownMenuItem`s themselves: Radix registers them in the same roving-focus collection, so arrow keys traverse toggle → actions and Enter/Space activate them like any other menu item. The row's `onSelect` preventDefaults (collapse toggle), each action decides its own close semantics (`+` closes, pin preventDefaults).

## Invariants

- Every row-action button in the app renders through `ItemAction`; no surface builds its own hover-overlay action button.
- The tooltip is always the native `title` — Radix Tooltip must not be re-introduced here; app-root `TooltipProvider` is not required to render these components.
- A disabled action's `disabledReason` is reachable without pointer events: it sits on the focusable wrapper span's `title` (and on the button's own `title`), and the wrapper span's `aria-label` keeps the action named (`"label: reason"`) for assistive tech.
- `ItemAction` buttons keep their own hover/active feedback (`transition-colors`, `enabled:hover:bg-accent/20`, `enabled:active:bg-accent/30`) because the overlay only reveals them on row hover while the row owns the pointer cursor.
- `ItemActions` never lets a click reach the parent row (stopPropagation on pointerdown/pointerup/click), nor an Enter/Space keypress (the keydown stop) — in Radix-menu rows the overlay's actions are nested **registered** menu items, keyboard-reachable in the same roving-focus collection, and their activation must never re-fire the parent row's `onSelect`.
- A row hosting `ItemActions` is `relative` and `group/item`.

## Related Specs

- [button-tooltips.md](button-tooltips.md) — the project-wide button tooltip convention and its AST guard; this overlay's native-title contract is one compliant shape under it (title, TooltipTrigger, or visible label)
- [Frontend README](README.md) — sidebar layout (project/session selectors), design system
- [ui-scale.md](ui-scale.md) — zoom-safety rules that `ItemActions`' absolute positioning respects (layout px, no viewport units)
