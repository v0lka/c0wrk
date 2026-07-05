// Subagent events: subagent_launch, subagent_complete

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isSubAgentLaunchData, isSubAgentCompleteData } from '@/types/events'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'

export function useSubagentEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // --- subagent_launch ---
    cleanups.push(
      onSessionEvent(sessionId, 'subagent_launch', (data) => {
        if (!isSubAgentLaunchData(data)) { reportDroppedEvent('subagent_launch', data); return }
        useChatStore.getState().setActivityStatus('Launching sub-agent...')
        useChatStore.getState().addMessage(sessionId, {
          id: `subagent-${data.step_id}-launch`,
          sessionId,
          type: 'subagent_launch',
          content: `SubAgent: ${data.description}`,
          metadata: { step_id: data.step_id, description: data.description, plan_step_id: data.plan_step_id },
          timestamp: Date.now(),
        })
      }),
    )

    // --- subagent_complete ---
    cleanups.push(
      onSessionEvent(sessionId, 'subagent_complete', (data) => {
        if (!isSubAgentCompleteData(data)) { reportDroppedEvent('subagent_complete', data); return }
        useChatStore.getState().addMessage(sessionId, {
          id: `subagent-${data.step_id}-complete`,
          sessionId,
          type: 'subagent_complete',
          content: '',
          metadata: { step_id: data.step_id, success: data.success, duration: data.duration, plan_step_id: data.plan_step_id },
          timestamp: Date.now(),
        })
        // Update plan store immediately so steps reflect completion
        // without waiting for the batched plan_step_complete event.
        usePlanStore.getState().updateStepStatus(
          data.step_id,
          data.success ? 'completed' : 'failed',
          data.duration,
        )
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
