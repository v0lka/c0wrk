// Plan-anchor backfill after a mid-execution reload/remount.
//
// The plan declaration (`type: 'plan'`) is persisted at the START of a task,
// while a long-running plan-driven task keeps appending rows. When the webview
// reloads mid-execution (tool-manager splash cycle, remount), ChatArea's load
// effect re-fetches only the NEWEST page: the declaration sits further back,
// `openSteps`/`planGroups` rebuild empty, so subsequent subagent/plan_step
// events render in the chat ROOT and the Execution Plan panel stays hidden
// until the user happens to scroll up far enough to load the anchor page.
//
// The durable work-unit snapshot (GetSessionRuntimeStatus.work_units, reported
// only while a resumable task exists) is the signal that this session's task
// IS plan-driven: when it carries plan_step/subagent units but the accumulated
// messages hold no plan row, walk OLDER pages backwards (bounded) and prepend
// them until the declaration is in the store, then rebuild the panel — the
// same restore useOlderHistoryLoader runs for pages the user pages in by hand.

import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import { getSessionHistory } from '@/api/chat'
import type { WorkUnitSnapshot } from '@/api/chat'
import { chatMessageToUI, isPersistableHistoryMessage, isAgentMetricsRow, isRoutingRequestRow } from '@/lib/chatUtils'
import { restorePlanAndGoalFromHistory, applyWorkUnitOverlayToPlan } from '@/lib/sessionStoreRestore'
import { logger } from '@/lib/logger'

/** Default page size of the backwards walk; ChatArea passes HISTORY_PAGE_SIZE
 *  (the same page the scroll-up loader uses) so both paths page identically. */
const DEFAULT_PAGE_SIZE = 200

/** Hard cap on pages fetched per call: the anchor of a plan-driven task sits
 *  near the task's start, so a handful of pages is enough — and a session with
 *  NO declaration at all must not walk its entire history on every reload. */
export const PLAN_ANCHOR_MAX_PAGES = 5

export interface PlanAnchorBackfillOptions {
  /** Rows fetched per page in the backwards walk. */
  pageSize?: number
  /** Maximum pages to fetch before giving up (default 5). */
  maxPages?: number
  /** Abort signal evaluated before each fetch: when it returns true the walk
   *  stops without touching the plan panel (the caller's session may have
   *  switched — a late rebuild would clobber the new session's panel). */
  shouldAbort?: () => boolean
}

/** Whether the durable work-unit snapshot marks the session's task as
 *  plan-driven (units the chat nests under plan steps / open steps). */
function isPlanDriven(workUnits: WorkUnitSnapshot[] | undefined): boolean {
  return workUnits?.some(u => u.kind === 'plan_step' || u.kind === 'subagent') ?? false
}

/**
 * Ensure the plan declaration row is loaded for a plan-driven session whose
 * accumulated messages lack it. No-op (no RPC) when the snapshot does not
 * indicate a plan-driven unfinished task, or when the anchor is already in the
 * store AND the panel already reflects a plan. When the anchor IS in the store
 * but the panel is empty (a previous backfill prepended the page, then the
 * panel was cleared by a re-mount), the panel is rebuilt locally — still no
 * RPC. Otherwise walks older pages via the store's keyset cursor, prepending
 * each through `prependHistoryMessages` (dedupe + cursor advance come with
 * it), and on finding a `type: 'plan'` row rebuilds the plan panel and goal
 * badge from the accumulated messages and re-applies the durable work-unit
 * overlay. Returns whether the panel ends the call holding a plan.
 */
export async function ensurePlanAnchorLoaded(
  sessionId: string,
  workUnits: WorkUnitSnapshot[] | undefined,
  options: PlanAnchorBackfillOptions = {},
): Promise<boolean> {
  const {
    pageSize = DEFAULT_PAGE_SIZE,
    maxPages = PLAN_ANCHOR_MAX_PAGES,
    shouldAbort = () => false,
  } = options

  // Gate 1 — only a plan-driven unfinished task needs the anchor. work_units
  // exist only while a resumable task is persisted, so a finished session
  // never enters the walk.
  if (!isPlanDriven(workUnits)) return false

  const store = useChatStore.getState()
  const accumulated = selectSessionMessages(store, sessionId)
  const anchorInStore = accumulated.some(m => m.type === 'plan')
  if (anchorInStore) {
    if (usePlanStore.getState().planGroups.length > 0) return false
    // The declaration row is already accumulated (an earlier backfill walk
    // prepended it, or a re-load's merge kept previously prepended rows) but
    // the panel was cleared since — rebuild locally, no RPC needed.
    restorePlanAndGoalFromHistory(sessionId, accumulated)
    applyWorkUnitOverlayToPlan(sessionId)
    return true
  }

  // Gate 2 — paging bookkeeping: no older pages to walk back through.
  let cursor = store.historyCursor[sessionId] ?? ''
  let hasMore = store.historyHasMore[sessionId] ?? false
  if (!hasMore || shouldAbort()) return false

  // Hold the in-flight flag for the whole walk so the scroll-up loader does
  // not race us page-by-page. A walk interleaved with an already-in-flight
  // loader fetch stays benign: prependHistoryMessages dedupes by id and both
  // paths converge on the same next cursor.
  store.setHistoryLoading(sessionId, true)
  try {
    for (let fetched = 0; fetched < maxPages && hasMore && !shouldAbort(); fetched++) {
      const page = await getSessionHistory(sessionId, pageSize, cursor)
      // A session switch while the RPC was in flight: discard the page (the
      // cursor was not advanced, so nothing is lost — the next visit re-fetches).
      if (shouldAbort()) return false
      const rows = page.messages.filter(isPersistableHistoryMessage).map(chatMessageToUI)
      const ui = rows.filter(m => !isAgentMetricsRow(m) && !isRoutingRequestRow(m))
      useChatStore.getState().prependHistoryMessages(sessionId, ui, page.next_cursor, page.has_more)
      cursor = page.next_cursor
      hasMore = page.has_more
      if (ui.some(m => m.type === 'plan')) {
        restorePlanAndGoalFromHistory(sessionId, selectSessionMessages(useChatStore.getState(), sessionId))
        applyWorkUnitOverlayToPlan(sessionId)
        return true
      }
    }
    return false
  } catch (err) {
    logger.error('ensurePlanAnchorLoaded: failed to backfill the plan declaration:', err)
    return false
  } finally {
    useChatStore.getState().setHistoryLoading(sessionId, false)
  }
}
