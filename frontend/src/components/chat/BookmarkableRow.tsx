import { bookmarkKey } from '@/lib/bookmarks'
import { BookmarkStar } from './BookmarkStar'
import type { DisplayItem } from '@/types/messages'

/**
 * BookmarkableRow — a chat item's left gutter + content.
 *
 * The unbookmarked star's visibility is driven by the ChatHoverRegion
 * controller (single active bookmark across the whole chat). This row carries
 * `data-bookmark-id={bookmarkKey(item)}`, which the region resolves via
 * `closest('[data-bookmark-id]')` to reveal exactly the row under the pointer
 * and hide every other star. The `group` class is retained for other hover
 * revealers (e.g. the Copy/Save actions in MessageFooter), not for the star.
 */
export function BookmarkableRow({ item, content }: { item: DisplayItem; content: React.ReactNode }) {
  const key = bookmarkKey(item)
  return (
    <div
      data-bookmark-id={key}
      className="group relative flex items-start gap-2"
    >
      <div className="w-5 shrink-0 flex items-start">
        <BookmarkStar item={item} />
      </div>
      <div className="min-w-0 flex-1">{content}</div>
    </div>
  )
}
