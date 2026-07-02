import { useCallback } from 'react'
import { File, Check } from 'lucide-react'
import { cn } from '@/lib/utils'
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

/** Map git status codes to One Dark theme Tailwind text colors */
function statusColorClass(status: string): string {
  switch (status) {
    case 'M':
      return 'text-warning'
    case 'A':
      return 'text-success'
    case 'R':
      return 'text-info'
    case 'C':
      return 'text-hljs-keyword'
    case 'U':
      return 'text-muted-foreground'
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
  const handleToggle = useCallback(() => {
    onToggle(entry.path)
  }, [entry.path, onToggle])

  const handleDoubleClick = useCallback(() => {
    onOpenDiff(entry.path)
  }, [entry.path, onOpenDiff])

  // Strip workspace root prefix from the absolute path for display
  const displayPath =
    workspaceRoot && entry.path.startsWith(workspaceRoot)
      ? entry.path.slice(workspaceRoot.length).replace(/^\//, '')
      : entry.path

  const { dir, name } = splitPathParts(displayPath)
  const statusCls = statusColorClass(entry.status)

  return (
    <div className="group flex h-7 items-center gap-1.5 px-2 hover:bg-muted/50 cursor-pointer select-none">
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

      {/* File icon */}
      <File className="size-3.5 shrink-0 text-muted-foreground" />

      {/* File name */}
      <span
        className="min-w-0 truncate text-sm leading-none"
        onDoubleClick={handleDoubleClick}
      >
        {dir && (
          <span className="text-muted-foreground/60">{dir}</span>
        )}
        <span>{name}</span>
      </span>

      {/* Status badge */}
      <span
        className={cn(
          'shrink-0 rounded px-1.5 py-px text-[11px] font-semibold leading-none',
          statusCls,
          'bg-muted/60',
        )}
      >
        {entry.status}
      </span>

      {/* Diff stat */}
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
    </div>
  )
}
