import { useState } from 'react'
import {
  ChevronDown,
  ListChecks,
  ClipboardCheck,
  CircleHelp,
  CircleDot,
  CircleCheck,
  CircleX,
  ChevronRight,
} from 'lucide-react'
import {
  usePanelStore,
  usePlanCompleted,
  usePlanTotal,
  useEvalCompleted,
  useEvalTotal,
  type PlanItem,
  type EvalItem,
  type PlanGroup,
  type EvalGroup,
} from '@/stores/panelStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useScrollStore } from '@/stores/scrollStore'
import { DAGGraph } from './DAGGraph'

// Status icon for plan items
function PlanStatusIcon({ status }: { status: PlanItem['status'] }) {
  switch (status) {
    case 'pending':
      return <CircleHelp className="h-3.5 w-3.5 text-muted-foreground flex-shrink-0" />
    case 'running':
      return <CircleDot className="h-3.5 w-3.5 text-blue-400 animate-pulse flex-shrink-0" />
    case 'completed':
      return <CircleCheck className="h-3.5 w-3.5 text-emerald-400 flex-shrink-0" />
    case 'failed':
      return <CircleX className="h-3.5 w-3.5 text-red-400 flex-shrink-0" />
  }
}

// Status icon for eval items
function EvalStatusIcon({ status }: { status: EvalItem['status'] }) {
  switch (status) {
    case 'pending':
      return <CircleHelp className="h-3.5 w-3.5 text-muted-foreground flex-shrink-0" />
    case 'pass':
      return <CircleCheck className="h-3.5 w-3.5 text-emerald-400 flex-shrink-0" />
    case 'fail':
      return <CircleX className="h-3.5 w-3.5 text-red-400 flex-shrink-0" />
    case 'unclear':
      return <CircleHelp className="h-3.5 w-3.5 text-amber-400 flex-shrink-0" />
  }
}

// Collapsible panel header
interface PanelHeaderProps {
  isOpen: boolean
  onToggle: () => void
  icon: React.ReactNode
  title: string
  completed: number
  verb: string
  total: number
}

function PanelHeader({ isOpen, onToggle, icon, title, completed, total, verb }: PanelHeaderProps) {
  return (
    <button
      onClick={onToggle}
      className="flex items-center gap-2 w-full px-3 py-2 text-left text-foreground hover:bg-muted transition-colors rounded-sm"
    >
      {isOpen ? (
        <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
      ) : (
        <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
      )}
      {icon}
      <span className="text-sm font-medium">{title}</span>
      <span className="text-xs text-muted-foreground">{completed}/{total} {verb}</span>
    </button>
  )
}

// Plan panel content
interface PlanContentProps {
  groups: PlanGroup[]
  onStepClick?: (stepId: string) => void
}

function PlanContent({ groups, onStepClick }: PlanContentProps) {
  return (
    <div className="max-h-48 overflow-y-auto px-3 pb-2">
      {groups.map((group, groupIdx) => (
        <div key={group.id}>
          {groupIdx > 0 && <div className="border-t border-border my-2" />}
          <div className="flex items-start">
            <DAGGraph items={group.items} />
            <div className="flex-1 min-w-0">
              {group.items.map((item) => (
                <button
                  key={`${group.id}-${item.id}`}
                  onClick={() => onStepClick?.(item.id)}
                  className="flex items-center gap-2 h-[24px] px-1 -mx-1 w-full text-left rounded hover:bg-muted/50 transition-colors cursor-pointer"
                >
                  <PlanStatusIcon status={item.status} />
                  <span className="text-xs text-muted-foreground truncate">{item.title}</span>
                </button>
              ))}
            </div>
          </div>
        </div>
      ))}
    </div>
  )
}

// Eval panel content
interface EvalContentProps {
  groups: EvalGroup[]
}

function EvalContent({ groups }: EvalContentProps) {
  return (
    <div className="max-h-48 overflow-y-auto px-3 pb-2">
      {groups.map((group, groupIdx) => (
        <div key={group.id}>
          {groupIdx > 0 && <div className="border-t border-border my-2" />}
          <div className="space-y-1">
            {group.items.map((item) => (
              <div key={`${group.id}-${item.name}`} className="py-0.5">
                <div className="flex items-center gap-2">
                  <EvalStatusIcon status={item.status} />
                  <span className="text-xs text-muted-foreground truncate">{item.description}</span>
                </div>
                {item.diagnostic && (item.status === 'fail' || item.status === 'unclear') && (
                  <p className="text-xs text-muted-foreground/60 ml-[22px] mt-0.5 whitespace-pre-wrap">{item.diagnostic}</p>
                )}
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

export function ExecutionPanels() {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const planGroups = usePanelStore((s) => s.planGroups)
  const evalGroups = usePanelStore((s) => s.evalGroups)
  const planCompleted = usePlanCompleted()
  const planTotal = usePlanTotal()
  const evalCompleted = useEvalCompleted()
  const evalTotal = useEvalTotal()

  const scrollToStep = useScrollStore(s => s.scrollToStep)

  const [planOpen, setPlanOpen] = useState(false)
  const [evalOpen, setEvalOpen] = useState(false)

  const hasPlan = planGroups.length > 0
  const hasEval = evalGroups.length > 0

  // Don't render if both are empty or no active session
  if ((!hasPlan && !hasEval) || !activeSessionId) {
    return null
  }

  return (
    <div className="border-t border-border bg-card">
      {/* Execution plan panel */}
      {hasPlan && (
        <div>
          <PanelHeader
            isOpen={planOpen}
            onToggle={() => setPlanOpen(!planOpen)}
            icon={<ListChecks className="h-3.5 w-3.5 text-muted-foreground" />}
            title="Execution plan"
            completed={planCompleted}
            total={planTotal}
            verb="completed"
          />
          {planOpen && <PlanContent groups={planGroups} onStepClick={scrollToStep ?? undefined} />}
        </div>
      )}

      {/* Acceptance criteria panel */}
      {hasEval && (
        <div className={hasPlan ? 'border-t border-border' : ''}>
          <PanelHeader
            isOpen={evalOpen}
            onToggle={() => setEvalOpen(!evalOpen)}
            icon={<ClipboardCheck className="h-3.5 w-3.5 text-muted-foreground" />}
            title="Acceptance criteria"
            completed={evalCompleted}
            total={evalTotal}
            verb="checked"
          />
          {evalOpen && <EvalContent groups={evalGroups} />}
        </div>
      )}
    </div>
  )
}
