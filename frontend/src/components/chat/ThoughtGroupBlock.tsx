import { useState } from 'react'
import { BrainCircuit, ChevronDown, ChevronRight } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'

interface ThoughtGroupBlockProps {
  thoughts: Array<{ content: string; reasoning?: string }>
}

export function ThoughtGroupBlock({ thoughts }: ThoughtGroupBlockProps) {
  const [isOpen, setIsOpen] = useState(false)

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen}>
      <CollapsibleTrigger className="flex items-center gap-1.5 text-muted-foreground hover:text-foreground transition-colors">
        {isOpen ? (
          <ChevronDown className="h-3.5 w-3.5" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5" />
        )}
        <BrainCircuit className="h-3.5 w-3.5" />
        <span className="text-sm">Reasoning ({thoughts.length})</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-2 pl-3 border-l-2 border-muted space-y-2 min-w-0">
          {thoughts.map((t, idx) => (
            <div key={t.content || `thought-${idx}`}>
              {t.reasoning && t.reasoning.trim() !== '' && (
                <p className="text-sm text-muted-foreground whitespace-pre-wrap">{t.reasoning}</p>
              )}
              {t.content && t.content.trim() !== '' && (
                <p className="text-muted-foreground text-sm whitespace-pre-wrap">{t.content}</p>
              )}
            </div>
          ))}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}
