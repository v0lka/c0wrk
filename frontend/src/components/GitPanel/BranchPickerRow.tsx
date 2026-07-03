import { useState } from 'react'
import { GitBranch, Check, Loader2, GitMerge, GitFork } from 'lucide-react'
import { cn } from '@/lib/utils'
import { merge, rebase } from '@/api/git'

interface BranchPickerRowProps {
  name: string
  isCurrent: boolean
  isBusy: boolean
  /** SHA/name of the branch currently checked out (used to block self-ops). */
  currentBranchName: string
  onCheckout: (name: string) => void
  /** Surface an error message to the picker's shared error banner. */
  onError: (message: string) => void
  /** Called after a merge/rebase succeeds — picker closes & store auto-refreshes. */
  onActionSuccess: () => void
}

/**
 * A single branch row inside BranchPicker. Handles checkout (click) and the
 * per-branch merge/rebase initiate actions (FE-4 / D2), shown on hover.
 *
 * merge()/rebase() exist on the backend; on success the backend emits
 * `git:status_changed`, which `useGitStatusEvents` picks up to refresh
 * status + mergeRebaseState (toolbar abort buttons appear if conflicts).
 */
export function BranchPickerRow({
  name,
  isCurrent,
  isBusy,
  currentBranchName,
  onCheckout,
  onError,
  onActionSuccess,
}: BranchPickerRowProps) {
  const [actionInProgress, setActionInProgress] = useState<'merge' | 'rebase' | null>(null)

  // Self-merge/rebase is a no-op git rejects — disable it.
  const isSelf = name === currentBranchName
  const disabled = isCurrent || isBusy || actionInProgress !== null

  const runAction = async (op: 'merge' | 'rebase') => {
    if (disabled || isSelf) return
    setActionInProgress(op)
    try {
      if (op === 'merge') {
        await merge(name)
      } else {
        await rebase(name)
      }
      // Backend emits git:status_changed → store refreshes automatically.
      onActionSuccess()
    } catch (err) {
      onError(
        err instanceof Error
          ? err.message
          : `Failed to ${op} ${name}`,
      )
    } finally {
      setActionInProgress(null)
    }
  }

  const handleCheckout = () => {
    if (!disabled) onCheckout(name)
  }

  return (
    <button
      type="button"
      onClick={handleCheckout}
      disabled={disabled}
      className={cn(
        'group flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs transition-colors',
        'focus:outline-none focus:ring-1 focus:ring-ring',
        isCurrent
          ? 'bg-primary/10 text-primary'
          : 'hover:bg-muted text-foreground',
        !isCurrent && isBusy && 'cursor-not-allowed opacity-50',
      )}
    >
      <GitBranch className="size-3.5 shrink-0 text-muted-foreground" />
      <span className="flex-1 truncate font-mono">{name}</span>

      {/* Merge / rebase actions — revealed on hover, hidden for the current branch. */}
      {!isCurrent && !isSelf && (
        <span className="flex shrink-0 items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity">
          <span
            role="button"
            tabIndex={disabled ? -1 : 0}
            aria-label={`Merge ${name} into current`}
            title="Merge into current"
            onClick={(e) => {
              e.stopPropagation()
              void runAction('merge')
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault()
                e.stopPropagation()
                void runAction('merge')
              }
            }}
            className={cn(
              'flex items-center justify-center size-5 rounded text-info hover:bg-info/15',
              disabled && 'pointer-events-none opacity-40',
            )}
          >
            {actionInProgress === 'merge' ? (
              <Loader2 className="size-3.5 animate-spin" />
            ) : (
              <GitMerge className="size-3.5" />
            )}
          </span>
          <span
            role="button"
            tabIndex={disabled ? -1 : 0}
            aria-label={`Rebase current onto ${name}`}
            title="Rebase current onto"
            onClick={(e) => {
              e.stopPropagation()
              void runAction('rebase')
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault()
                e.stopPropagation()
                void runAction('rebase')
              }
            }}
            className={cn(
              'flex items-center justify-center size-5 rounded text-warning hover:bg-warning/15',
              disabled && 'pointer-events-none opacity-40',
            )}
          >
            {actionInProgress === 'rebase' ? (
              <Loader2 className="size-3.5 animate-spin" />
            ) : (
              <GitFork className="size-3.5" />
            )}
          </span>
        </span>
      )}

      {actionInProgress === null && isCurrent ? (
        <Check className="size-3.5 shrink-0" />
      ) : null}
    </button>
  )
}
