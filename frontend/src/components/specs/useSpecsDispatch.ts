// The one dispatch path shared by the quick-actions row and the empty
// state's Initialize CTA (ADR-105 D4: the panel never writes to specs/
// itself — every mutating gesture activates a `spec-*` skill).

import { useCallback } from 'react'
import { useMessageSender } from '@/hooks/useMessageSender'
import { useSpecsStore } from '@/stores/specsStore'
import type { SpecQuickAction } from './specsActions'

/**
 * Dispatch one quick action: send its constant prompt with the skill in
 * sendMessage's activeSkills slot. Shift-modified gestures go to a fresh
 * session (the research panel's Shift-click contract).
 *
 * send() renders sendMessage failures in-chat itself but RETHROWS when the
 * auto-created session fails (the splash race); the rejection lands on the
 * specs panel's own error banner (specsStore.setError — the research [22]a
 * contract), deliberately NOT a global toast, which could fire while the
 * user is typing into the chat input.
 */
export function useSpecsDispatch() {
  const { send } = useMessageSender()
  return useCallback(
    (action: SpecQuickAction, newSession: boolean) => {
      // Promise.resolve: tolerate a sender that returns void (defensive —
      // the real send is async, but mocks/type skew must not crash a click).
      void Promise.resolve(
        send(
          action.prompt,
          [action.skill],
          undefined,
          undefined,
          undefined,
          { newSession },
        ),
      ).catch((err) => {
        useSpecsStore.getState().setError(
          `Failed to dispatch ${action.skill}: ${
            err instanceof Error ? err.message : 'unknown error'
          }`,
        )
      })
    },
    [send],
  )
}
