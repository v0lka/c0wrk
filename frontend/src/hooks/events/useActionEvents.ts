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
        const valid = isTaskFailedResumableData(data)
        const msg = valid && data.message
          ? data.message
          : 'Plan execution failed.'
        // auto_retry_at (unix seconds) rides along when the backend stamped
        // an auto-resend deadline into the payload. The LIVE handler marks
        // it with auto_retry_live: true — the countdown may only ever run
        // for a banner created by a live event in THIS app run. Restored
        // rows (history reload) carry the raw persisted payload without the
        // flag and always render the plain manual banner: after a restart
        // there is no timer to keep (the deadline served the open session;
        // the user decides manually from then on).
        const autoRetryAt = valid && typeof data.auto_retry_at === 'number' && data.auto_retry_at > 0
          ? data.auto_retry_at
          : undefined
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'task_failed_resumable',
          content: msg,
          metadata: autoRetryAt !== undefined
            ? { resolved: false, auto_retry_at: autoRetryAt, auto_retry_live: true }
            : { resolved: false },
          timestamp: Date.now(),
        })
        useChatStore.getState().setActivityStatus(sessionId, null)
        useChatStore.getState().setTaskActive(sessionId, false)
        useChatStore.getState().setPausing(sessionId, false)
        // The failed task stays resumable: the backend emits this right AFTER
        // the terminal task_complete/error (which cleared the overlay) whenever
        // the task remains unfinished — re-arm the SINGLE live unfinished-task
        // overlay as 'failed' so every status dot repaints red live (sidebar
        // list, radar badge, radar dropdown) and the busy check protects it.
        useChatStore.getState().setUnfinishedTaskStatus(sessionId, 'failed')
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
        // A resume also clears the paused state (session_resumed fires too,
        // but this covers any resume path and keeps the two consistent).
        store.setPausing(sessionId, false)
        store.setPaused(sessionId, false)
        store.setTaskActive(sessionId, true)
        store.setActivityStatus(sessionId, 'Resuming...')
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
