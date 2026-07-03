import { useMemo } from 'react'
import { TreeRow } from './TreeRow'
import { buildTree } from './buildTree'
import type { GitPanelEntry, SortBy } from '@/stores/gitPanelStore'

// ───────────────────────────── Tree Section ──────────────────────────────────

interface TreeSectionProps {
  entries: GitPanelEntry[]
  /** Sort criterion applied to leaf (file) nodes within the tree (D8). */
  sortBy: SortBy
  workspaceRoot: string
  expandedDirs: Set<string>
  onToggleExpandedDir: (dir: string) => void
  onToggleFile: (path: string) => void
  onOpenDiff: (path: string) => void
}

export function TreeSection({
  entries,
  sortBy,
  workspaceRoot,
  expandedDirs,
  onToggleExpandedDir,
  onToggleFile,
  onOpenDiff,
}: TreeSectionProps) {
  const tree = useMemo(
    () => buildTree(entries, workspaceRoot, sortBy),
    [entries, workspaceRoot, sortBy],
  )

  return (
    <>
      {tree.map((node) => (
        <TreeRow
          key={node.isDir ? node.fullPath : node.entry.path}
          node={node}
          depth={0}
          workspaceRoot={workspaceRoot}
          expandedDirs={expandedDirs}
          onToggleExpandedDir={onToggleExpandedDir}
          onToggleFile={onToggleFile}
          onOpenDiff={onOpenDiff}
        />
      ))}
    </>
  )
}
