// Restore session-scoped store state (plan panel, goal badge, agent-metrics
// stats row) from persisted history rows.
//
// History is loaded one page at a time (the newest page first, older pages on
// scroll-up). The plan is declared at the START of a session, and the goal
// snapshots / metrics report land throughout the run — so the rows a store
// rebuild needs are frequently on an OLDER page than the one the initial load
// fetched. Rebuilding only from the newest page would leave the Execution
// Panels empty (the panel hides itself when `planGroups` is empty) for every
// session longer than a single page, while the chat still grows step blocks as
// older pages stream in — the two views diverge.
//
// So the rebuild is run twice: once after the initial newest-page load
// (ChatArea) and again after each older page is prepended
// (useOlderHistoryLoader), always over the messages accumulated SO FAR. Both
// call sites live here so the wiring is written once.

import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import { useGoalStore } from '@/stores/goalStore'
import { rebuildPlanFromHistory, rebuildGoalFromHistory, lastAgentMetricsFromHistory } from '@/lib/chatUtils'
import type { ChatMessageUI } from '@/types/messages'

/**
 * Rebuild the plan panel and goal badge from the messages accumulated for a
 * session. `rebuildPlanFromHistory` clears the panel before replaying, so
 * passing the accumulated set is idempotent: the panel always reflects the
 * newest plan declaration within the loaded window. The durable work-unit
 * overlay is applied separately (see {@link applyWorkUnitOverlayToPlan}), so
 * the caller controls whether the snapshot correction runs.
 */
export function restorePlanAndGoalFromHistory(sessionId: string, messages: ChatMessageUI[]): void {
  rebuildPlanFromHistory(messages, usePlanStore.getState())
  rebuildGoalFromHistory(messages, useGoalStore.getState(), useGoalStore.getState().activeGoal?.[sessionId])
}

/**
 * Re-apply the session's durable work-unit overlay to the plan panel. After an
 * older-page rebuild, a step whose replayed history lacks its terminal event
 * (the ledger settled it after a crash) would otherwise spin at 'running' in
 * the panel while the chat overlay already shows it settled.
 */
export function applyWorkUnitOverlayToPlan(sessionId: string): void {
  const overlay = useChatStore.getState().workUnitStatus?.[sessionId]
  if (overlay) usePlanStore.getState().applyWorkUnitStatuses(overlay)
}

/**
 * Restore the agent-metrics stats row from a history page, but only when the
 * store does not already hold one. The older-page path uses this: pages load
 * newest-first, so the newest-page load already set the freshest report, and an
 * older page's metrics must never overwrite it with a stale one. It matters
 * only when the newest pages carried no metrics row at all (the report sat
 * further back than one page) — the first such row encountered walking
 * backwards is then the newest report there is.
 */
export function restoreAgentMetricsIfAbsent(sessionId: string, messages: ChatMessageUI[]): void {
  const plan = usePlanStore.getState()
  if (plan.sessionStats[sessionId]?.lastAgentMetrics !== undefined) return
  const metrics = lastAgentMetricsFromHistory(messages)
  if (metrics) plan.setSessionStats(sessionId, { lastAgentMetrics: metrics })
}
