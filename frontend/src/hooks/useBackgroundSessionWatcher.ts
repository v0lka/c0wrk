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

import { useEffect } from 'react'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { onSessionEvent } from '@/api/runtime'

/**
 * Watch all running background sessions for completion events.
 *
 * For each session with `taskActive === true` that is not the active session,
 * subscribes to `task_complete`, `task_cancelled`, and `error`. On any of
 * these, resets `taskActive` to false and clears transient UI state
 * (streaming text, activity status).
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
      // Reset the session's running flag and transient UI state. The final
      // answer is already persisted by the backend — it will appear when the
      // user switches to the session and the reconcile effect loads history.
      const handleCompletion = (): void => {
        const store = useChatStore.getState()
        store.setTaskActive(sessionId, false)
        store.setActivityStatus(null)
        store.clearStreamingText()
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
    }

    return () => cleanups.forEach(fn => fn())
  }, [watchedKey])
}
