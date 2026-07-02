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
          type: 'tool_call',
          content: `SubAgent: ${data.description}`,
          metadata: { tool: 'subagent', args: data.description, step: data.step_id, plan_step_id: data.plan_step_id },
          timestamp: Date.now(),
        })
      }),
    )

    // --- subagent_complete ---
    cleanups.push(
      onSessionEvent(sessionId, 'subagent_complete', (data) => {
        if (!isSubAgentCompleteData(data)) { reportDroppedEvent('subagent_complete', data); return }
        useChatStore.getState().updateMessage(sessionId, `subagent-${data.step_id}-launch`, {
          metadata: {
            tool: 'subagent',
            completed: true,
            error: data.success ? undefined : 'SubAgent failed',
            result_preview: data.success ? `Completed in ${data.duration}ms` : `Failed after ${data.duration}ms`,
            result_len: 0,
            plan_step_id: data.plan_step_id,
          },
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
