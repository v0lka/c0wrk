import { useState, useCallback, useMemo, useRef, useEffect } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { Loader2, AlertCircle } from 'lucide-react'
import { computeGraphLayout, computeRowYLayout, type GraphNode } from '@/lib/gitGraphLayout'
import { useGitHistory } from '@/hooks/useGitHistory'
import { useGitHistoryFilter } from '@/hooks/useGitHistoryFilter'
import { useReviewStore } from '@/stores/reviewStore'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useGitGraphStore } from '@/stores/gitGraphStore'
import { useProjectStore } from '@/stores/projectStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { getCommitMessage } from '@/api/git'
import { getCommitDiff } from '@/api/review'
import type { ReviewFileDiffLike } from '@/components/review/diffParsing'
import { ROW_SPACING, NODE_OFFSET, LANE_SPACING, xFor } from '@/components/GitPanel/gitGraphRender'
import { GitGraphGutter } from '@/components/GitPanel/GitGraphGutter'
import { DiffViewModeToggle } from '@/components/diff/DiffViewModeToggle'
import { FilterBar } from '@/components/ui/FilterBar'
import { GitHistoryContextMenu } from '@/components/GitPanel/GitHistoryContextMenu'
import { CommitFileContextMenu } from './CommitFileContextMenu'
import { GitGraphCard } from './GitGraphCard'
import { cardHeight, sliceDiffByPath, type CardExpansionState, type FileDiffUiState } from './gitGraphCardHeights'

type FetchPhase = 'loading' | 'failed'

/** Stable empty per-card file sets — the no-expansion fallback for the store read. */
const EMPTY_EXPANDED_FILES: Record<string, ReadonlySet<string>> = {}

/**
 * The file viewer's history page (the `c0wrk:graph` pseudo-tab): the lane
 * gutter + expandable commit cards (full commit message, changed files with
 * status + stats, per-file inline diffs via the review tables) driven by the
 * SHARED reviewStore.diffViewMode (DiffViewModeToggle). The lane gutter
 * renders here only — the Git panel has no history section of its own —
 * and hides while the file filter is active, since a filtered subset
 * is not a connected graph.
 *
 * Data comes from the same paginated source as the Git panel
 * (`useGitHistory` → GetGitHistory, next page on scroll-to-end / "Load
 * more"), the same client-side glob/regex file filter
 * (`useGitHistoryFilter` + `FilterBar`, which also owns the shared
 * changed-files cache this page reads) and from lazily-fetched per-commit
 * detail: GetCommitFiles via the shared useCommitFiles cache, the full
 * message via getCommitMessage (one fetch per SHA, cached), and the commit
 * diff via getCommitDiff (ONE fetch per SHA, cached, sliced per file). Every
 * expansion height is a deterministic function of this state
 * (gitGraphCardHeights) so the SVG gutter routes edges around expanded cards
 * without any DOM measurement.
 *
 * The expand/collapse DECISIONS (which card is open, which files inside it)
 * live in gitGraphStore — in-memory, keyed per project, scoped to the whole
 * application session: closing/reopening the tab, collapsing the viewer or
 * switching projects remounts this page with the same expansion (an app
 * restart starts collapsed). The payload caches below stay page-local; a
 * restored card re-fetches its message/files/diff through the deduped lazy
 * loaders whenever the persisted SHA is present in the loaded history.
 */
