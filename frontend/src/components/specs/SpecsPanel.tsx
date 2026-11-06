// The SPECS panel (ADR-106): a view over specsStore plus the refresh
// subscription. Reads open in the file viewer; every mutating gesture is a
// dispatched spec-* skill (the quick actions); drift is the two-tier signal
// (header badge + findings list here, "Check drift" for the semantic tier).

import { useCallback, useEffect } from 'react'
import { AlertCircle } from 'lucide-react'
import { subscribe } from '@/api/runtime'
import { useProjectStore } from '@/stores/projectStore'
import { useSpecsStore } from '@/stores/specsStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { SpecsHeader } from './SpecsHeader'
import { SpecsQuickActions } from './SpecsQuickActions'
import { SpecsEmptyState } from './SpecsEmptyState'
import { SpecsSectionTree } from './SpecsSectionTree'
import { SpecsFindings } from './SpecsFindings'
import { adrDatesByFile, isSpecsRelevantChange, joinSpecPath } from './specsPaths'
import { buildFixFindingAction, buildFixFindingsAction } from './specsActions'
import { useSpecsDispatch } from './useSpecsDispatch'
import type { SpecFinding, SpecsStatus } from '@/types/models'

/**
 * Refresh model (ADR-106 D3): one light read-only parse on mount — the
 * tabpanel mounts only while the Specs tab is active, so every tab
 * activation refreshes — plus a refetch on `workspace:tree_changed` events
 * that touch specs/ paths.
 *
 * The subscription lives HERE, inside the panel, deliberately NOT in an
 * App-root bridge: this panel is the single consumer of specsStore
 * (research's root bridge exists only because two surfaces — sidebar panel
 * + workspace tab — share its store), so a bridge would be a second
 * always-mounted subscription for nothing. The panel unmount with the tab
 * unsubscribes with it.
 */
