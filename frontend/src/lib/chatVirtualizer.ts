// Imperative handle the virtualized transcript exposes so external navigation
// (a plan-step click, a bookmark jump) can reach a row that is not mounted.
//
// The virtualizer mounts only the rows intersecting the viewport (+ overscan),
// so ChatScrollManager's DOM lookup finds nothing for an off-screen target and
// the navigation silently does nothing. These helpers locate the TARGET ROW's
// index in the item list; VirtualizedChatList then scrolls the virtualizer to
// it, which mounts the row so the normal DOM positioning can run.

import { bookmarkKey } from './bookmarks'
import type { DisplayItem } from '@/types/messages'

/** Imperative navigation handle registered by VirtualizedChatList. */
export interface ChatVirtualizerHandle {
  /** Scroll the top-level row containing `key` (on itself or a nested child)
   *  to the top; returns false when no row carries the key. */
  scrollToKey: (key: string) => boolean
  /** Scroll the top-level plan-step row with `stepId` to the top; returns
   *  false when no such row exists. */
  scrollToStep: (stepId: string) => boolean
}

/** Whether `item` or any of its descendants carries the bookmark `key`. */
function subtreeHasKey(item: DisplayItem, key: string): boolean {
  if (bookmarkKey(item) === key) return true
  if (item.kind === 'plan_step' || item.kind === 'subagent') {
    return item.children.some((child) => subtreeHasKey(child, key))
  }
  return false
}

/**
 * Index of the top-level row whose subtree carries the bookmark `key`, or -1.
 * Bookmarks may anchor a NESTED child (a tool card inside a plan step), whose
 * DOM node lives inside its top-level ancestor's row — so the search descends
 * into plan-step/subagent children rather than matching only root keys.
 */
export function indexOfKey(items: DisplayItem[], key: string): number {
  return items.findIndex((item) => subtreeHasKey(item, key))
}

/**
 * Index of the top-level plan-step row with `stepId`, or -1. Plan-step blocks
 * are always root-level items (see groupMessages), so a root scan suffices.
 */
export function indexOfStep(items: DisplayItem[], stepId: string): number {
  return items.findIndex((item) => item.kind === 'plan_step' && item.stepId === stepId)
}

/**
 * Index of the user row leading the turn that owns the current scroll
 * position: the LAST user item whose row start sits at or above the viewport
 * top (`rowStart(index) <= scrollTop`), or -1 when the transcript is scrolled
 * above its first user row.
 *
 * Rows in the virtualized transcript are absolutely positioned, which disables
 * the native `position: sticky` pin of the turn's user message — so the list
 * re-creates the pin as an overlay and needs to know which turn is current.
 * Row starts are passed as a callback (backed by the virtualizer's measurement
 * cache — measured where seen, estimated elsewhere) so this helper stays pure
 * and unit-testable without a virtualizer instance.
 *
 * Scans backwards and returns at the first (highest) matching user row: the
 * turn owner is the closest user message above the viewport top, not the first
 * one in the transcript. A user row whose start is unknown (not yet in the
 * measurement cache) is skipped — an earlier user row may still own the turn.
 */
export function indexOfUserLeader(
  items: DisplayItem[],
  scrollTop: number,
  rowStart: (index: number) => number | undefined,
): number {
  for (let i = items.length - 1; i >= 0; i--) {
    if (items[i]!.kind !== 'user') continue
    const start = rowStart(i)
    if (start !== undefined && start <= scrollTop) return i
  }
  return -1
}
