import { useEffect, useState } from 'react'
import { AlertCircle } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { PathWithBreaks } from '@/lib/wrapPath'
import type { PendingWorktreeDelete } from '@/hooks/useWorktreeActions'

interface WorktreeDeleteConfirmDialogProps {
  pending: PendingWorktreeDelete | null
  /**
   * Backend failure from the last confirmed attempt, shown inline so the
   * user can react in place: a dirty tree refuses with "worktree has
   * uncommitted changes" (retry with Force), a locked tree with "worktree
   * is locked" (tick the unlock box). Null after a fresh request.
   */
  error?: string | null
  onConfirm: (mode: 'safe' | 'force', unlock: boolean) => void
  onCancel: () => void
}

/**
 * Confirmation dialog for worktree deletion (mandatory — the removal
 * discards the tree's directory from disk).
 *
 * - Safe delete maps to plain `git worktree remove`: git's protection
 *   against uncommitted changes stays in force (the backend refuses a
 *   dirty tree).
 * - Force delete maps to `git worktree remove --force`: it discards the
 *   tree's uncommitted changes.
 * - A locked tree offers an explicit "unlock before removing" consent
 *   (the backend refuses a locked tree without it).
 * - The branch the tree was on is never deleted.
 *
 * A backend refusal re-opens the dialog with the verbatim reason (see
 * useWorktreeActions), where the user can retry with Force / unlock or
 * cancel.
 */
export function WorktreeDeleteConfirmDialog({
  pending,
  error,
  onConfirm,
  onCancel,
}: WorktreeDeleteConfirmDialogProps) {
  const locked = pending?.worktree.locked === true
  const [unlock, setUnlock] = useState(false)
  // Unlock consent is per-tree: a fresh pending worktree resets it, so a
  // confirmation given for one tree can never leak into the next dialog.
  useEffect(() => {
    setUnlock(false)
  }, [pending?.worktree.path])

  // A dirty-tree refusal names the missing confirmation; point the user at
  // the Force path right in the dialog instead of leaving the raw error.
  const dirtyRefusal = error !== null && error !== undefined && error.includes('uncommitted changes')

  return (
    <Dialog open={pending !== null} onOpenChange={(open) => { if (!open) onCancel() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Delete worktree?</DialogTitle>
          <DialogDescription>
            {pending ? (
              <>
                <PathWithBreaks path={pending.worktree.name} className="font-medium" />
                {pending.worktree.branch && (
                  <PathWithBreaks
                    path={pending.worktree.branch}
                    className="ml-1.5 font-mono text-xs"
                  />
                )}
                <span className="mt-1 block break-all font-mono text-xs text-muted-foreground">
                  {pending.worktree.path}
                </span>
                <span className="mt-1 block">
                  The worktree directory will be deleted from disk; its branch is kept. Safe delete
                  (git worktree remove) keeps git's protection against uncommitted changes; force
                  delete (git worktree remove --force) discards them.
                </span>
              </>
            ) : (
              ''
            )}
          </DialogDescription>
        </DialogHeader>
        {locked && pending && (
          <label className="flex items-start gap-2 text-xs text-muted-foreground" data-testid="worktree-unlock-row">
            <input
              type="checkbox"
              checked={unlock}
              onChange={(e) => setUnlock(e.target.checked)}
              data-testid="worktree-unlock-checkbox"
              className="mt-0.5 size-3.5 accent-primary"
            />
            <span>
              Unlock before removing (git worktree unlock) — this worktree is locked, and removal
              refuses while it stays locked.
            </span>
          </label>
        )}
        {error && (
          <div className="flex flex-col gap-0.5 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
            <span className="flex items-center gap-1.5">
              <AlertCircle className="size-3.5 shrink-0" />
              <span className="min-w-0 break-words">{error}</span>
            </span>
            {dirtyRefusal && <span>Force delete discards the uncommitted changes.</span>}
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onCancel} title="Close without deleting">Cancel</Button>
          <Button
            variant="destructive"
            onClick={() => onConfirm('force', unlock)}
            title="git worktree remove --force"
          >
            Force delete
          </Button>
          <Button
            variant="destructive"
            onClick={() => onConfirm('safe', unlock)}
            title="git worktree remove"
          >
            Delete
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
