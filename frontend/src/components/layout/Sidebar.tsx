import { useRef } from 'react'
import { cn } from '@/lib/utils'
import { useProjectStore } from '@/stores/projectStore'
import { useUIStore } from '@/stores/uiStore'
import { useVerticalSplit } from '@/hooks/useVerticalSplit'
import { SidebarHeader } from './SidebarHeader'
import { ProjectSelector } from './ProjectSelector'
import { SessionSelector } from './SessionSelector'
import { SessionList } from './SessionList'
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
  const projects = useProjectStore((s) => s.projects)

  const noProject = projects?.find((p) => p.is_no_project)
  const isChatMode = noProject ? activeProjectId === noProject.id : false

  if (collapsed) {
    return (
      <div
        className="flex shrink-0 flex-col items-start pl-2 justify-center border-r border-border bg-background select-none"
        style={{ width }}
        data-sidebar
      >
        <Button variant="ghost" size="icon-xs" onClick={onToggleCollapse} aria-label="Expand sidebar">
          <PanelLeftOpen className="size-4" />
        </Button>
      </div>
    )
  }

  return (
    <div
      className={cn('flex shrink-0 flex-col border-r border-border bg-background select-none')}
      style={{ width }}
      data-sidebar
    >
      <SidebarHeader onToggleCollapse={onToggleCollapse} collapsed={collapsed} />
      {/* CODE mode: project selector + dropdown session selector + workspace panel. */}
      {!isChatMode && <ProjectSelector />}
      {!isChatMode && activeProjectId && <SessionSelector />}
      {activeProjectId && (
        <div className="flex-1 overflow-hidden">
          {isChatMode ? (
            <ChatSidebarSplit />
          ) : (
            <WorkspacePanel />
          )}
        </div>
      )}
    </div>
  )
}

/**
 * CHAT (No Project) sidebar body: a vertically-resizable split between the
 * flat session list (top) and the workspace explorer (bottom). The split ratio
 * is persisted in uiStore (`chatSessionListRatio`, default 0.5 = even split)
 * so the user's chosen proportion survives reloads and window resizes.
 */
function ChatSidebarSplit() {
  const ratio = useUIStore((s) => s.chatSessionListRatio)
  const setRatio = useUIStore((s) => s.setChatSessionListRatio)

  const containerRef = useRef<HTMLDivElement>(null)
  const { handleMouseDown, handleKeyDown } = useVerticalSplit({
    containerRef,
    ratio,
    onChange: setRatio,
  })

  // Clamp the rendered percentages so neither pane collapses below a usable
  // sliver even if the persisted ratio is near an extreme.
  const topPct = Math.round(ratio * 100)

  return (
    <div ref={containerRef} className="flex h-full flex-col overflow-hidden">
      <div className="min-h-0 overflow-hidden" style={{ flexBasis: `${topPct}%`, flexGrow: 0, flexShrink: 0 }}>
        <SessionList />
      </div>

      {/* Resize handle (horizontal divider, drag vertically). */}
      <div
        role="separator"
        aria-orientation="horizontal"
        aria-label="Resize session list"
        tabIndex={0}
        className="flex-shrink-0 h-1 cursor-row-resize bg-transparent transition-colors hover:bg-ring active:bg-primary"
        onMouseDown={handleMouseDown}
        onKeyDown={handleKeyDown}
      />

      <div className="min-h-0 flex-1 overflow-hidden">
        <WorkspacePanel />
      </div>
    </div>
  )
}
