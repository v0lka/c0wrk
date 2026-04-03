import { useEffect, useLayoutEffect, useRef, useMemo, useState } from 'react'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useChatStore, ChatMessageUI, groupMessages } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { usePanelStore } from '@/stores/panelStore'
import { UserMessage } from './UserMessage'
import { AssistantMessage } from './AssistantMessage'
import { ThoughtBlock } from './ThoughtBlock'
import { StepGroup } from './StepGroup'
import { ToolConfirmation } from './ToolConfirmation'
import { ErrorBlock } from './ErrorBlock'
import { ServiceMessage } from './ServiceMessage'
import { ActivityIndicator } from './ActivityIndicator'
import { useSessionEvents } from '@/hooks/useSessionEvents'
import { MessageCircle } from 'lucide-react'
import { GetSessionHistory } from '../../../wailsjs/go/main/App'
import { chatMessageToUI } from '@/lib/chatUtils'

// Stable empty array to prevent infinite re-render loops in Zustand selectors
const EMPTY_MESSAGES: ChatMessageUI[] = []

export function ChatArea() {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const messages = useChatStore(s =>
    activeSessionId ? (s.messages[activeSessionId] ?? EMPTY_MESSAGES) : EMPTY_MESSAGES
  )
  const streamingText = useChatStore(s => s.streamingText)
  const setMessages = useChatStore(s => s.setMessages)
  const scrollRef = useRef<HTMLDivElement>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const [containerHeight, setContainerHeight] = useState(0)

  // Track container height for pinned message max height calculation
  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    // Feature detection for ResizeObserver
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(entries => {
      for (const entry of entries) {
        setContainerHeight(entry.contentRect.height)
      }
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

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
      if (history && history.length > 0) {
        const uiMessages = history.map(chatMessageToUI)
        setMessages(activeSessionId, uiMessages)
        usePanelStore.getState().rebuildFromEvents(uiMessages)
      }
    }).catch((err) => {
      console.error('Failed to load session history:', err)
    })
  }, [activeSessionId, setMessages])

  // Auto-scroll to bottom on new messages or streaming text
  useLayoutEffect(() => {
    if (scrollRef.current) {
      const viewport = scrollRef.current.querySelector('[data-slot="scroll-area-viewport"]')
      if (viewport) {
        viewport.scrollTop = viewport.scrollHeight
      }
    }
  }, [messages, streamingText])

  const displayItems = useMemo(() => groupMessages(messages), [messages])

  // Find the last user message for pinning at the top
  const lastUserMessage = useMemo(() => {
    for (let i = displayItems.length - 1; i >= 0; i--) {
      if (displayItems[i].kind === 'user') {
        return displayItems[i] as Extract<typeof displayItems[number], { kind: 'user' }>
      }
    }
    return null
  }, [displayItems])

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
      <ScrollArea className="flex-1 min-w-0" ref={scrollRef}>
        <div className="p-4 space-y-4 min-w-0">
          {displayItems.map((item) => {
            // Skip the last user message since it's pinned at the top
            if (item.kind === 'user' && lastUserMessage && item.message.id === lastUserMessage.message.id) {
              return null
            }
            switch (item.kind) {
              case 'user':
                return <UserMessage key={item.message.id} content={item.message.content} timestamp={item.message.timestamp} />
              case 'assistant':
                return <AssistantMessage key={item.message.id} content={item.message.content} />
              case 'thought':
                return <ThoughtBlock key={item.id} content={item.content} />
              case 'step_group':
                return <StepGroup key={item.id} steps={item.steps} />
              case 'tool_confirm':
                return <ToolConfirmation key={item.message.id} metadata={item.message.metadata} />
              case 'error':
                return <ErrorBlock key={item.message.id} content={item.message.content} />
              case 'service':
                return <ServiceMessage key={item.id} id={item.id} variant={item.variant} content={item.content} metadata={item.metadata} />
              default:
                return null
            }
          })}

          {/* Streaming text indicator */}
          {streamingText && (
            <AssistantMessage
              content={streamingText}
              isStreaming
            />
          )}

          {/* Activity indicator */}
          <ActivityIndicator />
        </div>
      </ScrollArea>
    </div>
  )
}
