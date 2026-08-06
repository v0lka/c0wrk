import { useCallback, useEffect, useRef } from 'react'
import { ResizeHandle } from '@/components/ResizeHandle'
import { useUIStore } from '@/stores/uiStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useChatInputController } from '@/hooks/useChatInputController'
import { useFileDrop } from '@/hooks/useFileDrop'
import { ChatInputToolbar } from '@/components/chat/ChatInputToolbar'
import { ChatEditorPane } from '@/components/chat/ChatEditorPane'
import { AttachmentChips } from '@/components/chat/AttachmentChips'
import { ImageErrorBanner } from '@/components/chat/ImageErrorBanner'
import { DropzoneOverlay } from '@/components/chat/DropzoneOverlay'
import { cn } from '@/lib/utils'

/**
 * ChatInput is the bottom-of-screen input shell composed of:
 * - A drag-resizable header (ResizeHandle)
 * - The chat/terminal pane swap (ChatEditorPane)
 * - The bottom toolbar with mode toggles + send/optimize/cancel (ChatInputToolbar)
 *
 * State and handlers live in useChatInputController so this component stays
 * presentational. (W-28 split)
 */
export function ChatInput() {
  const controller = useChatInputController()
  const sidebarCollapsed = useUIStore((s) => s.sidebarCollapsed)
  const viewerCollapsed = useFileViewerStore((s) => s.collapsed)
  const { height, setHeight, activeSessionId } = controller

  // Native OS drag-and-drop → attachment staging. Active only in chat mode;
  // dragActive drives the full-window drop-zone highlight overlay.
  const { dragActive } = useFileDrop(activeSessionId)

  const cleanupRef = useRef<(() => void) | null>(null)

  useEffect(() => {
    return () => { cleanupRef.current?.() }
  }, [])

  const handleResizeStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    const startY = e.clientY
    const startHeight = height

    const handleMouseMove = (e: MouseEvent) => {
      const delta = startY - e.clientY
      const newHeight = Math.max(140, Math.min(800, startHeight + delta))
      setHeight(newHeight)
    }

    const handleMouseUp = () => {
      document.removeEventListener('mousemove', handleMouseMove)
      document.removeEventListener('mouseup', handleMouseUp)
      cleanupRef.current = null
    }

    document.addEventListener('mousemove', handleMouseMove)
    document.addEventListener('mouseup', handleMouseUp)
    cleanupRef.current = handleMouseUp
  }, [height, setHeight])

  const handleResizeKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      setHeight(height + 20)
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      setHeight(height - 20)
    }
  }, [height, setHeight])

  return (
    <>
      <DropzoneOverlay active={dragActive} />
      <div
        className={cn(
          'flex flex-col flex-shrink-0 border-t border-x border-border bg-card overflow-hidden',
          sidebarCollapsed && 'ml-1',
          viewerCollapsed && 'mr-1',
        )}
        style={{ height }}
      >
        <ResizeHandle
          orientation="horizontal"
          onMouseDown={handleResizeStart}
          onKeyDown={handleResizeKeyDown}
        />
        <AttachmentChips />
        <ImageErrorBanner />
        <ChatEditorPane controller={controller} />
        <ChatInputToolbar controller={controller} />
      </div>
    </>
  )
}
