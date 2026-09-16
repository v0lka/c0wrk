import { useVirtualizer } from '@tanstack/react-virtual'
import type { ReactNode, RefObject } from 'react'
import type { DisplayItem } from '@/types/messages'
import { bookmarkKey } from '@/lib/bookmarks'
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
 * Trade-off: absolute positioning disables the `position: sticky` pinned user
 * message; ChatArea only virtualizes once the item count crosses a threshold,
 * so small/medium sessions keep the sticky behaviour.
 */
export function VirtualizedChatList({
  items,
  scrollRef,
  trailingContent,
  bookmarkable = true,
}: {
  items: DisplayItem[]
  scrollRef: RefObject<HTMLElement | null>
  trailingContent?: ReactNode
  bookmarkable?: boolean
}) {
  const virtualizer = useVirtualizer({
    count: items.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ESTIMATED_ROW_HEIGHT,
    overscan: OVERSCAN_ROWS,
    // Key by the item's stable bookmark key so measured heights survive a
    // prepend (older page) instead of being recomputed for every shifted index.
    getItemKey: (index) => {
      const item = items[index]
      return item ? bookmarkKey(item) : index
    },
  })

  return (
    <>
      <div style={{ height: virtualizer.getTotalSize(), width: '100%', position: 'relative' }}>
        {virtualizer.getVirtualItems().map((vi) => {
          const item = items[vi.index]
          if (!item) return null
          return (
            <div
              key={vi.key}
              data-index={vi.index}
              ref={virtualizer.measureElement}
              style={{
                position: 'absolute',
                top: 0,
                left: 0,
                width: '100%',
                transform: `translateY(${vi.start}px)`,
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
