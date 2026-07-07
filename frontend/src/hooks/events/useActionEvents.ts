// Action events: ask_user, step_limit, task_failed_resumable, task_resumed

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isAskUserData, isStepLimitData, isTaskFailedResumableData, isPlanReviewReadyData } from '@/types/events'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { generateMessageId } from '@/lib/ids'
import { handleAskUserEvent, handleStepLimitEvent, handlePlanReviewEvent } from './hitlHandlers'

export function useActionEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // --- ask_user ---
    cleanups.push(
      onSessionEvent(sessionId, 'ask_user', (data) => {
        if (!isAskUserData(data)) { reportDroppedEvent('ask_user', data); return }
        handleAskUserEvent(sessionId, data)
      }),
    )

    // --- step_limit ---
    cleanups.push(
      onSessionEvent(sessionId, 'step_limit', (data) => {
        if (!isStepLimitData(data)) { reportDroppedEvent('step_limit', data); return }
        handleStepLimitEvent(sessionId, data)
      }),
    )

    // --- task_failed_resumable ---
    cleanups.push(
      onSessionEvent(sessionId, 'task_failed_resumable', (data) => {
        const msg = isTaskFailedResumableData(data) && data.message
          ? data.message
          : 'Plan execution failed.'
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'task_failed_resumable',
          content: msg,
          metadata: { resolved: false },
          timestamp: Date.now(),
        })
        useChatStore.getState().setActivityStatus(null)
        useChatStore.getState().setTaskActive(sessionId, false)
      }),
    )

    // --- task_resumed ---
    cleanups.push(
      onSessionEvent(sessionId, 'task_resumed', () => {
        // Resolve the latest task_failed_resumable message
        const store = useChatStore.getState()
        const msgs = selectSessionMessages(store, sessionId)
        for (let i = msgs.length - 1; i >= 0; i--) {
          const m = msgs[i]!
          if (m.type === 'task_failed_resumable' && m.metadata?.resolved !== true) {
            store.updateMessage(sessionId, m.id, { metadata: { ...m.metadata, resolved: true } })
            break
          }
        }
        store.setTaskActive(sessionId, true)
        store.setActivityStatus('Resuming...')
      }),
    )

    // --- plan_review_ready (declare_plan await_approval) ---
    cleanups.push(
      onSessionEvent(sessionId, 'plan_review_ready', (data) => {
        if (!isPlanReviewReadyData(data)) { reportDroppedEvent('plan_review_ready', data); return }
        handlePlanReviewEvent(sessionId, data)
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
