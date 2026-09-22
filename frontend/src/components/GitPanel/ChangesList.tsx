import { useMemo } from 'react'
import { ChevronDown, ChevronRight, File, Loader2 } from 'lucide-react'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useProjectStore } from '@/stores/projectStore'
import { sortEntries } from '@/lib/gitSortGroup'
import { Section } from './ChangesList/Section'
import { SortGroupControls } from './ChangesList/SortGroupControls'
import { TreeExpandControls } from './ChangesList/TreeExpandControls'
import { ChangesToolbar } from './ChangesToolbar'
import { classifyEntries } from '@/lib/gitStatus'
import type { StageToggleHandler } from '@/lib/gitStatus'
import type { SectionData } from './ChangesList/types'

// ─────────────────────────────────── Types ───────────────────────────────────

interface ChangesListProps {
  onToggleFile: StageToggleHandler
  onOpenDiff: (path: string) => void
}

// ─────────────────────── Collapsible Section Header ──────────────────────────

interface SectionHeaderProps {
  title: string
  count: number
  expanded: boolean
  onToggle: () => void
}

export function SectionHeader({ title, count, expanded, onToggle }: SectionHeaderProps) {
  return (
    <button
      type="button"
      onClick={onToggle}
      className="flex w-full items-center gap-1.5 px-2 py-1 text-xs font-semibold text-muted-foreground hover:text-foreground transition-colors select-none"
    >
      <span className="inline-flex">
        {expanded ? (
          <ChevronDown className="size-3.5" />
        ) : (
          <ChevronRight className="size-3.5" />
        )}
      </span>
      <span>{title}</span>
      <span className="ml-auto rounded-full bg-muted px-1.5 py-px text-[10px] tabular-nums">
        {count}
      </span>
    </button>
  )
}

// ────────────────────────────── Main Component ───────────────────────────────

export function ChangesList({ onToggleFile, onOpenDiff }: ChangesListProps) {
  const entries = useGitPanelStore((s) => s.entries)
  const viewMode = useGitPanelStore((s) => s.viewMode)
  const sortBy = useGitPanelStore((s) => s.sortBy)
  const groupBy = useGitPanelStore((s) => s.groupBy)
  const expandedDirs = useGitPanelStore((s) => s.expandedDirs)
  const isLoading = useGitPanelStore((s) => s.isLoading)
  const toggleExpandedDir = useGitPanelStore((s) => s.toggleExpandedDir)

  // Resolve workspace root for relative path display
  const workspaceRoot = useProjectStore((s) => {
    const activeProjectId = s.activeProjectId
    if (!activeProjectId || !s.projects) return ''
    return s.projects.find((p) => p.id === activeProjectId)?.workspace_path ?? ''
  })

  // Split entries into the 3 structural sections along their porcelain axis
  // (see `classifyEntries`) and sort each section by the selected criterion.
  // The split is by-axis, not exclusive: a path modified on both axes (`MM`)
  // appears in Staged Changes AND Changes. The structural split is always
  // preserved — `sortBy` only reorders entries *within* each section.
  //
  // Each section carries the axis its rows act on (`side`): Staged Changes →
  // 'index' (checkbox checked, "Unstage"), Changes / Untracked Files →
  // 'worktree' (checkbox unchecked, "Stage").
  const sections = useMemo<SectionData[]>(() => {
    const { staged, unstaged, untracked } = classifyEntries(entries)

    return [
      {
        key: 'staged',
        title: 'Staged Changes',
        side: 'index',
        entries: sortEntries(staged, sortBy),
      },
      {
        key: 'unstaged',
        title: 'Changes',
        side: 'worktree',
        entries: sortEntries(unstaged, sortBy),
      },
      {
        key: 'untracked',
        title: 'Untracked Files',
        side: 'worktree',
        entries: sortEntries(untracked, sortBy),
      },
    ]
  }, [entries, sortBy])

  // Section defaults: "Staged Changes" and "Changes" open by default;
  // "Untracked Files" collapsed by default.
  const sectionDefaults: Record<string, boolean> = {
    staged: true,
    unstaged: true,
    untracked: false,
  }

  return (
    <div className="flex flex-col flex-1 min-h-0">
      <ChangesToolbar />
      {viewMode === 'flat' ? (
        <SortGroupControls />
      ) : (
        <TreeExpandControls workspaceRoot={workspaceRoot} />
      )}

      {isLoading && entries.length === 0 ? (
        // ── Loading state ──
        <div className="flex flex-1 items-center justify-center min-h-0">
          <Loader2 className="size-5 animate-spin text-muted-foreground" />
        </div>
      ) : entries.length === 0 ? (
        // ── Empty state ──
        <div className="flex flex-1 items-center justify-center min-h-0">
          <div className="flex flex-col items-center gap-2 text-muted-foreground">
            <File className="size-6 opacity-40" />
            <span className="text-sm">No changes</span>
            <span className="text-xs opacity-60">
              Working tree is clean
            </span>
          </div>
        </div>
      ) : (
        // ── Sections ──
        <div className="custom-scrollbar flex-1 overflow-y-auto min-h-0" role="list">
          {sections.map((section) => (
            <Section
              key={section.key}
              section={section}
              defaultExpanded={sectionDefaults[section.key] ?? true}
              viewMode={viewMode}
              sortBy={sortBy}
              groupBy={groupBy}
              workspaceRoot={workspaceRoot}
              expandedDirs={expandedDirs}
              onToggleExpandedDir={toggleExpandedDir}
              onToggleFile={onToggleFile}
              onOpenDiff={onOpenDiff}
            />
          ))}
        </div>
      )}
    </div>
  )
}
