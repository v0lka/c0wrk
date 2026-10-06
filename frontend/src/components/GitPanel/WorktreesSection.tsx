import { Check, FolderGit2, GitBranch, Link2, Loader2, Lock, Pin } from 'lucide-react'
import { DropdownMenuItem } from '@/components/ui/dropdown-menu'
import type { GitWorktree } from '@/types/models'

/**
 * The worktree focus-switcher list, shared by the BranchDropdown combo and
 * the BranchPicker (switch-branch dialog). Every worktree of the active
 * project is listed — the local checkout, the app-managed session trees
 * (with their owning session and a pin marker: branch checkout is refused
 * there), and external linked trees. Selecting an entry switches the Git
 * panel's FOCUS to that tree (SetGitPanelFocus) — it never checks anything
 * out, never retargets a session's execution workspace.
 *
 * `variant="menu"` renders Radix dropdown items (BranchDropdown);
 * `variant="list"` renders plain rows (BranchPicker).
 */
export function WorktreesSection({
  worktrees,
  onSelect,
  busyPath = null,
  variant = 'list',
}: {
  worktrees: GitWorktree[]
  onSelect: (worktree: GitWorktree) => void
  /** Path of a worktree whose focus switch is in flight (spinner row). */
  busyPath?: string | null
  variant?: 'menu' | 'list'
}) {
  if (worktrees.length === 0) return null

  return (
    <>
      {worktrees.map((w) => {
        const busy = busyPath === w.path
        const content = (
          <>
            <WorktreeKindIcon worktree={w} />
            <span className="min-w-0 flex-1 truncate">
              <span className="truncate font-medium">
                {w.kind === 'main' ? 'Local checkout' : w.name}
              </span>
              <span className="ml-1.5 truncate font-mono text-[10px] text-muted-foreground">
                {w.branch || 'detached'}
              </span>
              {w.managed && w.session_name && (
                <span className="ml-1.5 truncate text-[10px] text-muted-foreground/80">
                  · {w.session_name}
                </span>
              )}
            </span>
            {w.pinned && (
              <Pin
                className="size-3 shrink-0 text-muted-foreground/70"
                aria-label="Pinned branch"
              />
            )}
            {w.locked && <Lock className="size-3 shrink-0 text-muted-foreground/70" aria-label="Locked" />}
            {busy ? (
              <Loader2 className="size-3.5 shrink-0 animate-spin text-muted-foreground" />
            ) : (
              w.is_focus && <Check className="size-3.5 shrink-0 text-primary" aria-label="Focused" />
            )}
          </>
        )
        if (variant === 'menu') {
          return (
            <DropdownMenuItem
              key={w.path}
              data-testid="worktree-row"
              data-path={w.path}
              disabled={busy}
              onSelect={() => onSelect(w)}
            >
              {content}
            </DropdownMenuItem>
          )
        }
        return (
          <button
            key={w.path}
            type="button"
            data-testid="worktree-row"
            data-path={w.path}
            disabled={busy}
            onClick={() => onSelect(w)}
            title={`Focus ${w.kind === 'main' ? 'the local checkout' : `worktree ${w.name}`}${w.branch ? ` (${w.branch})` : ''}`}
            className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-xs transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:opacity-50"
          >
            {content}
          </button>
        )
      })}
    </>
  )
}

/** Kind icon: folder for the checkout, branch for managed trees, link for external. */
function WorktreeKindIcon({ worktree }: { worktree: GitWorktree }) {
  if (worktree.kind === 'main') {
    return <FolderGit2 className="size-4 shrink-0 text-muted-foreground" />
  }
  if (worktree.kind === 'managed') {
    return <GitBranch className="size-4 shrink-0 text-muted-foreground" />
  }
  return <Link2 className="size-4 shrink-0 text-muted-foreground" />
}
