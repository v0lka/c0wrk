import { useEffect, useRef, useMemo } from 'react'
import { useChatStore, useSessionMessages } from '@/stores/chatStore'
import { groupMessages, chatMessageToUI, rebuildPlanFromHistory, rebuildGoalFromHistory, isPersistableHistoryMessage, lastAgentMetricsFromHistory, isAgentMetricsRow } from '@/lib/chatUtils'
import { useSessionStore } from '@/stores/sessionStore'
import { usePlanStore } from '@/stores/planStore'
import { useGoalStore } from '@/stores/goalStore'
import { getSessionHistory, getSessionRuntimeStatus, getPendingActions, resolveStalePrompt } from '@/api/chat'
import { reconcileRuntimeStatus, reconcilePendingActions, stalePromptMatchField } from '@/lib/sessionRuntime'
import { generateMessageId } from '@/lib/ids'
import type { ChatMessageUI } from '@/types/messages'
import { AssistantMessage } from './AssistantMessage'
import { ActivityIndicator } from './ActivityIndicator'
import { ChatScrollManager } from './ChatScrollManager'
import { ChatMessageRenderer, CompactErrorFallback } from './ChatMessageRenderer'
import { ExecutionPanels } from './ExecutionPanels'
import { BlackboardPanel } from './BlackboardPanel'
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
  const messages = useSessionMessages(activeSessionId)
  const streamingText = useChatStore(s => activeSessionId ? s.streamingText[activeSessionId] : undefined)
  const scrollRef = useRef<HTMLDivElement>(null)

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
        // Filter out "event_unknown" rows — transient UI events
        // (attachments:changed, session_pinned, etc.) that leaked into the DB
        // before they were marked transient in the persister. Their content is
        // the raw JSON metadata payload, which would render as garbage text.
        const filteredHistory = history.filter(isPersistableHistoryMessage)
        const droppedCount = history.length - filteredHistory.length
        if (droppedCount > 0) {
          logger.debug(`Filtered ${droppedCount} transient event_unknown row(s) from session history`)
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
        const chatMessages = uiMessages.filter((m) => !isAgentMetricsRow(m))
        // Merge (not replace) so live events delivered while the RPC was in
        // flight — e.g. a terminal `error` — are not clobbered.
        useChatStore.getState().mergeHistoryMessages(activeSessionId, chatMessages, loadStartedAt)
        rebuildPlanFromHistory(chatMessages, usePlanStore.getState())
        // Rebuild the goal store from persisted goal_status snapshots so the
        // status-bar badge and the settled goal card's verdict survive a reload
        // (live goal_status events are not replayed on session load).
        rebuildGoalFromHistory(chatMessages, useGoalStore.getState(), useGoalStore.getState().activeGoal[activeSessionId])
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
  // This handles background sessions that complete while not viewed: the
  // "stop" button must reflect the real state even before the (slower)
  // history RPC resolves. Only sets taskActive here — full message
  // reconciliation happens in the history effect above (where the store is
  // populated), to avoid injecting a synthetic resume banner into an empty
  // store.
  useEffect(() => {
    if (!activeSessionId) return
    let cancelled = false
    getSessionRuntimeStatus(activeSessionId).then((status) => {
      if (cancelled || !status) return
      useChatStore.getState().setTaskActive(activeSessionId, status.active)
    }).catch((err) => {
      logger.error('Failed to get session runtime status:', err)
    })
    return () => { cancelled = true }
  }, [activeSessionId])

  const { items: displayItems } = useMemo(() => groupMessages(messages), [messages])

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
        <ErrorBoundary fallback={<div className="text-xs text-destructive p-2">Input error</div>}>
          {inputShell}
        </ErrorBoundary>
      </div>
    )
  }

  return (
    <ScrollProvider>
      <div className="relative flex flex-1 flex-col min-h-0 bg-background">
        <ChatScrollManager key={activeSessionId} messages={messages} streamingText={streamingText} scrollRef={scrollRef}>
          <div className="p-4 space-y-4 min-w-0">
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
          </div>
        </ChatScrollManager>
        <ErrorBoundary fallback={<div className="text-xs text-destructive p-2">Panel error</div>}>
          <ExecutionPanels />
          <BlackboardPanel />
        </ErrorBoundary>
        <ErrorBoundary fallback={<div className="text-xs text-destructive p-2">Input error</div>}>
          {inputShell}
        </ErrorBoundary>
      </div>
    </ScrollProvider>
  )
}
