import { useRef, useCallback, useState, useEffect, type MouseEvent as ReactMouseEvent } from 'react'
import { Sidebar } from './Sidebar'
import { StatusBar } from './StatusBar'
import { ChatArea } from '@/components/chat/ChatArea'
import { ChatInput } from '@/components/chat/ChatInput'
import { ExecutionPanels } from '@/components/chat/ExecutionPanels'
import { PendingActionsBar } from '@/components/chat/PendingActionsBar'
import { useProjectStore } from '@/stores/projectStore'
import { NoProjectEmptyState } from '@/components/project/NoProjectEmptyState'

const SIDEBAR_DEFAULT = 300
const SIDEBAR_MIN = 180
const SIDEBAR_MAX = 500

function useResizeHandle(
  defaultWidth: number,
  minWidth: number,
  maxWidth: number,
  side: 'left' | 'right'
) {
  const [width, setWidth] = useState(defaultWidth)
  const dragging = useRef(false)
  const startX = useRef(0)
  const startWidth = useRef(0)
  const moveHandlerRef = useRef<((ev: globalThis.MouseEvent) => void) | null>(null)
  const upHandlerRef = useRef<(() => void) | null>(null)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      if (moveHandlerRef.current) {
        document.removeEventListener('mousemove', moveHandlerRef.current)
        moveHandlerRef.current = null
      }
      if (upHandlerRef.current) {
        document.removeEventListener('mouseup', upHandlerRef.current)
        upHandlerRef.current = null
      }
      dragging.current = false
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
  }, [])

  const onMouseDown = useCallback(
    (e: ReactMouseEvent) => {
      e.preventDefault()
      dragging.current = true
      startX.current = e.clientX
      startWidth.current = width

      const onMouseMove = (ev: MouseEvent) => {
        if (!dragging.current) return
        const delta = ev.clientX - startX.current
        const newWidth =
          side === 'left'
            ? startWidth.current + delta
            : startWidth.current - delta
        setWidth(Math.max(minWidth, Math.min(maxWidth, newWidth)))
      }

      const onMouseUp = () => {
        dragging.current = false
        document.removeEventListener('mousemove', onMouseMove)
        document.removeEventListener('mouseup', onMouseUp)
        document.body.style.cursor = ''
        document.body.style.userSelect = ''
        if (mountedRef.current) {
          moveHandlerRef.current = null
          upHandlerRef.current = null
        }
      }

      moveHandlerRef.current = onMouseMove
      upHandlerRef.current = onMouseUp

      document.addEventListener('mousemove', onMouseMove)
      document.addEventListener('mouseup', onMouseUp)
      document.body.style.cursor = 'col-resize'
      document.body.style.userSelect = 'none'
    },
    [width, minWidth, maxWidth, side]
  )

  return { width, setWidth, onMouseDown }
}

interface ResizeHandleProps {
  onMouseDown: (e: ReactMouseEvent) => void
  onResize: (delta: number) => void
}

function ResizeHandle({ onMouseDown, onResize }: ResizeHandleProps) {
  return (
    <div
      className="w-1 flex-shrink-0 bg-border hover:bg-ring active:bg-ring transition-colors cursor-col-resize focus:outline-none focus:bg-ring"
      onMouseDown={onMouseDown}
      role="separator"
      aria-label="Resize panel"
      aria-orientation="vertical"
      tabIndex={0}
      onKeyDown={(e) => {
        const step = e.shiftKey ? 50 : 10
        if (e.key === 'ArrowLeft') {
          e.preventDefault()
          onResize(-step)
        } else if (e.key === 'ArrowRight') {
          e.preventDefault()
          onResize(step)
        }
      }}
    />
  )
}

export function AppLayout() {
  const sidebar = useResizeHandle(SIDEBAR_DEFAULT, SIDEBAR_MIN, SIDEBAR_MAX, 'left')
  const projects = useProjectStore(s => s.projects)
  const activeProjectId = useProjectStore(s => s.activeProjectId)
  const isLoading = projects === null
  const hasProjects = projects !== null && projects.length > 0
  const showEmptyState = !isLoading && (!hasProjects || !activeProjectId)

  return (
    <div className="h-screen w-screen flex overflow-hidden">
      {/* Sidebar */}
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
          </main>
        )}
        <StatusBar />
      </div>
    </div>
  )
}