export function GitGraphPage() {
  const { commits, isLoading, isLoadingMore, hasMore, error, reload, loadMore } = useGitHistory()
  // The filter hook owns the shared changed-files cache (filesBySha /
  // fetchFiles / pendingShas) — the same cache the card expansion reads.
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

  // Consume a pending history filter set externally (e.g. "View History" from
  // the file-tree context menu, which also opens this page). The filter is
  // applied once and then cleared from the store so it doesn't re-apply on
  // subsequent renders. Subscribing reactively (rather than via getState())
  // ensures the effect re-runs even when the page is already mounted — e.g.
  // the user is already viewing history and right-clicks another file in the
  // tree.
  const pendingHistoryFilter = useGitPanelStore((s) => s.pendingHistoryFilter)
  const clearPendingHistoryFilter = useGitPanelStore((s) => s.clearPendingHistoryFilter)
  useEffect(() => {
    if (pendingHistoryFilter !== null) {
      setFilterText(pendingHistoryFilter)
      clearPendingHistoryFilter()
    }
  }, [pendingHistoryFilter, setFilterText, clearPendingHistoryFilter])

  const diffViewMode = useReviewStore((s) => s.diffViewMode)
  // Current branch name — used by the commit context menu to label the
  // "Reset <branch> to This Commit" submenu.
  const currentBranch = useGitPanelStore((s) => s.branch.name)

  // ── Card expansion state (gitGraphStore — app-session scope) ────────
  // The expansion decisions persist for the whole application session,
  // keyed per project; the payload caches below stay page-local. The
  // fallbacks are module-level constants — selectors/renders must never
  // allocate fresh containers (Zustand snapshots compare by reference).
  /** Expansion entry of the project whose history this page shows. */
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const expansion = useGitGraphStore((s) =>
    activeProjectId !== null ? s.byProject[activeProjectId] : undefined,
  )
  /** The single expanded commit card (null = all collapsed). */
  const expandedSha = expansion?.expandedSha ?? null
  /** Per-card set of file paths whose inline diff is open. */
  const expandedFiles = expansion?.expandedFiles ?? EMPTY_EXPANDED_FILES
  // Actions are stable (created once, never change identity), so reading
  // them via getState() needs no subscription. An empty scope (no active
  // project) makes every action a no-op — the page can't render a history
  // without one anyway.
  const { expandCard, collapseCard, toggleFile } = useGitGraphStore.getState()
  const scope = activeProjectId ?? ''
  /** Full commit messages by SHA — the lazy getCommitMessage cache. */
  const [messagesBySha, setMessagesBySha] = useState<Record<string, string>>({})
  /** Fetch phase per SHA (entry present = message not (yet) cached). */
  const [messagePhases, setMessagePhases] = useState<Record<string, FetchPhase>>({})
  /** Full commit diffs by SHA — the ONE-getCommitDiff-per-commit cache. */
  const [diffsBySha, setDiffsBySha] = useState<Record<string, ReviewFileDiffLike[]>>({})
  /** Fetch phase per SHA (entry present = diff not (yet) cached). */
  const [diffPhases, setDiffPhases] = useState<Record<string, FetchPhase>>({})

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
      // Store-only: the lazy message/files fetches fire from the restore
      // effect below, the same path a session-restored expansion takes.
      if (expandedSha === sha) {
        collapseCard(scope)
        return
      }
      expandCard(scope, sha)
    },
    [expandedSha, scope, expandCard, collapseCard],
  )

  const handleFileToggle = useCallback(
    (sha: string, path: string) => {
      // The ensureDiff call dedupes against the cache and in-flight requests,
      // so it is safe to fire on every toggle (collapse included).
      ensureDiff(sha)
      toggleFile(scope, sha, path)
    },
    [ensureDiff, scope, toggleFile],
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

  // ── Commit context menu (bespoke position-based; a single page-level
  // instance fed by card right-clicks). ─────────────────────────────────
  const [contextMenu, setContextMenu] = useState<{
    sha: string
    refs: string[]
    x: number
    y: number
  } | null>(null)
  const handleCardContextMenu = useCallback(
    (e: React.MouseEvent, sha: string, refs: string[]) => {
      e.preventDefault()
      // A right-click places a text-selection range as the browser's
      // mousedown default (notably on macOS). Clear it so opening the
      // context menu doesn't leave the card's text highlighted.
      window.getSelection()?.removeAllRanges()
      setContextMenu({ sha, refs, x: e.clientX, y: e.clientY })
    },
    [],
  )
  const closeContextMenu = useCallback(() => setContextMenu(null), [])

  // Per-file context menu inside an expanded card: right-clicking a changed
  // file opens the single-entry "Open in Viewer" menu. The sha rides the
  // state so the open action can build the commit-file pseudo-path.
  const [fileMenu, setFileMenu] = useState<{
    sha: string
    path: string
    x: number
    y: number
  } | null>(null)
  const handleFileContextMenu = useCallback(
    (e: React.MouseEvent, sha: string, path: string) => {
      // The card wrapper carries the COMMIT context menu; the file row's
      // menu wins — stop propagation so only one opens.
      e.preventDefault()
      e.stopPropagation()
      window.getSelection()?.removeAllRanges()
      setFileMenu({ sha, path, x: e.clientX, y: e.clientY })
    },
    [],
  )
  const closeFileMenu = useCallback(() => setFileMenu(null), [])

  // Open the file's COMMIT-VERSION diff (not the current working-tree copy)
  // as a virtual tab: "c0wrk:commit:<sha>/<path>" renders a read-only
  // ReviewPage narrowed to that one file. The virtual flag keeps the viewer's
  // data loader / tree_changed reload / persistence away from the pseudo-path.
  const handleOpenFileInViewer = useCallback((sha: string, path: string) => {
    const { openVirtualFile, setCollapsed } = useFileViewerStore.getState()
    openVirtualFile(`c0wrk:commit:${sha}/${path}`)
    setCollapsed(false)
  }, [])

  // ── Lane layout over the full commit set ────────────────────────────
  const nodes = useMemo(() => computeGraphLayout(commits), [commits])
  const shaToNode = useMemo(() => {
    const map = new Map<string, GraphNode>()
    for (const n of nodes) map.set(n.sha, n)
    return map
  }, [nodes])
  const shaToCommit = useMemo(() => {
    const map = new Map<string, (typeof commits)[number]>()
    for (const c of commits) map.set(c.sha, c)
    return map
  }, [commits])

  // ── Session-restored expansion: the single lazy-fetch trigger ───────
  // A persisted expanded card either mounts already-expanded (tab reopen,
  // viewer uncollapse, project switch back) or only scrolls into view
  // later (a SHA sitting beyond the first page arrives via loadMore).
  // Whenever the persisted SHA is present in the loaded history, fire the
  // exact lazy fetches an interactive expand would. Every fetch dedupes
  // (cache + in-flight guards in ensureMessage/fetchFiles/ensureDiff), so
  // re-runs of this effect are no-ops, and a SHA absent from the history
  // (rebased away, foreign project) fetches nothing.
  useEffect(() => {
    if (expandedSha === null || !shaToCommit.has(expandedSha)) return
    void fetchFiles(expandedSha)
    ensureMessage(expandedSha)
    // Open file diffs of a restored card need the commit diff — the
    // interactive path gets it from handleFileToggle, restoration here.
    if ((expandedFiles[expandedSha]?.size ?? 0) > 0) ensureDiff(expandedSha)
  }, [expandedSha, shaToCommit, expandedFiles, fetchFiles, ensureMessage, ensureDiff])

  const rowHeightFor = useCallback(
    (sha: string) => cardHeight(cardStateFor(sha), expandedSha === sha),
    [cardStateFor, expandedSha],
  )
  // Heights over the FULL commit set — the gutter always spans every row
  // (it renders only when the filter is off, i.e. over the full set).
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

  // The rows the list renders: every commit normally, the matching subset
  // while the filter is active (then the gutter is hidden — a filtered
  // subset is not a connected graph).
  const visibleNodes = useMemo(() => {
    if (!isFiltering) return nodes
    return filteredCommits
      .map((c) => shaToNode.get(c.sha))
      .filter((n): n is GraphNode => n !== undefined)
  }, [isFiltering, nodes, filteredCommits, shaToNode])

  // ── Virtualization ─────────────────────────────────────────────────
  // Exact per-row heights (from the deterministic math) so the virtualizer
  // positions items without DOM measurement (which is unreliable for
  // dynamically expanded rows, and returns 0 in jsdom).
  const scrollRef = useRef<HTMLDivElement>(null)
  const virtualizer = useVirtualizer({
    count: visibleNodes.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: (i: number) => rowHeightFor(visibleNodes[i]?.sha ?? ''),
    overscan: 8,
    getItemKey: (i: number) => visibleNodes[i]?.sha ?? i,
  })

  // Infinite-scroll edge trigger (idempotent loadMore — see useGitHistory).
  // The "Load more" button below is the keyboard path.
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
      {/* Toolbar: the file filter + the SHARED unified/split toggle. No page
          title — the viewer's tab ("Git History") already names the page. */}
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-secondary/30 px-2 py-1.5">
        <FilterBar
          className="flex-1 min-w-0 border-b-0 py-0"
          value={filterText}
          onChange={setFilterText}
          mode={filterMode}
          onToggleMode={toggleFilterMode}
          placeholder="Filter by file"
        />
        <DiffViewModeToggle />
      </div>
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="flex flex-col min-h-0 flex-1 overflow-y-auto custom-scrollbar"
      >
        {isFiltering && isInvalidFilter ? (
          <p className="p-4 text-center text-xs text-destructive">Invalid regex</p>
        ) : isFiltering && visibleNodes.length === 0 && !isResolvingFiles ? (
          <p className="p-4 text-center text-xs text-muted-foreground">No matching commits</p>
        ) : (
          <div className="shrink-0" style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
            {/* Lane gutter only over the FULL commit set; hidden while the
                filter narrows the list. */}
            {!isFiltering && (
              <div style={{ position: 'absolute', left: 0, top: 0 }}>
                <GitGraphGutter nodes={nodes} rowY={rowY} />
              </div>
            )}
            {virtualizer.getVirtualItems().map((vi) => {
              const node = visibleNodes[vi.index]
              if (!node) return null
              const commit = shaToCommit.get(node.sha)
              if (!commit) return null
              return (
                <div
                  key={vi.key}
                  style={{
                    position: 'absolute',
                    top: vi.start,
                    left: isFiltering ? 0 : svgWidth,
                    right: 0,
                    height: vi.size,
                  }}
                  onContextMenu={(e) =>
                    handleCardContextMenu(e, node.sha, commit.refs)
                  }
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
                    onFileContextMenu={(e, sha, path) => handleFileContextMenu(e, sha, path)}
                  />
                </div>
              )
            })}
          </div>
        )}
        {isResolvingFiles && (
          <div className="flex items-center justify-center gap-1.5 py-2 text-xs text-muted-foreground">
            <Loader2 className="size-3.5 animate-spin" /> Resolving files…
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
      <CommitFileContextMenu
        path={fileMenu?.path ?? ''}
        position={fileMenu ? { x: fileMenu.x, y: fileMenu.y } : null}
        onClose={closeFileMenu}
        onOpenInViewer={(path) => {
          if (fileMenu) handleOpenFileInViewer(fileMenu.sha, path)
        }}
      />
    </div>
  )
}
