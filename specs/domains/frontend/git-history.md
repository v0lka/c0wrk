# Git History

## Role

The commit-history surfaces: the Git panel's History tab — a compact, virtualized commit list with per-commit changed-file stats and a client-side file filter — and the file viewer's history page (`c0wrk:graph` pseudo-tab) — the lane layout with richer expandable commit cards: full commit message, changed-file list and per-file inline diffs. The two views share one paginated data source and one graph-layout engine; only the card presentation differs.

## Key Files

- `backend/frontend_api_git.go` — `GetGitHistory` (unified history+graph+stats page), `GetCommitMessage` (full commit message, 64 KiB cap), `GetCommitFiles`/`GetCommitFilesBatch` (changed files)
- `backend/frontend_api_review.go` — `GetCommitDiff` (per-file commit diff in the review format)
- `core/workspace/types.go` — `CommitStats`, `GitHistoryCommit`, `GitHistoryPage`
- `frontend/src/api/git.ts` — `getGitHistory` (page size `GIT_HISTORY_PAGE_SIZE = 300`), `getCommitMessage`
- `frontend/src/hooks/useGitHistory.ts` — the shared paginated loader (append-only accumulation, idempotent `loadMore`, epoch guard)
- `frontend/src/hooks/useCommitFiles.ts` — the shared changed-files cache (`fetchFiles`/`fetchMany`, deduped against cache + in-flight)
- `frontend/src/hooks/useGitHistoryFilter.ts` — the client-side file filter (decides WHEN to fetch, uses the shared files cache)
- `frontend/src/lib/commitStats.ts` — `statBucket`/`countFileStats`/`statColorClass`/`totalChanges` (client-side mirror of the backend bucketing)
- `frontend/src/components/GitPanel/GitHistoryTab.tsx` — the compact view: virtualizer, history button, filter bar, context menu
- `frontend/src/components/GitPanel/GitHistoryRow.tsx` — the inert fixed-height commit row (stats cell, SHA click)
- `frontend/src/components/GitPanel/GitGraphGutter.tsx` + `frontend/src/lib/gitGraphLayout.ts` + `frontend/src/components/GitPanel/gitGraphRender.ts` — the shared lane-graph engine (`ROW_SPACING = 32`, `NODE_OFFSET = 11`, `LANE_SPACING = 22`)
- `frontend/src/components/gitGraph/GitGraphPage.tsx` — the viewer page: header + diff-mode toggle, expansion state, lazy fetches
- `frontend/src/components/gitGraph/GitGraphCard.tsx` — the expandable commit card (presentational; all state via props)
- `frontend/src/components/gitGraph/gitGraphCardHeights.ts` — the deterministic height + diff-slice math (pure, no React/DOM)
- `frontend/src/components/gitGraph/CommitFileDiff.tsx` — one file's diff body (review tables + highlight.js, capped scroll container)
- `frontend/src/components/diff/DiffViewModeToggle.tsx` — the shared unified/split toggle bound to `reviewStore.diffViewMode`
- `frontend/src/stores/gitPanelStore.ts` — `GIT_GRAPH_TAB_PATH` (`'c0wrk:graph'`), `pendingHistoryFilter`

## Behavior

### Data source (shared by both views)

`useGitHistory` calls `getGitHistory` (`GetGitHistory`) with a fixed page size of 300. The first page loads eagerly; `loadMore` requests the page at the previous `next_skip` and appends — never replaces — so already-rendered rows keep identity and per-row UI state. `loadMore` is idempotent: an in-flight ref plus the `hasMore` flag make repeated invocations (scroll edge trigger, button clicks) harmless. A reload bumps an epoch token so an in-flight page from the previous epoch can never append stale commits after a refresh.

`GetGitHistory` returns `GitHistoryPage{Commits, NextSkip, HasMore}`; `HasMore` is page saturation (`len == limit`), `NextSkip = skip + len(Commits)`. Each `GitHistoryCommit` carries the log fields (author/email/date/subject), the graph topology (`parents`, parsed `refs` decorations) and `stats` — the file-change buckets counted from `--name-status`: `A` → added, `D` → deleted, any other letter (M/R/C/T with similarity scores bucketed by their leading letter) → modified. Rename detection is pinned with an explicit `-M` in the invocation — a rename reports a single R entry → modified, matching `GetCommitFiles` and independent of the ambient `diff.renames` config. Merge commits always report zero stats (git records no per-file name-status for them); the root commit is diffed against the empty tree (`--root`) so its files count. The wire field is typed optional on the frontend (`stats?: CommitStats`) so a stale-bindings backend that omits it degrades to stats-less rows instead of breaking.

