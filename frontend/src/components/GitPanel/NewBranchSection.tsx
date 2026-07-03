import { useCallback, useState } from 'react'
import { Plus, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { createBranch } from '@/api/git'

interface NewBranchSectionProps {
  /** Disabled while a checkout/merge/rebase is in flight elsewhere. */
  disabled: boolean
  /** Clear the picker's shared error banner on input. */
  onClearError: () => void
  /** Surface a creation error to the picker's shared error banner. */
  onError: (message: string) => void
  /** Called after a branch is created — backend emits git:status_changed. */
  onCreated: () => void
}

/**
 * "Create new branch" footer of BranchPicker. Calls CreateBranch (which
 * checks out the new branch immediately) and closes the picker on success.
 * Extracted so BranchPicker stays under the 200-line target (FE-4).
 */
export function NewBranchSection({
  disabled,
  onClearError,
  onError,
  onCreated,
}: NewBranchSectionProps) {
  const [name, setName] = useState('')
  const [creating, setCreating] = useState(false)

  const handleCreate = useCallback(async () => {
    const trimmed = name.trim()
    if (!trimmed || creating || disabled) return
    setCreating(true)
    try {
      await createBranch(trimmed)
      // Backend emits git:status_changed → store updates automatically.
      setName('')
      onCreated()
    } catch (err) {
      onError(err instanceof Error ? err.message : 'Failed to create branch')
    } finally {
      setCreating(false)
    }
  }, [name, creating, disabled, onCreated, onError])

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' && name.trim()) {
      e.preventDefault()
      void handleCreate()
    }
  }

  return (
    <div className="border-t border-border px-4 py-3">
      <div className="mb-1.5 text-xs font-medium text-muted-foreground">
        Create new branch
      </div>
      <div className="flex items-center gap-1.5">
        <Input
          value={name}
          onChange={(e) => {
            setName(e.target.value)
            onClearError()
          }}
          onKeyDown={handleKeyDown}
          placeholder="branch-name"
          className="h-8 text-xs"
        />
        <Button
          variant="secondary"
          size="sm"
          onClick={handleCreate}
          disabled={!name.trim() || creating || disabled}
          title="Create and switch to new branch"
        >
          {creating ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <Plus className="size-3.5" />
          )}
          New
        </Button>
      </div>
    </div>
  )
}
