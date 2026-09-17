import { useVirtualizer } from '@tanstack/react-virtual'
import { useCallback, useLayoutEffect, useRef } from 'react'
import type { ReactNode, RefObject } from 'react'
import type { DisplayItem } from '@/types/messages'
import { bookmarkKey } from '@/lib/bookmarks'
import { indexOfKey, indexOfStep, indexOfUserLeader, type ChatVirtualizerHandle } from '@/lib/chatVirtualizer'
import { ChatItem } from './ChatMessageRenderer'

/**
 * Windowed chat transcript. Mounts only the items intersecting the viewport
 * (plus `overscan` neighbours), so the DOM node count is a function of the
 * viewport height — not of the session's history length. Long sessions (tens of
 * thousands of rows) therefore stay responsive after paged history loading.
 *
 * Rendering of each row is delegated to {@link ChatItem} (the same registry and
 * bookmark wrapping the sequential list uses), so a message looks identical
 * whether it is virtualized or not. Rows are absolutely positioned and measured
 * dynamically (heights vary enormously — a one-line status vs a long tool
 * output), so the virtualizer's offsets converge on the real layout.
 *
 * Three measurement hardening rules keep the in-flow trailing block (stream
 * indicator / "Idle…") from overlapping the last rows:
 * - rows are sized via `offsetHeight` (LAYOUT px, zoom-safe) instead of the
 *   library default, which prefers the ResizeObserver `borderBoxSize` — a
 *   VISUAL-px value under the app-wide CSS zoom on `<html>`;
 * - every still-mounted row is re-measured synchronously in a layout effect
 *   when `items` changes (BEFORE paint), because a row that grew after mount
 *   (expanded thinking block, streamed text) is otherwise only picked up by
 *   the ResizeObserver after paint — one frame too late for `getTotalSize()`;
 * - that synchronous re-measure calls the virtualizer's public `resizeItem`
 *   directly, because `measureElement` skips measuring while `isScrolling` is
 *   set — and the tail-pinned auto-scroll of a live multi-subagent run keeps
 *   the viewport in exactly that state.
 *
 * Pinned user message: absolute positioning disables the native
 * `position: sticky` pin of a turn's user message, so the list re-creates it
 * as a single overlay pinned above the rows container (see the overlay logic
 * below). The overlay is `UserMessage` in sticky mode — it collapses to one
 * line and expands on click exactly like the sequential renderer's pin.
 */
