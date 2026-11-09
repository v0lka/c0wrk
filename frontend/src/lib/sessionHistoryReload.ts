// Session history (re)load — the shared loader behind the ChatArea mount
// effect and the shallow-resync triggers (WS re-connect, window focus).
//
// Extracted from ChatArea so a re-run does not require a session switch: the
// merge into chatStore (mergeHistoryMessages) dedupes by message id and
// preserves live messages that arrived after the load started (plus unresolved
// HITL prompts), so re-running this loader on a session the user is watching
// catches the store up with the backend without duplicating or dropping
// anything — including an in-flight stream, whose tail lives in
// chatStore.streamingText and is never touched here.
//
// Failure contract: the history RPC rejection is rendered by the shared
// reportHistoryLoadFailure card (deterministic per-session id — repeated
// failures upsert, a populated transcript logs instead); reconcile-phase
// errors still propagate to the caller, whose catch routes through the same
// helper. Everything after the merge is best-effort: runtime-status and
// pending-action wrappers already resolve to null on failure, and the stale
// resolution persist is fire-and-forget. On BOTH outcomes the wrapper's
// finally settles the chatStore historyLoaded marker, so the ChatArea
// history-loading spinner never hangs — except for a cancelled attempt
// (session switched mid-flight), whose snapshot never reached the store.

import { getSessionHistory, getSessionRuntimeStatus, getPendingActions, resolveStalePrompt } from '@/api/chat'
import { chatMessageToUI, isPersistableHistoryMessage, isLegacyReviewPromptRow, lastAgentMetricsFromHistory, isAgentMetricsRow, isRoutingRequestRow } from '@/lib/chatUtils'
import { restorePlanAndGoalFromHistory } from '@/lib/sessionStoreRestore'
import { reconcileRuntimeStatus, reconcilePendingActions, reconcileWorkUnits, stalePromptMatchField } from '@/lib/sessionRuntime'
import { logger } from '@/lib/logger'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { ChatMessageUI } from '@/types/messages'

export interface ReloadSessionHistoryOptions {
  /** Cancellation probe, polled between the async steps. ChatArea passes its
   *  effect's `cancelled` flag; the resync passes a live active-session check
   *  so a session switch mid-flight discards the result instead of writing
   *  another session's snapshot into the stores. When true the load stops
   *  without touching any store. */
  isCancelled?: () => boolean
}

/**
 * Load the whole session history in one request, merge it into chatStore
 * (merge-dedup), rebuild the plan panel and goal badge from it, then
 * reconcile the store against the authoritative runtime status and
 * pending-action set. Re-runnable for the same session: every write lands
 * through idempotent merge/reconcile paths.
 */
export async function reloadSessionHistory(
  sessionId: string,
  options: ReloadSessionHistoryOptions = {},
): Promise<void> {
  const isCancelled = options.isCancelled ?? (() => false)
  const loadStartedAt = Date.now()
  try {
    await loadAndReconcile(sessionId, isCancelled, loadStartedAt)
  } finally {
    // Settle the ChatArea history-loading spinner on BOTH outcomes: resolve
    // (content merged, or a genuinely empty session — the placeholder branch
    // takes over) and reject (the caller's error card renders instead). A
    // cancelled attempt — the user switched sessions mid-flight — deliberately
    // leaves the marker unset: its snapshot never reached the store, so the
    // next visit to the session spins until its own load settles. The same
    // guard covers a mid-flight DELETION: marking a session the catalog no
    // longer knows would resurrect the marker dropSessions just cleared (and
    // a recreated session with a reused id would then skip its history load).
    if (!isCancelled() && sessionInCatalog(sessionId)) useChatStore.getState().markHistoryLoaded(sessionId)
  }
}

/** True while the session id still exists in the session catalog. */
function sessionInCatalog(sessionId: string): boolean {
  const sessions = useSessionStore.getState().sessions
  return sessions?.some((s) => s.id === sessionId) ?? false
}

