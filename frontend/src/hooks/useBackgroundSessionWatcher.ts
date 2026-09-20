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
// session that is running, pausing, or paused — whether that state is known
// live to chatStore OR reported as unfinished (in_progress / paused) by the
// authoritative backend snapshot (activeSessionsStore.sessions). The snapshot
// path is what makes a webview reload safe: chatStore's live maps start empty,
// but the DB still says a task is in flight, so the completion is announced
// instead of being silently dropped. It keeps the keyed UI state in sync in
// real time. The final answer and intermediate history are already
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
import { useActiveSessionsStore } from '@/stores/activeSessionsStore'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isToolConfirmData, isAskUserData, isStepLimitData, isPlanReviewReadyData, isGoalProposalData } from '@/types/events'
import type { SessionEventKey } from '@/types/events'
import { handleToolConfirmEvent, handleAskUserEvent, handleStepLimitEvent, handlePlanReviewEvent } from './events/hitlHandlers'
import { handleGoalProposalEvent } from './events/goalHandlers'
import { handleSessionPausedEvent, handleSessionResumedEvent } from './events/sessionLifecycleHandlers'
import { classifySessionEvent, notifySessionCue } from './events/useSoundEvents'
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
 * Announce a background-session event through BOTH cues, if any.
 *
 * Reuses the single event→sound mapping (`classifySessionEvent`) that the
 * active-session hook uses, so a background session produces the same tone the
 * user would hear had they been viewing it, and routes the notification through
 * the same shared `notifySessionCue` dispatch — identical coverage for the
 * tone and the banner, with that session's id as context so the banner title
 * names the session. A no-op when the master toggles are off or the platform
 * lacks audio/notifications (see `lib/sound.ts` / `lib/systemNotifications.ts`).
 */