export function SpecsPanel() {
  const projectId = useProjectStore((s) => s.activeProjectId)
  // Cross-project stale-status guard (the gitRepoProjectId pairing
  // convention of this panel strip): the store's status renders only while
  // it belongs to the ACTIVE project, so a previous project's parse never
  // reaches the layout — not even for the frames between a project switch
  // and the switch's own fetch clearing the store. Both branches return
  // store-held references, so the selector stays referentially stable.
  const status = useSpecsStore((s) =>
    projectId !== null && s.projectId === projectId ? s.status : null,
  )
  const isLoading = useSpecsStore((s) => s.isLoading)
  const error = useSpecsStore((s) => s.error)
  const fetchStatus = useSpecsStore((s) => s.fetchStatus)
  const dispatch = useSpecsDispatch()

  // Mount / project switch: the initial parse. Never in No-Project (the
  // tab strip is absent there; the guard below is the defensive neutral).
  useEffect(() => {
    if (projectId !== null) void fetchStatus(projectId)
  }, [projectId, fetchStatus])

  // tree_changed → refetch, scoped to specs/-touching changes when the
  // payload names paths (fail-open otherwise — see isSpecsRelevantChange).
  // The root is read from the store at event time, not captured at render.
  useEffect(() => {
    if (projectId === null) return
    const onChange = (data: unknown) => {
      const store = useSpecsStore.getState()
      if (!isSpecsRelevantChange(data, store.status?.root ?? '')) return
      void store.fetchStatus(projectId)
    }
    return subscribe('workspace:tree_changed', onChange)
  }, [projectId])

  // Read path (ADR-106 D4): a specs-root-relative reference resolves
  // against the absolute root and opens through the existing file viewer —
  // no new file-read RPC. The root is read from the store AT CLICK TIME
  // and only from the CURRENT project's status: a stale cross-project root
  // must never reach the viewer (defense in depth under the render guard —
  // the clickable rows themselves are already project-scoped).
  const openSpec = useCallback(
    (relPath: string) => {
      const store = useSpecsStore.getState()
      if (store.projectId !== projectId || store.status === null) return
      const viewer = useFileViewerStore.getState()
      viewer.setCollapsed(false)
      viewer.openFile(joinSpecPath(store.status.root, relPath))
    },
    [projectId],
  )

  // Fix-findings gesture (ADR-109): the header's findings badge dispatches
  // one spec-update task whose prompt enumerates the CURRENT parse's
  // findings verbatim. Built at click time from the store so the task sees
  // exactly what the panel renders (a mid-flight refetch updates the list).
  const onFixFindings = useCallback(
    (newSession: boolean) => {
      const store = useSpecsStore.getState()
      if (store.projectId !== projectId || store.status === null) return
      dispatch(buildFixFindingsAction(store.status.findings), newSession)
    },
    [projectId, dispatch],
  )

  // Per-finding fix gesture (ADR-109): a finding card's badge dispatches
  // one spec-update task scoped to THAT single finding. The same guard as
  // the aggregate badge — only the CURRENT project's parse may dispatch.
  const onFixFinding = useCallback(
    (finding: SpecFinding, newSession: boolean) => {
      const store = useSpecsStore.getState()
      if (store.projectId !== projectId || store.status === null) return
      dispatch(buildFixFindingAction(finding), newSession)
    },
    [projectId, dispatch],
  )

  // Manual refresh: the same refetch as the mount/tree_changed paths — a
  // SAME-PROJECT fetch keeps the previous status rendered (no flicker) and
  // the store's LWW ticket (fetchSeq) serializes overlapping flights, so a
  // slow response never clobbers a newer parse. The guard mirrors the
  // other two entry points; the render path below already returns null
  // without a project, making the null check the defensive neutral.
  const onRefresh = useCallback(() => {
    if (projectId !== null) void fetchStatus(projectId)
  }, [projectId, fetchStatus])

  // Defensive No-Project neutral render (the tab never mounts there).
  if (projectId === null) return null

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="specs-panel">
      {error !== null && (
        <div className="flex items-center gap-1.5 border-b border-destructive/20 bg-destructive/10 px-3 py-1.5 text-xs text-destructive">
          <AlertCircle className="size-3.5 shrink-0" />
          <span className="truncate" data-testid="specs-error">
            {error}
          </span>
        </div>
      )}

      {status === null ? (
        <div className="p-3 text-xs text-muted-foreground">
          {isLoading ? 'Loading specs…' : 'No specs data.'}
        </div>
      ) : (
        <>
          {/* The header renders in BOTH branches: the badge stays visible
              (inert without a present tree) even when the body swaps, so
              the affordance never teleports in and out on present flips. */}
          <SpecsHeader status={status} onFixFindings={onFixFindings} onRefresh={onRefresh} isLoading={isLoading} />
          {status.present ? (
            <>
              <SpecsQuickActions present />
              <div className="custom-scrollbar flex min-h-0 flex-1 flex-col gap-2 overflow-auto px-1.5 py-1.5">
                {/* The sidebar carries the section tree (with the DECISIONS
                    date column, ADR-109) and the findings. */}
                <PanelTree
                  projectId={projectId}
                  files={status.files}
                  adrDates={adrDatesByFile(status.adrs)}
                  onOpen={openSpec}
                />
                <SpecsFindings
                  findings={status.findings}
                  onOpen={openSpec}
                  onFixFinding={onFixFinding}
                />
              </div>
            </>
          ) : (
            <SpecsEmptyState root={status.root} />
          )}
        </>
      )}
    </div>
  )
}

/** The sidebar tree with the store-owned, per-project expansion: the panel
 *  unmounts on every tab switch, so the expanded set lives in specsStore
 *  (keyed by the active project and persisted) rather than in component
 *  state. Everything starts collapsed. The DECISIONS date column reads the
 *  parse's ADR dates (ADR-109). */
function PanelTree({
  projectId,
  files,
  adrDates,
  onOpen,
}: {
  projectId: string
  files: SpecsStatus['files']
  adrDates: Record<string, string>
  onOpen: (relPath: string) => void
}) {
  // Both selectors return store-held references (the per-project map object
  // and the action), so they stay referentially stable across renders.
  const expandedNodes = useSpecsStore((s) => s.treeExpandedNodes[projectId])
  const toggleTreeNode = useSpecsStore((s) => s.toggleTreeNode)
  return (
    <SpecsSectionTree
      files={files}
      onOpen={onOpen}
      adrDates={adrDates}
      expandedNodes={expandedNodes}
      onToggleNode={toggleTreeNode}
    />
  )
}
