// Turn splitting for the chat transcript: separates a turn into ordered
// SEGMENTS — each one collapsible work block plus the plan/subagent step
// blocks that ran before it — and the TAIL (final answer + live panels).
//
// A turn is everything after the LAST user message. Within a turn the final
// `assistant` item is the model's answer; everything between the user message
// and that answer (thoughts, tool cards, intermediate assistant texts, service
// markers, …) is the turn's WORK — the activity that produced the answer.
// Everything from the final assistant item onward is the TAIL: the answer
// itself plus whatever follows it.
//
// SEGMENTATION: the appearance of a plan_step / subagent block — or a LAUNCHER
// tool card (execute_plan / delegate: the compact markers that start those
// hierarchies) — CUTS the work accumulated so far into a settled block
// ("before the step") and everything after the step starts a NEW live block.
// The step blocks and launcher cards themselves render
// OUTSIDE any collapsible (their status chips must survive a collapse — the
// plan-panel contract), between the block they interrupted and the next one,
// so a turn renders as
//   [work₁][step₁][work₂][step₂]…[tail]
// in exact stream order. Consecutive step blocks (no work between them)
// share one pinned group — no empty blocks are produced. The LAST segment is
// the live one (open while the task runs); every earlier segment is settled
// (collapsed, superseded-by-a-step → completed status, not interrupted).
//
// Unresolved pending-action panels (tool_confirm, ask_user, step_limit,
// plan_review, goal_proposal, resume_action) are LIFTED out of the work
// region into the tail in BOTH shapes — with or without a committed answer.
// They are blocking interactive affordances, and a collapsed collapsible
// unmounts its content: a panel buried inside a work container would be
// unreachable exactly while the run waits for the user. The confirmation wait
// is a turn with NO final assistant item yet, so the anchor-only split would
// otherwise file the panel under a work segment. Resolved panels stay at
// their stream position inside a work segment — settled decision history
// renders like any other work item.
//
// Pure data transform: no React, no stores, no mutation of the input.

import type { ChatMessageUI, DisplayItem } from '@/types/messages'

/** One collapsible work block plus the step blocks that ran before it. */
export interface TurnWorkSegment {
  /**
   * The segment's work items — the content of ONE collapsible block.
   * Empty when the segment only carries step blocks (steps that ran with no
   * observable work between them — e.g. back-to-back plan steps).
   */
  work: DisplayItem[]
  /**
   * Top-level plan_step / subagent blocks that interrupted the previous
   * block / start this segment, in stream order — the same step statuses the
   * Execution plan panel surfaces outside the collapsed container. Rendered
   * AFTER the segment's work block (which precedes them in the stream).
   */
  pinned: DisplayItem[]
}

export interface TurnWorkSplit {
  /** Work segments in stream order; the LAST one is the live block. */
  segments: TurnWorkSegment[]
  /**
   * The last assistant item plus everything after it — the final answer, any
   * trailing items, and every UNRESOLVED pending-action panel lifted out of
   * the work region (panels keep their relative order after the answer).
   * May hold ONLY lifted panels while the turn is still running (no answer
   * yet). Empty when the turn has neither an assistant item nor a pending
   * panel.
   */
  tail: DisplayItem[]
}

/** Pending-action panel kinds — the blocking interactive cards (HITL + resume). */
const ACTION_KINDS: ReadonlySet<DisplayItem['kind']> = new Set<DisplayItem['kind']>([
  'tool_confirm',
  'ask_user',
  'step_limit',
  'plan_review',
  'goal_proposal',
  'resume_action',
])

/** True for a top-level plan-step / subagent block — cuts the work stream. */
function isPinnedItem(it: DisplayItem): boolean {
  return it.kind === 'plan_step' || it.kind === 'subagent'
}

/**
 * A LAUNCHER tool card is the compact tool marker that starts the pinned
 * step hierarchy: `execute_plan` opens the root plan's step blocks,
 * `delegate` opens the subagent blocks. It belongs WITH the hierarchy it
 * started — outside the work container, ahead of its step blocks — so
 * "Executing: plan" never stays buried inside the block whose cut it caused.
 * Both extract-static-title tools with no body; the name is the identity.
 */
const LAUNCHER_TOOLS: ReadonlySet<string> = new Set(['execute_plan', 'delegate'])

/** True for a launcher card that opened the pinned steps (the cut marker). */
function isLauncherItem(it: DisplayItem): boolean {
  return it.kind === 'tool' && LAUNCHER_TOOLS.has(it.toolName)
}

/** True for an UNRESOLVED pending-action panel (a blocking live affordance). */
function isUnresolvedActionItem(it: DisplayItem): boolean {
  if (!ACTION_KINDS.has(it.kind)) return false
  const { metadata } = (it as { message: ChatMessageUI }).message
  return metadata?.resolved !== true
}

/** Splits the flat display items of a turn into { segments, tail }. */
export function splitTurnWork(items: DisplayItem[]): TurnWorkSplit {
  const lastUserIdx = findLastIndex(items, it => it.kind === 'user')
  const lastAssistantIdx = findLastIndex(items, it => it.kind === 'assistant')

  // No assistant after the last user message: the turn is still running (or
  // died before answering). Everything after the user message is the work
  // region; the tail starts empty — no answer block, only lifted segments
  // can appear in the tail.
  let region: DisplayItem[]
  let tail: DisplayItem[]
  if (lastAssistantIdx === -1 || lastAssistantIdx <= lastUserIdx) {
    region = items.slice(lastUserIdx + 1)
    tail = []
  } else {
    region = items.slice(lastUserIdx + 1, lastAssistantIdx)
    tail = items.slice(lastAssistantIdx)
  }

  // Lift unresolved panels out of the work region into the tail, preserving
  // their relative order (groupMessages() already sinks them to the end of
  // the root list, so appending keeps both orders stable).
  if (region.some(isUnresolvedActionItem)) {
    tail = [...tail, ...region.filter(isUnresolvedActionItem)]
    region = region.filter(it => !isUnresolvedActionItem(it))
  }

  // Segment the work region: a step block — or the launcher card that opened
  // it — attaches to the segment whose work produced it (it CUTS that work
  // out as a settled block); the first work item AFTER a step starts the next
  // segment. Work items never cut by themselves — consecutive steps share
  // their segment, and plain work accumulates into one block. Rendered per
  // segment as block → pinned, the original stream order is reproduced
  // exactly (the launcher card lands at the HEAD of the pinned group — it
  // precedes the steps it started in the stream).
  const segments: TurnWorkSegment[] = []
  let cur: TurnWorkSegment = { work: [], pinned: [] }
  for (const it of region) {
    if (isPinnedItem(it) || isLauncherItem(it)) {
      cur.pinned.push(it)
      continue
    }
    if (cur.pinned.length > 0) {
      segments.push(cur)
      cur = { work: [], pinned: [] }
    }
    cur.work.push(it)
  }
  if (cur.work.length > 0 || cur.pinned.length > 0) segments.push(cur)

  return { segments, tail }
}

/** Array.prototype.findLastIndex is ES2023; this module targets a lower lib. */
function findLastIndex(items: DisplayItem[], pred: (it: DisplayItem) => boolean): number {
  for (let i = items.length - 1; i >= 0; i--) {
    if (pred(items[i]!)) return i
  }
  return -1
}
