// Plan lifecycle events: plan_generated, plan_step_start, plan_step_complete

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isPlanData, isPlanStepStartData, isPlanStepCompleteData, isStepTodoUpdateData } from '@/types/events'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import { generateMessageId } from '@/lib/ids'
import type { PlanItem, PlanGroup } from '@/types/models'
import type { PlanData, PlanStepData } from '@/types/events'

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

/**
 * plan_generated handler, module-level so it is directly testable against the
 * real stores (same pattern as hitlHandlers / handleContextFill).
 */
export function handlePlanGenerated(sessionId: string, data: PlanData): void {
  useChatStore.getState().setActivityStatus(sessionId, 'Executing plan...')
  // Plan step ids (step_1, ...) are reused by every new plan in the same
  // session, and fills must survive session switches (A→B→A) — so a new
  // plan is the invalidation point: drop the previous plan's fill
  // badges here, before the new steps execute and re-emit context_fill.
  // Without this, a step whose fill has not been re-reported yet would
  // briefly show the previous plan's percentage.
  useChatStore.getState().clearStepContextFill(sessionId)
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
}

export function usePlanEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    cleanups.push(
      onSessionEvent(sessionId, 'plan_generated', (data) => {
        if (!isPlanData(data)) { reportDroppedEvent('plan_generated', data); return }
        handlePlanGenerated(sessionId, data)
      }),
    )

    cleanups.push(
      onSessionEvent(sessionId, 'plan_step_start', (data) => {
        if (!isPlanStepStartData(data)) { reportDroppedEvent('plan_step_start', data); return }
        useChatStore.getState().setActivityStatus(sessionId, `Executing step ${data.step_id}...`)
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
        // Use upsertChecklistMessage (not addMessage): the Conductor emits a
        // checklist update after every tool call, so adding a fresh row each
        // time would grow the message list without bound. Collapsing to one
        // row per step_id keeps the store bounded; groupMessages additionally
        // collapses root-level checklists across different step_ids to a
        // single card (one active checklist per chat level).
        useChatStore.getState().upsertChecklistMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'step_todo_update',
          content: '',
          metadata: {
            step_id: data.step_id ?? '',
            items: data.items.map((item) => ({ text: item.text, checked: item.checked })),
          },
          timestamp: Date.now(),
        })
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
