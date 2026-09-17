// Anchor list (E1) — the paper card's source anchors as clickable entry points.
//
// Shared by the Overview and the Source section. Clicking an anchor asks the
// workspace to resolve it against the extracted source text; when it cannot be
// located confidently the workspace marks that row, so the list shows an honest
// "not found" instead of jumping somewhere wrong.

import type { PaperAnchor } from '@/api/papers'
import { cn } from '@/lib/utils'

interface PaperAnchorListProps {
  anchors: PaperAnchor[]
  /** Index of the anchor whose last resolution failed (null = none). */
  missedIndex: number | null
  onSelect: (anchor: PaperAnchor, index: number) => void
}

function anchorLabel(anchor: PaperAnchor): string {
  return anchor.label !== '' ? anchor.label : anchor.ref
}

export function PaperAnchorList({ anchors, missedIndex, onSelect }: PaperAnchorListProps) {
  if (anchors.length === 0) {
    return (
      <p data-testid="paper-anchors-empty" className="text-[11px] text-muted-foreground">
        No anchors recorded.
      </p>
    )
  }
  return (
    <ul data-testid="paper-anchors" className="flex flex-wrap gap-1">
      {anchors.map((anchor, index) => (
        <li key={index} className="flex items-center gap-1">
          <button
            type="button"
            data-testid="paper-anchor"
            data-anchor-index={index}
            title={anchor.note !== '' ? anchor.note : anchor.ref}
            onClick={() => onSelect(anchor, index)}
            className={cn(
              'inline-flex max-w-56 items-center gap-1 rounded border border-border bg-background px-1 py-0.5',
              'text-[10px] text-foreground transition-colors hover:bg-muted',
            )}
          >
            <span className="truncate font-medium">{anchorLabel(anchor)}</span>
            {anchor.label !== '' && anchor.ref !== '' && (
              <span className="truncate text-muted-foreground">{anchor.ref}</span>
            )}
          </button>
          {missedIndex === index && (
            <span data-testid="paper-anchor-miss" className="shrink-0 text-[10px] text-destructive">
              not found
            </span>
          )}
        </li>
      ))}
    </ul>
  )
}
