import { useState, useCallback, useEffect, useRef } from 'react'
import { Copy, Check, Save } from 'lucide-react'
import { cn } from '@/lib/utils'
import { clipboardSetText, emit } from '@/api/runtime'
import { saveMessageAsMarkdown } from '@/api/files'
import { useShiftHeld } from '@/hooks/useShiftHeld'
import { logger } from '@/lib/logger'

interface CopyButtonProps {
  /** Text copied to the clipboard on click (or saved to a file on Shift+click). */
  text: string
  /** Accessible label / tooltip shown in the idle state. */
  label?: string
  className?: string
}

/** Which success state the button is flashing, if any. */
type Feedback = 'copied' | 'saved' | null

/**
 * CopyButton — small, low-contrast copy-to-clipboard control.
 *
 * Renders a `Copy` icon plus a text label that flips to a success `Check` +
 * "Copied" for a short window after a successful copy. While Shift is held
 * the button morphs into a `Save` action: clicking opens the native save-file
 * dialog (default directory = the active project) and writes the text to the
 * chosen .md file, flashing "Saved" on success. The button itself is always
 * visible; the hover reveal that shows/hides it in chat messages is handled
 * by the parent via a `group` + `group-hover:*` wrapper.
 */
export function CopyButton({ text, label = 'Copy', className }: CopyButtonProps) {
  const [feedback, setFeedback] = useState<Feedback>(null)
  const shiftHeld = useShiftHeld()
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
    // Copy through the native Wails runtime, NOT `navigator.clipboard`. The
    // Web Clipboard API is unreliable inside the Wails webview
    // (`navigator.clipboard` is undefined in production because the webview
    // origin is not a secure context, and even when present `writeText`
    // rejects with NotAllowedError under WKWebView's transient-activation
    // rules) — that was the root cause of the message copy buttons needing
    // several clicks to register. See `clipboardSetText` in @/api/runtime.
    clipboardSetText(text)
      .then(() => flashFeedback('copied'))
      .catch(() => {
        // Clipboard write can fail (permissions / unsupported environment);
        // fail silently rather than disrupting the chat UX.
      })
  }, [text, flashFeedback])

  const handleSave = useCallback(() => {
    saveMessageAsMarkdown(text)
      .then((path) => {
        // An empty path means the user cancelled the native dialog — no
        // feedback, the button simply stays in its idle Save state.
        if (path) flashFeedback('saved')
      })
      .catch((err) => {
        // Unlike clipboard writes, a failed save (dialog error, unwritable
        // path) is invisible without a toast — surface it.
        logger.error('Failed to save message to file:', err)
        emit('runtime_error', {
          id: crypto.randomUUID(),
          message: 'Failed to save message to file',
        })
      })
  }, [text, flashFeedback])

  const currentLabel =
    feedback === 'copied' ? 'Copied' : feedback === 'saved' ? 'Saved' : shiftHeld ? 'Save' : label
  const Icon = feedback !== null ? Check : shiftHeld ? Save : Copy

  return (
    <button
      type="button"
      onClick={shiftHeld ? handleSave : handleCopy}
      className={cn(
        'inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-muted-foreground hover:bg-muted/50 hover:text-foreground transition-colors',
        className,
      )}
      title={currentLabel}
      aria-label={currentLabel}
    >
      <Icon className={cn('h-3.5 w-3.5', feedback !== null && 'text-success')} />
      <span>{currentLabel}</span>
    </button>
  )
}
