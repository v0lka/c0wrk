import { useState } from 'react'
import { BrainCircuit, ChevronDown, ChevronRight } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'

interface ThoughtBlockProps {
  id: string
  stepNum: number
  content: string
}

export function ThoughtBlock({ content }: ThoughtBlockProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [showFull, setShowFull] = useState(false)

  const MAX_CHARS = 500
  const isLong = content.length > MAX_CHARS
  const displayContent = (!showFull && isLong) ? content.slice(0, MAX_CHARS) + '...' : content
  const isEmpty = !content || content.trim() === ''

  return (
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
          {isEmpty ? (
            <div className="flex items-center gap-2 text-muted-foreground">
              <span className="text-sm">Thinking</span>
              <span className="flex gap-1">
                <span className="w-1.5 h-1.5 rounded-full bg-muted-foreground animate-bounce [animation-delay:-0.3s]" />
                <span className="w-1.5 h-1.5 rounded-full bg-muted-foreground animate-bounce [animation-delay:-0.15s]" />
                <span className="w-1.5 h-1.5 rounded-full bg-muted-foreground animate-bounce" />
              </span>
            </div>
          ) : (
            <>
              <p className="text-sm text-muted-foreground whitespace-pre-wrap">
                {displayContent}
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
            </>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}
