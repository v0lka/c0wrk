import { useMemo } from 'react'
import { ChevronDown, ChevronRight, File, Loader2 } from 'lucide-react'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useProjectStore } from '@/stores/projectStore'
import { sortEntries } from '@/lib/gitSortGroup'
import { Section } from './ChangesList/Section'
import { SortGroupControls } from './ChangesList/SortGroupControls'
import type { SectionData } from './ChangesList/types'

// ─────────────────────────────────── Types ───────────────────────────────────

interface ChangesListProps {
  onToggleFile: (path: string) => void
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

  // Group entries into 3 structural sections and sort each section by the
  // selected criterion. The structural split (Staged / Changes / Untracked)
  // is always preserved — `sortBy` only reorders entries *within* each section.
  //
  // Untracked files are identified by the precise porcelain field
  // `worktreeStatus === '?'` (the backend sets WorkTreeStatus "?" and
  // Status "A" for untracked files — see core/workspace/git.go). The
  // legacy `status === 'U'` check was wrong: "U" means *unmerged* (merge
  // conflict), not untracked. All conflict combos carry a non-empty index
  // status (hence `staged: true`), so the `!e.staged` guard routes them to
  // "Staged Changes" and out of "Untracked Files".
  const sections = useMemo<SectionData[]>(() => {
    const staged = sortEntries(entries.filter((e) => e.staged), sortBy)
    const unstaged = sortEntries(
      entries.filter((e) => !e.staged && e.worktreeStatus !== '?'),
      sortBy,
    )
    const untracked = sortEntries(
      entries.filter((e) => !e.staged && e.worktreeStatus === '?'),
      sortBy,
    )

    return [
      { key: 'staged', title: 'Staged Changes', entries: staged },
      { key: 'unstaged', title: 'Changes', entries: unstaged },
      { key: 'untracked', title: 'Untracked Files', entries: untracked },
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
      <SortGroupControls viewMode={viewMode} />

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
