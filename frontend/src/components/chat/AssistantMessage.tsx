import type { DisplayItem } from '@/types/messages'
import { MarkdownViewer } from '@/components/MarkdownViewer'
import { CopyButton } from '@/components/chat/CopyButton'

interface AssistantMessageProps {
  item?: Extract<DisplayItem, { kind: 'assistant' }>
  content?: string
  isStreaming?: boolean
}

export function AssistantMessage({ item, content: rawContent, isStreaming }: AssistantMessageProps) {
  const text = rawContent ?? item?.message.content ?? ''
  // The copy affordance is only relevant for finished messages that actually
  // carry answer text — not the in-progress streaming bubble.
  const canCopy = !isStreaming && text.trim().length > 0

  return (
    <div className="group flex-1 min-w-0 overflow-hidden">
      <MarkdownViewer content={text} />
      {isStreaming && (
        <span className="inline-block w-2 h-4 bg-primary ml-1 animate-pulse" />
      )}
      {canCopy && (
        <div className="mt-1 max-h-0 overflow-hidden opacity-0 group-hover:max-h-10 group-hover:opacity-100 transition-all duration-150 ease-out">
          <CopyButton text={text} />
        </div>
      )}
    </div>
  )
}
