import { useState } from 'react'
import { AlertTriangle, ChevronDown, ChevronRight } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'

interface ReflectionBlockProps {
  summary: string
  suggestedAction: string
  rootCause: string
  failureAnalysis: string
  actionPlan: string
  reasoning: string
  hypotheses: string[]
  attempt: number
  maxAttempts: number
}

const actionBadgeColors: Record<string, string> = {
  retry: 'bg-amber-500/15 text-amber-400 border-amber-500/30',
  replan: 'bg-blue-500/15 text-blue-400 border-blue-500/30',
  abort: 'bg-red-500/15 text-red-400 border-red-500/30',
}

export function ReflectionBlock({
  summary,
  suggestedAction,
  rootCause,
  failureAnalysis,
  actionPlan,
  reasoning,
  hypotheses,
  attempt,
  maxAttempts,
}: ReflectionBlockProps) {
  const [isOpen, setIsOpen] = useState(false)

  const badgeColor = actionBadgeColors[suggestedAction] ?? 'bg-muted text-muted-foreground border-border'
  const hasDetails = rootCause || actionPlan || failureAnalysis || reasoning || hypotheses.length > 0

  return (
    <div className="border-l-2 border-amber-500/60 rounded pl-3 py-2">
      <div className="flex items-center gap-1.5 text-sm">
        <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-amber-500" />
        <span className="text-muted-foreground">{summary}</span>
        {suggestedAction && (
          <span className={`text-xs px-1.5 py-0.5 rounded border ${badgeColor}`}>
            {suggestedAction}
          </span>
        )}
        {maxAttempts > 0 && (
          <span className="text-xs text-muted-foreground/50 ml-auto shrink-0">
            {attempt}/{maxAttempts}
          </span>
        )}
      </div>

      {hasDetails && (
        <Collapsible open={isOpen} onOpenChange={setIsOpen} className="group">
          <CollapsibleTrigger className="flex items-center gap-1 mt-1.5 text-xs text-muted-foreground/60 hover:text-muted-foreground transition-colors">
            <span className="opacity-0 group-hover:opacity-100 transition-opacity inline-flex">
              {isOpen ? (
                <ChevronDown className="h-3 w-3" />
              ) : (
                <ChevronRight className="h-3 w-3" />
              )}
            </span>
            <span>Details</span>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="mt-2 space-y-2 text-xs text-muted-foreground">
              {rootCause && (
                <div>
                  <span className="text-muted-foreground/60">Root cause:</span>{' '}
                  <span>{rootCause}</span>
                </div>
              )}
              {actionPlan && (
                <div>
                  <span className="text-muted-foreground/60">Action plan:</span>{' '}
                  <span>{actionPlan}</span>
                </div>
              )}
              {failureAnalysis && (
                <div>
                  <span className="text-muted-foreground/60">Failure analysis:</span>{' '}
                  <span>{failureAnalysis}</span>
                </div>
              )}
              {hypotheses.length > 0 && (
                <div>
                  <span className="text-muted-foreground/60">Hypotheses:</span>
                  <ul className="list-disc list-inside mt-0.5 space-y-0.5">
                    {hypotheses.map((h, i) => (
                      <li key={`${i}-${h}`}>{h}</li>
                    ))}
                  </ul>
                </div>
              )}
              {reasoning && (
                <div>
                  <span className="text-muted-foreground/60">Reasoning:</span>{' '}
                  <span>{reasoning}</span>
                </div>
              )}
            </div>
          </CollapsibleContent>
        </Collapsible>
      )}
    </div>
  )
}
