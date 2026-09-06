// Shared graph-application orchestrator for every research mutation /
// incremental-fetch site ([18]b). The file-watcher path always guarded its
// applies; the two direct mutation paths (the workspace editor's save and
// the dashboard's quick status flip) previously applied their responses
// unconditionally. Every site now funnels through one helper that owns the
// two guards the review found missing:
//
//   1. ([18]a) the active c0wrk project must STILL be the one the fetch or
//      RPC targeted when the response lands — a project switch mid-flight
//      drops the payload silently instead of painting the old project's
//      graph onto the new project's panel (R-NNN ids collide across
//      projects, so the stale graph would otherwise be accepted).
//   2. loadGraph() === false (a research project the store has never seen,
//      or a stale [60] LWW ticket) means the incremental apply is
//      impossible — converge via the full status refetch so the panel never
//      freezes on stale data.
//
// Pure module — no React — so it is unit-testable against the real stores
// (mirroring researchLogUtils / researchWorkspaceUtils).

import { getResearchStatus, getResearchNextStep } from '@/api/research'
import { logger } from '@/lib/logger'
import { useProjectStore } from '@/stores/projectStore'
import { useResearchStore } from '@/stores/researchStore'
import type { ResearchGraphResponse } from '@/types/models'

/**
 * Best-effort full status + next-step refetch (the incremental path's
 * fallback, shared by all mutation sites). Captures its own [60] LWW ticket
 * at fetch start, applies only while the target project is still active,
 * and is quiet on failure — the watchdog in useResearchStatusEvents is the
 * outer safety net.
 *
 * The recommended next step is refreshed alongside the status ([54]): this
 * fallback runs on the very paths where the callers' own next-step fetch is
 * skipped, so without it a phase flip during those edits would leave a
 * stale recommendation on the dashboard — within the same project,
 * potentially indefinitely.
 */
export async function fullResearchRefresh(projectId: string): Promise<void> {
  // [60] LWW ticket: capture the sync sequence at fetch START so the store
  // can reject this payload when a newer sync lands while the fetch is in
  // flight.
  const startedSeq = useResearchStore.getState().graphSyncSeq
  try {
    const status = await getResearchStatus(projectId)
    if (useProjectStore.getState().activeProjectId === projectId) {
      useResearchStore.getState().loadStatus(status, projectId, startedSeq)
    }

    // Best-effort: a failure leaves the previous recommendation in place
    // and never surfaces as a status error.
    try {
      const nextStep = await getResearchNextStep(projectId)
      if (useProjectStore.getState().activeProjectId === projectId) {
        useResearchStore.getState().loadNextStep(nextStep)
      }
    } catch (err) {
      logger.debug('[research] fallback next-step fetch failed:', err)
    }
  } catch (err) {
    logger.debug('[research] fallback status fetch failed:', err)
  }
}

/**
 * Apply a fetched/mutated graph response to the research store — the single
 * convergence path shared by the three graph-writing sites (the watcher's
 * incremental fetch, the editor's save, and the quick-mutate status flip).
 *
 * `projectId` is the c0wrk project the fetch/RPC targeted, `startedSeq` the
 * store's graphSyncSeq captured when the caller STARTED (the [60] LWW
 * ticket). The payload is applied via loadGraph only while the active
 * project still matches; a `false` result (unknown research project / stale
 * ticket) falls back to the full status refetch so the panel converges
 * instead of freezing.
 */
export async function applyGraphOrRefresh(
  graph: ResearchGraphResponse,
  projectId: string,
  startedSeq: number,
): Promise<void> {
  // [18]a: the panel now belongs to a different c0wrk project — drop the
  // payload. The new project's own mount/switch load is authoritative;
  // applying here would paint the old project's graph onto it.
  if (useProjectStore.getState().activeProjectId !== projectId) return

  const applied = useResearchStore.getState().loadGraph(graph, startedSeq)
  if (applied) {
    logger.debug(
      '[research] graph applied',
      'project',
      projectId,
      'nodes',
      graph.graph.nodes.length,
    )
    return
  }
  // loadGraph could not apply incrementally (a brand-new R-NNN the store
  // has never seen, or a stale LWW ticket) — converge via the full refetch.
  await fullResearchRefresh(projectId)
}
