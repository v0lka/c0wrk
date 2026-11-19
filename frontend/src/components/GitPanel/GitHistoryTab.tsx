import { useState, useMemo, useCallback, useEffect, useRef } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { Loader2, AlertCircle, GitGraph } from 'lucide-react'
import { computeGraphLayout, computeRowYLayout, type GraphNode } from '@/lib/gitGraphLayout'
import { useGitHistory } from '@/hooks/useGitHistory'
import { useGitHistoryFilter } from '@/hooks/useGitHistoryFilter'
import { useGitPanelStore, GIT_GRAPH_TAB_PATH } from '@/stores/gitPanelStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { FilterBar } from '@/components/ui/FilterBar'
import { Button } from '@/components/ui/button'
import { ROW_SPACING, NODE_OFFSET, LANE_SPACING, xFor } from './gitGraphRender'
import { GitGraphGutter } from './GitGraphGutter'
import { GitHistoryRow } from './GitHistoryRow'
import { GitHistoryContextMenu } from './GitHistoryContextMenu'

/**
 * Unified commit history + graph tab: an SVG lane gutter alongside fixed
 * ROW_SPACING commit rows that carry every field from both former tabs
 * (message, refs, short SHA, per-commit changed-file stats, author, date).
 * Rows never expand inline — heights are constant, so the lane layout is
 * trivial — and the full-width DAG inspection lives in the file viewer:
 * the graph button beside the filter bar opens the GIT_GRAPH_TAB_PATH tab.
 * Clicking a commit's short SHA opens its read-only diff review in the
 * viewer as well.
 *
 * Data comes from a single paginated source (`useGitHistory` →
 * `GetGitHistory`); the next page is loaded on scroll-to-end or via the
 * "Load more" button.
 *
 * A glob/regex file filter (shared with the file-tree panel via `FilterBar`)
 * narrows the list to commits that touched matching files. While a filter is
 * active the lane graph is hidden and the remaining rows take the full width
 * (pushed left), since a filtered subset no longer forms a connected graph.
 */
