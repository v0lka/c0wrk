import { useState, useMemo, useCallback, useEffect, useRef } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { Loader2, AlertCircle } from 'lucide-react'
import { computeGraphLayout, computeRowYLayout, type GraphNode } from '@/lib/gitGraphLayout'
import { useGitHistory } from '@/hooks/useGitHistory'
import { useGitHistoryFilter } from '@/hooks/useGitHistoryFilter'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { FilterBar } from '@/components/ui/FilterBar'
import { ROW_SPACING, NODE_OFFSET, LANE_SPACING, xFor, expandedContentHeight } from './gitGraphRender'
import { GitGraphGutter } from './GitGraphGutter'
import { GitHistoryRow } from './GitHistoryRow'

/**
 * Unified commit history + graph tab. Replaces the former separate History
 * and Graph tabs with a single view: an SVG lane gutter alongside commit
 * rows that carry every field from both (message, refs, short SHA, author,
 * date). Clicking a commit lazily expands its changed files inline; the
 * graph edges route around the expanded gap via variable row heights.
 *
 * Data comes from a single source (`useGitHistory` → `GetGitHistory`);
 * changed files are fetched lazily per commit via `GetCommitFiles` and
 * cached by `useGitHistoryFilter`.
 *
 * A glob/regex file filter (shared with the file-tree panel via `FilterBar`)
 * narrows the list to commits that touched matching files. While a filter is
 * active the lane graph is hidden and the remaining rows take the full width
 * (pushed left), since a filtered subset no longer forms a connected graph.
 */
export function GitHistoryTab() {
  const { commits, isLoading, error } = useGitHistory()
  const {
    filterText,
    filterMode,
    setFilterText,
    toggleFilterMode,
    isFiltering,
    isInvalidFilter,
    isResolvingFiles,
    filteredCommits,
    filesBySha,
    fetchFiles,
    pendingShas,
  } = useGitHistoryFilter(commits)
  const [expandedSha, setExpandedSha] = useState<string | null>(null)

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

  // Per-row heights: collapsed rows are ROW_SPACING; the expanded row grows
  // by its file-list height so the SVG routes edges around the gap.
  const rowY = useMemo(() => {
    const rowHeights = nodes.map((n) =>
      expandedSha === n.sha
        ? ROW_SPACING + expandedContentHeight(pendingShas.has(n.sha), filesBySha[n.sha])
        : ROW_SPACING,
    )
    return computeRowYLayout(rowHeights, ROW_SPACING, NODE_OFFSET)
  }, [nodes, expandedSha, pendingShas, filesBySha])

  const handleCommitClick = useCallback(
    async (sha: string) => {
      if (expandedSha === sha) {
        setExpandedSha(null)
        return
      }
      setExpandedSha(sha)
      void fetchFiles(sha)
    },
    [expandedSha, fetchFiles],
  )

  // Open a read-only review page showing the given commit's diff in the file
  // viewer. The commit SHA is encoded in a synthetic pseudo-path
  // ("c0wrk:commit:<sha>") that FileViewerContent renders as a ReviewPage in
  // read-only mode. The file viewer is expanded so the review is visible.
  const handleShaClick = useCallback((sha: string) => {
    const { openFile, setCollapsed } = useFileViewerStore.getState()
    openFile(`c0wrk:commit:${sha}`)
    setCollapsed(false)
  }, [])

  // Pixel height of a single row, accounting for inline expansion.
  const rowHeightFor = useCallback(
    (sha: string): number => {
      if (expandedSha !== sha) return ROW_SPACING
      return ROW_SPACING + expandedContentHeight(pendingShas.has(sha), filesBySha[sha])
    },
    [expandedSha, pendingShas, filesBySha],
  )

  // ── Virtualization ───────────────────────────────────────────────────
  // Only visible rows are mounted — essential now that GetGitHistory
  // returns the full commit list without pagination. Two virtualizers are
  // created (one per rendering mode); the inactive one has count 0 so it
  // produces no items. Both share the same scroll element.
  const scrollRef = useRef<HTMLDivElement>(null)

  // SVG gutter width — needed to offset the row column past the graph.
  const svgWidth = useMemo(() => {
    if (nodes.length === 0) return 0
    const maxLane = nodes.reduce((m, n) => Math.max(m, n.lane), 0)
    return xFor(maxLane) + LANE_SPACING
  }, [nodes])

  // Exact per-row heights so the virtualizer can position items without
  // DOM measurement (which returns 0 in jsdom and is unreliable for
  // dynamically expanded rows).
  const fullHeights = useMemo(
    () => nodes.map((n) => rowHeightFor(n.sha)),
    [nodes, rowHeightFor],
  )
  const filteredHeights = useMemo(
    () => filteredCommits.map((c) => rowHeightFor(c.sha)),
    [filteredCommits, rowHeightFor],
  )

  const fullVirtualizer = useVirtualizer({
    count: isFiltering ? 0 : nodes.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: (i: number) => fullHeights[i] ?? ROW_SPACING,
    overscan: 8,
    getItemKey: (i: number) => nodes[i]?.sha ?? i,
  })
  const filteredVirtualizer = useVirtualizer({
    count: isFiltering ? filteredCommits.length : 0,
    getScrollElement: () => scrollRef.current,
    estimateSize: (i: number) => filteredHeights[i] ?? ROW_SPACING,
    overscan: 8,
    getItemKey: (i: number) => filteredCommits[i]?.sha ?? i,
  })

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
      <FilterBar
        value={filterText}
        onChange={setFilterText}
        mode={filterMode}
        onToggleMode={toggleFilterMode}
        placeholder="Filter by file"
      />
      <div
        ref={scrollRef}
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
                    >
                      <GitHistoryRow
                        node={node}
                        author={commit.author}
                        date={commit.date}
                        height={rowHeightFor(node.sha)}
                        expanded={expandedSha === node.sha}
                        files={filesBySha[node.sha]}
                        loadingFiles={pendingShas.has(node.sha)}
                        onClick={() => void handleCommitClick(node.sha)}
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
          <div style={{ height: rowY.totalHeight, position: 'relative' }}>
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
                >
                  <GitHistoryRow
                    node={node}
                    author={commit?.author ?? ''}
                    date={commit?.date ?? ''}
                    height={rowHeightFor(node.sha)}
                    expanded={expandedSha === node.sha}
                    files={filesBySha[node.sha]}
                    loadingFiles={pendingShas.has(node.sha)}
                    onClick={() => void handleCommitClick(node.sha)}
                    onShaClick={handleShaClick}
                  />
                </div>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}
