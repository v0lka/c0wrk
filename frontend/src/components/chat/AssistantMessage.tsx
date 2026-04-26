import type { DisplayItem } from '@/types/messages'
import { MarkdownViewer } from '@/components/MarkdownViewer'

interface AssistantMessageProps {
  item?: Extract<DisplayItem, { kind: 'assistant' }>
  content?: string
  isStreaming?: boolean
}

export function AssistantMessage({ item, content: rawContent, isStreaming }: AssistantMessageProps) {
  const text = rawContent ?? item?.message.content ?? ''

  return (
    <div className="flex-1 min-w-0 overflow-hidden">
      <MarkdownViewer content={text} />
      {isStreaming && (
        <span className="inline-block w-2 h-4 bg-primary ml-1 animate-pulse" />
      )}
    </div>
  )
}
