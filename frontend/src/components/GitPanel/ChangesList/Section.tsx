import { useState } from 'react'
import { SectionHeader } from '../ChangesList'
import { FlatSection } from './FlatSection'
import { TreeSection } from './TreeSection'
import type { SortBy, GroupBy } from '@/stores/gitPanelStore'
import type { SectionData } from './types'

// ────────────────────────── Collapsible Section ──────────────────────────────

interface SectionProps {
  section: SectionData
  defaultExpanded?: boolean
  viewMode: 'flat' | 'tree'
  /** Sort criterion forwarded to flat (pre-sorted entries) and tree renderers (D8). */
  sortBy: SortBy
  /** Sub-grouping criterion forwarded to the flat renderer (D8). */
  groupBy: GroupBy
  workspaceRoot: string
  expandedDirs: Set<string>
  onToggleExpandedDir: (dir: string) => void
  onToggleFile: (path: string) => void
  onOpenDiff: (path: string) => void
}

export function Section({
  section,
  defaultExpanded = true,
  viewMode,
  sortBy,
  groupBy,
  workspaceRoot,
  expandedDirs,
  onToggleExpandedDir,
  onToggleFile,
  onOpenDiff,
}: SectionProps) {
  const [expanded, setExpanded] = useState(defaultExpanded)

  if (section.entries.length === 0) return null

  return (
    <div>
      <SectionHeader
        title={section.title}
        count={section.entries.length}
        expanded={expanded}
        onToggle={() => setExpanded((prev) => !prev)}
      />
      {expanded && (
        <div>
          {viewMode === 'flat' ? (
            <FlatSection
              entries={section.entries}
              groupBy={groupBy}
              workspaceRoot={workspaceRoot}
              onToggleFile={onToggleFile}
              onOpenDiff={onOpenDiff}
            />
          ) : (
            <TreeSection
              entries={section.entries}
              sortBy={sortBy}
              workspaceRoot={workspaceRoot}
              expandedDirs={expandedDirs}
              onToggleExpandedDir={onToggleExpandedDir}
              onToggleFile={onToggleFile}
              onOpenDiff={onOpenDiff}
            />
          )}
        </div>
      )}
    </div>
  )
}
