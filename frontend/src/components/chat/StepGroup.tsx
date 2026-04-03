import { useState } from 'react'
import { Eye, ChevronDown, ChevronRight, Check, X, Loader2 } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import type { StepItem } from '@/stores/chatStore'

interface StepGroupProps {
  steps: StepItem[]
}

export function StepGroup({ steps }: StepGroupProps) {
  const [isOpen, setIsOpen] = useState(false)
  const stepCount = steps.length

  if (stepCount === 0) return null

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen}>
      <CollapsibleTrigger className="flex items-center gap-1.5 text-muted-foreground hover:text-foreground transition-colors">
        {isOpen ? (
          <ChevronDown className="h-3.5 w-3.5" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5" />
        )}
        <Eye className="h-3.5 w-3.5" />
        <span className="text-sm">View {stepCount} step{stepCount !== 1 ? 's' : ''}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-2 space-y-1 pl-3 min-w-0">
          {steps.map((step, idx) => {
            const StatusIcon = step.status === 'success' ? Check : step.status === 'error' ? X : Loader2
            const statusClass = step.status === 'success'
              ? 'text-emerald-500'
              : step.status === 'error'
                ? 'text-red-500'
                : 'text-muted-foreground animate-spin'

            // Truncate preview to 120 chars
            const previewText = step.resultPreview
              ? step.resultPreview.length > 120
                ? step.resultPreview.slice(0, 120) + '...'
                : step.resultPreview
              : null

            // Format result length for large results
            const formatResultLen = (len: number): string => {
              if (len >= 1000) {
                return (len / 1000).toFixed(1).replace(/\.0$/, '') + 'K chars'
              }
              return len + ' chars'
            }

            return (
              <div key={idx} className="flex items-start gap-2">
                <StatusIcon className={`h-3.5 w-3.5 shrink-0 mt-0.5 ${statusClass}`} />
                <div className="flex flex-col min-w-0 flex-1">
                  <span className="text-sm text-muted-foreground">{step.label}</span>
                  {previewText && (
                    <div className="flex items-center gap-2">
                      <span className="text-xs text-muted-foreground/70 truncate max-w-[200px]">{previewText}</span>
                      {step.resultLen && step.resultLen > 500 && (
                        <span className="text-xs text-muted-foreground/50 bg-muted/50 px-1.5 py-0.5 rounded">
                          {formatResultLen(step.resultLen)}
                        </span>
                      )}
                    </div>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}
