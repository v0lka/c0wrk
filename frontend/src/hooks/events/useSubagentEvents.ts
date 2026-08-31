// Subagent events: subagent_launch, subagent_complete, subagent_paused
//
// Delegated steps are NOT tracked in the plan panel (Execution panel). They
// are an execution mechanism, not a planning one. Their progress is shown
// only as subagent blocks in chat with an inline checklist updated via
// step_todo_update events. The plan store is never touched here.

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isSubAgentLaunchData, isSubAgentCompleteData, isSubAgentPausedData } from '@/types/events'
import { useChatStore } from '@/stores/chatStore'
import { generateMessageId } from '@/lib/ids'
import type { SubAgentPausedData } from '@/types/events'

/**
 * subagent_paused handler — pure delegate runs only (plan-step subagents
 * surface as plan_step_paused via the backend translator). No plan-store
 * touch: delegated steps are not tracked in the plan panel. The durable chat
 * message flips the subagent block to 'paused' in the grouping replay.
 */
export function handleSubAgentPaused(sessionId: string, data: SubAgentPausedData): void {
  useChatStore.getState().setActivityStatus(sessionId, `Paused: sub-agent ${data.step_id}`)
  useChatStore.getState().addMessage(sessionId, {
    id: generateMessageId(),
    sessionId,
    type: 'subagent_paused',
    content: '',
    metadata: { step_id: data.step_id, duration: data.duration },
    timestamp: Date.now(),
  })
}

export function useSubagentEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // --- subagent_launch ---
    cleanups.push(
      onSessionEvent(sessionId, 'subagent_launch', (data) => {
        if (!isSubAgentLaunchData(data)) { reportDroppedEvent('subagent_launch', data); return }
        useChatStore.getState().setActivityStatus(sessionId, 'Launching sub-agent...')
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
      }),
    )

    // --- subagent_paused ---
    cleanups.push(
      onSessionEvent(sessionId, 'subagent_paused', (data) => {
        if (!isSubAgentPausedData(data)) { reportDroppedEvent('subagent_paused', data); return }
        handleSubAgentPaused(sessionId, data)
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
