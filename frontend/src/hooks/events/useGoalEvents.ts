// Goal lifecycle events: goal_proposal, goal_status, and goal_progress are each
// their OWN dedicated session event.
//
// Backend wiring (see core/orchestrator_goal.go + backend/session/emitter.go):
//  - goal_proposal is emitted by the desktop goal proposer when the derivation
//    agent calls propose_goal.
//  - goal_status / goal_progress are emitted via the dedicated Emitter methods
//    GoalStatus / GoalProgress (NOT the generic phase-discriminated `service`
//    channel). This dedicated routing is what makes the live subscription
//    reliably reach the goal store — the previous `service`-piggybacking lost
//    turn telemetry in transit, leaving the turn badge stuck at the seeded
//    value.
//
// Three subscriptions => three cleanups:
//   1. goal_proposal  → handleGoalProposalEvent
//   2. goal_status    → handleGoalStatusEvent
//   3. goal_progress  → handleGoalProgressEvent

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isGoalProposalData, isGoalStatusData, isGoalProgressData } from '@/types/events'
import { handleGoalProposalEvent, handleGoalStatusEvent, handleGoalProgressEvent } from './goalHandlers'

export function useGoalEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // --- goal_proposal (distinct session event) ---
    cleanups.push(
      onSessionEvent(sessionId, 'goal_proposal', (data) => {
        if (!isGoalProposalData(data)) { reportDroppedEvent('goal_proposal', data); return }
        handleGoalProposalEvent(sessionId, data)
      }),
    )

    // --- goal_status (dedicated session event) ---
    cleanups.push(
      onSessionEvent(sessionId, 'goal_status', (data) => {
        if (!isGoalStatusData(data)) { reportDroppedEvent('goal_status', data); return }
        handleGoalStatusEvent(sessionId, data)
      }),
    )

    // --- goal_progress (dedicated session event) ---
    cleanups.push(
      onSessionEvent(sessionId, 'goal_progress', (data) => {
        if (!isGoalProgressData(data)) { reportDroppedEvent('goal_progress', data); return }
        handleGoalProgressEvent(sessionId, data)
      }),
    )

    return () => cleanups.forEach((fn) => fn())
  }, [sessionId])
}
