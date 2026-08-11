import { useEffect, useLayoutEffect, useRef, useMemo, useState } from 'react'
import { useChatStore, useSessionMessages } from '@/stores/chatStore'
import { groupMessages, chatMessageToUI, rebuildPlanFromHistory } from '@/lib/chatUtils'
import { useSessionStore } from '@/stores/sessionStore'
import { usePlanStore } from '@/stores/planStore'
import { getSessionHistory, getSessionRuntimeStatus, getPendingActions, resolveStalePrompt } from '@/api/chat'
import { reconcileRuntimeStatus, reconcilePendingActions, stalePromptMatchField } from '@/lib/sessionRuntime'
import { generateMessageId } from '@/lib/ids'
import type { ChatMessageUI } from '@/types/messages'
import { UserMessage } from './UserMessage'
import { AssistantMessage } from './AssistantMessage'
import { ActivityIndicator } from './ActivityIndicator'
import { ChatScrollManager } from './ChatScrollManager'
import { ChatMessageRenderer, CompactErrorFallback } from './ChatMessageRenderer'
import { ExecutionPanels } from './ExecutionPanels'
import { BlackboardPanel } from './BlackboardPanel'
import { ChatInput } from './ChatInput'
import { ScrollProvider } from './ScrollContext'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { MessageCircle } from 'lucide-react'
import { logger } from '@/lib/logger'

export function ChatArea() {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const messages = useSessionMessages(activeSessionId)
  const streamingText = useChatStore(s => activeSessionId ? s.streamingText[activeSessionId] : undefined)
  const scrollRef = useRef<HTMLDivElement>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const [containerHeight, setContainerHeight] = useState(600)

  // Track container height via ResizeObserver (no rAF fallback)
  useLayoutEffect(() => {
    const el = containerRef.current
    if (!el) return
    const h = el.getBoundingClientRect().height
    if (h > 0) setContainerHeight(h)

    const ro = new ResizeObserver(entries => {
      for (const entry of entries) {
        if (entry.contentRect.height > 0) setContainerHeight(entry.contentRect.height)
      }
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [activeSessionId])

  const maxPinnedHeight = containerHeight / 7

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
        const uiMessages = history.map((msg) => chatMessageToUI(msg))
        // Merge (not replace) so live events delivered while the RPC was in
        // flight — e.g. a terminal `error` — are not clobbered.
        useChatStore.getState().mergeHistoryMessages(activeSessionId, uiMessages, loadStartedAt)
        rebuildPlanFromHistory(uiMessages, usePlanStore.getState())
      }

      // Reconcile AFTER the merge so the store is populated. Fetch the
      // authoritative runtime status and pending-action set in parallel.
      const [status, pending] = await Promise.all([
        getSessionRuntimeStatus(activeSessionId),
        getPendingActions(activeSessionId),
      ])
      if (cancelled) return

      // Collect messages resolved as stale so their resolution can be
      // persisted (otherwise they reappear on the next reload).
      const staleResolved: ChatMessageUI[] = []
      if (status) {
        for (const msg of reconcileRuntimeStatus(activeSessionId, status)) staleResolved.push(msg)
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

  // Find last user message for conditional pinning (message stays in chat history)
  const lastUserItem = useMemo(() => {
    for (let i = displayItems.length - 1; i >= 0; i--) {
      if (displayItems[i]!.kind === 'user') {
        return displayItems[i] as Extract<typeof displayItems[number], { kind: 'user' }>
      }
    }
    return null
  }, [displayItems])

  // IntersectionObserver ref for stable cleanup across effect re-runs
  const observerRef = useRef<IntersectionObserver | null>(null)

  // Track whether the last user message is visible in the scroll viewport
  const [isLastUserVisible, setIsLastUserVisible] = useState(false)
  const lastUserMessageId = lastUserItem?.message.id

  useEffect(() => {
    if (!lastUserMessageId || !scrollRef.current) {
      setIsLastUserVisible(true)
      return
    }

    const viewport = scrollRef.current
    const raf = requestAnimationFrame(() => {
      const element = viewport.querySelector(`[data-message-id="${lastUserMessageId}"]`)
      if (!element) {
        setIsLastUserVisible(true)
        return
      }

      const observer = new IntersectionObserver(
        ([entry]) => {
          setIsLastUserVisible(entry?.isIntersecting ?? true)
        },
        { root: viewport, threshold: 0 },
      )
      observer.observe(element)
      observerRef.current = observer
    })

    return () => {
      cancelAnimationFrame(raf)
      observerRef.current?.disconnect()
    }
  }, [lastUserMessageId])

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
          <ChatInput />
        </ErrorBoundary>
      </div>
    )
  }

  return (
    <ScrollProvider>
      <div className="relative flex flex-1 flex-col min-h-0 bg-background" ref={containerRef}>
        {/* Pinned last user message — overlay the viewport so showing it cannot
            change the IntersectionObserver root height and toggle itself. */}
        {lastUserItem && !isLastUserVisible && (
          <div className="absolute inset-x-0 top-0 z-10 bg-background/95 backdrop-blur-sm border-b border-border/50 px-4 py-3">
            <UserMessage item={lastUserItem} isPinned maxHeight={maxPinnedHeight} />
          </div>
        )}
        <ChatScrollManager key={activeSessionId} messages={messages} streamingText={streamingText} scrollRef={scrollRef}>
          <div className="p-4 space-y-4 min-w-0">
            <ChatMessageRenderer items={displayItems} />
            {streamingText && (
              <ErrorBoundary fallback={<CompactErrorFallback />}>
                <AssistantMessage content={streamingText} isStreaming />
              </ErrorBoundary>
            )}
            <ActivityIndicator />
          </div>
        </ChatScrollManager>
        <ErrorBoundary fallback={<div className="text-xs text-destructive p-2">Panel error</div>}>
          <ExecutionPanels />
          <BlackboardPanel />
        </ErrorBoundary>
        <ErrorBoundary fallback={<div className="text-xs text-destructive p-2">Input error</div>}>
          <ChatInput />
        </ErrorBoundary>
      </div>
    </ScrollProvider>
  )
}
