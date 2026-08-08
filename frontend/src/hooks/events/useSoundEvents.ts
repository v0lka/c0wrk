// Sound notification events.
//
// A dedicated, side-effect-free hook that subscribes to the session events the
// user wants audible cues for and plays the matching tone. Keeping this
// isolated in its own hook (rather than scattering playSound calls across
// useChatEvents / useActionEvents / useToolEvents) means the sound wiring is
// discoverable in one place and the event→sound mapping is a pure, unit-testable
// function. Subscribing to an event a second time is safe: Wails EventsOn
// supports multiple independent listeners, each with its own cleanup.

import { useEffect } from 'react'
import { onSessionEvent } from '@/api/runtime'
import type { SessionEventKey } from '@/types/events'
import { playSound, initSoundUnlock, type SoundKind } from '@/lib/sound'

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

export function useSoundEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    // Unlock the AudioContext on the first user gesture (desktop webviews start
    // suspended until a gesture occurs). Idempotent.
    initSoundUnlock()

    const events: SessionEventKey[] = [
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

    const cleanups = events.map((event) =>
      onSessionEvent(sessionId, event, (data) => {
        const kind = classifySessionEvent(event, data)
        if (kind) playSound(kind)
      }),
    )

    return () => cleanups.forEach((fn) => fn())
  }, [sessionId])
}
