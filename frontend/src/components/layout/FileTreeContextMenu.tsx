import { useEffect, useCallback, useMemo, useRef, useState } from 'react'
import { Terminal, Copy, Eye, EyeOff, History, Loader2, Microscope, ChevronLeft } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useMessageSender } from '@/hooks/useMessageSender'
import {
  STUDY_MODE_OPTIONS,
  STUDY_PAPER_SKILL,
  buildStudyPrompt,
  type StudyMode,
} from '@/components/papers/paperActions'
import { appendToGitignore } from '@/api/git'
import { runGitOperation } from '@/lib/gitOperation'
import { emit, clipboardSetText } from '@/api/runtime'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useProjectStore } from '@/stores/projectStore'
import { useUIStore } from '@/stores/uiStore'
import { useCursorMenuPosition } from '@/lib/cursorMenuPosition'
import { logger } from '@/lib/logger'
import type { FileEntry } from '@/types/models'

interface FileTreeContextMenuProps {
  entry: FileEntry
  /** Workspace root — when provided, stripped to form the relative path. */
  workspaceRoot: string | null
  /**
   * Viewport coordinates where the menu appears (VISUAL px, as reported by
   * `MouseEvent.clientX/clientY`); null renders nothing. Unit conversion and
   * the viewport fit/flip decision live in {@link useCursorMenuPosition}.
   */
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
 * Contextual menu for a file-tree entry: Open in Viewer (files only),
 * Open in Terminal (directories only), Copy Path, Copy Relative Path,
 * Add to .gitignore, and View History. The two git-dependent actions are
 * shown only when the active project's workspace is a git repository.
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
  // Whether the "Study this paper…" reading-depth picker has replaced the main
  // item list. Declared here so the placement below can re-measure when it flips.
  const [studyOpen, setStudyOpen] = useState(false)

  // The placement hook recomputes only when the anchor identity changes (or on
  // window resize). Flipping `studyOpen` swaps the menu's contents for the
  // TALLER depth picker without moving the pointer, so the anchor identity must
  // track it: otherwise the flip/clamp decision is made against the stale
  // (shorter) height and the picker can grow past the window bottom. `reseed`
  // carries that state into the identity (the hook ignores the extra field).
  const anchorX = position?.x
  const anchorY = position?.y
  const anchor = useMemo(
    () =>
      anchorX === undefined || anchorY === undefined
        ? null
        : { x: anchorX, y: anchorY, reseed: studyOpen },
    [anchorX, anchorY, studyOpen],
  )
  // Zoom-corrected, viewport-clamped placement (left/top in layout px).
  const menuPosition = useCursorMenuPosition(anchor, menuRef)

  // Git-only actions ("Add to .gitignore", "View History") make no sense in
  // a project whose workspace is not a git repository — the Git panel does
  // not even exist there. The pairing with the checked project id keeps a
  // stale answer from a previously active project from showing the items.
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const isGitRepo = useGitPanelStore(
    (s) => s.isGitRepo && s.gitRepoProjectId === activeProjectId,
  )

  // --- Study this paper… (PDF files only) ---
  // The `study-paper` skill accepts a PDF at a path; the gesture opens a
  // compact reading-depth picker inside the menu, then dispatches the skill
  // with the file path and the chosen depth.
  const { send } = useMessageSender()
  const isPdf = !entry.is_dir && entry.path.toLowerCase().endsWith('.pdf')

  // Drop the depth picker whenever the menu retargets another entry or closes
  // (the position prop is a fresh object per open; x/y pin the actual point,
  // so a plain re-render with the same point never resets the picker).
  useEffect(() => {
    setStudyOpen(false)
  }, [entry.path, position?.x, position?.y])

  const handleStudy = useCallback(
    (mode: StudyMode) => {
      setStudyOpen(false)
      // send() reports send failures in-chat itself; it rethrows only when the
      // auto-created session fails (the documented splash race). The file tree
      // has no panel-level error banner, so surface that as a runtime error.
      Promise.resolve(
        send(buildStudyPrompt(entry.path, mode), [STUDY_PAPER_SKILL], undefined, undefined, {
          newSession: false,
        }),
      ).catch((err) => {
        logger.error('Failed to dispatch study-paper:', err)
        emit('runtime_error', {
          id: crypto.randomUUID(),
          message: 'Failed to start studying the paper',
        })
      })
      onClose()
    },
    [entry.path, send, onClose],
  )

