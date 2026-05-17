import { BrainCircuit } from 'lucide-react'
import { CollapsibleBlock } from '@/components/chat/CollapsibleBlock'
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
    >
      <div className="mt-2 pl-3 border-l-2 border-muted space-y-2 min-w-0">
        {item.thoughts.map((t, idx) => {
          const hasReasoning = !!t.reasoning && t.reasoning.trim() !== ''
          return (
            <div key={`thought-${idx}`}>
              {hasReasoning && (
                <p className="text-sm text-muted-foreground whitespace-pre-wrap">{t.reasoning}</p>
              )}
              {t.content && t.content.trim() !== '' && (
                <p className={`text-muted-foreground text-sm whitespace-pre-wrap${hasReasoning ? ' mt-1.5' : ''}`}>{t.content}</p>
              )}
            </div>
          )
        })}
      </div>
    </CollapsibleBlock>
  )
}
