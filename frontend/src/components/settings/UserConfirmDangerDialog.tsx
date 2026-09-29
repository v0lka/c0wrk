// User-confirm danger dialog — the explicit gate in front of
// security.silent_mode.user_confirm = "confirm".
//
// "Confirm" is the one silent-mode value that lets a confirmation-gated tool
// call run with NO human whenever the strict judge cannot clear it (the judge
// speaks CONFIRM, or is missing, errors, times out, or returns an unparseable
// verdict). Selecting it is therefore never persisted straight from the
// dropdown: SilentModeCard opens this dialog instead, and the value is saved
// only after an explicit confirmation here. Dismissing the dialog (overlay
// click, Escape, the close button, or Cancel) is a cancel — the caller leaves
// the stored value untouched, which for the controlled dropdown is exactly the
// revert.

import { AlertTriangle, ShieldAlert } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'

interface UserConfirmDangerDialogProps {
  open: boolean
  /**
   * Radix open-state change. Receives `false` on every dismissal (overlay
   * click, Escape, close button) and on Cancel; the caller treats it as a
   * cancel and leaves the stored value untouched.
   */
  onOpenChange: (open: boolean) => void
  /**
   * The explicit confirmation — persist user_confirm = "confirm". Invoked only
   * by the destructive button.
   */
  onConfirm: () => void
}

export function UserConfirmDangerDialog({ open, onOpenChange, onConfirm }: UserConfirmDangerDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md" data-testid="user-confirm-danger-dialog">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-destructive">
            <ShieldAlert className="h-4 w-4 shrink-0" />
            Enable unattended confirm execution?
          </DialogTitle>
          <DialogDescription>
            The <strong>Confirm</strong> sub-policy lets a confirmation-gated tool call run with no
            human whenever the strict judge cannot clear it, and the call is audited as an allow. It
            is the most permissive silent-mode value — pick it only if you intend the agent to act
            fully unattended.
          </DialogDescription>
        </DialogHeader>

        <div
          role="alert"
          data-testid="user-confirm-danger-note"
          className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/10 p-3 text-xs text-destructive"
        >
          <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
          <span>
            A fail-closed CONFIRM outcome — the judge says CONFIRM, or the judge is unavailable,
            errors, times out, or returns an unparseable verdict — will{' '}
            <strong>execute without confirmation</strong>. A deliberate judge DENY is still blocked.
            Enable this only while you accept unsupervised execution of ambiguous calls.
          </span>
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            data-testid="user-confirm-danger-cancel"
          >
            Cancel
          </Button>
          <Button
            variant="destructive"
            onClick={onConfirm}
            data-testid="user-confirm-danger-confirm"
          >
            <ShieldAlert className="h-3.5 w-3.5" />
            Enable unattended
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
