import { useRef, useCallback, useState, useEffect, type MouseEvent as ReactMouseEvent } from 'react'
import { Sidebar } from './Sidebar'
import { StatusBar } from './StatusBar'
import { InspectorPanel } from '@/components/inspector/InspectorPanel'
import { ChatArea } from '@/components/chat/ChatArea'
import { ChatInput } from '@/components/chat/ChatInput'
import { ExecutionPanels } from '@/components/chat/ExecutionPanels'
import { PendingActionsBar } from '@/components/chat/PendingActionsBar'

const SIDEBAR_DEFAULT = 260
const SIDEBAR_MIN = 180
const SIDEBAR_MAX = 400

const INSPECTOR_DEFAULT = 320
const INSPECTOR_MIN = 200
const INSPECTOR_MAX = 500

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

  // Cleanup effect for unmount during active drag
  useEffect(() => {
    return () => {
      if (moveHandlerRef.current) {
        document.removeEventListener('mousemove', moveHandlerRef.current)
      }
      if (upHandlerRef.current) {
        document.removeEventListener('mouseup', upHandlerRef.current)
      }
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
        moveHandlerRef.current = null
        upHandlerRef.current = null
      }

      // Store references for cleanup
      moveHandlerRef.current = onMouseMove
      upHandlerRef.current = onMouseUp

      document.addEventListener('mousemove', onMouseMove)
      document.addEventListener('mouseup', onMouseUp)
      document.body.style.cursor = 'col-resize'
      document.body.style.userSelect = 'none'
    },
    [width, minWidth, maxWidth, side]
  )

  return { width, onMouseDown }
}

function ResizeHandle({ onMouseDown }: { onMouseDown: (e: ReactMouseEvent) => void }) {
  return (
    <div
      className="w-1 flex-shrink-0 bg-border hover:bg-ring active:bg-ring transition-colors cursor-col-resize"
      onMouseDown={onMouseDown}
    />
  )
}

export function AppLayout() {
  const sidebar = useResizeHandle(SIDEBAR_DEFAULT, SIDEBAR_MIN, SIDEBAR_MAX, 'left')
  const inspector = useResizeHandle(INSPECTOR_DEFAULT, INSPECTOR_MIN, INSPECTOR_MAX, 'right')

  return (
    <div className="h-screen w-screen flex overflow-hidden">
      {/* Sidebar */}
      <div
        className="flex-shrink-0 overflow-hidden"
        style={{ width: sidebar.width }}
      >
        <Sidebar />
      </div>

      <ResizeHandle onMouseDown={sidebar.onMouseDown} />

      {/* Main content */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        <main className="flex-1 flex min-h-0 min-w-0">
          <div className="flex-1 flex flex-col min-w-0 min-h-0">
            <ChatArea />
            <PendingActionsBar />
            <ExecutionPanels />
            <ChatInput />
          </div>
        </main>
        <StatusBar />
      </div>

      <ResizeHandle onMouseDown={inspector.onMouseDown} />

      {/* Inspector */}
      <div
        className="flex-shrink-0 overflow-hidden"
        style={{ width: inspector.width }}
      >
        <InspectorPanel />
      </div>
    </div>
  )
}
