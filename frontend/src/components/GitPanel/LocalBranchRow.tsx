import { GitBranch, Check, Loader2, GitMerge, GitFork, Pencil, Trash2, Upload } from 'lucide-react'
import { cn } from '@/lib/utils'
import { ItemAction, ItemActions } from '@/components/layout/ItemAction'
import type { Branch } from '@/types/models'
import type { BranchActionKind } from '@/hooks/useBranchActions'

interface LocalBranchRowProps {
  branch: Branch
  /** The action currently in-flight on this row (null = idle). */
  inFlight: BranchActionKind | null
  /** True when a different row's operation is in-flight — dims & blocks this row. */
  disabled: boolean
  onCheckout: (name: string) => void
  onRename: (name: string) => void
  onMerge: (name: string) => void
  onRebase: (name: string) => void
  onPush: (name: string) => void
  onDelete: (name: string) => void
}

/**
 * A single local branch row. Clicking checks out the branch; hovering reveals
 * the per-branch actions (push / merge / rebase / rename / delete) via the
 * shared ItemAction overlay. Merge & rebase are hidden for the current branch
 * (self-merge/rebase is rejected by git), and delete is disabled there too —
 * the backend refuses to delete the checked-out branch.
 */
export function LocalBranchRow({
  branch,
  inFlight,
  disabled,
  onCheckout,
  onRename,
  onMerge,
  onRebase,
  onPush,
  onDelete,
}: LocalBranchRowProps) {
  const isCurrent = branch.is_current
  // A row is blocked when any operation is running on it OR elsewhere.
  const blocked = disabled || inFlight !== null

  const handleCheckout = () => {
    if (isCurrent || blocked) return
    onCheckout(branch.name)
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      handleCheckout()
    }
  }

  return (
    <div
      role="button"
      tabIndex={isCurrent || blocked ? -1 : 0}
      aria-current={isCurrent ? 'true' : undefined}
      onClick={handleCheckout}
      onKeyDown={handleKeyDown}
      className={cn(
        'group/item relative flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs transition-colors',
        isCurrent ? 'bg-primary/10 text-primary' : 'text-foreground hover:bg-muted',
        disabled && 'cursor-not-allowed opacity-50',
        !disabled && !isCurrent && 'cursor-pointer',
      )}
    >
      <GitBranch className="size-3.5 shrink-0 text-muted-foreground" />
      <span className="flex-1 truncate font-mono">{branch.name}</span>
      {isCurrent && <Check className="size-3.5 shrink-0" />}
      {inFlight === 'checkout' && (
        <Loader2 className="size-3.5 shrink-0 animate-spin text-muted-foreground" />
      )}

      {/* Hover action overlay — push/merge/rebase/rename/delete. */}
      <ItemActions>
        <ItemAction label={`Push ${branch.name}`} onClick={() => onPush(branch.name)} disabled={blocked}>
          {inFlight === 'push' ? (
            <Loader2 className="size-3 animate-spin text-foreground" />
          ) : (
            <Upload className="size-3 text-primary" />
          )}
        </ItemAction>

        {!isCurrent && (
          <ItemAction label={`Merge ${branch.name} into current`} onClick={() => onMerge(branch.name)} disabled={blocked}>
            {inFlight === 'merge' ? (
              <Loader2 className="size-3 animate-spin text-info" />
            ) : (
              <GitMerge className="size-3 text-info" />
            )}
          </ItemAction>
        )}

        {!isCurrent && (
          <ItemAction label={`Rebase current onto ${branch.name}`} onClick={() => onRebase(branch.name)} disabled={blocked}>
            {inFlight === 'rebase' ? (
              <Loader2 className="size-3 animate-spin text-warning" />
            ) : (
              <GitFork className="size-3 text-warning" />
            )}
          </ItemAction>
        )}

        <ItemAction label="Rename" onClick={() => onRename(branch.name)} disabled={blocked}>
          {inFlight === 'rename' ? (
            <Loader2 className="size-3 animate-spin text-info" />
          ) : (
            <Pencil className="size-3 text-info" />
          )}
        </ItemAction>

        <ItemAction
          label={isCurrent ? 'Delete branch' : `Delete ${branch.name}`}
          onClick={() => onDelete(branch.name)}
          disabled={blocked || isCurrent}
          disabledReason={isCurrent ? 'Cannot delete the current branch' : undefined}
        >
          {inFlight === 'delete' ? (
            <Loader2 className="size-3 animate-spin text-destructive" />
          ) : (
            <Trash2 className="size-3 text-destructive" />
          )}
        </ItemAction>
      </ItemActions>
    </div>
  )
}
