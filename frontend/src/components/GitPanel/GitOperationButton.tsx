import { type Ref } from 'react'
import { Loader2, FileTerminal } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { GitOperationRecord } from '@/stores/gitPanelStore'

/**
 * Text-colour class for the operation glyph.
 *
 * Neutral while an operation is running (there is no result to report yet),
 * when the project has no record, or once the result has been acknowledged
 * (opening the log acknowledges it — see the footer's `handleLogOpenChange`);
 * otherwise green on success and red on failure.
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
  /** Manual toggle handler. Unset when the button is rendered as a Radix
   *  PopoverTrigger (`asChild`): the Slot-composed onClick toggles then. */
  onToggle?: () => void
  /** Ref to the trigger, so the owner can restore focus when the panel closes. */
  buttonRef?: Ref<HTMLButtonElement>
  /** Id of the log panel this trigger controls (`aria-controls`). */
  controlsId?: string
  /** Forwarded when the button serves as a Radix Slot child (`asChild`):
   *  Radix needs the DOM node for anchoring and focus restoration. */
  ref?: Ref<HTMLButtonElement>
}

/**
 * Footer affordance for the last git operation: a file-terminal glyph tinted by the
 * outcome (green on success / red on failure / neutral when unread-or-none)
 * that spins while an operation is active. The button stays clickable during
 * an operation so the *previous* result's log remains readable; opening it is
 * the owner's job (see the footer's `handleLogOpenChange`).
 *
 * Slot-compatible: forwarded props (`ref` + the rest spread) let it serve as
 * a `PopoverTrigger asChild` child — Radix's composed aria/onClick/data-state
 * arrive through the spread and win over the manual ones.
 */
export function GitOperationButton({ record, busy, open, onToggle, buttonRef, controlsId, ref, ...rest }: GitOperationButtonProps) {
  return (
    <Button
      ref={ref ?? buttonRef}
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
      {...rest}
    >
      {busy ? (
        <Loader2 className="size-3.5 animate-spin" />
      ) : (
        <FileTerminal className="size-3.5" />
      )}
    </Button>
  )
}
