import { useEffect } from 'react'
import { Sidebar } from './Sidebar'
import { StatusBar } from './StatusBar'
import { ChatArea } from '@/components/chat/ChatArea'
import { ChatInput } from '@/components/chat/ChatInput'
import { ExecutionPanels } from '@/components/chat/ExecutionPanels'
import { PendingActionsBar } from '@/components/chat/PendingActionsBar'
import { FileViewerPanel } from '@/components/fileViewer/FileViewerPanel'
import { useProjectStore } from '@/stores/projectStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useUIStore } from '@/stores/uiStore'
import { NoProjectEmptyState } from '@/components/project/NoProjectEmptyState'
import { useResizeHandle } from '@/hooks/useResize'
import { ResizeHandle } from '@/components/ResizeHandle'
import { PanelLeftOpen, PanelRightOpen } from 'lucide-react'
import { Button } from '@/components/ui/button'

const SIDEBAR_DEFAULT = 300
const SIDEBAR_MIN = 180
const SIDEBAR_MAX = 500

const SIDEBAR_COLLAPSED_WIDTH = 32

const FILE_VIEWER_DEFAULT = 500
const FILE_VIEWER_MIN = 250
const FILE_VIEWER_MAX = 900

const FILE_VIEWER_COLLAPSED_WIDTH = 32

export function AppLayout() {
  const sidebar = useResizeHandle(SIDEBAR_DEFAULT, SIDEBAR_MIN, SIDEBAR_MAX, 'left')
  const fileViewer = useResizeHandle(FILE_VIEWER_DEFAULT, FILE_VIEWER_MIN, FILE_VIEWER_MAX, 'right')
  const openFiles = useFileViewerStore((s) => s.openFiles)
  const persistedPanelWidth = useFileViewerStore((s) => s.panelWidth)
  const setPersistedPanelWidth = useFileViewerStore((s) => s.setPanelWidth)
  const fileViewerCollapsed = useFileViewerStore((s) => s.isCollapsed)
  const hasOpenFiles = openFiles.length > 0
  const sidebarCollapsed = useUIStore((s) => s.sidebarCollapsed)
  const setSidebarCollapsed = useUIStore((s) => s.setSidebarCollapsed)

  // Sync persisted width into the resize hook on mount and when it changes externally
  useEffect(() => {
    fileViewer.setWidth(persistedPanelWidth)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Sync resize hook width back to the store for persistence
  useEffect(() => {
    setPersistedPanelWidth(fileViewer.width)
  }, [fileViewer.width, setPersistedPanelWidth])
  const projects = useProjectStore(s => s.projects)
  const activeProjectId = useProjectStore(s => s.activeProjectId)
  const isLoading = projects === null
  const hasProjects = projects !== null && projects.length > 0
  const showEmptyState = !isLoading && (!hasProjects || !activeProjectId)

  return (
    <div className="h-screen w-screen flex overflow-hidden">
      {/* Sidebar */}
      {sidebarCollapsed ? (
        <div
          className="flex-shrink-0 flex items-center justify-center bg-card border-r border-border"
          style={{ width: SIDEBAR_COLLAPSED_WIDTH }}
        >
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7"
            onClick={() => setSidebarCollapsed(false)}
            title="Expand sidebar"
          >
            <PanelLeftOpen className="h-3.5 w-3.5" />
          </Button>
        </div>
      ) : (
        <>
          <div
            className="flex-shrink-0 overflow-hidden"
            style={{ width: sidebar.width }}
          >
            <Sidebar />
          </div>

          <ResizeHandle
            onMouseDown={sidebar.onMouseDown}
            onResize={(delta) => sidebar.setWidth(w => Math.max(SIDEBAR_MIN, Math.min(SIDEBAR_MAX, w + delta)))}
          />
        </>
      )}

      {/* Main content */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        {showEmptyState ? (
          <NoProjectEmptyState />
        ) : (
          <main className="flex-1 flex min-h-0 min-w-0">
            <div className="flex-1 flex flex-col min-w-0 min-h-0">
              <ChatArea />
              <PendingActionsBar />
              <ExecutionPanels />
              <ChatInput />
            </div>
            {hasOpenFiles && (
              fileViewerCollapsed ? (
                <div
                  className="flex-shrink-0 flex items-center justify-center bg-card border-l border-border"
                  style={{ width: FILE_VIEWER_COLLAPSED_WIDTH }}
                >
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    onClick={() => useFileViewerStore.getState().toggleCollapsed()}
                    title="Expand inspector"
                  >
                    <PanelRightOpen className="h-3.5 w-3.5" />
                  </Button>
                </div>
              ) : (
                <>
                  <ResizeHandle
                    onMouseDown={fileViewer.onMouseDown}
                    onResize={(delta) => fileViewer.setWidth(w => Math.max(FILE_VIEWER_MIN, Math.min(FILE_VIEWER_MAX, w + delta)))}
                  />
                  <FileViewerPanel width={fileViewer.width} />
                </>
              )
            )}
          </main>
        )}
        <StatusBar />
      </div>
    </div>
  )
}
