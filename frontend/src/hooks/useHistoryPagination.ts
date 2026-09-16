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
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { getSessionHistory } from '@/api/chat'
import { chatMessageToUI, isPersistableHistoryMessage, isAgentMetricsRow, isRoutingRequestRow } from '@/lib/chatUtils'
import { restorePlanAndGoalFromHistory, applyWorkUnitOverlayToPlan, restoreAgentMetricsIfAbsent } from '@/lib/sessionStoreRestore'
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
 *
 * Because a prepended page can carry the rows a store rebuild needs (the plan
 * declaration, goal snapshots, the metrics report), the plan panel / goal badge
 * / stats row are rebuilt from the accumulated messages after each prepend —
 * otherwise the Execution Panels stay empty for a session longer than one page.
 */
export function useOlderHistoryLoader(
  sessionId: string | null,
  scrollRef: RefObject<HTMLElement | null>,
): void {
  // Granular selector returns a primitive — referentially stable (AGENTS.md).
  // It is only the trigger that re-runs the effect (and the at-top load) once
  // the newest-page RPC has recorded whether older pages exist; the guard and
  // cursor are read synchronously from the store inside loadOlder.
  const hasMore = useChatStore(s => (sessionId ? s.historyHasMore[sessionId] : undefined)) ?? false

  const sessionIdRef = useRef(sessionId)
  sessionIdRef.current = sessionId

  const loadOlder = useCallback(async () => {
    const sid = sessionIdRef.current
    if (!sid) return
    // Read the guard AND the cursor synchronously from the store: the render
    // snapshot lags a store write by a render, so two fast scroll events could
    // both pass a ref-based guard and fetch the same page twice. The store is
    // the single source of truth here.
    const store = useChatStore.getState()
    if (!store.historyHasMore[sid] || store.historyLoading[sid]) return
    const cursor = store.historyCursor[sid] ?? ''
    store.setHistoryLoading(sid, true)
    const el = scrollRef.current
    const heightBefore = el?.scrollHeight ?? 0
    try {
      const page = await getSessionHistory(sid, HISTORY_PAGE_SIZE, cursor)
      // Discard the result if the user switched sessions meanwhile.
      if (sessionIdRef.current !== sid) return
      const rows = page.messages.filter(isPersistableHistoryMessage).map(chatMessageToUI)
      const ui = rows.filter(m => !isAgentMetricsRow(m) && !isRoutingRequestRow(m))
      useChatStore.getState().prependHistoryMessages(sid, ui, page.next_cursor, page.has_more)
      // Rebuild the session stores from the messages accumulated so far — the
      // plan declaration and goal snapshots may live on this (older) page.
      // Guarded on the page actually carrying an anchor row so an ordinary
      // content page does not trigger a needless full replay + clearPlan.
      const hasPlanAnchor = ui.some(m => m.type === 'plan')
      const hasGoalAnchor = ui.some(m => m.type === 'goal_status' || m.type === 'goal_proposal')
      if (hasPlanAnchor || hasGoalAnchor) {
        restorePlanAndGoalFromHistory(sid, selectSessionMessages(useChatStore.getState(), sid))
        applyWorkUnitOverlayToPlan(sid)
      }
      // Metrics: only fill in when the store has none (a newer page wins).
      restoreAgentMetricsIfAbsent(sid, rows)
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
      // Clear the in-flight flag UNCONDITIONALLY for sid. It is keyed by sid,
      // so clearing even after a session switch is safe — and it is required:
      // a session switch during the await would otherwise leave
      // historyLoading[sid] stuck true, blocking older-page loading in this
      // session until an app restart.
      useChatStore.getState().setHistoryLoading(sid, false)
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
