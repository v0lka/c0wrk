import { useEffect, useCallback, useRef } from 'react'
import { MessageSquarePlus } from 'lucide-react'
import { useInputModeStore } from '@/stores/inputModeStore'
import { cn } from '@/lib/utils'

interface FileViewerContextMenuProps {
  /** File reference string to insert, e.g. `@path/to/file.go#L10-25`. */
  reference: string
  /** Viewport coordinates where the menu should appear. */
  position: { x: number; y: number } | null
  /** Called when the menu should close. */
  onClose: () => void
}

/**
 * A contextual dropdown rendered at the pointer position offering "Add to chat".
 *
 * Handles its own dismissal on click outside, Escape key, and window scroll.
 * The parent controls visibility via the `position` prop — when `null`,
 * nothing is rendered.
 */
export function FileViewerContextMenu({ reference, position, onClose }: FileViewerContextMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null)
  const insertTextIntoInput = useInputModeStore((s) => s.insertTextIntoInput)

  const handleAddToChat = useCallback(() => {
    insertTextIntoInput(reference)
    onClose()
  }, [insertTextIntoInput, reference, onClose])

  // Dismiss on click outside
  useEffect(() => {
    if (!position) return
    const handler = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        onClose()
      }
    }
    document.addEventListener('mousedown', handler, true)
    return () => document.removeEventListener('mousedown', handler, true)
  }, [position, onClose])

  // Dismiss on Escape
  useEffect(() => {
    if (!position) return
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [position, onClose])

  // Dismiss on scroll (capture phase to catch all scrolling)
  useEffect(() => {
    if (!position) return
    window.addEventListener('scroll', onClose, true)
    return () => window.removeEventListener('scroll', onClose, true)
  }, [position, onClose])

  if (!position) return null

  return (
    <div
      ref={menuRef}
      role="menu"
      aria-label="File viewer context menu"
      style={{
        position: 'fixed',
        left: position.x,
        top: position.y,
        zIndex: 9999,
      }}
      className={cn(
        'min-w-[10rem] overflow-hidden rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
        'animate-in fade-in-0 zoom-in-95',
      )}
    >
      <button
        role="menuitem"
        onClick={handleAddToChat}
        className={cn(
          'relative flex w-full cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
          'hover:bg-muted/50 focus:bg-muted/50',
          '[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-4 [&_svg]:text-muted-foreground',
        )}
      >
        <MessageSquarePlus className="size-4" />
        Add to chat
      </button>
    </div>
  )
}
