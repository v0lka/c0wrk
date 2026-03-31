import { useState } from 'react'
import { CheckCircle2, XCircle, HelpCircle, ClipboardCheck, ChevronDown, ChevronRight } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import type { EvalCriterionDisplay } from '@/stores/chatStore'

interface EvalCardProps {
  id: string
  passed: number
  total: number
  criteria: EvalCriterionDisplay[]
}

function StatusIcon({ status }: { status: EvalCriterionDisplay['status'] }) {
  switch (status) {
    case 'pass':
      return <CheckCircle2 className="h-3.5 w-3.5 text-green-500" />
    case 'fail':
      return <XCircle className="h-3.5 w-3.5 text-red-500" />
    case 'unclear':
      return <HelpCircle className="h-3.5 w-3.5 text-amber-500" />
  }
}

function StatusBadge({ status }: { status: EvalCriterionDisplay['status'] }) {
  switch (status) {
    case 'pass':
      return <Badge variant="secondary" className="text-xs bg-green-500/10 text-green-500 hover:bg-green-500/20">Pass</Badge>
    case 'fail':
      return <Badge variant="secondary" className="text-xs bg-red-500/10 text-red-500 hover:bg-red-500/20">Fail</Badge>
    case 'unclear':
      return <Badge variant="secondary" className="text-xs bg-amber-500/10 text-amber-500 hover:bg-amber-500/20">Unclear</Badge>
  }
}

export function EvalCard({ passed, total, criteria }: EvalCardProps) {
  const [isOpen, setIsOpen] = useState(true)
  const percentage = total > 0 ? Math.round((passed / total) * 100) : 0

  const getHeaderColor = () => {
    if (percentage === 100) return 'text-green-500'
    if (percentage === 0) return 'text-red-500'
    return 'text-amber-500'
  }

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen}>
      <CollapsibleTrigger className="flex items-center gap-1.5 text-muted-foreground hover:text-foreground transition-colors group">
        {isOpen ? (
          <ChevronDown className="h-3.5 w-3.5" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5" />
        )}
        <ClipboardCheck className="h-3.5 w-3.5" />
        <span className="text-sm">Evaluation</span>
        <span className={`text-xs ${getHeaderColor()}`}>
          ({passed}/{total} passed)
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-2 space-y-1.5 pl-3 border-l-2 border-muted min-w-0">
          {criteria.map((criterion) => (
            <div key={criterion.name} className="flex items-center gap-2">
              <StatusIcon status={criterion.status} />
              <span className="text-sm text-muted-foreground flex-1 truncate">
                {criterion.description}
              </span>
              <StatusBadge status={criterion.status} />
            </div>
          ))}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}
