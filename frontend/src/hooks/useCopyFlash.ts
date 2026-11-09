import { useState, useCallback, useEffect, useRef } from 'react'
import { clipboardSetText } from '@/api/runtime'

/** The feedback kinds every copy consumer shares. */
export type CopyFeedback = 'copied' | 'failed'

/** How long a feedback flash stays visible before reverting to idle. */
export const COPY_FLASH_MS = 2000

export interface CopyFlash<E extends string = never> {
  /** The currently flashed state, if any — null when idle. */
  feedback: CopyFeedback | E | null
  /** Copy `text` through the native Wails clipboard and flash the verdict. */
  copy: (text: string) => void
  /** Flash an arbitrary state for COPY_FLASH_MS (the Save path's kinds). */
  flash: (kind: CopyFeedback | E) => void
}

/**
 * useCopyFlash — the shared copy-to-clipboard flash contract (chat/CopyButton
 * and the specs CopyIconButton): the write goes through the native Wails
 * runtime via `clipboardSetText` — NOT `navigator.clipboard`, which is
 * unreliable inside the Wails webview (`navigator.clipboard` is undefined in
 * production because the webview origin is not a secure context, and even
 * when present `writeText` rejects with NotAllowedError under WKWebView's
 * transient-activation rules — that was the root cause of the message copy
 * buttons needing several clicks to register; see `clipboardSetText` in
 * @/api/runtime). The verdict flashes for COPY_FLASH_MS: a success flashes
 * `'copied'`; a `false` verdict (or a rejection, folded in defensively)
 * flashes `'failed'` and must NEVER render the success state. The revert
 * timer re-arms on every flash and is cleared on unmount, so it never fires
 * into a dead tree.
 *
 * Type parameter E extends the flash state with component-specific kinds —
 * chat/CopyButton's save path flashes `'saved'`/`'download-started'` through
 * the same timer via `flash`; the default (never) covers copy-only
 * consumers. The Web Access fields' flash (settings/useWebAccessCopyFlash) is
 * a DIFFERENT contract — no failure flash, a `runtime_error` toast instead —
 * and deliberately does not use this hook.
 */
export function useCopyFlash<E extends string = never>(): CopyFlash<E> {
  const [feedback, setFeedback] = useState<CopyFeedback | E | null>(null)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current)
    }
  }, [])

  const flash = useCallback((kind: CopyFeedback | E) => {
    setFeedback(kind)
    if (timerRef.current) clearTimeout(timerRef.current)
    timerRef.current = setTimeout(() => setFeedback(null), COPY_FLASH_MS)
  }, [])

  const copy = useCallback(
    (text: string) => {
      clipboardSetText(text)
        .then((ok) => {
          // `false` is a failed copy (see `clipboardSetText`) — it must never
          // render the success state.
          flash(ok ? 'copied' : 'failed')
        })
        .catch(() => {
          // `clipboardSetText` resolves instead of throwing; this is a safety
          // net — fail with the same visible signal rather than silently.
          flash('failed')
        })
    },
    [flash],
  )

  return { feedback, copy, flash }
}
