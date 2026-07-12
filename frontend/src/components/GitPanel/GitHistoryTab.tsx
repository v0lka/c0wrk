import { useState, useMemo, useCallback } from 'react'
import { Loader2, AlertCircle } from 'lucide-react'
import { getCommitFiles } from '@/api/git'
import { computeGraphLayout, computeRowYLayout } from '@/lib/gitGraphLayout'
import { useGitHistory } from '@/hooks/useGitHistory'
import { ROW_SPACING, NODE_OFFSET, expandedContentHeight } from './gitGraphRender'
import { GitGraphGutter } from './GitGraphGutter'
import { GitHistoryRow } from './GitHistoryRow'
import type { CommitFile } from '@/types/models'

/**
 * Unified commit history + graph tab. Replaces the former separate History
 * and Graph tabs with a single view: an SVG lane gutter alongside commit
 * rows that carry every field from both (message, refs, short SHA, author,
 * date). Clicking a commit lazily expands its changed files inline; the
 * graph edges route around the expanded gap via variable row heights.
 *
 * Data comes from one paginated source (`useGitHistory` → `GetGitHistory`);
 * changed files are still fetched lazily per commit via `GetCommitFiles`.
 */
export function GitHistoryTab() {
  const { commits, hasMore, isLoading, isLoadingMore, error, loadMore } = useGitHistory()
  const [expandedSha, setExpandedSha] = useState<string | null>(null)
  const [filesBySha, setFilesBySha] = useState<Record<string, CommitFile[]>>({})
  const [loadingFilesSha, setLoadingFilesSha] = useState<string | null>(null)

  // Lane layout is computed over the accumulated commits (newest-first).
  const nodes = useMemo(() => computeGraphLayout(commits), [commits])

  // Per-row heights: collapsed rows are ROW_SPACING; the expanded row grows
  // by its file-list height so the SVG routes edges around the gap.
  const rowY = useMemo(() => {
    const rowHeights = nodes.map((n) =>
      expandedSha === n.sha
        ? ROW_SPACING + expandedContentHeight(loadingFilesSha === n.sha, filesBySha[n.sha])
        : ROW_SPACING,
    )
    return computeRowYLayout(rowHeights, ROW_SPACING, NODE_OFFSET)
  }, [nodes, expandedSha, loadingFilesSha, filesBySha])

  const handleCommitClick = useCallback(
    async (sha: string) => {
      if (expandedSha === sha) {
        setExpandedSha(null)
        return
      }
      setExpandedSha(sha)
      if (filesBySha[sha] !== undefined) return // already fetched
      setLoadingFilesSha(sha)
      try {
        const files = await getCommitFiles(sha)
        setFilesBySha((prev) => ({ ...prev, [sha]: files }))
      } catch {
        setFilesBySha((prev) => ({ ...prev, [sha]: [] }))
      } finally {
        setLoadingFilesSha(null)
      }
    },
    [expandedSha, filesBySha],
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
    <div className="flex flex-col min-h-0 flex-1 overflow-y-auto custom-scrollbar">
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
                height={
                  expandedSha === node.sha
                    ? ROW_SPACING +
                      expandedContentHeight(loadingFilesSha === node.sha, filesBySha[node.sha])
                    : ROW_SPACING
                }
                expanded={expandedSha === node.sha}
                files={filesBySha[node.sha]}
                loadingFiles={loadingFilesSha === node.sha}
                onClick={() => void handleCommitClick(node.sha)}
              />
            )
          })}
        </div>
      </div>
      {hasMore && (
        <button
          type="button"
          onClick={loadMore}
          disabled={isLoadingMore}
          className="flex items-center justify-center gap-1.5 py-2 text-xs text-muted-foreground hover:bg-muted/50 transition-colors disabled:opacity-50"
        >
          {isLoadingMore && <Loader2 className="size-3.5 animate-spin" />}
          {isLoadingMore ? 'Loading…' : 'Load more'}
        </button>
      )}
    </div>
  )
}
