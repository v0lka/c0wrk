import { memo, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Loader2, CheckCircle2, XCircle, CircleSlash } from 'lucide-react'
import { cn } from '@/lib/utils'
import { bookmarkDefaultTitle, bookmarkKey, flattenDisplayItems } from '@/lib/bookmarks'
import { areDisplayItemsEqual } from '@/lib/displayItemStability'
import { formatDuration } from '@/lib/formatters'
import { CollapsibleBlock } from '@/components/chat/CollapsibleBlock'
import { StepChecklistProgress } from './StepChecklistProgress'
import { turnWorkOwners } from './turnWorkOwners'
import { ChatMessageRenderer } from './ChatMessageRenderer'
import { BookmarkableContext } from './BookmarkableContext'
import type { DisplayItem } from '@/types/messages'

/**
 * Collapsible wrapper around ONE work segment of a chat turn — the activity
 * (thoughts, tool cards, intermediate texts, …) that ran before the segment's
 * plan/subagent steps, as split by `splitTurnWork`. Step blocks render OUTSIDE
 * any collapsible (the plan-panel contract), between their segment's block
 * and the next one.
 *
 * Open-state rule: the LAST segment of a turn is OPEN while the turn is LIVE
 * (it is the last turn AND the session's task is still active — streaming,
 * plan steps, tool calls can still land) and auto-COLLAPSES when the turn
 * settles: the answer commits, OR the turn is superseded (a nudge / resume
 * started a new turn), OR the task ended without an answer (stop / error /
 * app exit — historical dead turns settle the same way after a reload).
 * EARLIER segments are settled from birth (`superseded`): the step that cut
 * them out completed the activity they hold, so they render COLLAPSED with
 * the completed status — "block considered finished when a step appeared".
 * A manual toggle wins until the next auto edge, mirroring SubAgentBlock:
 * `userOverride ?? derived`, with the override reset by a `useEffect` keyed
 * on the settle edge. The reverse edge — the turn stops being the last one —
 * remounts the block (the next turn's split takes over), which naturally
 * resets the override.
 */
interface TurnWorkBlockProps {
  /** The turn "can still produce new items": it is the session's last turn
   * AND the session's task is active. True keeps the block open and the
   * status running; false settles it (collapsed) even without a committed
   * answer — a stop, an error, or a superseding turn (nudge / resume).
   */
  live?: boolean
  /**
   * This segment was cut out by a later plan/subagent step: settled from
   * birth with the completed status (failed when its work holds an error) —
   * never open, regardless of `live`.
   */
  superseded?: boolean
  /** The segment's work items — one collapsible block's content. */
  work: DisplayItem[]
  /** The turn's tail (final answer + lifted panels) — LAST segment only.
   * Scanned for the header preview / checklist chip / answer-committed
   * status; rendered as tail rows by the chat renderer, not here.
   */
  tail: DisplayItem[]
  /**
   * Render-slot: trailing content (the streaming answer, passed to the root
   * renderer as `trailingContent`) is rendered INSIDE the open block, below
   * the work list. Slot-only — never folded into `work` or `tail` — so the
   * memo comparator's structural item equality is unaffected by it, exactly
   * as SubAgentBlock's memo treats its own non-item children. The renderer
   * mounts the slot only on the live segment's block, so the stream lives
   * inside the block from the first chunk and the settled answer swaps out to
   * the tail row without any block-boundary remount. The activity indicator
   * is NOT part of this slot — it renders outside the blocks (below them), so
   * it stays visible when the user collapses the live block.
   */
  tailSlot?: ReactNode
}

type WorkStatus = 'running' | 'completed' | 'failed' | 'interrupted'

// Mirrors SubAgentBlock's statusConfig, narrowed to the states a work block
// can take: `failed` is derived from an error item inside the work, and
// `interrupted` (settled without an answer — a stop, an error turn, or the
// turn superseded by a nudge/resume) from `live=false` with an empty tail.
const statusConfig = {
  running:   { Icon: Loader2,      iconClass: 'text-info animate-spin', accent: 'info' },
  completed: { Icon: CheckCircle2, iconClass: 'text-success', accent: 'success' },
  failed:    { Icon: XCircle,      iconClass: 'text-destructive', accent: 'destructive' },
  interrupted: { Icon: CircleSlash, iconClass: 'text-muted-foreground', accent: 'muted' },
} as const

