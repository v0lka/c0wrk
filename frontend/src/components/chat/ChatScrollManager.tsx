import { useEffect, useLayoutEffect, useRef, useState, useCallback } from 'react'
import { useScrollContext } from './ScrollContext'
import { scrollBlockStartIntoView } from '@/lib/chatScroll'
import type { ChatMessageUI } from '@/types/messages'
import { unresolvedReviewPromptIds } from '@/types/messages'
import { ChatNewActivityBanner } from './ChatNewActivityBanner'

interface ChatScrollManagerProps {
  messages: ChatMessageUI[]
  streamingText: string | undefined
  scrollRef: React.RefObject<HTMLDivElement | null>
  children: React.ReactNode
}

// After an explicit bookmark/step navigation, stick-to-bottom auto-scroll is
// suppressed for this window. During the smooth scroll's first frames the
// recorded scroll state still says "at bottom" (the passive scroll handler
// only updates it as frames land), so an assistant_chunk arriving
// mid-navigation would otherwise yank the viewport straight back to the bottom
// and abort the navigation (finding [27]).
const NAVIGATION_AUTO_SCROLL_SUPPRESS_MS = 500

export function ChatScrollManager({
  messages,
  streamingText,
  scrollRef,
  children,
}: ChatScrollManagerProps) {
  const { setScrollToStep, setScrollToBookmark } = useScrollContext()
  const isAtBottomRef = useRef(true)
  const viewportRef = useRef<HTMLElement | null>(null)
  // The component remounts per session (key={activeSessionId} in ChatArea), so a
  // freshly-reset flag marks the first layout effect of each session switch.
  const isInitialMountRef = useRef(true)
  const prevScrollState = useRef({ scrollTop: 0, scrollHeight: 0, clientHeight: 0 })
  // IDs of review_prompt messages that still needed a decision the last time
  // the auto-scroll effect ran. A newly-appearing unresolved prompt forces the
  // chat to the bottom so the request is fully visible even if the user had
  // scrolled up to read earlier output.
  const prevReviewPromptIdsRef = useRef<Set<string>>(new Set())
  // Timestamp until which bookmark/step navigation suppresses auto-scroll.
  const suppressAutoScrollUntilRef = useRef(0)
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
  // the user was already there. A freshly-appearing review-mode prompt is an
  // exception: it requires a user decision, so the chat is forced to the
  // bottom to reveal it even when the user had scrolled away. Both behaviors
  // are suppressed for a short window after an explicit bookmark/step
  // navigation (see NAVIGATION_AUTO_SCROLL_SUPPRESS_MS).
  useLayoutEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return

    const currentReviewPromptIds = unresolvedReviewPromptIds(messages)
    const hasNewReviewPrompt =
      !isInitialMountRef.current &&
      [...currentReviewPromptIds].some((id) => !prevReviewPromptIdsRef.current.has(id))
    prevReviewPromptIdsRef.current = currentReviewPromptIds

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
      // An explicit bookmark/step navigation just moved the viewport: hold off
      // on any auto-scroll until its smooth animation settles, otherwise the
      // stale "was at bottom" baseline (or a fresh review prompt) would snap
      // the chat back to the bottom mid-navigation.
      const navigationSuppressed = Date.now() < suppressAutoScrollUntilRef.current

      if (hasNewReviewPrompt && !navigationSuppressed) {
        // A fresh review-mode prompt needs a user decision — reveal it even
        // when the user had scrolled away from the bottom.
        viewport.scrollTop = viewport.scrollHeight
        isAtBottomRef.current = true
        setHasNewActivity(false)
      } else if (wasAtBottom && !navigationSuppressed) {
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
        scrollBlockStartIntoView(viewport, target)
        isAtBottomRef.current = false
        suppressAutoScrollUntilRef.current = Date.now() + NAVIGATION_AUTO_SCROLL_SUPPRESS_MS
      }
    }
    setScrollToStep(scrollToStepFn)
    return () => setScrollToStep(null)
  }, [setScrollToStep])

  // Register scroll-to-bookmark callback. Unlike steps, a bookmark key can
  // contain arbitrary characters (plan step ids, tool ids), so match via
  // getAttribute rather than a CSS attribute selector (which would need
  // escaping and could break on unusual ids). The block-start scroll accounts
  // for the floating sticky user-message bar covering the scrollport top.
  useEffect(() => {
    const scrollToBookmarkFn = (key: string) => {
      const viewport = viewportRef.current
      if (!viewport) return
      const elements = viewport.querySelectorAll('[data-bookmark-id]')
      let target: Element | null = null
      for (const el of Array.from(elements)) {
        if (el.getAttribute('data-bookmark-id') === key) {
          target = el
          break
        }
      }
      if (target) {
        scrollBlockStartIntoView(viewport, target)
        isAtBottomRef.current = false
        suppressAutoScrollUntilRef.current = Date.now() + NAVIGATION_AUTO_SCROLL_SUPPRESS_MS
      }
    }
    setScrollToBookmark(scrollToBookmarkFn)
    return () => setScrollToBookmark(null)
  }, [setScrollToBookmark])

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
