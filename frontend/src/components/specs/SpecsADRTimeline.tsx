// The ADR timeline (ADR-105): decisions/ records newest-first, each with
// its Status classification and supersession link. Clicking a row (or a
// supersession chip) opens the ADR file through the panel's onOpen callback
// (specs-root-relative; the panel resolves it against the absolute root).

import { GitCommitHorizontal } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { SpecADRMeta } from '@/types/models'
import { sortADRsNewestFirst } from './specsPaths'

/** The Status chip: accepted → success tone; superseded → muted, struck;
 *  anything else (proposed, draft, …) → neutral. */
function StatusChip({ status }: { status: string }) {
  if (status === 'superseded') {
    return (
      <span className="shrink-0 rounded bg-secondary px-1 text-xs text-muted-foreground line-through">
        superseded
      </span>
    )
  }
  if (status === 'accepted') {
    return (
      <span className="shrink-0 rounded bg-success/10 px-1 text-xs text-success">
        accepted
      </span>
    )
  }
  return (
    <span className="shrink-0 rounded bg-secondary px-1 text-xs text-muted-foreground">
      {status}
    </span>
  )
}

/** One timeline row: the ADR number chip, the title (or slug fallback), the
 *  status chip, and — when the record names a successor — a supersession
 *  chip that jumps to the successor's row target. */
function ADRRow({
  adr,
  successor,
  successorAmbiguous,
  onOpen,
}: {
  adr: SpecADRMeta
  successor: SpecADRMeta | null
  /** True when the successor's number is carried by more than one file
   *  (DUPLICATE_ADR_NUMBER data): the chip then opens the group's lowest
   *  file path, deterministically, and the tooltip says so. */
  successorAmbiguous: boolean
  onOpen: (relPath: string) => void
}) {
  return (
    <div className="flex items-start gap-1.5">
      <span className="shrink-0 pt-0.5 font-mono text-xs text-muted-foreground">
        {adr.number.padStart(3, '0')}
      </span>
      <button
        type="button"
        data-testid="specs-adr"
        data-number={adr.number}
        data-status={adr.status}
        title={adr.file}
        onClick={() => onOpen(adr.file)}
        className={cn(
          'min-w-0 flex-1 truncate rounded px-1 py-0.5 text-left text-xs transition-colors hover:bg-muted',
          adr.status === 'superseded'
            ? 'text-muted-foreground'
            : 'text-foreground',
        )}
      >
        {adr.title !== undefined && adr.title !== '' ? adr.title : adr.slug}
      </button>
      <StatusChip status={adr.status} />
      {adr.superseded_by !== undefined && adr.superseded_by !== '' && (
        <button
          type="button"
          data-testid="specs-adr-supersession"
          data-number={adr.superseded_by}
          title={
            successor !== null
              ? `Superseded by ADR-${successor.number}: ${successor.title ?? successor.slug}` +
                (successorAmbiguous
                  ? ' (duplicate ADR number in the tree — opening the lowest file)'
                  : '')
              : `Superseded by ADR-${adr.superseded_by} (not in the tree)`
          }
          disabled={successor === null}
          onClick={() => successor !== null && onOpen(successor.file)}
          className="shrink-0 rounded px-1 text-xs text-muted-foreground underline decoration-muted-foreground/40 underline-offset-2 transition-colors hover:bg-muted disabled:no-underline disabled:opacity-60"
        >
          → {adr.superseded_by.padStart(3, '0')}
        </button>
      )}
    </div>
  )
}

/** The timeline section: ADRs sorted newest-first; a superseded record's
 *  successor is resolved against the same list so the chip opens the
 *  successor's file (or renders inert when it is absent). */
export function SpecsADRTimeline({
  adrs,
  onOpen,
}: {
  adrs: SpecADRMeta[]
  onOpen: (relPath: string) => void
}) {
  if (adrs.length === 0) return null
  const sorted = sortADRsNewestFirst(adrs)
  // Duplicate numbers are ANTICIPATED data (DUPLICATE_ADR_NUMBER is a
  // tier-1 finding, ADR-105 D5 — e.g. a decision archived into a
  // decisions/ subdirectory re-parses with the same number): rows key by
  // the file path (unique by construction — it is the parse's map key),
  // and a successor number carried by several files resolves
  // DETERMINISTICALLY to the group's lowest file path, never to whichever
  // copy the wire happened to list last.
  const byNumber = new Map<string, SpecADRMeta>()
  const numberCounts = new Map<string, number>()
  for (const adr of adrs) {
    numberCounts.set(adr.number, (numberCounts.get(adr.number) ?? 0) + 1)
    const existing = byNumber.get(adr.number)
    if (existing === undefined || adr.file < existing.file) {
      byNumber.set(adr.number, adr)
    }
  }
  return (
    <section data-testid="specs-adrs" className="flex flex-col gap-0.5">
      <div className="flex items-center gap-1 px-0.5 text-xs uppercase tracking-wide text-muted-foreground">
        <GitCommitHorizontal className="size-3" />
        Decisions — timeline
      </div>
      {sorted.map((adr) => (
        <ADRRow
          key={adr.file}
          adr={adr}
          successor={adr.superseded_by ? byNumber.get(adr.superseded_by) ?? null : null}
          successorAmbiguous={
            adr.superseded_by !== undefined &&
            adr.superseded_by !== '' &&
            (numberCounts.get(adr.superseded_by) ?? 0) > 1
          }
          onOpen={onOpen}
        />
      ))}
    </section>
  )
}
