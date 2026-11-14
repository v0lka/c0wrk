import { useCallback, useEffect, useState } from 'react'
import { reindexVectorIndex } from '@/api/vector'
import { emit, subscribe } from '@/api/runtime'
import { generateUUID } from '@/lib/ids'
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
 * is running, and a plain reconcile cannot repair it — and the action is the
 * user's repair path exactly for the failure kinds a repair addresses
 * (`error_kind` `repairable` / `transient` / `suspended` / `unknown`; for
 * `suspended` the manager-level Reindex is the ADR-124 breaker's own reset
 * path): with no live
 * collection the backend re-attaches the snapshot view, which rejects an
 * unopenable corpus as unpublished and rebuilds it from source
 * (cache-spared), so the busy tooltip reads "Repairing index…" rather than an
 * incremental reindex. Registry-level kinds (`fatal` — e.g. the embedder
 * never came up; `no_target`; `shutting_down`) deterministically reject the
 * RPC before any repair, so the action renders DISABLED with a
 * reason-specific title there, and a rejection that still slips through is
 * surfaced as a runtime_error toast instead of a silent no-op. In No Project
 * (CHAT mode), where the vector index is disabled, the action is hidden
 * entirely instead of being rendered disabled.
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
  // renders WITHOUT the busy spinner. Whether it stays ENABLED is decided by
  // the failure KIND (`error_kind` on the same status): the repair escalation
  // addresses exactly the manager-level kinds — `repairable` (a proven store
  // defect; the rebuild heals it), `transient` (an OS fault; the re-attach
  // retries the open once the fault clears), `suspended` (the ADR-124
  // incremental-failure circuit breaker; the manager-level Reindex is the
  // breaker's own reset path — it re-opens file-change arming and relaunches
  // the pass, which fails honestly again if the embedder is still dead) and
  // the defensive `unknown` / missing kind (a classification gap must not
  // dead-end the user). The registry-level kinds deterministically REJECT the
  // RPC before any repair runs — `fatal` (the embedder never came up; the
  // remedy is the model install in Settings, not a rebuild), `no_target` (no
  // focus root to reindex), `shutting_down` — so for those the action renders
  // disabled with a reason-specific title instead of a click that can only
  // fail.
  const isIndexUnavailable = indexState === 'unavailable'
  const errorKind = useVectorIndexStore((s) => s.status.error_kind)
  // Absent kind (`omitempty` on the wire, or a hand-crafted status) is the
  // defensive branch: enable rather than dead-end the user on a gap.
  const repairKindSupported =
    !errorKind ||
    errorKind === 'repairable' ||
    errorKind === 'transient' ||
    errorKind === 'suspended' ||
    errorKind === 'unknown'
  const repairUnsupported = isIndexUnavailable && !repairKindSupported
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
  // (isBusy) — from then on isBusy owns the disabled state — when the project
  // becomes unavailable, when the RPC resolves (the pass is scheduled; a
  // duplicate click merely restarts the cancel-and-restart pass), or when the
  // RPC rejects.
  const [reindexRequested, setReindexRequested] = useState(false)
  const reindexBusy = isBusy || reindexRequested
  // The disabled set the button renders from: busy (a pass or the optimistic
  // latch), the master-switch-off terminal state, or an `unavailable` whose
  // kind the repair cannot address (see repairUnsupported above).
  const reindexDisabled = reindexBusy || isIndexDisabled || repairUnsupported

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
  // The reason-specific title for an `unavailable` whose kind the repair
  // cannot address (`fatal` / `no_target` / `shutting_down`; of the manager
  // taxonomy only `fatal` is excluded — nothing a rebuild can retry — every
  // retryable manager kind, including `suspended`, is supported above) — the
  // same honesty rule as every other disabled state: the title names why a
  // click would not help, and the inline `last_error` line in the panel
  // names the actual cause/remedy.
  const repairUnsupportedTitle =
    errorKind === 'no_target'
      ? 'No focused workspace to reindex'
      : errorKind === 'shutting_down'
        ? 'App is shutting down'
        : 'A rebuild cannot fix this failure — see the status cause below'
  const reindexDisabledTitle = repairUnsupported && !reindexBusy
    ? repairUnsupportedTitle
    : isIndexDisabled && !reindexBusy
      ? 'Indexing is off — enable it in Settings → Search'
      : reindexBusyTitle
  // The action label for an ENABLED button: an unavailable index is rebuilt
  // (the broken store is discarded and the corpus recreated from source),
  // which is a different promise from the ordinary force-reindex — except
  // the ADR-124 suspension, where the collection is live and the same
  // manager-level Reindex runs a validation-based reconcile that RESUMES
  // indexing (the breaker's own reset path), so the title must not promise
  // a rebuild it does not perform. The Resume label keys on the suspended
  // KIND alone, not on the unavailable state: the verdict can ride a READY
  // status (the pair-persistence rule — a failing branch-switch pass's
  // terminal ready report), and the action is the same reset either way.
  const reindexActionTitle =
    errorKind === 'suspended'
      ? 'Resume indexing — rerun the pass and re-arm file changes'
      : isIndexUnavailable
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
    // the busy status (see reindexRequested above), against the
    // master-switch-off state, and against an `unavailable` whose kind the
    // repair cannot address (the RPC rejects it before any pass). A supported
    // `unavailable` is deliberately NOT guarded: with no live collection the
    // backend escalates the request into a repair.
    if (isBusy || isIndexDisabled || repairUnsupported || reindexRequested) return
    setReindexRequested(true)
    reindexVectorIndex()
      .then(() => {
        // The RPC resolved — the pass is scheduled. Release the latch: the
        // first busy status event takes over the disabled state, and until it
        // lands a duplicate click merely restarts the pass (Reindex cancels
        // any in-flight one first) instead of firing a parallel request.
        setReindexRequested(false)
      })
      .catch((err: unknown) => {
        // The request never reached a running pass — release the latch so a
        // retry is possible, and surface the refusal through the app-wide
        // runtime_error toast: a click that only logs to the console reads
        // as a broken repair action. The backend error carries the routing
        // cause (embedder, shutdown, no target), which names the real
        // remedy. The wording is neutral because the click may have been
        // either repair title — the rebuild or the ADR-124 resume — and the
        // toast must not contradict the action the user actually took.
        setReindexRequested(false)
        const message = err instanceof Error ? err.message : String(err)
        // generateUUID, never the raw crypto.randomUUID: this UI is also
        // served over plain HTTP on non-loopback addresses by the web-access
        // sidecar (ADR-101), where randomUUID is absent and the raw call
        // would throw INSIDE the catch — no toast, an unhandled rejection.
        emit('runtime_error', {
          id: generateUUID(),
          message: `Index repair failed: ${message}`,
        })
      })
  }, [isBusy, isIndexDisabled, repairUnsupported, reindexRequested])

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
