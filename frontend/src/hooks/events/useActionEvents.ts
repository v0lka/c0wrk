// Action events: ask_user, step_limit, task_failed_resumable, task_resumed

import { useEffect } from 'react'
import { onSessionEvent } from '@/api/runtime'
import { isAskUserData, isStepLimitData, isTaskFailedResumableData } from '@/types/events'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { generateMessageId } from '@/lib/ids'

export function useActionEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // --- ask_user ---
    cleanups.push(
      onSessionEvent(sessionId, 'ask_user', (data) => {
        if (!isAskUserData(data)) return
        useChatStore.getState().addMessage(sessionId, {
          id: `ask-user-${data.request_id}`,
          sessionId,
          type: 'ask_user',
          content: data.questions.map(q => q.question).join('; '),
          metadata: {
            request_id: data.request_id,
            questions: data.questions,
          } as Record<string, unknown>,
          timestamp: Date.now(),
        })
        useChatStore.getState().setActivityStatus('Waiting for your answer...')
      }),
    )

    // --- step_limit ---
    cleanups.push(
      onSessionEvent(sessionId, 'step_limit', (data) => {
        if (!isStepLimitData(data)) return
        useChatStore.getState().addMessage(sessionId, {
          id: `step-limit-${data.request_id}`,
          sessionId,
          type: 'step_limit',
          content: data.reason
            ? `Circuit breaker: ${data.reason}`
            : `Step limit reached: ${data.current_step} of ${data.max_steps}`,
          metadata: {
            request_id: data.request_id,
            current_step: data.current_step,
            max_steps: data.max_steps,
            reason: data.reason,
          } as Record<string, unknown>,
          timestamp: Date.now(),
        })
        useChatStore.getState().setActivityStatus(
          data.reason
            ? 'Circuit breaker triggered — awaiting decision...'
            : 'Step limit reached — awaiting decision...',
        )
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

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
