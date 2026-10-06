// Encapsulates the message send flow: session creation, optimistic UI message,
// backend RPC, and error handling.  Keeps getState() calls in a single place.

import { useCallback, useState } from 'react'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore } from '@/stores/chatStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useE2SStore } from '@/stores/e2sStore'
import { useAttachmentsStore, EMPTY_ATTACHMENTS } from '@/stores/attachmentsStore'
import { buildUserMessageMeta } from '@/lib/userMessageMeta'
import { isE2SSendEnabled } from '@/lib/e2sGate'
import { sendMessage, cancelTask } from '@/api/chat'
import { createSessionFromDraft } from '@/lib/sessionDraft'
import { generateMessageId } from '@/lib/ids'
import { logger } from '@/lib/logger'

export interface SendOptions {
  /** Force a fresh session: create + activate a new session and dispatch the
   *  message there instead of the active/origin one (e.g. the research
   *  panel's Shift-click gesture). */
  newSession?: boolean
}

export interface UseMessageSenderResult {
  /** Send a user message, auto-creating a session if needed.
   *  originSessionId, when provided (non-null), pins the send to the session
   *  the user initiated it from — the caller may have awaited between the
   *  send gesture and this call (e.g. the #agent catalog fetch), and the
   *  then-active session would be the WRONG target. `options.newSession`
   *  overrides the pin and always starts a brand-new session. */
  send: (
    messageText: string,
    activeSkills?: string[],
    activeAgents?: string[],
    activeMCPServers?: string[],
    originSessionId?: string | null,
    options?: SendOptions,
  ) => Promise<void>
  /** Cancel the running task in the active session. */
  cancel: () => Promise<void>
  /** True while the send/create-session RPC is in flight. */
  isProcessing: boolean
}

export function useMessageSender(): UseMessageSenderResult {
  const [isProcessing, setIsProcessing] = useState(false)

  const send = useCallback(async (
    messageText: string,
    activeSkills?: string[],
    activeAgents?: string[],
    activeMCPServers?: string[],
    originSessionId?: string | null,
    options?: SendOptions,
  ) => {
    if (!messageText.trim()) return
    setIsProcessing(true)

    // The pinned origin session wins; only an explicitly null origin (the
    // no-session scratch space) falls through to the auto-create flow —
    // unless the caller forced a fresh session (research Shift-click). Both
    // paths honor an armed session draft: the first real action commits it,
    // creating the session on the project checkout (local) or provisioning
    // its managed worktree (branch draft) via CreateManagedSession.
    let sessionId = originSessionId ?? useSessionStore.getState().activeSessionId
    if (options?.newSession || !sessionId) {
      try {
        const newSession = await createSessionFromDraft()
        sessionId = newSession.id
      } catch (error) {
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
    // Snapshot the live unfinished-task overlay BEFORE the optimistic activation
    // below. setTaskActive(true) supersedes a stale DB 'failed'/'paused' snapshot
    // by pinning the overlay to '' (a running session has no unfinished task) —
    // correct for a CONFIRMED activation, but a REJECTED send must put the old
    // value back. Without this the overlay stays '' (which outranks the DB
    // snapshot), so an unfinished session renders idle and silently loses its
    // busy guard (Fork enablement / archive-delete confirmation).
    const prevUnfinished = useChatStore.getState().unfinishedTaskStatus[sessionId]

    // Optimistic metadata mirroring the SNAKE_CASE blob the backend persists
    // via PendingMessageMetadata: the goal flag, the staged attachments
    // (document summaries + image records), and the nudge marker. Snapshot
    // THIS session's pending list BEFORE the send RPC — the backend's send-clear
    // "attachments:changed" event empties the session's slice only after this
    // message is already rendered with its goal/attachment badges, so the
    // indicators no longer wait for a session/project switch to appear.
    const goalEnabled = useInputModeStore.getState().goalEnabled
    // E2S is mutually exclusive with goal (the store's setters enforce it), so
    // at most one of the two flags is armed here. `isE2SSendEnabled` is the
    // single fail-closed gate: it composes the experimental availability gate
    // with the armed toggle so a stale persisted `true` can never arm a send
    // the backend would reject.
    const e2sEnabled = isE2SSendEnabled()
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
      await sendMessage(sessionId, messageText, activeSkills ?? [], activeAgents ?? [], activeMCPServers ?? [], modelOverride, reasoningOverride, goalEnabled, goalBudget, e2sEnabled)
      // A confirmed fresh non-E2S task supersedes any prior E2S run in this
      // session: drop the stale Σ snapshot so the Execution State panel does
      // not shadow the plan view for the new task (E2S is selected per
      // message, not per session). Cleared AFTER a successful send so a
      // rejected send leaves the previous snapshot intact; a live interjection
      // into a running task or a nudge-resume leaves it untouched.
      if (!e2sEnabled && !wasPaused && !isRunning) {
        useE2SStore.getState().clearSession(sessionId)
      }
      // Goal is per-task opt-in: after a goal-defining message is sent, reset
      // the toggle so the user explicitly re-enables it for the next goal
      // (rather than silently staying in goal mode across every subsequent
      // send). Goal is still available on continuations — a re-enabled toggle
      // runs the goal loop on the inherited blackboard of the prior task.
      if (goalEnabled) {
        useInputModeStore.getState().setGoalEnabled(false)
        useInputModeStore.getState().setGoalBudget('')
      }
      // E2S mirrors goal's per-task opt-in: an E2S-defining message disarms
      // the toggle after the send, so the next message runs the default flow
      // unless the user re-arms it (the persisted preference only bridges
      // reloads of an armed-but-unsent toggle).
      if (e2sEnabled) {
        useInputModeStore.getState().setE2sEnabled(false)
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
      // Restore the unfinished-task overlay the optimistic activation pinned to
      // '' — only when this send actually activated the session
      // (`wasPaused || !isRunning`). A failed LIVE interjection never activated
      // the session, so its overlay is already correct. `prevUnfinished` may be
      // `undefined` (chatStore held no live knowledge): passing it through
      // DELETES the key so the DB snapshot again drives the status, rather than
      // fabricating a defined '' that would mask a real unfinished task.
      if (wasPaused || !isRunning) {
        useChatStore.getState().setUnfinishedTaskStatus(sessionId, prevUnfinished)
      }
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
