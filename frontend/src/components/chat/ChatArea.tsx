import { useEffect, useLayoutEffect, useRef, useMemo, useState } from 'react'
import { useChatStore, useSessionMessages } from '@/stores/chatStore'
import { groupMessages, chatMessageToUI, rebuildPlanFromHistory } from '@/lib/chatUtils'
import { useSessionStore } from '@/stores/sessionStore'
import { usePlanStore } from '@/stores/planStore'
import { getSessionHistory, getSessionRuntimeStatus } from '@/api/chat'
import { reconcileRuntimeStatus } from '@/lib/sessionRuntime'
import { useLatestAsync } from '@/hooks/useLatestAsync'
import { generateMessageId } from '@/lib/ids'
import { UserMessage } from './UserMessage'
import { AssistantMessage } from './AssistantMessage'
import { ActivityIndicator } from './ActivityIndicator'
import { ChatScrollManager } from './ChatScrollManager'
import { ChatMessageRenderer, CompactErrorFallback } from './ChatMessageRenderer'
import { ExecutionPanels } from './ExecutionPanels'
import { BlackboardPanel } from './BlackboardPanel'
import { PendingActionsBar } from './PendingActionsBar'
import { ChatInput } from './ChatInput'
import { ScrollProvider } from './ScrollContext'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { MessageCircle } from 'lucide-react'
import { logger } from '@/lib/logger'

export function ChatArea() {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const messages = useSessionMessages(activeSessionId)
  const streamingText = useChatStore(s => s.streamingText)
  const scrollRef = useRef<HTMLDivElement>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const [containerHeight, setContainerHeight] = useState(600)
  const { wrap } = useLatestAsync()

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

  // Load persisted history on session change
  useEffect(() => {
    if (!activeSessionId) {
      usePlanStore.getState().clearPlan()
      return
    }
    usePlanStore.getState().clearPlan()
    const loadStartedAt = Date.now()
    wrap(getSessionHistory(activeSessionId)).then(async (history) => {
      if (!history) return // stale — superseded by a newer session switch
      if (history.length > 0) {
        const uiMessages = history.map((msg) => chatMessageToUI(msg))
        // Merge (not replace) so live events delivered while the RPC was in
        // flight — e.g. a terminal `error` — are not clobbered.
        useChatStore.getState().mergeHistoryMessages(activeSessionId, uiMessages, loadStartedAt)
        rebuildPlanFromHistory(uiMessages, usePlanStore.getState())
      }
      // Reconcile visual state with the backend runtime status: restores the
      // running flag after reload and surfaces unfinished (resumable) tasks
      // instead of defaulting to "idle/completed".
      const status = await wrap(getSessionRuntimeStatus(activeSessionId))
      if (status) reconcileRuntimeStatus(activeSessionId, status)
    }).catch((err) => {
      logger.error('Failed to load session history:', err)
      useChatStore.getState().addMessage(activeSessionId, {
        id: generateMessageId(),
        sessionId: activeSessionId,
        type: 'error',
        content: 'Failed to load session history. Please try switching sessions.',
        metadata: {},
        timestamp: Date.now(),
      })
    })
  }, [activeSessionId, wrap])

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
          <PendingActionsBar />
        </ErrorBoundary>
        <ErrorBoundary fallback={<div className="text-xs text-destructive p-2">Input error</div>}>
          <ChatInput />
        </ErrorBoundary>
      </div>
    )
  }

  return (
    <ScrollProvider>
      <div className="flex flex-1 flex-col min-h-0 bg-background" ref={containerRef}>
        {/* Pinned last user message — only when scrolled out of viewport */}
        {lastUserItem && !isLastUserVisible && (
          <div className="sticky top-0 z-10 bg-background/95 backdrop-blur-sm border-b border-border/50 px-4 py-3">
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
          <PendingActionsBar />
        </ErrorBoundary>
        <ErrorBoundary fallback={<div className="text-xs text-destructive p-2">Input error</div>}>
          <ChatInput />
        </ErrorBoundary>
      </div>
    </ScrollProvider>
  )
}
