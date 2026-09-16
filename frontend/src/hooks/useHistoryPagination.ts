// Chunked history loading.
//
// The NEWEST page of a session's history is loaded by ChatArea's session-load
// effect (so opening a session never fetches the whole row set). This hook owns
// paging OLDER pages in: it watches the scroll position and, when the user
// nears the top, fetches the preceding page with the store's keyset cursor and
// prepends it. Keeping the two apart means the load/reconcile effect stays
// focused on the tail while pagination lives in one small, testable unit.

import { useCallback, useEffect, useRef } from 'react'
import type { RefObject } from 'react'
import { useChatStore } from '@/stores/chatStore'
import { getSessionHistory } from '@/api/chat'
import { chatMessageToUI, isPersistableHistoryMessage, isAgentMetricsRow, isRoutingRequestRow } from '@/lib/chatUtils'
import { logger } from '@/lib/logger'

/** Number of messages fetched per page (the initial tail page and each older
 *  page). Small enough to keep the payload and the grouping pass bounded, large
 *  enough that a normal session opens in a single page. */
export const HISTORY_PAGE_SIZE = 200

/** Distance (px) from the top at which the preceding page starts loading. */
const LOAD_OLDER_THRESHOLD_PX = 400

/**
 * Loads OLDER history pages as the user scrolls toward the top of `scrollRef`.
 * Idempotent: no-ops while a fetch is in flight or once the oldest page is
 * reached. After a successful prepend the viewport is re-anchored (scrollTop
 * grows by the inserted height) so the content the user was reading stays put
 * instead of jumping as older rows appear above it.
 */
export function useOlderHistoryLoader(
  sessionId: string | null,
  scrollRef: RefObject<HTMLElement | null>,
): void {
  // Granular selectors return primitives — referentially stable (AGENTS.md).
  const hasMore = useChatStore(s => (sessionId ? s.historyHasMore[sessionId] : undefined)) ?? false
  const cursor = useChatStore(s => (sessionId ? s.historyCursor[sessionId] : undefined)) ?? ''
  const loading = useChatStore(s => (sessionId ? s.historyLoading[sessionId] : undefined)) ?? false

  // Latest values for the (once-per-session) scroll listener, which must not
  // re-subscribe on every cursor/loading change.
  const stateRef = useRef({ hasMore, cursor, loading })
  stateRef.current = { hasMore, cursor, loading }
  const sessionIdRef = useRef(sessionId)
  sessionIdRef.current = sessionId

  const loadOlder = useCallback(async () => {
    const sid = sessionIdRef.current
    if (!sid) return
    const st = stateRef.current
    if (!st.hasMore || st.loading) return
    const store = useChatStore.getState()
    store.setHistoryLoading(sid, true)
    const el = scrollRef.current
    const heightBefore = el?.scrollHeight ?? 0
    try {
      const page = await getSessionHistory(sid, HISTORY_PAGE_SIZE, st.cursor)
      // Discard the result if the user switched sessions meanwhile.
      if (sessionIdRef.current !== sid) return
      const ui = page.messages
        .filter(isPersistableHistoryMessage)
        .map(chatMessageToUI)
        .filter(m => !isAgentMetricsRow(m) && !isRoutingRequestRow(m))
      useChatStore.getState().prependHistoryMessages(sid, ui, page.next_cursor, page.has_more)
      // Re-anchor after the DOM has laid out the prepended rows.
      if (el) {
        requestAnimationFrame(() => {
          const delta = el.scrollHeight - heightBefore
          if (delta > 0) el.scrollTop += delta
        })
      }
    } catch (err) {
      logger.error('Failed to load older session history:', err)
    } finally {
      if (sessionIdRef.current === sid) useChatStore.getState().setHistoryLoading(sid, false)
    }
  }, [scrollRef])

  useEffect(() => {
    const el = scrollRef.current
    if (!el || !sessionId) return
    const onScroll = () => {
      if (el.scrollTop <= LOAD_OLDER_THRESHOLD_PX) void loadOlder()
    }
    el.addEventListener('scroll', onScroll, { passive: true })
    // A page may open already at the top with older pages still available.
    if (el.scrollTop <= LOAD_OLDER_THRESHOLD_PX) void loadOlder()
    return () => el.removeEventListener('scroll', onScroll)
  }, [sessionId, loadOlder, scrollRef, hasMore])
}
