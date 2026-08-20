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
// This hook subscribes to lifecycle and terminal events for EVERY background
// session that is running, pausing, or paused, and keeps the keyed UI state in
// sync in real time. The final answer and intermediate history are already
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
import { isToolConfirmData, isAskUserData, isStepLimitData, isPlanReviewReadyData, isGoalProposalData } from '@/types/events'
import type { SessionEventKey } from '@/types/events'
import { handleToolConfirmEvent, handleAskUserEvent, handleStepLimitEvent, handlePlanReviewEvent } from './events/hitlHandlers'
import { handleGoalProposalEvent } from './events/goalHandlers'
import { handleSessionPausedEvent, handleSessionResumedEvent } from './events/sessionLifecycleHandlers'
import { classifySessionEvent } from './events/useSoundEvents'
import { playSound } from '@/lib/sound'

/**
 * Play the audible cue for a background-session event, if any.
 *
 * Reuses the single event→sound mapping (`classifySessionEvent`) that the
 * active-session hook uses, so a background session produces the same cue the
 * user would hear had they been viewing it. A no-op when the master toggle is
 * off or the Web Audio API is unavailable (see `lib/sound.ts`).
 */
function playBackgroundCue(event: SessionEventKey, data: unknown): void {
  const kind = classifySessionEvent(event, data)
  if (kind) playSound(kind)
}

/**
 * Watch all live background sessions for lifecycle, completion, and HITL events.
 *
 * For each background session that is running, pausing, or paused, subscribes
 * to terminal and pause/resume lifecycle events, and to
 * `tool_confirm`, `step_limit`, `plan_review_ready`, `ask_user` (adding the
 * pending-action message to the chat store so the user can respond even when
 * viewing a different session).
 *
 * Sound parity: every watched event also plays the audible cue the active
 * session would play (`classifySessionEvent` → `playSound`), so a task that
 * finishes or blocks on HITL in the background is still announced. There is
 * no double-play risk: this hook excludes the active session, whose cues are
 * owned by `useSoundEvents`.
 *
 * Called once at the app level (App.tsx) — not per session.
 */
export function useBackgroundSessionWatcher(): void {
  const taskActive = useChatStore(s => s.taskActive)
  const paused = useChatStore(s => s.paused)
  const pausing = useChatStore(s => s.pausing)
  const activeSessionId = useSessionStore(s => s.activeSessionId)

  // Watch background sessions throughout the running → pausing → paused
  // lifecycle. Keeping paused sessions subscribed is necessary for a later
  // session_resumed event to reach the store even though taskActive is false.
  const watchedIds = new Set([
    ...Object.keys(taskActive),
    ...Object.keys(paused),
    ...Object.keys(pausing),
  ])
  const watchedKey = [...watchedIds]
    .filter(id => (taskActive[id] === true || paused[id] === true || pausing[id] === true) && id !== activeSessionId)
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
        // A pause that was still in flight (user clicked Pause, then switched
        // sessions before the step boundary) is superseded by the terminal
        // event — clear its spinner flag.
        store.setPausing(sessionId, false)
      }

      cleanups.push(
        onSessionEvent(sessionId, 'task_complete', (data) => { playBackgroundCue('task_complete', data); handleCompletion() }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'task_cancelled', (data) => { playBackgroundCue('task_cancelled', data); handleCompletion() }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'error', (data) => { playBackgroundCue('error', data); handleCompletion() }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'session_paused', () => {
          handleSessionPausedEvent(sessionId)
        }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'session_resumed', () => {
          handleSessionResumedEvent(sessionId)
        }),
      )

      // HITL events — the agent goroutine blocks until the user responds.
      // Without these listeners the event is lost and the session hangs.
      // The cue is played only after the payload validates so a dropped
      // (malformed) event does not beep without anything for the user to act on.
      cleanups.push(
        onSessionEvent(sessionId, 'tool_confirm', (data) => {
          if (!isToolConfirmData(data)) { reportDroppedEvent('tool_confirm', data); return }
          playBackgroundCue('tool_confirm', data)
          handleToolConfirmEvent(sessionId, data)
        }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'ask_user', (data) => {
          if (!isAskUserData(data)) { reportDroppedEvent('ask_user', data); return }
          playBackgroundCue('ask_user', data)
          handleAskUserEvent(sessionId, data)
        }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'step_limit', (data) => {
          if (!isStepLimitData(data)) { reportDroppedEvent('step_limit', data); return }
          playBackgroundCue('step_limit', data)
          handleStepLimitEvent(sessionId, data)
        }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'plan_review_ready', (data) => {
          if (!isPlanReviewReadyData(data)) { reportDroppedEvent('plan_review_ready', data); return }
          playBackgroundCue('plan_review_ready', data)
          handlePlanReviewEvent(sessionId, data)
        }),
      )
      // Goal proposal — the derivation agent blocks on propose_goal until the
      // user confirms/cancels. Without this listener a background session that
      // hits a proposal would lose the event and hang (HITL parity).
      cleanups.push(
        onSessionEvent(sessionId, 'goal_proposal', (data) => {
          if (!isGoalProposalData(data)) { reportDroppedEvent('goal_proposal', data); return }
          playBackgroundCue('goal_proposal', data)
          handleGoalProposalEvent(sessionId, data)
        }),
      )
    }

    return () => cleanups.forEach(fn => fn())
  }, [watchedKey])
}