### Compact representation (Git panel History tab)

A vertical list of fixed-height commit rows (`ROW_SPACING = 32` px, two lines). Rows are virtualized (`@tanstack/react-virtual`, constant `estimateSize`, SHA keys, overscan 8); only visible rows mount. There is NO lane graph in the panel — the SVG lane gutter renders exclusively in the viewer's history page, which keeps the compact rows full-width in every mode (filtered or not).

Row anatomy (line 1): truncating commit message (selectable), ref chips (`tag:` prefix stripped, `refColor` tint — HEAD → highlight, tag → warning, branch → the theme's keyword purple `text-hljs-keyword`), the stats cell `+A ~M -D` colored `success`/`warning`/`destructive` with a native `title` spelling the triple out — the cell is omitted entirely when the backend sent no stats — and the short SHA. Line 2: `author · relative time`. The row body is inert: the only interactive element is the short SHA (a `role="button"` span with Enter/Space handling) which opens `c0wrk:commit:<sha>` — the read-only commit-diff review page in the file viewer. Hovering a row shows a tooltip with the full date and the commit subject; right-clicking opens the commit context menu (tag management, reset-to-commit; its mutations trigger a history reload).

The toolbar row carries the history button and the shared `FilterBar` (glob/regex file filter, same matcher semantics as the file-tree filter). The history button — icon-only, so its `title` is its only label ("Open history viewer") — opens the `c0wrk:graph` pseudo-tab in the file viewer as a VIRTUAL tab (`openVirtualFile`, the canonical `openResearch`/`openPaper` pattern) and expands the viewer (`setCollapsed(false)`), moving the full-width history (lane graph, expandable cards) out of the panel. The virtual flag is what keeps the viewer's data layers away from the pseudo-path: the active-file loader never fires a ReadFile RPC for it, the coalesced `workspace:tree_changed` reload skips it, and the persisted-snapshot partialize excludes it (a restart never rehydrates it).

The filter is CLIENT-SIDE over the loaded window: it never queries the backend and only sees commits paged in so far — loading more pages extends the match set. To match, every loaded commit's changed files must be known: uncached SHAs are fetched once per valid-filter activation through the shared `useCommitFiles` cache via `GetCommitFilesBatch` (one batched RPC, deduped per SHA), with a "Resolving files…" indicator; invalid regex renders "Invalid regex", no matches renders "No matching commits". An external filter intent (`gitPanelStore.pendingHistoryFilter`, e.g. "View History" on a file) is consumed once on arrival.

### Full representation (viewer Git-graph page)

`FileViewerContent` renders `GitGraphPage` when the active file is `GIT_GRAPH_TAB_PATH` (`'c0wrk:graph'`, a synthetic pseudo-tab — no session and no disk file involved). The tab bar renders the tinted `GitGraph` glyph directly for it — `TabFileIcon`/`getFileIcon` never runs for it and no `fileIcons` cache entry is created (the icon hook guards on the whole `c0wrk:` pseudo-path prefix) — and the tab title is pinned by `fileNameFromPath` (`'c0wrk:graph'` → "Git History"; the literal is inlined in the lib so it stays store-free).

The page toolbar carries NO title (the viewer tab already names the page): only the shared `FilterBar` file filter (same client-side glob/regex semantics as the panel — the gutter hides while filtering, "Invalid regex" / "No matching commits" / "Resolving files…" render as in the panel) and the shared `DiffViewModeToggle` (unified/split), which reads and writes `reviewStore.diffViewMode` — one app-wide, persisted preference shared with the review page; callers never thread the mode through props, and the mode is part of every height computation.

Right-clicking a card opens the commit context menu — the SAME `GitHistoryContextMenu` component instance pattern the panel's History tab renders (View Commit, Copy SHA, Create › Branch/Tag, Reset `<branch>`, per-tag actions); the page owns its own instance fed by the card's SHA/refs/pointer position, takes `currentBranch` from `gitPanelStore`, and its mutations trigger a history `reload`.

The lane gutter and pagination behave exactly as in the compact view. The difference is the card: a `GitGraphCard` is the same two-line collapsed look (chevron, message, refs, short SHA, author · time, stats badges) plus a chevron-driven inline expansion. Exactly ONE card is expanded at a time. The expansion renders:

1. **Full commit message** — lazily fetched via `getCommitMessage` on first expansion (one fetch per SHA, cached, in-flight deduped), rendered as a MARKDOWN QUOTE: every line is `> `-prefixed and passed through the shared `Markdown` component (sanitized pipeline; `.prose blockquote` styling) inside the same capped scrollable box — the box height stays the deterministic line-count estimate from `gitGraphCardHeights` (capped, scrolls internally, determinism over pixel-perfection). Loading and failure render fixed-height placeholder lines.
2. **Changed files** — from the shared `useCommitFiles` cache (status letter + path rows). Loaded-empty lists render one empty-state line whose text matches the backend's behavior: "No diff (merge commit)" (diff-tree emits no merge patch), "No files (root commit)" (no parent to diff against), otherwise "No files". Right-clicking a file row opens the per-file context menu (`CommitFileContextMenu`): a single **"Open in Viewer"** entry that opens the file's version AT THE COMMIT — the `c0wrk:commit:<sha>/<path>` virtual tab rendering a read-only review narrowed to that file (its commit diff), never the current working-tree copy. Left-click keeps toggling the inline diff.
3. **Per-file inline diffs** — each file row toggles its own diff body. The commit's diff is fetched ONCE per SHA via `getCommitDiff` (cached, in-flight deduped) and sliced per file by `sliceDiffByPath` (post-commit path first, `old_path` fallback so a renamed file listed by destination still finds its hunks). The slice renders through the review page's tables (`UnifiedDiffTable`/`SplitDiffTable`) with highlight.js per-file language detection, inside an explicit-height, internally scrolling container.

Stats badges on the card prefer the backend triple; when the backend omitted it and the file list is loaded, the stats are derived client-side via `countFileStats` (same bucketing). The badge line renders only when a triple is available AND at least one file changed (`totalChanges > 0`) — a merge commit's zero triple renders no badges — and while files are loading with no backend stats present, none renders either.

### Deterministic heights

Every expansion height is a pure function of the card's UI state (`gitGraphCardHeights.ts` — no React, no DOM measurement anywhere). The message block is `pad(8) + 16px × line count` capped at 96px (longer messages scroll inside); a placeholder message block is 24px. A file row is 16px; an expanded file's diff body is `min(240, rows × 19.5)` where the row count mirrors the rendered tables exactly — unified = one row per raw hunk line, split = the `buildSideBySidePairs` count (the mode is part of the height state, so toggling it recomputes heights); loading/failed/hunk-less bodies are a 24px placeholder. The loading-files line is 19px, an empty-state line 19px. The expansion is `message + 4px + files + 6px`; a card row's reserved height is `ROW_SPACING + expansion`.

Both consumers of height — the virtualizer (`estimateSize` = the exact computed height) and the lane gutter — read the same numbers, so edges route around expanded cards and scroll offsets stay aligned. The card renders each section with an explicit pixel height derived from the same constants, so DOM height ≡ computed height. Soft line-wrapping inside a diff row can only make a section scroll internally within its cap; it can never grow a row beyond its reserved height.

### Lazy RPCs

Nothing beyond the current history page is fetched eagerly. Changed files are fetched when a card expands or when a valid filter needs them (batched); the full message only on first expansion; the commit diff only when a file inside an expanded card is toggled. Each resource is fetched at most once per SHA per page instance (cache + in-flight dedupe); collapse and re-expand do not refetch. The expand path itself is single-channeled: BOTH an interactive expand and a session-restored expansion fire the same deduped lazy fetches from the page's restore effect — a persisted SHA triggers fetches only once it is present in the loaded history (a SHA beyond the first page stays dormant until `loadMore` brings it in; a SHA absent from the history fetches nothing).

### Expansion persistence (app session)

The expand/collapse decisions live OUTSIDE the page in `gitGraphStore` — an in-memory, non-persisted Zustand store keyed per project (`byProject`: the single `expandedSha` plus per-commit sets of open file paths). Scope: the whole APPLICATION SESSION — closing/reopening the `c0wrk:graph` tab, collapsing the viewer, switching projects (and back) or navigating sessions keeps the expansion; an app restart starts collapsed (deliberately no localStorage — this is session state, not a user preference). Keying per project mirrors the backend's active-project history RPCs, so a card expanded in project A never leaks into project B's graph (the page reads the entry of the project whose history it shows). Content-addressed SHAs make entries self-validating: a persisted SHA only affects rendering while it is present in the loaded history — a rebased-away SHA degrades to all-collapsed. The payload caches (message/files/diff) stay page-local — only the decisions persist; remounting re-fetches the payloads through the deduped lazy loaders.

## Error Handling

A history load error renders only when no commits are loaded (inline error state with the message); an exhausted or mid-session failure of `loadMore` surfaces through the same error channel without discarding loaded pages. Per-commit lazy fetches degrade locally: a failed message or diff fetch renders a "Failed to load …" placeholder line at the deterministic placeholder height (and the phase flips to `failed`, not retried automatically); an unknown SHA or empty SHA is rejected by `GetCommitMessage` before git runs. A SHA that git cannot resolve surfaces as the fetch's rejection — the placeholder, not a crash. The filter's invalid-regex state is a rendered notice, never an exception.

## Invariants

- Rows in the compact view NEVER expand inline: row heights are constant (`ROW_SPACING`), the panel carries no lane graph, and full-width inspection lives in the viewer's history page.
- Every card expansion height is a deterministic function of UI state — computed by pure functions in `gitGraphCardHeights.ts`, consumed identically by the virtualizer and the lane gutter, with no DOM measurement anywhere in either view.
- Sections render with explicit pixel heights from the same constants the height math uses, so DOM height ≡ reserved height; overflow is absorbed by capped internal scrolling, never by growing a row.
- An icon-only button carries a native `title` as its only label (the history button: "Open history viewer"), per the button-tooltip convention — exactly one channel.
- The `c0wrk:graph` pseudo-tab is opened as a virtual file (`openVirtualFile`): it never triggers the file-icon RPC, never enters a `fileIcons` cache entry, is never fetched by the viewer's data loader (no ReadFile RPC, no `tree_changed` reload), never persists in the tab snapshot, and opening it expands the file viewer.
- History pages load lazily and append-only: `loadMore` is idempotent (in-flight guard + `hasMore`), and an epoch token prevents a stale in-flight page from appending after a reload.
- Per-commit detail (message, files, diff) is fetched lazily, at most once per SHA per resource, deduped against the cache and in-flight requests.
- The viewer page's expansion decisions persist for the whole application session in `gitGraphStore` (in-memory, keyed per project, never localStorage): a remount restores the expanded card and its open file diffs; a persisted SHA applies only while it is present in the loaded history; an app restart starts collapsed.
- The file filter is client-side over the loaded window only; it never queries the backend. (The panel has no lane graph to hide; the viewer's page hides its gutter while a filter is active.)
- Commit stats are file-change buckets (A/D/other-letter); merge commits report zeros on the wire; the frontend renders the backend triple when present and derives the same buckets client-side from the fetched file list otherwise; a stats-less row (no backend value, files not loaded) renders no stats cell.
- Empty-state texts distinguish merge ("No diff (merge commit)") from root ("No files (root commit)") from ordinary file-less commits ("No files"), matching the backend's `diff-tree` behavior.
- The unified/split diff mode is the single shared `reviewStore.diffViewMode` preference (persisted, app-wide, no session affinity) driven only through `DiffViewModeToggle`.

## Related Specs

- [desktop-frontend.md](../../contracts/desktop-frontend.md) — the `GetGitHistory` (stats + pagination), `GetCommitMessage`, `GetCommitFiles`/`GetCommitFilesBatch` and `GetCommitDiff` RPC contract, and the Git-panel focus resolution every history RPC resolves through
- [git-changes-list.md](git-changes-list.md) — the Changes tab list (the other Git-panel tab)
- [git-operation-console.md](git-operation-console.md) — the mutation-result log the history context menu's operations feed
- [button-tooltips.md](button-tooltips.md) — the tooltip convention behind the icon-only history button and the SHA/stats `title`s
- [stores.md](stores.md) — `gitPanelStore` (`GIT_GRAPH_TAB_PATH`, `pendingHistoryFilter`), `fileViewerStore` (tab + expansion), `reviewStore` (`diffViewMode`)
- [review.md](../review.md) — the read-only `c0wrk:commit:<sha>` review page and the shared diff tables/mode the graph page renders with
