# Git Changes List

## Role

The changes file list of the Git panel's Changes tab: it splits every `git status` entry along its porcelain axis into three collapsible sections and renders each entry as a stage-toggle row in flat or directory-tree mode.

## Key Files

- `frontend/src/components/GitPanel/ChangesList.tsx` — the list shell: store selectors, the 3-section split (`classifyEntries`), section defaults, loading/empty states
- `frontend/src/components/GitPanel/ChangesList/Section.tsx` — one collapsible section; routes to the flat or tree renderer by `viewMode`
- `frontend/src/components/GitPanel/ChangesList/FlatSection.tsx` — flat rendering with optional sub-group headers (`SortGroupControls` grouping)
- `frontend/src/components/GitPanel/ChangesList/TreeSection.tsx` / `TreeRow.tsx` — directory-tree rendering; `TreeRow` renders file nodes via `GitFileEntry` with `pathDisplay="name-only"`
- `frontend/src/components/GitPanel/ChangesList/buildTree.ts` — display-relative path computation, tree construction, `collectAllDirPaths` (expand-all)
- `frontend/src/components/GitPanel/GitFileEntry.tsx` — THE row: checkbox, icon, truncating path span, diff stat, status badge; owns the optimistic stage toggle
- `frontend/src/lib/gitStatus.ts` — `classifyEntries` (axis split), axis/badge/conflict helpers
- `frontend/src/lib/gitSortGroup.ts` — `sortEntries`/`groupEntries`/`compareEntries` (D8 sort & group)

## Behavior

### Section split (porcelain axis, not exclusive)

`classifyEntries` splits entries into three always-present sections. The split is **by-axis, not exclusive**: a path modified on both axes (`MM`) renders as TWO independent rows — under "Staged Changes" (checked, unstage) and "Changes" (unchecked, stage). Sections carry the axis their rows act on (`side`): Staged Changes → `index`, Changes / Untracked Files → `worktree`.

Section defaults: "Staged Changes" and "Changes" expanded, "Untracked Files" collapsed.

### Row anatomy (`GitFileEntry`)

```
[☐] [icon] <truncating path span>  [+N -M] [badge]
```

- The path span carries `flex-1 truncate` and the native `title` tooltip with the **full workspace-relative display path** (workspace root stripped at a path-separator boundary). The tooltip is the complete path reference in every mode.
- **Flat mode (`pathDisplay="name-first"`)** — the span renders `name` first, then the muted directory: `file.txt path/to/some/dir`. The directory carries NO trailing slash. Because the span truncates at its END, overflow ellipsis consumes the directory tail (`file.txt path/to/s…`) and the file name always stays visible.
- **Tree mode (`pathDisplay="name-only"`)** — the span renders only the basename; the directory structure is carried by the enclosing tree rows. The full-path tooltip stays.
- Untracked rows render the `A` badge rather than the raw `?` marker (git reports untracked as additions).

### Tree expansion

Directory rows toggle via `expandedDirs` — a `Set` of **display-relative** directory paths (workspace root stripped; the same semantics `buildTree` and `collectAllDirPaths` use). A file row renders only when EVERY ancestor directory is expanded. Expand/Collapse All replaces the set atomically.

## Error Handling

Row-level stage toggles flow through `onToggleFile` (the optimistic checkbox / per-row request pump lives in `GitFileEntry`). The git-status load error row is fed by `useGitStatusEvents` and rendered by `GitPanel/index.tsx` — see [git-operation-console.md](git-operation-console.md) for the mutation-log surface.

## Invariants

- The tooltip of a file row is the full workspace-relative path in BOTH modes.
- In flat mode the row text is `name` + `directory` (name first, directory muted, no trailing slash) — overflow truncation consumes the directory, never the name.
- In tree mode the row text is the bare basename.
- A path with changes on both axes renders one row per axis, never one merged row.
- `expandedDirs` keys and tree node paths use display-relative paths; callback payloads (`entry.path`) stay absolute.

## Related Specs

- [stores.md](stores.md) — `gitPanelStore`: `viewMode`, `sortBy`/`groupBy` (persisted), `expandedDirs`, `entries`
- [git-operation-console.md](git-operation-console.md) — the footer log of git mutation results (stage/unstage/discard fire from these rows)
- [button-tooltips.md](button-tooltips.md) — the native-`title` tooltip convention this list's path tooltips follow (title, TooltipTrigger, or visible label; hide-capable markup keeps a title)
