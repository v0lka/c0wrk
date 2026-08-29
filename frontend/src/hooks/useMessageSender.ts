// Encapsulates the message send flow: session creation, optimistic UI message,
// backend RPC, and error handling.  Keeps getState() calls in a single place.

import { useCallback, useState } from 'react'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore } from '@/stores/chatStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useAttachmentsStore, EMPTY_ATTACHMENTS } from '@/stores/attachmentsStore'
import { buildUserMessageMeta } from '@/lib/userMessageMeta'
import { sendMessage, cancelTask } from '@/api/chat'
import { createSession } from '@/api/sessions'
import { generateMessageId } from '@/lib/ids'
import { logger } from '@/lib/logger'

interface UseMessageSenderResult {
  /** Send a user message, auto-creating a session if needed. */
  send: (messageText: string, activeSkills?: string[], activeAgents?: string[]) => Promise<void>
  /** Cancel the running task in the active session. */
  cancel: () => Promise<void>
  /** True while the send/create-session RPC is in flight. */
  isProcessing: boolean
}

export function useMessageSender(): UseMessageSenderResult {
  const [isProcessing, setIsProcessing] = useState(false)

  const send = useCallback(async (messageText: string, activeSkills?: string[], activeAgents?: string[]) => {
    if (!messageText.trim()) return
    setIsProcessing(true)

    let sessionId = useSessionStore.getState().activeSessionId
    if (!sessionId) {
      try {
        const newSession = await createSession()
        useSessionStore.getState().addSession(newSession)
        useSessionStore.getState().setActiveSessionId(newSession.id)
        sessionId = newSession.id
      } catch (error) {
        logger.error('Failed to create session:', error)
        setIsProcessing(false)
        throw error // let caller restore text
      }
    }

    // A message sent while a task is cooperatively paused is a nudge-resume:
    // the backend's SendMessage detects the paused task and routes to
    // ResumeSession (seeding the resumed turn with this text), persisting the
    // message with is_nudge metadata. We mirror that here optimistically so the
    // card renders the nudge badge immediately and the UI leaves the paused
    // state (input re-locks, Pause/Stop return) without waiting for the
    // session_resumed/task_resumed event.
    //
    // A message sent while a task is RUNNING is a live interjection: the
    // backend queues it into the running request (delivered at the next LLM
    // call) and also persists it with is_nudge. Same badge, different flow:
    // the UI stays in the running state — the task is untouched.
    const wasPaused = useChatStore.getState().paused[sessionId] ?? false
    const isRunning = useChatStore.getState().taskActive[sessionId] ?? false
    const wasActivity = useChatStore.getState().activityStatus[sessionId] ?? null

    // Optimistic metadata mirroring the SNAKE_CASE blob the backend persists
    // via PendingMessageMetadata: the goal flag, the staged attachments
    // (document summaries + image records), and the nudge marker. Snapshot
    // THIS session's pending list BEFORE the send RPC — the backend's send-clear
    // "attachments:changed" event empties the session's slice only after this
    // message is already rendered with its goal/attachment badges, so the
    // indicators no longer wait for a session/project switch to appear.
    const goalEnabled = useInputModeStore.getState().goalEnabled
    const pendingAttachments =
      useAttachmentsStore.getState().attachmentsBySession[sessionId] ?? EMPTY_ATTACHMENTS
    const metadata = buildUserMessageMeta(goalEnabled, pendingAttachments, wasPaused || isRunning)

    const optimisticId = generateMessageId()
    useChatStore.getState().addMessage(sessionId, {
      id: optimisticId,
      sessionId,
      type: 'user',
      content: messageText,
      metadata,
      timestamp: Date.now(),
    })

    useSessionStore.getState().touchSession(sessionId)
    if (wasPaused) {
      // Nudge-resume: optimistically leave the paused state (input re-locks,
      // Pause/Stop return). A live send keeps the running state as is.
      useChatStore.getState().setPaused(sessionId, false)
      useChatStore.getState().setTaskActive(sessionId, true)
      useChatStore.getState().setActivityStatus(sessionId, 'Processing...')
    }
    if (!wasPaused && !isRunning) {
      // Fresh task: mark active and show the activity label.
      useChatStore.getState().setTaskActive(sessionId, true)
      useChatStore.getState().setActivityStatus(sessionId, 'Processing...')
    }

    try {
      const modelOverride = useInputModeStore.getState().selectedModel ?? ''
      const reasoningOverride = useInputModeStore.getState().selectedReasoning ?? ''
      const goalBudget = useInputModeStore.getState().goalBudget
      await sendMessage(sessionId, messageText, activeSkills ?? [], activeAgents ?? [], modelOverride, reasoningOverride, goalEnabled, goalBudget)
      // Goal is per-task opt-in: after a goal-defining message is sent, reset
      // the toggle so the user explicitly re-enables it for the next goal
      // (rather than silently staying in goal mode across every subsequent
      // send). Goal is still available on continuations — a re-enabled toggle
      // runs the goal loop on the inherited blackboard of the prior task.
      if (goalEnabled) {
        useInputModeStore.getState().setGoalEnabled(false)
        useInputModeStore.getState().setGoalBudget('')
      }
    } catch (error) {
      logger.error('Failed to send message:', error)
      const errorMessage = error instanceof Error ? error.message : String(error)
      // Roll back the optimistic user message: a rejected send (e.g. a live
      // send into the pausing window, or a goal/skill/agent reference into a
      // running task) must not leave a phantom user card next to the error.
      useChatStore.getState().removeMessage(sessionId, optimisticId)
      useChatStore.getState().addMessage(sessionId, {
        id: generateMessageId(),
        sessionId,
        type: 'error',
        content: `Failed to send message: ${errorMessage}`,
        timestamp: Date.now(),
      })
      // A failed LIVE send leaves the running task untouched: the task is
      // still active (its events keep flowing), so only the optimistic user
      // message is rolled back — never the task-active state. Nudge-resume
      // and fresh-task sends restore the exact pre-send task state instead of
      // a partial revert: a failed nudge-resume must return the session to
      // the paused state (and its "Paused" label), and a failed fresh send
      // must clear the transient "Processing..." activity.
      useChatStore.getState().setTaskActive(sessionId, isRunning)
      useChatStore.getState().setPaused(sessionId, wasPaused)
      useChatStore.getState().setActivityStatus(sessionId, wasActivity)
    } finally {
      setIsProcessing(false)
    }
  }, [])

  const cancel = useCallback(async () => {
    const sessionId = useSessionStore.getState().activeSessionId
    if (!sessionId) return
    try {
      await cancelTask(sessionId)
    } catch (error) {
      logger.error('Failed to cancel task:', error)
    }
  }, [])

  return { send, cancel, isProcessing }
}
