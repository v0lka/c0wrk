import { useCallback } from 'react'
import { Copy, Check, Save, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { emit, isWebTransport } from '@/api/runtime'
import { saveMessageAsMarkdown } from '@/api/files'
import { useShiftHeld } from '@/hooks/useShiftHeld'
import { useCopyFlash } from '@/hooks/useCopyFlash'
import { logger } from '@/lib/logger'
import { generateUUID } from '@/lib/ids'

interface CopyButtonProps {
  /** Text copied to the clipboard on click (or saved to a file on Shift+click). */
  text: string
  /** Accessible label / tooltip shown in the idle state. */
  label?: string
  className?: string
}

/** The save path's extra flash kinds, on top of the shared copy contract. */
type SaveFeedback = 'saved' | 'download-started'

/**
 * CopyButton — small, low-contrast copy-to-clipboard control.
 *
 * Renders a `Copy` icon plus a text label that flips to a success `Check` +
 * "Copied" for a short window after a successful copy. While Shift is held
 * the button morphs into a `Save` action: clicking opens the native save-file
 * dialog (default directory = the active project) and writes the text to the
 * chosen .md file, flashing "Saved" on success. The copy verdict's flash
 * contract (native Wails runtime via `clipboardSetText`, false/rejection is a
 * failed copy, bounded revert timer) is the shared `useCopyFlash` hook, with
 * the save kinds flashed through the same timer via `flash`. The button
 * itself is always visible; the hover reveal that shows/hides it in chat
 * messages is handled by the parent via a `group` + `group-hover:*` wrapper.
 */
export function CopyButton({ text, label = 'Copy', className }: CopyButtonProps) {
  const { feedback, copy, flash } = useCopyFlash<SaveFeedback>()
  const shiftHeld = useShiftHeld()

  const handleCopy = useCallback(() => copy(text), [copy, text])

  const handleSave = useCallback(() => {
    if (isWebTransport()) {
      // Web transport: there is no native save dialog — hand the browser a
      // plain anchor download instead (the trivially-replaceable native
      // dialog from the roadmap).
      const blob = new Blob([text], { type: 'text/markdown' })
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = 'message.md'
      anchor.click()
      // Defer the revocation — a same-tick revokeObjectURL is NOT safe here:
      // WebKit/Safari have historically raced it against the in-flight
      // download handoff, so the click cancels, no file arrives, and the
      // button still claims "Download started" (Chromium/Firefox tolerate
      // the same-tick revoke; the web transport targets every browser).
      // The 10 s revoke is the FileSaver.js approach. The timer is
      // deliberately detached: its callback touches no state, so firing
      // after unmount is exactly how the URL still gets reclaimed.
      setTimeout(() => URL.revokeObjectURL(url), 10_000)
      // The anchor click cannot report failure (a popup blocker can eat it
      // silently) — claim only that the download was handed to the browser.
      flash('download-started')
      return
    }
    saveMessageAsMarkdown(text)
      .then((path) => {
        // An empty path means the user cancelled the native dialog — no
        // feedback, the button simply stays in its idle Save state.
        if (path) flash('saved')
      })
      .catch((err) => {
        // Unlike clipboard writes, a failed save (dialog error, unwritable
        // path) is invisible without a toast — surface it.
        logger.error('Failed to save message to file:', err)
        emit('runtime_error', {
          id: generateUUID(),
          message: 'Failed to save message to file',
        })
      })
  }, [text, flash])

  const currentLabel =
    feedback === 'copied'
      ? 'Copied'
      : feedback === 'saved'
        ? 'Saved'
        : feedback === 'download-started'
          ? 'Download started'
          : feedback === 'failed'
            ? 'Failed'
            : shiftHeld
              ? 'Save'
              : label
  const Icon =
    feedback === 'failed' ? X : feedback !== null ? Check : shiftHeld ? Save : Copy

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
      <Icon
        className={cn(
          'h-3.5 w-3.5',
          feedback === 'failed' ? 'text-destructive' : feedback !== null && 'text-success',
        )}
      />
      <span>{currentLabel}</span>
    </button>
  )
}
