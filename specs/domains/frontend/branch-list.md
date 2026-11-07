# Branch Lists (BranchDropdown / BranchPicker)

## Role

The Local/Remote branch lists of the Git panel's two picker surfaces — the
toolbar `BranchDropdown` (dropdown menu) and the modal `BranchPicker`
(checkout dialog, which also serves the chat-side draft worktree-pick mode) —
rendered by one shared `BranchList` component in flat or `/`-slash
directory-tree display mode.

## Key Files

- `frontend/src/components/GitPanel/BranchList.tsx` — the shared list: local/remote kind split, name filter, empty state; hands each section to `BranchSection` with the memoized by-name maps and built trees; `variant: 'menu' | 'dialog'`
- `frontend/src/components/GitPanel/BranchListRows.tsx` — `BranchSection`: one section's header chrome + flat/tree row renderers (leaf rows through `LocalBranchRow`/`RemoteBranchRow`, dir rows through `CollapsibleDirRow`); the `BranchDraftSelect` row-handler contract
- `frontend/src/components/GitPanel/CollapsibleDirRow.tsx` — the collapsible directory row shared with the Changes list's `TreeRow`: chevron + folder, click or Enter/Space toggle, `role="button"` + `tabIndex` reachability, `depth * 12px` indent; the label font is the consumer's (`text-xs` in the branch list for leaf parity, `text-sm` in the Changes list)
- `frontend/src/components/GitPanel/BranchDropdown.tsx` — the menu surface: toolbar dropdown with a display-mode toggle + search input on one row; renders its own always-flat Worktrees section beside the list
- `frontend/src/components/GitPanel/BranchPicker.tsx` — the dialog surface: filter row with the display-mode toggle; one component serves switch mode and draft pick mode
- `frontend/src/components/GitPanel/LocalBranchRow.tsx` / `RemoteBranchRow.tsx` — the leaf rows (checkout/rename/merge/rebase/push/delete context-menu actions, draft selection, managed-session pins and focus gates)
- `frontend/src/lib/branchTree.ts` — `buildBranchTree`: implicit `/`-segment directory nodes, the leaf/dir collision payload, dirs-first alphabetical sorting; `branchLeafLabel`: the tree leaf's visible label (the last name segment)
- `frontend/src/stores/gitPanelStore.ts` — the persisted display preference `branchListTree` + `setBranchListTree`; scalar-validated on rehydrate by `mergeGitPanel`

## Behavior

### Two surfaces, one list

Both surfaces split `branches` by `kind`, apply a full-name filter, and render
the Local → Remote sections through `BranchList`; only the chrome differs:

- **`variant="menu"`** (BranchDropdown, inside `DropdownMenuContent`):
  collapsible `CollapsibleSectionHeader` headers (arrow-reachable; the
  roving-focus collection starts at the display-mode toggle), a
  `DropdownMenuSeparator` before Remote, bare (unwrapped) rows.
- **`variant="dialog"`** (BranchPicker): static uppercase `<li>` label headers
  inside a single `<ul>`, `<li>`-wrapped rows, no separators.

The Worktrees section is NOT part of `BranchList` — each surface renders its
own section and it stays flat in both display modes (a worktree has no branch
namespace).

### Display mode (flat ⇄ tree)

The display preference is `gitPanelStore.branchListTree` (default **false =
flat**), written through `setBranchListTree`. It persists in the
`git-panel-settings` localStorage entry via `partializeGitPanel`;
`mergeGitPanel` accepts only a real boolean — a legacy entry without the key,
or any non-boolean value (stringified `"true"`, a number, `null`), rehydrates
to the flat default.

Each surface hosts one toggle on its search/filter row, showing the mode it
switches TO (`FolderTree` when flat, `List` when tree), titled
`"Show branch tree"` / `"Show flat list"` — and BOTH announce the current
mode to assistive tech, so a screen-reader user never has to guess it:

- the menu toggle is a registered `DropdownMenuCheckboxItem` (`checked` =
  the tree mode, so `aria-checked` carries the state; the wrapper's check
  indicator is suppressed — the glyph already shows the mode) whose
  `onSelect` calls `event.preventDefault()` — flipping the mode never
  dismisses the dropdown;
- the dialog toggle is a plain button with `aria-pressed` and
  `data-testid="branch-view-toggle"`.

Both toggles write the SAME persisted flag, so the two surfaces always render
the same mode (flipping it in one is instantly visible in the other); the
choice survives open/close cycles and app restarts (localStorage).

