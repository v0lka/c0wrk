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
 * Slot-compatible: forwarded `ref` + the trailing props spread let it serve
 * as a `PopoverTrigger asChild` child. The whole trigger contract —
 * aria-haspopup/aria-expanded/aria-controls, the toggling onClick and the
 * data-state attribute — is Radix's, composed through the spread: the button
 * declares no click handler or expanded-state props of its own, so a composed
 * onClick can never double-fire a manual toggle.
 */
export function GitOperationButton({ record, busy, ref, ...rest }: GitOperationButtonProps) {
  return (
    <Button
      ref={ref}
      type="button"
      variant="ghost"
      size="xs"
      aria-label="Git operation log"
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
