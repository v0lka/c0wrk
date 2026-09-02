import { useState } from 'react'
import { Target, FileText, ImageIcon, Zap } from 'lucide-react'
import type { DisplayItem } from '@/types/messages'
import { UserMessageContent } from '@/components/chat/UserMessageContent'
import { MessageFooter } from '@/components/chat/MessageFooter'
import { UserMessageMetaBadges } from '@/components/chat/UserMessageMetaBadges'
import { parseUserMessageMeta } from '@/lib/userMessageMeta'
import { cn } from '@/lib/utils'

interface UserMessageProps {
  item: Extract<DisplayItem, { kind: 'user' }>
  sticky?: boolean
  /** Bookmark star slot rendered inside the sticky floating row (left gutter). */
  bookmarkStar?: React.ReactNode
}

/**
 * Compact inline indicators shown in the collapsed (one-line) sticky state, so
 * the user can still see at a glance that the pinned message carried a goal,
 * document attachments, or images — without expanding it. The expanded state
 * renders the full UserMessageMetaBadges row instead.
 *
 *   🎯  ⚡  📄×N  🖼️×N  <truncated text>
 *
 * Icons are purely visual (no counts/text) to keep the single line tight; the
 * exact names/sizes are revealed on expand.
 */
function CollapsedMessageIndicators({
  meta,
}: {
  meta: ReturnType<typeof parseUserMessageMeta>
}): React.JSX.Element | null {
  const docCount = meta.attachments?.length ?? 0
  const imgCount = meta.images?.length ?? 0
  const hasGoal = meta.goal === true
  const hasNudge = meta.is_nudge === true

  if (!hasGoal && !hasNudge && docCount === 0 && imgCount === 0) return null

  return (
    <span className="inline-flex items-center gap-1 shrink-0">
      {hasGoal && (
        <Target
          className="size-3.5 shrink-0 text-[var(--color-highlight)]"
          aria-label="Goal"
        />
      )}
      {hasNudge && (
        <Zap
          className="size-3.5 shrink-0 text-info"
          aria-label="Nudge"
        />
      )}
      {docCount > 0 && (
        <span className="inline-flex items-center gap-0.5 text-muted-foreground" title={`${docCount} attachment${docCount > 1 ? 's' : ''}`}>
          <FileText className="size-3.5 shrink-0" />
          {docCount > 1 && <span className="text-xs leading-none">{docCount}</span>}
        </span>
      )}
      {imgCount > 0 && (
        <span className="inline-flex items-center gap-0.5 text-muted-foreground" title={`${imgCount} image${imgCount > 1 ? 's' : ''}`}>
          <ImageIcon className="size-3.5 shrink-0" />
          {imgCount > 1 && <span className="text-xs leading-none">{imgCount}</span>}
        </span>
      )}
    </span>
  )
}

/**
 * UserMessage — renders a user chat bubble.
 *
 * Non-sticky: a full, right-aligned bubble with metadata badges, rich content
 * and a footer.
 *
 * Sticky ("floating"): CSS `sticky top-0` within its user turn. It collapses to
 * a single truncated line by default and expands to the full message on click
 * (collapse again with another click). An opaque background row plus a gradient
 * fade strip below it "erase" chat content that scrolls underneath the floating
 * bubble — the same solid-to-transparent gradient technique used for the action
 * buttons in the session list — so the transition reads as a smooth fade rather
 * than a hard clip.
 */
export function UserMessage({ item, sticky = false, bookmarkStar }: UserMessageProps) {
  const { content, timestamp, metadata } = item.message
  const meta = parseUserMessageMeta(metadata)
  const formattedTime = new Date(timestamp).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
  })
  const [expanded, setExpanded] = useState(false)

  if (!sticky) {
    return (
      <div className="group flex justify-end" data-message-id={item.message.id}>
        <div className="relative flex flex-col gap-1 max-w-[80%] min-w-0">
          <div className="bg-secondary text-foreground rounded-2xl rounded-tr-sm px-4 py-2.5 w-full overflow-hidden min-w-0 break-words shrink-0">
            <UserMessageMetaBadges meta={meta} />
            <UserMessageContent content={content} />
          </div>
          <MessageFooter copyText={content} time={formattedTime} />
        </div>
      </div>
    )
  }

  const collapsed = !expanded
  // Single-line preview used while collapsed. Whitespace is collapsed so the
  // bubble reads as one line regardless of line breaks in the source content;
  // the rich rendering (file/skill chips, markdown) is restored on expand.
  const previewText = content.replace(/\s+/g, ' ').trim()

  const toggle = () => setExpanded((v) => !v)
  const handleClick = (e: React.MouseEvent) => {
    // Don't collapse when the user clicked an interactive descendant (file refs,
    // markdown links, the copy button) — let it perform its own action. Only
    // relevant in the expanded state, since collapsed content is truncated and
    // carries no active controls.
    if (expanded && (e.target as HTMLElement).closest('a, button, [role="link"]')) {
      return
    }
    toggle()
  }
  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      toggle()
    }
  }

  return (
    <div className="group/bm sticky top-0 z-10" data-message-id={item.message.id} data-bookmark-id={item.message.id}>
      {bookmarkStar && (
        <div className="absolute left-0 top-2 z-10">{bookmarkStar}</div>
      )}
      {/* Opaque background row (full width) hides chat content scrolling under
          the floating bubble across the whole row, not just behind the bubble. */}
      <div className="flex justify-end bg-background pt-3 pb-1">
        <div
          className={cn(
            'group relative ml-auto flex max-w-[80%] min-w-0 cursor-pointer flex-col gap-1',
            collapsed && 'select-none',
          )}
          role="button"
          tabIndex={0}
          aria-expanded={expanded}
          aria-label={expanded ? 'Collapse message' : 'Expand message'}
          title={expanded ? 'Collapse' : 'Expand'}
          onClick={handleClick}
          onKeyDown={handleKeyDown}
        >
          {collapsed ? (
            <div className="flex items-center gap-1.5 rounded-2xl rounded-tr-sm bg-secondary px-4 py-2 text-sm text-foreground shadow-md min-w-0">
              <CollapsedMessageIndicators meta={meta} />
              <span className="truncate min-w-0 flex-1">{previewText}</span>
            </div>
          ) : (
            <>
              <div className="w-full min-w-0 shrink-0 overflow-hidden break-words rounded-2xl rounded-tr-sm bg-secondary px-4 py-2.5 text-foreground shadow-md">
                <UserMessageMetaBadges meta={meta} isPinned />
                <UserMessageContent content={content} />
              </div>
              <MessageFooter copyText={content} time={formattedTime} />
            </>
          )}
        </div>
      </div>
      {/* Gradient erasure — a fixed-height strip that fades from the chat
          background to transparent, so content scrolling up toward the floating
          message dissolves smoothly instead of being hard-clipped. Mirrors the
          solid-to-transparent gradient used for session-list action buttons. */}
      <div className="pointer-events-none h-6 bg-gradient-to-b from-background to-transparent" />
    </div>
  )
}
