import { useState } from 'react'
import { CheckCircle2, Circle, Loader2, XCircle, Clock, ChevronDown, ChevronRight, ListTodo } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import type { PlanStepDisplay } from '@/stores/chatStore'

interface PlanCardProps {
  id: string
  steps: PlanStepDisplay[]
}

// Format duration human-readably
function formatDuration(ms: number): string {
  if (ms < 1000) {
    return `${ms}ms`
  } else if (ms < 60000) {
    return `${(ms / 1000).toFixed(1)}s`
  } else {
    const minutes = Math.floor(ms / 60000)
    const seconds = Math.round((ms % 60000) / 1000)
    return `${minutes}m ${seconds}s`
  }
}

function StatusIcon({ status }: { status: PlanStepDisplay['status'] }) {
  switch (status) {
    case 'completed':
      return <CheckCircle2 className="h-3.5 w-3.5 text-green-500" />
    case 'running':
      return <Loader2 className="h-3.5 w-3.5 text-blue-500 animate-spin" />
    case 'pending':
      return <Circle className="h-3.5 w-3.5 text-muted-foreground" />
    case 'failed':
      return <XCircle className="h-3.5 w-3.5 text-red-500" />
  }
}

export function PlanCard({ steps }: PlanCardProps) {
  const hasRunning = steps.some(s => s.status === 'running')
  const allDone = steps.every(s => s.status === 'completed')
  const [isOpen, setIsOpen] = useState(hasRunning || !allDone)

  const completedCount = steps.filter(s => s.status === 'completed').length
  const totalCount = steps.length

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen}>
      <CollapsibleTrigger className="flex items-center gap-1.5 text-muted-foreground hover:text-foreground transition-colors group">
        {isOpen ? (
          <ChevronDown className="h-3.5 w-3.5" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5" />
        )}
        <ListTodo className="h-3.5 w-3.5" />
        <span className="text-sm">Plan</span>
        <span className="text-xs text-muted-foreground">
          ({completedCount}/{totalCount} completed)
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-2 space-y-1.5 pl-3 border-l-2 border-muted min-w-0">
          {steps.map((step, idx) => (
            <div key={step.id} className="flex items-center gap-2">
              <StatusIcon status={step.status} />
              <span className="text-xs text-muted-foreground">#{idx + 1}</span>
              <span className="text-sm text-muted-foreground flex-1 truncate">
                {step.description}
              </span>
              {step.duration !== undefined && (step.status === 'completed' || step.status === 'failed') && (
                <span className="flex items-center gap-1 text-xs text-muted-foreground">
                  <Clock className="h-3 w-3" />
                  {formatDuration(step.duration)}
                </span>
              )}
            </div>
          ))}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}
