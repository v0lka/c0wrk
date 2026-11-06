import { useCallback } from 'react'
import { Copy, Check, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useCopyFlash } from '@/hooks/useCopyFlash'

interface CopyIconButtonProps {
  /** Text copied to the clipboard on click. */
  text: string
  /** Accessible label / tooltip shown in the idle state. */
  label?: string
  className?: string
}

/**
 * CopyIconButton — icon-only copy-to-clipboard control.
 *
 * Renders just a `Copy` icon (`h-3.5 w-3.5`); the label lives only in
 * `title` / `aria-label` and flips to "Copied" / "Failed" for a short window
 * after a click (success flashes the `Check` icon in `text-success`, failure
 * the `X` icon in `text-destructive`). The clipboard write and the flash
 * contract (native Wails runtime via `clipboardSetText`, false/rejection is a
 * failed copy, bounded revert timer) are the shared `useCopyFlash` hook.
 * Unlike chat/CopyButton there is no visible text label and no Save morph —
 * hover reveal in chat messages is handled by the parent via `group` +
 * `group-hover:*` wrappers.
 */
export function CopyIconButton({ text, label = 'Copy', className }: CopyIconButtonProps) {
  const { feedback, copy } = useCopyFlash()

  const handleCopy = useCallback(() => copy(text), [copy, text])

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
