import { useState } from 'react'
import { Lightbulb, Info, ChevronDown, ChevronRight } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'

interface ReflectionCardProps {
  id: string
  summary: string
  insights: string[]
  attempt: number
  maxAttempts: number
}

export function ReflectionCard({ summary, insights, attempt, maxAttempts }: ReflectionCardProps) {
  const [isOpen, setIsOpen] = useState(false)

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen}>
      <CollapsibleTrigger className="flex items-center gap-1.5 text-muted-foreground hover:text-foreground transition-colors group">
        {isOpen ? (
          <ChevronDown className="h-3.5 w-3.5" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5" />
        )}
        <Lightbulb className="h-3.5 w-3.5" />
        <span className="text-sm">Reflection</span>
        <span className="text-xs text-muted-foreground">
          (attempt {attempt}/{maxAttempts})
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-2 pl-3 border-l-2 border-muted space-y-2">
          <p className="text-sm text-muted-foreground">{summary}</p>
          {insights.length > 0 && (
            <div className="space-y-1">
              {insights.map((insight, idx) => (
                <div key={idx} className="flex items-start gap-2">
                  <Info className="h-3.5 w-3.5 text-muted-foreground mt-0.5 shrink-0" />
                  <span className="text-sm text-muted-foreground">{insight}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}
