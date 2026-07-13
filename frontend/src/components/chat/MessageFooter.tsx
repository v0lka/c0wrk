import { CopyButton } from '@/components/chat/CopyButton'
import { cn } from '@/lib/utils'

interface MessageFooterProps {
  /** Text copied to the clipboard on click. Omit / blank to hide the control. */
  copyText?: string
  /** Pre-formatted timestamp string shown on the right. */
  time?: string
  className?: string
}

/**
 * MessageFooter — reserved-height row anchored to the bottom of a message.
 *
 * Left slot: copy-to-clipboard control, revealed on hover via opacity only.
 * Right slot: message timestamp.
 *
 * The row always occupies its fixed height, so the button's hover appearance
 * never animates the layout (no chat "jump"). The hover reveal relies on the
 * nearest ancestor carrying the Tailwind `group` class.
 */
export function MessageFooter({ copyText, time, className }: MessageFooterProps) {
  const hasCopy = !!copyText && copyText.trim().length > 0
  return (
    <div className={cn('flex h-6 w-full items-center', className)}>
      {hasCopy && (
        <div
          className="opacity-0 transition-opacity duration-150 group-hover:opacity-100"
          // The copy control is a self-contained action: stop its click/keydown
          // from bubbling into ancestor handlers (e.g. the pinned message's
          // expand/collapse toggle) so copying never collapses the message.
          onClick={(e) => e.stopPropagation()}
          onKeyDown={(e) => e.stopPropagation()}
        >
          <CopyButton text={copyText} />
        </div>
      )}
      {time && <span className="ml-auto text-xs text-muted-foreground">{time}</span>}
    </div>
  )
}
