import { Folder, ChevronDown, ChevronRight } from 'lucide-react'
import { GitFileEntry } from '../GitFileEntry'
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
    const indentPx = depth * 12

    return (
      <>
        {/* Keyboard twin of the click toggle: a plain div carries
            role="button" + tabIndex so the row stays reachable (no owning
            role="tree" — treeitem would be orphaned ARIA). */}
        <div
          className="flex h-7 cursor-pointer select-none items-center gap-1.5 px-2 hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors"
          style={{ paddingLeft: `${indentPx + 8}px` }}
          onClick={() => onToggleExpandedDir(node.fullPath)}
          role="button"
          tabIndex={0}
          aria-expanded={isExpanded}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault()
              onToggleExpandedDir(node.fullPath)
            }
          }}
        >
          {isExpanded ? (
            <ChevronDown className="size-3.5 shrink-0" />
          ) : (
            <ChevronRight className="size-3.5 shrink-0" />
          )}
          <Folder className="size-3.5 shrink-0" />
          <span className="min-w-0 truncate text-sm leading-none">
            {node.name}
          </span>
        </div>
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
