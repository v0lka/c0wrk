import { useEffect, useCallback, useRef, useState } from 'react'
import { Terminal, Copy, EyeOff, History, Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { appendToGitignore } from '@/api/git'
import { emit, clipboardSetText } from '@/api/runtime'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useUIStore } from '@/stores/uiStore'
import { logger } from '@/lib/logger'
import type { FileEntry } from '@/types/models'

interface FileTreeContextMenuProps {
  entry: FileEntry
  /** Workspace root — when provided, stripped to form the relative path. */
  workspaceRoot: string | null
  /** Viewport coordinates where the menu appears; null renders nothing. */
  position: { x: number; y: number } | null
  /** Called when the menu should close. */
  onClose: () => void
}

/** Platform path separator — Windows uses `\`, POSIX uses `/`. */
const PATH_SEP = navigator.platform.includes('Win') ? '\\' : '/'

/** Repo-relative path suitable as a .gitignore pattern / display string.
 *  Matches workspaceRoot only at a path-separator boundary to avoid a sibling
 *  directory sharing the same prefix (e.g. "/repo" vs "/repo-extra"). Uses a
 *  platform-aware separator so it works on Windows as well as macOS/Linux. */
function toRelativePath(path: string, workspaceRoot?: string | null): string {
  if (workspaceRoot && (path === workspaceRoot || path.startsWith(workspaceRoot + PATH_SEP))) {
    return path.slice(workspaceRoot.length).replace(/^[\\/]/, '')
  }
  return path
}

/**
 * Contextual menu for a file-tree entry: Open in Terminal (directories only),
 * Copy Path, Copy Relative Path, Add to .gitignore, and View History.
 * Self-contained — calls the API and stores directly, so no callback prop
 * threading is required.
 */
export function FileTreeContextMenu({
  entry,
  workspaceRoot,
  position,
  onClose,
}: FileTreeContextMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null)
  const [isIgnoring, setIsIgnoring] = useState(false)
  const relativePath = toRelativePath(entry.path, workspaceRoot ?? undefined)

  // --- Open in Terminal (directories only) ---
  const handleOpenInTerminal = useCallback(() => {
    useInputModeStore.getState().setPendingTerminalDir(entry.path)
    useInputModeStore.getState().setMode('terminal')
    onClose()
  }, [entry.path, onClose])

  // --- Copy Path (absolute) ---
  const handleCopyPath = useCallback(async () => {
    try {
      await clipboardSetText(entry.path)
    } catch (err) {
      logger.error('Failed to copy path:', err)
      emit('runtime_error', {
        id: crypto.randomUUID(),
        message: 'Failed to copy path to clipboard',
      })
    }
    onClose()
  }, [entry.path, onClose])

  // --- Copy Relative Path ---
  const handleCopyRelativePath = useCallback(async () => {
    try {
      await clipboardSetText(relativePath)
    } catch (err) {
      logger.error('Failed to copy relative path:', err)
      emit('runtime_error', {
        id: crypto.randomUUID(),
        message: 'Failed to copy relative path to clipboard',
      })
    }
    onClose()
  }, [relativePath, onClose])

  // --- Add to .gitignore ---
  const handleAddToGitignore = useCallback(async () => {
    setIsIgnoring(true)
    try {
      await appendToGitignore(relativePath)
    } catch (err) {
      useGitPanelStore.getState().setError(
        err instanceof Error ? err.message : 'Failed to update .gitignore',
      )
      // Switch to the Git panel so the store-level error banner is visible —
      // the user is on the Explorer tab and wouldn't see it otherwise.
      useUIStore.getState().setWorkspaceTab('git')
    } finally {
      setIsIgnoring(false)
      onClose()
    }
  }, [relativePath, onClose])

  // --- View History ---
  const handleViewHistory = useCallback(() => {
    useUIStore.getState().setWorkspaceTab('git')
    useGitPanelStore.getState().setActiveTab('history')
    // For a directory, append the OS path separator so the glob filter
    // matches only files *inside* it — not a sibling that shares the same
    // prefix (e.g. "src/components" would otherwise also match
    // "src/components-extra"). Uses the platform-aware separator so it
    // works on Windows as well as macOS/Linux.
    const filter = entry.is_dir ? relativePath + PATH_SEP : relativePath
    useGitPanelStore.getState().setPendingHistoryFilter(filter)
    onClose()
  }, [entry.is_dir, relativePath, onClose])

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

  const menuItemClass = cn(
    'relative flex w-full cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
    'hover:bg-muted/50 focus:bg-muted/50 disabled:opacity-50',
    '[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-4 [&_svg]:text-muted-foreground',
  )

  return (
    <>
      {position && (
        <div
          ref={menuRef}
          role="menu"
          aria-label="File tree actions"
          style={{ position: 'fixed', left: position.x, top: position.y, zIndex: 9999 }}
          className={cn(
            'min-w-[12rem] overflow-hidden rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
            'animate-in fade-in-0 zoom-in-95',
          )}
        >
          {entry.is_dir && (
            <button
              role="menuitem"
              onClick={handleOpenInTerminal}
              className={menuItemClass}
            >
              <Terminal className="size-4" />
              Open in Terminal
            </button>
          )}
          {entry.is_dir && <MenuSeparator />}
          <button
            role="menuitem"
            onClick={handleCopyPath}
            className={menuItemClass}
          >
            <Copy className="size-4" />
            Copy Path
          </button>
          <button
            role="menuitem"
            onClick={handleCopyRelativePath}
            className={menuItemClass}
          >
            <Copy className="size-4" />
            Copy Relative Path
          </button>
          <MenuSeparator />
          <button
            role="menuitem"
            disabled={isIgnoring}
            onClick={() => void handleAddToGitignore()}
            className={menuItemClass}
          >
            {isIgnoring ? <Loader2 className="size-4 animate-spin" /> : <EyeOff className="size-4" />}
            Add to .gitignore
          </button>
          <button
            role="menuitem"
            onClick={handleViewHistory}
            className={menuItemClass}
          >
            <History className="size-4" />
            View History
          </button>
        </div>
      )}
    </>
  )
}

function MenuSeparator() {
  return <div className="my-1 h-px bg-border" />
}
