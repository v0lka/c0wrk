import type { DisplayItem } from '@/types/messages'
import { MarkdownViewer } from '@/components/MarkdownViewer'
import { MessageFooter } from '@/components/chat/MessageFooter'

interface AssistantMessageProps {
  item?: Extract<DisplayItem, { kind: 'assistant' }>
  content?: string
  isStreaming?: boolean
}

export function AssistantMessage({ item, content: rawContent, isStreaming }: AssistantMessageProps) {
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
}
