// The Remove confirmation of the embedded local model.
//
// A removal is the only irreversible action of this subsystem: it stops the
// server, deletes the parts of the install the chosen scope names and drops
// the generated `embedded` provider (migrating the default model). The block
// therefore never issues the RPC on the first click — this dialog is the gate
// (specs/domains/embedded-llm.md § Remove).
//
// The copy is SCOPE-AWARE: a partial removal states exactly which bytes go and
// which survive as a cache, because "reinstalling downloads everything again"
// is only true of the full removal — the downloader re-verifies surviving
// artifacts instead of re-fetching them.

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import type { EmbeddedLLMRemoveScope } from '@/api/embedded'

interface ScopeCopy {
  title: string
  description: string
}

function scopeCopy(scope: EmbeddedLLMRemoveScope): ScopeCopy {
  switch (scope) {
    case 'runtime':
      return {
        title: 'Remove the runtime?',
        description:
          'Stops the local server, deletes the inference runtime and drops the generated ' +
          '"embedded" provider — the default model moves to your next enabled model. The weights ' +
          'and the vision projector stay on disk as a verified cache: reinstalling re-verifies ' +
          'them instead of downloading the model again. This cannot be undone.',
      }
    case 'weights':
      return {
        title: 'Remove the weights?',
        description:
          'Stops the local server, deletes the model weights (about 7 GiB) and drops the ' +
          'generated "embedded" provider — the default model moves to your next enabled model. ' +
          'The runtime and the vision projector stay on disk as a verified cache: reinstalling ' +
          're-downloads only the weights. This cannot be undone.',
      }
    case 'projection':
      return {
        title: 'Remove the vision projector?',
        description:
          'Stops the local server, deletes the vision projector (image input stops working ' +
          'until it is reinstalled) and drops the generated "embedded" provider — the default ' +
          'model moves to your next enabled model. The runtime and the weights stay on disk as ' +
          'a verified cache. This cannot be undone.',
      }
    default:
      return {
        title: 'Remove the embedded model?',
        description:
          'Stops the local server, deletes the runtime and the weights (about 7 GiB) and drops ' +
          'the generated "embedded" provider — the default model moves to your next enabled ' +
          'model. The auto-unload setting is kept. This cannot be undone: reinstalling ' +
          'downloads everything again.',
      }
  }
}

export function EmbeddedLLMRemoveDialog({
  open,
  busy,
  scope = 'all',
  onCancel,
  onConfirm,
}: {
  open: boolean
  busy: boolean
  /** What the confirmed removal deletes; "all" is the historical full
   *  removal. */
  scope?: EmbeddedLLMRemoveScope
  onCancel: () => void
  onConfirm: () => void
}) {
  const copy = scopeCopy(scope)
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onCancel()
      }}
    >
      <DialogContent className="sm:max-w-md" data-testid="embedded-llm-remove-dialog">
        <DialogHeader>
          <DialogTitle data-testid="embedded-llm-remove-title">{copy.title}</DialogTitle>
          <DialogDescription data-testid="embedded-llm-remove-description">
            {copy.description}
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
