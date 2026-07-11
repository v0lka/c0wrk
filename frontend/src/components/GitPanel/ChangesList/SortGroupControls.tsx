import { ArrowDownUp, Group } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
} from '@/components/ui/dropdown-menu'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import type { SortBy, GroupBy } from '@/stores/gitPanelStore'

// ───────────────────────────── Sort/Group Options ────────────────────────────

const SORT_OPTIONS: { value: SortBy; label: string }[] = [
  { value: 'path', label: 'Path' },
  { value: 'status', label: 'Status' },
  { value: 'extension', label: 'Extension' },
]

const GROUP_OPTIONS: { value: GroupBy; label: string }[] = [
  { value: 'none', label: 'None' },
  { value: 'status', label: 'Status' },
  { value: 'directory', label: 'Directory' },
]

// ─────────────────────────── Sort/Group Controls ─────────────────────────────

const triggerClass =
  'flex items-center gap-1 px-1.5 py-0.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted transition-colors focus:outline-none focus:ring-1 focus:ring-ring'

/**
 * Compact sort & group mode selectors for the Changes list (D8).
 *
 * Rendered only in flat (list) view. In tree view the tree structure already
 * groups by directory, so these controls are replaced by `TreeExpandControls`
 * (expand-all / collapse-all) — see `ChangesList`.
 */
export function SortGroupControls() {
  const sortBy = useGitPanelStore((s) => s.sortBy)
  const groupBy = useGitPanelStore((s) => s.groupBy)
  const setSortBy = useGitPanelStore((s) => s.setSortBy)
  const setGroupBy = useGitPanelStore((s) => s.setGroupBy)

  const sortLabel = SORT_OPTIONS.find((o) => o.value === sortBy)?.label ?? 'Path'
  const groupLabel = GROUP_OPTIONS.find((o) => o.value === groupBy)?.label ?? 'None'

  return (
    <div className="flex items-center gap-1 px-2 py-1 shrink-0 border-b border-border bg-secondary/20 text-xs">
      {/* Sort selector */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" className={triggerClass} aria-label="Sort by">
            <ArrowDownUp className="size-3" />
            <span>Sort: {sortLabel}</span>
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-36">
          <DropdownMenuRadioGroup
            value={sortBy}
            onValueChange={(v) => setSortBy(v as SortBy)}
          >
            {SORT_OPTIONS.map((o) => (
              <DropdownMenuRadioItem key={o.value} value={o.value}>
                {o.label}
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </DropdownMenuContent>
      </DropdownMenu>

      {/* Group selector */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" className={triggerClass} aria-label="Group by">
            <Group className="size-3" />
            <span>Group: {groupLabel}</span>
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-40">
          <DropdownMenuRadioGroup
            value={groupBy}
            onValueChange={(v) => setGroupBy(v as GroupBy)}
          >
            {GROUP_OPTIONS.map((o) => (
              <DropdownMenuRadioItem key={o.value} value={o.value}>
                {o.label}
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}
