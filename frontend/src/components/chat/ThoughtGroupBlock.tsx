import { BrainCircuit } from 'lucide-react'
import { CollapsibleBlock } from '@/components/chat/CollapsibleBlock'
import { bookmarkKey } from '@/lib/bookmarks'
import { Markdown } from '@/lib/markdownConfig'
import type { DisplayItem } from '@/types/messages'

type ThoughtGroupItem = Extract<DisplayItem, { kind: 'thought_group' }>

interface ThoughtGroupBlockProps {
  item: ThoughtGroupItem
}

export function ThoughtGroupBlock({ item }: ThoughtGroupBlockProps) {
  return (
    <CollapsibleBlock
      icon={<BrainCircuit className="h-3.5 w-3.5" />}
      label={`Reasoning (${item.thoughts.length})`}
      revealId={bookmarkKey(item)}
    >
      <div className="mt-2 pl-3 border-l-2 border-muted space-y-2 min-w-0">
        {item.thoughts.map((t, idx) => {
          const hasReasoning = !!t.reasoning && t.reasoning.trim() !== ''
          return (
            <div key={t.reasoning || t.content ? `thought-${t.reasoning?.slice(0, 16) ?? t.content?.slice(0, 16)}-${idx}` : `thought-${idx}`}>
              {hasReasoning && (
                <Markdown content={t.reasoning!} compact />
              )}
              {t.content && t.content.trim() !== '' && (
                <div className={hasReasoning ? 'mt-1.5' : ''}>
                  <Markdown content={t.content} compact />
                </div>
              )}
            </div>
          )
        })}
      </div>
    </CollapsibleBlock>
  )
}