function announceBackgroundCue(sessionId: string, event: SessionEventKey, data: unknown): void {
  playBackgroundCue(event, data)
  notifySessionCue(event, data, { sessionId })
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
 * The watched set is the union of chatStore's live flags and the sessions the
 * backend snapshot still reports unfinished (in_progress / paused), so a task
 * whose start this frontend never observed — after a reload, or in a project
 * the user has not opened — is still announced on completion.
 *
 * Cue parity: every watched event also plays the tone AND raises the system
 * notification the active session would produce (`classifySessionEvent` /
 * `notifySessionCue`), so a task that finishes or blocks on HITL in the
 * background is still announced both ways. There is no double-send risk: this
 * hook excludes the active session, whose cues are owned by `useSoundEvents`.
 *
 * Called once at the app level (App.tsx) — not per session.
 */
export function useBackgroundSessionWatcher(): void {
  const taskActive = useChatStore(s => s.taskActive)
  const paused = useChatStore(s => s.paused)
  const pausing = useChatStore(s => s.pausing)
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  // Authoritative DB snapshot (loaded at the App root by useActiveSessionsRefresh).
  // A direct store-field reference — changes only on a real RPC refresh, so it
  // is a stable selector value (React #185).
  const sessions = useActiveSessionsStore(s => s.sessions)

  // Watch background sessions throughout the running → pausing → paused
  // lifecycle. Keeping paused sessions subscribed is necessary for a later
  // session_resumed event to reach the store even though taskActive is false.
  //
  // The chatStore maps are the LIVE signal, but they are blind spots: a webview
  // reload empties them, and they only ever know sessions this frontend has
  // followed in its lifetime. The backend snapshot fills the gap — any session
  // it still reports unfinished (in_progress / paused) is watched too, so its
  // task_complete / HITL events are caught even though chatStore never saw it
  // start. `failed` is intentionally excluded: nothing is running to announce.
  const snapshotWatchedIds = new Set<string>()
  if (sessions) {
    for (const session of sessions) {
      if (session.unfinished_task_status === 'in_progress' || session.unfinished_task_status === 'paused') {
        snapshotWatchedIds.add(session.id)
      }
    }
  }

  const watchedIds = new Set([
    ...Object.keys(taskActive),
    ...Object.keys(paused),
    ...Object.keys(pausing),
    ...snapshotWatchedIds,
  ])
  const watchedKey = [...watchedIds]
    .filter(
      id =>
        (taskActive[id] === true || paused[id] === true || pausing[id] === true || snapshotWatchedIds.has(id)) &&
        id !== activeSessionId,
    )
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
        // Mirror the active-session terminal handler (useChatEvents): clear the
        // single live unfinished-task overlay so every status surface (sidebar
        // list, radar badge, radar dropdown) repaints idle live and the busy
        // check releases the session — no switch-back reconcile needed. A
        // failure that stays resumable is followed by the task_failed_resumable
        // subscription below, which re-arms the overlay as 'failed'.
        useChatStore.getState().setUnfinishedTaskStatus(sessionId, '')
      }

      cleanups.push(
        onSessionEvent(sessionId, 'task_failed_resumable', (data) => {
          // Degraded background completion: the task stays resumable — re-arm
          // the SINGLE live unfinished-task overlay as 'failed' so every status
          // surface repaints red live (mirrors useActionEvents' active-session
          // handling; the resume banner itself is rebuilt by the switch-back
          // history load + runtime reconcile). The cue (tone + banner) is sent
          // unconditionally: this event has no payload guard (any payload, even
          // a malformed one, still means the task stopped resumable).
          announceBackgroundCue(sessionId, 'task_failed_resumable', data)
          useChatStore.getState().setUnfinishedTaskStatus(sessionId, 'failed')
        }),
      )

      cleanups.push(
        onSessionEvent(sessionId, 'task_complete', (data) => { announceBackgroundCue(sessionId, 'task_complete', data); handleCompletion() }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'task_cancelled', (data) => { announceBackgroundCue(sessionId, 'task_cancelled', data); handleCompletion() }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'error', (data) => { announceBackgroundCue(sessionId, 'error', data); handleCompletion() }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'session_paused', () => {
          // Suppressed during manual compaction (the flow's own pause): the
          // background session must not surface paused affordances.
          if (useChatStore.getState().compacting[sessionId]) return
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
      // BOTH cues (tone and banner) are sent only after the payload validates
      // so a dropped (malformed) event neither beeps nor raises a banner
      // without anything for the user to act on.
      cleanups.push(
        onSessionEvent(sessionId, 'tool_confirm', (data) => {
          if (!isToolConfirmData(data)) { reportDroppedEvent('tool_confirm', data); return }
          announceBackgroundCue(sessionId, 'tool_confirm', data)
          handleToolConfirmEvent(sessionId, data)
        }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'ask_user', (data) => {
          if (!isAskUserData(data)) { reportDroppedEvent('ask_user', data); return }
          announceBackgroundCue(sessionId, 'ask_user', data)
          handleAskUserEvent(sessionId, data)
        }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'step_limit', (data) => {
          if (!isStepLimitData(data)) { reportDroppedEvent('step_limit', data); return }
          announceBackgroundCue(sessionId, 'step_limit', data)
          handleStepLimitEvent(sessionId, data)
        }),
      )
      cleanups.push(
        onSessionEvent(sessionId, 'plan_review_ready', (data) => {
          if (!isPlanReviewReadyData(data)) { reportDroppedEvent('plan_review_ready', data); return }
          announceBackgroundCue(sessionId, 'plan_review_ready', data)
          handlePlanReviewEvent(sessionId, data)
        }),
      )
      // Goal proposal — the derivation agent blocks on propose_goal until the
      // user confirms/cancels. Without this listener a background session that
      // hits a proposal would lose the event and hang (HITL parity).
      cleanups.push(
        onSessionEvent(sessionId, 'goal_proposal', (data) => {
          if (!isGoalProposalData(data)) { reportDroppedEvent('goal_proposal', data); return }
          announceBackgroundCue(sessionId, 'goal_proposal', data)
          handleGoalProposalEvent(sessionId, data)
        }),
      )
    }

    return () => cleanups.forEach(fn => fn())
  }, [watchedKey])
}
