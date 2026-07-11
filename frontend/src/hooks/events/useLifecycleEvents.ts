// Lifecycle events: routing, step_start, step_complete, retry, step_retry,
// service, finishing, session_renamed

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import {
  isRoutingData, isStepData, isRetryData, isStepRetryData,
  isServiceData, isSkillsActivatedData,
} from '@/types/events'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import { generateMessageId } from '@/lib/ids'

export function useLifecycleEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // Track step-start message IDs so step_complete can update the correct one.
    const stepIdMap = new Map<number, string>()

    // --- routing ---
    cleanups.push(
      onSessionEvent(sessionId, 'routing', (data) => {
        if (!isRoutingData(data)) { reportDroppedEvent('routing', data); return }
        useChatStore.getState().setActivityStatus(sessionId, 'Analyzing request...')
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'routing',
          content: `Domain: ${data.domain} | Complexity: ${data.complexity}`,
          metadata: { domain: data.domain, complexity: data.complexity },
          timestamp: Date.now(),
        })
        usePlanStore.getState().setSessionStats(sessionId, {
          routingDomain: data.domain,
        })
      }),
    )

    // --- step_start ---
    cleanups.push(
      onSessionEvent(sessionId, 'step_start', (data) => {
        if (!isStepData(data)) { reportDroppedEvent('step_start', data); return }
        useChatStore.getState().setActivityStatus(sessionId, 'Thinking...')
        const id = generateMessageId()
        stepIdMap.set(data.step_num, id)
        useChatStore.getState().addMessage(sessionId, {
          id,
          sessionId,
          type: 'thinking',
          content: `Step ${data.step_num || ''}...`,
          timestamp: Date.now(),
        })
      }),
    )

    // --- step_complete ---
    cleanups.push(
      onSessionEvent(sessionId, 'step_complete', (data) => {
        if (!isStepData(data)) { reportDroppedEvent('step_complete', data); return }
        const msgId = stepIdMap.get(data.step_num)
        if (msgId) {
          useChatStore.getState().updateMessage(sessionId, msgId, {
            type: 'step_done',
          })
          stepIdMap.delete(data.step_num)
        }
      }),
    )

    // --- retry ---
    cleanups.push(
      onSessionEvent(sessionId, 'retry', (data) => {
        if (!isRetryData(data)) { reportDroppedEvent('retry', data); return }
        useChatStore.getState().setActivityStatus(sessionId, `Retrying (attempt ${data.attempt}/${data.max_attempts})...`)
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'routing',
          content: `Retry attempt ${data.attempt}/${data.max_attempts}`,
          metadata: { ...data },
          timestamp: Date.now(),
        })
        usePlanStore.getState().setSessionStats(sessionId, {
          attemptCount: data.attempt + 1,
          maxAttempts: data.max_attempts,
        })
      }),
    )

    // --- step_retry ---
    cleanups.push(
      onSessionEvent(sessionId, 'step_retry', (data) => {
        if (!isStepRetryData(data)) { reportDroppedEvent('step_retry', data); return }
        useChatStore.getState().setActivityStatus(sessionId, `Retrying step ${data.attempt}/${data.max_attempts}...`)
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'step_retry',
          content: `Retrying step ${data.attempt}/${data.max_attempts}...`,
          metadata: { step_id: data.step_id, attempt: data.attempt, max_attempts: data.max_attempts },
          timestamp: Date.now(),
        })
      }),
    )

    // --- service ---
    cleanups.push(
      onSessionEvent(sessionId, 'service', (data) => {
        if (!isServiceData(data)) { reportDroppedEvent('service', data); return }
        if (data.content) {
          useChatStore.getState().setActivityStatus(sessionId, data.content)
        }
        if (data.phase === 'orchestration' && data.content) {
          useChatStore.getState().addMessage(sessionId, {
            id: generateMessageId(),
            sessionId,
            type: 'status',
            content: data.content,
            timestamp: Date.now(),
          })
        }
      }),
    )

    // --- finishing ---
    cleanups.push(
      onSessionEvent(sessionId, 'finishing', () => {
        useChatStore.getState().setActivityStatus(sessionId, 'Finishing...')
      }),
    )

    // --- session_renamed ---
    // Handled globally in useSessionLoader via the `session:renamed` event so
    // that non-active sessions' titles update in the sidebar too. The
    // session-scoped event has no listener for non-active sessions, so a
    // per-session handler here would miss background auto-titling.

    // --- skills_activated ---
    cleanups.push(
      onSessionEvent(sessionId, 'skills_activated', (data) => {
        if (!isSkillsActivatedData(data)) { reportDroppedEvent('skills_activated', data); return }
        const skillList = data.skills.join(', ')
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'status',
          content: `Skills activated: ${skillList}`,
          metadata: { skills: data.skills },
          timestamp: Date.now(),
        })
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
