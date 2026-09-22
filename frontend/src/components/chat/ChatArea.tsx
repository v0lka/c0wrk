import { useEffect, useRef, useMemo } from 'react'
import { useChatStore, useSessionMessages, useSessionWorkUnits } from '@/stores/chatStore'
import { useBookmarkStore } from '@/stores/bookmarkStore'
import { groupMessages, stabilizeDisplayItems, chatMessageToUI, isPersistableHistoryMessage, isLegacyReviewPromptRow, lastAgentMetricsFromHistory, isAgentMetricsRow, isRoutingRequestRow } from '@/lib/chatUtils'
import { restorePlanAndGoalFromHistory } from '@/lib/sessionStoreRestore'
import { useSessionStore } from '@/stores/sessionStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { usePlanStore } from '@/stores/planStore'
import { getSessionHistory, getSessionRuntimeStatus, getPendingActions, resolveStalePrompt } from '@/api/chat'
import { useTaskFlagRestore } from '@/hooks/useTaskFlagRestore'
import { reconcileRuntimeStatus, reconcilePendingActions, reconcileWorkUnits, stalePromptMatchField } from '@/lib/sessionRuntime'
import { generateMessageId } from '@/lib/ids'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'
import { AssistantMessage } from './AssistantMessage'
import { ActivityIndicator } from './ActivityIndicator'
import { ChatScrollManager } from './ChatScrollManager'
import { ChatMessageRenderer, CompactErrorFallback } from './ChatMessageRenderer'
import { ChatHoverRegion } from './ChatHoverRegion'
import { ExecutionPanels } from './ExecutionPanels'
import { BlackboardPanel } from './BlackboardPanel'
import { BookmarksPanel } from './BookmarksPanel'
import { ChatInput } from './ChatInput'
import { ArchivedBanner } from './ArchivedBanner'
import { ScrollProvider } from './ScrollContext'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { MessageCircle } from 'lucide-react'
import { logger } from '@/lib/logger'

