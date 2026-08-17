import { useState } from 'react'
import { ChevronDown, ListChecks, ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'
import { usePlanStore, usePlanCompleted, usePlanFailed, usePlanTotal } from '@/stores/planStore'
import type { AgentMetricsCounters } from '@/types/events'
import { useSessionStore } from '@/stores/sessionStore'
import { useUIStore } from '@/stores/uiStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { DAGGraph } from './DAGGraph'
import { PlanView } from './PlanView'

/** Summarizes one nudges/aborts counter block as "2 (1r · 1s · 0f · 0p)". */
function countersSummary(c: AgentMetricsCounters): string {
  const total = c.repeat + c.same_tool + c.fruitless + c.parse
  return `${total} (${c.repeat}r·${c.same_tool}s·${c.fruitless}f·${c.parse}p)`
}

/**
 * Session stats strip: routing/attempt stats plus the per-run agent quality
 * report delivered by the `agent_metrics` event on task finish/abort
 * (parse errors, loop-detector nudges/aborts, steps, output tokens and the
 * active small-LLM profile). Minimal by design — data, not decoration.
 */
function SessionStatsRow({ sessionId }: { sessionId: string }) {
  const stats = usePlanStore(s => s.sessionStats[sessionId])
  const m = stats?.lastAgentMetrics
  if (!stats || !m) return null
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-1.5 border-t border-border text-xs text-muted-foreground">
      {stats.routingDomain && <span>routing: {stats.routingDomain}</span>}
      {typeof stats.attemptCount === 'number' && stats.maxAttempts !== undefined && (
        <span>attempts: {stats.attemptCount}/{stats.maxAttempts}</span>
      )}
      <span className="text-foreground/80">finish: {m.finish}</span>
      <span>steps: {m.steps}</span>
      <span>out tokens: {m.output_tokens}</span>
      <span>parse errors: {m.parse_errors}</span>
      <span>nudges: {countersSummary(m.nudges)}</span>
      <span>aborts: {countersSummary(m.aborts)}</span>
      {m.small_llm.enabled && (
        <span className="text-warning">
          small-llm: {m.small_llm.variants.length > 0 ? m.small_llm.variants.join(', ') : 'on'}
        </span>
      )}
    </div>
  )
}

export function ExecutionPanels() {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const planGroups = usePlanStore(s => s.planGroups)
  const planCompleted = usePlanCompleted()
  const planFailed = usePlanFailed()
  const planTotal = usePlanTotal()
  const sessionStats = usePlanStore(s => (activeSessionId ? s.sessionStats[activeSessionId] : undefined))
  const sidebarCollapsed = useUIStore(s => s.sidebarCollapsed)
  const viewerCollapsed = useFileViewerStore(s => s.collapsed)

  const [planOpen, setPlanOpen] = useState(false)

  const hasPlan = planGroups.length > 0
  // The stats row renders only once the per-run agent_metrics report arrives;
  // routing/retry stats alone must not produce an empty bordered container.
  const hasMetrics = sessionStats?.lastAgentMetrics !== undefined
  if (!activeSessionId || (!hasPlan && !hasMetrics)) return null

  return (
    <div className={cn(
      'border-t border-x border-border bg-card',
      sidebarCollapsed && 'ml-1',
      viewerCollapsed && 'mr-1',
    )}>
      {hasPlan && (
        <div className="group">
          <button
            onClick={() => setPlanOpen(!planOpen)}
            className="flex items-center gap-2 w-full px-3 py-2 text-left text-foreground hover:bg-muted transition-colors rounded-sm"
          >
            <span className="opacity-0 group-hover:opacity-100 transition-opacity inline-flex">
              {planOpen
                ? <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
                : <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />}
            </span>
            <ListChecks className="h-3.5 w-3.5 text-muted-foreground" />
            <span className="text-sm font-medium">Execution plan</span>
            <span className="text-xs text-muted-foreground">{planCompleted}/{planTotal} completed</span>
            {planFailed > 0 && (
              <span className="text-xs text-destructive">{planFailed} failed</span>
            )}
          </button>
          {planOpen && (
            <div className="max-h-48 overflow-y-auto px-3 pb-2 custom-scrollbar">
              {planGroups.map((group) => (
                <div key={group.id} className="flex items-start">
                  <DAGGraph items={group.items} />
                  <div className="flex-1 min-w-0">
                    <PlanView />
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
      <SessionStatsRow sessionId={activeSessionId} />
    </div>
  )
}
