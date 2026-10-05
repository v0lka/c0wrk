import { Loader2, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import type { DeleteTarget } from './useResearchProjectActions'

interface ConfirmDeleteResearchDialogProps {
  /** The research project pending deletion; null keeps the dialog closed. */
  target: DeleteTarget | null
  /** True while the delete RPC is in flight (locks both buttons). */
  deleting: boolean
  /** Inline error rendered inside the dialog after a failed delete. */
  deleteError: string | null
  onConfirm: () => void
  onClose: () => void
}

/**
 * Destructive-confirmation modal for deleting a research project (R-NNN):
 * its directory tree, index rows, and pins are removed permanently. Extracted
 * from ResearchProjectPicker so the picker stays a thin combobox shell and
 * the dialog's failure/retry UX can be tested without mounting the dropdown.
 */
export function ConfirmDeleteResearchDialog({
  target,
  deleting,
  deleteError,
  onConfirm,
  onClose,
}: ConfirmDeleteResearchDialogProps) {
  return (
    <Dialog
      open={target !== null}
      onOpenChange={(o) => {
        if (!o && !deleting) onClose()
      }}
    >
      <DialogContent className="sm:max-w-md" showCloseButton={false}>
        <DialogHeader>
          <div className="flex items-center gap-3">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-full bg-destructive/15">
              <Trash2 className="size-5 text-destructive" />
            </div>
            <div className="flex flex-col gap-1">
              <DialogTitle>Delete research project?</DialogTitle>
              <DialogDescription>
                {target?.id} — {target?.title}. Its directory tree, index rows, and pins
                are removed permanently. This cannot be undone.
              </DialogDescription>
            </div>
          </div>
        </DialogHeader>

        {deleteError && (
          <div className="text-xs text-destructive" role="alert">
            {deleteError}
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" title="Cancel deletion" onClick={onClose} disabled={deleting} autoFocus>
            Cancel
          </Button>
          <Button variant="destructive" title="Delete research" onClick={onConfirm} disabled={deleting}>
            {deleting && <Loader2 className="size-3.5 animate-spin" />}
            Delete
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
