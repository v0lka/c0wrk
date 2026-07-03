import { GitFileEntry } from '../GitFileEntry'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

// ───────────────────────────── Flat Section ──────────────────────────────────

interface FlatSectionProps {
  entries: GitPanelEntry[]
  workspaceRoot: string
  onToggleFile: (path: string) => void
  onOpenDiff: (path: string) => void
}

export function FlatSection({ entries, workspaceRoot, onToggleFile, onOpenDiff }: FlatSectionProps) {
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
