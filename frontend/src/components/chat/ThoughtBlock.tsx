import { useState } from 'react'
import { BrainCircuit, ChevronDown, ChevronRight } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'

interface ThoughtBlockProps {
  content: string
  reasoning?: string
}

export function ThoughtBlock({ content, reasoning }: ThoughtBlockProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [showFull, setShowFull] = useState(false)

  const MAX_CHARS = 500
  const hasReasoning = !!reasoning && reasoning.trim() !== ''
  const isLong = hasReasoning && reasoning.length > MAX_CHARS
  const displayReasoning = hasReasoning
    ? (!showFull && isLong) ? reasoning.slice(0, MAX_CHARS) + '...' : reasoning
    : ''

  return (
    <div>
      {hasReasoning && (
        <Collapsible open={isOpen} onOpenChange={setIsOpen}>
          <CollapsibleTrigger className="flex items-center gap-1.5 text-muted-foreground hover:text-foreground transition-colors group">
            {isOpen ? (
              <ChevronDown className="h-3.5 w-3.5" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5" />
            )}
            <BrainCircuit className="h-3.5 w-3.5" />
            <span className="text-sm">Thought</span>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="mt-2 pl-3 border-l-2 border-muted min-w-0">
              <p className="text-sm text-muted-foreground whitespace-pre-wrap">
                {displayReasoning}
              </p>
              {isLong && (
                <button
                  onClick={(e) => {
                    e.stopPropagation()
                    setShowFull(!showFull)
                  }}
                  className="text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 active:bg-accent/70 rounded px-1 py-0.5 mt-1 transition-colors"
                >
                  {showFull ? 'Show less' : 'Show more'}
                </button>
              )}
            </div>
          </CollapsibleContent>
        </Collapsible>
      )}
      {content && content.trim() !== '' && (
        <p className="text-muted-foreground text-sm whitespace-pre-wrap">
          {content}
        </p>
      )}
    </div>
  )
}
