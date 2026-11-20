import { useCallback, useEffect, useRef } from 'react'
import { FileCode } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useCursorMenuPosition } from '@/lib/cursorMenuPosition'

interface CommitFileContextMenuProps {
  /** Repo-relative path of the file the menu was opened on. */
  path: string
  /**
   * Viewport coordinates where the menu appears (VISUAL px, as reported by
   * `MouseEvent.clientX/clientY`); null renders nothing. Unit conversion and
   * the viewport fit/flip decision live in {@link useCursorMenuPosition}.
   */
  position: { x: number; y: number } | null
  /** Called when the menu should close. */
  onClose: () => void
  /** Open this file's commit-version diff in the viewer. */
  onOpenInViewer: (path: string) => void
}

/**
 * Context menu for ONE changed file inside an expanded history card — a
 * single "Open in Viewer" entry that opens the file's version AT THE COMMIT
 * (its commit diff), not the current working-tree copy. Mirrors the bespoke
 * position-based menu pattern shared by GitFileContextMenu /
 * GitHistoryContextMenu / FileTreeContextMenu (parent owns the position;
 * this component dismisses on click-outside / Escape / scroll).
 */
export function CommitFileContextMenu({
  path,
  position,
  onClose,
  onOpenInViewer,
}: CommitFileContextMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null)
  // Zoom-corrected, viewport-clamped placement (left/top in layout px).
  const menuPosition = useCursorMenuPosition(position, menuRef)

  const handleOpenInViewer = useCallback(() => {
    onOpenInViewer(path)
    onClose()
  }, [path, onOpenInViewer, onClose])

  // Dismiss the dropdown on click-outside / Escape / scroll (mirrors
  // GitFileContextMenu).
  useEffect(() => {
    if (!position) return
    const onDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) onClose()
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('mousedown', onDown, true)
    document.addEventListener('keydown', onKey)
    window.addEventListener('scroll', onClose, true)
    return () => {
      document.removeEventListener('mousedown', onDown, true)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('scroll', onClose, true)
    }
  }, [position, onClose])

  return (
    <>
      {position && (
        <div
          ref={menuRef}
          role="menu"
          aria-label="File actions"
          style={{
            position: 'fixed',
            left: menuPosition?.left ?? 0,
            top: menuPosition?.top ?? 0,
            visibility: menuPosition ? 'visible' : 'hidden',
            zIndex: 9999,
          }}
          className={cn(
            'min-w-48 overflow-hidden rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
            'animate-in fade-in-0 zoom-in-95',
          )}
        >
          <button
            role="menuitem"
            onClick={handleOpenInViewer}
            className={cn(
              'relative flex w-full select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
              'hover:bg-muted/50 focus:bg-muted/50',
              '[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-3.5 [&_svg]:text-muted-foreground',
            )}
          >
            <FileCode className="size-3.5" />
            Open in Viewer
          </button>
        </div>
      )}
    </>
  )
}
