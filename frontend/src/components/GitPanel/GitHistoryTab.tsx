import { useState, useMemo, useCallback, useEffect } from 'react'
import { Loader2, AlertCircle } from 'lucide-react'
import { computeGraphLayout, computeRowYLayout, type GraphNode } from '@/lib/gitGraphLayout'
import { useGitHistory } from '@/hooks/useGitHistory'
import { useGitHistoryFilter } from '@/hooks/useGitHistoryFilter'
import { useGitHistoryAutofill } from '@/hooks/useGitHistoryAutofill'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { FilterBar } from '@/components/ui/FilterBar'
import { ROW_SPACING, NODE_OFFSET, expandedContentHeight } from './gitGraphRender'
import { GitGraphGutter } from './GitGraphGutter'
import { GitHistoryRow } from './GitHistoryRow'

/**
 * Unified commit history + graph tab. Replaces the former separate History
 * and Graph tabs with a single view: an SVG lane gutter alongside commit
 * rows that carry every field from both (message, refs, short SHA, author,
 * date). Clicking a commit lazily expands its changed files inline; the
 * graph edges route around the expanded gap via variable row heights.
 *
 * Data comes from one paginated source (`useGitHistory` → `GetGitHistory`);
 * changed files are fetched lazily per commit via `GetCommitFiles` and
 * cached by `useGitHistoryFilter`.
 *
 * A glob/regex file filter (shared with the file-tree panel via `FilterBar`)
 * narrows the list to commits that touched matching files. While a filter is
 * active the lane graph is hidden and the remaining rows take the full width
 * (pushed left), since a filtered subset no longer forms a connected graph.
 */
export function GitHistoryTab() {
  const { commits, hasMore, isLoading, isLoadingMore, error, loadMore } = useGitHistory()
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

  // While filtering, auto-load more pages until enough matched commits are
  // found (HISTORY_PAGE_SIZE), the log is exhausted, or the safety cap is
  // reached. The `allFilesResolved` guard prevents firing the next page
  // load before the filter hook has fetched changed files for the newly
  // loaded commits.
  const allFilesResolved = commits.every((c) => filesBySha[c.sha] !== undefined)
  const isAutofilling = useGitHistoryAutofill({
    isFiltering,
    isInvalidFilter,
    isResolvingFiles,
    filteredCount: filteredCommits.length,
    loadedCount: commits.length,
    allFilesResolved,
    hasMore,
    isLoadingMore,
    loadMore,
  })

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

  // Pixel height of a single row, accounting for inline expansion.
  const rowHeightFor = useCallback(
    (sha: string): number => {
      if (expandedSha !== sha) return ROW_SPACING
      return ROW_SPACING + expandedContentHeight(pendingShas.has(sha), filesBySha[sha])
    },
    [expandedSha, pendingShas, filesBySha],
  )

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
      <div className="flex flex-col min-h-0 flex-1 overflow-y-auto custom-scrollbar">
        {isFiltering ? (
          // Filtered view: graph hidden, rows take the full width (left-aligned).
          <div className="min-w-0">
            {isInvalidFilter ? (
              <p className="p-4 text-center text-xs text-destructive">Invalid regex</p>
            ) : filteredCommits.length === 0 && !isResolvingFiles ? (
              <p className="p-4 text-center text-xs text-muted-foreground">No matching commits</p>
            ) : (
              <>
                {filteredCommits.map((commit) => {
                  const node = shaToNode.get(commit.sha)
                  if (!node) return null
                  return (
                    <GitHistoryRow
                      key={node.sha}
                      node={node}
                      author={commit.author}
                      date={commit.date}
                      height={rowHeightFor(node.sha)}
                      expanded={expandedSha === node.sha}
                      files={filesBySha[node.sha]}
                      loadingFiles={pendingShas.has(node.sha)}
                      onClick={() => void handleCommitClick(node.sha)}
                    />
                  )
                })}
                {isResolvingFiles && (
                  <div className="flex items-center justify-center gap-1.5 py-2 text-xs text-muted-foreground">
                    <Loader2 className="size-3.5 animate-spin" /> Resolving files…
                  </div>
                )}
              </>
            )}
          </div>
        ) : (
          // Full history + lane graph.
          <div className="flex">
            <GitGraphGutter nodes={nodes} rowY={rowY} />
            <div className="flex-1 min-w-0">
              {nodes.map((node, i) => {
                const commit = commits[i]
                return (
                  <GitHistoryRow
                    key={node.sha}
                    node={node}
                    author={commit?.author ?? ''}
                    date={commit?.date ?? ''}
                    height={rowHeightFor(node.sha)}
                    expanded={expandedSha === node.sha}
                    files={filesBySha[node.sha]}
                    loadingFiles={pendingShas.has(node.sha)}
                    onClick={() => void handleCommitClick(node.sha)}
                  />
                )
              })}
            </div>
          </div>
        )}
        {isFiltering && (isAutofilling || isLoadingMore) ? (
          <div className="flex items-center justify-center gap-1.5 py-2 text-xs text-muted-foreground">
            <Loader2 className="size-3.5 animate-spin" /> Loading more matches…
          </div>
        ) : hasMore ? (
          <button
            type="button"
            onClick={loadMore}
            disabled={isLoadingMore}
            className="flex items-center justify-center gap-1.5 py-2 text-xs text-muted-foreground hover:bg-muted/50 transition-colors disabled:opacity-50"
          >
            {isLoadingMore && <Loader2 className="size-3.5 animate-spin" />}
            {isLoadingMore ? 'Loading…' : 'Load more'}
          </button>
        ) : null}
      </div>
    </div>
  )
}
