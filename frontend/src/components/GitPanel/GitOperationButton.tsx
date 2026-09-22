import { type Ref } from 'react'
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
  /** True while a remote git operation is in flight — spins the glyph. The
   *  button stays clickable so the previous result's log can be opened. */
  busy: boolean
  /** Whether this button's log popover is open (drives `aria-expanded`). */
  open: boolean
  /** Toggle the log popover. The owner acknowledges the record on open. */
  onToggle: () => void
  /** Ref to the trigger, so the owner can restore focus when the panel closes. */
  buttonRef?: Ref<HTMLButtonElement>
  /** Id of the log panel this trigger controls (`aria-controls`). */
  controlsId?: string
}

/**
 * Footer affordance for the last git operation: a terminal glyph tinted by the
 * outcome (green on success / red on failure / neutral when unread-or-none)
 * that spins while an operation is active. The button stays clickable during
 * an operation so the *previous* result's log remains readable; opening it is
 * the owner's job (see the footer's `toggleLog`).
 */
export function GitOperationButton({ record, busy, open, onToggle, buttonRef, controlsId }: GitOperationButtonProps) {
  return (
    <Button
      ref={buttonRef}
      type="button"
      variant="ghost"
      size="xs"
      onClick={onToggle}
      aria-label="Git operation log"
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-controls={controlsId}
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
