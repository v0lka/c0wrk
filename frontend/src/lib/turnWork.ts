// Turn splitting for the chat transcript: separates the work block of the
// latest turn from its tail (final answer + live panels).
//
// A turn is everything after the LAST user message. Within a turn the final
// `assistant` item is the model's answer; everything between the user message
// and that answer (thoughts, tool cards, plan steps, intermediate assistant
// texts, service markers, …) is the turn's WORK — the activity that produced
// the answer. Everything from the final assistant item onward is the TAIL:
// the answer itself plus whatever follows it (sunken unresolved HITL panels,
// the active checklist) that must stay pinned below the answer.
//
// Pure data transform: no React, no stores, no mutation of the input.

import type { DisplayItem } from '@/types/messages'

export interface TurnWorkSplit {
  /**
   * Items between the last user message and the last assistant item
   * (both boundaries excluded) — the activity block of the latest turn.
   * Empty when the turn is a plain chat (user → answer, nothing between).
   */
  work: DisplayItem[]
  /**
   * The last assistant item plus everything after it: the final answer and
   * any trailing unresolved panels. Empty when the turn has no assistant
   * item yet (everything then belongs to `work`).
   */
  tail: DisplayItem[]
}

/** Splits the flat display items of a session into { work, tail }. */
export function splitTurnWork(items: DisplayItem[]): TurnWorkSplit {
  const lastUserIdx = findLastIndex(items, it => it.kind === 'user')
  const lastAssistantIdx = findLastIndex(items, it => it.kind === 'assistant')

  // No assistant after the last user message: the turn is still running (or
  // died before answering). Everything after the user message is work; the
  // tail stays empty so no answer block is rendered.
  if (lastAssistantIdx === -1 || lastAssistantIdx <= lastUserIdx) {
    return { work: items.slice(lastUserIdx + 1), tail: [] }
  }

  return {
    work: items.slice(lastUserIdx + 1, lastAssistantIdx),
    tail: items.slice(lastAssistantIdx),
  }
}

/** Array.prototype.findLastIndex is ES2023; this module targets a lower lib. */
function findLastIndex(items: DisplayItem[], pred: (it: DisplayItem) => boolean): number {
  for (let i = items.length - 1; i >= 0; i--) {
    if (pred(items[i]!)) return i
  }
  return -1
}
