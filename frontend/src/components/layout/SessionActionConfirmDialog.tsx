import {
  Dialog,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { PathDialogContent, PathWithBreaks } from '@/lib/wrapPath'
import type { PendingSessionAction } from '@/hooks/useSessionActions'

interface SessionActionConfirmDialogProps {
  pending: PendingSessionAction | null
  onConfirm: () => void
  onCancel: () => void
}

/**
 * Confirmation dialog for destructive session actions (archive/delete) on a
 * busy session — one with a running, paused, or unfinished task that the
 * backend will cancel first. Group actions (a branch group's members, see
 * `PendingSessionAction.sessionIds`) render branch-named copy — count-aware,
 * so a single-member group reads "groups 1 session", never a branch name
 * posing as a session name. Rendered by both session list surfaces.
 */
export function SessionActionConfirmDialog({ pending, onConfirm, onCancel }: SessionActionConfirmDialogProps) {
  const isArchive = pending?.kind === 'archive'
  // Group copy whenever the action targets a branch group — INCLUDING a
  // single-member group: there `sessionName` is the BRANCH name, so the
  // singular copy ("Delete session? "<name>"…") would name a branch as a
  // session. The count-aware noun/pronoun keeps the N=1 copy grammatical.
  const groupSize = pending?.sessionIds?.length ?? 0
  const isGroup = groupSize > 0
  const noun = groupSize === 1 ? 'session' : 'sessions'
  return (
    <Dialog open={pending !== null} onOpenChange={(open) => { if (!open) onCancel() }}>
      <PathDialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            {isGroup
              ? `${isArchive ? 'Archive' : 'Delete'} ${noun}?`
              : isArchive ? 'Archive session?' : 'Delete session?'}
          </DialogTitle>
          <DialogDescription>
            {pending ? (
              isGroup ? (
                isArchive ? (
                  // Conditionally-neutral claim: a multi-member group confirms
                  // even when nobody is busy (blast radius), so the copy must
                  // not assert that a task IS in progress.
                  <>
                    Branch "<PathWithBreaks path={pending.sessionName} />" groups {groupSize} {noun}. Archiving will cancel any tasks in progress and make the {noun} read-only.
                  </>
                ) : (
                  <>
                    Branch "<PathWithBreaks path={pending.sessionName} />" groups {groupSize} {noun}. Deleting will cancel any running tasks and permanently remove the {noun} and {groupSize === 1 ? 'its' : 'their'} files.
                  </>
                )
              ) : isArchive ? (
                <>
                  "<PathWithBreaks path={pending.sessionName} />" has a task in progress or unfinished. Archiving will cancel it and make the session read-only.
                </>
              ) : (
                <>
                  "<PathWithBreaks path={pending.sessionName} />" has a task in progress or unfinished. Deleting will cancel it and permanently remove the session and its files.
                </>
              )
            ) : (
              ''
            )}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>Cancel</Button>
          <Button variant={isArchive ? 'default' : 'destructive'} onClick={onConfirm}>
            {isArchive ? 'Archive' : 'Delete'}
          </Button>
        </DialogFooter>
      </PathDialogContent>
    </Dialog>
  )
}
