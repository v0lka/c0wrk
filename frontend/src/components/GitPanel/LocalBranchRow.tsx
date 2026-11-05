import { GitBranch, Check, Loader2, GitMerge, GitFork, Lock, Pencil, Trash2, Upload } from 'lucide-react'
import { cn } from '@/lib/utils'
import { ItemAction, ItemActions } from '@/components/layout/ItemAction'
import type { Branch } from '@/types/models'
import type { BranchActionKind } from '@/hooks/useBranchActions'

interface LocalBranchRowProps {
  branch: Branch
  /**
   * Visible label override: in tree display mode the leaf renders the last
   * `/`-segment of the name (the ancestor dir rows carry the rest — the
   * Changes list's name-only convention); undefined (flat mode) renders the
   * full name. Payloads, hover-action labels, tooltips and the accessible
   * name (aria-label) always stay the FULL branch name.
   */
  displayName?: string
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
  /**
   * Session-draft mode (ADR-080): when set, clicking SELECTS the branch for
   * the new session's managed worktree instead of checking it out — no git
   * operation runs, and the per-branch hover actions are hidden.
   */
  draftSelect?: {
    selected: boolean
    onSelect: (name: string) => void
  }
  /**
   * Draft-mode block reason: when set (together with `draftSelect`), the
   * branch cannot back the new session's worktree and a click is a no-op;
   * the text doubles as the row tooltip. The picker passes it ONLY for the
   * branch checked out in the MAIN checkout — git allows a branch in at
   * most one worktree. Branches held by existing managed worktrees stay
   * selectable (the choice's first send adopts the existing tree,
   * ADR-082) even
   * when they are `is_current` — that flag follows the Git panel's focus
   * (GetBranches runs in the focused tree), not the main checkout.
   */
  draftBlockedReason?: string | null
  /**
   * Managed-session pin (ADR-080): the branch is the ACTIVE session's pinned
   * branch. The backend refuses branch switches off a managed session tree
   * (ErrPinnedWorktreeBranch) and the binding is immutable, so the row shows
   * a LOCK instead of the current-branch checkmark, offers no delete action,
   * renders Rename disabled with a pinned reason, and clicking is a no-op —
   * the pin is communicated up front instead of being discovered through
   * clicks that fail server-side. Consumers pass it in manage mode only
   * (never in draft mode, where no session pin exists).
   */
  pinned?: boolean
  /**
   * Managed-focus gate (ADR-080): a non-null reason means the Git panel's
   * focus sits on a managed session tree that refuses every off-pin branch
   * switch (ErrPinnedWorktreeBranch) — the checkout click is a no-op, the
   * row dims and the reason is its tooltip, so the refusal is communicated
   * up front rather than discovered through a failing server call. The
   * row's hover actions (push/merge/rebase/rename/delete) stay live: the
   * backend allows them under a managed focus. The row deliberately stays
   * in the tab order so keyboard users can reach the reason. Consumers pass
   * it in manage mode only (from useSessionWorktreePin's
   * branchSwitchBlockedReason); draft mode never checks anything out.
   */
  checkoutBlockedReason?: string | null
}

/**
 * A single local branch row. Clicking checks out the branch; hovering reveals
 * the per-branch actions (push / merge / rebase / rename / delete) via the
 * shared ItemAction overlay. Merge & rebase are hidden for the current branch
 * (self-merge/rebase is rejected by git), and delete is disabled there too —
 * the backend refuses to delete the checked-out branch.
 *
 * With `draftSelect` set, the row is a pure selection target for the chat
 * draft's managed-worktree branch — see {@link LocalBranchRowProps.draftSelect}.
 * With `pinned` set (manage mode, active managed-worktree session), the row
 * is the session's pinned branch: a lock instead of the checkmark, no delete
 * action, a disabled Rename, and a no-op click — see
 * {@link LocalBranchRowProps.pinned}. With `checkoutBlockedReason` set
 * (manage mode, panel focus on a managed session tree), the checkout is a
 * dimmed no-op with the reason as its tooltip — see
 * {@link LocalBranchRowProps.checkoutBlockedReason}.
 */
