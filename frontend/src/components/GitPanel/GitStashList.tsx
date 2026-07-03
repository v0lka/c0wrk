import { Loader2, ArchiveRestore, Trash2 } from 'lucide-react'
import type { StashEntry } from '@/types/models'

interface GitStashListProps {
  entries: StashEntry[]
  isLoading: boolean
  /** Index of the entry currently being popped/dropped (disable others). */
  busyIndex: number | null
  /** Run a pop or drop on the given stash index. */
  onAction: (index: number, op: 'pop' | 'drop') => void
}

/**
 * The stash list dropdown rendered by GitStashButtons (FE-5 / D3).
 * Shows each stash with Pop and Drop actions keyed by index. Rendering is
 * kept here so the button group stays compact; mutation lives in the parent
 * so a single `busyIndex` guards the whole list.
 */
export function GitStashList({ entries, isLoading, busyIndex, onAction }: GitStashListProps) {
  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-4 text-muted-foreground">
        <Loader2 className="size-4 animate-spin" />
      </div>
    )
  }

  if (entries.length === 0) {
    return (
      <div className="px-2 py-4 text-center text-xs text-muted-foreground">No stashes</div>
    )
  }

  return (
    <ul className="max-h-64 overflow-y-auto custom-scrollbar">
      {entries.map((entry) => (
        <li key={entry.index} className="group flex items-center gap-2 rounded px-2 py-1.5 hover:bg-muted/50">
          <span className="flex-1 min-w-0">
            <span className="block truncate text-xs text-foreground">
              {entry.message || 'WIP'}
            </span>
            <span className="block font-mono text-[10px] text-muted-foreground">
              {'stash@{' + entry.index + '}'}
            </span>
          </span>
          <button
            type="button"
            onClick={() => onAction(entry.index, 'pop')}
            disabled={busyIndex !== null}
            title="Pop this stash"
            aria-label={`Pop stash@{${entry.index}}`}
            className="flex items-center justify-center size-6 rounded text-info hover:bg-info/15 disabled:opacity-40"
          >
            {busyIndex === entry.index ? (
              <Loader2 className="size-3.5 animate-spin" />
            ) : (
              <ArchiveRestore className="size-3.5" />
            )}
          </button>
          <button
            type="button"
            onClick={() => onAction(entry.index, 'drop')}
            disabled={busyIndex !== null}
            title="Drop this stash"
            aria-label={`Drop stash@{${entry.index}}`}
            className="flex items-center justify-center size-6 rounded text-destructive hover:bg-destructive/15 disabled:opacity-40"
          >
            <Trash2 className="size-3.5" />
          </button>
        </li>
      ))}
    </ul>
  )
}
