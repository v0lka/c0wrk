import { useEffect, useCallback, useRef, useState, type ReactNode } from 'react'
import {
  GitBranch,
  Tag as TagIcon,
  RotateCcw,
  Eye,
  Copy,
  Trash2,
  Upload,
  CornerDownRight,
  Loader2,
  ChevronRight,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import {
  createTag,
  deleteTag,
  pushTag,
  deleteRemoteTag,
  resetToCommit,
} from '@/api/git'
import { clipboardSetText, emit } from '@/api/runtime'
import { logger } from '@/lib/logger'

/** Reset modes offered by the "Reset <branch> to This Commit" submenu. */
const RESET_MODES = [
  {
    mode: 'soft' as const,
    label: 'Soft',
    description: 'keep staged & working tree',
  },
  {
    mode: 'mixed' as const,
    label: 'Mixed',
    description: 'unstage, keep working tree',
  },
  {
    mode: 'hard' as const,
    label: 'Hard',
    description: 'discard all changes',
  },
]

/** Extract tag names from a commit's `refs` decorations (tag: <name>). */
function tagNamesFromRefs(refs: string[]): string[] {
  return refs
    .filter((r) => r.startsWith('tag: '))
    .map((r) => r.slice('tag: '.length))
}

/** Shared class for a menu button (mirrors GitFileContextMenu item styling). */
const itemClass =
  'relative flex w-full cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-4 [&_svg:not([class*=\'text-\'])]:text-muted-foreground'

// ---------------------------------------------------------------------------
// Submenu — a self-contained hover-driven nested menu. Opens on mouseenter,
// closes on mouseleave of the whole trigger+content area. The nested content
// is positioned at the trigger's right edge via `left-full top-0`, so no DOM
// measurement is required (and it renders deterministically in jsdom).
// ---------------------------------------------------------------------------

interface SubmenuProps {
  label: ReactNode
  icon?: ReactNode
  disabled?: boolean
  children: ReactNode
}

function Submenu({ label, icon, disabled, children }: SubmenuProps) {
  const [open, setOpen] = useState(false)
  return (
    <div
      className="relative"
      onMouseEnter={() => !disabled && setOpen(true)}
      onMouseLeave={() => setOpen(false)}
    >
      <button
        type="button"
        role="menuitem"
        disabled={disabled}
        className={cn(itemClass, 'w-full hover:bg-muted/50 disabled:opacity-50')}
      >
        {icon}
        <span className="flex-1 text-left truncate">{label}</span>
        <ChevronRight className="ml-auto size-3.5 text-muted-foreground" />
      </button>
      {open && !disabled && (
        <div
          role="menu"
          className={cn(
            'absolute left-full top-0 min-w-48 overflow-hidden rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
            'animate-in fade-in-0 zoom-in-95',
          )}
        >
          {children}
        </div>
      )}
    </div>
  )
}

/** A leaf menu button. */
interface MenuItemProps {
  icon?: ReactNode
  children: ReactNode
  variant?: 'default' | 'destructive'
  disabled?: boolean
  onClick: () => void
}

function MenuItem({ icon, children, variant = 'default', disabled, onClick }: MenuItemProps) {
  return (
    <button
      type="button"
      role="menuitem"
      disabled={disabled}
      onClick={onClick}
      className={cn(
        itemClass,
        'w-full',
        variant === 'destructive'
          ? 'text-destructive hover:bg-destructive/10 focus:bg-destructive/10 [&_svg]:text-destructive'
          : 'hover:bg-muted/50 disabled:opacity-50',
      )}
    >
      {icon}
      <span className="flex-1 text-left truncate">{children}</span>
    </button>
  )
}

// ---------------------------------------------------------------------------
// Main menu
// ---------------------------------------------------------------------------

interface GitHistoryContextMenuProps {
  /** Full commit SHA the menu was opened on. */
  sha: string
  /** Git ref decorations for the commit (branch names, `tag: <name>`, HEAD). */
  refs: string[]
  /** Name of the currently checked-out branch (for the "Reset" label). */
  currentBranch: string
  /** Viewport coordinates where the menu appears; null renders nothing. */
  position: { x: number; y: number } | null
  /** Called when the menu (and any spawned dialog) should close. */
  onClose: () => void
  /** Called after a successful mutating op so the history can refresh. */
  onAfterMutation: () => void
}

/**
 * Bespoke context menu for a single commit in the Git History list.
 *
 * Structure (mirrors the user request):
 *   - View Commit   (open read-only commit review)
 *   - Copy          (copy the commit SHA)
 *   ──
 *   - Create
 *       └ Branch   (open Switch Branch dialog with this commit as base)
 *       └ Tag       (prompt for a name, create a tag on this commit)
 *   - Reset <branch> to This Commit
 *       ├ Soft  (keep staged & working tree)
 *       ├ Mixed (unstage, keep working tree)
 *       └ Hard  (discard all changes — with confirmation)
 *   ──
 *   - Tag (only when the commit carries tags)
 *       └ <tagName>
 *           ├ Create Branch  (switch dialog with the tag as base)
 *           ├ Push Tag
 *           ├ Delete Tag local
 *           ├ Delete Tag remote
 *           └ Copy          (copy the tag name)
 *
 * Follows the bespoke position-based menu pattern used by GitFileContextMenu /
 * FileTreeContextMenu (parent owns the position; this component dismisses on
 * click-outside / Escape / scroll). Nested submenus are hover-driven via the
 * self-contained `Submenu` helper. All mutations call the git API directly
 * and invoke `onAfterMutation` on success so the parent can reload history.
 */
export function GitHistoryContextMenu({
  sha,
  refs,
  currentBranch,
  position,
  onClose,
  onAfterMutation,
}: GitHistoryContextMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null)

  // Tag-creation dialog state.
  const [tagDialogOpen, setTagDialogOpen] = useState(false)
  const [tagName, setTagName] = useState('')
  const [tagTargetSha, setTagTargetSha] = useState('')
  const [creatingTag, setCreatingTag] = useState(false)
  const [tagError, setTagError] = useState<string | null>(null)

  // Hard-reset confirmation dialog state.
  const [resetConfirmOpen, setResetConfirmOpen] = useState(false)
  const [resetting, setResetting] = useState(false)
  const [resetError, setResetError] = useState<string | null>(null)

  const tags = tagNamesFromRefs(refs)

  // ── View Commit ────────────────────────────────────────────────────
  const handleViewCommit = useCallback(() => {
    const { openFile, setCollapsed } = useFileViewerStore.getState()
    openFile(`c0wrk:commit:${sha}`)
    setCollapsed(false)
    onClose()
  }, [sha, onClose])

  // ── Copy SHA ───────────────────────────────────────────────────────
  const handleCopySha = useCallback(async () => {
    onClose()
    try {
      await clipboardSetText(sha)
    } catch (err) {
      logger.error('Failed to copy commit SHA:', err)
      emit('runtime_error', {
        id: crypto.randomUUID(),
        message: 'Failed to copy commit SHA',
      })
    }
  }, [sha, onClose])

  // ── Create › Branch (open Switch Branch dialog with this commit as base) ──
  const handleCreateBranch = useCallback(
    (base: string) => {
      onClose()
      useGitPanelStore.getState().setPendingBranchBase(base)
      useGitPanelStore.getState().openBranchPicker()
    },
    [onClose],
  )

  // ── Create › Tag (open the prompt dialog) ──────────────────────────
  const openCreateTagDialog = useCallback(() => {
    setTagTargetSha(sha)
    setTagName('')
    setTagError(null)
    setTagDialogOpen(true)
    onClose()
  }, [sha, onClose])

  const handleConfirmCreateTag = useCallback(async () => {
    const trimmed = tagName.trim()
    if (!trimmed || creatingTag) return
    setCreatingTag(true)
    setTagError(null)
    try {
      await createTag(trimmed, tagTargetSha)
      setTagDialogOpen(false)
      onAfterMutation()
    } catch (err) {
      setTagError(err instanceof Error ? err.message : 'Failed to create tag')
    } finally {
      setCreatingTag(false)
    }
  }, [tagName, creatingTag, tagTargetSha, onAfterMutation])

  // ── Reset <branch> to This Commit ──────────────────────────────────
  const handleReset = useCallback(
    (mode: 'soft' | 'mixed' | 'hard') => {
      onClose()
      // Hard reset is destructive — route through the confirmation dialog.
      if (mode === 'hard') {
        setResetError(null)
        setResetConfirmOpen(true)
        return
      }
      void (async () => {
        try {
          await resetToCommit(sha, mode)
          onAfterMutation()
        } catch (err) {
          useGitPanelStore.getState().setError(
            err instanceof Error ? err.message : 'Failed to reset branch',
          )
        }
      })()
    },
    [sha, onClose, onAfterMutation],
  )

  const handleConfirmHardReset = useCallback(async () => {
    setResetting(true)
    setResetError(null)
    try {
      await resetToCommit(sha, 'hard')
      setResetConfirmOpen(false)
      onAfterMutation()
    } catch (err) {
      setResetError(err instanceof Error ? err.message : 'Failed to reset branch')
    } finally {
      setResetting(false)
    }
  }, [sha, onAfterMutation])

  // ── Per-tag actions ────────────────────────────────────────────────
  const handlePushTag = useCallback(
    (tagName: string) => {
      onClose()
      void (async () => {
        try {
          const out = await pushTag(tagName, '')
          // git pushes its progress to stderr, returned in the combined
          // output. There is no success-toast channel (the app only toasts
          // errors) and the branch Push output panel lives in
          // GitPanelFooter local state, which this menu cannot reach — so
          // surface the output via the logger for debuggability.
          if (out) {
            logger.info(`Pushed tag ${tagName}:`, out)
          }
          onAfterMutation()
        } catch (err) {
          useGitPanelStore.getState().setError(
            err instanceof Error ? err.message : 'Failed to push tag',
          )
        }
      })()
    },
    [onClose, onAfterMutation],
  )

  const handleDeleteTagLocal = useCallback(
    (tagName: string) => {
      onClose()
      void (async () => {
        try {
          await deleteTag(tagName)
          onAfterMutation()
        } catch (err) {
          useGitPanelStore.getState().setError(
            err instanceof Error ? err.message : 'Failed to delete tag',
          )
        }
      })()
    },
    [onClose, onAfterMutation],
  )

  const handleDeleteTagRemote = useCallback(
    (tagName: string) => {
      onClose()
      void (async () => {
        try {
          await deleteRemoteTag(tagName, '')
          onAfterMutation()
        } catch (err) {
          useGitPanelStore.getState().setError(
            err instanceof Error ? err.message : 'Failed to delete remote tag',
          )
        }
      })()
    },
    [onClose, onAfterMutation],
  )

  const handleCopyTagName = useCallback(
    async (tagName: string) => {
      onClose()
      try {
        await clipboardSetText(tagName)
      } catch (err) {
        logger.error('Failed to copy tag name:', err)
        emit('runtime_error', {
          id: crypto.randomUUID(),
          message: 'Failed to copy tag name',
        })
      }
    },
    [onClose],
  )

  // Dismiss the dropdown on click-outside / Escape / scroll (mirrors
  // GitFileContextMenu). The dialogs manage their own dismissal.
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

  const branchLabel = currentBranch || 'HEAD'

  return (
    <>
      {position && (
        <div
          ref={menuRef}
          role="menu"
          aria-label="Commit actions"
          style={{ position: 'fixed', left: position.x, top: position.y, zIndex: 9999 }}
          className={cn(
            'min-w-52 overflow-visible rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
            'animate-in fade-in-0 zoom-in-95',
          )}
        >
          <MenuItem icon={<Eye />} onClick={handleViewCommit}>
            View Commit
          </MenuItem>
          <MenuItem icon={<Copy />} onClick={() => void handleCopySha()}>
            Copy
          </MenuItem>

          {/* separator */}
          <div className="-mx-1 my-1 h-px bg-border" />

          {/* Create */}
          <Submenu icon={<CornerDownRight />} label="Create">
            <MenuItem icon={<GitBranch />} onClick={() => handleCreateBranch(sha)}>
              Branch
            </MenuItem>
            <MenuItem icon={<TagIcon />} onClick={openCreateTagDialog}>
              Tag
            </MenuItem>
          </Submenu>

          {/* Reset <branch> to This Commit */}
          <Submenu icon={<RotateCcw />} label={`Reset ${branchLabel} to This Commit`}>
            {RESET_MODES.map(({ mode, label, description }) => (
              <MenuItem
                key={mode}
                variant={mode === 'hard' ? 'destructive' : 'default'}
                onClick={() => handleReset(mode)}
              >
                {label} ({description})
              </MenuItem>
            ))}
          </Submenu>

          {/* Per-tag submenu — only when the commit carries tags. */}
          {tags.length > 0 && (
            <>
              <div className="-mx-1 my-1 h-px bg-border" />
              <Submenu icon={<TagIcon />} label="Tag">
                {tags.map((tag) => (
                  <Submenu key={tag} icon={<TagIcon />} label={tag}>
                    <MenuItem
                      icon={<GitBranch />}
                      onClick={() => handleCreateBranch(tag)}
                    >
                      Create Branch
                    </MenuItem>
                    <MenuItem icon={<Upload />} onClick={() => handlePushTag(tag)}>
                      Push Tag
                    </MenuItem>
                    <MenuItem
                      variant="destructive"
                      icon={<Trash2 />}
                      onClick={() => handleDeleteTagLocal(tag)}
                    >
                      Delete Tag local
                    </MenuItem>
                    <MenuItem
                      variant="destructive"
                      icon={<Trash2 />}
                      onClick={() => handleDeleteTagRemote(tag)}
                    >
                      Delete Tag remote
                    </MenuItem>
                    <MenuItem icon={<Copy />} onClick={() => void handleCopyTagName(tag)}>
                      Copy
                    </MenuItem>
                  </Submenu>
                ))}
              </Submenu>
            </>
          )}
        </div>
      )}

      {/* Create Tag dialog */}
      <Dialog open={tagDialogOpen} onOpenChange={setTagDialogOpen}>
        <DialogContent showCloseButton={false} className="max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2 text-sm">
              <TagIcon className="size-4" />
              Create Tag
            </DialogTitle>
            <DialogDescription>
              Create a lightweight tag on commit{' '}
              <span className="font-mono text-foreground">{tagTargetSha.slice(0, 7)}</span>.
            </DialogDescription>
          </DialogHeader>
          <div className="px-1">
            <Input
              value={tagName}
              onChange={(e) => {
                setTagName(e.target.value)
                setTagError(null)
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && tagName.trim()) {
                  e.preventDefault()
                  void handleConfirmCreateTag()
                }
              }}
              placeholder="v1.0.0"
              className="h-8 text-xs"
              autoFocus
            />
            {tagError && (
              <p className="mt-2 text-xs text-destructive">{tagError}</p>
            )}
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setTagDialogOpen(false)}
              disabled={creatingTag}
            >
              Cancel
            </Button>
            <Button
              onClick={() => void handleConfirmCreateTag()}
              disabled={!tagName.trim() || creatingTag}
            >
              {creatingTag && <Loader2 className="size-4 animate-spin" />}
              Create
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Hard reset confirmation dialog */}
      <Dialog open={resetConfirmOpen} onOpenChange={setResetConfirmOpen}>
        <DialogContent showCloseButton={false} className="max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2 text-sm">
              <RotateCcw className="size-4 text-destructive" />
              Hard reset {branchLabel}?
            </DialogTitle>
            <DialogDescription>
              This will reset{' '}
              <span className="font-mono text-foreground">{branchLabel}</span>{' '}
              to commit{' '}
              <span className="font-mono text-foreground">{sha.slice(0, 7)}</span>{' '}
              and <strong>permanently discard</strong> all uncommitted changes
              (staged and unstaged). This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          {resetError && (
            <p className="px-1 text-xs text-destructive">{resetError}</p>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setResetConfirmOpen(false)}
              disabled={resetting}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => void handleConfirmHardReset()}
              disabled={resetting}
            >
              {resetting && <Loader2 className="size-4 animate-spin" />}
              Hard Reset
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
