import { Server, Loader2, Trash2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { ItemAction, ItemActions } from '@/components/layout/ItemAction'
import type { Branch } from '@/types/models'
import type { BranchActionKind } from '@/hooks/useBranchActions'

interface RemoteBranchRowProps {
  branch: Branch
  /** The action currently in-flight on this row (null = idle). */
  inFlight: BranchActionKind | null
  /** True when a different row's operation is in-flight — dims & blocks this row. */
  disabled: boolean
  /** Called with the full remote-tracking ref (e.g. `origin/feature/x`). */
  onCheckoutRemote: (remoteBranch: string) => void
  /** Called with the short branch name + remote (e.g. `feature/x`, `origin`). */
  onDeleteRemote: (name: string, remote: string) => void
}

/**
 * A single remote branch row (`origin/feature/x`). Clicking checks the branch
 * out as a new local tracking branch; hovering reveals a single delete-remote
 * action. The remote name (before the first `/`) and short branch name (after
 * it) are derived from the full ref so callers get the same split the backend
 * `DeleteRemoteBranch(name, remote)` expects.
 */
export function RemoteBranchRow({
  branch,
  inFlight,
  disabled,
  onCheckoutRemote,
  onDeleteRemote,
}: RemoteBranchRowProps) {
  const slash = branch.name.indexOf('/')
  const remote = slash === -1 ? '' : branch.name.slice(0, slash)
  const shortName = slash === -1 ? branch.name : branch.name.slice(slash + 1)

  const blocked = disabled || inFlight !== null

  const handleCheckout = () => {
    if (blocked) return
    onCheckoutRemote(branch.name)
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
      tabIndex={blocked ? -1 : 0}
      onClick={handleCheckout}
      onKeyDown={handleKeyDown}
      className={cn(
        'group/item relative flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs transition-colors',
        'text-foreground hover:bg-muted',
        disabled && 'cursor-not-allowed opacity-50',
        !disabled && 'cursor-pointer',
      )}
    >
      <Server className="size-3.5 shrink-0 text-muted-foreground" />
      <span className="flex-1 truncate font-mono">{branch.name}</span>
      {inFlight === 'checkoutRemote' && <Loader2 className="size-3.5 shrink-0 animate-spin" />}

      {/* Hover action overlay — delete remote branch. */}
      <ItemActions>
        <ItemAction
          label={`Delete ${remote}/${shortName} on remote`}
          onClick={() => onDeleteRemote(shortName, remote)}
          disabled={blocked}
        >
          {inFlight === 'deleteRemote' ? (
            <Loader2 className="size-3 animate-spin text-destructive" />
          ) : (
            <Trash2 className="size-3 text-destructive" />
          )}
        </ItemAction>
      </ItemActions>
    </div>
  )
}
