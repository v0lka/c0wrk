import { useCallback, useEffect, useRef, useState } from 'react'

import { resumeTask } from '@/api/chat'
import { logger } from '@/lib/logger'
import { useChatStore } from '@/stores/chatStore'

/**
 * Auto-resend countdown for the resume banner (`task_failed_resumable`).
 *
 * The countdown is purely UI-owned (ADR-065): when a task fails on a
 * retryable provider error (rate limit / overload) and the provider has a
 * configured `auto_retry_seconds` interval, the backend stamps the deadline
 * as `auto_retry_at` (unix seconds) into the event payload. The LIVE event
 * handler copies it into the banner metadata together with
 * `auto_retry_live: true` — the only discriminator a countdown may ever run
 * on. A 1s `setInterval` recomputes the remaining seconds; on zero the hook
 * fires `resumeTask` exactly once (the ordinary guarded backend resume
 * path). No backend timer exists.
 *
 * Arming rules:
 *  - ONLY a live-marked banner (auto_retry_live === true) with a future
 *    deadline at mount ever arms the ticker. A banner restored from the DB
 *    (history reload / app restart) carries the raw payload WITHOUT the
 *    live flag and always renders the plain manual banner — after a
 *    restart there is no timer to keep; the user decides manually.
 *  - ANY manual click (Resume or Cancel) stops the countdown immediately
 *    via `stop()` — the optimistic stop does not wait for the RPC
 *    round-trip; the manual flow proceeds on its own.
 *
 * Failure handling: when the auto fire's resumeTask rejects (or the session
 * is busy — the backend refuses), the live keys are stripped from the
 * banner metadata so the panel falls back to the plain manual banner
 * instead of a permanently disabled dead end. A REJECTED MANUAL RESUME
 * degrades the same way: the catch in handleResume reverts to the original
 * metadata with the live keys stripped, so the unmount/remount round-trip
 * of the optimistic 'resumed' marking cannot resurrect the countdown (the
 * one-shot disarm is instance state and dies with the unmount).
 */

export interface AutoRetryCountdown {
  /** True while the deadline is in the future and the ticker runs. */
  readonly counting: boolean
  /** Whole seconds left; meaningful only while `counting`. */
  readonly secondsLeft: number
  /** True once the deadline was reached while mounted — the auto-resend
   *  fired and the manual Resume affordance yields to it. */
  readonly autoResending: boolean
  /** Stops the countdown and disarms the auto fire. Called by BOTH manual
   *  click handlers (Resume / Cancel) before their own flow proceeds. */
  readonly stop: () => void
}

/** Read and validate the live auto-resend deadline from banner message
 *  metadata. Returns the unix-seconds deadline, or null when the banner is
 *  not live-marked (restored row, already stripped, or never armed). */
export function readAutoRetryAt(metadata: Record<string, unknown> | undefined): number | null {
  if (metadata?.auto_retry_live !== true) return null
  const v = metadata.auto_retry_at
  return typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : null
}

/** Strip the live auto-resend keys from a banner's metadata, producing the
 *  plain manual-banner metadata. Used after a failed auto fire AND after a
 *  rejected manual resume (the reverted metadata must not re-arm the
 *  countdown — see the failure-handling note below). The store's
 *  updateMessage SHALLOW-MERGES metadata, so key removal must be expressed
 *  as explicit undefined overwrites (they drop out of JSON serialization;
 *  readAutoRetryAt treats any non-true as absent). */
export function stripLiveKeys(metadata: Record<string, unknown>): Record<string, unknown> {
  return { ...metadata, auto_retry_at: undefined, auto_retry_live: undefined }
}

export function useAutoRetryCountdown(
  pending: boolean,
  sessionId: string,
  messageId: string,
  autoRetryAt: number | null,
): AutoRetryCountdown {
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000))
  // Whether the ticker was ever armed for this deadline — distinguishes
  // "deadline reached while mounted" (autoResending) from "never armed"
  // (restored banner / past-at-mount deadline → plain banner).
  const [ticking, setTicking] = useState(false)
  // One-shot fire guard: resumeTask is called at most once per deadline.
  const firedRef = useRef(false)

  // Fire the auto-resend exactly once when the deadline is reached while
  // the panel is mounted. Plain async — the countdown UI (autoResending
  // state) already communicates in-flight-ness; a rejection strips the
  // live keys so the banner falls back to manual.
  const fire = useCallback(async () => {
    if (firedRef.current) return
    firedRef.current = true
    try {
      await resumeTask(sessionId)
    } catch (err) {
      logger.warn('Auto-resend failed; falling back to the manual resume banner', { sessionId, error: String(err) })
      const store = useChatStore.getState()
      const m = store.messages[sessionId]?.[messageId]
      if (m && m.metadata?.auto_retry_live === true) {
        store.updateMessage(sessionId, messageId, { metadata: stripLiveKeys(m.metadata) })
      }
    }
  }, [sessionId, messageId])

  useEffect(() => {
    if (!pending || autoRetryAt === null) return
    // A deadline already in the past at mount NEVER arms the ticker: this
    // covers both a restored banner (no live flag → readAutoRetryAt already
    // returned null) and a deadline that expired between the event and the
    // mount. The plain manual banner renders.
    if (autoRetryAt <= Math.floor(Date.now() / 1000)) return
    setTicking(true)
    const id = window.setInterval(() => {
      const now = Math.floor(Date.now() / 1000)
      setNowSec(now)
      if (now >= autoRetryAt) {
        window.clearInterval(id)
        void fire()
      }
    }, 1000)
    return () => window.clearInterval(id)
  }, [pending, autoRetryAt, fire])

  // The optimistic manual-stop: flips ticking off and disarms the one-shot
  // fire. Idempotent; safe to call from both click handlers on every path
  // (armed or not).
  const stop = useCallback(() => {
    setTicking(false)
    firedRef.current = true
  }, [])

  if (!ticking || autoRetryAt === null) {
    return { counting: false, secondsLeft: 0, autoResending: false, stop }
  }
  const secondsLeft = Math.max(0, autoRetryAt - nowSec)
  return {
    counting: secondsLeft > 0,
    secondsLeft,
    autoResending: secondsLeft === 0,
    stop,
  }
}
