import { useCallback, useEffect, useState } from 'react'
import { reindexVectorIndex } from '@/api/vector'
import { subscribe } from '@/api/runtime'
import { useProjectStore } from '@/stores/projectStore'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'

/**
 * The force-full-reindex action for the semantics (vector store) search
 * panel. The vector index state drives it: the action is disabled and shown
 * spinning while the index is already busy — a pass in flight, or the
 * branch-scoped DB still opening (`loading`, ADR-064) — or while a request is
 * pending (see the optimistic latch below). In No Project (CHAT mode), where
 * the vector index is disabled, the action is hidden entirely instead of being
 * rendered disabled.
 *
 * Extracted from FileTreePanel when the button moved to the search panel's
 * mode-selector row (hybrid/vector/lexical); ADR-064 later folded the
 * branch-scoped DB open (`loading`) into the busy set and gave the disabled
 * tooltip a reason-specific label.
 */
export function useVectorReindex() {
  const projects = useProjectStore((s) => s.projects)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const isNoProject = projects?.find((p) => p.id === activeProjectId)?.is_no_project === true

  const indexState = useVectorIndexStore((s) => s.status.state)
  // `loading` is the branch-scoped chromem DB open (ADR-064): no pass is
  // running yet, but the index is busy — a reindex fired now would race the
  // open and, until the manager publishes the indexer, could only fail. It
  // counts as busy so the action stays disabled and spinning, exactly as it did
  // while the open still reported `indexing` before ADR-064.
  const isPassRunning = indexState === 'indexing' || indexState === 'reindexing'
  const isOpening = indexState === 'loading'
  const isBusy = isPassRunning || isOpening
  const reindexUnavailable = isNoProject || !activeProjectId

  // Optimistic latch: between the click and the arrival of the first
  // `vector_index:status` event the store still reports the previous (non-busy)
  // state, so without this flag a quick second click would fire a duplicate
  // reindex RPC. It is released as soon as the store reflects the busy state
  // (isBusy) — from then on isBusy owns the disabled state — or when the
  // project becomes unavailable or the RPC rejects.
  const [reindexRequested, setReindexRequested] = useState(false)
  const reindexBusy = isBusy || reindexRequested

  // The action's tooltip names the REAL reason it is disabled, so the open is
  // never labelled a reindex pass — the same honesty rule the status-bar pill
  // and the search panel follow (ADR-064). The optimistic latch (a request in
  // flight, no busy status observed yet) reads as a reindex: that is what the
  // user just asked for.
  const reindexBusyTitle = isOpening && !reindexRequested ? 'Opening the index…' : 'Reindexing...'

  useEffect(() => {
    // The backend emits a busy status (loading/indexing/reindexing) as the first
    // event of a project open or of a pass; once the store reflects it the latch
    // is redundant. A switch to an unavailable project (No Project / no active
    // project) also invalidates a pending request.
    if (isBusy || reindexUnavailable) setReindexRequested(false)
  }, [isBusy, reindexUnavailable])

  // Missed-status release: if the backend resolves the reindex RPC without
  // the store ever observing a busy state (a skipped/coalesced pass), the
  // latch above would never release and the button would stay disabled until
  // a project switch. Any `vector_index:status` event arriving after the
  // request means the backend has answered — a busy state flips `isBusy`
  // (which owns the disabled state from then on), and any other state is
  // terminal for the request — so the latch is released either way.
  useEffect(() => {
    if (!reindexRequested) return
    return subscribe('vector_index:status', () => setReindexRequested(false))
  }, [reindexRequested])

  const handleReindex = useCallback(() => {
    // Fire-and-forget: the pass runs in the background and reports progress via
    // vector_index:status. Guard against re-entry before the store has observed
    // the busy status (see reindexRequested above).
    if (isBusy || reindexRequested) return
    setReindexRequested(true)
    reindexVectorIndex().catch(() => {
      // The request never reached a running pass (No Project / no wired
      // manager / transient failure) — release the latch so a retry is possible.
      setReindexRequested(false)
    })
  }, [isBusy, reindexRequested])

  return { reindexBusy, reindexBusyTitle, reindexUnavailable, handleReindex }
}
