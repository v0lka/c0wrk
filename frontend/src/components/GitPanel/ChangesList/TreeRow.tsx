import { GitFileEntry } from '../GitFileEntry'
import { CollapsibleDirRow } from '../CollapsibleDirRow'
import type { StageSide, StageToggleHandler } from '@/lib/gitStatus'
import type { TreeNode } from './types'

// ─────────────────────────── Tree View Renderer ──────────────────────────────

interface TreeRowProps {
  node: TreeNode
  depth: number
  /** Porcelain axis this tree's rows act on (index | worktree). */
  side: StageSide
  /** Root the display paths are relative to (focus root, else workspace). */
  displayRoot: string
  expandedDirs: Set<string>
  onToggleExpandedDir: (dir: string) => void
  onToggleFile: StageToggleHandler
  onOpenDiff: (path: string) => void
}

export function TreeRow({
  node,
  depth,
  side,
  displayRoot,
  expandedDirs,
  onToggleExpandedDir,
  onToggleFile,
  onOpenDiff,
}: TreeRowProps) {
  // ── Directory node ──
  if (node.isDir) {
    const isExpanded = expandedDirs.has(node.fullPath)

    return (
      <>
        <CollapsibleDirRow
          name={node.name}
          expanded={isExpanded}
          onToggle={() => onToggleExpandedDir(node.fullPath)}
          depth={depth}
        />
        {isExpanded &&
          node.children.map((child) => (
            <TreeRow
              key={child.isDir ? child.fullPath : child.entry.path}
              node={child}
              depth={depth + 1}
              side={side}
              displayRoot={displayRoot}
              expandedDirs={expandedDirs}
              onToggleExpandedDir={onToggleExpandedDir}
              onToggleFile={onToggleFile}
              onOpenDiff={onOpenDiff}
            />
          ))}
      </>
    )
  }

  // ── File node ──
  const indentPx = (depth + 1) * 12
  return (
    <div style={{ paddingLeft: `${indentPx}px` }}>
      <GitFileEntry
        entry={node.entry}
        side={side}
        displayRoot={displayRoot}
        pathDisplay="name-only"
        onToggle={onToggleFile}
        onOpenDiff={onOpenDiff}
      />
    </div>
  )
}
