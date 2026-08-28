// Chat stream & response events: assistant_chunk, assistant_done, thought, error,
// task_complete, task_cancelled, reflection

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isAssistantChunkData, isThoughtData, isErrorData, isTaskCompleteData, isReflectionData } from '@/types/events'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useReviewStore } from '@/stores/reviewStore'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore, selectIsNoProject } from '@/stores/projectStore'
import * as reviewApi from '@/api/review'
import { generateMessageId } from '@/lib/ids'
import { refreshCompactionNoOp } from '@/lib/sessionRuntime'
import type { ChatMessageUI } from '@/types/messages'
import { handleSessionPausedEvent, handleSessionResumedEvent } from './sessionLifecycleHandlers'

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

/**
 * Whether a post-task review action (auto-review loop or first-time prompt)
 * should fire for this task_complete. Review is a CODE-mode-only feature: it
 * must never trigger in CHAT (No Project) mode, even if the git panel store
 * holds stale/leaked entries, and requires uncommitted working-tree changes.
 *
 * Pure + exported so it is unit-testable without the Wails runtime.
 */
export function shouldTriggerReview(isNoProject: boolean, hasChanges: boolean): boolean {
  if (isNoProject) return false
  return hasChanges
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
        store.setPausing(sessionId, false)
        // Terminal event: no unfinished task survives an errored run — the
        // session is no longer busy for the archive/delete confirmation (the
        // list's flag was a stale snapshot). A failure that stays resumable is
        // immediately followed by the backend's task_failed_resumable event,
        // which re-sets the flag (see useActionEvents).
        useSessionStore.getState().setUnfinishedTask(sessionId, false)
        // The failed exchange still grew the conversation history — refresh
        // the compaction no-op flag (the compact button's disabled state).
        refreshCompactionNoOp(sessionId)
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
        store.setPausing(sessionId, false)
        // The task settled: refresh the session list's stale `has_unfinished_task`
        // snapshot so isSessionBusy() stops reporting the session as busy
        // without an app restart. Degraded completions (success === false) are
        // still cleared here — the backend emits its task_failed_resumable
        // event right AFTER this one when the task stays resumable, and that
        // handler re-sets the flag (see useActionEvents).
        useSessionStore.getState().setUnfinishedTask(sessionId, false)
        // The completed exchange was appended to the conversation history
        // (the orchestrator records the outcome before this event fires), so
        // a previously-no-op session may be compactable again — refresh the
        // compaction no-op flag (the compact button's disabled state).
        refreshCompactionNoOp(sessionId)
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

        // --- Review triggers ---
        // Only on successful task_complete. Two modes:
        // 1) Auto-review loop: if reviewLoopActive[session] is set (user
        //    previously clicked Submit), reopen the review page with fresh diff.
        // 2) First-time prompt: if the project has git changes and the prompt
        //    hasn't been shown for this task turn, inject a review_prompt block.
        if (data.success !== false) {
          const reviewStore = useReviewStore.getState()
          const isLoopActive = !!reviewStore.reviewLoopActive[sessionId]
          const gitState = useGitPanelStore.getState()
          const hasChanges = gitState.isGitRepo && gitState.entries.length > 0
          // Review is a CODE-mode-only feature: never show the review prompt or
          // reopen the review page in CHAT (No Project) mode, even if the git
          // store holds stale/leaked entries.
          const isNoProject = selectIsNoProject(useProjectStore.getState())

          if (shouldTriggerReview(isNoProject, hasChanges)) {
            if (isLoopActive) {
              const fvStore = useFileViewerStore.getState()
              fvStore.openFile('c0wrk:review')
              fvStore.setCollapsed(false)
              reviewStore.openReviewPage(sessionId)
              void reviewStore.loadReview(sessionId)
            } else if (!reviewStore.promptShownForTask[sessionId]) {
              // Persist the prompt as a real session message so it survives a
              // session switch / restart (the old frontend-only message was
              // dropped by mergeHistoryMessages on the next history reload).
              // The id is derived from the backend-assigned prompt_id so the
              // live card and the reloaded history share an id and dedupe
              // cleanly. markPromptShown runs after the persist succeeds so a
              // persist failure leaves the prompt retryable on a later turn.
              void reviewApi.saveReviewPrompt(sessionId).then((prompt) => {
                reviewStore.markPromptShown(sessionId, 'task')
                store.addMessage(sessionId, {
                  id: `review-prompt-${prompt.prompt_id}`,
                  sessionId,
                  type: 'review_prompt',
                  content: prompt.content,
                  metadata: { prompt_id: prompt.prompt_id },
                  timestamp: Date.now(),
                })
              }).catch(() => { /* logged in the saveReviewPrompt wrapper */ })
            }
          }
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
        store.setPaused(sessionId, false)
        store.setPausing(sessionId, false)
        // A user-initiated cancel persists the task as cancelled in the task
        // store, so no unfinished task survives it — refresh the session
        // list's stale `has_unfinished_task` snapshot (busy check for
        // archive/delete) without waiting for an app restart.
        useSessionStore.getState().setUnfinishedTask(sessionId, false)
        // A cancelled task still records its exchange in the conversation
        // history (the orchestrator's cancellation epilogue appends the user
        // message + cancellation note), so a previously-no-op session may be
        // compactable again — refresh the compaction no-op flag (the compact
        // button's disabled state), mirroring task_complete / error.
        refreshCompactionNoOp(sessionId)
        store.addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'error',
          content: 'Task was cancelled',
          timestamp: Date.now(),
        })
      }),
    )

    // --- session_paused ---
    // A cooperative pause is a clean checkpoint: enter the paused UI state
    // (input unlocked, Resume/Stop). taskActive clears because the task is
    // no longer running — it is suspended, resumable via Resume or a nudge.
    // SUPPRESSED while a manual compaction is in flight: the compaction flow
    // pauses the running task itself, and the paused affordances (unlocked
    // input, Resume/Stop) must not appear — the session stays in the
    // "Compacting" state until compaction_finished, after which the flow
    // auto-resumes and session_resumed clears everything.
    cleanups.push(
      onSessionEvent(sessionId, 'session_paused', () => {
        const chat = useChatStore.getState()
        if (chat.compacting[sessionId]) return
        handleSessionPausedEvent(sessionId)
      }),
    )

    // --- session_resumed ---
    // Resume clears the paused state: input re-locks, Pause/Stop return.
    // Complementary to session_paused and task_resumed (which also fires).
    cleanups.push(
      onSessionEvent(sessionId, 'session_resumed', () => {
        handleSessionResumedEvent(sessionId)
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
