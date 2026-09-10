import { useState, useMemo, useRef } from 'react'
import { ChevronDown, ChevronRight, Bookmark, Pencil, Trash2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/sessionStore'
import { useUIStore } from '@/stores/uiStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useBookmarkStore, useSessionBookmarks } from '@/stores/bookmarkStore'
import { useScrollContext } from './ScrollContext'
import { ChatMessageRenderer } from './ChatMessageRenderer'
import { indexDisplayItems } from '@/lib/bookmarks'
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'
import { ItemAction, ItemActions } from '@/components/layout/ItemAction'
import { Input } from '@/components/ui/input'
import type { SessionBookmark } from '@/types/models'
import type { DisplayItem } from '@/types/messages'

/**
 * BookmarksPanel — collapsible panel above the message input listing the active
 * session's bookmarks. Each row shows the full bookmark title clipped to the
 * panel width (CSS ellipsis, no fixed character cap; a hover preview of the
 * event is rendered as in chat), an inline rename affordance and a delete
 * action, mirroring the session-list item actions. Clicking a row scrolls the
 * chat to the bookmarked event.
 */
export function BookmarksPanel({ displayItems }: { displayItems: DisplayItem[] }) {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const bookmarks = useSessionBookmarks(activeSessionId)
  const sidebarCollapsed = useUIStore((s) => s.sidebarCollapsed)
  const viewerCollapsed = useFileViewerStore((s) => s.collapsed)
  const scrollToBookmark = useScrollContext().scrollToBookmark

  const [open, setOpen] = useState(true)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const renameRef = useRef<HTMLInputElement>(null)

  const itemIndex = useMemo(() => indexDisplayItems(displayItems), [displayItems])

  if (!activeSessionId || bookmarks.length === 0) return null

  const startRename = (b: SessionBookmark) => {
    setRenamingId(b.id)
    setRenameValue(b.title)
    setTimeout(() => renameRef.current?.focus(), 50)
  }

  const commitRename = async () => {
    if (!activeSessionId || !renamingId) {
      setRenamingId(null)
      return
    }
    const title = renameValue.trim()
    if (title) {
      await useBookmarkStore.getState().renameBookmark(activeSessionId, renamingId, title)
    }
    setRenamingId(null)
  }

  const cancelRename = () => setRenamingId(null)

  return (
    <div className={cn(
      'border-t border-x border-border bg-card',
      sidebarCollapsed && 'ml-1',
      viewerCollapsed && 'mr-1',
    )}>
      <div className="group">
        <button
          onClick={() => setOpen(!open)}
          className="flex items-center gap-2 w-full px-3 py-2 text-left text-foreground hover:bg-muted transition-colors rounded-sm"
        >
          <span className="opacity-0 group-hover:opacity-100 transition-opacity inline-flex">
            {open
              ? <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
              : <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />}
          </span>
          <Bookmark className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-sm font-medium">Bookmarks</span>
          <span className="text-xs text-muted-foreground">{bookmarks.length}</span>
        </button>
        {open && (
          <div className="max-h-48 overflow-y-auto px-3 pb-2 custom-scrollbar">
            {bookmarks.map((b) => (
              <BookmarkRow
                key={b.id}
                bookmark={b}
                item={itemIndex.get(b.event_key)}
                renaming={renamingId === b.id}
                renameValue={renameValue}
                renameRef={renameRef}
                onRenameValue={setRenameValue}
                onStartRename={() => startRename(b)}
                onCommitRename={commitRename}
                onCancelRename={cancelRename}
                onNavigate={scrollToBookmark ? () => scrollToBookmark(b.event_key) : undefined}
              />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

interface BookmarkRowProps {
  bookmark: SessionBookmark
  item: DisplayItem | undefined
  renaming: boolean
  renameValue: string
  renameRef: React.RefObject<HTMLInputElement | null>
  onRenameValue: (v: string) => void
  onStartRename: () => void
  onCommitRename: () => void
  onCancelRename: () => void
  onNavigate: (() => void) | undefined
}

function BookmarkRow({
  bookmark,
  item,
  renaming,
  renameValue,
  renameRef,
  onRenameValue,
  onStartRename,
  onCommitRename,
  onCancelRename,
  onNavigate,
}: BookmarkRowProps) {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const removeBookmark = useBookmarkStore((s) => s.removeBookmark)

  if (renaming) {
    return (
      <div className="px-1 py-0.5">
        <Input
          ref={renameRef}
          value={renameValue}
          onChange={(e) => onRenameValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') onCommitRename()
            if (e.key === 'Escape') onCancelRename()
          }}
          onBlur={onCommitRename}
          className="h-7 text-sm"
        />
      </div>
    )
  }

  return (
    <div className="group/item relative flex w-full items-center gap-2 py-0.5 pl-2 border-l border-border text-xs hover:bg-muted/40 transition-colors">
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            onClick={onNavigate}
            disabled={!onNavigate}
            className="min-w-0 flex-1 truncate text-left text-foreground"
          >
            {bookmark.title}
          </button>
        </TooltipTrigger>
        <TooltipContent
          align="start"
          sideOffset={8}
          collisionPadding={32}
          avoidCollisions={true}
          updatePositionStrategy="always"
          className="max-w-md w-auto max-h-[min(70vh,calc(var(--radix-tooltip-content-available-height)-32px))] overflow-y-auto custom-scrollbar p-3"
        >
          {item
            ? <ChatMessageRenderer items={[item]} bookmarkable={false} />
            : <span className="text-muted-foreground">Event not available</span>}
        </TooltipContent>
      </Tooltip>

      <ItemActions>
        <ItemAction label="Rename" onClick={onStartRename}>
          <Pencil className="size-3 text-info" />
        </ItemAction>
        <ItemAction label="Delete" onClick={() => { if (activeSessionId) void removeBookmark(activeSessionId, bookmark.id) }}>
          <Trash2 className="size-3 text-destructive" />
        </ItemAction>
      </ItemActions>
    </div>
  )
}
