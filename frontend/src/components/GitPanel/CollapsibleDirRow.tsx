import { ChevronDown, ChevronRight, Folder } from 'lucide-react'

interface CollapsibleDirRowProps {
  /** Visible directory label. */
  name: string
  expanded: boolean
  onToggle: () => void
  /** 0-based nesting depth — indents the row by `depth * 12px` (+8px base). */
  depth?: number
  testId?: string
  /** Label font classes; the consumer matches its leaf rows' font. */
  labelClassName?: string
}

/**
 * The collapsible directory row shared by the branch tree (BranchList) and
 * the Changes list tree (TreeRow): chevron + folder, click or Enter/Space
 * toggles. The keyboard twin of the click keeps the row reachable —
 * role="button" + tabIndex, since there is no owning role="tree" and
 * treeitem would be orphaned ARIA.
 */
export function CollapsibleDirRow({
  name,
  expanded,
  onToggle,
  depth = 0,
  testId,
  labelClassName = 'text-sm leading-none',
}: CollapsibleDirRowProps) {
  return (
    <div
      className="flex h-7 cursor-pointer select-none items-center gap-1.5 px-2 hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors"
      style={{ paddingLeft: `${depth * 12 + 8}px` }}
      onClick={onToggle}
      role="button"
      tabIndex={0}
      aria-expanded={expanded}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onToggle()
        }
      }}
      data-testid={testId}
    >
      {expanded ? (
        <ChevronDown className="size-3.5 shrink-0" />
      ) : (
        <ChevronRight className="size-3.5 shrink-0" />
      )}
      <Folder className="size-3.5 shrink-0" />
      <span className={`min-w-0 truncate ${labelClassName}`}>{name}</span>
    </div>
  )
}