export function GitHistoryTab() {
  const { commits, isLoading, isLoadingMore, hasMore, error, reload, loadMore } = useGitHistory()
  // The file filter is CLIENT-SIDE and applies only to the commits loaded so
  // far (the "loaded window"). It does not query the backend and therefore
  // never sees commits that have not been paged in yet — loading more pages
  // extends the set the filter matches against.
  const {
    filterText,
    filterMode,
    setFilterText,
    toggleFilterMode,
    isFiltering,
    isInvalidFilter,
    isResolvingFiles,
    filteredCommits,
  } = useGitHistoryFilter(commits)

  // Current branch name — used by the commit context menu to label the
  // "Reset <branch> to This Commit" submenu.
  const currentBranch = useGitPanelStore((s) => s.branch.name)

  // Commit context menu (bespoke position-based, mirroring GitFileContextMenu).
  // A single instance is rendered at the tab level; right-clicking a row
  // records the commit + viewport coordinates and opens the menu.
  const [contextMenu, setContextMenu] = useState<{
    sha: string
    refs: string[]
    x: number
    y: number
  } | null>(null)
  const handleRowContextMenu = useCallback(
    (e: React.MouseEvent, sha: string, refs: string[]) => {
      e.preventDefault()
      // A right-click places a text-selection range as the browser's
      // mousedown default (notably on macOS). Clear it so opening the
      // context menu doesn't leave the row's commit message/SHA/author
      // highlighted. Left-click selection (the deliberate `select-text`
      // spans) remains unaffected.
      window.getSelection()?.removeAllRanges()
      setContextMenu({ sha, refs, x: e.clientX, y: e.clientY })
    },
    [],
  )
  const closeContextMenu = useCallback(() => setContextMenu(null), [])

  // Consume a pending history filter set externally (e.g. "View History" from
  // the file-tree context menu). The filter is applied once and then cleared
  // from the store so it doesn't re-apply on subsequent renders. Subscribing
  // reactively (rather than via getState()) ensures the effect re-runs even
  // when the tab is already mounted — e.g. the user is already viewing history
  // and right-clicks another file in the tree.
  const pendingHistoryFilter = useGitPanelStore((s) => s.pendingHistoryFilter)
  const clearPendingHistoryFilter = useGitPanelStore((s) => s.clearPendingHistoryFilter)
  useEffect(() => {
    if (pendingHistoryFilter !== null) {
      setFilterText(pendingHistoryFilter)
      clearPendingHistoryFilter()
    }
  }, [pendingHistoryFilter, setFilterText, clearPendingHistoryFilter])

  // Lane layout over the full commit set (used only while the graph shows).
  const nodes = useMemo(() => computeGraphLayout(commits), [commits])
  const shaToNode = useMemo(() => {
    const map = new Map<string, GraphNode>()
    for (const n of nodes) map.set(n.sha, n)
    return map
  }, [nodes])

  // Rows are fixed-height (no inline expansion), so the lane layout is
  // trivial: every row is ROW_SPACING tall and the SVG node always sits
  // NODE_OFFSET below the row top.
  const rowY = useMemo(
    () => computeRowYLayout(nodes.map(() => ROW_SPACING), ROW_SPACING, NODE_OFFSET),
    [nodes],
  )

  // Open the commit-graph tab in the file viewer (same synthetic pseudo-path
  // mechanism as the SHA-click review below). The viewer is expanded so the
  // graph is visible.
  const handleOpenGraph = useCallback(() => {
    const { openFile, setCollapsed } = useFileViewerStore.getState()
    openFile(GIT_GRAPH_TAB_PATH)
    setCollapsed(false)
  }, [])

  // Open a read-only review page showing the given commit's diff in the file
  // viewer. The commit SHA is encoded in a synthetic pseudo-path
  // ("c0wrk:commit:<sha>") that FileViewerContent renders as a ReviewPage in
  // read-only mode. The file viewer is expanded so the review is visible.
  const handleShaClick = useCallback((sha: string) => {
    const { openFile, setCollapsed } = useFileViewerStore.getState()
    openFile(`c0wrk:commit:${sha}`)
    setCollapsed(false)
  }, [])

  // ── Virtualization ───────────────────────────────────────────────────
  // Only visible rows are mounted — essential now that GetGitHistory loads
  // large histories a page at a time. Two virtualizers are created (one per
  // rendering mode); the inactive one has count 0 so it produces no items.
  // Both share the same scroll element. Heights are constant (ROW_SPACING),
  // so sizing needs no DOM measurement (which returns 0 in jsdom anyway).
  const scrollRef = useRef<HTMLDivElement>(null)

  // SVG gutter width — needed to offset the row column past the graph.
  const svgWidth = useMemo(() => {
    if (nodes.length === 0) return 0
    const maxLane = nodes.reduce((m, n) => Math.max(m, n.lane), 0)
    return xFor(maxLane) + LANE_SPACING
  }, [nodes])

  const fullVirtualizer = useVirtualizer({
    count: isFiltering ? 0 : nodes.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_SPACING,
    overscan: 8,
    getItemKey: (i: number) => nodes[i]?.sha ?? i,
  })
  const filteredVirtualizer = useVirtualizer({
    count: isFiltering ? filteredCommits.length : 0,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_SPACING,
    overscan: 8,
    getItemKey: (i: number) => filteredCommits[i]?.sha ?? i,
  })

  // Infinite-scroll edge trigger: when the user scrolls near the bottom of
  // the loaded window, request the next page. `loadMore` is idempotent (it
  // no-ops while a page is in flight or once `hasMore` is false), so firing
  // on every qualifying scroll event is harmless. The explicit "Load more"
  // button below is the keyboard/accessible path for the same action.
  const handleScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el || !hasMore || isLoadingMore) return
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 200) {
      void loadMore()
    }
  }, [hasMore, isLoadingMore, loadMore])

  if (isLoading) {
    return (
      <div className="flex flex-1 items-center justify-center min-h-0">
        <Loader2 className="size-4 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (error && commits.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center min-h-0 px-4 text-center">
        <div className="flex flex-col items-center gap-2 text-muted-foreground">
          <AlertCircle className="size-6 opacity-50" />
          <span className="text-xs">{error}</span>
        </div>
      </div>
    )
  }

  if (commits.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center min-h-0">
        <span className="text-sm text-muted-foreground select-none">No commits yet</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col min-h-0 flex-1">
      <div className="flex shrink-0 items-center border-b border-border pl-1.5">
        {/* Commit DAG inspection moved to the file viewer: this button opens
            the graph pseudo-path tab full-width there. `title` is mandatory —
            it is the button's only label (icon-only control). */}
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={handleOpenGraph}
          title="Open commit graph"
          aria-label="Open commit graph"
        >
          <GitGraph className="size-4" />
        </Button>
        <FilterBar
          className="flex-1 shrink border-b-0"
          value={filterText}
          onChange={setFilterText}
          mode={filterMode}
          onToggleMode={toggleFilterMode}
          placeholder="Filter by file"
        />
      </div>
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="flex flex-col min-h-0 flex-1 overflow-y-auto custom-scrollbar"
      >
        {isFiltering ? (
          // Filtered view: graph hidden, rows take the full width (left-aligned).
          isInvalidFilter ? (
            <p className="p-4 text-center text-xs text-destructive">Invalid regex</p>
          ) : filteredCommits.length === 0 && !isResolvingFiles ? (
            <p className="p-4 text-center text-xs text-muted-foreground">No matching commits</p>
          ) : (
            <>
              <div
                className="shrink-0"
                style={{
                  height: filteredVirtualizer.getTotalSize(),
                  position: 'relative',
                }}
              >
                {filteredVirtualizer.getVirtualItems().map((vi) => {
                  const commit = filteredCommits[vi.index]
                  if (!commit) return null
                  const node = shaToNode.get(commit.sha)
                  if (!node) return null
                  return (
                    <div
                      key={vi.key}
                      style={{
                        position: 'absolute',
                        top: vi.start,
                        left: 0,
                        right: 0,
                        height: vi.size,
                      }}
                      onContextMenu={(e) =>
                        handleRowContextMenu(e, node.sha, commit.refs)
                      }
                    >
                      <GitHistoryRow
                        node={node}
                        author={commit.author}
                        date={commit.date}
                        stats={commit.stats}
                        onShaClick={handleShaClick}
                      />
                    </div>
                  )
                })}
              </div>
              {isResolvingFiles && (
                <div className="flex items-center justify-center gap-1.5 py-2 text-xs text-muted-foreground">
                  <Loader2 className="size-3.5 animate-spin" /> Resolving files…
                </div>
              )}
            </>
          )
        ) : (
          // Full history + lane graph. The SVG gutter is absolutely
          // positioned so it scrolls in sync with the virtualized rows
          // beside it. Only visible rows are mounted; the gutter still
          // renders all nodes/edges (SVG elements are far lighter than
          // React component trees).
          <div className="shrink-0" style={{ height: rowY.totalHeight, position: 'relative' }}>
            <div style={{ position: 'absolute', left: 0, top: 0 }}>
              <GitGraphGutter nodes={nodes} rowY={rowY} />
            </div>
            {fullVirtualizer.getVirtualItems().map((vi) => {
              const node = nodes[vi.index]
              if (!node) return null
              const commit = commits[vi.index]
              return (
                <div
                  key={vi.key}
                  style={{
                    position: 'absolute',
                    top: vi.start,
                    left: svgWidth,
                    right: 0,
                    height: vi.size,
                  }}
                  onContextMenu={(e) =>
                    handleRowContextMenu(e, node.sha, commit?.refs ?? [])
                  }
                >
                  <GitHistoryRow
                    node={node}
                    author={commit?.author ?? ''}
                    date={commit?.date ?? ''}
                    stats={commit?.stats}
                    onShaClick={handleShaClick}
                  />
                </div>
              )
            })}
          </div>
        )}
        {hasMore && (
          <div className="flex items-center justify-center py-3">
            <button
              type="button"
              onClick={() => void loadMore()}
              disabled={isLoadingMore}
              className="inline-flex items-center gap-1.5 rounded-md border border-border px-3 py-1 text-xs text-muted-foreground hover:text-foreground disabled:opacity-50"
            >
              {isLoadingMore && <Loader2 className="size-3.5 animate-spin" />}
              Load more
            </button>
          </div>
        )}
      </div>
      <GitHistoryContextMenu
        sha={contextMenu?.sha ?? ''}
        refs={contextMenu?.refs ?? []}
        currentBranch={currentBranch}
        position={contextMenu ? { x: contextMenu.x, y: contextMenu.y } : null}
        onClose={closeContextMenu}
        onAfterMutation={reload}
      />
    </div>
  )
}