/**
 * The shared "Failed to load session history" card emission behind BOTH
 * failure sites (the load-phase catch inside the loader and ChatArea's
 * reconcile-phase catch). One contract for the card:
 *
 *  - Deterministic per-session id (`history-load-failed:<sessionId>`, the
 *    HITL-card pattern): {@link useChatStore.addMessage} upserts by id, so a
 *    repeated failure — each focus/visibility/WS-reconnect resync trigger
 *    re-runs the loader — refreshes the one card in place instead of stacking
 *    a fresh "Failed to load session history" card per trigger.
 *  - A populated transcript never gets the card: a failure beneath a fully
 *    rendered transcript is a transient resync miss, logged and left to the
 *    next trigger. Only an empty (or never-loaded) transcript renders the
 *    card — a load failure can be the only visible entry of a session.
 *  - The `historyLoadFailed` metadata flag keeps the card out of the
 *    workspace selector's "confirmed empty" freshness predicate in both
 *    branches (see SessionWorkspaceSelector.isFresh) and lets the
 *    success-path reapers (ensureEmptyTranscript) clear it as stale.
 */
export function reportHistoryLoadFailure(sessionId: string, phase: string): void {
  const store = useChatStore.getState()
  const transcript = store.messages[sessionId]
  if (transcript !== undefined && Object.keys(transcript).length > 0) {
    logger.warn(
      `Failed to load session history (${phase}): transcript already populated — not appending the failure card`,
    )
    return
  }
  store.addMessage(sessionId, {
    id: `history-load-failed:${sessionId}`,
    sessionId,
    type: 'error',
    content: 'Failed to load session history. Please try switching sessions.',
    metadata: { historyLoadFailed: true },
    timestamp: Date.now(),
  })
}

/**
 * The load-and-reconcile body of {@link reloadSessionHistory}, kept as a
 * separate function so the exported wrapper owns the single try/finally that
 * settles the historyLoaded spinner marker on both resolve and reject.
 *
 * statusReadAt (stamped inside) predates the snapshot: live events that mutate
 * the activity label / streaming text after this point are fresher than the
 * snapshot's phase, and reconcileRuntimeStatus skips overwriting them.
 * loadStartedAt (stamped by the wrapper, before the first await) plays the
 * same role for messages inside mergeHistoryMessages.
 */