/** First message timestamp (epoch ms) among the items; 0 when none carries one. */
function firstTimestamp(items: readonly DisplayItem[]): number {
  for (const it of items) {
    if ('message' in it && it.message.timestamp > 0) return it.message.timestamp
  }
  return 0
}

/** Last message timestamp (epoch ms) among the items; 0 when none carries one. */
function lastTimestamp(items: readonly DisplayItem[]): number {
  for (let i = items.length - 1; i >= 0; i--) {
    const it = items[i]!
    if ('message' in it && it.message.timestamp > 0) return it.message.timestamp
  }
  return 0
}

/** Structural equality over item arrays — the memo comparator's array halves. */
function displayItemsArraysEqual(a: readonly DisplayItem[], b: readonly DisplayItem[]): boolean {
  if (a === b) return true
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    if (!areDisplayItemsEqual(a[i]!, b[i]!)) return false
  }
  return true
}

/**
 * Every anchor key a top-level work item is navigable by: its bookmark key,
 * plus — for plan_step/subagent — the plan `stepId` PlanView's scrollToStep
 * navigates by (the item's `id` is the plan_step_start EVENT id, not the step
 * id, so registering the bookmark key alone leaves plan-step navigation into
 * a collapsed block unresolved).
 */
function anchorKeysFor(it: DisplayItem): string[] {
  const keys = [bookmarkKey(it)]
  if (it.kind === 'plan_step' || it.kind === 'subagent') keys.push(it.stepId)
  return keys
}

