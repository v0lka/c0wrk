import { useState } from 'react'
import { useUIStore } from '@/stores/uiStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useResize } from '@/hooks/useResize'
import { ResizeHandle } from '@/components/ResizeHandle'
import { Sidebar } from './Sidebar'
import { ChatArea } from '@/components/chat/ChatArea'
import { StatusBar } from '@/components/layout/StatusBar'
import { FileViewerPanel } from '@/components/fileViewer/FileViewerPanel'

// --- Constants ---

const SIDEBAR_MIN = 180
const SIDEBAR_MAX = 500
const VIEWER_MIN = 250
const VIEWER_MAX = 900
const COLLAPSED_WIDTH = 40

function clamp(value: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, value))
}

function getDefaultSidebarWidth(): number {
  return clamp(Math.round(window.innerWidth / 6), SIDEBAR_MIN, SIDEBAR_MAX)
}

export function AppLayout() {
  const sidebarCollapsed = useUIStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useUIStore((s) => s.toggleSidebarCollapsed)

  const viewerWidth = useFileViewerStore((s) => s.width)
  const viewerCollapsed = useFileViewerStore((s) => s.collapsed)
  const setViewerWidth = useFileViewerStore((s) => s.setWidth)

  const [sidebarWidth, setSidebarWidth] = useState(getDefaultSidebarWidth)

  const sidebarResize = useResize({
    initialWidth: sidebarCollapsed ? COLLAPSED_WIDTH : sidebarWidth,
    min: SIDEBAR_MIN,
    max: SIDEBAR_MAX,
    onChange: setSidebarWidth,
  })

  const viewerResize = useResize({
    initialWidth: viewerWidth,
    min: VIEWER_MIN,
    max: VIEWER_MAX,
    direction: -1,
    onChange: setViewerWidth,
  })

  return (
    <div className="flex h-screen w-screen overflow-hidden bg-background text-foreground">
      {/* Sidebar */}
      <Sidebar
        width={sidebarCollapsed ? COLLAPSED_WIDTH : sidebarWidth}
        collapsed={sidebarCollapsed}
        onToggleCollapse={toggleSidebar}
      />

      {/* Resize handle between sidebar and main */}
      {!sidebarCollapsed && (
        <ResizeHandle
          onMouseDown={sidebarResize.handleMouseDown}
          onKeyDown={sidebarResize.handleKeyDown}
        />
      )}

      {/* Main content area */}
      <div className="flex min-w-0 flex-1 flex-col">
        <ChatArea />
        <StatusBar />
      </div>

      {/* Resize handle between main and file viewer */}
      {!viewerCollapsed && (
        <ResizeHandle
          onMouseDown={viewerResize.handleMouseDown}
          onKeyDown={viewerResize.handleKeyDown}
        />
      )}

      {/* File viewer */}
      <div
        className="flex shrink-0 flex-col border-l border-border bg-background"
        style={{ width: viewerCollapsed ? COLLAPSED_WIDTH : viewerWidth }}
      >
        <FileViewerPanel />
      </div>
    </div>
  )
}
