import { Server, Check, Loader2, Trash2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { ItemAction, ItemActions } from '@/components/layout/ItemAction'
import type { Branch } from '@/types/models'
import type { BranchActionKind } from '@/hooks/useBranchActions'

interface RemoteBranchRowProps {
  branch: Branch
  /**
   * Visible label override: in tree display mode the leaf renders the last
   * `/`-segment of the ref (the ancestor dir rows carry the rest — the
   * Changes list's name-only convention); undefined (flat mode) renders the
   * full ref. Payloads, hover-action labels, tooltips and the accessible
   * name (aria-label) always stay the FULL remote-tracking ref.
   */
  displayName?: string
  /** The action currently in-flight on this row (null = idle). */
  inFlight: BranchActionKind | null
  /** True when a different row's operation is in-flight — dims & blocks this row. */
  disabled: boolean
  /** Called with the full remote-tracking ref (e.g. `origin/feature/x`). */
  onCheckoutRemote: (remoteBranch: string) => void
  /** Called with the short branch name + remote (e.g. `feature/x`, `origin`). */
  onDeleteRemote: (name: string, remote: string) => void
  /**
   * Precomputed draft-blocking reason for this row's LOCAL twin (the short
   * branch name), or null when the twin is a valid draft target. Computed by
   * BranchPicker with the same predicate as the local rows — the main
   * checkout's branch — so both sections block (and word) identically. Only
   * consulted in workspace-pick mode (see {@link RemoteBranchRowProps.draftSelect}).
   */
  draftBlockedReason?: string | null
  /**
   * Workspace-pick mode (ADR-091): when set, clicking SELECTS the remote
   * branch for the fresh session's managed worktree instead of checking it
   * out — no git operation runs at pick time. The pick records a
   * create-branch intent (local short name from the remote-tracking start
   * point), so the first send's provisioning follows git DWIM semantics: an
   * existing local branch of that name is reused, a missing one is created
   * from the remote ref (SendMessage's workspace argument). The per-row hover
   * actions are hidden — the row is a selection target, not a management
   * one. A row whose LOCAL twin (the short name) is the working tree's own
   * branch is disabled, mirroring {@link LocalBranchRow}'s current-branch
   * guard: the DWIM reuse would silently land the session in the shared
   * checkout instead of a managed worktree.
   */
  draftSelect?: {
    selected: boolean
    onSelect: (remoteBranch: string) => void
  }
  /**
   * Managed-focus gate (ADR-080): a non-null reason means the Git panel's
   * focus sits on a managed session tree that refuses every off-pin branch
   * switch — checking a remote branch out creates a local branch and moves
   * the tree off its pin (ErrPinnedWorktreeBranch), so the click is a no-op,
   * the row dims and the reason is its tooltip. The delete-remote hover
   * action stays live: the backend allows it under a managed focus. The row
   * stays in the tab order so keyboard users can reach the reason. Manage
   * mode only (useSessionWorktreePin's branchSwitchBlockedReason); draft
   * mode never checks anything out.
   */
  checkoutBlockedReason?: string | null
}

/**
 * A single remote branch row (`origin/feature/x`). Clicking checks the branch
 * out as a new local tracking branch; hovering reveals a single delete-remote
 * action. The remote name (before the first `/`) and short branch name (after
 * it) are derived from the full ref so callers get the same split the backend
 * `DeleteRemoteBranch(name, remote)` expects.
 *
 * With `draftSelect` set, the row is a pure selection target for the chat
 * draft's managed-worktree branch — see {@link RemoteBranchRowProps.draftSelect}.
 */
export function RemoteBranchRow({
  branch,
  displayName,
  inFlight,
  disabled,
  onCheckoutRemote,
  onDeleteRemote,
  draftBlockedReason,
  draftSelect,
  checkoutBlockedReason,
}: RemoteBranchRowProps) {
  const slash = branch.name.indexOf('/')
  const remote = slash === -1 ? '' : branch.name.slice(0, slash)
  const shortName = slash === -1 ? branch.name : branch.name.slice(slash + 1)

  const blocked = disabled || inFlight !== null
  const selected = draftSelect?.selected ?? false
  // Draft mode: a remote ref whose LOCAL twin (the short name) cannot back a
  // managed worktree — commit-time DWIM would reuse that branch and the main
  // checkout would serve the session instead. The BLOCKING REASON is computed
  // by BranchPicker with the same predicate as the local rows (the main
  // checkout's branch — focus-independent) and handed down verbatim, so a
  // branch is blocked (and worded) identically in both sections. A null
  // reason (unknown main checkout, or a free branch) blocks nothing.
  const draftBlocked = draftSelect !== undefined && !!draftBlockedReason
  // Managed-focus gate: manage mode only — the row's checkout (a branch
  // switch onto a brand-new local branch) is refused while the panel's
  // focus sits on a managed session tree.
  const checkoutBlocked = draftSelect === undefined && (checkoutBlockedReason ?? '') !== ''

  const handleCheckout = () => {
    if (blocked || checkoutBlocked) return
    if (draftSelect) {
      if (!draftBlocked) draftSelect.onSelect(branch.name)
      return
    }
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
      // The checkout-gated row stays in the tab order despite the no-op:
      // the tooltip with the reason must stay keyboard-reachable, and
      // aria-disabled announces the inert primary action.
      tabIndex={blocked || draftBlocked ? -1 : 0}
      aria-disabled={checkoutBlocked || draftBlocked ? 'true' : undefined}
      aria-current={selected ? 'true' : undefined}
      // The accessible name is always the FULL remote-tracking ref — the
      // visible label is the tree-mode basename, and two dirs can hold
      // same-named leaves (`origin/ui/x` and `origin/cli/x`).
      aria-label={branch.name}
      title={
        draftBlocked
          ? (draftBlockedReason ?? undefined)
          : checkoutBlocked
            ? (checkoutBlockedReason ?? undefined)
            : undefined
      }
      onClick={handleCheckout}
      onKeyDown={handleKeyDown}
      data-testid={draftSelect ? 'draft-remote-branch-row' : undefined}
      className={cn(
        'group/item relative flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs transition-colors',
        selected ? 'bg-primary/10 text-primary' : 'text-foreground hover:bg-muted',
        (disabled || draftBlocked || checkoutBlocked) && 'cursor-not-allowed opacity-50',
        !disabled && !draftBlocked && !checkoutBlocked && 'cursor-pointer',
      )}
    >
      <Server className="size-4 shrink-0 text-muted-foreground" />
      <span className="flex-1 truncate">{displayName ?? branch.name}</span>
      {selected && <Check className="size-4 shrink-0" />}
      {inFlight === 'checkoutRemote' && <Loader2 className="size-4 shrink-0 animate-spin" />}

      {/* Hover action overlay — delete remote branch. Hidden in draft mode:
          the row is a selection target, not a management one. */}
      {draftSelect ? null : (
        <ItemActions>
          <ItemAction
            label={`Delete ${remote}/${shortName} on remote`}
            onClick={() => onDeleteRemote(shortName, remote)}
            disabled={blocked}
            disabledReason={blocked ? 'A git operation is in progress' : undefined}
          >
            {inFlight === 'deleteRemote' ? (
              <Loader2 className="size-3.5 animate-spin text-destructive" />
            ) : (
              <Trash2 className="size-3.5 text-destructive" />
            )}
          </ItemAction>
        </ItemActions>
      )}
    </div>
  )
}
