// Encapsulates the message send flow: session creation, optimistic UI message,
// backend RPC, and error handling.  Keeps getState() calls in a single place.

import { useCallback, useState } from 'react'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore } from '@/stores/chatStore'
import { useInputModeStore } from '@/stores/inputModeStore'
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
    const wasPaused = useChatStore.getState().paused[sessionId] ?? false

    useChatStore.getState().addMessage(sessionId, {
      id: generateMessageId(),
      sessionId,
      type: 'user',
      content: messageText,
      metadata: wasPaused ? { is_nudge: true } : undefined,
      timestamp: Date.now(),
    })

    useSessionStore.getState().touchSession(sessionId)
    useChatStore.getState().setTaskActive(sessionId, true)
    useChatStore.getState().setActivityStatus(sessionId, 'Processing...')
    if (wasPaused) {
      useChatStore.getState().setPaused(sessionId, false)
    }

    try {
      const modelOverride = useInputModeStore.getState().selectedModel ?? ''
      const reasoningOverride = useInputModeStore.getState().selectedReasoning ?? ''
      const goalEnabled = useInputModeStore.getState().goalEnabled
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
      useChatStore.getState().addMessage(sessionId, {
        id: generateMessageId(),
        sessionId,
        type: 'error',
        content: `Failed to send message: ${errorMessage}`,
        timestamp: Date.now(),
      })
      useChatStore.getState().setTaskActive(sessionId, false)
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