export function VirtualizedChatList({
  items,
  scrollRef,
  trailingContent,
  bookmarkable = true,
  virtualizerRef,
}: {
  items: DisplayItem[]
  scrollRef: RefObject<HTMLElement | null>
  trailingContent?: ReactNode
  bookmarkable?: boolean
  /**
   * Optional handle sink: when provided, the list registers an imperative
   * navigation handle here so ChatScrollManager can scroll to a row that is
   * outside the mounted window (the virtualizer mounts only visible rows, so a
   * DOM lookup cannot find an off-screen step/bookmark target).
   */
  virtualizerRef?: React.RefObject<ChatVirtualizerHandle | null>
}) {
  const virtualizer = useVirtualizer<HTMLElement, HTMLDivElement>({
    count: items.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ESTIMATED_ROW_HEIGHT,
    overscan: OVERSCAN_ROWS,
    // Zoom-safe sizing: `offsetHeight` reports LAYOUT px under the app-wide CSS
    // zoom on <html>, while the library default prefers the ResizeObserver
    // `borderBoxSize` — VISUAL px (layout × zoom). Mixing the two coordinate
    // spaces desyncs `getTotalSize()` from the real content height whenever
    // zoom ≠ 100%, so the in-flow trailing block overlaps the last rows.
    measureElement: (el) => el.offsetHeight,
    // Key by the item's stable bookmark key so measured heights survive a
    // prepend (older page) instead of being recomputed for every shifted index.
    getItemKey: (index) => {
      const item = items[index]
      return item ? bookmarkKey(item) : index
    },
  })

  // Row nodes currently mounted, accumulated by the `measureRow` ref callback.
  // `useLayoutEffect` below consumes the set to re-measure every still-mounted
  // row synchronously when `items` changes.
  const rowNodesRef = useRef(new Set<HTMLDivElement>())

  // Wrapper over the virtualizer's ref callback that records mounted nodes.
  // The virtualizer instance is stable across renders, so this callback is too
  // — React never detaches/reattaches the row refs on a plain re-render.
  const measureRow = useCallback(
    (el: HTMLDivElement | null) => {
      if (el) {
        rowNodesRef.current.add(el)
      } else {
        // React calls the ref with null on unmount without naming the node;
        // prune whatever left the DOM (mirrors the virtualizer's own null-path
        // cache cleanup performed by `virtualizer.measureElement(null)` below).
        for (const node of rowNodesRef.current) {
          if (!node.isConnected) rowNodesRef.current.delete(node)
        }
      }
      virtualizer.measureElement(el)
    },
    [virtualizer],
  )

  // A row can grow AFTER mount (expanded thinking block, streamed text). The
  // ResizeObserver reports that growth only after paint — one frame too late:
  // `getTotalSize()` still reflects the stale row heights, so the in-flow
  // trailing block overlaps the last rows (most visible with several live
  // subagents pinning the tail in view).
  //
  // The re-measure deliberately does NOT go through `virtualizer.measureElement`:
  // the library gates that path on `!isScrolling` and silently skips the
  // `resizeItem` call while scroll events stream (a 150 ms quiescence window).
  // During a live multi-subagent run the stick-to-bottom auto-scroll keeps the
  // viewport scrolling almost continuously — exactly when the correction is
  // needed — so the gated path would no-op precisely in the bug's repro
  // conditions. Feeding each measurement straight to the public `resizeItem`
  // (same zoom-safe LAYOUT-px `offsetHeight` the custom measureElement uses)
  // converges the offsets and the total size in the same commit, BEFORE paint.
  useLayoutEffect(() => {
    for (const node of rowNodesRef.current) {
      const index = Number.parseInt(node.dataset.index ?? '', 10)
      if (Number.isInteger(index) && index >= 0 && index < items.length) {
        virtualizer.resizeItem(index, node.offsetHeight)
      }
    }
  }, [items, virtualizer])

  // Latest items for the (once-registered) navigation handle, so scrolling to a
  // target always resolves against the current tree without re-registering.
  const itemsRef = useRef(items)
  itemsRef.current = items

  // Register the imperative navigation handle. `scrollToIndex` re-renders the
  // virtualizer window around the target row, which mounts it; the caller then
  // positions it with the sticky-bar compensation.
  useLayoutEffect(() => {
    if (!virtualizerRef) return
    const handle: ChatVirtualizerHandle = {
      scrollToKey: (key) => {
        const index = indexOfKey(itemsRef.current, key)
        if (index < 0) return false
        virtualizer.scrollToIndex(index, { align: 'start' })
        return true
      },
      scrollToStep: (stepId) => {
        const index = indexOfStep(itemsRef.current, stepId)
        if (index < 0) return false
        virtualizer.scrollToIndex(index, { align: 'start' })
        return true
      },
    }
    virtualizerRef.current = handle
    return () => {
      if (virtualizerRef.current === handle) virtualizerRef.current = null
    }
  }, [virtualizerRef, virtualizer])

  // --- Pinned user-message overlay -------------------------------------------------
  //
  // Rows are absolutely positioned, which neutralizes `position: sticky` — so
  // the pin of the current turn's user message is re-created as ONE overlay
  // rendered before the rows container, while the natural row is hidden with
  // `visibility: hidden` (its slot and measurements stay untouched).
  //
  // Reading `scrollTop` during render is safe here: the react-virtual adapter
  // re-renders this component exactly when the overlay state can change — on
  // every visible-range change (a row crossing the viewport top) and on the
  // isScrolling start/end toggles — so the value is fresh at every moment the
  // pin could engage or release.
  const scrollTop = scrollRef.current?.scrollTop ?? 0
  const rowStart = (index: number): number | undefined => virtualizer.measurementsCache[index]?.start
  const leaderIndex = indexOfUserLeader(items, scrollTop, rowStart)
  const leaderStart = leaderIndex >= 0 ? rowStart(leaderIndex) : undefined
  // Pin engages only once the leader's top edge is strictly above the viewport
  // top; at the turn start (scrollTop <= start) the natural row is visible and
  // the overlay stays unmounted.
  const pinnedIndex = leaderStart !== undefined && leaderStart < scrollTop ? leaderIndex : -1
  const pinnedItem = pinnedIndex >= 0 ? items[pinnedIndex] : undefined

  return (
    <>
      {/* Native sticky pin (zoom-safe): ALWAYS mounted as the first child, at
          ZERO height, so engaging or releasing the pin never changes the flow
          (mounting a full-height overlay would push the rows container down by
          the bar height and flip its `space-y-4` first-child margin, visibly
          jumping the transcript). The wrapper carries the `sticky top-0` pin;
          its containing block is the transcript content wrapper, so it glues to
          the scrollport top while the transcript scrolls underneath, and the
          bar inside overflows the 0-height box downward (overflow visible).
          `UserMessage` sticky mode carries `data-sticky-user-message`, so
          chatScroll.ts's overlay compensation keeps working. Keyed by the
          bookmark key: when the turn switches, the overlay remounts and its
          collapse/expand state resets. */}
      <div className="sticky top-0 z-10 h-0 min-h-0">
        {pinnedItem && (
          <ChatItem key={bookmarkKey(pinnedItem)} item={pinnedItem} sticky bookmarkable={bookmarkable} />
        )}
      </div>
      <div style={{ height: virtualizer.getTotalSize(), width: '100%', position: 'relative' }}>
        {virtualizer.getVirtualItems().map((vi) => {
          const item = items[vi.index]
          if (!item) return null
          return (
            <div
              key={vi.key}
              data-index={vi.index}
              ref={measureRow}
              style={{
                position: 'absolute',
                top: 0,
                left: 0,
                width: '100%',
                transform: `translateY(${vi.start}px)`,
                // The overlay takes over the pinned user row's visuals; hiding
                // (not unmounting) keeps the row's slot and measured height.
                ...(vi.index === pinnedIndex ? { visibility: 'hidden' as const } : {}),
              }}
            >
              {/* pb-4 reproduces the `space-y-4` gap of the sequential list. */}
              <div className="pb-4">
                <ChatItem item={item} bookmarkable={bookmarkable} />
              </div>
            </div>
          )
        })}
      </div>
      {trailingContent}
    </>
  )
}

/** Initial row-height estimate before measurement (px). */
const ESTIMATED_ROW_HEIGHT = 120
/** Neighbour rows rendered beyond the viewport on each side. */
const OVERSCAN_ROWS = 6
