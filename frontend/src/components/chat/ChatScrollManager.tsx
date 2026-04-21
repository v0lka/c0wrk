import React, { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useScrollStore } from '@/stores/scrollStore'
import { ChatMessageUI } from '@/stores/chatStore'
import { ChatNewActivityBanner } from './ChatNewActivityBanner'

interface ChatScrollManagerProps {
  messages: ChatMessageUI[]
  streamingText: string | null
  scrollRef: React.RefObject<HTMLDivElement | null>
  children: React.ReactNode
}

export function ChatScrollManager({
  messages,
  streamingText,
  scrollRef,
  children,
}: ChatScrollManagerProps): React.ReactNode {
  const setScrollToStep = useScrollStore(s => s.setScrollToStep)
  const isAtBottomRef = useRef(true)
  const viewportRef = useRef<HTMLElement | null>(null)
  const prevScrollState = useRef<{ scrollTop: number; scrollHeight: number; clientHeight: number }>({
    scrollTop: 0,
    scrollHeight: 0,
    clientHeight: 0,
  })
  const [hasNewActivity, setHasNewActivity] = useState(false)

  // Cache viewport element
  useEffect(() => {
    if (!scrollRef.current) return
    const vp = scrollRef.current
    viewportRef.current = vp
    prevScrollState.current = {
      scrollTop: vp.scrollTop,
      scrollHeight: vp.scrollHeight,
      clientHeight: vp.clientHeight,
    }
  }, [scrollRef])

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
  }, [])

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

  return (
    <div className="flex-1 min-w-0 overflow-auto custom-scrollbar" ref={scrollRef}>
      {children}
      <ChatNewActivityBanner
        hasNewActivity={hasNewActivity}
        isAtBottomRef={isAtBottomRef}
        viewportRef={viewportRef}
        setHasNewActivity={setHasNewActivity}
      />
    </div>
  )
}
