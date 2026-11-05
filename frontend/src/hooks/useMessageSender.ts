// Encapsulates the message send flow: session creation, optimistic UI message,
// backend RPC, and error handling.  Keeps getState() calls in a single place.

import { useCallback, useState } from 'react'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore } from '@/stores/chatStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useE2SStore } from '@/stores/e2sStore'
import { useAttachmentsStore, EMPTY_ATTACHMENTS } from '@/stores/attachmentsStore'
import { buildUserMessageMeta } from '@/lib/userMessageMeta'
import { detectGoalCommand, goalCommandShadowedByMention } from '@/lib/goalCommand'
import { isE2SSendEnabled } from '@/lib/e2sGate'
import { sendMessage, cancelTask } from '@/api/chat'
import { listSessions } from '@/api/sessions'
import { ensureActiveSession } from '@/lib/ensureActiveSession'
import { useSessionWorkspaceStore, pendingWorkspaceToWire } from '@/stores/sessionWorkspaceStore'
import { generateMessageId } from '@/lib/ids'
import { logger } from '@/lib/logger'
import type { SessionInfo } from '@/types/models'

/** The consumption-window refusal message (shared by the early guard and the
 *  beginConsumption backstop — one wording for both entry points). */
const WORKSPACE_BUSY_MESSAGE =
  'A workspace is already being prepared for this session — wait for the send in progress to settle'

/**
 * Reconcile one session row with the backend's authoritative store state.
 * Every send that carried a workspace choice refreshes its row (success AND
 * failure): the bind can provision a fresh tree, adopt an existing one, or
 * deliberately no-op (the checkout took the branch meanwhile) — and a failure
 * after a successful bind leaves the binding persisted anyway. Mirroring the
 * local guess would make the toolbar chip assert an outcome the backend did
 * not produce; the refreshed row is the fact.
 */
async function refreshSessionRow(sessionId: string): Promise<void> {
  try {
    const sessions = await listSessions()
    const fresh = sessions.find((s) => s.id === sessionId)
    if (!fresh) return
    const updates: Partial<SessionInfo> = fresh.workspace_binding !== undefined
      ? { last_active_at: fresh.last_active_at, workspace_binding: fresh.workspace_binding }
      : { last_active_at: fresh.last_active_at }
    useSessionStore.getState().updateSession(sessionId, updates)
  } catch (err) {
    logger.warn('Failed to refresh the session row after a workspace-carrying send:', err)
  }
}

export interface SendOptions {
  /** Force a fresh session: create + activate a new session and dispatch the
   *  message there instead of the active/origin one (e.g. the research
   *  panel's Shift-click gesture). */
  newSession?: boolean
  /** A custom command's frontmatter model binding (ADR-096): a FALLBACK
   *  override used only when the user has no explicit model selected in the
   *  toolbar — an explicit selection always wins. */
  commandModel?: string
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
    // unless the caller forced a fresh session (research Shift-click). The
    // fallback creates a plain LOCAL session exactly like CHAT mode's
    // auto-create (ADR-091): fast, never provisioning a worktree.
    let sessionId = originSessionId ?? useSessionStore.getState().activeSessionId
    if (options?.newSession || !sessionId) {
      try {
        const newSession = await ensureActiveSession()
        sessionId = newSession.id
      } catch (error) {
        setIsProcessing(false)
        throw error // let caller restore text
      }
    }

    // The fresh window's workspace choice rides on this message: a pending
    // branch choice (picked in the toolbar's workspace selector, or preset
    // by the per-branch gesture) makes THIS send provision the managed
    // worktree and bind the session before the task starts. A local choice
    // (or none) sends null — the session is already local. A choice only
    // exists on a fresh session, so continuations and nudges always send
    // null here.
    const pendingChoice = useSessionWorkspaceStore.getState().choices[sessionId]
    const workspace = pendingWorkspaceToWire(pendingChoice)

    // Re-entrancy guard (the consumption window): a send carrying a workspace
    // choice while ANOTHER send is consuming one would race the same fresh
    // window — a second concurrent bind of the same session, with the two
    // RPCs then both persisting their messages and dispatching tasks. The
    // window spans the whole provisioning (seconds; a full checkout), so the
    // guard REFUSES with a thrown error — the caller's catch restores the
    // typed text and surfaces the reason via the dismissible sendError hint
    // (the same convention as the ambiguous-mention no-op), and a silent
    // text loss is impossible. The store's beginConsumption re-checks under
    // the same rule as the backstop.
    if (workspace !== null && useSessionWorkspaceStore.getState().consumingFor !== null) {
      setIsProcessing(false)
      throw new Error(WORKSPACE_BUSY_MESSAGE)
    }

