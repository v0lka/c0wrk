// Session cue events: sound + system notification.
//
// A dedicated, side-effect-free hook that subscribes to the session events the
// user wants cues for and dispatches each one to BOTH sinks — the audible
// tone (playSound) and the native notification-center banner
// (sendSystemNotification). Keeping this isolated in its own hook (rather than
// scattering cue calls across useChatEvents / useActionEvents / useToolEvents)
// means the wiring is discoverable in one place and the event→cue mappings are
// pure, unit-testable functions. ONE subscription set feeds BOTH cues, so the
// tone and the banner can never drift apart in coverage. Subscribing to an
// event a second time is safe: Wails EventsOn supports multiple independent
// listeners, each with its own cleanup.
//
// NOTE: this hook does NOT set up the audio unlock (initSoundUnlock) or the
// OS notification bridge (initSystemNotifications). Both are registered once
// at App start (see App.tsx) so they exist even with no active session — a
// state in which this hook is a no-op and the suspended AudioContext would
// otherwise have no way back.

import { useEffect } from 'react'
import { onSessionEvent } from '@/api/runtime'
import type { SessionEventKey } from '@/types/events'
import { playSound, type SoundKind } from '@/lib/sound'
import {
  classifyNotificationContent,
  sendSystemNotification,
  type SessionNotificationContext,
} from '@/lib/systemNotifications'
import { useActiveSessionsStore } from '@/stores/activeSessionsStore'
import { useSessionStore } from '@/stores/sessionStore'

/**
 * Pure mapping from a session event to its sound category.
 *
 * Returns null for events that should stay silent. The disambiguation logic
 * (e.g. a successful vs failed task_complete) lives here so it can be tested
 * without the Wails runtime. The `data` argument is typed `unknown` on
 * purpose: callers (the hook) validate payloads with their own guards before
 * mutating store state, but for sound we only need one or two well-known
 * boolean/string fields, read defensively.
 */
export function classifySessionEvent(event: SessionEventKey, data: unknown): SoundKind | null {
  switch (event) {
    // --- success ---
    case 'task_complete': {
      // success === false means partial/failed/aborted — an error, not a win.
      const success = (data as { success?: boolean } | null | undefined)?.success
      return success === false ? 'error' : 'success'
    }

    // --- attention: any interactive prompt requiring the user ---
    case 'ask_user':
    case 'step_limit':
    case 'tool_confirm':
    case 'plan_review_ready':
    case 'task_failed_resumable':
    case 'goal_proposal':
      return 'attention'

    // --- error ---
    case 'error':
    case 'task_cancelled':
      return 'error'

    default:
      return null
  }
}

/** Fallback name when the session cannot be resolved from loaded snapshots. */
const NOTIFICATION_APP_NAME = 'c0wrk'

/** What a defensive snapshot read yields for one session id. */
interface SessionNotificationLabel {
  name: string
  projectId?: string
}

/**
 * Resolve the human-readable label of a session for a notification.
 *
 * Purely a defensive read of the already-loaded snapshots — NO subscription,
 * NO RPC: both stores are queried via getState() at notify time. The
 * cross-project snapshot (activeSessionsStore, loaded at the App root) is the
 * primary source since it covers background sessions in other projects; the
 * current project's sessionStore covers the freshly-loaded active list. An
 * unknown id (background cross-project session after a webview reload, before
 * the first snapshot lands) falls back to the app name.
 */
export function resolveSessionNotificationLabel(sessionId: string): SessionNotificationLabel {
  const snapshot = useActiveSessionsStore.getState().sessions
  const fromSnapshot = snapshot?.find((session) => session.id === sessionId)
  if (fromSnapshot?.name) return { name: fromSnapshot.name, projectId: fromSnapshot.project_id }
  const projectSessions = useSessionStore.getState().sessions
  const fromProject = projectSessions?.find((session) => session.id === sessionId)
  if (fromProject?.name) return { name: fromProject.name, projectId: fromProject.project_id }
  return { name: NOTIFICATION_APP_NAME }
}

/**
 * True when a banner about `sessionId` would be redundant RIGHT NOW: the app
 * window is focused AND the session the banner is about is the one on screen.
 * The Telegram "new message" rule — you are already looking at that chat, so
 * a banner carries no information. Defensive DOM/store reads only, no RPC.
 *
 * The TONE is deliberately not suppressed here: this gates the visual banner
 * only (see sendSystemNotification), so an audible cue still marks the event
 * while the user is focused elsewhere in the app (e.g. the terminal panel of
 * the same window).
 */
export function isSessionNotificationRedundant(sessionId: string): boolean {
  if (typeof document === 'undefined' || !document.hasFocus()) return false
  return useSessionStore.getState().activeSessionId === sessionId
}

/**
 * Send the system notification for a cued session event, if any.
 *
 * The notification counterpart of the sound path above: the same defensive
 * mapping (`classifyNotificationContent`), the same nine events. APPENDS the
 * session name after the event title (`Event — Session name`, the app name
 * when unresolvable) so a background banner leads with WHAT happened — the
 * scannable part — and still identifies which session raised it; threads the
 * resolved project id through for click routing. Suppressed when redundant
 * (focused window + that session on screen — see
 * isSessionNotificationRedundant). Best-effort: the send itself never throws
 * (see sendSystemNotification).
 */
export function notifySessionCue(event: SessionEventKey, data: unknown, context: SessionNotificationContext): void {
  const content = classifyNotificationContent(event, data)
  if (!content) return
  if (isSessionNotificationRedundant(context.sessionId)) return
  const label = resolveSessionNotificationLabel(context.sessionId)
  void sendSystemNotification(
    { ...content, title: `${content.title} — ${label.name}` },
    { sessionId: context.sessionId, projectId: label.projectId },
  )
}

/**
 * The single list of session events that carry a cue.
 *
 * The active-session hook below subscribes to exactly this list. The background
 * watcher subscribes to this list PLUS the non-cued lifecycle events
 * (session_paused / session_resumed) it must observe for state repair — its
 * cued subset is kept aligned with this list by the coverage integration test.
 */
export const CUED_SESSION_EVENTS: readonly SessionEventKey[] = [
  'task_complete',
  'task_cancelled',
  'error',
  'ask_user',
  'step_limit',
  'tool_confirm',
  'plan_review_ready',
  'task_failed_resumable',
  'goal_proposal',
]

export function useSoundEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups = CUED_SESSION_EVENTS.map((event) =>
      onSessionEvent(sessionId, event, (data) => {
        const kind = classifySessionEvent(event, data)
        if (kind) playSound(kind)
        notifySessionCue(event, data, { sessionId })
      }),
    )

    return () => cleanups.forEach((fn) => fn())
  }, [sessionId])
}