async function loadAndReconcile(
  sessionId: string,
  isCancelled: () => boolean,
  loadStartedAt: number,
): Promise<void> {
  let history: Awaited<ReturnType<typeof getSessionHistory>>
  try {
    history = await getSessionHistory(sessionId)
  } catch (err) {
    if (isCancelled()) return
    logger.error('Failed to load session history:', err)
    reportHistoryLoadFailure(sessionId, 'load phase')
    return
  }
  if (isCancelled()) return

  // An EMPTY history must NOT go through the merge: its loadStartedAt guard
  // would drop every stored row older than this load — on a fresh session
  // that is exactly the frontend-only transcript (a rejected send's error
  // card, an in-flight optimistic message) — so the empty case only
  // MATERIALIZES the confirmed-empty index the workspace selector's
  // freshness contract needs (an absent index reads as "history not loaded
  // yet" and would keep the workspace picker locked forever). With an index
  // already present, ensureEmptyTranscript keeps every live row untouched —
  // it only reaps superseded history-load-failure cards, which this
  // successful load proves stale.
  if (history.length === 0) {
    useChatStore.getState().ensureEmptyTranscript(sessionId)
  }

  if (history.length > 0) {
    // Filter out rows that must never surface in the chat:
    //  - "event_unknown" — transient UI events (attachments:changed,
    //    session_pinned, etc.) that leaked into the DB before they were
    //    marked transient in the persister. Their content is the raw JSON
    //    metadata payload, which would render as garbage text.
    //  - "review_prompt" — the removed post-task code-review prompt card
    //    (ADR-060). Legacy databases still carry one per completed task;
    //    without this they render as a stale "Uncommitted changes detected
    //    in this repository." status line at the end of the chat.
    const filteredHistory = history.filter(
      (msg) => isPersistableHistoryMessage(msg) && !isLegacyReviewPromptRow(msg),
    )
    const droppedCount = history.length - filteredHistory.length
    if (droppedCount > 0) {
      logger.debug(`Filtered ${droppedCount} non-displayable row(s) from session history`)
    }
    const uiMessages = filteredHistory.map((msg) => chatMessageToUI(msg))
    // agent_metrics rows are session store-state, not chat content (the
    // live handler writes them to planStore, never to the chat): restore
    // the newest report into the ExecutionPanels stats row and keep the
    // rows out of the message list so no raw-JSON card renders.
    const agentMetrics = lastAgentMetricsFromHistory(uiMessages)
    if (agentMetrics) {
      usePlanStore.getState().setSessionStats(sessionId, { lastAgentMetrics: agentMetrics })
    }
    // Drop store-state rows (agent_metrics) and the legacy per-task
    // "Routing request..." activity boilerplate so the chat shows only the
    // routing decision itself.
    const chatMessages = uiMessages.filter((m) => !isAgentMetricsRow(m) && !isRoutingRequestRow(m))
    // Merge (not replace) so live events delivered while the RPC was in
    // flight — e.g. a terminal `error` — are not clobbered.
    useChatStore.getState().mergeHistoryMessages(sessionId, chatMessages, loadStartedAt)
    // Rebuild the plan panel and goal badge from the loaded history: the
    // whole row set is present, so the plan declaration and the goal
    // snapshots are always available. Idempotent (the rebuild clears the
    // panel before replaying), so a resync re-run is safe.
    restorePlanAndGoalFromHistory(sessionId, chatMessages)
  }

  // Reconcile AFTER the merge so the store is populated. Fetch the
  // authoritative runtime status and pending-action set in parallel.
  const statusReadAt = Date.now()
  const [status, pending] = await Promise.all([
    getSessionRuntimeStatus(sessionId),
    getPendingActions(sessionId),
  ])
  if (isCancelled()) return

  // Capture the LIVE task-flag transition time BEFORE the status application
  // below writes through the stamping flag actions (setUnfinishedTaskStatus /
  // applyRuntimeTaskFlags → setTaskActive). The pending sweep's taskActive
  // heal is freshness-guarded against transitions newer than its read; the
  // status application itself stamps the map after that read, so without
  // this snapshot-of-the-stamp a stale active=false status would suppress
  // the very heal that exists to correct it. Only a non-null status stamps
  // anything before the pending application — with no status snapshot the
  // live map IS the pre-application value, so nothing is captured (undefined
  // makes reconcilePendingActions read the map directly).
  const liveFlagsEventAt = status ? useChatStore.getState().taskFlagsEventAt[sessionId] ?? 0 : undefined

  // Collect messages resolved as stale so their resolution can be persisted
  // (otherwise they reappear on the next reload).
  const staleResolved: ChatMessageUI[] = []
  if (status) {
    for (const msg of reconcileRuntimeStatus(sessionId, status, statusReadAt, loadStartedAt)) staleResolved.push(msg)
    // Align paused/interrupted delegate & plan-step blocks with the durable
    // work-unit snapshot (rendered via groupMessages). Runs AFTER the
    // history merge so the blocks exist to be corrected. statusReadAt
    // guards against a live settlement that landed after this snapshot was
    // read, and the same overlay repairs the execution plan panel — rebuilt
    // from the replayed history, it would otherwise keep spinning a step the
    // ledger settled as interrupted.
    const workUnitOverlay = reconcileWorkUnits(sessionId, status.work_units, statusReadAt)
    usePlanStore.getState().applyWorkUnitStatuses(workUnitOverlay)
  }
  if (pending) {
    // readAt = statusReadAt: the pending RPC was launched together with the
    // status RPC, so the same read time bounds both snapshot applications.
    // liveFlagsEventAt (captured above, BEFORE the status application) is
    // what the heal's freshness guard compares, so only LIVE pause/terminal
    // transitions outrank the sweep — never the status snapshot's own write.
    for (const msg of reconcilePendingActions(sessionId, pending, statusReadAt, liveFlagsEventAt)) staleResolved.push(msg)
  }

  // Persist stale resolutions to the backend (best-effort, idempotent).
  // Once persisted, the message reloads with resolved:true and is never
  // reprocessed.
  for (const msg of staleResolved) {
    const field = stalePromptMatchField(msg.type)
    if (!field) continue
    const value = msg.metadata?.[field]
    if (typeof value === 'string') {
      void resolveStalePrompt(sessionId, msg.type, field, value, { resolved: true, stale: true })
    }
  }
}
