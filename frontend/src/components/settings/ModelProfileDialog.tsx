import { useEffect, useRef, useState } from 'react'
import { AlertTriangle } from 'lucide-react'
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

export type ModelProfileDialogKind = 'duplicate' | 'rename' | 'delete'

interface ModelProfileDialogProps {
  kind: ModelProfileDialogKind
  /** Display name of the profile the dialog acts on. */
  profileName: string
  /** Initial input value for the name dialogs (duplicate proposes "<name> (copy)"). */
  defaultName?: string
  busy?: boolean
  /** Error from a failed confirm; the dialog stays open and shows it. */
  error?: string | null
  /** Name dialogs: called with the trimmed input; delete: called with no args. */
  onConfirm: (name: string) => void
  onClose: () => void
}

const NAME_LABELS: Record<Exclude<ModelProfileDialogKind, 'delete'>, { title: string; action: string }> = {
  duplicate: { title: 'Duplicate profile', action: 'Duplicate' },
  rename: { title: 'Rename profile', action: 'Rename' },
}

/**
 * Controlled dialog for the three profile actions: duplicate / rename (a name
 * input) and delete (a confirmation). Rendered inside the settings modal —
 * Radix nested dialogs are the established pattern here (ModelConfigDialog).
 * Zoom-safe: the shadcn dialog centers via fixed percentages, no viewport
 * units.
 */
export function ModelProfileDialog({
  kind,
  profileName,
  defaultName = '',
  busy = false,
  error = null,
  onConfirm,
  onClose,
}: ModelProfileDialogProps) {
  const [name, setName] = useState(defaultName)
  const inputRef = useRef<HTMLInputElement>(null)

  // A closed dialog unmounts (conditional render in the parent), so the draft
  // state is created fresh per open; an effect focuses the input after mount.
  useEffect(() => {
    if (kind !== 'delete') inputRef.current?.focus()
  }, [kind])

  const trimmed = name.trim()
  const confirm = () => {
    if (kind === 'delete') {
      onConfirm('')
      return
    }
    if (trimmed === '' || busy) return
    onConfirm(trimmed)
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md" data-testid={`model_profiles-profile-dialog-${kind}`}>
        {kind === 'delete' ? (
          <>
            <DialogHeader>
              <DialogTitle>Delete profile</DialogTitle>
              <DialogDescription>
                Delete the custom profile <span className="font-medium text-foreground">{profileName}</span>?
                If it is active, the effective profile falls back to Generic (model-agnostic).
                This cannot be undone.
              </DialogDescription>
            </DialogHeader>
            {error && (
              <div className="flex items-start gap-2 p-2 rounded-md bg-destructive/10 border border-destructive/20 text-xs">
                <AlertTriangle className="h-3.5 w-3.5 text-destructive flex-shrink-0 mt-0.5" />
                <span className="text-destructive">{error}</span>
              </div>
            )}
            <DialogFooter>
              <Button variant="outline" onClick={onClose} disabled={busy}>
                Cancel
              </Button>
              <Button variant="destructive" onClick={confirm} disabled={busy} aria-label="Confirm delete profile">
                {busy ? 'Deleting…' : 'Delete'}
              </Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{NAME_LABELS[kind].title}</DialogTitle>
              <DialogDescription>
                {kind === 'duplicate'
                  ? `Create an editable custom copy of ${profileName}.`
                  : `Rename the custom profile ${profileName}.`}
              </DialogDescription>
            </DialogHeader>
            <div className="flex flex-col gap-1">
              <label htmlFor="model_profiles-profile-name" className="text-xs text-muted-foreground">
                Profile name
              </label>
              <Input
                id="model_profiles-profile-name"
                ref={inputRef}
                value={name}
                placeholder="Profile name"
                disabled={busy}
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') confirm()
                }}
                data-testid="model_profiles-profile-name-input"
              />
            </div>
            {error && (
              <div className="flex items-start gap-2 p-2 rounded-md bg-destructive/10 border border-destructive/20 text-xs">
                <AlertTriangle className="h-3.5 w-3.5 text-destructive flex-shrink-0 mt-0.5" />
                <span className="text-destructive">{error}</span>
              </div>
            )}
            <DialogFooter>
              <Button variant="outline" onClick={onClose} disabled={busy}>
                Cancel
              </Button>
              <Button onClick={confirm} disabled={busy || trimmed === ''} aria-label={`Confirm ${kind} profile`}>
                {busy ? 'Saving…' : kind === 'duplicate' ? 'Duplicate' : 'Rename'}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
