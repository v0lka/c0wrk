import { useFileViewerStore } from '@/stores/fileViewerStore'
import { FileViewerTabBar } from './FileViewerTabBar'
import { FileViewerContent } from './FileViewerContent'

interface FileViewerPanelProps {
  width: number
}

export function FileViewerPanel({ width }: FileViewerPanelProps) {
  const openFiles = useFileViewerStore((s) => s.openFiles)
  const isCollapsed = useFileViewerStore((s) => s.isCollapsed)

  // Hide completely when no files are open or panel is collapsed
  // (collapsed state is handled by a narrow strip in AppLayout)
  if (openFiles.length === 0 || isCollapsed) return null

  return (
    <div
      className="flex flex-col bg-card border-l border-border overflow-hidden"
      style={{ width }}
    >
      <FileViewerTabBar />
      <FileViewerContent />
    </div>
  )
}
