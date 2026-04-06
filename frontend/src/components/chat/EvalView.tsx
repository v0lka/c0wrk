import { CheckCircle2, XCircle, HelpCircle, ClipboardCheck } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { usePanelStore } from '@/stores/panelStore'

// View-specific criterion type for display
interface EvalCriterionView {
  id: string
  description: string
  status: 'pending' | 'pass' | 'fail' | 'unclear'
}

function StatusIcon({ status }: { status: EvalCriterionView['status'] }) {
  switch (status) {
    case 'pass':
      return <CheckCircle2 className="h-4 w-4 text-green-500" />
    case 'fail':
      return <XCircle className="h-4 w-4 text-red-500" />
    case 'unclear':
      return <HelpCircle className="h-4 w-4 text-amber-500" />
    case 'pending':
      return <HelpCircle className="h-4 w-4 text-muted-foreground" />
  }
}

function StatusBadge({ status }: { status: EvalCriterionView['status'] }) {
  switch (status) {
    case 'pass':
      return <Badge variant="secondary" className="text-xs bg-green-500/10 text-green-500 hover:bg-green-500/20">Pass</Badge>
    case 'fail':
      return <Badge variant="secondary" className="text-xs bg-red-500/10 text-red-500 hover:bg-red-500/20">Fail</Badge>
    case 'unclear':
      return <Badge variant="secondary" className="text-xs bg-amber-500/10 text-amber-500 hover:bg-amber-500/20">Unclear</Badge>
    case 'pending':
      return <Badge variant="outline" className="text-xs text-muted-foreground">Pending</Badge>
  }
}

function CriterionItem({ criterion }: { criterion: EvalCriterionView }) {
  return (
    <div className="flex items-start gap-3 p-3 border border-border rounded-lg">
      <div className="mt-0.5">
        <StatusIcon status={criterion.status} />
      </div>
      <div className="flex-1 min-w-0">
        <p className="text-sm">{criterion.description}</p>
      </div>
      <StatusBadge status={criterion.status} />
    </div>
  )
}

function SummaryHeader({ passed, total }: { passed: number; total: number }) {
  const percentage = total > 0 ? Math.round((passed / total) * 100) : 0
  
  return (
    <div className="flex items-center justify-between p-3 bg-muted/50 rounded-lg mb-4">
      <div className="flex items-center gap-2">
        <ClipboardCheck className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-medium">Criteria Passed</span>
      </div>
      <div className="flex items-center gap-2">
        <span className="text-sm font-semibold">
          {passed}/{total}
        </span>
        <Badge 
          variant={percentage === 100 ? "secondary" : "outline"} 
          className={percentage === 100 ? "bg-green-500/10 text-green-500" : ""}
        >
          {percentage}%
        </Badge>
      </div>
    </div>
  )
}

export function EvalView() {
  // Get eval criteria from the latest eval group in panelStore
  const latestEvalGroup = usePanelStore((s) => s.evalGroups.length > 0 ? s.evalGroups[0] : null)
  
  // Map store criteria to view format
  const criteria: EvalCriterionView[] = latestEvalGroup
    ? latestEvalGroup.items.map((item) => ({
        id: item.name,
        description: item.description,
        status: item.status,
      }))
    : []
  const hasEvalResult = criteria.length > 0
  
  if (!hasEvalResult || criteria.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-12 text-center">
        <ClipboardCheck className="h-8 w-8 text-muted-foreground/50 mb-3" />
        <p className="text-sm text-muted-foreground">No evaluation results</p>
        <p className="text-xs text-muted-foreground/70 mt-1">
          Evaluation results will appear here after task completion
        </p>
      </div>
    )
  }

  const passedCount = criteria.filter((c) => c.status === 'pass').length

  return (
    <div>
      <SummaryHeader passed={passedCount} total={criteria.length} />
      <div className="space-y-2">
        {criteria.map((criterion) => (
          <CriterionItem key={criterion.id} criterion={criterion} />
        ))}
      </div>
    </div>
  )
}
