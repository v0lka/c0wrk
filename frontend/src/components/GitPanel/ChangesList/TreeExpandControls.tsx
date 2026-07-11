import { useMemo } from 'react'
import { ChevronsDown, ChevronsUp } from 'lucide-react'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { collectAllDirPaths } from './buildTree'

// ──────────────────────── Tree Expand / Collapse All ─────────────────────────

interface TreeExpandControlsProps {
  /** Workspace root — needed to compute display-relative directory paths. */
  workspaceRoot: string
}

const triggerClass =
  'flex items-center gap-1 px-1.5 py-0.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted transition-colors focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-40 disabled:pointer-events-none'

/**
 * Expand-all / collapse-all controls for the tree view of the Changes list.
 *
 * Replaces `SortGroupControls` when `viewMode === 'tree'`. The sort criterion
 * still applies to leaf nodes (set in flat view), but the tree structure
 * already groups by directory, so sort/group selectors are not shown here.
 *
 * "Expand All" collects every directory path from the current entries (using
 * the same display-relative path semantics as `buildTree`) and replaces the
 * `expandedDirs` set in one atomic update. "Collapse All" clears the set.
 */
export function TreeExpandControls({ workspaceRoot }: TreeExpandControlsProps) {
  const entries = useGitPanelStore((s) => s.entries)
  const setExpandedDirs = useGitPanelStore((s) => s.setExpandedDirs)

  const allDirPaths = useMemo(
    () => collectAllDirPaths(entries, workspaceRoot),
    [entries, workspaceRoot],
  )

  const hasDirs = allDirPaths.length > 0

  return (
    <div className="flex items-center gap-1 px-2 py-1 shrink-0 border-b border-border bg-secondary/20 text-xs">
      <button
        type="button"
        className={triggerClass}
        disabled={!hasDirs}
        onClick={() => setExpandedDirs(new Set(allDirPaths))}
        aria-label="Expand all directories"
        title="Expand all"
      >
        <ChevronsDown className="size-3" />
        <span>Expand All</span>
      </button>
      <button
        type="button"
        className={triggerClass}
        disabled={!hasDirs}
        onClick={() => setExpandedDirs(new Set())}
        aria-label="Collapse all directories"
        title="Collapse all"
      >
        <ChevronsUp className="size-3" />
        <span>Collapse All</span>
      </button>
    </div>
  )
}
