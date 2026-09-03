import { useCallback, useMemo } from 'react'
import { Star } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/sessionStore'
import { useBookmarkStore, useSessionBookmarks } from '@/stores/bookmarkStore'
import { bookmarkKey, bookmarkDefaultTitle } from '@/lib/bookmarks'
import { useChatHoverBookmark } from './chatHoverStore'
import type { DisplayItem } from '@/types/messages'

interface BookmarkStarProps {
  item: DisplayItem
}

/**
 * BookmarkStar — the per-event bookmark toggle rendered in the left gutter of
 * a chat item (or inside the sticky pinned message).
 *
 * Unbookmarked: an outline star in the same muted color as Copy/Save, hidden by
 * default and revealed on hover. Visibility is driven EXCLUSIVELY, and
 * programmatically, by the {@link chatHoverStore} (a single active bookmark
 * across the whole chat): a star is visible only when the store's `bookmark`
 * equals this item's key. An inline `visibility` style (not Tailwind opacity,
 * not CSS `:hover`) toggles it, and hidden stars are never live hit-targets.
 *
 * Bookmarked: a filled yellow star, always visible. Clicking toggles the
 * bookmark in the per-session persistent store.
 */
export function BookmarkStar({ item }: BookmarkStarProps) {
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

  // Programmatic visibility via the single chat hover store. No CSS `:hover` /
  // `group-hover` / Tailwind opacity classes — see chatHoverStore.ts.
  const activeBookmark = useChatHoverBookmark()
  const isActiveReveal = activeBookmark === key
  const visible = isBookmarked || isActiveReveal
  const colorClass = isBookmarked
    ? 'text-highlight hover:text-highlight/70'
    : 'text-muted-foreground hover:text-foreground'

  return (
    <button
      type="button"
      onClick={handleClick}
      onKeyDown={(e) => e.stopPropagation()}
      title={isBookmarked ? 'Remove bookmark' : 'Add bookmark'}
      aria-label={isBookmarked ? 'Remove bookmark' : 'Add bookmark'}
      aria-pressed={isBookmarked}
      style={{ visibility: visible ? 'visible' : 'hidden' }}
      className={cn('inline-flex items-center justify-center rounded-sm p-0.5 shrink-0', colorClass)}
    >
      <Star className="h-3.5 w-3.5" fill={isBookmarked ? 'currentColor' : 'none'} />
    </button>
  )
}