With tree on, each section renders `buildBranchTree` over the section's
**visible** (post-filter) names:

- `/`-separated branch segments become directory rows (the shared
  `CollapsibleDirRow`: chevron + folder icon, `role="button"` +
  `tabIndex="0"` with an Enter/Space twin of the click toggle — there is no
  owning `role="tree"`, so `treeitem` would be orphaned ARIA;
  `aria-expanded`, `data-testid="branch-dir-<fullPath>"`); children indent
  by `depth * 12px`.
- A remote-tracking ref's remote prefix (`origin`) emerges as the top-level
  directory of the Remote section.
- A leaf's visible label is the last `/`-segment of its name
  (`branchLeafLabel`): `origin/ui/branch-grouping` renders as
  `branch-grouping` beneath the `origin` → `ui` dir rows — the Changes
  list's name-only convention; flat mode renders full names. The accessible
  name (`aria-label`) stays the FULL name, so same-named leaves in sibling
  dirs stay distinguishable to assistive tech. The dir row's label uses the
  leaf font size (`text-xs`) — only the muted color marks the row as a
  container.
- A name that is both a leaf and a directory prefix (`feature` +
  `feature/login`) renders ONCE — as the directory's own `leaf` payload,
  shown inside the expanded dir row; no duplicate sibling leaf is created.
- Sorting is directories first, then leaves, alphabetical within each group —
  the same convention as the Changes list's tree.

### Search and collapse

- Filtering feeds only matching names into the tree: a directory whose rows
  all fail the filter is pruned, never rendered empty.
- An ACTIVE search force-expands every section and directory
  (`searchActive`): matching leaves hidden inside collapsed dirs would read as
  broken filtering. Clearing the filter restores the user's collapse state.
- Sections (`local` / `remote`) and directories (`dir:<fullPath>` keys)
  collapse through `useCollapsibleSections` — ephemeral, component lifetime.
- The empty state (`data-testid="branch-list-empty"`) shows
  "No matching branches" when the filter matches nothing and "No branches"
  when the store has no branches at all (suppressible via `hideEmptyState`).

### Payload names

Row callbacks always receive the FULL branch name — local rows the branch
name, remote rows the full remote-tracking ref (`origin/feat/x`) — in BOTH
display modes; tree display is cosmetic (the leaf's visible label is the
basename, while hover-action labels, tooltips and the accessible name keep
the full name), exactly like the Changes list's display paths. In draft pick
mode the row click flows through
`draftSelect.onSelect(fullName)` unchanged, so the picker's decision payload
(`{kind, branch, createBranch, startPoint}`) is mode-independent.

## Error Handling

`BranchList` owns no async behavior — every per-row state (in-flight action,
disabled, pinned, blocked reason, draft selection) arrives as a function of
the full branch name and is threaded through verbatim; the surfaces' own
load/error chrome is theirs (BranchPicker's fetch-error footer, the dropdown's
closed-when-error toolbar behavior).

## Invariants

- One list implementation, two surfaces: `BranchList` owns the kind split,
  filter, tree building, and empty state; a surface adds only chrome.
- `branchListTree` defaults to false (flat); a legacy localStorage entry
  without the key and any non-boolean value both rehydrate to flat.
- Row payloads are always full branch names / full remote-tracking refs —
  never the tree's display basename or a directory path.
- A tree leaf's visible label is the basename (`branchLeafLabel`); its
  accessible name (`aria-label`), hover-action labels and tooltips stay the
  full branch name / remote ref.
- Tree mode is built from filtered (visible) names only; a directory never
  renders without at least one matching leaf beneath it (including the
  collision `leaf` payload).
- An active search force-expands every section and directory; without an
  active search the user's collapse state governs.
- The menu display-mode toggle never closes the dropdown; both surfaces'
  toggles drive the one persisted `branchListTree` flag, so the mode is
  shared across surfaces and survives restarts.

## Related Specs

- [stores.md](stores.md) — `gitPanelStore`: the persisted `branchListTree` flag + `setBranchListTree`
- [git-changes-list.md](git-changes-list.md) — the sibling Changes-list tree whose dirs-first sorting and display-vs-payload split this list mirrors
- [../../decisions/091-eager-session-creation.md](../../decisions/091-eager-session-creation.md) — the draft pick mode the dialog variant serves
- [../../decisions/080-session-worktrees.md](../../decisions/080-session-worktrees.md) — the managed-session pins and focus gates threaded through the local rows
