import { useState } from 'react'
import { CheckCircle2, Circle, Loader2, ChevronDown, ChevronRight, ListTodo, XCircle, Clock } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { formatDuration } from '@/lib/formatters'
import { usePanelStore } from '@/stores/panelStore'

// View-specific step type for display
interface PlanStepView {
  id: string
  description: string
  status: 'done' | 'running' | 'waiting' | 'failed'
  details?: string
  duration?: number // milliseconds
}

function StatusIcon({ status }: { status: PlanStepView['status'] }) {
  switch (status) {
    case 'done':
      return <CheckCircle2 className="h-4 w-4 text-green-500" />
    case 'running':
      return <Loader2 className="h-4 w-4 text-blue-500 animate-spin" />
    case 'waiting':
      return <Circle className="h-4 w-4 text-muted-foreground" />
    case 'failed':
      return <XCircle className="h-4 w-4 text-red-500" />
  }
}

function StatusBadge({ status }: { status: PlanStepView['status'] }) {
  switch (status) {
    case 'done':
      return <Badge variant="secondary" className="text-xs bg-green-500/10 text-green-500 hover:bg-green-500/20">Done</Badge>
    case 'running':
      return <Badge variant="secondary" className="text-xs bg-blue-500/10 text-blue-500 hover:bg-blue-500/20">Running</Badge>
    case 'waiting':
      return <Badge variant="secondary" className="text-xs text-muted-foreground">Waiting</Badge>
    case 'failed':
      return <Badge variant="secondary" className="text-xs bg-red-500/10 text-red-500 hover:bg-red-500/20">Failed</Badge>
  }
}

function PlanStepItem({ step }: { step: PlanStepView }) {
  const [expanded, setExpanded] = useState(false)

  return (
    <div className="border border-border rounded-lg overflow-hidden">
      <button
        onClick={() => step.details && setExpanded(!expanded)}
        className={cn(
          "w-full flex items-center gap-3 p-3 text-left transition-colors",
          step.details && "hover:bg-accent/50 active:bg-accent/70 cursor-pointer",
          !step.details && "cursor-default"
        )}
      >
        <StatusIcon status={step.status} />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-xs font-medium text-muted-foreground">#{step.id}</span>
            <span className="text-sm truncate">{step.description}</span>
          </div>
        </div>
        {step.duration !== undefined && (step.status === 'done' || step.status === 'failed') && (
          <span className="flex items-center gap-1 text-xs text-muted-foreground">
            <Clock className="h-3 w-3" />
            {formatDuration(step.duration)}
          </span>
        )}
        <StatusBadge status={step.status} />
        {step.details && (
          <div className="text-muted-foreground">
            {expanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
          </div>
        )}
      </button>
      {expanded && step.details && (
        <div className="px-3 pb-3 pl-10">
          <p className="text-xs text-muted-foreground">{step.details}</p>
        </div>
      )}
    </div>
  )
}

export function PlanView() {
  // Get plan steps from the latest plan group in panelStore
  const latestPlanGroup = usePanelStore((s) => s.planGroups.length > 0 ? s.planGroups[0] : null)
  
  // Map store status to view status
  const steps: PlanStepView[] = latestPlanGroup
    ? latestPlanGroup.items.map((item, index) => ({
        id: item.id || String(index + 1),
        description: item.title,
        status: item.status === 'completed' ? 'done' : item.status === 'pending' ? 'waiting' : item.status,
        duration: item.duration,
      }))
    : []
  const hasPlan = steps.length > 0

  if (!hasPlan) {
    return (
      <div className="flex flex-col items-center justify-center py-12 text-center">
        <ListTodo className="h-8 w-8 text-muted-foreground/50 mb-3" />
        <p className="text-sm text-muted-foreground">No plan generated</p>
        <p className="text-xs text-muted-foreground/70 mt-1">
          Plans will appear here when tasks are created
        </p>
      </div>
    )
  }

  return (
    <div className="space-y-2 min-w-0">
      {steps.map((step) => (
        <PlanStepItem key={step.id} step={step} />
      ))}
    </div>
  )
}
