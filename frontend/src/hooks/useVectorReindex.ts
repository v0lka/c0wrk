import { useCallback, useEffect, useState } from 'react'
import { reindexVectorIndex } from '@/api/vector'
import { subscribe } from '@/api/runtime'
import { useProjectStore } from '@/stores/projectStore'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'

/**
 * The force-full-reindex action for the semantics (vector store) search
 * panel. The vector index state drives it: the action is disabled and shown
 * spinning while the index is already busy — a pass in flight, the
 * branch-scoped DB still opening (`loading`, ADR-064), the ADR-104 stage-2
 * pre-work (`preparing` view-model attach window / `migration-rebuild`
 * legacy store promotion) — or while a request is pending (see the
 * optimistic latch below). A terminal `unavailable` index state (a failed
 * open or attach failure) runs the action WITHOUT a spinning pass — nothing
 * is running, and a plain reconcile cannot repair it. The action therefore
 * stays ENABLED in this state — it is the user's repair path: with no live
 * collection the backend re-attaches the snapshot view, which rejects an
 * unopenable corpus as unpublished and rebuilds it from source
 * (cache-spared), so the busy tooltip reads "Repairing index…" rather than an
 * incremental reindex. In No Project (CHAT
 * mode), where the vector index is disabled, the action is hidden entirely
 * instead of being rendered disabled.
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
  // `preparing` / `migration-rebuild` (ADR-104 stage 2) are the view-model
  // attach window and the one-time legacy store promotion — pre-work, not a
  // pass — but the index is busy exactly like the `loading` open: a reindex
  // fired now would race the attach/migration, and until it settles the
  // manager could only fail. They count as busy so the action stays disabled
  // and spinning (events.ts VALID_VECTOR_STATES admits them onto the store).
  const isAttaching = indexState === 'preparing' || indexState === 'migration-rebuild'
  // `unavailable` is terminal, not busy: a failed open/attach left no
  // collection, so there is nothing to validate or add to and the action
  // renders WITHOUT the busy spinner. It stays ENABLED: the backend escalates
  // a no-op reindex into a repair (re-attach the snapshot view, which rebuilds
  // an unopenable corpus from source), so this is the user's escape hatch — no
  // branch switch or project reopen (which cannot fix a durable corpus defect
  // and does not even re-run the attach for a cached manager) is required.
  const isIndexUnavailable = indexState === 'unavailable'
  // `disabled` (ADR-103) is terminal the same way: the master switch is off,
  // the registry is parked (every reindex RPC fails fast), and only Settings
  // can change it. Disabled without the spinner, with its own tooltip.
  const isIndexDisabled = indexState === 'disabled'
  const isBusy = isPassRunning || isOpening || isAttaching
  const reindexUnavailable = isNoProject || !activeProjectId

  // Optimistic latch: between the click and the arrival of the first
  // `vector_index:status` event the store still reports the previous (non-busy)
  // state, so without this flag a quick second click would fire a duplicate
  // reindex RPC. It is released as soon as the store reflects the busy state
  // (isBusy) — from then on isBusy owns the disabled state — or when the
  // project becomes unavailable or the RPC rejects.
  const [reindexRequested, setReindexRequested] = useState(false)
  const reindexBusy = isBusy || reindexRequested
  // The disabled set the button renders from: busy (a pass or the optimistic
  // latch) OR the master-switch-off terminal state. `unavailable` is NOT in it
  // — that is the repair path (see isIndexUnavailable above).
  const reindexDisabled = reindexBusy || isIndexDisabled

  // The action's tooltip names the REAL reason it is disabled, so pre-work is
  // never labelled a reindex pass — the same honesty rule the status-bar pill
  // and the search panel follow (ADR-064). The labels match IndexingStatus
  // exactly ('Preparing index…' / 'Migrating store…'). The optimistic latch (a
  // request in flight, no busy status observed yet) reads as a reindex: that
  // is what the user just asked for. `unavailable` is enabled, so it carries an
  // ACTION title instead (rebuild, not "reindex"); only `disabled` keeps a
  // reason-specific disabled title.
  const preWorkTitle = isOpening
    ? 'Opening the index…'
    : indexState === 'preparing'
      ? 'Preparing index…'
      : 'Migrating store…'
  const reindexBusyTitle =
    isIndexUnavailable
      ? 'Repairing index…'
      : (isOpening || isAttaching) && !reindexRequested ? preWorkTitle : 'Reindexing...'
  const reindexDisabledTitle =
    isIndexDisabled && !reindexBusy
      ? 'Indexing is off — enable it in Settings → Search'
      : reindexBusyTitle
  // The action label for an ENABLED button: an unavailable index is rebuilt
  // (the broken store is discarded and the corpus recreated from source), which
  // is a different promise from the ordinary force-reindex.
  const reindexActionTitle = isIndexUnavailable
    ? 'Rebuild index — recreate the broken index from source'
    : 'Force full project reindex'

  useEffect(() => {
    // The backend emits a busy status (loading/indexing/reindexing, and since
    // ADR-104 stage 2 preparing/migration-rebuild) as the first event of a
    // project open or of a pass; once the store reflects it the latch is
    // redundant. A switch to an unavailable project (No Project / no active
    // project) — or to a terminal unavailable/disabled index state (a repair
    // that did not produce a collection settles the request; the master switch
    // off rejects it) — also invalidates a pending request.
    if (isBusy || isIndexUnavailable || isIndexDisabled || reindexUnavailable) setReindexRequested(false)
  }, [isBusy, isIndexUnavailable, isIndexDisabled, reindexUnavailable])

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
    // the busy status (see reindexRequested above) and against the
    // master-switch-off state. `unavailable` is deliberately NOT guarded: with
    // no live collection the backend escalates the request into a repair.
    if (isBusy || isIndexDisabled || reindexRequested) return
    setReindexRequested(true)
    reindexVectorIndex().catch(() => {
      // The request never reached a running pass (No Project / no wired
      // manager / transient failure) — release the latch so a retry is possible.
      setReindexRequested(false)
    })
  }, [isBusy, isIndexDisabled, reindexRequested])

  return {
    reindexBusy,
    reindexBusyTitle,
    reindexDisabled,
    reindexDisabledTitle,
    reindexActionTitle,
    reindexUnavailable,
    handleReindex,
  }
}
