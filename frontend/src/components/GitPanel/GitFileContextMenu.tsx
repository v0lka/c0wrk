import { useEffect, useCallback, useRef, useState } from 'react'
import { Trash2, EyeOff, FileCode, Loader2, Plus, Minus } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useCursorMenuPosition } from '@/lib/cursorMenuPosition'
import { discardChanges, appendToGitignore, stageFile, unstageFile } from '@/api/git'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore } from '@/stores/projectStore'
import { runGitOperation } from '@/lib/gitOperation'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import type { StageSide } from '@/lib/gitStatus'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

interface GitFileContextMenuProps {
  entry: GitPanelEntry
  /**
   * The porcelain axis of the row that opened this menu: 'index' → the action
   * is "Unstage"; 'worktree' → "Stage". Chosen by the row, not the entry's
   * coarse `staged` flag, so an `MM` file's two rows offer opposite actions.
   */
  side: StageSide
  /** Workspace root — when provided, stripped to form the .gitignore pattern. */
  workspaceRoot?: string
  /**
   * Viewport coordinates where the menu appears (VISUAL px, as reported by
   * `MouseEvent.clientX/clientY`); null renders nothing. Unit conversion and
   * the viewport fit/flip decision live in {@link useCursorMenuPosition}.
   */
  position: { x: number; y: number } | null
  /** Called when the menu (and any spawned dialog) should close. */
  onClose: () => void
}

/** Repo-relative path suitable as a .gitignore pattern / display string.
 *  Matches workspaceRoot only at a path-separator boundary to avoid a sibling
 *  directory sharing the same prefix (e.g. "/repo" vs "/repo-extra"). */
function toRelativePath(path: string, workspaceRoot?: string): string {
  if (workspaceRoot && (path === workspaceRoot || path.startsWith(workspaceRoot + '/'))) {
    return path.slice(workspaceRoot.length).replace(/^\//, '')
  }
  return path
}

/**
 * Contextual menu for a git file entry: Stage/Unstage (first item, the label,
 * icon and action chosen by the row's `side` axis), Discard Changes (with
 * confirm), Add to .gitignore, and Open in Viewer. Self-contained — calls the
 * API and stores directly, so no callback prop threading is required.
 */
export function GitFileContextMenu({
  entry,
  side,
  workspaceRoot,
  position,
  onClose,
}: GitFileContextMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [isDiscarding, setIsDiscarding] = useState(false)
  const [isIgnoring, setIsIgnoring] = useState(false)
  const [isStaging, setIsStaging] = useState(false)
  const relativePath = toRelativePath(entry.path, workspaceRoot)
  // Zoom-corrected, viewport-clamped placement (left/top in layout px).
  const menuPosition = useCursorMenuPosition(position, menuRef)

  // --- Stage / Unstage ---
  const handleToggleStage = useCallback(async () => {
    const projectId = useProjectStore.getState().activeProjectId
    setIsStaging(true)
    try {
      if (projectId) {
        // Success is silent (the row simply moves between the staged /
        // unstaged sections); only a failure is recorded in the console.
        await runGitOperation({
          projectId,
          kind: side === 'index' ? 'unstage' : 'stage',
          label: `${side === 'index' ? 'Unstaged' : 'Staged'} ${entry.path}`,
          fn: () =>
            side === 'index' ? unstageFile(entry.path) : stageFile(entry.path),
          recordSuccess: false,
        })
      }
    } finally {
      setIsStaging(false)
      // Close the menu regardless of outcome; failures surface in the console.
      onClose()
    }
  }, [entry.path, side, onClose])

  // --- Discard (with confirmation) ---
  const handleConfirmDiscard = useCallback(async () => {
    const projectId = useProjectStore.getState().activeProjectId
    setIsDiscarding(true)
    try {
      if (projectId) {
        await runGitOperation({
          projectId,
          kind: 'discard',
          label: `Discarded changes in ${entry.path}`,
          fn: () => discardChanges(entry.path),
          recordSuccess: false,
        })
      }
      // Backend emits git:status_changed → useGitStatusEvents refreshes.
      setConfirmOpen(false)
      onClose()
    } finally {
      setIsDiscarding(false)
    }
  }, [entry.path, onClose])

  // --- Add to .gitignore ---
  const handleAddToGitignore = useCallback(async () => {
    const projectId = useProjectStore.getState().activeProjectId
    setIsIgnoring(true)
    try {
      if (projectId) {
        // Success is silent (the entry leaves the tree); only a failure is
        // recorded in the operation console.
        await runGitOperation({
          projectId,
          kind: 'gitignore',
          label: `Added ${relativePath} to .gitignore`,
          fn: () => appendToGitignore(relativePath),
          recordSuccess: false,
        })
      }
    } finally {
      setIsIgnoring(false)
      // Close the menu regardless of outcome; failures surface in the console.
      onClose()
    }
  }, [relativePath, onClose])

  // --- Open in Viewer (normal mode, no diff) ---
  const handleOpenInViewer = useCallback(() => {
    useFileViewerStore.getState().openFile(entry.path)
    onClose()
  }, [entry.path, onClose])

  // Dismiss the dropdown on click-outside / Escape / scroll (mirrors
  // FileViewerContextMenu). The confirm dialog manages its own dismissal.
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
          aria-label="Git file actions"
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
              '[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-4 [&_svg]:text-muted-foreground',
            )}
          >
            <FileCode className="size-4" />
            Open in Viewer
          </button>
          <button
            role="menuitem"
            disabled={isStaging}
            onClick={() => void handleToggleStage()}
            className={cn(
              'relative flex w-full select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
              'hover:bg-muted/50 focus:bg-muted/50 disabled:opacity-50',
              '[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-4 [&_svg]:text-muted-foreground',
            )}
          >
            {isStaging ? (
              <Loader2 className="size-4 animate-spin" />
            ) : side === 'index' ? (
              <Minus className="size-4" />
            ) : (
              <Plus className="size-4" />
            )}
            {side === 'index' ? 'Unstage' : 'Stage'}
          </button>
          <button
            role="menuitem"
            onClick={() => {
              onClose()
              setConfirmOpen(true)
            }}
            className={cn(
              'relative flex w-full select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
              'hover:bg-destructive/10 focus:bg-destructive/10 text-destructive',
              '[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-4',
            )}
          >
            <Trash2 className="size-4" />
            Discard Changes
          </button>
          <button
            role="menuitem"
            disabled={isIgnoring}
            onClick={() => void handleAddToGitignore()}
            className={cn(
              'relative flex w-full select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
              'hover:bg-muted/50 focus:bg-muted/50 disabled:opacity-50',
              '[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-4 [&_svg]:text-muted-foreground',
            )}
          >
            {isIgnoring ? <Loader2 className="size-4 animate-spin" /> : <EyeOff className="size-4" />}
            Add to .gitignore
          </button>
        </div>
      )}

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent showCloseButton={false} className="max-w-sm">
          <DialogHeader>
            <DialogTitle>Discard changes?</DialogTitle>
            <DialogDescription>
              This will permanently discard all local changes to{' '}
              <span className="font-mono text-foreground">{relativePath}</span>. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setConfirmOpen(false)}
              disabled={isDiscarding}
            >
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => void handleConfirmDiscard()} disabled={isDiscarding}>
              {isDiscarding && <Loader2 className="size-4 animate-spin" />}
              Discard
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
