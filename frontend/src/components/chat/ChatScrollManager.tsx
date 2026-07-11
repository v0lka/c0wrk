import { useEffect, useLayoutEffect, useRef, useState, useCallback } from 'react'
import { useScrollContext } from './ScrollContext'
import type { ChatMessageUI } from '@/types/messages'
import { ChatNewActivityBanner } from './ChatNewActivityBanner'

interface ChatScrollManagerProps {
  messages: ChatMessageUI[]
  streamingText: string | undefined
  scrollRef: React.RefObject<HTMLDivElement | null>
  children: React.ReactNode
}

export function ChatScrollManager({
  messages,
  streamingText,
  scrollRef,
  children,
}: ChatScrollManagerProps) {
  const { setScrollToStep } = useScrollContext()
  const isAtBottomRef = useRef(true)
  const viewportRef = useRef<HTMLElement | null>(null)
  // The component remounts per session (key={activeSessionId} in ChatArea), so a
  // freshly-reset flag marks the first layout effect of each session switch.
  const isInitialMountRef = useRef(true)
  const prevScrollState = useRef({ scrollTop: 0, scrollHeight: 0, clientHeight: 0 })
  const [hasNewActivity, setHasNewActivity] = useState(false)

  const scrollToBottom = useCallback(() => {
    const viewport = viewportRef.current
    if (viewport) {
      viewport.scrollTop = viewport.scrollHeight
      isAtBottomRef.current = true
      setHasNewActivity(false)
    }
  }, [])

  // Cache viewport element. Runs as a layout effect (and is declared before the
  // auto-scroll effect) so viewportRef is populated on the very first commit of
  // a session switch, letting the auto-scroll effect act immediately.
  useLayoutEffect(() => {
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

  // Auto-scroll: on a session switch (component remount) always jump to the
  // latest content; on incremental content growth, stick to the bottom only if
  // the user was already there.
  useLayoutEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return

    if (isInitialMountRef.current) {
      // New session selected — always reveal the most recent messages so
      // stick-to-bottom engages without the user having to scroll down first.
      viewport.scrollTop = viewport.scrollHeight
      isAtBottomRef.current = true
      isInitialMountRef.current = false
      setHasNewActivity(false)
    } else {
      const prev = prevScrollState.current
      const wasAtBottom = prev.scrollTop + prev.clientHeight >= prev.scrollHeight - 50

      if (wasAtBottom) {
        viewport.scrollTop = viewport.scrollHeight
        isAtBottomRef.current = true
      } else {
        setHasNewActivity(true)
      }
    }

    // Always record the post-scroll state so the next incremental run has an
    // accurate baseline (matters after the initial force-scroll too).
    prevScrollState.current = {
      scrollTop: viewport.scrollTop,
      scrollHeight: viewport.scrollHeight,
      clientHeight: viewport.clientHeight,
    }
  }, [messages, streamingText])

  // Register scroll-to-step callback
  useEffect(() => {
    const scrollToStepFn = (stepId: string) => {
      const viewport = viewportRef.current
      if (!viewport) return
      const elements = viewport.querySelectorAll(`[data-step-id="${stepId}"]`)
      const target = elements[elements.length - 1]
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
        hasNewActivity={hasNewActivity && !isAtBottomRef.current}
        scrollToBottom={scrollToBottom}
      />
    </div>
  )
}
