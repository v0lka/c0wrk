import { useEffect, useCallback, useRef } from 'react'
import { X, Copy, FolderTree, Terminal } from 'lucide-react'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { clipboardSetText, emit } from '@/api/runtime'
import { revealInWorkspace } from '@/lib/revealInWorkspace'
import { relativePath } from '@/lib/localFileLink'
import { logger } from '@/lib/logger'
import { cn } from '@/lib/utils'

interface FileViewerTabContextMenuProps {
  /** The file path of the tab the menu was opened on. */
  path: string
  /** Viewport coordinates where the menu appears; null renders nothing. */
  position: { x: number; y: number } | null
  /** Called when the menu should close. */
  onClose: () => void
}

/**
 * Contextual menu for a file-viewer tab: Close / Close Others / Close All,
 * Copy Path / Copy Relative Path, Reveal In Workspace, Open In Terminal.
 *
 * Path-dependent items (Copy, Reveal, Open In Terminal) are disabled for
 * non-filesystem tabs — virtual files (e.g. blackboard attachments) and
 * synthetic pseudo-paths (`c0wrk:…`) — because they have no real on-disk
 * path to operate on. Close actions apply to every tab.
 *
 * Self-contained — calls the API and stores directly, so no callback prop
 * threading is required.
 */
export function FileViewerTabContextMenu({ path, position, onClose }: FileViewerTabContextMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null)

  // The file tree's root is the authoritative workspace root: "Reveal In
  // Workspace" reveals inside that exact tree, and the relative path is
  // computed against it so it matches what the tree shows.
  const rootPath = useFileTreeStore((s) => s.rootPath)
  // A tab is backed by a real file unless it is virtual or a synthetic
  // pseudo-path (e.g. `c0wrk:review`, `c0wrk:commit:<sha>`).
  const isVirtualTab = useFileViewerStore((s) => s.files[path]?.virtual === true)
  const isRealFile = !path.startsWith('c0wrk:') && !isVirtualTab

  // --- Close ---
  const handleClose = useCallback(() => {
    useFileViewerStore.getState().closeFile(path)
    onClose()
  }, [path, onClose])

  // --- Close Others ---
  const handleCloseOthers = useCallback(() => {
    useFileViewerStore.getState().closeOthersFiles(path)
    onClose()
  }, [path, onClose])

  // --- Close All ---
  const handleCloseAll = useCallback(() => {
    useFileViewerStore.getState().closeAllFiles()
    onClose()
  }, [onClose])

  // --- Copy Path (absolute) ---
  const handleCopyPath = useCallback(async () => {
    try {
      await clipboardSetText(path)
    } catch (err) {
      logger.error('Failed to copy path:', err)
      emit('runtime_error', {
        id: crypto.randomUUID(),
        message: 'Failed to copy path to clipboard',
      })
    }
    onClose()
  }, [path, onClose])

  // --- Copy Relative Path ---
  const handleCopyRelativePath = useCallback(async () => {
    const rel = rootPath ? relativePath(rootPath, path) : path
    try {
      await clipboardSetText(rel)
    } catch (err) {
      logger.error('Failed to copy relative path:', err)
      emit('runtime_error', {
        id: crypto.randomUUID(),
        message: 'Failed to copy relative path to clipboard',
      })
    }
    onClose()
  }, [path, rootPath, onClose])

  // --- Reveal In Workspace ---
  const handleRevealInWorkspace = useCallback(async () => {
    try {
      await revealInWorkspace(path)
    } catch (err) {
      logger.error('Failed to reveal in workspace:', err)
    }
    onClose()
  }, [path, onClose])

  // --- Open In Terminal (parent directory as cwd) ---
  const handleOpenInTerminal = useCallback(() => {
    const lastSlash = path.lastIndexOf('/')
    const dir = lastSlash > 0 ? path.slice(0, lastSlash) : path
    useInputModeStore.getState().setPendingTerminalDir(dir)
    useInputModeStore.getState().setMode('terminal')
    onClose()
  }, [path, onClose])

  // Dismiss the menu on click-outside / Escape / scroll (mirrors
  // FileTreeContextMenu).
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

  const menuItemClass = cn(
    'relative flex w-full cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
    'hover:bg-muted/50 focus:bg-muted/50 disabled:opacity-50 disabled:pointer-events-none',
    '[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-4 [&_svg]:text-muted-foreground',
  )

  return (
    <>
      {position && (
        <div
          ref={menuRef}
          role="menu"
          aria-label="File viewer tab actions"
          style={{ position: 'fixed', left: position.x, top: position.y, zIndex: 9999 }}
          className={cn(
            'min-w-[12rem] overflow-hidden rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
            'animate-in fade-in-0 zoom-in-95',
          )}
        >
          <button role="menuitem" onClick={handleClose} className={menuItemClass}>
            <X className="size-4" />
            Close
          </button>
          <button role="menuitem" onClick={handleCloseOthers} className={menuItemClass}>
            <X className="size-4" />
            Close Others
          </button>
          <button role="menuitem" onClick={handleCloseAll} className={menuItemClass}>
            <X className="size-4" />
            Close All
          </button>
          <MenuSeparator />
          <button role="menuitem" disabled={!isRealFile} onClick={() => void handleCopyPath()} className={menuItemClass}>
            <Copy className="size-4" />
            Copy Path
          </button>
          <button role="menuitem" disabled={!isRealFile} onClick={() => void handleCopyRelativePath()} className={menuItemClass}>
            <Copy className="size-4" />
            Copy Relative Path
          </button>
          <MenuSeparator />
          <button role="menuitem" disabled={!isRealFile} onClick={() => void handleRevealInWorkspace()} className={menuItemClass}>
            <FolderTree className="size-4" />
            Reveal In Workspace
          </button>
          <button role="menuitem" disabled={!isRealFile} onClick={handleOpenInTerminal} className={menuItemClass}>
            <Terminal className="size-4" />
            Open In Terminal
          </button>
        </div>
      )}
    </>
  )
}

function MenuSeparator() {
  return <div className="my-1 h-px bg-border" />
}
