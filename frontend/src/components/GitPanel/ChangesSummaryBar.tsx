import { useMemo } from 'react'
import { cn } from '@/lib/utils'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { countChangeKinds } from '@/lib/gitStatus'
import type { ChangeKind } from '@/lib/gitStatus'

// ───────────────────────────── Display config ──────────────────────────────

/** Dot + bar-segment color per kind — mirrors the row badge colors in
 *  GitFileEntry (M=warning, D/U=destructive, A=success). */
const KIND_COLOR_CLASS: Record<ChangeKind, string> = {
  modified: 'bg-warning',
  deleted: 'bg-destructive',
  new: 'bg-success',
}

/** Human label per kind, matching the request wording (changed/deleted/new). */
const KIND_LABEL: Record<ChangeKind, string> = {
  modified: 'modified',
  deleted: 'deleted',
  new: 'new',
}

const KIND_ORDER: readonly ChangeKind[] = ['modified', 'deleted', 'new']

// ─────────────────────────────── Component ─────────────────────────────────

/**
 * Compact change-summary strip for the Changes tab of the Git panel.
 *
 * Shows the breakdown behind the Changes tab's single total badge as a
 * visual: three colored counters — modified / deleted / new — plus a thin
 * proportional bar whose segments share the counters' colors, so the mix of
 * change kinds is readable at a glance. Counts are per unique path (an `MM`
 * file counts once; see `countChangeKinds`).
 *
 * Renders nothing while there are no entries — the list's "No changes"
 * empty state already covers that case.
 */
export function ChangesSummaryBar() {
  const entries = useGitPanelStore((s) => s.entries)
  const counts = useMemo(() => countChangeKinds(entries), [entries])

  if (counts.total === 0) return null

  return (
    <div
      data-testid="changes-summary"
      className="flex shrink-0 flex-col gap-1 border-b border-border bg-secondary/20 px-2 py-1.5 select-none"
    >
      {/* Counters row — dot + count + label per kind, grand total right */}
      <div className="flex items-center gap-3 text-xs leading-none">
        {KIND_ORDER.map((kind) => (
          <span
            key={kind}
            data-testid={`changes-summary-${kind}`}
            className="inline-flex items-center gap-1 whitespace-nowrap"
            title={`${counts[kind]} ${KIND_LABEL[kind]} file${counts[kind] === 1 ? '' : 's'}`}
          >
            <span className={cn('size-2 shrink-0 rounded-full', KIND_COLOR_CLASS[kind])} />
            <span className="font-medium tabular-nums text-foreground">{counts[kind]}</span>
            <span className="text-muted-foreground">{KIND_LABEL[kind]}</span>
          </span>
        ))}
        <span
          data-testid="changes-summary-total"
          className="ml-auto whitespace-nowrap text-muted-foreground tabular-nums"
        >
          {counts.total} total
        </span>
      </div>
      {/* Proportional mix bar — segment width follows each kind's share; a
          zero count collapses to zero width. Purely decorative: the same
          numbers are already readable as text above. */}
      <div
        data-testid="changes-summary-bar"
        aria-hidden="true"
        className="flex h-1 w-full overflow-hidden rounded-full bg-muted/50"
      >
        {KIND_ORDER.map((kind) => (
          <span
            key={kind}
            data-testid={`changes-summary-bar-${kind}`}
            className={cn('shrink-0', KIND_COLOR_CLASS[kind])}
            style={{ flexGrow: counts[kind] }}
          />
        ))}
      </div>
    </div>
  )
}
