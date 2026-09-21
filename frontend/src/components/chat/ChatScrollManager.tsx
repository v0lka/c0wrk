import { useEffect, useLayoutEffect, useRef, useState, useCallback } from 'react'
import { useScrollContext } from './ScrollContext'
import { scrollBlockStartIntoView } from '@/lib/chatScroll'
import { cssEscape } from '@/lib/cssEscape'
import { useChatStore } from '@/stores/chatStore'
import type { ChatMessageUI } from '@/types/messages'
import { unresolvedReviewPromptIds } from '@/types/messages'
import { ChatNewActivityBanner } from './ChatNewActivityBanner'

interface ChatScrollManagerProps {
  /** Session whose transcript this viewport shows. The component remounts per
   *  session (key={activeSessionId} in ChatArea), so the scroll position is
   *  saved under this id on unmount and restored on the next initial mount.
   *  Null disables both (no session context). */
  sessionId: string | null
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

// Distance (px) from the content bottom within which the viewport counts as
// "at the bottom" — the single threshold behind stick-to-bottom engagement,
// the new-activity pill and the restored-position re-check.
const AT_BOTTOM_THRESHOLD_PX = 50

export function ChatScrollManager({
  sessionId,
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
  // Message count + tail id from the previous run. A count increase with an
  // unchanged tail id means an OLDER page was prepended above the viewport
  // (chunked history loading) rather than new activity appearing at the bottom.
  // That must not raise the "New activity" pill or yank the scroll position.
  const prevCountRef = useRef(0)
  const prevLastIdRef = useRef('')
  // Streaming text from the previous run. Together with the count/tail-id
  // pair above it identifies a CONTENT-IDENTICAL re-merge: a fresh array of
  // the same messages with unchanged streaming (e.g. the newest-page history
  // RPC re-merging after a session switch restored a mid-scroll position).
  // Nothing new appeared in that case, so the "New activity" pill must not
  // be raised — only a genuinely changed tail (or new streaming output)
  // counts as activity.
  const prevStreamingTextRef = useRef<string | undefined>(undefined)
  // IDs of review_prompt messages that still needed a decision the last time
  // the auto-scroll effect ran. A newly-appearing unresolved prompt forces the
  // chat to the bottom so the request is fully visible even if the user had
  // scrolled up to read earlier output.
  const prevReviewPromptIdsRef = useRef<Set<string>>(new Set())
  // Timestamp until which bookmark/step navigation suppresses auto-scroll.
  const suppressAutoScrollUntilRef = useRef(0)
  // scrollTop of the most recent programmatic at-bottom write (post-clamp —
  // the value the browser will report in the scroll event the write queues).
  // Scroll events are delivered asynchronously at the browser's rendering
  // steps, by which time the content may have grown FURTHER (async
  // markdown/highlight layout, an image decoding). An event whose position
  // still equals our own write is
  // not the user scrolling away — recomputing "at bottom" against the grown
  // content there would poison isAtBottomRef/prevScrollState (the write
  // targeted the bottom of the content AS IT WAS) and permanently disable
  // stick-to-bottom until the user manually re-scrolls to the bottom.
  // handleScroll keeps the writer's intent for such events instead. Armed by
  // every at-bottom-intent write; cleared when an event diverges from it.
  const lastWriteTopRef = useRef<number | null>(null)
  const [hasNewActivity, setHasNewActivity] = useState(false)

  const scrollToBottom = useCallback(() => {
    const viewport = viewportRef.current
    if (!viewport) return
    viewport.scrollTop = viewport.scrollHeight
    lastWriteTopRef.current = viewport.scrollTop
    isAtBottomRef.current = true
    // Refresh the incremental-growth baseline too: the passive scroll
    // handler's own-write branch deliberately returns early WITHOUT updating
    // prevScrollState, so a stale pre-pin baseline here would make the next
    // message re-raise the "New activity" pill (and skip the stick-to-bottom
    // write) even though the viewport is pinned at the bottom.
    prevScrollState.current = {
      scrollTop: viewport.scrollTop,
      scrollHeight: viewport.scrollHeight,
      clientHeight: viewport.clientHeight,
    }
    setHasNewActivity(false)
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
      // Own-write filtering: an event whose position equals our last
      // programmatic at-bottom write is the delivery of THAT write (possibly
      // after further content growth), not the user scrolling away. Keep the
      // writer's intent — do not recompute the at-bottom baseline against
      // content that grew past the write's target, and do not clobber the
      // effect's post-run baseline either. Any diverging position (a real user
      // scroll, a smooth navigation frame, the history-prepend re-anchor)
      // invalidates the marker and resumes normal tracking.
      const ownWrite = lastWriteTopRef.current !== null && viewport.scrollTop === lastWriteTopRef.current
      if (!ownWrite) lastWriteTopRef.current = null
      if (ownWrite) {
        if (isAtBottomRef.current) setHasNewActivity(false)
        return
      }
      const atBottom = viewport.scrollTop + viewport.clientHeight >= viewport.scrollHeight - AT_BOTTOM_THRESHOLD_PX
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

  // Save the reading position when this session's viewport goes away. ChatArea
  // remounts the manager per session (key={activeSessionId}), so this cleanup
  // fires exactly on a session switch (and on app teardown); prevScrollState is
  // the freshest snapshot — the passive scroll handler above updates it on
  // every user scroll, so it holds where reading actually stopped. sessionId
  // never changes within one mount, making this an unmount-only save.
  useEffect(() => {
    if (!sessionId) return
    return () => {
      const { scrollTop, scrollHeight } = prevScrollState.current
      useChatStore.getState().saveScrollPosition(sessionId, { scrollTop, scrollHeight })
    }
  }, [sessionId])

  // Auto-scroll: on a session switch (component remount) restore the saved
  // reading position, or jump to the latest content when none was saved —
  // except for a session whose task is STILL RUNNING: it keeps producing
  // output at the bottom, so switching to it always reveals the live tail,
  // saved position notwithstanding; on incremental content growth, stick to
  // the bottom only if the user was already there. A freshly-appearing review-mode prompt is an exception: it
  // requires a user decision, so the chat is forced to the bottom to reveal it
  // even when the user had scrolled away. Both behaviors are suppressed for a
  // short window after an explicit bookmark/step navigation (see
  // NAVIGATION_AUTO_SCROLL_SUPPRESS_MS).
  useLayoutEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return

    const currentReviewPromptIds = unresolvedReviewPromptIds(messages)
    const hasNewReviewPrompt =
      !isInitialMountRef.current &&
      [...currentReviewPromptIds].some((id) => !prevReviewPromptIdsRef.current.has(id))
    prevReviewPromptIdsRef.current = currentReviewPromptIds

    const lastId = messages.length > 0 ? messages[messages.length - 1]!.id : ''
    const prependedOlder =
      !isInitialMountRef.current &&
      messages.length > prevCountRef.current &&
      lastId === prevLastIdRef.current
    // True when the recognized tail did not change at all: same tail id, same
    // count, same streaming text. The messages array may still be brand new
    // (mergeHistoryMessages always writes a fresh index/order), so identity
    // cannot be used — only the recognized content can.
    const tailUnchanged =
      !isInitialMountRef.current &&
      messages.length === prevCountRef.current &&
      lastId === prevLastIdRef.current &&
      streamingText === prevStreamingTextRef.current
    prevCountRef.current = messages.length
    prevLastIdRef.current = lastId
    prevStreamingTextRef.current = streamingText

    if (isInitialMountRef.current) {
      // Session selected. A previously saved reading position (captured when
      // this session's viewport last unmounted) is restored as-is — the
      // browser clamps the offset against the freshly mounted (still
      // estimated, possibly shorter) content, and the ResizeObserver below
      // keeps a restored-at-bottom viewport glued to the bottom as real
      // measurements replace the estimates. A RUNNING session is the
      // exception: its live output grows the tail, so the switch must open at
      // the bottom regardless of where reading stopped (taskActive survives
      // session switches — nothing blindly resets it, the background watcher
      // clears it on terminal events, and the switch-time status RPC
      // restores it). When the flag is still stale-false at mount — the
      // switch-time correctors are asynchronous — the saved position is
      // restored for now and the taskActive edge effect below re-pins to the
      // live tail the moment the flag lands. Without a saved position (first
      // visit) the latest content is revealed so stick-to-bottom engages
      // without the user having to scroll down first.
      const store = useChatStore.getState()
      const saved = sessionId ? store.scrollPositions[sessionId] : undefined
      const taskRunning = sessionId ? store.taskActive[sessionId] === true : false
      if (saved && !taskRunning) {
        viewport.scrollTop = saved.scrollTop
        isAtBottomRef.current =
          viewport.scrollTop + viewport.clientHeight >= viewport.scrollHeight - AT_BOTTOM_THRESHOLD_PX
      } else {
        viewport.scrollTop = viewport.scrollHeight
        lastWriteTopRef.current = viewport.scrollTop
        isAtBottomRef.current = true
      }
      isInitialMountRef.current = false
      setHasNewActivity(false)
    } else {
      const prev = prevScrollState.current
      const wasAtBottom = prev.scrollTop + prev.clientHeight >= prev.scrollHeight - AT_BOTTOM_THRESHOLD_PX
      // An explicit bookmark/step navigation just moved the viewport: hold off
      // on any auto-scroll until its smooth animation settles, otherwise the
      // stale "was at bottom" baseline (or a fresh review prompt) would snap
      // the chat back to the bottom mid-navigation.
      const navigationSuppressed = Date.now() < suppressAutoScrollUntilRef.current

      if (prependedOlder) {
        // Older rows inserted above the viewport: leave the viewport where it
        // is (the prepend path re-anchors it) and do not raise the new-activity
        // pill — nothing new appeared at the bottom.
      } else if (hasNewReviewPrompt && !navigationSuppressed) {
        // A fresh review-mode prompt needs a user decision — reveal it even
        // when the user had scrolled away from the bottom.
        viewport.scrollTop = viewport.scrollHeight
        lastWriteTopRef.current = viewport.scrollTop
        isAtBottomRef.current = true
        setHasNewActivity(false)
      } else if (wasAtBottom && !navigationSuppressed) {
        viewport.scrollTop = viewport.scrollHeight
        lastWriteTopRef.current = viewport.scrollTop
        isAtBottomRef.current = true
      } else if (tailUnchanged) {
        // Content-identical re-merge in a fresh array: nothing new appeared
        // at the tail, so the pill must not be raised — the user simply is
        // not at the bottom (e.g. a session switch restored a saved
        // mid-transcript position and the newest-page history RPC just
        // re-merged the same rows). Consistent with the mount branch, which
        // deliberately opens pill-free, and with the ResizeObserver path,
        // which guards against the same false positive.
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
    // sessionId never changes within one mount (ChatArea remounts the manager
    // per session via key=), so listing it only satisfies the deps lint — it
    // cannot re-fire this effect on its own.
  }, [messages, streamingText, sessionId])

  // Live-tail re-pin on a late task-flag correction. The mount-time decision
  // above reads the in-memory taskActive flag SYNCHRONOUSLY, but the flag's
  // authoritative switch-time correctors resolve asynchronously — the fast
  // status RPC in useTaskFlagRestore, and reconcileRuntimeStatus after the
  // history merge. A session that IS running can therefore still read
  // taskActive=false at mount (a stale-false in-memory flag), take the
  // saved-position restore branch, and then never re-pin once the RPC lands —
  // leaving a running session's live tail hidden behind a stale reading
  // position and a "New activity" pill. Watch the flag: when it flips to true
  // AFTER the initial mount, mirror the mount-time running-session behavior
  // and pin to the live tail. The reverse edge (true→false — a completion /
  // pause observed while mounted) intentionally does nothing: content-growth
  // logic owns subsequent movement. Suppressed inside the bookmark/step
  // navigation window so a correction landing mid-navigation cannot abort the
  // user's explicit scroll target.
  const taskActive = useChatStore(s => (sessionId ? s.taskActive[sessionId] === true : false))
  const prevTaskActiveRef = useRef<boolean | null>(null)
  useLayoutEffect(() => {
    const prev = prevTaskActiveRef.current
    prevTaskActiveRef.current = taskActive
    // First run after mount: the layout effect above already made the
    // mount-time decision with this very flag value — nothing to correct.
    if (prev === null || prev || !taskActive) return
    if (Date.now() < suppressAutoScrollUntilRef.current) return
    scrollToBottom()
  }, [taskActive, sessionId, scrollToBottom])

  // Content-growth stickiness. The auto-scroll effect above only re-runs when
  // messages/streamingText change, but the content height can keep growing
  // AFTER that commit: images decoding, async markdown/highlight layout.
  // Observing the transcript content wrapper (the viewport's first element
  // child — ChatHoverRegion, whose height tracks the content) catches those:
  // when the content grew while the user was following the tail
  // (isAtBottomRef), re-pin to the bottom; when they had scrolled up, do
  // nothing (no jerk). This is also what keeps a restored/pinned initial
  // position glued to the TRUE bottom as late async layout settles.
  useEffect(() => {
    const viewport = viewportRef.current
    const content = viewport?.firstElementChild
    if (!viewport || !content || typeof ResizeObserver === 'undefined') return
    let lastHeight = viewport.scrollHeight
    const observer = new ResizeObserver(() => {
      const height = viewport.scrollHeight
      if (height > lastHeight && isAtBottomRef.current) {
        viewport.scrollTop = viewport.scrollHeight
        lastWriteTopRef.current = viewport.scrollTop
      }
      lastHeight = height
    })
    observer.observe(content)
    return () => observer.disconnect()
  }, [])

  // Register scroll-to-step callback
  useEffect(() => {
    const scrollToStepFn = (stepId: string) => {
      const viewport = viewportRef.current
      if (!viewport) return
      // Step ids originate from LLM-authored declare_plan payloads and can
      // contain selector metacharacters (quotes, backslashes) — escape the
      // id before interpolating it into the attribute selector, or the
      // querySelectorAll call throws a SyntaxError DOMException and the
      // navigation silently dies inside the click handler.
      const elements = viewport.querySelectorAll(`[data-step-id="${cssEscape(stepId)}"]`)
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
