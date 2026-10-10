import { ResizeHandle } from '@/components/ResizeHandle'
import { useResize } from '@/hooks/useResize'
import { useUIStore } from '@/stores/uiStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useChatInputController } from '@/hooks/useChatInputController'
import { useFileDrop } from '@/hooks/useFileDrop'
import { ChatInputToolbar } from '@/components/chat/ChatInputToolbar'
import { ChatEditorPane } from '@/components/chat/ChatEditorPane'
import { AttachmentChips } from '@/components/chat/AttachmentChips'
import { ImageErrorBanner } from '@/components/chat/ImageErrorBanner'
import { PromptOptimizeErrorBanner } from '@/components/chat/PromptOptimizeErrorBanner'
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

  // Zoom-aware drag/keyboard resize (shared hook): pointer deltas arrive in
  // visual px under the UI-scale zoom and are converted to layout px before
  // being applied to the panel's layout-px height.
  const resize = useResize({
    initialWidth: height,
    min: 140,
    max: 800,
    // Bottom panel: dragging the top handle up grows the height (the divider
    // follows the pointer).
    direction: -1,
    axis: 'y',
    onChange: setHeight,
  })

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
          onMouseDown={resize.handleMouseDown}
          onKeyDown={resize.handleKeyDown}
        />
        <AttachmentChips />
        <ImageErrorBanner />
        <PromptOptimizeErrorBanner />
        <ChatEditorPane controller={controller} />
        <ChatInputToolbar controller={controller} />
      </div>
    </>
  )
}
