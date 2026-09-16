import React from 'react'
import type { DisplayItem } from '@/types/messages'
import { areDisplayItemsEqual } from '@/lib/displayItemStability'
import { MarkdownViewer } from '@/components/MarkdownViewer'
import { MessageFooter } from '@/components/chat/MessageFooter'

interface AssistantMessageProps {
  item?: Extract<DisplayItem, { kind: 'assistant' }>
  content?: string
  isStreaming?: boolean
}

/**
 * Memoized so a message that did not change is not re-rendered (and its
 * Markdown not re-parsed) when a sibling message is appended or updated. The
 * comparator compares the item's stable payload (`item.message` identity) and
 * the plain `content`/`isStreaming` props instead of the item object identity,
 * which `groupMessages` rebuilds on every store change.
 */
export const AssistantMessage = React.memo(function AssistantMessage({
  item,
  content: rawContent,
  isStreaming,
}: AssistantMessageProps) {
  const text = rawContent ?? item?.message.content ?? ''
  const timestamp = item?.message.timestamp
  const formattedTime = timestamp
    ? new Date(timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : undefined
  // The copy affordance is only relevant for finished messages that actually
  // carry answer text — not the in-progress streaming bubble.
  const canCopy = !isStreaming && text.trim().length > 0

  return (
    <div className="group flex-1 min-w-0 overflow-hidden">
      <MarkdownViewer content={text} />
      {isStreaming && (
        <span className="inline-block w-2 h-4 bg-primary ml-1 animate-pulse" />
      )}
      {!isStreaming && (
        <MessageFooter copyText={canCopy ? text : undefined} time={formattedTime} className="mt-1" />
      )}
    </div>
  )
}, assistantMessagePropsEqual)

function assistantMessagePropsEqual(
  prev: AssistantMessageProps,
  next: AssistantMessageProps,
): boolean {
  if (prev.isStreaming !== next.isStreaming) return false
  // Streaming bubbles carry `content` (not `item`); the finished bubble carries
  // `item`. Both are compared by value / stable message identity.
  if (prev.content !== next.content) return false
  if (prev.item === next.item) return true
  if (!prev.item || !next.item) return false
  return areDisplayItemsEqual(prev.item, next.item)
}
