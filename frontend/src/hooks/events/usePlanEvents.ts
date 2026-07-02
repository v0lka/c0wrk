// Plan lifecycle events: plan_generated, plan_step_start, plan_step_complete

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isPlanData, isPlanStepStartData, isPlanStepCompleteData, isStepTodoUpdateData } from '@/types/events'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import { generateMessageId } from '@/lib/ids'
import type { PlanItem, PlanGroup } from '@/types/models'
import type { PlanStepData } from '@/types/events'

function toPlanItem(step: PlanStepData, index: number): PlanItem {
  return {
    id: step.id ?? `step-${index}`,
    title: step.summary || step.description,
    description: step.description,
    summary: step.summary,
    status: (step.status as PlanItem['status']) ?? 'pending',
    dependsOn: step.depends_on ?? [],
  }
}

export function usePlanEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    cleanups.push(
      onSessionEvent(sessionId, 'plan_generated', (data) => {
        if (!isPlanData(data)) { reportDroppedEvent('plan_generated', data); return }
        useChatStore.getState().setActivityStatus('Executing plan...')
        if (data.steps) {
          const items = data.steps.map(toPlanItem)
          const group: PlanGroup = {
            id: generateMessageId(),
            items,
            progress: data.progress,
            completedCount: data.completed_count,
            totalCount: data.total_count,
          }
          usePlanStore.getState().setPlan(group)
        }
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'plan',
          content: '',
          metadata: {
            steps: data.steps,
            progress: data.progress,
            current_step_index: data.current_step_index,
            completed_count: data.completed_count,
            total_count: data.total_count,
          },
          timestamp: Date.now(),
        })
      }),
    )

    cleanups.push(
      onSessionEvent(sessionId, 'plan_step_start', (data) => {
        if (!isPlanStepStartData(data)) { reportDroppedEvent('plan_step_start', data); return }
        useChatStore.getState().setActivityStatus(`Executing step ${data.step_id}...`)
        usePlanStore.getState().updateStepStatus(data.step_id, 'running')
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'plan_step_start',
          content: data.description || '',
          metadata: { step_id: data.step_id, description: data.description, summary: data.summary },
          timestamp: Date.now(),
        })
      }),
    )

    cleanups.push(
      onSessionEvent(sessionId, 'plan_step_complete', (data) => {
        if (!isPlanStepCompleteData(data)) { reportDroppedEvent('plan_step_complete', data); return }
        usePlanStore.getState().updateStepStatus(
          data.step_id,
          data.success ? 'completed' : 'failed',
          data.duration,
        )
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'plan_step_complete',
          content: '',
          metadata: {
            step_id: data.step_id,
            success: data.success,
            duration: data.duration,
            ...(data.error ? { error: data.error } : {}),
          },
          timestamp: Date.now(),
        })
      }),
    )

    cleanups.push(
      onSessionEvent(sessionId, 'step_todo_update', (data) => {
        if (!isStepTodoUpdateData(data)) { reportDroppedEvent('step_todo_update', data); return }
        usePlanStore.getState().updateStepTodo(
          data.step_id,
          data.items.map((item) => ({ text: item.text, checked: item.checked })),
        )
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
