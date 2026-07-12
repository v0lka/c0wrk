import { useRef, useState, useLayoutEffect, useCallback } from 'react'
import type { DisplayItem } from '@/types/messages'
import { UserMessageContent } from '@/components/chat/UserMessageContent'
import { MessageFooter } from '@/components/chat/MessageFooter'

interface UserMessageProps {
  item: Extract<DisplayItem, { kind: 'user' }>
  isPinned?: boolean
  maxHeight?: number
}

export function UserMessage({ item, isPinned, maxHeight }: UserMessageProps) {
  const { content, timestamp } = item.message
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

  const isOverflowing = isPinned && maxHeight !== undefined && maxHeight > 0 && naturalHeight > maxHeight
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
      <div className="group flex flex-col items-end gap-1 max-w-[80%] ml-auto overflow-hidden min-w-0" data-message-id={item.message.id}>
        <div className="bg-secondary text-foreground rounded-2xl rounded-tr-sm px-4 py-2.5 overflow-hidden">
          <UserMessageContent content={content} />
        </div>
        <MessageFooter copyText={content} time={formattedTime} />
      </div>
    )
  }

  const shouldClip = isOverflowing && !effectiveExpanded
  return (
    <div
      className={`group relative transition-all duration-200 ${isOverflowing ? 'cursor-pointer' : ''}`}
      style={{ maxHeight: shouldClip ? maxHeight : undefined, overflow: shouldClip ? 'hidden' : undefined }}
      onClick={handleClick}
      onKeyDown={isOverflowing ? handleKeyDown : undefined}
      role={isOverflowing ? 'button' : undefined}
      tabIndex={isOverflowing ? 0 : undefined}
      aria-expanded={isOverflowing ? effectiveExpanded : undefined}
    >
      <div ref={contentRef} className="flex flex-col items-end gap-1 max-w-[80%] ml-auto overflow-hidden min-w-0">
        <div className="bg-secondary text-foreground rounded-2xl rounded-tr-sm px-4 py-2.5 overflow-hidden">
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
