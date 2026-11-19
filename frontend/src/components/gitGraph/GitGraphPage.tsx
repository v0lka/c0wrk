import { useState, useCallback, useMemo, useRef } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { Loader2, AlertCircle, GitCommit } from 'lucide-react'
import { computeGraphLayout, computeRowYLayout } from '@/lib/gitGraphLayout'
import { useGitHistory } from '@/hooks/useGitHistory'
import { useCommitFiles } from '@/hooks/useCommitFiles'
import { useReviewStore } from '@/stores/reviewStore'
import { getCommitMessage } from '@/api/git'
import { getCommitDiff } from '@/api/review'
import type { ReviewFileDiffLike } from '@/components/review/diffParsing'
import { ROW_SPACING, NODE_OFFSET, LANE_SPACING, xFor } from '@/components/GitPanel/gitGraphRender'
import { GitGraphGutter } from '@/components/GitPanel/GitGraphGutter'
import { DiffViewModeToggle } from '@/components/diff/DiffViewModeToggle'
import { GitGraphCard } from './GitGraphCard'
import { cardHeight, sliceDiffByPath, type CardExpansionState, type FileDiffUiState } from './gitGraphCardHeights'

type FetchPhase = 'loading' | 'failed'

/**
 * The file viewer's standalone Git-graph page: the unified lane-gutter
 * history (same layout engine as the Git panel's History tab) with richer
 * expandable commit cards — full commit message, changed files with status
 * + stats, and a per-file inline diff rendered with the review page's
 * tables (UnifiedDiffTable / SplitDiffTable), in the SHARED
 * reviewStore.diffViewMode driven by the header's DiffViewModeToggle.
 *
 * Data comes from the same paginated source as the Git panel
 * (`useGitHistory` → GetGitHistory, next page on scroll-to-end / "Load
 * more") and from lazily-fetched per-commit detail: GetCommitFiles via the
 * shared useCommitFiles cache, the full message via getCommitMessage (one
 * fetch per SHA, cached), and the commit diff via getCommitDiff (ONE fetch
 * per SHA, cached, sliced per file). Every expansion height is a
 * deterministic function of this state (gitGraphCardHeights) so the SVG
 * gutter routes edges around expanded cards without any DOM measurement.
 */
