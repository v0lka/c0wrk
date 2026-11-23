# Git Changes List

## Role

The changes file list of the Git panel's Changes tab: it splits every `git status` entry along its porcelain axis into three collapsible sections and renders each entry as a stage-toggle row in flat or directory-tree mode.

## Key Files

- `frontend/src/components/GitPanel/ChangesList.tsx` — the list shell: store selectors, the display-root resolution (`focus` path, else workspace path, threaded down), the 3-section split (`classifyEntries`), section defaults, loading/empty states; renders `ChangesToolbar` and `ChangesSummaryBar` above the sections
- `frontend/src/components/GitPanel/ChangesToolbar.tsx` — the Changes-tab toolbar: flat/tree view-mode toggle, bulk stage-all/unstage-all, merge/rebase abort, stash buttons and the Review entry (every mutation it fires is an operation-console kind — see [git-operation-console.md](git-operation-console.md))
- `frontend/src/components/GitPanel/ChangesSummaryBar.tsx` — the change-kind summary strip: modified / deleted / new counters (per unique path, `countChangeKinds`) plus a decorative proportional mix bar; renders nothing when there are no entries
- `frontend/src/components/GitPanel/ChangesList/Section.tsx` — one collapsible section; routes to the flat or tree renderer by `viewMode`
- `frontend/src/components/GitPanel/ChangesList/FlatSection.tsx` — flat rendering with optional sub-group headers (`SortGroupControls` grouping)
- `frontend/src/components/GitPanel/ChangesList/TreeSection.tsx` / `TreeRow.tsx` — directory-tree rendering; `TreeRow` renders file nodes via `GitFileEntry` with `pathDisplay="name-only"`
- `frontend/src/components/GitPanel/ChangesList/buildTree.ts` — display-path computation (via the shared `stripRootPrefix`), tree construction, `collectAllDirPaths` (expand-all)
- `frontend/src/components/GitPanel/GitFileEntry.tsx` — THE row: checkbox, icon, truncating path span, diff stat, status badge; owns the optimistic stage toggle
- `frontend/src/lib/gitStatus.ts` — `classifyEntries` (axis split), axis/badge/conflict helpers, `stripRootPrefix` (display-root stripping shared by row tooltips, tree keys, directory-group labels, and the context menu)
- `frontend/src/lib/gitSortGroup.ts` — `sortEntries`/`groupEntries`/`compareEntries` (D8 sort & group)

## Behavior

### Toolbar and change-kind summary

Above the sections the shell renders `ChangesToolbar` (view-mode toggle, bulk stage/unstage, merge-rebase abort, stash actions, Review entry — all mutations funnel into the operation console) and `ChangesSummaryBar`: three colored counters (modified / deleted / new, dot + count + label) plus a decorative proportional mix bar whose segments share the counters' colors, self-hiding when the total is zero (the list's "No changes" empty state carries that case). Counts are per unique path (`countChangeKinds`), so an `MM` entry counts once rather than once per porcelain axis — the same total the tab strip's Changes badge shows.

### Display root (Git-panel focus)

Every rendered path is shown relative to the panel's **display root**:
`ChangesList.tsx` computes `displayRoot = gitPanelStore.focus?.path ||
workspace_path` (the Git-panel focus root when set, the project workspace
path otherwise) and threads it down through the sections to `GitFileEntry`,
`FlatSection`, the tree builders, and the context menu. `stripRootPrefix`
strips the display root at a path-segment boundary — a sibling directory
merely sharing the prefix is never stripped, a path outside the root stays
absolute. This mirrors the backend, which keys `git status` onto the focus
worktree root (`resolveGitFocusRoot`), so a focused managed tree displays
`src/main.ts` rather than `.worktrees/<name>/src/main.ts`. **Display-only**:
callbacks and payloads (`entry.path` for stage/unstage/discard/open/diff)
always carry the absolute path; the tooltip, row labels, directory-group
labels, tree node keys, and the context menu's `.gitignore` patterns and
operation-console labels are the consumers of the display form.

### Section split (porcelain axis, not exclusive)

`classifyEntries` splits entries into three always-present sections. The split is **by-axis, not exclusive**: a path modified on both axes (`MM`) renders as TWO independent rows — under "Staged Changes" (checked, unstage) and "Changes" (unchecked, stage). Sections carry the axis their rows act on (`side`): Staged Changes → `index`, Changes / Untracked Files → `worktree`.

Section defaults: "Staged Changes" and "Changes" expanded, "Untracked Files" collapsed.

### Row anatomy (`GitFileEntry`)

```
[☐] [icon] <truncating path span>  [+N -M] [badge]
```

- The path span carries `flex-1 truncate` and the native `title` tooltip with the **full display path relative to the panel's display root** (display root stripped at a path-segment boundary — see [Display root](#display-root-git-panel-focus)). The tooltip is the complete path reference in every mode.
- **Flat mode (`pathDisplay="name-first"`)** — the span renders `name` first, then the muted directory: `file.txt path/to/some/dir`. The directory carries NO trailing slash. Because the span truncates at its END, overflow ellipsis consumes the directory tail (`file.txt path/to/s…`) and the file name always stays visible.
- **Tree mode (`pathDisplay="name-only"`)** — the span renders only the basename; the directory structure is carried by the enclosing tree rows. The full-path tooltip stays.
- Untracked rows render the `A` badge rather than the raw `?` marker (git reports untracked as additions).

### Tree expansion

Directory rows toggle via `expandedDirs` — a `Set` of **display-root-relative** directory paths (display root stripped; the same semantics `buildTree` and `collectAllDirPaths` use). A file row renders only when EVERY ancestor directory is expanded. Expand/Collapse All replaces the set atomically.

## Error Handling

Row-level stage toggles flow through `onToggleFile` (the optimistic checkbox / per-row request pump lives in `GitFileEntry`). The git-status load error row is fed by `useGitStatusEvents` and rendered by `GitPanel/index.tsx` — see [git-operation-console.md](git-operation-console.md) for the mutation-log surface.

## Invariants

- The tooltip of a file row is the full display-root-relative path in BOTH modes.
- In flat mode the row text is `name` + `directory` (name first, directory muted, no trailing slash) — overflow truncation consumes the directory, never the name.
- In tree mode the row text is the bare basename.
- A path with changes on both axes renders one row per axis, never one merged row.
- `expandedDirs` keys and tree node paths use display-root-relative paths; callback payloads (`entry.path`) stay absolute.
- Display paths are derived from the display root (Git-panel focus root, else the project workspace path) and are cosmetic: stripping happens only at a path-segment boundary, and staging, discard, open, and diff always receive the absolute path.

## Related Specs

- [stores.md](stores.md) — `gitPanelStore`: `viewMode`, `sortBy`/`groupBy` (persisted), `expandedDirs`, `entries`, `focus`
- [git-operation-console.md](git-operation-console.md) — the footer log of git mutation results (stage/unstage/discard fire from these rows)
- [button-tooltips.md](button-tooltips.md) — the native-`title` tooltip convention this list's path tooltips follow (title, TooltipTrigger, or visible label; hide-capable markup keeps a title)
- [../session-worktrees.md](../session-worktrees.md) — the Git-panel focus model whose focus root doubles as this list's display root (the backend keys status onto the focused tree)
