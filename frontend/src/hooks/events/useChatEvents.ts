// Chat stream & response events: assistant_chunk, assistant_done, thought, error,
// task_complete, task_cancelled, reflection

import { useEffect } from 'react'
import { onSessionEvent } from '@/api/runtime'
import { isAssistantChunkData, isThoughtData, isErrorData, isTaskCompleteData } from '@/types/events'
import { useChatStore } from '@/stores/chatStore'
import { generateMessageId } from '@/lib/ids'

export function useChatEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // --- assistant_chunk ---
    cleanups.push(
      onSessionEvent(sessionId, 'assistant_chunk', (data) => {
        if (!isAssistantChunkData(data)) return
        const store = useChatStore.getState()
        store.setActivityStatus('Generating response...')
        if (data.accumulated_content !== undefined) {
          store.setStreamingText(data.accumulated_content, sessionId)
        } else if (data.content) {
          // Ensure streaming session is set before appending
          if (!store.streamingSessionId) {
            store.setStreamingText(data.content, sessionId)
          } else {
            store.appendStreamingText(data.content)
          }
        }
      }),
    )

    // --- assistant_done ---
    cleanups.push(
      onSessionEvent(sessionId, 'assistant_done', () => {
        const store = useChatStore.getState()
        const text = store.streamingText
        if (text) {
          store.addMessage(sessionId, {
            id: generateMessageId(),
            sessionId,
            type: 'assistant',
            content: text,
            timestamp: Date.now(),
          })
          store.clearStreamingText()
        }
      }),
    )

    // --- thought ---
    cleanups.push(
      onSessionEvent(sessionId, 'thought', (data) => {
        if (!isThoughtData(data)) return
        useChatStore.getState().setActivityStatus('Reasoning...')
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'thought',
          content: data.content,
          metadata: { step_num: data.step_num, reasoning: data.reasoning, plan_step_id: data.plan_step_id },
          timestamp: Date.now(),
        })
      }),
    )

    // --- error ---
    cleanups.push(
      onSessionEvent(sessionId, 'error', (data) => {
        if (!isErrorData(data)) return
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'error',
          content: data.error || 'An error occurred',
          timestamp: Date.now(),
        })
        const store = useChatStore.getState()
        store.clearStreamingText()
        store.setActivityStatus(null)
        store.setTaskActive(sessionId, false)
      }),
    )

    // --- task_complete ---
    cleanups.push(
      onSessionEvent(sessionId, 'task_complete', (data) => {
        if (!isTaskCompleteData(data)) return
        const store = useChatStore.getState()
        store.clearStreamingText()
        store.setActivityStatus(null)
        store.setTaskActive(sessionId, false)
        if (data.output) {
          store.addMessage(sessionId, {
            id: generateMessageId(),
            sessionId,
            type: 'assistant',
            content: data.output,
            timestamp: Date.now(),
          })
        }
      }),
    )

    // --- task_cancelled ---
    cleanups.push(
      onSessionEvent(sessionId, 'task_cancelled', () => {
        const store = useChatStore.getState()
        store.clearStreamingText()
        store.setActivityStatus(null)
        store.setTaskActive(sessionId, false)
        store.addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'error',
          content: 'Task was cancelled',
          timestamp: Date.now(),
        })
      }),
    )

    // --- reflection ---
    cleanups.push(
      onSessionEvent(sessionId, 'reflection', () => {
        useChatStore.getState().setActivityStatus('Reflecting on results...')
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
