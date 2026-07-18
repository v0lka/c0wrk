// Goal lifecycle events: goal_proposal (distinct event), and goal_status /
// goal_progress (carried on the phase-discriminated `service` channel).
//
// Backend wiring (see core/orchestrator_goal.go):
//  - goal_proposal is emitted as its OWN session event by the desktop goal
//    proposer when the derivation agent calls propose_goal.
//  - goal_status / goal_progress are emitted via ServiceWithMeta — i.e. the
//    generic `service` event — with a `phase` discriminator
//    ('goal_status' / 'goal_progress'). There is no dedicated emitter method,
//    so we subscribe to `service` and branch on the phase (mirroring the
//    existing phase-discriminated handler in useLifecycleEvents).
//
// Three subscriptions => three cleanups:
//   1. goal_proposal  → handleGoalProposalEvent
//   2. service[phase=goal_status]    → handleGoalStatusEvent
//   3. service[phase=goal_progress]  → handleGoalProgressEvent
//
// The two `service` listeners are intentionally separate (rather than one
// listener branching on phase) to give each goal stream a focused handler and
// to map 1:1 onto the three goal event contracts. Ordinary (non-goal) service
// events fall through both listeners as no-ops.

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

    // --- goal_status (carried on `service` with phase === 'goal_status') ---
    cleanups.push(
      onSessionEvent(sessionId, 'service', (data) => {
        if (!isGoalStatusData(data)) return // not a goal_status payload — ignore silently
        handleGoalStatusEvent(sessionId, data)
      }),
    )

    // --- goal_progress (carried on `service` with phase === 'goal_progress') ---
    cleanups.push(
      onSessionEvent(sessionId, 'service', (data) => {
        if (!isGoalProgressData(data)) return // not a goal_progress payload — ignore silently
        handleGoalProgressEvent(sessionId, data)
      }),
    )

    return () => cleanups.forEach((fn) => fn())
  }, [sessionId])
}
