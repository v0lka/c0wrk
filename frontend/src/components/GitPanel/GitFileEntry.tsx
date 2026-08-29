import { useCallback, useState } from 'react'
import { File, Check, AlertTriangle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { GitFileContextMenu } from './GitFileContextMenu'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

// --- Props ---

interface GitFileEntryProps {
  entry: GitPanelEntry
  /** Optional workspace root path — when provided, strips it for display rendering */
  workspaceRoot?: string
  onToggle: (path: string) => void
  onOpenDiff: (path: string) => void
}

// --- Helpers ---

/** Two-char porcelain status combinations that indicate an unresolved merge conflict. */
const CONFLICT_COMBOS = new Set(['UU', 'AA', 'DD', 'AU', 'UD', 'UA', 'DU'])

/** True when the index/worktree status pair marks an unresolved merge conflict. */
function isMergeConflict(indexStatus: string, worktreeStatus: string): boolean {
  return CONFLICT_COMBOS.has(`${indexStatus}${worktreeStatus}`)
}

/** Map git status codes to One Dark theme Tailwind text colors */
function statusColorClass(status: string): string {
  switch (status) {
    case 'M':
      return 'text-warning'
    case 'A':
      return 'text-success'
    case 'D':
      return 'text-destructive'
    case 'R':
      return 'text-info'
    case 'C':
      return 'text-hljs-keyword'
    case 'U':
      return 'text-destructive'
    default:
      return 'text-muted-foreground'
  }
}

/** Split a git file path into directory (muted) and basename (normal) parts */
function splitPathParts(filePath: string): { dir: string; name: string } {
  const lastSep = filePath.lastIndexOf('/')
  if (lastSep === -1) {
    return { dir: '', name: filePath }
  }
  return {
    dir: filePath.slice(0, lastSep + 1),
    name: filePath.slice(lastSep + 1),
  }
}

// --- Component ---

export function GitFileEntry({ entry, workspaceRoot, onToggle, onOpenDiff }: GitFileEntryProps) {
  const [contextMenuPos, setContextMenuPos] = useState<{ x: number; y: number } | null>(null)

  const handleToggle = useCallback(() => {
    onToggle(entry.path)
  }, [entry.path, onToggle])

  const handleDoubleClick = useCallback(() => {
    onOpenDiff(entry.path)
  }, [entry.path, onOpenDiff])

  const handleContextMenu = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    setContextMenuPos({ x: e.clientX, y: e.clientY })
  }, [])

  const closeContextMenu = useCallback(() => setContextMenuPos(null), [])

  // Strip workspace root prefix from the absolute path for display.
  // Match only at a path-separator boundary to avoid a sibling directory
  // sharing the same prefix (e.g. "/repo" vs "/repo-extra").
  const displayPath =
    workspaceRoot && (entry.path === workspaceRoot || entry.path.startsWith(workspaceRoot + '/'))
      ? entry.path.slice(workspaceRoot.length).replace(/^\//, '')
      : entry.path

  const { dir, name } = splitPathParts(displayPath)
  const statusCls = statusColorClass(entry.status)
  const conflict = isMergeConflict(entry.indexStatus, entry.worktreeStatus)

  return (
    <>
    <div
      className={cn(
        'group flex h-7 items-center gap-1.5 px-2 hover:bg-muted/50 cursor-pointer select-none',
        conflict && 'bg-destructive/10 hover:bg-destructive/15',
      )}
      onDoubleClick={handleDoubleClick}
      onContextMenu={handleContextMenu}
    >
      {/* Checkbox */}
      <label className="relative flex items-center justify-center shrink-0 size-3.5 rounded border border-muted-foreground/40 cursor-pointer transition-colors hover:border-muted-foreground/60 has-checked:border-info has-checked:bg-info">
        <input
          type="checkbox"
          checked={entry.staged}
          onChange={handleToggle}
          className="sr-only"
        />
        {entry.staged && (
          <Check className="size-2.5 text-background pointer-events-none" strokeWidth={3} />
        )}
      </label>

      {/* File / conflict icon */}
      {conflict ? (
        <AlertTriangle
          className="size-3.5 shrink-0 text-destructive"
          aria-label="Merge conflict"
        />
      ) : (
        <File className="size-3.5 shrink-0 text-muted-foreground" />
      )}

      {/* File name — grows to fill remaining space so the diff stat and
          status badge are pushed to the right edge of the row. The native
          title exposes the untruncated path when the name overflows. */}
      <span
        className="min-w-0 flex-1 truncate text-sm leading-none"
        title={displayPath}
      >
        {dir && (
          <span className="text-muted-foreground/60">{dir}</span>
        )}
        <span>{name}</span>
      </span>

      {/* Diff stat — added/deleted line counts (rendered before the badge) */}
      {entry.diffStat && (
        <span className="shrink-0 flex items-center gap-1 text-[11px] leading-none font-mono">
          {entry.diffStat.added > 0 && (
            <span className="text-success">+{entry.diffStat.added}</span>
          )}
          {entry.diffStat.deleted > 0 && (
            <span className="text-destructive">-{entry.diffStat.deleted}</span>
          )}
        </span>
      )}

      {/* Status badge — change-type letter (M/A/D/R/…) */}
      <span
        className={cn(
          'shrink-0 rounded px-1.5 py-px text-[11px] font-semibold leading-none',
          statusCls,
          'bg-muted/60',
        )}
      >
        {entry.status}
      </span>
    </div>
    <GitFileContextMenu
      entry={entry}
      workspaceRoot={workspaceRoot}
      position={contextMenuPos}
      onClose={closeContextMenu}
    />
    </>
  )
}
