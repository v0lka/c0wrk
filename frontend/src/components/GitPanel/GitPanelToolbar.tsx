import { GitBranch, ChevronDown } from 'lucide-react'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import type { BranchInfo } from '@/types/models'

interface GitPanelToolbarProps {
  branch: BranchInfo
}

/**
 * Top toolbar of the Git panel — shows only the branch indicator, which spans
 * the full panel width. Clicking it opens the `BranchPicker`.
 *
 * Bulk change operations (stage/unstage all, stash, abort merge/rebase) and
 * the flat/tree view-mode toggle used to live here but were moved to
 * `ChangesToolbar`, rendered above the sort/group controls inside the
 * "Changes" section. This keeps the branch indicator visible across all tabs
 * (changes / history / graph) and lets it occupy the full width.
 */
export function GitPanelToolbar({ branch }: GitPanelToolbarProps) {
  const openBranchPicker = useGitPanelStore((s) => s.openBranchPicker)

  return (
    <div className="flex items-center px-2 py-1 min-h-[32px] shrink-0 border-b border-border bg-secondary/30">
      {/* Branch indicator — click to open the branch picker; spans full width */}
      <button
        type="button"
        onClick={openBranchPicker}
        title="Switch or create branch"
        className="flex items-center gap-1.5 w-full px-1.5 py-0.5 text-xs text-muted-foreground min-w-0 rounded-md hover:bg-muted transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
      >
        <GitBranch className="size-3.5 shrink-0" />
        <span className="truncate font-mono text-[11px]">
          {branch.name || <span className="italic opacity-50">no branch</span>}
        </span>
        {(branch.ahead > 0 || branch.behind > 0) && (
          <span className="shrink-0 font-mono text-[10px] text-muted-foreground/70">
            ↑{branch.ahead} ↓{branch.behind}
          </span>
        )}
        <ChevronDown className="size-3 shrink-0 opacity-60 ml-auto" />
      </button>
    </div>
  )
}
