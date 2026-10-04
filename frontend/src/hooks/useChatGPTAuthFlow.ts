import { useCallback, useEffect, useRef, useState } from 'react'
import {
  cancelChatGPTSignIn,
  getChatGPTAuthStatus,
  onChatGPTAuthState,
  signOutChatGPT,
  startChatGPTSignIn,
} from '@/api/auth'
import { logger } from '@/lib/logger'
import type { ChatGPTAuthStatusResponse } from '@/types/models'

/**
 * The ChatGPT oauth sign-in flow state for the settings panel: the live
 * status snapshot, the sign-in / sign-out busy flags, and the last
 * actionable auth failure.
 *
 * Flow progress arrives through the global `chatgpt_auth:state` event —
 * pending → success | error | cancelled — because the sign-in itself
 * completes in the user's browser, long after the start RPC returned. The
 * status RPC is the source of truth for the identity and the in-flight
 * flag; every read is epoch-guarded (see statusEpoch) so a slower EARLIER
 * read — one whose server-side in_flight was captured before a transition —
 * can never land after a newer one and fight the flow's own events for the
 * busy flag.
 */
export function useChatGPTAuthFlow() {
  const [status, setStatus] = useState<ChatGPTAuthStatusResponse | null>(null)
  /** A sign-in flow is awaiting its terminal event. Set by the Sign in
   *  click and cleared by success/error/cancelled — never by a timeout,
   *  because the user may sit in the browser arbitrarily long. */
  const [signInBusy, setSignInBusy] = useState(false)
  /** True from the local Sign-in click until the flow's terminal event:
   *  while a locally started flow runs, a stale status snapshot (one whose
   *  in_flight was read BEFORE the click) must not clear the busy flag —
   *  only the flow's own events resolve it. */
  const signInInitiatedLocally = useRef(false)
  const [signOutBusy, setSignOutBusy] = useState(false)
  /** The last actionable auth failure (a refused start, a failed flow, a
   *  failed sign-out). Cleared when a new attempt starts or succeeds. */
  const [authError, setAuthError] = useState<string | null>(null)
  /**
   * Monotonic refresh epoch: only the most recently STARTED status read may
   * apply its snapshot. Without it, two overlapping reads can resolve out
   * of order — a stale in_flight=false read that started before a
   * remount's restore would extinguish the restored busy flag, and a stale
   * in_flight=true read resolving after the flow's terminal event would
   * wedge the panel in Waiting with no terminal event left to clear it
   * (Cancel could not recover: there is no flow to cancel).
   */
  const statusEpoch = useRef(0)

  const refreshStatus = useCallback(async () => {
    const epoch = ++statusEpoch.current
    try {
      const snapshot = await getChatGPTAuthStatus()
      if (epoch !== statusEpoch.current) return // superseded by a newer read
      setStatus(snapshot)
    } catch (err) {
      // The wrapper logged the failure; keep the last snapshot — the panel
      // is more useful showing a stale identity than an empty flash.
      logger.warn('ChatGPT auth status refresh failed:', err)
    }
  }, [])

  // Snapshot once on mount (cheap read-only getter). The mode may flip
  // without a remount, and the signed-in identity is worth showing the
  // moment the user switches to oauth, so the fetch is not mode-gated.
  // The snapshot's in_flight is the AUTHORITATIVE busy flag: a remount
  // (collapsed accordion, reopened settings) loses the local signInBusy
  // state and may have missed the `pending` event, but the status read
  // restores the Waiting/Cancel posture for a still-running flow.
  useEffect(() => {
    void refreshStatus()
  }, [refreshStatus])

  // The sign-in flow's terminal transitions. The pending transition only
  // tightens the busy flag (the authorization URL is opened by the
  // startChatGPTSignIn wrapper); success/error/cancelled clear it and
  // re-read the authoritative snapshot — the event payload's identity
  // fields are advisory, the status RPC is the source of truth.
  useEffect(() => {
    return onChatGPTAuthState((data) => {
      switch (data.state) {
        case 'pending':
          setSignInBusy(true)
          break
        case 'success':
          signInInitiatedLocally.current = false
          setSignInBusy(false)
          setAuthError(null)
          void refreshStatus()
          break
        case 'error':
          signInInitiatedLocally.current = false
          setSignInBusy(false)
          setAuthError(data.error || 'Sign-in failed')
          void refreshStatus()
          break
        case 'cancelled':
          signInInitiatedLocally.current = false
          setSignInBusy(false)
          void refreshStatus()
          break
      }
    })
  }, [refreshStatus])

  // The status snapshot drives the busy flag, not just the identity: a
  // remount mid-flow restores Waiting/Cancel from in_flight, and a flow
  // that concluded elsewhere (another panel instance) clears it. Skipped
  // while a locally started flow runs — its snapshot was taken before the
  // click and would otherwise race the click's own busy=true with a stale
  // in_flight=false; the flow's terminal events resolve that run.
  useEffect(() => {
    if (status === null || signInInitiatedLocally.current) return
    setSignInBusy(status.in_flight)
  }, [status])

  const handleSignIn = useCallback(async () => {
    setAuthError(null)
    signInInitiatedLocally.current = true
    setSignInBusy(true)
    try {
      // The wrapper opens the system browser with the returned auth_url;
      // completion arrives via chatgpt_auth:state.
      await startChatGPTSignIn()
    } catch (err) {
      signInInitiatedLocally.current = false
      setSignInBusy(false)
      setAuthError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  const handleCancel = useCallback(async () => {
    try {
      await cancelChatGPTSignIn()
      // The quiet cancelled event clears the busy flag.
    } catch (err) {
      // The cancel itself failed — no cancelled event will arrive, so
      // release the local waiting posture HERE or the panel stays wedged
      // in Waiting. The authoritative re-read below restores the posture
      // when a flow is genuinely still running (its in_flight=true
      // snapshot re-arms the busy flag); when nothing runs, the panel is
      // simply usable again.
      signInInitiatedLocally.current = false
      setSignInBusy(false)
      setAuthError(err instanceof Error ? err.message : String(err))
      void refreshStatus()
    }
  }, [refreshStatus])

  const handleSignOut = useCallback(async () => {
    setAuthError(null)
    setSignOutBusy(true)
    try {
      await signOutChatGPT()
    } catch (err) {
      setAuthError(err instanceof Error ? err.message : String(err))
    } finally {
      setSignOutBusy(false)
      void refreshStatus()
    }
  }, [refreshStatus])

  return { status, signInBusy, signOutBusy, authError, handleSignIn, handleCancel, handleSignOut }
}
