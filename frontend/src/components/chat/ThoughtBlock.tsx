import React, { useState } from 'react'
import { BrainCircuit } from 'lucide-react'
import { CollapsibleBlock } from '@/components/chat/CollapsibleBlock'
import { bookmarkKey } from '@/lib/bookmarks'
import { Markdown } from '@/lib/markdownConfig'
import type { DisplayItem } from '@/types/messages'

type ThoughtItem = Extract<DisplayItem, { kind: 'thought' }>

const MAX_CHARS = 500

export const ThoughtBlock = React.memo(function ThoughtBlock({ item }: { item: ThoughtItem }) {
  const [showFull, setShowFull] = useState(false)

  const hasReasoning = !!item.reasoning && item.reasoning.trim() !== ''
  const isLong = hasReasoning && item.reasoning!.length > MAX_CHARS
  const displayReasoning = hasReasoning
    ? (!showFull && isLong ? item.reasoning!.slice(0, MAX_CHARS) + '...' : item.reasoning!)
    : ''

  return (
    <div>
      {hasReasoning && (
        <CollapsibleBlock
          icon={<BrainCircuit className="h-3.5 w-3.5" />}
          label="Reasoning"
          revealId={bookmarkKey(item)}
        >
          <div className="mt-2 pl-3 border-l-2 border-muted min-w-0">
            <Markdown content={displayReasoning} compact />
            {isLong && (
              <button
                onClick={(e) => { e.stopPropagation(); setShowFull(!showFull) }}
                className="text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 rounded px-1 py-0.5 mt-1 transition-colors"
              >
                {showFull ? 'Show less' : 'Show more'}
              </button>
            )}
          </div>
        </CollapsibleBlock>
      )}
      {item.content && item.content.trim() !== '' && (
        <div className={hasReasoning ? 'mt-3' : ''}>
          <Markdown content={item.content} compact />
        </div>
      )}
    </div>
  )
})