  // --- Open in Viewer (files only) ---
  const handleOpenInViewer = useCallback(() => {
    useFileViewerStore.getState().openFile(entry.path)
    onClose()
  }, [entry.path, onClose])

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
    const projectId = useProjectStore.getState().activeProjectId
    setIsIgnoring(true)
    try {
      if (projectId === null) {
        // No active project to key an operation record against (the file tree
        // only exists with one — defensive). Run the mutation and surface a
        // failure via the log; there is no console to record it to.
        try {
          await appendToGitignore(relativePath)
        } catch (err) {
          logger.error('Failed to append to .gitignore:', err)
        }
        return
      }
      // Success is silent (the entry leaves the tree); only a failure is
      // recorded in the operation console.
      const outcome = await runGitOperation({
        projectId,
        kind: 'gitignore',
        label: `Added ${relativePath} to .gitignore`,
        fn: () => appendToGitignore(relativePath),
        recordSuccess: false,
        logLevel: 'warn',
      })
      if (!outcome.ok) {
        // Switch to the Git panel so the recorded failure (shown in its
        // footer console) is visible — the user is on the Explorer tab and
        // wouldn't see it otherwise. Per-project: record the switch against
        // the active project.
        useUIStore.getState().setWorkspaceTab(projectId, 'git')
      }
    } finally {
      setIsIgnoring(false)
      onClose()
    }
  }, [relativePath, onClose])

  // --- View History ---
  const handleViewHistory = useCallback(() => {
    // Per-project: record the workspace-tab + Git-section switch against the
    // active project so switching away and back restores this view.
    const projectId = useProjectStore.getState().activeProjectId
    if (projectId !== null) {
      useUIStore.getState().setWorkspaceTab(projectId, 'git')
      useGitPanelStore.getState().setActiveTab(projectId, 'history')
    }
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
    'relative flex w-full select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none',
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
          style={{
            position: 'fixed',
            left: menuPosition?.left ?? 0,
            top: menuPosition?.top ?? 0,
            visibility: menuPosition ? 'visible' : 'hidden',
            zIndex: 9999,
          }}
          className={cn(
            'min-w-[12rem] overflow-hidden rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
            'animate-in fade-in-0 zoom-in-95',
          )}
        >
          {studyOpen ? (
            <>
              <div className="px-2 py-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                Reading depth
              </div>
              {STUDY_MODE_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  role="menuitem"
                  data-testid={`file-study-mode-${option.value}`}
                  onClick={() => handleStudy(option.value)}
                  className={menuItemClass}
                >
                  <Microscope className="size-4" />
                  {option.label}
                </button>
              ))}
              <MenuSeparator />
              <button
                role="menuitem"
                data-testid="file-study-back"
                onClick={() => setStudyOpen(false)}
                className={menuItemClass}
              >
                <ChevronLeft className="size-4" />
                Back
              </button>
            </>
          ) : (
            <>
              {!entry.is_dir && (
                <button
                  role="menuitem"
                  onClick={handleOpenInViewer}
                  className={menuItemClass}
                >
                  <Eye className="size-4" />
                  Open in Viewer
                </button>
              )}
              {!entry.is_dir && <MenuSeparator />}
              {isPdf && (
                <>
                  <button
                    role="menuitem"
                    data-testid="file-study-open"
                    onClick={() => setStudyOpen(true)}
                    className={menuItemClass}
                  >
                    <Microscope className="size-4" />
                    Study this paper…
                  </button>
                  <MenuSeparator />
                </>
              )}
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
              {isGitRepo && (
                <>
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
                </>
              )}
            </>
          )}
        </div>
      )}
    </>
  )
}

function MenuSeparator() {
  return <div className="my-1 h-px bg-border" />
}
