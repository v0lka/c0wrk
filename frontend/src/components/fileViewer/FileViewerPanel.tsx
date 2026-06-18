import { Folder, PanelRightClose, PanelRightOpen } from 'lucide-react'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { Button } from '@/components/ui/button'
import { FileViewerTabBar } from './FileViewerTabBar'
import { FileViewerContent } from './FileViewerContent'

export function FileViewerPanel() {
  const openTabs = useFileViewerStore((s) => s.openTabs)
  const collapsed = useFileViewerStore((s) => s.collapsed)
  const setCollapsed = useFileViewerStore((s) => s.setCollapsed)

  // Collapsed: narrow strip with expand button, vertically centered (mirrors Sidebar pattern)
  if (collapsed) {
    return (
      <div className="flex flex-col items-end pr-2 justify-center h-full">
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={() => setCollapsed(false)}
          title="Expand file viewer"
        >
          <PanelRightOpen className="size-4" />
        </Button>
      </div>
    )
  }

  // No open tabs: top bar with collapse button (same position as tab bar) + placeholder
  if (openTabs.length === 0) {
    return (
      <div className="flex flex-col h-full">
        {/* Top bar matching tab bar height — collapse button in the same pixel position as when tabs are open */}
        <div className="flex items-center justify-end border-b border-border bg-secondary/50 flex-shrink-0 h-10 pr-2">
          <Button
            variant="ghost"
            size="icon-xs"
            className="flex-shrink-0"
            onClick={() => setCollapsed(true)}
            title="Collapse file viewer"
          >
            <PanelRightClose className="size-4" />
          </Button>
        </div>
        {/* Placeholder body */}
        <div className="flex items-center justify-center flex-1 w-full">
          <Folder className="size-12 text-muted-foreground/30" />
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col bg-background border-l border-border overflow-hidden h-full">
      <FileViewerTabBar onToggleCollapse={() => setCollapsed(true)} collapsed={collapsed} />
      <FileViewerContent />
    </div>
  )
}
