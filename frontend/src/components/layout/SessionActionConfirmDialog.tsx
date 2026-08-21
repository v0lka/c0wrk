import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import type { PendingSessionAction } from '@/hooks/useSessionActions'

interface SessionActionConfirmDialogProps {
  pending: PendingSessionAction | null
  onConfirm: () => void
  onCancel: () => void
}

/**
 * Confirmation dialog for destructive session actions (archive/delete) on a
 * busy session — one with a running, paused, or unfinished task that the
 * backend will cancel first. Rendered by both session list surfaces.
 */
export function SessionActionConfirmDialog({ pending, onConfirm, onCancel }: SessionActionConfirmDialogProps) {
  const isArchive = pending?.kind === 'archive'
  return (
    <Dialog open={pending !== null} onOpenChange={(open) => { if (!open) onCancel() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{isArchive ? 'Archive session?' : 'Delete session?'}</DialogTitle>
          <DialogDescription>
            {pending
              ? isArchive
                ? `"${pending.sessionName}" has a task in progress or unfinished. Archiving will cancel it and make the session read-only.`
                : `"${pending.sessionName}" has a task in progress or unfinished. Deleting will cancel it and permanently remove the session and its files.`
              : ''}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>Cancel</Button>
          <Button variant={isArchive ? 'default' : 'destructive'} onClick={onConfirm}>
            {isArchive ? 'Archive' : 'Delete'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
