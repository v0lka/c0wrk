// Work-unit events: work_unit_settled.
//
// After a restart/session load the durable work-unit snapshot (carried by
// GetSessionRuntimeStatus.work_units) aligns paused/interrupted delegate &
// plan-step chat blocks. This hook covers the LIVE half: a unit the resume
// funnel explicitly settles (because it will not relaunch it) arrives as a
// `work_unit_settled` event, and a fresh launch (subagent_launch /
// plan_step_start with the same step id) proves the unit is running again and
// drops any stale overlay entry so the block stops rendering paused/interrupted.

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isWorkUnitSettledData } from '@/types/events'
import { useChatStore } from '@/stores/chatStore'
import { applyWorkUnitSettled } from '@/lib/sessionRuntime'

export function useWorkUnitEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // --- work_unit_settled ---
    // A durable unit was explicitly settled (the resume funnel will not
    // relaunch it) — e.g. an abandoned in-flight delegate settled as
    // interrupted. Record it so the block aligns without a reload.
    cleanups.push(
      onSessionEvent(sessionId, 'work_unit_settled', (data) => {
        if (!isWorkUnitSettledData(data)) { reportDroppedEvent('work_unit_settled', data); return }
        applyWorkUnitSettled(sessionId, data.step_id, data.status)
      }),
    )

    // --- fresh launches clear the overlay ---
    // A re-emitted launch (same step id) is the resume proof: the unit is
    // running again, so any stale paused/interrupted snapshot entry must stop
    // overriding the block's derived status.
    cleanups.push(
      onSessionEvent(sessionId, 'subagent_launch', (data) => {
        if (typeof data?.step_id === 'string') {
          useChatStore.getState().clearWorkUnitStep(sessionId, data.step_id)
        }
      }),
    )
    cleanups.push(
      onSessionEvent(sessionId, 'plan_step_start', (data) => {
        if (typeof data?.step_id === 'string') {
          useChatStore.getState().clearWorkUnitStep(sessionId, data.step_id)
        }
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
