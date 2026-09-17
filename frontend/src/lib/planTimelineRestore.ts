// Plan panel / plan-step block restoration from the backend's plan timeline.
//
// The plan declaration (`type: 'plan'`) is persisted at the START of a task,
// while a long-running plan-driven task keeps appending rows — easily
// thousands of tool calls between the declaration and the live tail. When
// the webview reloads mid-execution (tool-manager splash cycle, remount,
// session switch) ChatArea's load effect re-fetches only the NEWEST page, so
// the declaration sits far behind the window: `rebuildPlanFromHistory` finds
// no plan row, the Execution Plan panel stays hidden, and plan steps whose
// start/complete rows fell outside the window leave no block in the chat.
//
// The old fix walked older pages backwards until the declaration appeared —
// but it was capped at a handful of pages, so any session with more rows
// than the cap between declaration and tail (a normal delegated plan with
// many tool calls) silently gave up and the panel never came back.
//
// This module replaces the walk with ONE indexed backend query
// (GetSessionPlanTimeline) that returns the plan-lifecycle rows — the
// declaration plus every plan_step_start/complete/paused row — regardless of
// distance from the newest page. Those rows are deduped into the chat store
// (restoring the step index for in-window blocks and the completed-step
// blocks behind the window) and the panel is rebuilt from them with the
// durable work-unit overlay re-applied on top.

import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import { getSessionPlanTimeline } from '@/api/chat'
import { chatMessageToUI, isPersistableHistoryMessage, rebuildPlanFromHistory } from '@/lib/chatUtils'
import { applyWorkUnitOverlayToPlan } from '@/lib/sessionStoreRestore'

export interface PlanTimelineRestoreOptions {
  /** Abort check evaluated before and after the timeline RPC: when it
   *  returns true nothing is inserted and the panel is left untouched (the
   *  caller's session may have switched — a late rebuild would clobber the
   *  newly active session's panel). */
  shouldAbort?: () => boolean
}

/**
 * Restore the Execution Plan panel (and the chat's plan-lifecycle rows) from
 * the backend's plan timeline when the panel is empty after a history load.
 *
 * No-op (no RPC) when the panel already holds a plan — the window rebuild
 * succeeded or a live plan_generated arrived, and both are fresher than any
 * snapshot. Returns whether the panel holds a plan when the call completes.
 */
export async function restorePlanFromTimeline(
  sessionId: string,
  options: PlanTimelineRestoreOptions = {},
): Promise<boolean> {
  const { shouldAbort = () => false } = options

  if (usePlanStore.getState().planGroups.length > 0) return true
  if (shouldAbort()) return false

  let rows
  try {
    rows = await getSessionPlanTimeline(sessionId)
  } catch {
    // The api wrapper already logged the error; restoration is best-effort.
    return false
  }
  if (shouldAbort()) return false
  // A live plan_generated may have landed while the RPC was in flight — its
  // panel is fresher than the timeline snapshot; never clobber it.
  if (usePlanStore.getState().planGroups.length > 0) return true

  const ui = rows.filter(isPersistableHistoryMessage).map(chatMessageToUI)
  if (!ui.some(m => m.type === 'plan')) return false

  // 1) Chat store: insert the plan-lifecycle rows ahead of the loaded
  //    window. `prependHistoryMessages` dedupes by id, so rows the window
  //    already holds (e.g. a near-tail plan_step_start) keep their in-window
  //    position and only the out-of-window rows are added. The paging
  //    cursor/hasMore are passed back UNCHANGED — no page was fetched, so
  //    the scroll-up loader must keep paging from where it was. The inserted
  //    rows give groupMessages its step index (block numbering/titles) and
  //    re-open the completed steps' collapsed blocks behind the window.
  const store = useChatStore.getState()
  store.prependHistoryMessages(
    sessionId,
    ui,
    store.historyCursor[sessionId] ?? '',
    store.historyHasMore[sessionId] ?? false,
  )

  // 2) Panel: rebuild from the accumulated messages (window + timeline
  //    rows) so steps completed far behind the window settle to their
  //    terminal status instead of hanging at 'pending', then re-apply the
  //    durable work-unit overlay (paused/interrupted corrections) the same
  //    way the window rebuild path does.
  rebuildPlanFromHistory(selectSessionMessages(useChatStore.getState(), sessionId), usePlanStore.getState())
  applyWorkUnitOverlayToPlan(sessionId)
  return true
}
