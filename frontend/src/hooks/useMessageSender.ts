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
  send: (messageText: string, activeSkills?: string[]) => Promise<void>
  /** Cancel the running task in the active session. */
  cancel: () => Promise<void>
  /** True while the send/create-session RPC is in flight. */
  isProcessing: boolean
}

export function useMessageSender(): UseMessageSenderResult {
  const [isProcessing, setIsProcessing] = useState(false)

  const send = useCallback(async (messageText: string, activeSkills?: string[]) => {
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

    useChatStore.getState().addMessage(sessionId, {
      id: generateMessageId(),
      sessionId,
      type: 'user',
      content: messageText,
      timestamp: Date.now(),
    })

    useSessionStore.getState().touchSession(sessionId)
    useChatStore.getState().setTaskActive(sessionId, true)
    useChatStore.getState().setActivityStatus(sessionId, 'Processing...')

    try {
      const modelOverride = useInputModeStore.getState().selectedModel ?? ''
      const reasoningOverride = useInputModeStore.getState().selectedReasoning ?? ''
      const goalEnabled = useInputModeStore.getState().goalEnabled
      const goalBudget = useInputModeStore.getState().goalBudget
      await sendMessage(sessionId, messageText, activeSkills ?? [], modelOverride, reasoningOverride, goalEnabled, goalBudget)
      // Goal mode is first-message-only: once the goal-defining message is
      // sent, the toggle no longer applies (continuation messages ignore the
      // flag). Reset it so the user doesn't see a stale "on" toggle and so a
      // later normal send to a NEW session doesn't silently re-enter goal mode.
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
