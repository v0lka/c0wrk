import { useState, useMemo } from 'react'
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
import { useScrollStore } from '@/stores/scrollStore'
import { computeDAGLayout } from '@/lib/dagLayout'

// Status icon for plan items
function PlanStatusIcon({ status }: { status: PlanItem['status'] }) {
  switch (status) {
    case 'pending':
      return <CircleHelp className="h-3.5 w-3.5 text-zinc-500 flex-shrink-0" />
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
      return <CircleHelp className="h-3.5 w-3.5 text-zinc-500 flex-shrink-0" />
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
      className="flex items-center gap-2 w-full px-3 py-2 text-left text-zinc-300 hover:bg-zinc-800 transition-colors rounded-sm"
    >
      {isOpen ? (
        <ChevronDown className="h-3.5 w-3.5 text-zinc-500" />
      ) : (
        <ChevronRight className="h-3.5 w-3.5 text-zinc-500" />
      )}
      {icon}
      <span className="text-sm font-medium">{title}</span>
      <span className="text-xs text-zinc-500">{completed}/{total} {verb}</span>
    </button>
  )
}

// DAG graph constants
const LANE_WIDTH = 6
const ROW_HEIGHT = 24
const PADDING = 4
const STROKE_COLOR = '#52525b'
const STROKE_WIDTH = 1

// SVG DAG column for plan items
function DAGGraph({ items }: { items: PlanItem[] }) {
  const layout = useMemo(() => computeDAGLayout(items), [items])

  if (layout.maxLane === -1) return null

  const width = (layout.maxLane + 1) * LANE_WIDTH + PADDING * 2
  const height = items.length * ROW_HEIGHT

  const laneX = (lane: number) => lane * LANE_WIDTH + PADDING + LANE_WIDTH / 2
  const rowY = (row: number) => row * ROW_HEIGHT + ROW_HEIGHT / 2

  return (
    <svg
      width={width}
      height={height}
      className="flex-shrink-0"
    >
      {layout.connectors.map((c, i) => {
        const x1 = laneX(c.fromLane)
        const y1 = rowY(c.fromRow)
        const x2 = laneX(c.toLane)
        const y2 = rowY(c.toRow)

        if (c.type === 'vertical') {
          return (
            <line
              key={i}
              x1={x1} y1={y1}
              x2={x2} y2={y2}
              stroke={STROKE_COLOR}
              strokeWidth={STROKE_WIDTH}
              strokeLinecap="round"
              fill="none"
            />
          )
        }

        if (c.type === 'fork' || c.type === 'merge') {
          const midY = (y1 + y2) / 2
          return (
            <path
              key={i}
              d={`M ${x1} ${y1} L ${x1} ${midY} L ${x2} ${midY} L ${x2} ${y2}`}
              stroke={STROKE_COLOR}
              strokeWidth={STROKE_WIDTH}
              strokeLinecap="round"
              strokeLinejoin="round"
              fill="none"
            />
          )
        }

        return null
      })}
      {layout.nodes.map((node, i) => (
        <circle
          key={`node-${i}`}
          cx={laneX(node.lane)}
          cy={rowY(i)}
          r={2}
          fill={STROKE_COLOR}
          stroke="none"
        />
      ))}
    </svg>
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
          {groupIdx > 0 && <div className="border-t border-zinc-800 my-2" />}
          <div className="flex items-start">
            <DAGGraph items={group.items} />
            <div className="flex-1 min-w-0">
              {group.items.map((item) => (
                <button
                  key={`${group.id}-${item.id}`}
                  onClick={() => onStepClick?.(item.id)}
                  className="flex items-center gap-2 h-6 px-1 -mx-1 w-full text-left rounded hover:bg-zinc-800/50 transition-colors cursor-pointer"
                >
                  <PlanStatusIcon status={item.status} />
                  <span className="text-xs text-zinc-400 truncate">{item.title}</span>
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
          {groupIdx > 0 && <div className="border-t border-zinc-800 my-2" />}
          <div className="space-y-1">
            {group.items.map((item) => (
              <div key={`${group.id}-${item.name}`} className="flex items-center gap-2 py-0.5">
                <EvalStatusIcon status={item.status} />
                <span className="text-xs text-zinc-400 truncate">{item.description}</span>
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

export function ExecutionPanels() {
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

  // Don't render if both are empty
  if (!hasPlan && !hasEval) {
    return null
  }

  return (
    <div className="border-t border-zinc-700 bg-zinc-900">
      {/* Execution plan panel */}
      {hasPlan && (
        <div>
          <PanelHeader
            isOpen={planOpen}
            onToggle={() => setPlanOpen(!planOpen)}
            icon={<ListChecks className="h-3.5 w-3.5 text-zinc-400" />}
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
        <div className={hasPlan ? 'border-t border-zinc-800' : ''}>
          <PanelHeader
            isOpen={evalOpen}
            onToggle={() => setEvalOpen(!evalOpen)}
            icon={<ClipboardCheck className="h-3.5 w-3.5 text-zinc-400" />}
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