export const TurnWorkBlock = memo(function TurnWorkBlock({ live = false, superseded = false, work, tail, tailSlot }: TurnWorkBlockProps) {
  const bookmarkable = useContext(BookmarkableContext)

  // The auto-open state: the LAST segment of a turn is expanded while the
  // turn is live — the LAST turn of an ACTIVE task — regardless of whether an
  // answer has committed yet (a multi-step task commits intermediate answers
  // between steps; the turn keeps producing items). The block settles
  // (collapses) when the turn is done: answer committed AND the turn is no
  // longer live, or the turn is not live anymore without an answer (stop /
  // error / superseded by nudge / historical dead turn after a reload).
  // EARLIER segments (superseded) are settled from birth: the step that cut
  // them out completed the activity they hold.
  const settled = !live || superseded
  const derivedOpen = !settled
  const [userOverride, setUserOverride] = useState<boolean | null>(null)
  const isOpen = userOverride ?? derivedOpen
  // Reset the manual toggle on the settle edge so the auto rule (and only the
  // auto rule) decides the settled state. The reverse edge (turn stops being
  // the last one) remounts the block — nothing to reset here.
  useEffect(() => { setUserOverride(null) }, [settled])

  // An answer committed only when the tail holds the assistant item — the
  // split now lifts unresolved pending-action panels into the tail even while
  // the turn is still running (no answer yet), so bare tail length is NOT an
  // answer signal anymore. A superseded segment settled when the step that
  // cut it out started — completed, not interrupted (unless its work holds an
  // error → failed).
  const answerCommitted = tail.some(it => it.kind === 'assistant')
  const status: WorkStatus = useMemo(
    () =>
      work.some(it => it.kind === 'error')
        ? 'failed'
        : live && !superseded
          ? 'running'
          : answerCommitted
            ? 'completed'
            : superseded
              ? 'completed'
              : 'interrupted',
    [work, live, superseded, answerCommitted],
  )
  const cfg = statusConfig[status]
  const StatusIcon = cfg.Icon
  const iconClass = cfg.iconClass

  const statusIcon = useMemo(() => (
    <StatusIcon className={cn('h-3.5 w-3.5 shrink-0', iconClass)} />
  ), [StatusIcon, iconClass])

  // Wall-clock span of the work: first vs last message-backed timestamp.
  // Standalone items (tools, thoughts, service markers) carry no timestamp —
  // they are simply skipped in the scan. Omitted when not computable (no
  // timestamps at all, or an inverted span).
  const duration = useMemo(() => {
    const start = firstTimestamp(work)
    const end = lastTimestamp(work)
    if (start <= 0 || end <= 0 || end < start) return undefined
    return end - start
  }, [work])

  // A string label so CollapsibleBlock puts it on the trigger's `title`
  // (screen-reader / hover surface for the whole header line).
  const label = useMemo(() => (
    `Work steps (${work.length})${duration !== undefined ? ` · ${formatDuration(duration)}` : ''}`
  ), [work.length, duration])

  // One-line preview of the last action — the same default title the bookmarks
  // panel derives for the item (collapseTitle logic, not duplicated here).
  // Exception: while a checklist is in flight the preview shows its LAST
  // UNCHECKED entry instead — "where the run is now" — because the raw last
  // work item is whatever event happened to land last (a step-finish marker, a
  // service notice), which tells nothing about the current step. Checklists
  // supersede each other, so the LAST checklist in the flattened work+tail
  // tree is the current one (the active checklist sinks into the tail); a
  // fully checked checklist falls back to the default.
  const preview = useMemo(() => {
    const flat = flattenDisplayItems([...work, ...tail])
    for (let i = flat.length - 1; i >= 0; i--) {
      const it = flat[i]!
      if (it.kind !== 'checklist') continue
      for (let j = it.items.length - 1; j >= 0; j--) {
        if (!it.items[j]!.checked) return it.items[j]!.text
      }
      break
    }
    const last = work[work.length - 1]
    return last ? bookmarkDefaultTitle(last) : ''
  }, [work, tail])

  // Checklist status chip in the header (the plan-step contract): counts from
  // the LAST checklist in the flattened work+tail tree — checklists supersede
  // each other, and the active one sinks to the tail (grouping rule 7), so
  // the last one in the scan is the current one. Hidden while there is no
  // checklist (StepChecklistProgress itself hides on a non-positive total).
  const checklistCounts = useMemo((): { total: number; done: number } | undefined => {
    const flat = flattenDisplayItems([...work, ...tail])
    for (let i = flat.length - 1; i >= 0; i--) {
      const it = flat[i]!
      if (it.kind !== 'checklist') continue
      return { total: it.items.length, done: it.items.filter(x => x.checked).length }
    }
    return undefined
  }, [work, tail])

  const headerExtra = useMemo(() => (
    <>
      {status === 'interrupted' && (
        <span className="text-xs text-muted-foreground truncate min-w-0">— interrupted</span>
      )}
      {checklistCounts !== undefined && (
        <StepChecklistProgress
          total={checklistCounts.total}
          completed={checklistCounts.done}
          accent={cfg.accent}
        />
      )}
      {preview && (
        <span className="text-xs text-muted-foreground truncate min-w-0" title={preview}>— {preview}</span>
      )}
    </>
  ), [status, checklistCounts, cfg.accent, preview])

  // Stable chevron-reveal id derived from the block's first work item; falls
  // back to CollapsibleBlock's useId() while the work is empty.
  const revealId = work.length > 0 ? `turn-work:${bookmarkKey(work[0]!)}` : undefined

  // Publish this block as the collapse-owner of every TOP-LEVEL work item:
  // Radix unmounts collapsed content, so while this block is collapsed the
  // bookmark anchors of its work items are absent from the DOM and the
  // scroll manager resolves them through turnWorkOwners instead. Cleanup runs
  // before the next registration (effect ordering), and the guarded
  // unregister cannot evict a newer block re-using the same revealId.
  useEffect(() => {
    if (!revealId) return
    for (const it of work) for (const key of anchorKeysFor(it)) turnWorkOwners.register(key, revealId)
    return () => {
      for (const it of work) for (const key of anchorKeysFor(it)) turnWorkOwners.unregister(key, revealId)
    }
  }, [revealId, work])

  return (
    <CollapsibleBlock
      label={label}
      open={isOpen}
      onOpenChange={setUserOverride}
      statusIcon={statusIcon}
      revealId={revealId}
      headerExtra={headerExtra}
    >
      <div className="mt-2 border-l-2 border-border rounded pl-3 py-2 space-y-3 min-w-0">
        <ChatMessageRenderer items={work} bookmarkable={bookmarkable} />
        {tailSlot}
      </div>
    </CollapsibleBlock>
  )
}, (prev, next) =>
  displayItemsArraysEqual(prev.work, next.work) &&
  displayItemsArraysEqual(prev.tail, next.tail) &&
  prev.live === next.live &&
  prev.superseded === next.superseded &&
  // Slot-only streaming content: false when the node changed (a new chunk
  // landed) so the block re-renders and the stream keeps flowing inside it.
  prev.tailSlot === next.tailSlot)
