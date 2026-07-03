import { Fragment, useMemo } from 'react'
import { GitFileEntry } from '../GitFileEntry'
import { groupEntries } from '@/lib/gitSortGroup'
import type { GitPanelEntry, GroupBy } from '@/stores/gitPanelStore'

// ───────────────────────────── Flat Section ──────────────────────────────────

interface FlatSectionProps {
  /** Entries for this section — already sorted by the caller. */
  entries: GitPanelEntry[]
  /** Sub-grouping criterion applied within the section (D8). */
  groupBy: GroupBy
  workspaceRoot: string
  onToggleFile: (path: string) => void
  onOpenDiff: (path: string) => void
}

/**
 * Render a directory group label relative to the workspace root. Absolute
 * parent-directory keys (produced by `groupEntries`) are stripped of the
 * workspace prefix for display; the `(root)` sentinel is shown as-is.
 */
function displayDirLabel(label: string, workspaceRoot: string): string {
  if (label === '(root)') return '(root)'
  if (
    workspaceRoot !== '' &&
    (label === workspaceRoot || label.startsWith(workspaceRoot + '/'))
  ) {
    const rel = label.slice(workspaceRoot.length).replace(/^\//, '')
    return rel || '(root)'
  }
  return label
}

/** Compact sub-group header rendered above each group of files. */
function SubGroupHeader({ label }: { label: string }) {
  return (
    <div className="px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground/70 select-none">
      {label}
    </div>
  )
}

export function FlatSection({
  entries,
  groupBy,
  workspaceRoot,
  onToggleFile,
  onOpenDiff,
}: FlatSectionProps) {
  const groups = useMemo(
    () => (groupBy === 'none' ? null : groupEntries(entries, groupBy)),
    [entries, groupBy],
  )

  // No sub-grouping — render entries directly (already sorted).
  if (groups === null) {
    return (
      <>
        {entries.map((entry) => (
          <GitFileEntry
            key={entry.path}
            entry={entry}
            workspaceRoot={workspaceRoot}
            onToggle={onToggleFile}
            onOpenDiff={onOpenDiff}
          />
        ))}
      </>
    )
  }

  // Sub-grouped — render a header above each group of files.
  return (
    <>
      {Array.from(groups, ([label, items]) => (
        <Fragment key={label}>
          <SubGroupHeader
            label={groupBy === 'directory' ? displayDirLabel(label, workspaceRoot) : label}
          />
          {items.map((entry) => (
            <GitFileEntry
              key={entry.path}
              entry={entry}
              workspaceRoot={workspaceRoot}
              onToggle={onToggleFile}
              onOpenDiff={onOpenDiff}
            />
          ))}
        </Fragment>
      ))}
    </>
  )
}
