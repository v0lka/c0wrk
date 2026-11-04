// The SPECS panel (ADR-105): a view over specsStore plus the refresh
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
import { SpecsIndexTable } from './SpecsIndexTable'
import { SpecsSectionTree } from './SpecsSectionTree'
import { SpecsADRTimeline } from './SpecsADRTimeline'
import { SpecsFindings } from './SpecsFindings'
import { isSpecsRelevantChange, joinSpecPath } from './specsPaths'

/**
 * Refresh model (ADR-105 D3): one light read-only parse on mount — the
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
  const status = useSpecsStore((s) => s.status)
  const isLoading = useSpecsStore((s) => s.isLoading)
  const error = useSpecsStore((s) => s.error)
  const fetchStatus = useSpecsStore((s) => s.fetchStatus)

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

  // Read path (ADR-105 D4): a specs-root-relative reference resolves
  // against the absolute root and opens through the existing file viewer —
  // no new file-read RPC.
  const root = status?.root ?? ''
  const openSpec = useCallback(
    (relPath: string) => {
      const viewer = useFileViewerStore.getState()
      viewer.setCollapsed(false)
      viewer.openFile(joinSpecPath(root, relPath))
    },
    [root],
  )

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
      ) : !status.present ? (
        <SpecsEmptyState root={status.root} />
      ) : (
        <>
          <SpecsHeader status={status} />
          <SpecsQuickActions present={status.present} />
          <div className="custom-scrollbar flex min-h-0 flex-1 flex-col gap-2 overflow-auto px-1.5 py-1.5">
            <SpecsIndexTable indexTasks={status.index_tasks} onOpen={openSpec} />
            <SpecsSectionTree files={status.files} onOpen={openSpec} />
            <SpecsADRTimeline adrs={status.adrs} onOpen={openSpec} />
            <SpecsFindings findings={status.findings} onOpen={openSpec} />
          </div>
        </>
      )}
    </div>
  )
}