export function GitGraphPage() {
  const { commits, isLoading, isLoadingMore, hasMore, error, loadMore } = useGitHistory()
  const { filesBySha, fetchFiles, pendingShas } = useCommitFiles()
  const diffViewMode = useReviewStore((s) => s.diffViewMode)

  // ── Card expansion state ────────────────────────────────────────────
  /** The single expanded commit card (null = all collapsed). */
  const [expandedSha, setExpandedSha] = useState<string | null>(null)
  /** Full commit messages by SHA — the lazy getCommitMessage cache. */
  const [messagesBySha, setMessagesBySha] = useState<Record<string, string>>({})
  /** Fetch phase per SHA (entry present = message not (yet) cached). */
  const [messagePhases, setMessagePhases] = useState<Record<string, FetchPhase>>({})
  /** Full commit diffs by SHA — the ONE-getCommitDiff-per-commit cache. */
  const [diffsBySha, setDiffsBySha] = useState<Record<string, ReviewFileDiffLike[]>>({})
  /** Fetch phase per SHA (entry present = diff not (yet) cached). */
  const [diffPhases, setDiffPhases] = useState<Record<string, FetchPhase>>({})
  /** Per-card set of file paths whose inline diff is open. */
  const [expandedFiles, setExpandedFiles] = useState<Record<string, ReadonlySet<string>>>({})

  // Ref mirrors so the stable fetch callbacks see the latest caches
  // without being re-created (and re-triggering effects) on every render.
  const messagesRef = useRef(messagesBySha)
  messagesRef.current = messagesBySha
  const diffsRef = useRef(diffsBySha)
  diffsRef.current = diffsBySha
  const messagesInFlight = useRef(new Set<string>())
  const diffsInFlight = useRef(new Set<string>())

  /** Lazily fetch + cache one commit's full message (deduped). */
  const ensureMessage = useCallback((sha: string) => {
    if (messagesRef.current[sha] !== undefined) return
    if (messagesInFlight.current.has(sha)) return
    messagesInFlight.current.add(sha)
    setMessagePhases((prev) => ({ ...prev, [sha]: 'loading' }))
    getCommitMessage(sha)
      .then((message) => {
        setMessagesBySha((prev) => ({ ...prev, [sha]: message }))
        setMessagePhases((prev) => {
          const next = { ...prev }
          delete next[sha]
          return next
        })
      })
      .catch(() => {
        setMessagePhases((prev) => ({ ...prev, [sha]: 'failed' }))
      })
      .finally(() => {
        messagesInFlight.current.delete(sha)
      })
  }, [])

  /** Lazily fetch + cache one commit's full diff (ONE RPC per commit). */
  const ensureDiff = useCallback((sha: string) => {
    if (diffsRef.current[sha] !== undefined) return
    if (diffsInFlight.current.has(sha)) return
    diffsInFlight.current.add(sha)
    setDiffPhases((prev) => ({ ...prev, [sha]: 'loading' }))
    getCommitDiff(sha)
      .then((diff) => {
        setDiffsBySha((prev) => ({ ...prev, [sha]: diff }))
        setDiffPhases((prev) => {
          const next = { ...prev }
          delete next[sha]
          return next
        })
      })
      .catch(() => {
        setDiffPhases((prev) => ({ ...prev, [sha]: 'failed' }))
      })
      .finally(() => {
        diffsInFlight.current.delete(sha)
      })
  }, [])

  const handleCardToggle = useCallback(
    (sha: string) => {
      if (expandedSha === sha) {
        setExpandedSha(null)
        return
      }
      setExpandedSha(sha)
      void fetchFiles(sha)
      ensureMessage(sha)
    },
    [expandedSha, fetchFiles, ensureMessage],
  )

  const handleFileToggle = useCallback(
    (sha: string, path: string) => {
      // The ensureDiff call dedupes against the cache and in-flight requests,
      // so it is safe to fire on every toggle (collapse included).
      ensureDiff(sha)
      setExpandedFiles((prev) => {
        const current = prev[sha] ?? new Set<string>()
        const next = new Set(current)
        if (next.has(path)) {
          next.delete(path)
        } else {
          next.add(path)
        }
        return { ...prev, [sha]: next }
      })
    },
    [ensureDiff],
  )

  // Per-file diff UI state for the expanded card (only open paths appear).
  const activeFileDiffs = useMemo(() => {
    const paths = expandedSha !== null ? expandedFiles[expandedSha] : undefined
    if (!paths || paths.size === 0) return {}
    const diff = expandedSha !== null ? diffsBySha[expandedSha] : undefined
    const phase = expandedSha !== null ? diffPhases[expandedSha] : undefined
    const map: Record<string, FileDiffUiState> = {}
    for (const path of paths) {
      map[path] = {
        expanded: true,
        loading: phase === 'loading',
        failed: phase === 'failed',
        slice: diff !== undefined ? sliceDiffByPath(diff, path) : undefined,
        diffViewMode,
      }
    }
    return map
  }, [expandedSha, expandedFiles, diffsBySha, diffPhases, diffViewMode])

  /** The CardExpansionState for one commit — inputs to the height math. */
  const cardStateFor = useCallback(
    (sha: string): CardExpansionState => ({
      messageLoading: messagePhases[sha] === 'loading',
      messageFailed: messagePhases[sha] === 'failed',
      fullMessage: messagesBySha[sha],
      filesLoading: pendingShas.has(sha) || filesBySha[sha] === undefined,
      files: filesBySha[sha],
      fileDiffs: sha === expandedSha ? activeFileDiffs : {},
    }),
    [messagePhases, messagesBySha, pendingShas, filesBySha, expandedSha, activeFileDiffs],
  )

  // ── Lane layout over the full commit set ────────────────────────────
  const nodes = useMemo(() => computeGraphLayout(commits), [commits])

  const rowHeightFor = useCallback(
    (sha: string) => cardHeight(cardStateFor(sha), expandedSha === sha),
    [cardStateFor, expandedSha],
  )
  const rowHeights = useMemo(() => nodes.map((n) => rowHeightFor(n.sha)), [nodes, rowHeightFor])
  const rowY = useMemo(
    () => computeRowYLayout(rowHeights, ROW_SPACING, NODE_OFFSET),
    [rowHeights],
  )

  // SVG gutter width — offsets the card column past the lane graph.
  const svgWidth = useMemo(() => {
    if (nodes.length === 0) return 0
    const maxLane = nodes.reduce((m, n) => Math.max(m, n.lane), 0)
    return xFor(maxLane) + LANE_SPACING
  }, [nodes])

  // ── Virtualization ─────────────────────────────────────────────────
  // Exact per-row heights (from the deterministic math) so the virtualizer
  // positions items without DOM measurement (which is unreliable for
  // dynamically expanded rows, and returns 0 in jsdom).
  const scrollRef = useRef<HTMLDivElement>(null)
  const virtualizer = useVirtualizer({
    count: nodes.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: (i: number) => rowHeights[i] ?? ROW_SPACING,
    overscan: 8,
    getItemKey: (i: number) => nodes[i]?.sha ?? i,
  })

  // Infinite-scroll edge trigger (idempotent loadMore; see the Git panel's
  // History tab). The "Load more" button below is the keyboard path.
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
    <div className="flex flex-1 flex-col min-h-0">
      {/* Header: title + the SHARED unified/split toggle (reviewStore). */}
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-secondary/30 px-3 py-2">
        <GitCommit className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="text-sm font-medium">Git History</span>
        <DiffViewModeToggle className="ml-auto" />
      </div>
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="flex flex-col min-h-0 flex-1 overflow-y-auto custom-scrollbar"
      >
        <div className="shrink-0" style={{ height: rowY.totalHeight, position: 'relative' }}>
          {/* Lane gutter scrolls in sync beside the virtualized rows; only
              visible cards are mounted while the SVG renders all nodes. */}
          <div style={{ position: 'absolute', left: 0, top: 0 }}>
            <GitGraphGutter nodes={nodes} rowY={rowY} />
          </div>
          {virtualizer.getVirtualItems().map((vi) => {
            const node = nodes[vi.index]
            const commit = commits[vi.index]
            if (!node || !commit) return null
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
                <GitGraphCard
                  node={node}
                  commit={commit}
                  height={rowHeightFor(node.sha)}
                  expanded={expandedSha === node.sha}
                  messageLoading={messagePhases[node.sha] === 'loading'}
                  messageFailed={messagePhases[node.sha] === 'failed'}
                  fullMessage={messagesBySha[node.sha]}
                  files={filesBySha[node.sha]}
                  filesLoading={pendingShas.has(node.sha) || filesBySha[node.sha] === undefined}
                  fileDiffs={expandedSha === node.sha ? activeFileDiffs : {}}
                  diffViewMode={diffViewMode}
                  onToggle={() => handleCardToggle(node.sha)}
                  onToggleFile={(path) => handleFileToggle(node.sha, path)}
                />
              </div>
            )
          })}
        </div>
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
    </div>
  )
}
