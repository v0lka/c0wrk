import type { ReactNode } from 'react'
import { CheckCircle2, FileTerminal, XCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import type { GitOperationRecord } from '@/stores/gitPanelStore'

/** Header text: the operation's label, falling back to its kind. */
function headline(record: GitOperationRecord | undefined): string {
  if (record === undefined) return 'No git operations yet'
  return record.label.trim() || record.kind
}

/**
 * Body text: the captured stdout+stderr, or the failure message when the
 * operation produced no output (a failed spawn records an empty `output` and
 * the message in `error`), or a placeholder when there is nothing to show.
 */
function capturedText(record: GitOperationRecord | undefined): string {
  if (record === undefined) return 'No output'
  const captured = record.output.trim().length > 0 ? record.output : (record.error ?? '')
  return captured.trim().length > 0 ? captured : 'No output'
}

export interface GitOperationPopoverProps {
  /** The active project's last operation record, or undefined when none. */
  record: GitOperationRecord | undefined
  /** Controlled open state. Radix owns toggling/dismissal; the owner mirrors
   *  it (and acknowledges the record on open) through `onOpenChange`. */
  open: boolean
  /** Radix open-change callback: trigger toggle, Escape, outside click. */
  onOpenChange: (open: boolean) => void
  /**
   * The trigger element. Rendered through `PopoverTrigger asChild`, so it
   * must be Slot-compatible: forward `ref` and spread the extra props it
   * receives (Radix supplies aria-controls/aria-expanded/onClick/data-state).
   */
  children: ReactNode
}

/**
 * Anchored log panel for the last git operation: a status header (icon + the
 * operation label tinted green on success / red on failure) above a
 * scrollable, pre-wrapped `<pre>` of the captured output.
 *
 * A managed Radix Popover (`ui/popover.tsx`): the content is portaled to
 * `document.body`, so it is never clipped by the Git panel's
 * `overflow-hidden` ancestor — and never makes that ancestor SCROLL (an
 * in-place panel wider than the resizable sidebar used to overflow it, and
 * focusing it scrolled the clipped container). Geometry belongs to
 * Radix/floating-ui with the global zoom patch: it opens upward,
 * left-aligned to the trigger (`side="top" align="start"`), flips when the
 * room above is short, and clamps to the viewport. Sizing is token/class
 * only — `w-96` resting width, `max-w-(--radix-popover-content-available-width)`
 * for narrow windows, and a `min(...)` height cap of the Radix available
 * height and the old 360px ceiling. Focus management is Radix's: the content
 * takes focus on open, Escape/outside-click dismiss, and focus returns to the
 * trigger on close. Purely presentational — the footer owns open state.
 */
export function GitOperationPopover({
  record,
  open,
  onOpenChange,
  children,
}: GitOperationPopoverProps) {
  const tone =
    record === undefined
      ? 'text-muted-foreground'
      : record.ok
        ? 'text-success'
        : 'text-destructive'

  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger asChild>{children}</PopoverTrigger>
      <PopoverContent
        side="top"
        align="start"
        sideOffset={6}
        collisionPadding={8}
        aria-label="Git operation log"
        className="flex w-96 max-h-[min(var(--radix-popover-content-available-height),22.5rem)] max-w-(--radix-popover-content-available-width) flex-col overflow-hidden p-0 outline-none bg-background text-foreground"
      >
        <div className="flex shrink-0 items-center gap-1.5 border-b border-border px-2 py-1.5">
          {record === undefined ? (
            <FileTerminal className={cn('size-3.5 shrink-0', tone)} />
          ) : record.ok ? (
            <CheckCircle2 className={cn('size-3.5 shrink-0', tone)} />
          ) : (
            <XCircle className={cn('size-3.5 shrink-0', tone)} />
          )}
          <span className={cn('truncate text-xs font-medium', tone)}>{headline(record)}</span>
        </div>
        <pre className="custom-scrollbar min-h-0 flex-1 overflow-auto whitespace-pre-wrap break-words p-2 font-mono text-xs leading-tight text-muted-foreground">
          {capturedText(record)}
        </pre>
      </PopoverContent>
    </Popover>
  )
}
