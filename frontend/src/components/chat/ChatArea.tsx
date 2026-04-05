import React, { useEffect, useLayoutEffect, useRef, useMemo, useState } from 'react'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useChatStore, ChatMessageUI, DisplayItem, groupMessages } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { usePanelStore } from '@/stores/panelStore'
import { useScrollStore } from '@/stores/scrollStore'
import { UserMessage } from './UserMessage'
import { AssistantMessage } from './AssistantMessage'
import { ThoughtBlock } from './ThoughtBlock'
import { ToolBlock } from './ToolBlock'
import { PlanStepBlock } from './PlanStepBlock'
import { ToolConfirmation } from './ToolConfirmation'
import { AskUserPanel } from './AskUserPanel'
import { ErrorBlock } from './ErrorBlock'
import { ServiceMessage } from './ServiceMessage'
import { ActivityIndicator } from './ActivityIndicator'
import { ActionPlaceholder } from './ActionPlaceholder'
import { ThoughtGroupBlock } from './ThoughtGroupBlock'
import { useSessionEvents } from '@/hooks/useSessionEvents'
import { MessageCircle } from 'lucide-react'
import { GetSessionHistory } from '../../../wailsjs/go/main/App'
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
  const setScrollToStep = useScrollStore(s => s.setScrollToStep)
  const scrollRef = useRef<HTMLDivElement>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const [containerHeight, setContainerHeight] = useState(() =>
    typeof window !== 'undefined' ? window.innerHeight : 600
  )
  const isAtBottomRef = useRef(true)
  const [hasNewActivity, setHasNewActivity] = useState(false)
  const [historyError, setHistoryError] = useState<string | null>(null)

  // Derived flag: true when the container div with containerRef is in the DOM
  const showContainer = !!activeSessionId && (messages.length > 0 || !!streamingText)
  const viewportRef = useRef<HTMLElement | null>(null)
  const prevScrollState = useRef<{ scrollTop: number; scrollHeight: number; clientHeight: number }>({
    scrollTop: 0,
    scrollHeight: 0,
    clientHeight: 0,
  })

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

  // Cache viewport element
  useEffect(() => {
    if (!scrollRef.current) return
    const vp = scrollRef.current.querySelector('[data-slot="scroll-area-viewport"]') as HTMLElement | null
    viewportRef.current = vp
    if (vp) {
      prevScrollState.current = {
        scrollTop: vp.scrollTop,
        scrollHeight: vp.scrollHeight,
        clientHeight: vp.clientHeight,
      }
    }
  })

  // Track scroll position for "new activity" pill dismissal
  useEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return

    const handleScroll = () => {
      const atBottom = viewport.scrollTop + viewport.clientHeight >= viewport.scrollHeight - 50
      isAtBottomRef.current = atBottom
      prevScrollState.current = {
        scrollTop: viewport.scrollTop,
        scrollHeight: viewport.scrollHeight,
        clientHeight: viewport.clientHeight,
      }
      if (atBottom) setHasNewActivity(false)
    }

    viewport.addEventListener('scroll', handleScroll, { passive: true })
    return () => viewport.removeEventListener('scroll', handleScroll)
  })

  // Auto-scroll only when user was at bottom before new content arrived
  useLayoutEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return

    // Read current measurements synchronously (before paint)
    const currentScrollHeight = viewport.scrollHeight
    const currentClientHeight = viewport.clientHeight

    // Determine if user was at bottom using PREVIOUS state (before new content was added)
    const prev = prevScrollState.current
    const wasAtBottom = prev.scrollTop + prev.clientHeight >= prev.scrollHeight - 50

    if (wasAtBottom) {
      // User was at bottom → scroll to new bottom (direct assignment in useLayoutEffect, before paint)
      viewport.scrollTop = currentScrollHeight
      isAtBottomRef.current = true
    } else {
      // User had scrolled up → don't move, show "new activity" indicator
      setHasNewActivity(true)
    }

    // Update prev state with current measurements
    prevScrollState.current = {
      scrollTop: viewport.scrollTop,
      scrollHeight: currentScrollHeight,
      clientHeight: currentClientHeight,
    }
  }, [messages, streamingText])

  const { items: displayItems, pendingActions } = useMemo(() => groupMessages(messages), [messages])

  // Sync pendingActions to the store so PendingActionsBar can read them
  useEffect(() => {
    setPendingActions(pendingActions)
  }, [pendingActions, setPendingActions])

  // Register scroll-to-step callback
  useEffect(() => {
    const scrollToStepFn = (stepId: string) => {
      const viewport = viewportRef.current
      if (!viewport) return
      const elements = viewport.querySelectorAll(`[data-step-id="${stepId}"]`)
      const target = elements[elements.length - 1] // last match (for retries)
      if (target) {
        target.scrollIntoView({ behavior: 'smooth', block: 'start' })
        isAtBottomRef.current = false
      }
    }
    setScrollToStep(scrollToStepFn)
    return () => setScrollToStep(null)
  }, [setScrollToStep])

  // Find the last user message for pinning at the top
  const lastUserMessage = useMemo(() => {
    for (let i = displayItems.length - 1; i >= 0; i--) {
      if (displayItems[i].kind === 'user') {
        return displayItems[i] as Extract<typeof displayItems[number], { kind: 'user' }>
      }
    }
    return null
  }, [displayItems])

  // Recursive render function for DisplayItems (supports PlanStepBlock children)
  const renderDisplayItem = (item: DisplayItem): React.ReactNode => {
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
        return <ThoughtBlock key={item.id} content={item.content} reasoning={item.reasoning} />
      case 'tool':
        return <ToolBlock key={item.id} toolName={item.toolName} args={item.args} parsedArgs={item.parsedArgs} result={item.result} resultLen={item.resultLen} status={item.status} />
      case 'plan_step':
        return <PlanStepBlock key={item.id} stepId={item.stepId} stepNum={item.stepNum} title={item.title} status={item.status} duration={item.duration} isRetry={item.isRetry} children={item.children} renderItem={renderDisplayItem} />
      case 'tool_confirm':
        return <ToolConfirmation key={item.message.id} sessionId={item.message.sessionId} metadata={item.message.metadata} />
      case 'ask_user':
        return <AskUserPanel key={item.message.id} sessionId={item.message.sessionId} metadata={item.message.metadata} />
      case 'error':
        return <ErrorBlock key={item.message.id} content={item.message.content} />
      case 'service':
        return <ServiceMessage key={item.id} id={item.id} variant={item.variant} content={item.content} metadata={item.metadata} />
      case 'action_placeholder':
        return <ActionPlaceholder key={item.id} label={item.label} />
      case 'thought_group':
        return <ThoughtGroupBlock key={item.id} thoughts={item.thoughts} />
      default:
        return null
    }
  }

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
      <ScrollArea className="flex-1 min-w-0" ref={scrollRef}>
        <div className="p-4 space-y-4 min-w-0">
          {displayItems.map((item) => renderDisplayItem(item))}

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
        {hasNewActivity && !isAtBottomRef.current && (
          <button
            onClick={() => {
              const viewport = viewportRef.current
              if (viewport) {
                viewport.scrollTop = viewport.scrollHeight
                isAtBottomRef.current = true
                setHasNewActivity(false)
              }
            }}
            className="sticky bottom-2 left-1/2 -translate-x-1/2 z-10 px-3 py-1.5 rounded-full bg-blue-500 text-white text-xs shadow-lg hover:bg-blue-600 active:bg-blue-700 transition-colors flex items-center gap-1.5"
          >
            <span>↓</span>
            <span>New activity</span>
          </button>
        )}
      </ScrollArea>
    </div>
  )
}
