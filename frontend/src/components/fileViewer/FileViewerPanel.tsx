import { PanelRightOpen } from 'lucide-react'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { Button } from '@/components/ui/button'
import { FileViewerTabBar } from './FileViewerTabBar'
import { FileViewerContent } from './FileViewerContent'

export function FileViewerPanel() {
  const openTabs = useFileViewerStore((s) => s.openTabs)
  const collapsed = useFileViewerStore((s) => s.collapsed)
  const setCollapsed = useFileViewerStore((s) => s.setCollapsed)

  if (openTabs.length === 0) return null

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

  return (
    <div className="flex flex-col bg-background border-l border-border overflow-hidden h-full">
      <FileViewerTabBar onToggleCollapse={() => setCollapsed(true)} collapsed={collapsed} />
      <FileViewerContent />
    </div>
  )
}
