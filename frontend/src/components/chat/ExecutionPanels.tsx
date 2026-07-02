import { useCallback, useState } from 'react'
import { ChevronDown, ListChecks, ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'
import { usePlanStore, usePlanCompleted, usePlanFailed, usePlanTotal } from '@/stores/planStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useUIStore } from '@/stores/uiStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { DAGGraph } from './DAGGraph'
import { PlanView } from './PlanView'

export function ExecutionPanels() {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const planGroups = usePlanStore(s => s.planGroups)
  const planCompleted = usePlanCompleted()
  const planFailed = usePlanFailed()
  const planTotal = usePlanTotal()
  const sidebarCollapsed = useUIStore(s => s.sidebarCollapsed)
  const viewerCollapsed = useFileViewerStore(s => s.collapsed)

  const [planOpen, setPlanOpen] = useState(false)
  const [expandedItems, setExpandedItems] = useState<Set<string>>(() => new Set())

  const handleToggleItem = useCallback((itemId: string) => {
    setExpandedItems(prev => {
      const next = new Set(prev)
      if (next.has(itemId)) next.delete(itemId)
      else next.add(itemId)
      return next
    })
  }, [])

  const hasPlan = planGroups.length > 0
  if (!hasPlan || !activeSessionId) return null

  return (
    <div className={cn(
      'border-t border-x border-border bg-card',
      sidebarCollapsed && 'ml-1',
      viewerCollapsed && 'mr-1',
    )}>
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
                <DAGGraph items={group.items} expandedItems={expandedItems} />
                <div className="flex-1 min-w-0">
                  <PlanView expandedItems={expandedItems} onToggleItem={handleToggleItem} />
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
