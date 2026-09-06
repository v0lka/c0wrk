import { useRef, useState } from 'react'
import { Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { logger } from '@/lib/logger'
import { updateHypothesis } from '@/api/research'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import { applyGraphOrRefresh } from './applyGraphOrRefresh'
import { statusColorVar } from './researchDagRender'
import { statusOptions } from './hypothesisStatus'
import type { HypothesisNode, HypothesisStatus } from '@/types/models'

/**
 * Quick mutations — a compact status changer for each hypothesis on the active
 * front (open / in-progress). Each dropdown persists through the t4
 * `updateHypothesis` RPC and applies the refreshed graph through the shared
 * `applyGraphOrRefresh` convergence path ([18]b), so a status flip is a single
 * click with no workspace round-trip.
 */
export function ResearchQuickMutate() {
  const project = useResearchStore(selectActiveProject)
  const [savingId, setSavingId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  // [71] Action generation: bumped at every mutation START and captured by
  // the mutating action. Effects on this block's local state (savingId /
  // error) apply only while the captured generation is current, so a late
  // resolve/failure of an OLDER flip can neither re-enable nor annotate
  // over a NEWER flip's state (same-path re-entry the disabled selects
  // cannot fully rule out).
  const generationRef = useRef(0)

  const activeFront = project?.metrics.active_front ?? []
  const nodes = project?.graph.nodes ?? []
  const frontNodes = activeFront
    .map((id) => nodes.find((n) => n.id === id))
    .filter((n): n is HypothesisNode => n !== undefined)

  const changeStatus = async (node: HypothesisNode, status: HypothesisStatus) => {
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId || status === node.status) return
    // [19]a: resolve the research project from the LIVE store at action
    // start — the click's render snapshot can be one sync behind a
    // research-init that switched the active R-NNN, and H-001-style ids
    // collide across projects. The fresh read (plus the backend's [19]b
    // expected-R validation) keeps the flip on the project the user sees.
    const store = useResearchStore.getState()
    const researchId = selectActiveProject(store)?.id ?? null
    if (!researchId) return
    // Cross-project guard (see useHypothesisEditor's handleSave): the
    // research store's snapshot is stamped with the workspace project it
    // was loaded for. After a workspace project switch the store can keep
    // rendering the OLD project's graph until the new project's status
    // fetch lands, and R-NNN / H-NNN ids collide across projects — an
    // unconditional flip would overwrite the NEW project's card. Bail
    // without sending.
    if (store.projectId !== projectId) {
      setError(
        'The research view belongs to a different project — re-open it after the project switch.',
      )
      return
    }

    // [71]: capture this action's generation.
    const generation = ++generationRef.current
    setSavingId(node.id)
    setError(null)
    try {
      // [60] LWW ticket: capture the sync sequence at RPC START so the
      // store can reject this response when a newer sync (watchdog refresh,
      // file-watcher fallback) lands while the mutation is in flight —
      // applying the older snapshot would visually revert the flip.
      const startedSeq = useResearchStore.getState().graphSyncSeq
      const res = await updateHypothesis(projectId, researchId, node.id, { status })
      // [18]b: apply through the shared convergence helper (active-project
      // re-check + incremental loadGraph + full-refetch fallback).
      if (generation === generationRef.current) {
        await applyGraphOrRefresh(res, projectId, startedSeq)
      }
    } catch (err) {
      if (generation === generationRef.current) {
        logger.error('Failed to update hypothesis status:', err)
        setError(err instanceof Error ? err.message : 'Failed to update hypothesis')
      }
    } finally {
      if (generation === generationRef.current) {
        setSavingId(null)
      }
    }
  }

  if (frontNodes.length === 0) {
    return null
  }

  // [21]a: any in-flight save disables the WHOLE block — concurrent
  // cross-node flips interleave their loadGraph writes and share one error
  // banner, so the block mutates one hypothesis at a time.
  const blockDisabled = savingId !== null

  return (
    <div
      data-testid="research-quick-mutate"
      aria-busy={blockDisabled}
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
              disabled={blockDisabled}
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
