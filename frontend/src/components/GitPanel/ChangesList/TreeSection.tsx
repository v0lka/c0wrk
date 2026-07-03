import { useMemo } from 'react'
import { TreeRow } from './TreeRow'
import { buildTree } from './buildTree'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

// ───────────────────────────── Tree Section ──────────────────────────────────

interface TreeSectionProps {
  entries: GitPanelEntry[]
  workspaceRoot: string
  expandedDirs: Set<string>
  onToggleExpandedDir: (dir: string) => void
  onToggleFile: (path: string) => void
  onOpenDiff: (path: string) => void
}

export function TreeSection({
  entries,
  workspaceRoot,
  expandedDirs,
  onToggleExpandedDir,
  onToggleFile,
  onOpenDiff,
}: TreeSectionProps) {
  const tree = useMemo(() => buildTree(entries, workspaceRoot), [entries, workspaceRoot])

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
