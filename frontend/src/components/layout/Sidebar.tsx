import { cn } from '@/lib/utils'
import { useProjectStore } from '@/stores/projectStore'
import { SidebarHeader } from './SidebarHeader'
import { ProjectSelector } from './ProjectSelector'
import { SessionSelector } from './SessionSelector'
import { WorkspacePanel } from './WorkspacePanel'
import { Button } from '@/components/ui/button'
import { PanelLeftOpen } from 'lucide-react'

interface SidebarProps {
  width: number
  collapsed: boolean
  onToggleCollapse: () => void
}

export function Sidebar({ width, collapsed, onToggleCollapse }: SidebarProps) {
  const activeProjectId = useProjectStore((s) => s.activeProjectId)

  if (collapsed) {
    return (
      <div className="flex shrink-0 flex-col items-start pl-2 justify-center border-r border-border bg-background" style={{ width }}>
        <Button variant="ghost" size="icon-xs" onClick={onToggleCollapse} aria-label="Expand sidebar">
          <PanelLeftOpen className="size-4" />
        </Button>
      </div>
    )
  }

  return (
    <div
      className={cn('flex shrink-0 flex-col border-r border-border bg-background')}
      style={{ width }}
    >
      <SidebarHeader onToggleCollapse={onToggleCollapse} collapsed={collapsed} />
      <ProjectSelector />
      {activeProjectId && <SessionSelector />}
      {activeProjectId && (
        <div className="flex-1 overflow-hidden">
          <WorkspacePanel />
        </div>
      )}
    </div>
  )
}
