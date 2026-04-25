import { useEffect, useLayoutEffect, useRef, useMemo, useState } from 'react'
import { useChatStore, useSessionMessages } from '@/stores/chatStore'
import { groupMessages, chatMessageToUI, rebuildPlanFromHistory } from '@/lib/chatUtils'
import { useSessionStore } from '@/stores/sessionStore'
import { usePlanStore } from '@/stores/planStore'
import { getSessionHistory } from '@/api/chat'
import { useLatestAsync } from '@/hooks/useLatestAsync'
import { generateMessageId } from '@/lib/ids'
import { UserMessage } from './UserMessage'
import { AssistantMessage } from './AssistantMessage'
import { ActivityIndicator } from './ActivityIndicator'
import { ChatScrollManager } from './ChatScrollManager'
import { ChatMessageRenderer } from './ChatMessageRenderer'
import { ExecutionPanels } from './ExecutionPanels'
import { BlackboardPanel } from './BlackboardPanel'
import { PendingActionsBar } from './PendingActionsBar'
import { ChatInput } from './ChatInput'
import { ScrollProvider } from './ScrollContext'
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
    wrap(getSessionHistory(activeSessionId)).then((history) => {
      if (!history) return // stale — superseded by a newer session switch
      if (history.length > 0) {
        const uiMessages = history.map((msg) => chatMessageToUI(msg))
        useChatStore.getState().setMessages(activeSessionId, uiMessages)
        rebuildPlanFromHistory(uiMessages)
      }
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

  // Find last user message for pinning
  let lastUserItem: Extract<typeof displayItems[number], { kind: 'user' }> | null = null
  for (let i = displayItems.length - 1; i >= 0; i--) {
    if (displayItems[i]!.kind === 'user') {
      lastUserItem = displayItems[i] as Extract<typeof displayItems[number], { kind: 'user' }>
      break
    }
  }

  // Filter out pinned message from main list
  const filteredItems = lastUserItem
    ? displayItems.filter(item => !(item.kind === 'user' && 'message' in item && item.message.id === lastUserItem!.message.id))
    : displayItems

  if (!activeSessionId) {
    return (
      <div className="flex flex-1 flex-col">
        <div className="flex-1 flex items-center justify-center text-muted-foreground">
          <div className="flex flex-col items-center gap-3">
            <MessageCircle className="h-12 w-12 opacity-20" />
            <p>Send a message to start a new conversation</p>
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
            <p>Send a message to start the conversation</p>
          </div>
        </div>
        <ExecutionPanels />
        <BlackboardPanel />
        <PendingActionsBar />
        <ChatInput />
      </div>
    )
  }

  return (
    <ScrollProvider>
      <div className="flex flex-1 flex-col min-h-0 bg-background" ref={containerRef}>
        {/* Pinned last user message */}
        {lastUserItem && (
          <div className="sticky top-0 z-10 bg-background/95 backdrop-blur-sm border-b border-border/50 px-4 py-3">
            <UserMessage item={lastUserItem} isPinned maxHeight={maxPinnedHeight} />
          </div>
        )}
        <ChatScrollManager key={activeSessionId} messages={messages} streamingText={streamingText} scrollRef={scrollRef}>
          <div className="p-4 space-y-4 min-w-0">
            <ChatMessageRenderer items={filteredItems} />
            {streamingText && <AssistantMessage content={streamingText} isStreaming />}
            <ActivityIndicator />
          </div>
        </ChatScrollManager>
        <ExecutionPanels />
        <BlackboardPanel />
        <PendingActionsBar />
        <ChatInput />
      </div>
    </ScrollProvider>
  )
}