export function LocalBranchRow({
  branch,
  displayName,
  inFlight,
  disabled,
  onCheckout,
  onRename,
  onMerge,
  onRebase,
  onPush,
  onDelete,
  draftSelect,
  draftBlockedReason,
  pinned,
  checkoutBlockedReason,
}: LocalBranchRowProps) {
  const isCurrent = branch.is_current
  // The pin is a manage-mode concept; draft selection never carries one.
  const isPinned = !draftSelect && (pinned ?? false)
  // A row is blocked when any operation is running on it OR elsewhere.
  const blocked = disabled || inFlight !== null
  // Workspace-pick mode: blocked only when the picker says so — the MAIN
  // checkout's branch (git enforces one worktree per branch). `is_current`
  // deliberately does not block here: it follows the Git panel's focus, and
  // a branch held by an existing managed worktree is adopted by the first
  // send (ADR-082).
  const draftBlocked = draftSelect !== undefined && !!draftBlockedReason
  // Managed-focus gate: manage mode only — the row's checkout is refused
  // while the panel's focus sits on a managed session tree.
  const checkoutBlocked = !draftSelect && (checkoutBlockedReason ?? '') !== ''

  const handleCheckout = () => {
    if (draftSelect) {
      if (blocked || draftBlocked) return
      draftSelect.onSelect(branch.name)
      return
    }
    if (isCurrent || isPinned || checkoutBlocked || blocked) return
    onCheckout(branch.name)
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      handleCheckout()
    }
  }

  const selected = draftSelect?.selected ?? false

  return (
    <div
      role="button"
      // The pinned and checkout-gated rows stay in the tab order despite
      // being no-ops: the tooltip with the reason must stay keyboard-
      // reachable, and aria-disabled announces the inert primary action.
      tabIndex={(draftSelect ? blocked || draftBlocked : (isCurrent && !isPinned) || blocked) ? -1 : 0}
      aria-disabled={isPinned || checkoutBlocked || draftBlocked ? 'true' : undefined}
      aria-current={isCurrent || selected || isPinned ? 'true' : undefined}
      // The accessible name is always the FULL branch name — the visible
      // label is the tree-mode basename, and two dirs can hold same-named
      // leaves (`origin/ui/x` and `origin/cli/x`).
      aria-label={branch.name}
      title={
        draftBlocked
          ? (draftBlockedReason ?? undefined)
          : isPinned
            ? 'Pinned: this branch is held by this session\'s managed worktree — the Git panel follows that tree; switch the focus in the Worktrees section to work on other branches'
            : checkoutBlocked
              ? (checkoutBlockedReason ?? undefined)
              : undefined
      }
      onClick={handleCheckout}
      onKeyDown={handleKeyDown}
      data-testid={draftSelect ? 'draft-branch-row' : undefined}
      className={cn(
        'group/item relative flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs transition-colors',
        isCurrent || selected || isPinned ? 'bg-primary/10 text-primary' : 'text-foreground hover:bg-muted',
        (disabled || draftBlocked || checkoutBlocked) && 'cursor-not-allowed opacity-50',
        (draftSelect ? !blocked && !draftBlocked : !disabled && !isCurrent && !isPinned && !checkoutBlocked) && 'cursor-pointer',
      )}
    >
      <GitBranch className="size-4 shrink-0 text-muted-foreground" />
      <span className="flex-1 truncate">{displayName ?? branch.name}</span>
      {(isCurrent || selected) && !isPinned && <Check className="size-4 shrink-0" />}
      {isPinned && <Lock className="size-4 shrink-0" aria-label="Pinned to this session" />}
      {inFlight === 'checkout' && (
        <Loader2 className="size-4 shrink-0 animate-spin text-muted-foreground" />
      )}

      {/* Hover action overlay — push/merge/rebase/rename/delete. Hidden in
          draft mode: the row is a selection target, not a management one. */}
      {draftSelect ? null : (
      <ItemActions>
        <ItemAction
          label={`Push ${branch.name}`}
          onClick={() => onPush(branch.name)}
          disabled={blocked}
          disabledReason={blocked ? 'A git operation is in progress' : undefined}
        >
          {inFlight === 'push' ? (
            <Loader2 className="size-3.5 animate-spin text-foreground" />
          ) : (
            <Upload className="size-3.5 text-primary" />
          )}
        </ItemAction>

        {!isCurrent && (
          <ItemAction
            label={`Merge ${branch.name} into current`}
            onClick={() => onMerge(branch.name)}
            disabled={blocked}
            disabledReason={blocked ? 'A git operation is in progress' : undefined}
          >
            {inFlight === 'merge' ? (
              <Loader2 className="size-3.5 animate-spin text-info" />
            ) : (
              <GitMerge className="size-3.5 text-info" />
            )}
          </ItemAction>
        )}

        {!isCurrent && (
          <ItemAction
            label={`Rebase current onto ${branch.name}`}
            onClick={() => onRebase(branch.name)}
            disabled={blocked}
            disabledReason={blocked ? 'A git operation is in progress' : undefined}
          >
            {inFlight === 'rebase' ? (
              <Loader2 className="size-3.5 animate-spin text-warning" />
            ) : (
              <GitFork className="size-3.5 text-warning" />
            )}
          </ItemAction>
        )}

        <ItemAction
          label="Rename"
          onClick={() => onRename(branch.name)}
          disabled={blocked || isPinned}
          disabledReason={
            isPinned
              ? 'Pinned to this session — renaming is disabled'
              : blocked
                ? 'A git operation is in progress'
                : undefined
          }
        >
          {inFlight === 'rename' ? (
            <Loader2 className="size-3.5 animate-spin text-info" />
          ) : (
            <Pencil className="size-3.5 text-info" />
          )}
        </ItemAction>

        {/* The session-pinned branch offers no delete: the backend refuses it
            for a branch checked out in the session's managed worktree, and
            deleting the active session's own branch is never a valid move. */}
        {!isPinned && (
        <ItemAction
          label={isCurrent ? 'Delete branch' : `Delete ${branch.name}`}
          onClick={() => onDelete(branch.name)}
          disabled={blocked || isCurrent}
          disabledReason={
            isCurrent ? 'Cannot delete the current branch' : blocked ? 'A git operation is in progress' : undefined
          }
        >
          {inFlight === 'delete' ? (
            <Loader2 className="size-3.5 animate-spin text-destructive" />
          ) : (
            <Trash2 className="size-3.5 text-destructive" />
          )}
        </ItemAction>
        )}
      </ItemActions>
      )}
    </div>
  )
}