export function ChatArea() {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  // Whether the active session is archived. An archived session is read-only,
  // so the message input shell is replaced by an "Archived" banner. Returns a
  // primitive boolean for referential stability (no object allocation).
  const isArchived = useSessionStore(s =>
    s.sessions?.find(sess => sess.id === s.activeSessionId)?.archived ?? false
  )
  // Input mode is selected here (not just inside ChatInput) so the input
  // shell's error boundary can reset when the user flips chat↔terminal: a
  // caught error otherwise sticks for the lifetime of the mount and the input
  // stays replaced by its fallback even after the cause is gone.
  const inputMode = useInputModeStore(s => s.mode)
  const messages = useSessionMessages(activeSessionId)
  // Durable work-unit overlay (stepId -> block status) from the last session
  // load; consumed by groupMessages to align paused/interrupted delegate &
  // plan-step blocks. A stable store reference (no per-render allocation).
  const workUnits = useSessionWorkUnits(activeSessionId)
  const streamingText = useChatStore(s => activeSessionId ? s.streamingText[activeSessionId] : undefined)
  const scrollRef = useRef<HTMLDivElement>(null)
  // Baseline for transcript stabilization (see the displayItems memo below):
  // the previous committed item tree, reused for identity-stable items.
  const prevItemsRef = useRef<DisplayItem[]>([])

  // Load persisted history on session change, then reconcile the chat store
  // against the backend runtime status and pending-action set AFTER the merge.
  //
  // Reconciliation runs after mergeHistoryMessages (not in a separate effect)
  // so it always sees a populated store. Previously the status/pending RPCs
  // resolved faster than the history RPC and reconciled an empty store, then
  // history loaded with unresolved prompts and nothing re-triggered
  // reconciliation — so stale plan_review / task_failed_resumable banners
  // reappeared in completed sessions.
  //
  // A `cancelled` flag guards the whole async chain: if the user switches
  // sessions before it settles, the result is discarded (the new session's
  // own chain takes over). This is equivalent to `useLatestAsync.wrap` but
  // spans a multi-step await sequence.
  useEffect(() => {
    // A session switch invalidates the stabilization baseline: the new
    // session's items must not be compared against the old one's (their keys
    // may even collide). Dropping the baseline also releases the previous
    // session's item tree.
    prevItemsRef.current = []
    if (!activeSessionId) {
      usePlanStore.getState().clearPlan()
      return
    }
    usePlanStore.getState().clearPlan()
    const loadStartedAt = Date.now()
    let cancelled = false

    ;(async () => {
      let history: Awaited<ReturnType<typeof getSessionHistory>>
      try {
        // Load the whole session history in one request: the returned array is
        // the full row set for the session.
        history = await getSessionHistory(activeSessionId)
      } catch (err) {
        if (cancelled) return
        logger.error('Failed to load session history:', err)
        useChatStore.getState().addMessage(activeSessionId, {
          id: generateMessageId(),
          sessionId: activeSessionId,
          type: 'error',
          content: 'Failed to load session history. Please try switching sessions.',
          metadata: {},
          timestamp: Date.now(),
        })
        return
      }
      if (cancelled) return

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
          usePlanStore.getState().setSessionStats(activeSessionId, { lastAgentMetrics: agentMetrics })
        }
        // Drop store-state rows (agent_metrics) and the legacy per-task
        // "Routing request..." activity boilerplate so the chat shows only the
        // routing decision itself.
        const chatMessages = uiMessages.filter((m) => !isAgentMetricsRow(m) && !isRoutingRequestRow(m))
        // Merge (not replace) so live events delivered while the RPC was in
        // flight — e.g. a terminal `error` — are not clobbered.
        useChatStore.getState().mergeHistoryMessages(activeSessionId, chatMessages, loadStartedAt)
        // Rebuild the plan panel and goal badge from the loaded history: the
        // whole row set is present, so the plan declaration and the goal
        // snapshots are always available.
        restorePlanAndGoalFromHistory(activeSessionId, chatMessages)
      }

      // Reconcile AFTER the merge so the store is populated. Fetch the
      // authoritative runtime status and pending-action set in parallel.
      // statusReadAt predates the snapshot: live events that mutate the
      // activity label / streaming text after this point are fresher than the
      // snapshot's phase, and reconcileRuntimeStatus skips overwriting them.
      const statusReadAt = Date.now()
      const [status, pending] = await Promise.all([
        getSessionRuntimeStatus(activeSessionId),
        getPendingActions(activeSessionId),
      ])
      if (cancelled) return

      // Collect messages resolved as stale so their resolution can be
      // persisted (otherwise they reappear on the next reload).
      const staleResolved: ChatMessageUI[] = []
      if (status) {
        for (const msg of reconcileRuntimeStatus(activeSessionId, status, statusReadAt)) staleResolved.push(msg)
        // Align paused/interrupted delegate & plan-step blocks with the durable
        // work-unit snapshot (rendered via groupMessages). Runs AFTER the
        // history merge so the blocks exist to be corrected. statusReadAt
        // guards against a live settlement that landed after this snapshot was
        // read, and the same overlay repairs the execution plan panel — rebuilt
        // from the replayed history, it would otherwise keep spinning a step the
        // ledger settled as interrupted.
        const workUnitOverlay = reconcileWorkUnits(activeSessionId, status.work_units, statusReadAt)
        usePlanStore.getState().applyWorkUnitStatuses(workUnitOverlay)
      }
      if (pending) {
        for (const msg of reconcilePendingActions(activeSessionId, pending)) staleResolved.push(msg)
      }

      // Persist stale resolutions to the backend (best-effort, idempotent).
      // Once persisted, the message reloads with resolved:true and is never
      // reprocessed.
      for (const msg of staleResolved) {
        const field = stalePromptMatchField(msg.type)
        if (!field) continue
        const value = msg.metadata?.[field]
        if (typeof value === 'string') {
          void resolveStalePrompt(activeSessionId, msg.type, field, value, { resolved: true, stale: true })
        }
      }
    })()

    return () => { cancelled = true }
  }, [activeSessionId])

  // Restore the taskActive flag fast and independently of history loading.
  // Extracted into useTaskFlagRestore (single tested implementation, with the
  // stale-snapshot guard that keeps a live resume/terminal transition from
  // being reverted by an older status snapshot).
  useTaskFlagRestore(activeSessionId)

  // Load bookmarks for the active session (bookmarks are isolated per session).
  useEffect(() => {
    if (activeSessionId) void useBookmarkStore.getState().loadBookmarks(activeSessionId)
  }, [activeSessionId])

  // groupMessages rebuilds the WHOLE item tree on every store change (a new
  // message, a resolved confirmation, a streamed tail), handing every block a
  // brand-new `item` object. Stabilize the tree against the previous output so
  // items that did not change keep their object identity — that identity is
  // what lets the memoized message blocks skip re-rendering (and re-parsing
  // their Markdown) when a single message changes. The ref is updated after the
  // commit (not during render, which React StrictMode would double-invoke).
  const displayItems = useMemo(() => {
    const grouped = groupMessages(messages, workUnits).items
    return stabilizeDisplayItems(prevItemsRef.current, grouped)
  }, [messages, workUnits])
  useEffect(() => {
    prevItemsRef.current = displayItems
  }, [displayItems])

  if (!activeSessionId) {
    return (
      <div className="flex flex-1 flex-col">
        <div className="flex-1 flex items-center justify-center text-muted-foreground">
          <div className="flex flex-col items-center gap-3">
            <MessageCircle className="h-12 w-12 opacity-20" />
          </div>
        </div>
        <ChatInput />
      </div>
    )
  }

  const hasContent = messages.length > 0 || !!streamingText

  // Archived sessions are read-only: swap the input shell for an "Archived"
  // banner. Hoisted into a single const so the archived gate lives in one
  // place (used by both render branches below). activeSessionId is guaranteed
  // non-null past the early return above, so no extra null check is needed.
  const inputShell = isArchived ? <ArchivedBanner sessionId={activeSessionId} /> : <ChatInput />

  if (!hasContent) {
    return (
      <div className="flex flex-1 flex-col">
        <div className="flex-1 flex items-center justify-center text-muted-foreground">
          <div className="flex flex-col items-center gap-3">
            <MessageCircle className="h-12 w-12 opacity-20" />
          </div>
        </div>
        <ErrorBoundary fallback={<div className="text-xs text-destructive p-2">Panel error</div>}>
          <ExecutionPanels />
          <BlackboardPanel />
        </ErrorBoundary>
        <ErrorBoundary
          resetKeys={[activeSessionId, inputMode, isArchived]}
          fallback={<div className="text-xs text-destructive p-2">Input error</div>}
        >
          {inputShell}
        </ErrorBoundary>
      </div>
    )
  }

  return (
    <ScrollProvider>
      <div className="relative flex flex-1 flex-col min-h-0 bg-background">
        <ChatScrollManager key={activeSessionId} sessionId={activeSessionId} messages={messages} streamingText={streamingText} scrollRef={scrollRef}>
          <ChatHoverRegion className="p-4 space-y-4 min-w-0">
            <ChatMessageRenderer
              items={displayItems}
              stickyUserMessages
              trailingContent={(
                <>
                  {streamingText && (
                    <ErrorBoundary fallback={<CompactErrorFallback />}>
                      <AssistantMessage content={streamingText} isStreaming />
                    </ErrorBoundary>
                  )}
                  <ActivityIndicator />
                </>
              )}
            />
          </ChatHoverRegion>
        </ChatScrollManager>
        <ErrorBoundary fallback={<div className="text-xs text-destructive p-2">Panel error</div>}>
          <ExecutionPanels />
          <BlackboardPanel />
          <BookmarksPanel displayItems={displayItems} />
        </ErrorBoundary>
        <ErrorBoundary
          resetKeys={[activeSessionId, inputMode, isArchived]}
          fallback={<div className="text-xs text-destructive p-2">Input error</div>}
        >
          {inputShell}
        </ErrorBoundary>
      </div>
    </ScrollProvider>
  )
}
