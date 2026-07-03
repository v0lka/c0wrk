import { useState } from 'react'
import { SectionHeader } from '../ChangesList'
import { FlatSection } from './FlatSection'
import { TreeSection } from './TreeSection'
import type { SectionData } from './types'

// ────────────────────────── Collapsible Section ──────────────────────────────

interface SectionProps {
  section: SectionData
  defaultExpanded?: boolean
  viewMode: 'flat' | 'tree'
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
              workspaceRoot={workspaceRoot}
              onToggleFile={onToggleFile}
              onOpenDiff={onOpenDiff}
            />
          ) : (
            <TreeSection
              entries={section.entries}
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
