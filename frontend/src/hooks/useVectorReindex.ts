import { useCallback, useEffect, useState } from 'react'
import { reindexVectorIndex } from '@/api/vector'
import { subscribe } from '@/api/runtime'
import { useProjectStore } from '@/stores/projectStore'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'

/**
 * The force-full-reindex action for the semantics (vector store) search
 * panel. The vector index state drives it: the action is disabled and shown
 * spinning while a pass is already in flight (or a request is pending — see
 * the optimistic latch below). In No Project (CHAT mode), where the vector
 * index is disabled, the action is hidden entirely instead of being rendered
 * disabled.
 *
 * Extracted from FileTreePanel when the button moved to the search panel's
 * mode-selector row (hybrid/vector/lexical); the behavior is unchanged.
 */
export function useVectorReindex() {
  const projects = useProjectStore((s) => s.projects)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const isNoProject = projects?.find((p) => p.id === activeProjectId)?.is_no_project === true

  const indexState = useVectorIndexStore((s) => s.status.state)
  const isIndexing = indexState === 'indexing' || indexState === 'reindexing'
  const reindexUnavailable = isNoProject || !activeProjectId

  // Optimistic latch: between the click and the arrival of the first
  // `vector_index:status` event the store still reports the previous (non-busy)
  // state, so without this flag a quick second click would fire a duplicate
  // reindex RPC. It is released as soon as the store reflects the busy state
  // (isIndexing) — from then on isIndexing owns the disabled state — or when
  // the project becomes unavailable or the RPC rejects.
  const [reindexRequested, setReindexRequested] = useState(false)
  const reindexBusy = isIndexing || reindexRequested

  useEffect(() => {
    // The backend emits a busy status (indexing/reindexing) as the first event
    // of a pass; once the store reflects it the latch is redundant. A switch to
    // an unavailable project (No Project / no active project) also invalidates
    // a pending request.
    if (isIndexing || reindexUnavailable) setReindexRequested(false)
  }, [isIndexing, reindexUnavailable])

  // Missed-status release: if the backend resolves the reindex RPC without
  // the store ever observing a busy state (a skipped/coalesced pass), the
  // latch above would never release and the button would stay disabled until
  // a project switch. Any `vector_index:status` event arriving after the
  // request means the backend has answered — a busy state flips `isIndexing`
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
    if (isIndexing || reindexRequested) return
    setReindexRequested(true)
    reindexVectorIndex().catch(() => {
      // The request never reached a running pass (No Project / no wired
      // manager / transient failure) — release the latch so a retry is possible.
      setReindexRequested(false)
    })
  }, [isIndexing, reindexRequested])

  return { reindexBusy, reindexUnavailable, handleReindex }
}
