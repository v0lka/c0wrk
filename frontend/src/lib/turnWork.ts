// Turn splitting for the chat transcript: separates the work block of the
// latest turn from its PINNED steps and its tail (final answer + live panels).
//
// A turn is everything after the LAST user message. Within a turn the final
// `assistant` item is the model's answer; everything between the user message
// and that answer (thoughts, tool cards, intermediate assistant texts, service
// markers, …) is the turn's WORK — the activity that produced the answer.
// Everything from the final assistant item onward is the TAIL: the answer
// itself plus whatever follows it.
//
// Top-level plan_step / subagent blocks are LIFTED out of the work segment
// into their own `pinned` segment, in stream order, in BOTH shapes — with or
// without a committed answer. They are the same step statuses the Execution
// plan panel shows OUTSIDE the collapsed container (AGENTS.md: "the plan view
// follows the status-outside-the-collapsed-container contract"), so burying
// them inside the collapsible work block would hide the per-step status the
// moment the block settles. Each block owns the events nested under its
// plan_step_id, so lifting only the block itself keeps the turn's stream
// order intact: work → pinned → tail reproduces the original sequence.
//
// Unresolved pending-action panels (tool_confirm, ask_user, step_limit,
// plan_review, goal_proposal, resume_action) are LIFTED out of the work
// segment into the tail in BOTH shapes — with or without a committed answer.
// They are blocking interactive affordances, and a collapsed collapsible
// unmounts its content: a panel buried inside the work container would be
// unreachable exactly while the run waits for the user. The confirmation wait
// is a turn with NO final assistant item yet, so the anchor-only split would
// otherwise file the panel under `work`. Resolved panels stay at their stream
// position inside the work — settled decision history renders like any other
// work item.
//
// Pure data transform: no React, no stores, no mutation of the input.

import type { ChatMessageUI, DisplayItem } from '@/types/messages'

export interface TurnWorkSplit {
  /**
   * Items between the last user message and the last assistant item
   * (both boundaries excluded), minus lifted segments — the collapsible
   * activity block of the latest turn. Empty when the turn is a plain chat.
   */
  work: DisplayItem[]
  /**
   * Top-level plan_step / subagent blocks lifted out of the turn, in stream
   * order — the same step statuses the Execution plan panel surfaces outside
   * the collapsed container. Rendered BETWEEN the work block and the tail,
   * which reproduces the stream order (work → pinned → tail).
   */
  pinned: DisplayItem[]
  /**
   * The last assistant item plus everything after it — the final answer, any
   * trailing items, and every UNRESOLVED pending-action panel lifted out of
   * the work segment (panels keep their relative order after the answer).
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

/** True for a top-level plan-step / subagent block — a pinned segment item. */
function isPinnedItem(it: DisplayItem): boolean {
  return it.kind === 'plan_step' || it.kind === 'subagent'
}

/** True for an UNRESOLVED pending-action panel (a blocking live affordance). */
function isUnresolvedActionItem(it: DisplayItem): boolean {
  if (!ACTION_KINDS.has(it.kind)) return false
  const { metadata } = (it as { message: ChatMessageUI }).message
  return metadata?.resolved !== true
}

/** Splits the flat display items of a session into { work, pinned, tail }. */
export function splitTurnWork(items: DisplayItem[]): TurnWorkSplit {
  const lastUserIdx = findLastIndex(items, it => it.kind === 'user')
  const lastAssistantIdx = findLastIndex(items, it => it.kind === 'assistant')

  // No assistant after the last user message: the turn is still running (or
  // died before answering). Everything after the user message is work; the
  // tail starts empty — no answer block, only lifted segments can appear in
  // the tail.
  let work: DisplayItem[]
  let tail: DisplayItem[]
  if (lastAssistantIdx === -1 || lastAssistantIdx <= lastUserIdx) {
    work = items.slice(lastUserIdx + 1)
    tail = []
  } else {
    work = items.slice(lastUserIdx + 1, lastAssistantIdx)
    tail = items.slice(lastAssistantIdx)
  }

  // Lift unresolved panels out of the work segment into the tail, preserving
  // their relative order (groupMessages() already sinks them to the end of
  // the root list, so appending keeps both orders stable).
  if (work.some(isUnresolvedActionItem)) {
    tail = [...tail, ...work.filter(isUnresolvedActionItem)]
    work = work.filter(it => !isUnresolvedActionItem(it))
  }

  // Lift top-level plan_step / subagent blocks out of the work segment into
  // the pinned segment, preserving stream order. Rendered between the work
  // block and the tail, work → pinned → tail reproduces the original stream.
  if (work.some(isPinnedItem)) {
    const pinned = work.filter(isPinnedItem)
    work = work.filter(it => !isPinnedItem(it))
    return { work, pinned, tail }
  }

  return { work, pinned: [], tail }
}

/** Array.prototype.findLastIndex is ES2023; this module targets a lower lib. */
function findLastIndex(items: DisplayItem[], pred: (it: DisplayItem) => boolean): number {
  for (let i = items.length - 1; i >= 0; i--) {
    if (pred(items[i]!)) return i
  }
  return -1
}
