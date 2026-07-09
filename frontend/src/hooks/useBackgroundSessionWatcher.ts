// Background session completion watcher.
//
// When two sessions run in parallel, only the active session has live event
// listeners (via useChatEvents). If a background session completes while
// another session is open in the chat, its task_complete / task_cancelled /
// error events are emitted by the backend and persisted to SQLite, but no
// frontend listener catches them — taskActive[bgSessionId] stays true and
// the send button renders as a red "stop" when the user eventually switches
// to that session.
//
// This hook subscribes to the three terminal events for EVERY session with
// taskActive === true that is NOT the active session, and resets the flag in
// real time. The final answer and intermediate history are already persisted
// by the backend's EventPersister and will be loaded from the DB by the
// reconcile effect in ChatArea when the user switches to the session.
//
// Additionally, this hook subscribes to HITL events (tool_confirm, step_limit,
// plan_review_ready, ask_user) for background sessions. Without this, a
// background session that blocks on a HITL prompt would have its event
// silently dropped (Wails EventsEmit with no listener is a no-op), leaving
// the agent goroutine blocked forever and the user with no UI to respond.
// The shared handlers in hitlHandlers.ts add the pending-action message to
// the chat store so it renders (and sinks to the bottom) in the chat stream
// when the user switches to the session.

import { useEffect } from 'react'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isToolConfirmData, isAskUserData, isStepLimitData, isPlanReviewReadyData } from '@/types/events'
import { handleToolConfirmEvent, handleAskUserEvent, handleStepLimitEvent, handlePlanReviewEvent } from './events/hitlHandlers'

/**
 * Watch all running background sessions for completion and HITL events.
 *
 * For each session with `taskActive === true` that is not the active session,
 * subscribes to `task_complete`, `task_cancelled`, and `error` (resetting
 * taskActive) and to `tool_confirm`, `step_limit`, `plan_review_ready`,
 * `ask_user` (adding the pending-action message to the chat store so the
 * user can respond even when viewing a different session).
 *
 * Called once at the app level (App.tsx) — not per session.
 */
export function useBackgroundSessionWatcher(): void {
  const taskActive = useChatStore(s => s.taskActive)
  const activeSessionId = useSessionStore(s => s.activeSessionId)

  // Compute a stable string key from the set of background running sessions.
  // This prevents the effect from re-running on every taskActive mutation
  // when the set of watched sessions hasn't actually changed.
  const watchedKey = Object.keys(taskActive)
    .filter(id => taskActive[id] === true && id !== activeSessionId)
    .sort()
    .join('\n')

  useEffect(() => {
    const sessionIds = watchedKey ? watchedKey.split('\n') : []
    if (sessionIds.length === 0) return

    const cleanups: Array<() => void> = []

    for (const sessionId of sessionIds) {
      // Finalize THIS background session's ephemeral UI state on a terminal
      // event. Because streaming/activity are now keyed per session, clearing
      // them here only affects the completing session — it cannot contaminate
      // the currently-viewed active session. This mirrors what the
      // active-session terminal handler (useChatEvents) does and prevents a
      // stale partial stream from lingering when the user later switches back
      // to a session that finished in the background. The final answer is
      // persisted by the backend and loaded by the reconcile effect on switch.
      const handleCompletion = (): void => {
        const store = useChatStore.getState()
        store.setTaskActive(sessionId, false)
        store.clearStreamingText(sessionId)
        store.setActivityStatus(sessionId, null)
      }

      cleanups.push(
        onSessionEvent(sessionId, 'task_complete', () => handleCompletion()),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'task_cancelled', () => handleCompletion()),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'error', () => handleCompletion()),
      )

      // HITL events — the agent goroutine blocks until the user responds.
      // Without these listeners the event is lost and the session hangs.
      cleanups.push(
        onSessionEvent(sessionId, 'tool_confirm', (data) => {
          if (!isToolConfirmData(data)) { reportDroppedEvent('tool_confirm', data); return }
          handleToolConfirmEvent(sessionId, data)
        }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'ask_user', (data) => {
          if (!isAskUserData(data)) { reportDroppedEvent('ask_user', data); return }
          handleAskUserEvent(sessionId, data)
        }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'step_limit', (data) => {
          if (!isStepLimitData(data)) { reportDroppedEvent('step_limit', data); return }
          handleStepLimitEvent(sessionId, data)
        }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'plan_review_ready', (data) => {
          if (!isPlanReviewReadyData(data)) { reportDroppedEvent('plan_review_ready', data); return }
          handlePlanReviewEvent(sessionId, data)
        }),
      )
    }

    return () => cleanups.forEach(fn => fn())
  }, [watchedKey])
}