    // The authoritative backstop takes the window BEFORE any optimistic
    // write: a refusal (a racing second send took the window between the
    // guard and this call) throws with no message card, no task overlay and
    // no consumed choice to undo — and lands OUTSIDE the try below, so it
    // propagates straight to the caller, whose catch restores the typed
    // text and surfaces the reason. No transcript error card here: the
    // first send is still in flight and owns the transcript.
    if (workspace !== null && !useSessionWorkspaceStore.getState().beginConsumption(sessionId)) {
      setIsProcessing(false)
      throw new Error(WORKSPACE_BUSY_MESSAGE)
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
    // Badge parity (optimistic side): a leading /goal / /goal:N prefix arms
    // goal mode on the backend without the toggle, so the optimistic card
    // carries the goal stamp too (detectGoalCommand mirrors the backend
    // grammar; a prefix exposed by stripping a /-mention is stamped by the
    // backend's persisted metadata and shows on reload). The ADR-106
    // shadowing configuration suppresses the mirror: when the leading plain
    // /goal token was partitioned as a mention ("goal" rides one of the
    // threaded arrays), the backend's mention strip consumes it and no goal
    // arms — stamping the card then would be a false positive the reload
    // silently removes.
    const metaGoal =
      goalEnabled ||
      (detectGoalCommand(messageText).activates &&
        !goalCommandShadowedByMention(messageText, activeSkills, activeAgents, activeMCPServers))
    // E2S is mutually exclusive with goal (the store's setters enforce it), so
    // at most one of the two flags is armed here. `isE2SSendEnabled` is the
    // single fail-closed gate: it composes the experimental availability gate
    // with the armed toggle so a stale persisted `true` can never arm a send
    // the backend would reject.
    const e2sEnabled = isE2SSendEnabled()
    const pendingAttachments =
      useAttachmentsStore.getState().attachmentsBySession[sessionId] ?? EMPTY_ATTACHMENTS
    const metadata = buildUserMessageMeta(metaGoal, pendingAttachments, wasPaused || isRunning)

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
      const modelOverride = useInputModeStore.getState().selectedModel || options?.commandModel || ''
      const reasoningOverride = useInputModeStore.getState().selectedReasoning ?? ''
      const goalBudget = useInputModeStore.getState().goalBudget
      try {
        await sendMessage(sessionId, messageText, activeSkills ?? [], activeAgents ?? [], activeMCPServers ?? [], modelOverride, reasoningOverride, goalEnabled, goalBudget, e2sEnabled, false, optimisticId, workspace)
      } finally {
        if (workspace !== null) {
          useSessionWorkspaceStore.getState().endConsumption()
        }
      }
      if (workspace !== null) {
        // The workspace choice was consumed: drop the pending slice and
        // reconcile the session row with the backend FACT (refreshSessionRow)
        // — never mirror a local guess of the binding outcome.
        useSessionWorkspaceStore.getState().clearChoice(sessionId)
        await refreshSessionRow(sessionId)
      }
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
      if (workspace !== null) {
        // The workspace choice itself: a bind refusal rejects the send before
        // ANY backend state changes (no activity bump, no message row, no
        // task), so the choice was not consumed — re-arm it next to the
        // restored task state, or a corrected resend would silently leave it
        // behind and start the task in the shared checkout. When the failure
        // happened AFTER a successful bind instead, repeating the same choice
        // is a no-op on the backend — and the fact refresh below mirrors the
        // already-persisted binding into the row either way. NEVER for a
        // session deleted mid-provisioning (its delete path already dropped
        // the choice slice): re-arming onto a dead id would strand a stale
        // entry the session can never consume.
        const sessionExists =
          useSessionStore.getState().sessions?.some((s) => s.id === sessionId) ?? false
        if (sessionExists && pendingChoice !== undefined) {
          useSessionWorkspaceStore.getState().setChoice(sessionId, pendingChoice)
        }
        void refreshSessionRow(sessionId)
      }
    } finally {
      // The window cannot outlive this TRY: whatever throws inside it — the
      // RPC, the success reconciliation, the failure teardown — still
      // settles here, idempotently when the inner finally already closed it.
      // The optimistic writes between the take and the try are synchronous
      // store writes with no throw path; keep them that way (a throw there
      // would escape both finallys and leak the window).
      if (workspace !== null) {
        useSessionWorkspaceStore.getState().endConsumption()
      }
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
