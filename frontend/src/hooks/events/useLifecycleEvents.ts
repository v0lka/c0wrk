// Lifecycle events: routing, step_start, step_complete, retry, step_retry,
// service, finishing, session_renamed

import { useEffect } from 'react'
import { onSessionEvent } from '@/api/runtime'
import {
  isRoutingData, isStepData, isRetryData, isStepRetryData,
  isServiceData, isSessionRenamedData,
} from '@/types/events'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import { useSessionStore } from '@/stores/sessionStore'
import { generateMessageId } from '@/lib/ids'

export function useLifecycleEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // --- routing ---
    cleanups.push(
      onSessionEvent(sessionId, 'routing', (data) => {
        if (!isRoutingData(data)) return
        useChatStore.getState().setActivityStatus('Analyzing request...')
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
        if (!isStepData(data)) return
        useChatStore.getState().setActivityStatus('Thinking...')
        useChatStore.getState().addMessage(sessionId, {
          id: `step-${data.step_num}`,
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
        if (!isStepData(data)) return
        useChatStore.getState().updateMessage(sessionId, `step-${data.step_num}`, {
          type: 'step_done',
        })
      }),
    )

    // --- retry ---
    cleanups.push(
      onSessionEvent(sessionId, 'retry', (data) => {
        if (!isRetryData(data)) return
        useChatStore.getState().setActivityStatus(`Retrying (attempt ${data.attempt}/${data.max_attempts})...`)
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
        if (!isStepRetryData(data)) return
        useChatStore.getState().setActivityStatus(`Retrying step ${data.attempt}/${data.max_attempts}...`)
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
        if (!isServiceData(data)) return
        if (data.content) {
          useChatStore.getState().setActivityStatus(data.content)
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
        useChatStore.getState().setActivityStatus('Finishing...')
      }),
    )

    // --- session_renamed ---
    cleanups.push(
      onSessionEvent(sessionId, 'session_renamed', (data) => {
        if (!isSessionRenamedData(data)) return
        useSessionStore.getState().updateSession(sessionId, { name: data.new_name })
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
