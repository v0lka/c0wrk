// The Remove confirmation of the embedded local model.
//
// A removal is the only irreversible action of this subsystem: it stops the
// server, deletes both trees (about 7 GiB of verified weights among them) and
// drops the generated `embedded` provider, migrating the default model off the
// embedded composite. The block therefore never issues the RPC on the first
// click — this dialog is the gate (specs/domains/embedded-llm.md § Remove).

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

export function EmbeddedLLMRemoveDialog({
  open,
  busy,
  onCancel,
  onConfirm,
}: {
  open: boolean
  busy: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onCancel()
      }}
    >
      <DialogContent className="sm:max-w-md" data-testid="embedded-llm-remove-dialog">
        <DialogHeader>
          <DialogTitle>Remove the embedded model?</DialogTitle>
          <DialogDescription>
            Stops the local server, deletes the runtime and the weights (about 7 GiB) and drops the
            generated "embedded" provider — the default model moves to your next enabled model. The
            auto-unload setting is kept. This cannot be undone: reinstalling downloads everything
            again.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            onClick={onConfirm}
            disabled={busy}
            data-testid="embedded-llm-remove-confirm"
          >
            {busy ? 'Removing…' : 'Remove'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
