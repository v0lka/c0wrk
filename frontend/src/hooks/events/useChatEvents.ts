// Chat stream & response events: assistant_chunk, assistant_done, thought, error,
// task_complete, task_cancelled, reflection

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isAssistantChunkData, isThoughtData, isErrorData, isTaskCompleteData, isReflectionData } from '@/types/events'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { generateMessageId } from '@/lib/ids'
import type { ChatMessageUI } from '@/types/messages'

/**
 * Decide whether a task_complete.output should be added as a new assistant
 * message. In the implicit text-only finish path the executor streams the
 * answer via assistant_done (already flushed to a permanent assistant message)
 * AND sets it as Output, so task_complete would otherwise add a duplicate —
 * rendering the final answer twice. Returns false (skip) when the output
 * matches the most recent assistant message scoped after the last user message.
 *
 * Pure + exported so it is unit-testable without the Wails runtime.
 */
export function shouldAddTaskCompleteOutput(messages: ChatMessageUI[], output: string): boolean {
  if (!output) return false
  let lastAssistant: ChatMessageUI | undefined
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i]!
    if (m.type === 'user') break
    if (m.type === 'assistant') { lastAssistant = m; break }
  }
  return !lastAssistant || lastAssistant.content !== output
}

export function useChatEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // --- assistant_chunk ---
    cleanups.push(
      onSessionEvent(sessionId, 'assistant_chunk', (data) => {
        if (!isAssistantChunkData(data)) { reportDroppedEvent('assistant_chunk', data); return }
        const store = useChatStore.getState()
        store.setActivityStatus(sessionId, 'Generating response...')
        if (data.accumulated_content !== undefined) {
          store.setStreamingText(sessionId, data.accumulated_content)
        } else if (data.content) {
          // Ensure streaming session is set before appending
          if (!store.streamingText[sessionId]) {
            store.setStreamingText(sessionId, data.content)
          } else {
            store.appendStreamingText(sessionId, data.content)
          }
        }
      }),
    )

    // --- assistant_done ---
    cleanups.push(
      onSessionEvent(sessionId, 'assistant_done', () => {
        const store = useChatStore.getState()
        const text = store.streamingText[sessionId]
        if (text) {
          store.addMessage(sessionId, {
            id: generateMessageId(),
            sessionId,
            type: 'assistant',
            content: text,
            timestamp: Date.now(),
          })
          store.clearStreamingText(sessionId)
        }
      }),
    )

    // --- thought ---
    cleanups.push(
      onSessionEvent(sessionId, 'thought', (data) => {
        if (!isThoughtData(data)) { reportDroppedEvent('thought', data); return }
        useChatStore.getState().setActivityStatus(sessionId, 'Reasoning...')
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
        if (!isErrorData(data)) { reportDroppedEvent('error', data); return }
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'error',
          content: data.error || 'An error occurred',
          timestamp: Date.now(),
        })
        const store = useChatStore.getState()
        store.clearStreamingText(sessionId)
        store.setActivityStatus(sessionId, null)
        store.setTaskActive(sessionId, false)
      }),
    )

    // --- task_complete ---
    cleanups.push(
      onSessionEvent(sessionId, 'task_complete', (data) => {
        if (!isTaskCompleteData(data)) { reportDroppedEvent('task_complete', data); return }
        const store = useChatStore.getState()
        store.clearStreamingText(sessionId)
        store.setActivityStatus(sessionId, null)
        store.setTaskActive(sessionId, false)
        if (data.output) {
          // Dedup: in the implicit text-only finish path the executor streams
          // the answer via assistant_done (already flushed to a permanent
          // assistant message) AND sets it as Output, so task_complete would
          // otherwise add a duplicate assistant message — rendering the final
          // answer twice. Skip when the output matches the most recent
          // assistant message (scoped after the last user message).
          // Imperative read (not a reactive selector) — safe despite the ⚠️
          // in selectSessionMessages, which only applies to useStore(selector).
          const msgs = selectSessionMessages(store, sessionId)
          if (shouldAddTaskCompleteOutput(msgs, data.output)) {
            store.addMessage(sessionId, {
              id: generateMessageId(),
              sessionId,
              type: 'assistant',
              content: data.output,
              timestamp: Date.now(),
            })
          }
        } else {
          // No output — add a "[Task completed]" placeholder to match the
          // backend persister, which persists the same placeholder for empty
          // output. Without this, an empty-output completion is invisible in
          // the live render but appears as "[Task completed]" after reload.
          store.addMessage(sessionId, {
            id: generateMessageId(),
            sessionId,
            type: 'assistant',
            content: '[Task completed]',
            timestamp: Date.now(),
          })
        }
        // Degraded completion (partial/failed/aborted): surface it explicitly
        // instead of letting the task end look like a clean success.
        if (data.success === false) {
          const failed = data.failed_steps
            ? ` (${data.failed_steps} step${data.failed_steps === 1 ? '' : 's'} failed)`
            : ''
          store.addMessage(sessionId, {
            id: generateMessageId(),
            sessionId,
            type: 'error',
            content: `Task finished with ${data.completion ?? 'incomplete'} execution${failed}. The output above may be incomplete.`,
            timestamp: Date.now(),
          })
        }
      }),
    )

    // --- task_cancelled ---
    cleanups.push(
      onSessionEvent(sessionId, 'task_cancelled', () => {
        const store = useChatStore.getState()
        store.clearStreamingText(sessionId)
        store.setActivityStatus(sessionId, null)
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
      onSessionEvent(sessionId, 'reflection', (data) => {
        if (!isReflectionData(data)) { reportDroppedEvent('reflection', data); return }
        useChatStore.getState().setActivityStatus(sessionId, 'Reflecting on results...')
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'reflection',
          content: data.summary,
          metadata: {
            summary: data.summary,
            insights: data.insights,
            suggested_action: data.suggested_action,
            root_cause: data.root_cause,
            failure_analysis: data.failure_analysis,
            action_plan: data.action_plan,
            reasoning: data.reasoning,
            attempt: data.attempt,
            max_attempts: data.max_attempts,
          },
          timestamp: Date.now(),
        })
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
