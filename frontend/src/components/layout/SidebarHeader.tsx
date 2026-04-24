import { Button } from '@/components/ui/button'
import { useSettingsStore } from '@/stores/settingsStore'
import { PanelLeftClose, PanelLeftOpen, Settings } from 'lucide-react'

interface SidebarHeaderProps {
  onToggleCollapse: () => void
  collapsed?: boolean
}

export function SidebarHeader({ onToggleCollapse, collapsed }: SidebarHeaderProps) {
  const openSettings = useSettingsStore((s) => s.openSettings)

  return (
    <div className="flex h-10 shrink-0 items-center gap-1 border-b border-border px-2">
      <Button variant="ghost" size="icon-xs" onClick={onToggleCollapse} aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}>
        {collapsed ? <PanelLeftOpen className="size-4" /> : <PanelLeftClose className="size-4" />}
      </Button>
      <div className="flex-1" />
      <Button variant="ghost" size="icon-xs" onClick={() => openSettings()} aria-label="Settings">
        <Settings className="size-4" />
      </Button>
    </div>
  )
}
