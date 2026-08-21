import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import type { PendingBranchDelete } from '@/hooks/useBranchActions'

interface BranchDeleteConfirmDialogProps {
  pending: PendingBranchDelete | null
  onConfirm: (mode: 'safe' | 'force') => void
  onCancel: () => void
}

/**
 * Confirmation dialog for branch deletion.
 *
 * - Local branches offer a safe/force choice: safe maps to `git branch -d`
 *   (keeps the "not fully merged" protection), force maps to `git branch -D`
 *   (discards unmerged work).
 * - Remote branches offer a single confirm (`git push <remote> --delete`).
 */
export function BranchDeleteConfirmDialog({
  pending,
  onConfirm,
  onCancel,
}: BranchDeleteConfirmDialogProps) {
  const isLocal = pending?.kind === 'local'

  return (
    <Dialog open={pending !== null} onOpenChange={(open) => { if (!open) onCancel() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{isLocal ? 'Delete branch?' : 'Delete remote branch?'}</DialogTitle>
          <DialogDescription>
            {pending
              ? isLocal
                ? `"${pending.name}" will be deleted locally. Safe delete (git branch -d) keeps git's protection against deleting a branch that isn't fully merged; force delete (git branch -D) discards it regardless.`
                : `"${pending.remote}/${pending.name}" will be deleted from the remote.`
              : ''}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>Cancel</Button>
          {isLocal ? (
            <>
              <Button
                variant="destructive"
                onClick={() => onConfirm('force')}
                title="git branch -D"
              >
                Force delete
              </Button>
              <Button
                variant="destructive"
                onClick={() => onConfirm('safe')}
                title="git branch -d"
              >
                Delete
              </Button>
            </>
          ) : (
            <Button variant="destructive" onClick={() => onConfirm('safe')}>
              Delete
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
