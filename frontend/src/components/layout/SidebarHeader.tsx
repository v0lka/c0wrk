import { useCallback, useState } from 'react'
import { Button } from '@/components/ui/button'
import { useSettingsStore } from '@/stores/settingsStore'
import { useProjectStore } from '@/stores/projectStore'
import { useProjectSwitchState } from '@/hooks/useProjectSwitchState'
import { CreateProjectDialog } from '@/components/project/CreateProjectDialog'
import { cn } from '@/lib/utils'
import { PanelLeftClose, PanelLeftOpen, Settings, MessageCircle, Code2 } from 'lucide-react'

interface SidebarHeaderProps {
  onToggleCollapse: () => void
  collapsed?: boolean
}

export function SidebarHeader({ onToggleCollapse, collapsed }: SidebarHeaderProps) {
  const openSettings = useSettingsStore((s) => s.openSettings)
  const projects = useProjectStore((s) => s.projects)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const lastRealProjectId = useProjectStore((s) => s.lastRealProjectId)
  const switchProjectWithState = useProjectSwitchState()

  const noProject = projects?.find((p) => p.is_no_project)
  const isChatMode = noProject ? activeProjectId === noProject.id : false
  const hasRealProject = projects?.some((p) => !p.is_no_project)

  const [createDialogOpen, setCreateDialogOpen] = useState(false)

  const handleToggleMode = useCallback(async (mode: 'chat' | 'code') => {
    try {
      if (mode === 'chat' && noProject && activeProjectId !== noProject.id) {
        await switchProjectWithState(noProject.id)
      } else if (mode === 'code') {
        if (!hasRealProject) {
          setCreateDialogOpen(true)
          return
        }
        // Resolve target: lastRealProjectId if it still exists, otherwise first real project.
        const real = lastRealProjectId
          ? projects?.find((p) => p.id === lastRealProjectId && !p.is_no_project)
          : null
        const target = real ?? projects?.find((p) => !p.is_no_project)
        if (target && activeProjectId !== target.id) {
          await switchProjectWithState(target.id)
        }
      }
    } catch {
      // runtime_error toast already shown by global event listener
    }
  }, [noProject, activeProjectId, lastRealProjectId, projects, hasRealProject, switchProjectWithState])

  const handleCreateDialogClose = useCallback((open: boolean) => {
    setCreateDialogOpen(open)
  }, [])

  return (
    <div className="flex h-10 shrink-0 items-center gap-1 border-b border-border px-2">
      <Button variant="ghost" size="icon-xs" onClick={onToggleCollapse} aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}>
        {collapsed ? <PanelLeftOpen className="size-4" /> : <PanelLeftClose className="size-4" />}
      </Button>
      <div className="flex-1" />
      {projects && (
        <div className="flex items-center rounded bg-muted/60 p-0.5">
          <button
            type="button"
            onClick={() => handleToggleMode('chat')}
            title="Assistant mode"
            className={cn(
              'flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium transition-colors',
              isChatMode ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'
            )}
          >
            <MessageCircle className="size-3" />
            CHAT
          </button>
          <button
            type="button"
            onClick={() => handleToggleMode('code')}
            title="Coding agent mode"
            className={cn(
              'flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium transition-colors',
              !isChatMode ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'
            )}
          >
            <Code2 className="size-3" />
            CODE
          </button>
        </div>
      )}
      <div className="flex-1" />
      <Button variant="ghost" size="icon-xs" onClick={() => openSettings()} aria-label="Settings">
        <Settings className="size-4" />
      </Button>
      <CreateProjectDialog open={createDialogOpen} onOpenChange={handleCreateDialogClose} />
    </div>
  )
}
