import { useRef, useState, useLayoutEffect, useCallback } from 'react'
import type { DisplayItem } from '@/types/messages'
import { UserMessageContent } from '@/components/chat/UserMessageContent'
import { MessageFooter } from '@/components/chat/MessageFooter'
import { UserMessageMetaBadges } from '@/components/chat/UserMessageMetaBadges'
import { parseUserMessageMeta } from '@/lib/userMessageMeta'

interface UserMessageProps {
  item: Extract<DisplayItem, { kind: 'user' }>
  isPinned?: boolean
  maxHeight?: number
}

export function UserMessage({ item, isPinned, maxHeight }: UserMessageProps) {
  const { content, timestamp, metadata } = item.message
  const meta = parseUserMessageMeta(metadata)
  const formattedTime = new Date(timestamp).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
  })

  const contentRef = useRef<HTMLDivElement>(null)
  const [naturalHeight, setNaturalHeight] = useState(0)
  const [expanded, setExpanded] = useState(false)

  useLayoutEffect(() => {
    if (!isPinned || !contentRef.current) return
    const el = contentRef.current
    const ro = new ResizeObserver(() => {
      if (el) setNaturalHeight(el.scrollHeight)
    })
    setNaturalHeight(el.scrollHeight)
    ro.observe(el)
    return () => ro.disconnect()
  }, [isPinned])

  const hasPinnedLimit = isPinned && maxHeight !== undefined && maxHeight > 0
  const isOverflowing = hasPinnedLimit && naturalHeight > maxHeight
  const effectiveExpanded = isOverflowing ? expanded : false
  const handleClick = useCallback(() => {
    if (isOverflowing) setExpanded(prev => !prev)
  }, [isOverflowing])

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      handleClick()
    }
  }, [handleClick])

  if (!isPinned) {
    return (
      <div className="group flex justify-end" data-message-id={item.message.id}>
        <div className="flex flex-col gap-1 max-w-[80%] min-w-0">
          <div className="bg-secondary text-foreground rounded-2xl rounded-tr-sm px-4 py-2.5 w-full overflow-hidden min-w-0 break-words">
            <UserMessageMetaBadges meta={meta} isPinned={isPinned} />
            <UserMessageContent content={content} />
          </div>
          <MessageFooter copyText={content} time={formattedTime} />
        </div>
      </div>
    )
  }

  // Apply the limit before the first ResizeObserver measurement as a
  // fail-safe. Once measured, short content can render at its natural height;
  // overflowing content remains clipped until explicitly expanded.
  const shouldClip = hasPinnedLimit && naturalHeight === 0
    ? true
    : isOverflowing && !effectiveExpanded
  return (
    <div
      className={`group relative flex justify-end transition-all duration-200 ${isOverflowing ? 'cursor-pointer' : ''}`}
      style={{ maxHeight: shouldClip ? maxHeight : undefined, overflow: shouldClip ? 'hidden' : undefined }}
      onClick={handleClick}
      onKeyDown={isOverflowing ? handleKeyDown : undefined}
      role={isOverflowing ? 'button' : undefined}
      tabIndex={isOverflowing ? 0 : undefined}
      aria-expanded={isOverflowing ? effectiveExpanded : undefined}
    >
      <div ref={contentRef} className="flex flex-col gap-1 max-w-[80%] min-w-0">
        <div className="bg-secondary text-foreground rounded-2xl rounded-tr-sm px-4 py-2.5 w-full overflow-hidden min-w-0 break-words">
          <UserMessageMetaBadges meta={meta} isPinned={isPinned} />
          <UserMessageContent content={content} />
        </div>
        <MessageFooter copyText={content} time={formattedTime} />
      </div>
      {shouldClip && (
        <div className="absolute bottom-0 left-0 right-0 h-16 pointer-events-none bg-gradient-to-b from-transparent to-background" />
      )}
    </div>
  )
}
