import { useState, useCallback, useEffect, useRef } from 'react'
import { Copy, Check, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { clipboardSetText } from '@/api/runtime'

interface CopyIconButtonProps {
  /** Text copied to the clipboard on click. */
  text: string
  /** Accessible label / tooltip shown in the idle state. */
  label?: string
  className?: string
}

/** Which feedback state the button is flashing, if any. */
type Feedback = 'copied' | 'failed' | null

/**
 * CopyIconButton — icon-only copy-to-clipboard control.
 *
 * Renders just a `Copy` icon (`h-3.5 w-3.5`); the label lives only in
 * `title` / `aria-label` and flips to "Copied" / "Failed" for a short window
 * after a click (success flashes the `Check` icon in `text-success`, failure
 * the `X` icon in `text-destructive`). The write goes through the native
 * Wails runtime via `clipboardSetText` — NOT `navigator.clipboard`, which is
 * unreliable inside the Wails webview (see `clipboardSetText` in
 * @/api/runtime); a `false` verdict (or a rejection, folded in defensively)
 * is a failed copy and must never render the success state. Unlike
 * chat/CopyButton there is no visible text label and no Save morph — hover
 * reveal in chat messages is handled by the parent via `group` +
 * `group-hover:*` wrappers.
 */
export function CopyIconButton({ text, label = 'Copy', className }: CopyIconButtonProps) {
  const [feedback, setFeedback] = useState<Feedback>(null)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current)
    }
  }, [])

  const flashFeedback = useCallback((kind: Exclude<Feedback, null>) => {
    setFeedback(kind)
    if (timerRef.current) clearTimeout(timerRef.current)
    timerRef.current = setTimeout(() => setFeedback(null), 2000)
  }, [])

  const handleCopy = useCallback(() => {
    clipboardSetText(text)
      .then((ok) => {
        // `false` is a failed copy (see `clipboardSetText`) — it must never
        // render the success state.
        flashFeedback(ok ? 'copied' : 'failed')
      })
      .catch(() => {
        // `clipboardSetText` resolves instead of throwing; this is a safety
        // net — fail with the same visible signal rather than silently.
        flashFeedback('failed')
      })
  }, [text, flashFeedback])

  const currentLabel = feedback === 'copied' ? 'Copied' : feedback === 'failed' ? 'Failed' : label
  const Icon = feedback === 'failed' ? X : feedback !== null ? Check : Copy

  return (
    <button
      type="button"
      onClick={handleCopy}
      className={cn(
        'inline-flex items-center rounded-md p-1 text-muted-foreground hover:bg-muted/50 hover:text-foreground transition-colors',
        className,
      )}
      title={currentLabel}
      aria-label={currentLabel}
    >
      <Icon
        className={cn(
          'h-3.5 w-3.5',
          feedback === 'failed' ? 'text-destructive' : feedback !== null && 'text-success',
        )}
      />
    </button>
  )
}
