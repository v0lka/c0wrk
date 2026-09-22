import { Loader2, Terminal } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { GitOperationRecord } from '@/stores/gitPanelStore'

/**
 * Text-colour class for the operation glyph.
 *
 * Neutral while an operation is running (there is no result to report yet),
 * when the project has no record, or once the result has been acknowledged
 * (opening the log acknowledges it — see the footer's `toggleLog`); otherwise
 * green on success and red on failure.
 */
function operationTone(record: GitOperationRecord | undefined, busy: boolean): string {
  if (busy || record === undefined || record.acknowledged) return 'text-muted-foreground'
  return record.ok ? 'text-success' : 'text-destructive'
}

export interface GitOperationButtonProps {
  /** The active project's last operation record, or undefined when none. */
  record: GitOperationRecord | undefined
  /** True while a remote git operation is in flight — disables and spins. */
  busy: boolean
  /** Whether this button's log popover is open (drives `aria-expanded`). */
  open: boolean
  /** Toggle the log popover. The owner acknowledges the record on open. */
  onToggle: () => void
}

/**
 * Footer affordance for the last git operation: a terminal glyph tinted by the
 * outcome (green on success / red on failure / neutral when unread-or-none)
 * that spins and disables while an operation is active. Clicking it opens the
 * {@link GitOperationPopover} that its relatively-positioned footer wrapper
 * anchors.
 */
export function GitOperationButton({ record, busy, open, onToggle }: GitOperationButtonProps) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="xs"
      disabled={busy}
      onClick={onToggle}
      aria-label="Git operation log"
      aria-haspopup="dialog"
      aria-expanded={open}
      title="Git operation log"
      className={cn('gap-0.5 px-1', operationTone(record, busy))}
    >
      {busy ? (
        <Loader2 className="size-3.5 animate-spin" />
      ) : (
        <Terminal className="size-3.5" />
      )}
    </Button>
  )
}
