import { useRef, useState, useEffect, useLayoutEffect, useCallback } from 'react'

interface UserMessageProps {
  content: string
  timestamp: number
  isPinned?: boolean
  maxHeight?: number
}

export function UserMessage({ content, timestamp, isPinned, maxHeight }: UserMessageProps) {
  const formattedTime = new Date(timestamp).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
  })

  const contentRef = useRef<HTMLDivElement>(null)
  const [naturalHeight, setNaturalHeight] = useState(0)
  const [expanded, setExpanded] = useState(false)

  // Measure the natural height of the content
  useLayoutEffect(() => {
    if (!isPinned || !contentRef.current) return
    // Feature detection for ResizeObserver
    if (typeof ResizeObserver === 'undefined') return

    const measureHeight = () => {
      if (contentRef.current) {
        setNaturalHeight(contentRef.current.scrollHeight)
      }
    }

    measureHeight()

    const ro = new ResizeObserver(measureHeight)
    ro.observe(contentRef.current)
    return () => ro.disconnect()
  }, [isPinned])

  const isOverflowing = isPinned && maxHeight !== undefined && maxHeight > 0 && naturalHeight > maxHeight

  const handleClick = useCallback(() => {
    if (isOverflowing) {
      setExpanded(prev => !prev)
    } else {
      setExpanded(false)
    }
  }, [isOverflowing])

  const handleBlur = useCallback((e: React.FocusEvent) => {
    // Only collapse if focus moved outside this component
    if (e.relatedTarget && !e.currentTarget.contains(e.relatedTarget as Node)) {
      setExpanded(false)
    }
  }, [])

  // Reset expanded state when maxHeight changes and content no longer overflows
  useEffect(() => {
    if (!isOverflowing && expanded) {
      setExpanded(false)
    }
  }, [isOverflowing, expanded])

  // Non-pinned messages render as before
  if (!isPinned) {
    return (
      <div className="flex flex-col items-end gap-1 max-w-[80%] ml-auto">
        <div className="bg-muted text-foreground rounded-2xl rounded-tr-sm px-4 py-2.5">
          <p className="text-sm whitespace-pre-wrap">{content}</p>
        </div>
        <span className="text-xs text-muted-foreground px-1">{formattedTime}</span>
      </div>
    )
  }

  // Pinned message with collapsible behavior
  const shouldClip = isOverflowing && !expanded

  return (
    <div
      className={`relative transition-all duration-200 ${
        isOverflowing || expanded ? 'cursor-pointer' : ''
      }`}
      style={{
        maxHeight: shouldClip ? maxHeight : undefined,
        overflow: shouldClip ? 'hidden' : undefined,
      }}
      onClick={handleClick}
      onBlur={handleBlur}
      tabIndex={isOverflowing ? 0 : undefined}
      role={isOverflowing ? 'button' : undefined}
      aria-expanded={isOverflowing ? expanded : undefined}
    >
      <div ref={contentRef} className="flex flex-col items-end gap-1 max-w-[80%] ml-auto">
        <div className="bg-muted text-foreground rounded-2xl rounded-tr-sm px-4 py-2.5">
          <p className="text-sm whitespace-pre-wrap">{content}</p>
        </div>
        <span className="text-xs text-muted-foreground px-1">{formattedTime}</span>
      </div>

      {/* Gradient fade overlay when clipped */}
      {shouldClip && (
        <div
          className="absolute bottom-0 left-0 right-0 h-16 pointer-events-none bg-gradient-to-b from-transparent to-background"
        />
      )}
    </div>
  )
}
