import { CheckCircle2, Terminal, XCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { GitOperationRecord } from '@/stores/gitPanelStore'

export interface GitOperationPopoverProps {
  /** The active project's last operation record, or undefined when none. */
  record: GitOperationRecord | undefined
}

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

/**
 * Anchored log panel for the last git operation: a status header (icon + the
 * operation label tinted green on success / red on failure) above a
 * scrollable, pre-wrapped `<pre>` of the captured output.
 *
 * Positioned by its relatively-positioned footer wrapper (`absolute
 * bottom-full right-0`) and capped in `--ui-vh` units so it stays inside the
 * window at any UI scale. Purely presentational — the footer owns open state
 * and the outside-click / Escape dismissal.
 */
export function GitOperationPopover({ record }: GitOperationPopoverProps) {
  const tone =
    record === undefined
      ? 'text-muted-foreground'
      : record.ok
        ? 'text-success'
        : 'text-destructive'

  return (
    <div
      role="dialog"
      aria-label="Git operation log"
      className="absolute bottom-full right-0 z-50 mb-1 flex w-96 max-h-[calc(var(--ui-vh)*0.5)] flex-col overflow-hidden rounded-md border border-border bg-popover text-popover-foreground shadow-md"
    >
      <div className="flex shrink-0 items-center gap-1.5 border-b border-border px-2 py-1.5">
        {record === undefined ? (
          <Terminal className={cn('size-3.5 shrink-0', tone)} />
        ) : record.ok ? (
          <CheckCircle2 className={cn('size-3.5 shrink-0', tone)} />
        ) : (
          <XCircle className={cn('size-3.5 shrink-0', tone)} />
        )}
        <span className={cn('truncate text-xs font-medium', tone)}>{headline(record)}</span>
      </div>
      <pre className="custom-scrollbar min-h-0 flex-1 overflow-auto whitespace-pre-wrap break-words p-2 font-mono text-[10px] leading-tight text-muted-foreground">
        {capturedText(record)}
      </pre>
    </div>
  )
}
