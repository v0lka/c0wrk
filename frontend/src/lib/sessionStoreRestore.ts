// Restore session-scoped store state (plan panel, goal badge) from persisted
// history rows.
//
// The whole session history is loaded in ONE request (ChatArea), so the plan
// declaration at the start of the task and the goal snapshots that land
// throughout the run are all present in the loaded set — no page walk is needed
// to reach them.

import { usePlanStore } from '@/stores/planStore'
import { useGoalStore } from '@/stores/goalStore'
import { rebuildPlanFromHistory, rebuildGoalFromHistory } from '@/lib/chatUtils'
import type { ChatMessageUI } from '@/types/messages'

/**
 * Rebuild the plan panel and goal badge from the messages loaded for a session.
 * `rebuildPlanFromHistory` clears the panel before replaying, so passing the
 * full loaded set is idempotent: the panel always reflects the newest plan
 * declaration within the loaded history.
 */
export function restorePlanAndGoalFromHistory(sessionId: string, messages: ChatMessageUI[]): void {
  rebuildPlanFromHistory(messages, usePlanStore.getState())
  rebuildGoalFromHistory(messages, useGoalStore.getState(), useGoalStore.getState().activeGoal?.[sessionId])
}
