import { Check } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { BranchBase } from '@/types/models'

interface BaseSelectorRowProps {
  base: BranchBase
  selected: boolean
  isCurrent: boolean
  onSelect: (ref: string) => void
}

/**
 * Per-type presentation, single-sourced so a new `BranchBase['type']` variant
 * cannot half-land in parallel records: the badge class, the lowercase badge
 * word and the title-case word for the row title. Unknown types fail soft to
 * the raw type string / muted badge.
 */
const typeMeta: Record<string, { badge: string; label: string; title: string }> = {
  local: { badge: 'text-primary', label: 'branch', title: 'Branch' },
  remote: { badge: 'text-info', label: 'remote', title: 'Remote' },
  tag: { badge: 'text-warning', label: 'tag', title: 'Tag' },
  commit: { badge: 'text-muted-foreground', label: 'commit', title: 'Commit' },
}

/**
 * A single selectable row in BaseSelector. Shows the ref label, an
 * optional commit subject (Detail), a type badge, and a check mark
 * when selected. The current branch is annotated "(current)".
 *
 * Tooltips are native-only (`title` attributes): the row button carries the
 * commit subject when one exists, else a type-prefixed ref name ("Branch
 * main"); the detail span mirrors its own subject. Two native titles never
 * stack — hovering the detail shows the innermost (the detail's own title) —
 * unlike the former Radix-tooltip-inside-titled-button shape, which raised
 * both popups at once.
 */
export function BaseSelectorRow({
  base,
  selected,
  isCurrent,
  onSelect,
}: BaseSelectorRowProps) {
  // `detail` is an empty string (not null) when a base has no subject — the
  // detail span's own render gate is truthiness, so the title must match it
  // (?? would keep the empty string and produce an empty tooltip).
  const rowTitle = base.detail || `${typeMeta[base.type]?.title ?? base.type} ${base.label}`
  return (
    <button
      type="button"
      onClick={() => onSelect(base.ref)}
      title={rowTitle}
      className={cn(
        'flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs transition-colors',
        'focus:outline-none focus:ring-1 focus:ring-ring',
        selected
          ? 'bg-primary/10 text-primary'
          : 'hover:bg-muted text-foreground',
      )}
    >
      <span
        className={cn(
          'shrink-0 text-[10px] uppercase tracking-wide',
          typeMeta[base.type]?.badge ?? 'text-muted-foreground',
        )}
      >
        {typeMeta[base.type]?.label ?? base.type}
      </span>
      <span className="flex min-w-0 flex-1 items-center gap-1">
        <span
        className={cn(
          'shrink-0 max-w-[40%] truncate',
          // Commit SHAs stay monospaced (hex hashes align); branch, remote and
          // tag labels render in the proportional UI font per the mono policy.
          base.type === 'commit' && 'font-mono',
        )}
      >
          {base.label}
          {isCurrent && (
            <span className="ml-1 text-muted-foreground">(current)</span>
          )}
        </span>
        {base.detail && (
          <span
            className="min-w-0 flex-1 truncate text-muted-foreground"
            title={base.detail}
          >
            {base.detail}
          </span>
        )}
      </span>
      {selected && <Check className="size-4 shrink-0" />}
    </button>
  )
}
