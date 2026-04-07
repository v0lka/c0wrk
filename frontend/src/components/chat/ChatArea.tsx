import React, { useEffect, useLayoutEffect, useRef, useMemo, useState } from 'react'
import { useChatStore, ChatMessageUI, groupMessages } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { usePanelStore } from '@/stores/panelStore'
import { UserMessage } from './UserMessage'
import { ChatScrollManager } from './ChatScrollManager'
import { ChatMessageRenderer } from './ChatMessageRenderer'
import { useSessionEvents } from '@/hooks/useSessionEvents'
import { MessageCircle } from 'lucide-react'
import { GetSessionHistory } from '../../../wailsjs/go/desktop/App'
import { chatMessageToUI } from '@/lib/chatUtils'
import { logger } from '@/lib/logger'

// Stable empty array to prevent infinite re-render loops in Zustand selectors
const EMPTY_MESSAGES: ChatMessageUI[] = []

export function ChatArea(): React.ReactNode {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const messages = useChatStore(s =>
    activeSessionId ? (s.messages[activeSessionId] ?? EMPTY_MESSAGES) : EMPTY_MESSAGES
  )
  const streamingText = useChatStore(s => s.streamingText)
  const setMessages = useChatStore(s => s.setMessages)
  const setPendingActions = useChatStore(s => s.setPendingActions)
  const scrollRef = useRef<HTMLDivElement>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const [containerHeight, setContainerHeight] = useState(() =>
    typeof window !== 'undefined' ? window.innerHeight : 600
  )
  const [historyError, setHistoryError] = useState<string | null>(null)

  // Derived flag: true when the container div with containerRef is in the DOM
  const showContainer = !!activeSessionId && (messages.length > 0 || !!streamingText)

  // Track container height for pinned message max height calculation
  useLayoutEffect(() => {
    const el = containerRef.current
    if (!el) return

    // Immediate measurement attempt
    const h = el.getBoundingClientRect().height
    if (h > 0) {
      setContainerHeight(h)
    }

    // Fallback: measure after next frame in case layout isn't ready yet
    const rafId = requestAnimationFrame(() => {
      if (el) {
        const height = el.getBoundingClientRect().height
        if (height > 0) setContainerHeight(height)
      }
    })

    if (typeof ResizeObserver === 'undefined') {
      return () => cancelAnimationFrame(rafId)
    }

    const ro = new ResizeObserver(entries => {
      for (const entry of entries) {
        if (entry.contentRect.height > 0) {
          setContainerHeight(entry.contentRect.height)
        }
      }
    })
    ro.observe(el)
    return () => {
      cancelAnimationFrame(rafId)
      ro.disconnect()
    }
  }, [showContainer])

  const maxPinnedHeight = containerHeight / 5

  // Subscribe to session events
  useSessionEvents(activeSessionId)

  // Load persisted history when active session changes
  useEffect(() => {
    if (!activeSessionId) return

    // Always fetch full history from backend for panel reconstruction.
    // Backend persists plan/eval events that aren't kept in the frontend
    // message cache, so we must use the complete history to rebuild panels.
    GetSessionHistory(activeSessionId).then((history) => {
      setHistoryError(null)
      if (history && history.length > 0) {
        const uiMessages = history.map(chatMessageToUI)
        setMessages(activeSessionId, uiMessages)
        usePanelStore.getState().rebuildFromEvents(uiMessages)
      }
    }).catch((err) => {
      logger.error('Failed to load session history:', err)
      setHistoryError('Failed to load session history')
    })
  }, [activeSessionId, setMessages])

  const { items: displayItems, pendingActions } = useMemo(() => groupMessages(messages), [messages])

  // Sync pendingActions to the store so PendingActionsBar can read them
  useEffect(() => {
    setPendingActions(pendingActions)
  }, [pendingActions, setPendingActions])

  // Find the last user message for pinning at the top
  let lastUserMessage: Extract<typeof displayItems[number], { kind: 'user' }> | null = null
  for (let i = displayItems.length - 1; i >= 0; i--) {
    if (displayItems[i].kind === 'user') {
      lastUserMessage = displayItems[i] as Extract<typeof displayItems[number], { kind: 'user' }>
      break
    }
  }

  const lastUserMessageId = lastUserMessage ? lastUserMessage.message.id : null

  if (!activeSessionId) {
    return (
      <div className="flex-1 flex items-center justify-center text-muted-foreground">
        <div className="flex flex-col items-center gap-3">
          <MessageCircle className="h-12 w-12 opacity-20" />
          <p>Send a message to start a new conversation</p>
        </div>
      </div>
    )
  }

  const hasContent = messages.length > 0 || !!streamingText

  if (!hasContent) {
    return (
      <div className="flex-1 flex items-center justify-center text-muted-foreground">
        <div className="flex flex-col items-center gap-3">
          <MessageCircle className="h-12 w-12 opacity-20" />
          <p>Send a message to start the conversation</p>
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col flex-1 min-h-0" ref={containerRef}>
      {/* History load error */}
      {historyError && (
        <div className="px-4 py-2 text-sm text-destructive bg-destructive/10 border-b border-destructive/20">
          {historyError}
        </div>
      )}
      {/* Pinned last user message */}
      {lastUserMessage && (
        <div className="sticky top-0 z-10 bg-background/95 backdrop-blur-sm border-b border-border/50 px-4 py-3">
          <UserMessage
            content={lastUserMessage.message.content}
            timestamp={lastUserMessage.message.timestamp}
            isPinned
            maxHeight={maxPinnedHeight}
          />
        </div>
      )}
      <ChatScrollManager
        messages={messages}
        streamingText={streamingText}
        scrollRef={scrollRef}
      >
        <ChatMessageRenderer
          displayItems={displayItems}
          lastUserMessageId={lastUserMessageId}
          streamingText={streamingText}
        />
      </ChatScrollManager>
    </div>
  )
}
