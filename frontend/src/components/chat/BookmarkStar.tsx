import { useCallback, useMemo } from 'react'
import { Star } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/sessionStore'
import { useBookmarkStore, useSessionBookmarks } from '@/stores/bookmarkStore'
import { bookmarkKey, bookmarkDefaultTitle } from '@/lib/bookmarks'
import type { DisplayItem } from '@/types/messages'

interface BookmarkStarProps {
  item: DisplayItem
  /**
   * Whether the containing row is hovered. When provided it drives the
   * unbookmarked star's visibility directly, so that hovering a parent row does
   * NOT cascade to nested rows (avoids the CSS group-hover leak where a parent
   * block revealed every descendant star). When omitted it falls back to the
   * ancestor `group/bm` hover — used by the sticky pinned user message, which
   * contains no nested bookmarkable rows.
   */
  hovered?: boolean
}

/**
 * BookmarkStar — the per-event bookmark toggle rendered in the left gutter of
 * a chat item (or inside the sticky pinned message).
 *
 * Unbookmarked: an outline star in the same muted color as Copy/Save, revealed
 * on item hover. Bookmarked: a filled yellow star, always visible. Clicking
 * toggles the bookmark in the per-session persistent store.
 */
export function BookmarkStar({ item, hovered }: BookmarkStarProps) {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const bookmarks = useSessionBookmarks(activeSessionId)
  const addBookmark = useBookmarkStore((s) => s.addBookmark)
  const removeBookmark = useBookmarkStore((s) => s.removeBookmark)

  const key = bookmarkKey(item)
  const bookmark = useMemo(() => bookmarks.find((b) => b.event_key === key) ?? null, [bookmarks, key])
  const isBookmarked = bookmark !== null

  const handleClick = useCallback(
    (e: React.MouseEvent) => {
      e.stopPropagation()
      if (!activeSessionId) return
      if (isBookmarked && bookmark) {
        void removeBookmark(activeSessionId, bookmark.id)
      } else {
        void addBookmark(activeSessionId, key, bookmarkDefaultTitle(item))
      }
    },
    [activeSessionId, isBookmarked, bookmark, key, item, addBookmark, removeBookmark],
  )

  return (
    <button
      type="button"
      onClick={handleClick}
      onKeyDown={(e) => e.stopPropagation()}
      title={isBookmarked ? 'Remove bookmark' : 'Add bookmark'}
      aria-label={isBookmarked ? 'Remove bookmark' : 'Add bookmark'}
      aria-pressed={isBookmarked}
      className={cn(
        'inline-flex items-center justify-center rounded-sm p-0.5 transition-opacity',
        isBookmarked
          ? 'opacity-100 text-highlight hover:text-highlight/70'
          : cn(
              'text-muted-foreground hover:text-foreground focus-visible:opacity-100',
              hovered !== undefined
                ? hovered
                  ? 'opacity-100'
                  : 'opacity-0'
                : 'opacity-0 group-hover/bm:opacity-100',
            ),
      )}
    >
      <Star className="h-3.5 w-3.5" fill={isBookmarked ? 'currentColor' : 'none'} />
    </button>
  )
}
