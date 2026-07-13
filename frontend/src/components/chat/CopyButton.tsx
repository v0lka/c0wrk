import { useState, useCallback, useEffect, useRef } from 'react'
import { Copy, Check } from 'lucide-react'
import { cn } from '@/lib/utils'

interface CopyButtonProps {
  /** Text copied to the clipboard on click. */
  text: string
  /** Accessible label / tooltip shown in the idle state. */
  label?: string
  className?: string
}

/**
 * CopyButton — small, low-contrast copy-to-clipboard control.
 *
 * Renders a `Copy` icon plus a text label that flips to a success `Check` +
 * "Copied" for a short window after a successful copy. The button itself is
 * always visible; the hover reveal that shows/hides it in chat messages is
 * handled by the parent via a `group` + `group-hover:*` wrapper.
 */
export function CopyButton({ text, label = 'Copy', className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current)
    }
  }, [])

  const handleCopy = useCallback(() => {
    navigator.clipboard
      .writeText(text)
      .then(() => {
        setCopied(true)
        if (timerRef.current) clearTimeout(timerRef.current)
        timerRef.current = setTimeout(() => setCopied(false), 2000)
      })
      .catch(() => {
        // Clipboard write can fail (permissions / unsupported environment);
        // fail silently rather than disrupting the chat UX.
      })
  }, [text])

  const currentLabel = copied ? 'Copied' : label

  return (
    <button
      type="button"
      onClick={handleCopy}
      className={cn(
        'inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-muted-foreground hover:bg-muted/50 hover:text-foreground transition-colors',
        className,
      )}
      title={currentLabel}
      aria-label={currentLabel}
    >
      {copied ? (
        <Check className="h-3.5 w-3.5 text-success" />
      ) : (
        <Copy className="h-3.5 w-3.5" />
      )}
      <span>{currentLabel}</span>
    </button>
  )
}
