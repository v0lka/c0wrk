import { useState } from 'react'
import { Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { logger } from '@/lib/logger'
import { updateHypothesis } from '@/api/research'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import { statusColorVar } from './researchDagRender'
import type { HypothesisNode, HypothesisStatus } from '@/types/models'

// Legal lifecycle transitions, mirroring the backend status state machine in
// core/research/writer.go. Each status may move only to the listed targets; a
// terminal status (confirmed/refuted/cancelled) has no outgoing transitions.
const STATUS_TRANSITIONS: Record<HypothesisStatus, readonly HypothesisStatus[]> = {
  open: ['in-progress', 'cancelled'],
  'in-progress': ['confirmed', 'refuted', 'cancelled'],
  confirmed: [],
  refuted: [],
  cancelled: [],
}

// statusOptions returns the current status (so the controlled <select> always
// has a matching option) followed by its legal transition targets, hiding
// illegal jumps (e.g. open → confirmed) that the backend would reject. The
// current status is typed as string because HypothesisNode.status may carry a
// non-canonical value from an older backend; unknown statuses fall back to no
// outgoing transitions.
function statusOptions(current: string): string[] {
  const targets = STATUS_TRANSITIONS[current as HypothesisStatus] ?? []
  return [current, ...targets.filter((s) => s !== current)]
}

/**
 * Quick mutations — a compact status changer for each hypothesis on the active
 * front (open / in-progress). Each dropdown persists through the t4
 * `updateHypothesis` RPC and applies the refreshed graph via `loadGraph`, so a
 * status flip is a single click with no workspace round-trip.
 */
export function ResearchQuickMutate() {
  const project = useResearchStore(selectActiveProject)
  const [savingId, setSavingId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const activeFront = project?.metrics.active_front ?? []
  const nodes = project?.graph.nodes ?? []
  const frontNodes = activeFront
    .map((id) => nodes.find((n) => n.id === id))
    .filter((n): n is HypothesisNode => n !== undefined)

  if (frontNodes.length === 0) {
    return null
  }

  const changeStatus = async (node: HypothesisNode, status: HypothesisStatus) => {
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId || status === node.status) return
    setSavingId(node.id)
    setError(null)
    try {
      const res = await updateHypothesis(projectId, node.id, { status })
      useResearchStore.getState().loadGraph(res)
    } catch (err) {
      logger.error('Failed to update hypothesis status:', err)
      setError(err instanceof Error ? err.message : 'Failed to update hypothesis')
    } finally {
      setSavingId(null)
    }
  }

  return (
    <div
      data-testid="research-quick-mutate"
      className="flex shrink-0 flex-col gap-1"
    >
      <span className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
        Active front
      </span>

      {frontNodes.map((node) => {
        const saving = savingId === node.id
        return (
          <div
            key={node.id}
            className="flex items-center gap-1.5 rounded bg-background/60 px-2 py-1"
          >
            <span
              className="size-2 shrink-0 rounded-full"
              style={{ backgroundColor: statusColorVar(node.status) }}
              aria-hidden
            />
            <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
              {node.id}
            </span>
            <span className="min-w-0 flex-1 truncate text-xs" title={node.title}>
              {node.title}
            </span>
            <select
              value={node.status}
              disabled={saving}
              aria-label={`Status for ${node.id}`}
              onChange={(e) =>
                void changeStatus(node, e.target.value as HypothesisStatus)
              }
              className="h-6 shrink-0 rounded border border-input bg-background px-1 text-[11px] outline-none focus:border-primary disabled:opacity-50"
            >
              {statusOptions(node.status).map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
            {saving && <Loader2 className="size-3 shrink-0 animate-spin text-muted-foreground" />}
          </div>
        )
      })}

      {error && (
        <p className={cn('text-xs text-destructive')} role="alert">
          {error}
        </p>
      )}
    </div>
  )
}
