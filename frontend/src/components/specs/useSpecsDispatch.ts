// The one send path of the Specs panel, reached only AFTER its preview gate:
// SpecsPanel parks every mutating gesture (quick-actions row, node actions,
// findings badge, empty state's CTA) in its dispatch dialog, which shows the
// framework-declared prompt verbatim and calls this hook on confirm — a
// repo-authored prompt never enters the chat unseen. The gesture activates a
// DECLARED skill of the active framework (the panel never writes to the tree
// itself).

import { useCallback } from 'react'
import { useMessageSender } from '@/hooks/useMessageSender'
import { useSpecsStore } from '@/stores/specsStore'
import type { SpecsDispatch } from './specsActions'

/**
 * Dispatch one ready-to-send gesture: send its prompt with the skill in
 * sendMessage's activeSkills slot. Shift-modified gestures go to a fresh
 * session (the research panel's Shift-click contract).
 *
 * send() renders sendMessage failures in-chat itself but RETHROWS when the
 * auto-created session fails (the splash race); the rejection lands on the
 * specs panel's own error banner (specsStore.setError — the research [22]a
 * contract), deliberately NOT a global toast, which could fire while the user
 * is typing into the chat input.
 */
export function useSpecsDispatch() {
  const { send } = useMessageSender()
  return useCallback(
    (dispatch: SpecsDispatch, newSession: boolean) => {
      // Promise.resolve: tolerate a sender that returns void (defensive — the
      // real send is async, but mocks/type skew must not crash a click).
      void Promise.resolve(
        send(
          dispatch.prompt,
          [dispatch.skill],
          undefined,
          undefined,
          undefined,
          { newSession },
        ),
      ).catch((err) => {
        useSpecsStore.getState().setError(
          `Failed to dispatch ${dispatch.skill}: ${
            err instanceof Error ? err.message : 'unknown error'
          }`,
        )
      })
    },
    [send],
  )
}
