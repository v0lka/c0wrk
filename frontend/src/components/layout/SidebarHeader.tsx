import { useCallback } from 'react'
import { Button } from '@/components/ui/button'
import { useSettingsStore } from '@/stores/settingsStore'
import { useProjectStore } from '@/stores/projectStore'
import { useProjectSwitchState } from '@/hooks/useProjectSwitchState'
import { cn } from '@/lib/utils'
import { PanelLeftClose, PanelLeftOpen, Settings, MessageCircle, Code2 } from 'lucide-react'
import { ActiveSessionsIndicator } from './ActiveSessionsIndicator'

interface SidebarHeaderProps {
  onToggleCollapse: () => void
  collapsed?: boolean
}

export function SidebarHeader({ onToggleCollapse, collapsed }: SidebarHeaderProps) {
  const openSettings = useSettingsStore((s) => s.openSettings)
  const projects = useProjectStore((s) => s.projects)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const lastRealProjectId = useProjectStore((s) => s.lastRealProjectId)
  const setCreateProjectDialogOpen = useProjectStore((s) => s.setCreateProjectDialogOpen)
  const switchProjectWithState = useProjectSwitchState()

  const noProject = projects?.find((p) => p.is_no_project)
  const isChatMode = noProject ? activeProjectId === noProject.id : false
  const hasRealProject = projects?.some((p) => !p.is_no_project)

  const handleToggleMode = useCallback(async (mode: 'chat' | 'code') => {
    try {
      if (mode === 'chat' && noProject && activeProjectId !== noProject.id) {
        await switchProjectWithState(noProject.id)
      } else if (mode === 'code') {
        if (!hasRealProject) {
          setCreateProjectDialogOpen(true)
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
  }, [noProject, activeProjectId, lastRealProjectId, projects, hasRealProject, switchProjectWithState, setCreateProjectDialogOpen])

  return (
    // `@container` turns this row into a size container so the
    // ActiveSessionsIndicator can limit its layout participation to widths
    // where the row actually fits it (see LAYOUT_FIT_CLASSES there) — the
    // header must stay intact at the 180px sidebar minimum.
    <div className="@container flex h-10 shrink-0 items-center gap-1 border-b border-border px-2">
      <Button variant="ghost" size="icon-xs" onClick={onToggleCollapse} aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}>
        {collapsed ? <PanelLeftOpen className="size-4" /> : <PanelLeftClose className="size-4" />}
      </Button>
      <div className="flex-1" />
      <ActiveSessionsIndicator />
      {projects && (
        <div className="flex items-center rounded bg-muted/60 p-0.5">
          <button
            type="button"
            onClick={() => handleToggleMode('chat')}
            title="Assistant mode"
            className={cn(
              'flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium transition-colors',
              isChatMode ? 'bg-background text-muted-foreground shadow-sm' : 'text-foreground/60 hover:text-foreground'
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
              !isChatMode ? 'bg-background text-muted-foreground shadow-sm' : 'text-foreground/60 hover:text-foreground'
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
    </div>
  )
}
